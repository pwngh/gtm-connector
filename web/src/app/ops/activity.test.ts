import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement, type ReactElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import {type SealedCard, sealedCards} from '../../api/sealedCards.ts';
import type {CardDetail, ReceiptView} from '../../api/types.ts';
import {recorded, thornfield} from '../deals/card/recorded.ts';
import {activityHref, elapsedOf, inert, inertJson, inOrder, namedDigest, receiptsOf, sealMatches, statusOf} from './activity.ts';

// Activity from recorded cards: the receipts a card names, the table and the
// drawer as they render, and the seal check against the recorded bytes.

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let page: any, client: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  page = await server.ssrLoadModule('/src/app/ops/Activity.tsx');
  client = await server.ssrLoadModule('/src/api/client.ts');
});
after(async () => {
  await server?.close();
});

const hostile = '<img src=x onerror=alert(1)>';
// A receipt as GET /api/v1/receipts/{receipt} sends it.
const receiptOf = (id: string, card: CardDetail, changes: Partial<ReceiptView> = {}): ReceiptView => ({
  id,
  operation_id: `operation-${id}`,
  parent_id: null,
  ordinal: null,
  principal_id: 'principal-1',
  token_id: 'f'.repeat(64),
  tool: id === card.seal_receipt_id ? 'save_brief' : (card.units?.find(unit => unit.receipt_id === id)?.tool ?? 'unknown'),
  scope: null,
  account: card.account,
  connection_generation: null,
  arguments: {account_id: card.account},
  request_id: null,
  brief_id: id === card.seal_receipt_id ? null : card.brief_id,
  status: 'ok',
  http_status: 200,
  row_count: 1,
  rows: id === card.seal_receipt_id ? [`briefs/${card.brief_id}/${card.sha256}`] : ['crm/accounts/acc-003'],
  elapsed_ms: 0.06,
  error: null,
  started_at: '2027-01-15T15:00:00Z',
  finished_at: '2027-01-15T15:00:01Z',
  delivered_at: null,
  ...changes,
});
const inRouter = (element: ReactElement) => renderToStaticMarkup(createElement(MemoryRouter, {initialEntries: ['/ops/activity']}, element));
const decode = (html: string) =>
  html.replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#x27;', "'").replaceAll('&amp;', '&');
const hiddenHelp = /<span [^>]*class="ui-help__panel"[^>]*hidden=""[^>]*><span class="ui-help__text">[^<]*<\/span>(?:<a [^>]*>[^<]*<\/a>)?<\/span>/g;
const text = (html: string) =>
  decode(html.replace(hiddenHelp, '').replace(/<[^>]+>/g, ' '))
    .replace(/\s+/g, ' ')
    .trim();
const ready = (data: ReceiptView) => ({status: 'ready', data});
const readsOf = (card: CardDetail, receipt: (id: string) => unknown = id => ready(receiptOf(id, card))) =>
  new Map(receiptsOf(card).map(named => [`receipt:${named.id}`, receipt(named.id)]));
const tableRows = (html: string) => [...html.matchAll(/<tr data-receipt="([^"]+)"(?: data-seal="true")?>/g)].map(match => match[1]);

test('for every recorded card the table lists its seal receipt, then one row per distinct read receipt', () => {
  assert.ok(recorded.length > 0);
  for (const {file, key, card} of recorded) {
    const reads = new Set((card.units ?? []).map(unit => unit.receipt_id).filter(Boolean));
    const named = receiptsOf(card);
    assert.deepEqual(
      named.map(item => item.id),
      [card.seal_receipt_id, ...reads],
      `${file} ${key}`,
    );
    assert.deepEqual(
      named.map(item => item.seal),
      [true, ...[...reads].map(() => false)],
    );
    const html = inRouter(createElement(page.ReceiptTable, {card, reads: readsOf(card)}));
    assert.deepEqual(tableRows(html), [card.seal_receipt_id, ...reads], `${file} ${key}: the rows`);
    assert.equal((html.match(/data-seal="true"/g) ?? []).length, 1, `${file} ${key}: one seal row`);
  }
});

test('the table: started, tool, status, rows and elapsed, each row a link that opens its receipt', () => {
  const card = thornfield();
  const html = inRouter(createElement(page.ReceiptTable, {card, reads: readsOf(card)}));
  assert.match(html, /<caption>The card’s seal, then each read it rests on<\/caption>/);
  assert.deepEqual(
    [...html.matchAll(/<th scope="col"[^>]*>([^<]+)<\/th>/g)].map(match => match[1]),
    ['Started', 'Tool', 'Status', 'Rows', 'Elapsed'],
  );
  const seal = html.slice(html.indexOf('<tr data-receipt="seal-')).split('</tr>')[0] ?? '';
  assert.equal(text(seal), 'Started 15 Jan 2027, 15:00:00 UTC , open the seal receipt Tool Seal save_brief Status ok 200 Rows 1 Elapsed 0.06 ms');
  assert.ok(decode(seal).includes(`<a class="ac-open" href="${activityHref(card.id, card.seal_receipt_id)}"`));
  assert.match(seal, /<time dateTime="2027-01-15T15:00:00Z">/);
});

