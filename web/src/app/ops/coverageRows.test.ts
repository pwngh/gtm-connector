import assert from 'node:assert/strict';
import test from 'node:test';
import {ApiError} from '../../api/client.ts';
import type {CardView} from '../../api/types.ts';
import {type CoverageRow, filterOf, filterRows, groupOf, nextSort, type Sort, sortRows, summarize} from './coverageRows.ts';

const card = (id: string, published_at: string): CardView => ({
  id,
  account: 'a',
  brief_id: 'b',
  kind: 'go',
  schema_version: 3,
  rules_version: '3',
  sha256: 'x',
  size: 1,
  seal_receipt_id: 's',
  published_at,
});
const row = (account: string, name: string, list: CoverageRow['list']): CoverageRow => ({account, name, list});
const rows: CoverageRow[] = [
  row('acc-1', 'Beta', {status: 'ready', cards: [card('c-3', '2027-01-15T15:00:04Z'), card('c-2', '2027-01-10T09:00:00Z')]}),
  row('acc-2', 'Alpha', {status: 'ready', cards: []}),
  row('acc-3', 'Gamma', {status: 'ready', cards: [card('c-1', '2027-01-12T00:00:00Z')]}),
  row('acc-4', 'Delta', {status: 'failed', error: new ApiError({status: 403, kind: 'not_permitted', code: 'not_permitted'})}),
  row('acc-5', 'Epsilon', {status: 'reading'}),
  row('acc-6', 'Alpha', {status: 'ready', cards: []}),
];

test('every count in the summary equals the number of rows its filter shows', () => {
  const summary = summarize(rows);
  assert.deepEqual(summary, {all: 6, card: 2, none: 2, unread: 1, reading: 1});
  for (const filter of ['all', 'card', 'none', 'unread'] as const) {
    assert.equal(filterRows(rows, filter).length, summary[filter], filter);
  }
  assert.deepEqual(
    filterRows(rows, 'none').map(item => item.account),
    ['acc-2', 'acc-6'],
  );
  assert.equal(groupOf(rows[4] as CoverageRow), 'reading');
});

test('the filter comes from the address, and anything unknown is every row', () => {
  assert.equal(filterOf('card'), 'card');
  assert.equal(filterOf('none'), 'none');
  assert.equal(filterOf('unread'), 'unread');
  for (const value of [null, '', 'all', 'NONE', 'reading', 'card ']) {
    assert.equal(filterOf(value), 'all');
  }
});

const order = (sort: Sort) => sortRows(rows, sort).map(item => item.account);

test('sorting by deal, newest card or cards changes the order, and rows not read stay last', () => {
  assert.deepEqual(order({key: 'deal', direction: 'ascending'}), ['acc-2', 'acc-6', 'acc-1', 'acc-4', 'acc-5', 'acc-3']);
  assert.deepEqual(order({key: 'deal', direction: 'descending'}), ['acc-3', 'acc-5', 'acc-4', 'acc-1', 'acc-6', 'acc-2']);
  // No card is the oldest; equal values fall back to the deal name, then its ID.
  assert.deepEqual(order({key: 'newest', direction: 'ascending'}), ['acc-2', 'acc-6', 'acc-3', 'acc-1', 'acc-4', 'acc-5']);
  assert.deepEqual(order({key: 'newest', direction: 'descending'}), ['acc-1', 'acc-3', 'acc-2', 'acc-6', 'acc-4', 'acc-5']);
  assert.deepEqual(order({key: 'cards', direction: 'descending'}), ['acc-1', 'acc-3', 'acc-2', 'acc-6', 'acc-4', 'acc-5']);
  assert.deepEqual(order({key: 'cards', direction: 'ascending'}), ['acc-2', 'acc-6', 'acc-3', 'acc-1', 'acc-4', 'acc-5']);
  // Sorting never changes the rows it was given.
  assert.deepEqual(
    rows.map(item => item.account),
    ['acc-1', 'acc-2', 'acc-3', 'acc-4', 'acc-5', 'acc-6'],
  );
});

test('pressing the sorted column turns it round; another column starts ascending', () => {
  assert.deepEqual(nextSort({key: 'deal', direction: 'ascending'}, 'deal'), {key: 'deal', direction: 'descending'});
  assert.deepEqual(nextSort({key: 'deal', direction: 'descending'}, 'deal'), {key: 'deal', direction: 'ascending'});
  assert.deepEqual(nextSort({key: 'deal', direction: 'descending'}, 'cards'), {key: 'cards', direction: 'ascending'});
});
