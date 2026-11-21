import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import {ApiError, unitFacts} from '../../../api/client.ts';
import type {Read} from '../../../api/reads.ts';
import type {CardDetail, ReceiptView, UnitView} from '../../../api/types.ts';
import {notShown, onCard, type Sentence} from './card.ts';
import {around, askedUnit, firstLine, placeOf, readFrom, stepFrom, unitOf, unitSearch, unitsOf} from './proof.ts';
import {recorded, thornfield} from './recorded.ts';

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let proof: any;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let deal: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  proof = await server.ssrLoadModule('/src/app/deals/card/Proof.tsx');
  deal = await server.ssrLoadModule('/src/app/deals/card/DealCard.tsx');
});
after(async () => {
  await server?.close();
});

const decode = (html: string) =>
  html.replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#x27;', "'").replaceAll('&amp;', '&');
// The text a reader sees: without the help hints and panels, which are in the document but hidden until opened.
const hiddenHelp = /<span [^>]*class="ui-help__panel"[^>]*hidden=""[^>]*><span class="ui-help__text">[^<]*<\/span>(?:<a [^>]*>[^<]*<\/a>)?<\/span>/g;
const text = (html: string) =>
  decode(html.replace(hiddenHelp, '').replace(/<[^>]+>/g, ' '))
    .replace(/\s+/g, ' ')
    .trim();

// A receipt as GET /api/v1/cards/{card}/units/{n} sends it with a unit.
const receiptOf = (unit: UnitView): ReceiptView => ({
  id: `receipt-${unit.tool}`,
  operation_id: 'operation-1',
  parent_id: null,
  ordinal: 1,
  principal_id: 'principal-1',
  token_id: 'token-1',
  tool: unit.tool,
  scope: 'calls:read',
  account: 'acc-003',
  connection_generation: 1,
  arguments: {},
  request_id: null,
  brief_id: null,
  status: 'ok',
  http_status: 200,
  row_count: 1,
  rows: null,
  elapsed_ms: 1,
  error: null,
  started_at: '2027-01-15T15:00:00Z',
  finished_at: '2027-01-15T15:00:01Z',
  delivered_at: null,
});
const answered = (card: CardDetail, n: number): Read<UnitView> => {
  const unit = unitOf(card, n) as UnitView;
  return {status: 'ready', data: {...unit, receipt: receiptOf(unit)}};
};
const refused = (kind: string, code: string | null = kind): Read<UnitView> => ({
  status: 'failed',
  error: new ApiError({status: 403, kind: kind as never, code}),
  retry: () => undefined,
});
const render = (card: CardDetail, unit: string, read: Read<UnitView>) =>
  renderToStaticMarkup(
    createElement(
      MemoryRouter,
      {initialEntries: [`/deals/${card.account}?unit=${unit}`]},
      createElement(proof.ProofBody, {card, asked: askedUnit(unit), read}),
    ),
  );

test('the address: ?unit= names a unit by a positive whole number, and moving keeps every other parameter', () => {
  assert.deepEqual(askedUnit('5'), {n: 5, written: '5'});
  for (const value of ['0', '-1', '05', '5.0', 'abc', '', '1e3']) {
    assert.deepEqual(askedUnit(value), {n: null, written: value}, value);
  }
  assert.equal(askedUnit(null), null);
  assert.equal(unitSearch('?q=thorn&card=c1', 5), '?q=thorn&card=c1&unit=5');
  assert.equal(unitSearch('?q=thorn&unit=5', 6), '?q=thorn&unit=6');
  assert.equal(unitSearch('?q=thorn&unit=5', null), '?q=thorn');
  assert.equal(unitSearch('?unit=5', null), '');
});