test('reads in ordinal order, then by when they started, and a read not in yet keeps its place after them', () => {
  const card = thornfield();
  const [seal, ...reads] = receiptsOf(card);
  assert.ok(seal && reads.length >= 3);
  const [a, b, c] = reads as [(typeof reads)[0], (typeof reads)[0], (typeof reads)[0]];
  const answered = new Map<string, ReceiptView | null>([
    [a.id, receiptOf(a.id, card, {ordinal: null, started_at: '2027-01-15T15:00:00.5Z'})],
    [b.id, receiptOf(b.id, card, {ordinal: 2})],
    [c.id, receiptOf(c.id, card, {ordinal: 1})],
  ]);
  const ordered = inOrder([...reads, seal], row => answered.get(row.id) ?? null).map(row => row.id);
  assert.deepEqual(ordered.slice(0, 4), [seal.id, c.id, b.id, a.id]);
  assert.deepEqual(
    ordered.slice(4),
    reads.slice(3).map(row => row.id),
  );
  // Without ordinals, the start: a fraction of a second is earlier than the next whole second.
  const timed = new Map([
    [a.id, receiptOf(a.id, card, {started_at: '2027-01-15T15:00:01Z'})],
    [b.id, receiptOf(b.id, card, {started_at: '2027-01-15T15:00:00.25Z'})],
  ]);
  assert.deepEqual(
    inOrder([a, b], row => timed.get(row.id) ?? null).map(row => row.id),
    [b.id, a.id],
  );
});

test('a refused receipt says Not permitted in its row with the server code, and the table stays', () => {
  const card = thornfield();
  const [, first] = receiptsOf(card);
  const refused = new client.ApiError({status: 403, kind: 'not_permitted', code: 'not_permitted'});
  const html = inRouter(
    createElement(page.ReceiptTable, {
      card,
      reads: readsOf(card, id => (id === first?.id ? {status: 'failed', error: refused} : ready(receiptOf(id, card)))),
    }),
  );
  assert.equal(tableRows(html).length, receiptsOf(card).length);
  const row = html.slice(html.indexOf(`<tr data-receipt="${first?.id}"`)).split('</tr>')[0] ?? '';
  assert.equal(text(row), 'Started Not read , open the receipt Tool Status Not permitted Server code: not_permitted Rows Elapsed');
  assert.match(row, /data-refusal="not_permitted"/);
});

test('the drawer: every field of the receipt, what a caller supplied as inert text, and the refusal of a refused receipt', () => {
  const card = thornfield();
  const [seal, read] = receiptsOf(card);
  assert.ok(seal && read);
  const supplied = receiptOf(read.id, card, {
    tool: `read_call_notes ${hostile}`,
    arguments: {account_id: `acc-003 ${hostile}`, note: 'right\u202eleft', '10': 'ten', '2': ['two', null]},
    request_id: {kind: 'brief_child', parent_audit_id: 'p\u0000', ordinal: 3},
    rows: [`calls/call_notes/${hostile}`, 'calls/call_notes/note-\u2066x'],
    error: `request_failed ${hostile}`,
  });
  const html = inRouter(createElement(page.DrawerBody, {card, asked: read.id, named: read, read: ready(supplied)}));
  const fields = [...html.matchAll(/data-field="([a-z_]+)"/g)].map(match => match[1]);
  assert.deepEqual(fields, Object.keys(supplied));
  assert.doesNotMatch(html, /<img/);
  for (const char of ['\u202e', '\u2066', '\u0000']) {
    assert.ok(!html.includes(char), `U+${char.codePointAt(0)?.toString(16).padStart(4, '0')} is not on the page`);
  }
  assert.match(decode(html), new RegExp(`<span class="ac-ref">read_call_notes ${hostile.replace(/[()]/g, '\\$&')}</span>`));
  assert.ok(decode(html).includes('"note": "right\\u202eleft"'), 'a format character is written as an escape');
  assert.ok(decode(html).includes('"parent_audit_id": "p\\u0000"'));
  assert.ok(decode(html).includes('calls/call_notes/note-\\u2066x'));
  assert.match(html, /<pre class="ac-json">/);
  assert.doesNotMatch(html, /Check the seal/);
  assert.match(text(html), /Arguments.*Request ID.*Brief/);
  // The seal's drawer has the check.
  const sealed = inRouter(createElement(page.DrawerBody, {card, asked: seal.id, named: seal, read: ready(receiptOf(seal.id, card))}));
  assert.match(sealed, /<button type="button" class="ui-button ui-button--plain">Check the seal<\/button>/);
  assert.match(sealed, /<p class="ac-seal__said" role="status" data-seal-check="idle"><\/p>/);
  // A refused receipt: the refusal, and none of its fields.
  const refused = new client.ApiError({status: 403, kind: 'not_permitted', code: 'not_permitted'});
  const denied = inRouter(createElement(page.DrawerBody, {card, asked: read.id, named: read, read: {status: 'failed', error: refused}}));
  assert.match(text(denied), new RegExp(`^Receipt ${read.id} Not permitted\\..* Server code: not_permitted$`));
  assert.match(denied, /data-refusal="not_permitted"/);
  assert.doesNotMatch(denied, /data-field=/);
  // A receipt the card does not name is not read and nothing stands in its place.
  const missing = inRouter(createElement(page.DrawerBody, {card, asked: `other ${hostile}`, named: null, read: undefined}));
  assert.equal(text(missing), `This card names no receipt other ${hostile}. No other receipt is shown in its place.`);
  // While it is read, it says so.
  assert.equal(text(inRouter(createElement(page.DrawerBody, {card, asked: read.id, named: read, read: {status: 'reading'}}))), 'Reading the receipt');
});

