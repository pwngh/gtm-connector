import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import {ApiError} from '../../api/client.ts';
import type {AccountView, CardView} from '../../api/types.ts';
import {byName, cardReadLimit, dealAt, dealHref, matching, readInTurn, rowFrom} from './deals.ts';

// The demo book's accounts, as GET /api/v1/accounts lists them: by ID, with the recorded names.
const book = JSON.parse(readFileSync(new URL('../../../../testdata/oracle/cards/demo_book.json', import.meta.url), 'utf8')) as {
  crm: {accounts: {columns: string[]; rows: unknown[][]}};
};
const accounts: AccountView[] = book.crm.accounts.rows
  .map(row => ({account: String(row[0]), name: String(row[1])}))
  .sort((a, b) => (a.account < b.account ? -1 : 1));

test('the demo book lists 30 deals, ordered by name and not by ID', () => {
  assert.equal(accounts.length, 30);
  const ordered = byName(accounts);
  assert.equal(ordered.length, 30);
  const names = ordered.map(deal => deal.name);
  assert.deepEqual(
    names,
    [...names].sort((a, b) => a.localeCompare(b, 'en', {sensitivity: 'base', numeric: true})),
  );
  assert.notDeepEqual(
    ordered.map(deal => deal.account),
    accounts.map(deal => deal.account),
  );
  assert.ok(ordered.some(deal => deal.account === 'acc-003' && deal.name === 'Thornfield Wren LLP'));
  // The list it was given is not changed.
  assert.equal(accounts[0]?.account, 'acc-001');
});

test('two deals with one name keep a fixed order by ID', () => {
  const same = byName([
    {account: 'acc-9', name: 'Same'},
    {account: 'acc-10', name: 'same'},
    {account: 'acc-1', name: 'Alpha'},
  ]);
  assert.deepEqual(
    same.map(deal => deal.account),
    ['acc-1', 'acc-9', 'acc-10'],
  );
});

test('the filter narrows by name, ignoring case and outer space, and an empty filter keeps all', () => {
  const ordered = byName(accounts);
  assert.equal(matching(ordered, '').length, 30);
  assert.equal(matching(ordered, '   ').length, 30);
  assert.deepEqual(
    matching(ordered, '  thornfield ').map(deal => deal.account),
    ['acc-003'],
  );
  // The ID is not the name.
  assert.deepEqual(matching(ordered, 'acc-003'), []);
  assert.deepEqual(matching(ordered, 'no such deal'), []);
});

test('a deal address holds one encoded segment, and only that is a deal', () => {
  assert.equal(dealHref('acc-003'), '/deals/acc-003');
  assert.equal(dealHref('acc-003', 'wren'), '/deals/acc-003?q=wren');
  assert.equal(dealHref('acc-003', '   '), '/deals/acc-003');
  assert.equal(dealHref('a/b?c#d'), '/deals/a%2Fb%3Fc%23d');
  assert.equal(dealAt('/deals/a%2Fb%3Fc%23d'), 'a/b?c#d');
  assert.equal(dealAt('/deals/acc-003'), 'acc-003');
  assert.equal(dealAt('/deals'), null);
  assert.equal(dealAt('/deals/'), null);
  assert.equal(dealAt('/deals/acc-003/more'), null);
  assert.equal(dealAt('/ops/evidence'), null);
  // A segment that does not decode is passed on as written.
  assert.equal(dealAt('/deals/%E0%A4%A'), '%E0%A4%A');
});

const card = (id: string, published: string): CardView => ({
  id,
  account: 'acc-003',
  brief_id: `brief-${id}`,
  kind: 'go',
  schema_version: 3,
  rules_version: '3',
  sha256: '0'.repeat(64),
  size: 1,
  seal_receipt_id: `seal-${id}`,
  published_at: published,
});

test('a row shows the first card the server lists, which is the newest, or no card, or the refusal', () => {
  const newest = card('card-2', '2027-01-16T09:00:00Z');
  const read = rowFrom({status: 'ready', data: {account: 'acc-003', cards: [newest, card('card-1', '2027-01-15T15:00:04Z')]}});
  assert.deepEqual(read, {status: 'ready', newest});
  assert.deepEqual(rowFrom({status: 'ready', data: {account: 'acc-003', cards: []}}), {status: 'ready', newest: null});
  const refused = new ApiError({status: 403, kind: 'not_permitted', code: 'not_permitted'});
  assert.deepEqual(rowFrom({status: 'failed', error: refused}), {status: 'failed', error: refused});
  const odd = rowFrom({status: 'ready', data: {account: 'acc-003'} as never});
  assert.equal(odd.status === 'failed' && odd.error.kind, 'unexpected');
});

// A request that answers when told to, and records what is in flight.
function requests() {
  const inFlight = new Map<string, () => void>();
  const started: string[] = [];
  const released: string[] = [];
  let most = 0;
  const start = (name: string, done: () => void) => {
    started.push(name);
    inFlight.set(name, done);
    most = Math.max(most, inFlight.size);
    return () => {
      inFlight.delete(name);
      released.push(name);
    };
  };
  const answer = (name: string) => {
    const done = inFlight.get(name);
    assert.ok(done, `${name} is in flight`);
    inFlight.delete(name);
    done();
  };
  return {start, answer, inFlight, started, released, most: () => most};
}

test('card lists are read in order with at most four in flight, each answer starting the next', () => {
  const names = accounts.map(deal => deal.account);
  const run = requests();
  readInTurn(names, cardReadLimit, run.start);
  assert.deepEqual(run.started, names.slice(0, 4));
  // One slow answer holds only its own place.
  run.answer('acc-002');
  assert.deepEqual([...run.inFlight.keys()], ['acc-001', 'acc-003', 'acc-004', 'acc-005']);
  while (run.inFlight.size) {
    run.answer([...run.inFlight.keys()].at(-1) as string);
  }
  assert.deepEqual(run.started, names);
  assert.equal(run.most(), 4);
  assert.deepEqual(run.released, []);
});

test('stopping releases the reads in flight and starts no more', () => {
  const run = requests();
  const stop = readInTurn(
    accounts.map(deal => deal.account),
    cardReadLimit,
    run.start,
  );
  const held = new Map(run.inFlight);
  stop();
  assert.deepEqual(run.released, ['acc-001', 'acc-002', 'acc-003', 'acc-004']);
  // An answer that arrives after the stop starts nothing.
  held.get('acc-001')?.();
  assert.equal(run.started.length, 4);
});