test('unit 5 of the README’s deal: the recorded sentence character for character, who said it, on the card, the unit after it, and the read', () => {
  const card = thornfield();
  const html = render(card, '5', answered(card, 5));
  const sentence = unitFacts(unitOf(card, 5));
  const quote = html.match(/<blockquote class="dc-quote" data-unit="5">([^<]*)<\/blockquote>/);
  assert.equal(decode(quote?.[1] ?? ''), sentence.text);
  assert.equal(sentence.text, 'Our general counsel will not sign until the zero-retention and client-consent rider for AI processing is approved.');
  assert.match(text(html), /approved\. Marian Holt Customer \(by name\) 12 Jan 2027, 15:00:00 UTC call On the card/);
  // Around it: unit 6 is the next unit of the same call note; nothing of that note comes before 5.
  assert.doesNotMatch(html, /data-near="before"/);
  assert.match(html, /data-near="after"><span class="dc-proof__label">After<\/span><a class="dc-proof__step" href="\/deals\/acc-003\?unit=6"/);
  assert.match(text(html), /After \[6\] Please do not put a date on the order form\./);
  assert.match(text(html), /Read 15 Jan 2027, 15:00:00 UTC from calls Open the receipt in Ops$/);
  // Under the read, a link to its receipt whole in Ops; the drawer itself shows no receipt ID, digest or tool name.
  assert.match(
    html,
    /<a class="dc-proof__receipt" href="\/ops\/activity\?card=card-demo-\d+&amp;receipt=receipt-read_call_notes"[^>]*>Open the receipt in Ops<\/a>/,
  );
  assert.doesNotMatch(text(html), /receipt-|read_call_notes|operation-1|token-1|[0-9a-f]{64}/);
  assert.doesNotMatch(html.replace(/ href="[^"]*"/g, ''), /receipt-|read_call_notes|operation-1|token-1|[0-9a-f]{64}/);
});

test('a record unit shows its reference and says its fields are not served yet', () => {
  const card = thornfield();
  const html = render(card, '2', answered(card, 2));
  assert.match(
    text(html),
    /^Record crm\/contacts\/con-003 This server returns a record’s reference, not its fields yet\. On the card Read 15 Jan 2027, 15:00:00 UTC from the CRM Open the receipt in Ops$/,
  );
  assert.doesNotMatch(html, /<blockquote/);
});

test('the states: reading, a number the card lacks, a refusal, and a read refused as not permitted', () => {
  const card = thornfield();
  assert.match(text(render(card, '5', {status: 'loading'})), /^Reading unit 5$/);
  for (const missing of ['12', 'abc']) {
    const html = render(card, missing, answered(card, 5));
    assert.equal(text(html), `This card has no unit ${missing}. No other unit is shown in its place.`);
    assert.doesNotMatch(html, /<blockquote|dc-ref|data-near/);
  }
  const failed = render(card, '5', refused('server', 'internal'));
  assert.match(failed, /data-refusal="server"/);
  assert.match(text(failed), /Server code: internal/);
  assert.doesNotMatch(failed, /<blockquote|Read /);
  // The unit the card holds still shows; the read line alone says Not permitted.
  const denied = render(card, '5', refused('not_permitted'));
  assert.match(denied, /<blockquote class="dc-quote" data-unit="5">Our general counsel/);
  assert.match(denied, /<p class="dc-proof__read" data-read="refused"><span class="dc-proof__label">Read<\/span> <span>Not permitted<\/span><\/p>/);
  assert.doesNotMatch(denied, /data-refusal/);
  // An answer for another unit is not shown as this one.
  const other = render(card, '5', answered(card, 6));
  assert.match(other, /data-refusal="unexpected"/);
  assert.doesNotMatch(other, /<blockquote/);
});

test('a hostile speaker, basis, time and reference render as text', () => {
  const hostile = '<img src=x onerror=alert(1)>';
  const card = structuredClone(thornfield());
  card.units = (card.units ?? []).map(unit => ({
    ...unit,
    record_ref: `${unit.record_ref}${hostile}`,
    body: {...(unit.body as object), speaker: `Marian ${hostile}`, basis: `b${hostile}`, at: `t${hostile}`, source: `s${hostile}`},
  }));
  for (const n of ['5', '1']) {
    const html = render(card, n, answered(card, Number(n)));
    assert.doesNotMatch(html, /<img/);
    assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
  }
  assert.match(text(render(card, '5', answered(card, 5))), /Marian <img src=x onerror=alert\(1\)>/);
});

test('every unit of every golden card: the drawer shows that unit alone, whole or listed as cut, in the card page’s words, with its neighbours of the same record', () => {
  let units = 0;
  for (const {file, key, card} of recorded) {
    const all = unitsOf(card);
    for (const unit of all) {
      units += 1;
      const at = `${file} ${key} unit ${unit.n}`;
      const html = render(card, String(unit.n), answered(card, unit.n));
      const facts = unitFacts(unit) as Sentence;
      const quotes = [...html.matchAll(/<blockquote class="dc-quote" data-unit="(\d+)">([^<]*)<\/blockquote>/g)];
      if (unit.kind === 'sentence' && !facts.partial && facts.text !== null) {
        assert.equal(quotes.length, 1, at);
        assert.equal(decode(quotes[0]?.[2] ?? ''), facts.text, at);
      } else {
        assert.equal(quotes.length, 0, `${at}: a cut sentence or a record is never a quotation`);
      }
      const place = unit.kind === 'sentence' && !onCard(facts) ? notShown([facts])[0]?.label : 'On the card';
      assert.equal(placeOf(unit), place, at);
      assert.match(html, new RegExp(`<p class="dc-proof__place">${place?.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}</p>`), at);
      // The neighbours share the unit's record and are the nearest either side in the card's order.
      const same = all.filter(item => item.record_ref === unit.record_ref).map(item => item.n);
      const {before, after} = around(card, unit.n);
      assert.equal(before?.n ?? null, same[same.indexOf(unit.n) - 1] ?? null, at);
      assert.equal(after?.n ?? null, same[same.indexOf(unit.n) + 1] ?? null, at);
      for (const near of [before, after]) {
        if (near) {
          assert.ok(html.includes(`unit=${near.n}"`), `${at}: links unit ${near.n}`);
          assert.ok(decode(html).includes(firstLine(near)), `${at}: shows unit ${near.n}'s first line`);
        }
      }
      // Left and Right walk the card's order.
      const index = all.indexOf(unit);
      assert.equal(stepFrom(card, unit.n, -1), all[index - 1]?.n ?? null, at);
      assert.equal(stepFrom(card, unit.n, 1), all[index + 1]?.n ?? null, at);
    }
  }
  assert.ok(units > 1500, `${units} units`);
});

test('the read names its source in words', () => {
  assert.deepEqual(['calls', 'slack', 'crm', 'other', null].map(readFrom), ['calls', 'Slack', 'the CRM', 'other', 'a source not recorded']);
});

test('until a drawer has been opened in this browser, one line above the ledger says what a number does', () => {
  const card = thornfield();
  const html = renderToStaticMarkup(
    createElement(MemoryRouter, {initialEntries: ['/deals/acc-003']}, createElement(deal.CardBody, {account: 'acc-003', card, listed: [card], query: ''})),
  );
  const hint = html.indexOf('<p class="dc-proof-hint">Select a number to see its source.</p>');
  assert.ok(hint >= 0 && hint < html.indexOf('class="dc-ledger"'));
  assert.doesNotMatch(html, /role="dialog"/);
});