test('inert text: printable ASCII stays, a backslash and everything else become escapes', () => {
  assert.equal(inert('acc-003 <b>'), 'acc-003 <b>');
  assert.equal(inert('a\\b'), 'a\\\\b');
  assert.equal(inert('x\u202ey\u0007z\u00e9😀'), 'x\\u202ey\\u0007z\\u00e9\\ud83d\\ude00');
  assert.equal(inertJson({a: 'é', b: [1, null]}), '{\n  "a": "\\u00e9",\n  "b": [\n    1,\n    null\n  ]\n}');
  assert.equal(inertJson(null), 'null');
  assert.equal(statusOf({status: 'error', http_status: 403}), 'error 403');
  assert.equal(statusOf({status: 'pending', http_status: null}), 'pending');
  assert.equal(elapsedOf({elapsed_ms: 0.005}), '0.01 ms');
  assert.equal(elapsedOf({elapsed_ms: null}), 'Not recorded');
});

test('the digest the seal names is its one row for the card’s brief', () => {
  const card = {brief_id: 'b-1'};
  const digest = 'a'.repeat(64);
  assert.equal(namedDigest({rows: [`briefs/b-1/${digest}`]}, card), digest);
  assert.equal(namedDigest({rows: [`briefs/b-2/${digest}`]}, card), null);
  assert.equal(namedDigest({rows: [`briefs/b-1/${digest}`, 'x']}, card), null);
  assert.equal(namedDigest({rows: [`briefs/b-1/${digest.toUpperCase()}`]}, card), null);
  assert.equal(namedDigest({rows: null}, card), null);
});

// A server that answers one recorded card's bytes, digest header and text.
const serving = (card: SealedCard, bytes = card.bytes) => ({
  reads: 0,
  async cardBytes() {
    this.reads += 1;
    return {bytes, sha256: card.sha256};
  },
  async cardText() {
    return card.text;
  },
});
const briefOf = (card: SealedCard) => (JSON.parse(new TextDecoder().decode(card.bytes)) as {brief_id: string}).brief_id;

test('Check the seal: a match for the true bytes, no match when one byte is altered or the seal names another digest', async () => {
  const recordedCard = sealedCards.find(card => card.file === 'demo' && card.key === 'acc-003') as SealedCard;
  const card = {id: recordedCard.key, brief_id: briefOf(recordedCard)};
  const seal = {rows: [`briefs/${card.brief_id}/${recordedCard.sha256}`]};
  assert.equal(await sealMatches(serving(recordedCard), card, seal), true);
  const altered = new Uint8Array(recordedCard.bytes);
  altered[40] = (altered[40] as number) ^ 1;
  assert.equal(await sealMatches(serving(recordedCard, altered), card, seal), false);
  assert.equal(await sealMatches(serving(recordedCard), card, {rows: [`briefs/${card.brief_id}/${'0'.repeat(64)}`]}), false);
  const unnamed = serving(recordedCard);
  assert.equal(await sealMatches(unnamed, card, {rows: []}), false);
  assert.equal(unnamed.reads, 0, 'a seal that names no digest reads nothing');
  // Every recorded card matches its own seal.
  for (const each of sealedCards) {
    const own = {id: each.key, brief_id: briefOf(each)};
    assert.equal(await sealMatches(serving(each), own, {rows: [`briefs/${own.brief_id}/${each.sha256}`]}), true, `${each.file} ${each.key}`);
  }
});

test('the page with no card in the address asks for a deal in Coverage', () => {
  const html = inRouter(createElement(page.default));
  assert.equal(text(html), 'Activity Choose a deal in Coverage to see what ran for its card. Go to Coverage');
  assert.match(html, /<a class="ac-link" href="\/ops\/coverage"[^>]*>Go to Coverage<\/a>/);
});

test('the card head: its ID, account and publish time, and a link to the card', () => {
  const card = thornfield();
  const html = inRouter(createElement(page.CardHead, {card}));
  assert.equal(text(html), `Card ${card.id} Account acc-003 Published 15 Jan 2027, 15:00:04 UTC Open the card`);
  assert.match(html, new RegExp(`href="/deals/acc-003\\?card=${card.id}"`));
});
