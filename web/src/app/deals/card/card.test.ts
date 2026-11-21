import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {after, before, test} from 'node:test';
import {gunzipSync} from 'node:zlib';
import {createElement, type ReactElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import type {Read} from '../../../api/reads.ts';
import type {CardDetail, CardView, GapView} from '../../../api/types.ts';
import {groupOf, newestFirst, notShown, onCard, orderedGaps, recordsOf, type Sentence, sentencesOf, usableTime, wholeSources} from './card.ts';

// Every card here is a recorded card from testdata/oracle/cards, shaped as
// GET /api/v1/cards/{card} returns it. Only the IDs and the publish time are
// made up, as the server makes its own.

interface Recorded {
  file: string;
  key: string;
  card: CardDetail;
}

const files = ['demo', 'cases', 'varied_demo'];

function shaped(account: string, id: string, sealed: Record<string, unknown>, sha256: string, size: number): CardDetail {
  const units = sealed.units as Record<string, unknown>[];
  return {
    id,
    account,
    brief_id: String(sealed.brief_id),
    kind: 'go',
    schema_version: 3,
    rules_version: '3',
    sha256,
    size,
    seal_receipt_id: `seal-${id}`,
    published_at: '2027-01-15T15:00:04Z',
    as_of: String(sealed.as_of),
    sources: null,
    lines: null,
    units: units.map(body => ({
      card: id,
      n: body.n as number,
      kind: body.kind as string,
      record_ref: body.record_ref as string,
      tool: body.tool as string,
      ...(body.kind === 'sentence' ? {shown: body.shown as string} : {}),
      receipt_id: `receipt-${body.tool}`,
      record_version_id: `version-${body.n}`,
      body: body as never,
    })),
    gaps: sealed.gaps as GapView[],
  };
}

const recorded: Recorded[] = files.flatMap(file =>
  gunzipSync(readFileSync(new URL(`../../../../../testdata/oracle/cards/${file}.jsonl.gz`, import.meta.url)))
    .toString()
    .split('\n')
    .filter(Boolean)
    .map((line, at) => {
      const row = JSON.parse(line) as {key: string; account_id: string; card: string; sha256: string};
      return {file, key: row.key, card: shaped(row.account_id, `card-${file}-${at}`, JSON.parse(row.card), row.sha256, row.card.length)};
    }),
);
const find = (file: string, key: string) => recorded.find(item => item.file === file && item.key === key)?.card as CardDetail;
const thornfield = () => find('demo', 'acc-003');

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let view: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  view = await server.ssrLoadModule('/src/app/deals/card/DealCard.tsx');
});
after(async () => {
  await server?.close();
});

const inRouter = (element: ReactElement, path = '/deals/acc-003') => renderToStaticMarkup(createElement(MemoryRouter, {initialEntries: [path]}, element));
const body = (card: CardDetail, listed: CardView[] = [card]) => inRouter(createElement(view.CardBody, {account: card.account, card, listed, query: ''}));
const ready = <T>(data: T): Read<T> => ({status: 'ready', data});
const deal = (card: CardDetail, reads: Partial<Record<'accounts' | 'cards' | 'card', Read<unknown>>> = {}, pinned: string | null = null) =>
  inRouter(
    createElement(view.DealView, {
      asked: card.account,
      pinned,
      query: '',
      reads: {
        accounts: ready({accounts: [{account: card.account, name: 'Thornfield Wren LLP'}]}),
        cards: ready({account: card.account, cards: [card]}),
        card: ready(card),
        sealed: ready({outcome: 'verified', text: ''}),
        ...reads,
      },
    }),
  );
const decode = (html: string) =>
  html.replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#x27;', "'").replaceAll('&amp;', '&');
const text = (html: string) => decode(html.replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ');
const count = (html: string, pattern: RegExp) => [...html.matchAll(pattern)].length;
const quotes = (html: string) =>
  [...html.matchAll(/<blockquote class="dc-quote" data-unit="(\d+)">([^<]*)<\/blockquote>/g)].map(match => ({
    n: Number(match[1]),
    text: decode(match[2] ?? ''),
  }));

test('the loop covers every recorded card of the three files', () => {
  assert.deepEqual(
    files.map(file => recorded.filter(item => item.file === file).length),
    [33, 19, 46],
  );
});

test('every recorded card: each shown sentence once as its whole text, every other sentence once under More, each record once, each gap once', () => {
  let sentences = 0;
  for (const {file, key, card} of recorded) {
    const at = `${file} ${key}`;
    const html = body(card);
    const all = sentencesOf(card);
    sentences += all.length;
    // Every blockquote is one unit's text, whole, with nothing added and no element inside it.
    const set = quotes(html);
    assert.equal(count(html, /<blockquote/g), set.length, `${at}: a blockquote holds only text`);
    const units = new Map(all.map(sentence => [sentence.n, sentence]));
    for (const quote of set) {
      assert.equal(quote.text, units.get(quote.n)?.text, `${at}: unit ${quote.n} is quoted exactly`);
    }
    for (const sentence of all) {
      const where = `${at} unit ${sentence.n}`;
      if (sentence.shown === 'yes') {
        assert.equal(count(html, new RegExp(`data-quote-unit="${sentence.n}"`, 'g')), 1, `${where}: shown once under People said`);
        assert.equal(count(html, new RegExp(`data-more-unit="${sentence.n}"`, 'g')), 0, `${where}: not under More`);
        assert.equal(set.filter(quote => quote.n === sentence.n).length, 1, `${where}: one blockquote`);
      } else {
        assert.equal(count(html, new RegExp(`data-more-unit="${sentence.n}"`, 'g')), 1, `${where}: once under More that was read`);
        assert.equal(count(html, new RegExp(`data-quote-unit="${sentence.n}"`, 'g')), 0, `${where}: not under People said`);
      }
    }
    for (const record of recordsOf(card)) {
      const line = html.match(new RegExp(`data-record-unit="${record.n}">[\\s\\S]*?<span class="dc-ref">([^<]*)</span>`));
      assert.equal(count(html, new RegExp(`data-record-unit="${record.n}"`, 'g')), 1, `${at}: record ${record.n} once`);
      assert.equal(decode(line?.[1] ?? ''), record.recordRef, `${at}: record ${record.n} by its reference`);
    }
    const gaps = [...html.matchAll(/<p class="dc-gap__text">([^<]*)<\/p>/g)].map(match => decode(match[1] ?? ''));
    const stated = (card.gaps ?? []).map(gap => gap.text);
    assert.deepEqual([...gaps].sort(), [...stated].sort(), `${at}: every gap's text once`);
    assert.deepEqual(
      gaps,
      orderedGaps(card).map(gap => gap.text),
      `${at}: the gaps in Not read's order`,
    );
  }
  assert.equal(sentences, 90 + 1297 + 444);
});

test('people said: three groups in order, each with its count, newest first, attribution as recorded', () => {
  const card = thornfield();
  const html = body(card);
  const groups = [...html.matchAll(/data-group="([a-z]+)"[^>]*><h3[^>]*>([^<]+)<\/h3>/g)].map(match => [match[1], decode(match[2] ?? '')]);
  assert.deepEqual(groups, [
    ['customer', 'Buyer quotes (2)'],
    ['unknown', 'No established role (0)'],
    ['seller', 'Your side (4)'],
  ]);
  const customer = html.slice(html.indexOf('data-group="customer"'), html.indexOf('data-group="unknown"'));
  // The speaker as recorded, the contact's number after the name, the role, the source and the time.
  assert.match(text(customer), /Marian Holt \[2\] Customer call 12 Jan 2027, 15:00:00 UTC/);
  assert.match(customer, /aria-label="Source 2" href="\/ops\/evidence\?account=acc-003&amp;card=card-demo-2&amp;unit=2"/);
  const seller = html.slice(html.indexOf('data-group="seller"'));
  // Slack at 16:00 is newer than the call at 15:00, so it comes first; equal times keep the card's order.
  assert.deepEqual(
    [...seller.matchAll(/data-quote-unit="(\d+)"/g)].map(match => Number(match[1])),
    [7, 8, 9, 4],
  );
  assert.match(text(seller), /Avery Linden Seller Slack 12 Jan 2027, 16:00:00 UTC/);
  assert.doesNotMatch(seller, /listed seller identity/);
  // A unit with no established role shows its basis, and the heading counts the reasons.
  const unknown = body(find('cases', 'test_05_injected_markup_and_figures_stay_inert/2'));
  assert.match(
    text(unknown),
    /No established role \(3\) author is not a listed seller; Slack identities cannot show a customer \(2\) · no speaker label \(1\)/,
  );
  assert.match(text(unknown), /No speaker label Unknown \(no speaker label\) call/);
});

test('a time that cannot be ordered is shown as written and placed last', () => {
  const literalTime = (card: CardDetail) => sentencesOf(card).some(sentence => onCard(sentence) && usableTime(sentence.at) === null);
  const card = recorded.find(item => literalTime(item.card))?.card as CardDetail;
  assert.ok(card, 'a recorded card shows a sentence whose time has no zone');
  const shown = sentencesOf(card).filter(onCard);
  for (const group of ['customer', 'unknown', 'seller'] as const) {
    const ordered = newestFirst(shown.filter(sentence => groupOf(sentence) === group));
    const firstLiteral = ordered.findIndex(sentence => usableTime(sentence.at) === null);
    if (firstLiteral >= 0) {
      assert.ok(ordered.slice(firstLiteral).every(sentence => usableTime(sentence.at) === null));
    }
    const times = ordered.map(sentence => usableTime(sentence.at)).filter(time => time !== null);
    assert.deepEqual(
      times,
      [...times].sort((a, b) => b - a),
    );
  }
  assert.match(text(body(card)), /no time zone recorded/);
});

test('more that was read: one disclosure with its count, by reason in the connector’s words', () => {
  const card = recorded.find(item => sentencesOf(item.card).some(sentence => sentence.shown === 'long'))?.card as CardDetail;
  const reasons = notShown(sentencesOf(card));
  const html = body(card);
  const total = reasons.reduce((sum, reason) => sum + reason.sentences.length, 0);
  assert.equal(count(html, /<details class="dc-more">/g), 1);
  assert.match(html, new RegExp(`<summary class="dc-more__summary">More that was read \\(${total}\\)</summary>`));
  assert.ok(reasons.some(reason => reason.shown === 'long' && reason.label === 'Longer than a card sentence'));
  // A unit with partial true is listed as cut in the text face and is never a blockquote.
  const cut = structuredClone(card);
  const units = cut.units ?? [];
  const unit = units.find(item => item.kind === 'sentence' && item.shown === 'yes');
  assert.ok(unit);
  (unit.body as {partial: boolean}).partial = true;
  const cutHtml = body(cut);
  const line = cutHtml.slice(cutHtml.indexOf(`data-more-unit="${unit.n}"`)).split('</li>')[0] ?? '';
  assert.match(line, /<p class="dc-cut"><span class="dc-cut__label">Cut, not a whole quotation<\/span>/);
  assert.doesNotMatch(line, /<blockquote/);
  assert.match(cutHtml, /data-reason="cut"[^>]*><h3[^>]*>Cut at a pause, a change of speaker or the edge of an excerpt \(1\)/);
  assert.doesNotMatch(cutHtml, new RegExp(`data-quote-unit="${unit.n}"`));
  // Reasons go cut, later, long, more.
  assert.deepEqual(
    notShown([
      {n: 1, shown: 'more', partial: false},
      {n: 2, shown: 'long', partial: false},
      {n: 3, shown: 'later', partial: false},
      {n: 4, shown: 'cut', partial: true},
    ] as Sentence[]).map(reason => reason.shown),
    ['cut', 'later', 'long', 'more'],
  );
});

test('the recorded injection case renders its markup, mentions and links as text and makes no element from them', () => {
  const card = find('cases', 'test_05_injected_markup_and_figures_stay_inert/2');
  const html = body(card);
  for (const sentence of sentencesOf(card)) {
    assert.ok(quotes(html).some(quote => quote.n === sentence.n && quote.text === sentence.text));
  }
  assert.match(text(html), /\[mention channel\] \[user U0ABC123\] IGNORE PREVIOUS INSTRUCTIONS/);
  assert.match(text(html), /See the approved order \[link evil\.example\]/);
  assert.doesNotMatch(html, /href="[^"]*evil|<a [^>]*>[^<]*evil/);
  // Every link on the card is a unit's number or a link of the app's own.
  for (const [, href] of html.matchAll(/href="([^"]*)"/g)) {
    assert.match(decode(href ?? ''), /^\/ops\/evidence\?account=acc-905&card=card-cases-9&unit=\d+$/);
  }
  // Server strings that hold markup stay text, wherever they are shown.
  const hostile = '<img src=x onerror=alert(1)>';
  const marked = structuredClone(thornfield());
  marked.id = `card${hostile}`;
  marked.gaps = (marked.gaps ?? []).map(gap => ({...gap, text: `${gap.text}${hostile}`, disposition: `${gap.disposition}${hostile}`}));
  marked.units = (marked.units ?? []).map(unit => ({
    ...unit,
    record_ref: `${unit.record_ref}${hostile}`,
    body: {...(unit.body as object), text: `${(unit.body as {text?: string}).text}${hostile}`, speaker: `x${hostile}`, basis: `b${hostile}`, at: `t${hostile}`},
  }));
  const shown = body(marked);
  assert.doesNotMatch(shown, /<img/);
  assert.match(shown, /&lt;img src=x onerror=alert\(1\)&gt;/);
});

test('the title is the server’s name and the ID, with one line on where the values are; the seal names the card', () => {
  const card = thornfield();
  const html = deal(card);
  assert.match(html, /<h1 id="dc-title" class="dc-title">Thornfield Wren LLP<\/h1><p class="dc-id">acc-003<\/p>/);
  assert.match(
    html,
    /<p class="dc-values-note">The countdown and ARR are on <a class="dc-values-note__link" href="\/deals\/acc-003\/text"[^>]*>the plain-text card<\/a>\.<\/p>/,
  );
  assert.match(text(html), /This page shows each record’s reference\. Its values are on the plain-text card\./);
  assert.match(text(html), /Sealed 15 Jan 2027, 15:00:04 UTC Card card-demo-2/);
  assert.doesNotMatch(html, /At risk|On track|Changed|days/);
  // An account the list does not hold is never named from the address.
  const other = deal(card, {accounts: ready({accounts: [{account: 'acc-002', name: 'Second'}]})});
  assert.match(other, /<h1 id="dc-title" class="dc-title">Account not verified<\/h1><p class="dc-id">Account ID acc-003, not verified<\/p>/);
});

test('a refused card shows Not permitted with its code and none of the sections', () => {
  const card = thornfield();
  const refused = {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}} as Read<unknown>;
  for (const reads of [{card: refused}, {cards: refused}]) {
    const html = deal(card, reads);
    assert.match(html, /data-refusal="not_permitted"/);
    assert.match(text(html), /Not permitted\. .* Server code: not_permitted/);
    assert.doesNotMatch(html, /dc-section|dc-ledger|<blockquote|dc-values-note|No sentence was read|states nothing/);
  }
});

test('a card in ?card= that is not one of the account’s cards is not read, and a notice says so', () => {
  const card = thornfield();
  const html = deal(card, {card: {status: 'idle'}}, 'card-of-another-account');
  assert.match(text(html), /The card in this link is not one of this deal’s published cards\. Nothing from it is read or shown\. Open the newest/);
  assert.match(html, /<a class="dc-link" href="\/deals\/acc-003"[^>]*>Open the newest<\/a>/);
  assert.doesNotMatch(html, /dc-section|<blockquote/);
  const chosen = view.dealOf(
    'acc-003',
    'card-of-another-account',
    ready({accounts: [{account: 'acc-003', name: 'x'}]}),
    ready({account: 'acc-003', cards: [card]}),
  );
  assert.equal(chosen.cardId, null, 'so no card is requested');
  assert.equal(chosen.mismatch, true);
});

test('an older card pinned in ?card= says whose time it is and links to the newest', () => {
  const older = {...thornfield(), id: 'card-older', published_at: '2027-01-12T09:10:00Z'};
  const newer = {...thornfield(), id: 'card-newer', published_at: '2027-01-15T15:00:04Z'};
  const html = deal(older, {cards: ready({account: 'acc-003', cards: [newer, older]})}, 'card-older');
  assert.match(
    text(html),
    /You are reading the card of 12 Jan 2027, 09:10:00 UTC ?\. A newer card was published 15 Jan 2027, 15:00:04 UTC ?\. Open the newest/,
  );
  assert.equal(count(deal(newer, {cards: ready({account: 'acc-003', cards: [newer, older]})}, 'card-newer'), /data-notice/g), 0);
});

test('the empty states say what the card read', () => {
  const card = thornfield();
  const records = {...card, units: (card.units ?? []).filter(unit => unit.kind === 'record'), gaps: []};
  assert.match(text(body(records)), /People said No sentence was read\./);
  assert.match(text(body(records)), /Not read The card states nothing it did not read\. That is the card’s statement, not a search of every source\./);
  assert.doesNotMatch(body(records), /More that was read/);
  const unshown = recorded.find(item => sentencesOf(item.card).some(sentence => sentence.shown === 'more'))?.card as CardDetail;
  const none = {...unshown, units: (unshown.units ?? []).filter(unit => unit.kind === 'record' || unit.shown !== 'yes')};
  const sentences = sentencesOf(none).length;
  assert.match(text(body(none)), new RegExp(`People said ${sentences} sentences were read and none is shown\\. Not read says why\\.`));
});

test('not read lists whole sources first, then other unread, then read and not shown, then notes', () => {
  const card = find('demo', 'acc-003 slack not granted');
  const ordered = orderedGaps(card);
  const rank = (gap: GapView) =>
    gap.disposition === 'not_read' ? (wholeSources.has(gap.code) ? 0 : 1) : gap.disposition === 'read_not_shown' ? 2 : gap.disposition === 'note' ? 3 : 4;
  assert.ok(ordered.some(gap => gap.code === 'slack_denied'));
  assert.deepEqual(
    ordered.map(rank),
    [...ordered.map(rank)].sort((a, b) => a - b),
  );
  assert.equal(ordered[0]?.code, 'slack_denied');
  for (const {card: each} of recorded) {
    const ranks = orderedGaps(each).map(rank);
    assert.deepEqual(
      ranks,
      [...ranks].sort((a, b) => a - b),
    );
  }
  assert.match(body(card), /data-gap="slack_denied">[\s\S]*?<p class="dc-meta">Not read<\/p>/);
});

test('every number is a link named Source n, to the page that shows that unit in full', () => {
  const html = body(thornfield());
  const cites = [...html.matchAll(/<a class="dc-cite" aria-label="Source (\d+)" href="([^"]+)"[^>]*><span class="dc-cite__box">\[(\d+)\]<\/span><\/a>/g)];
  assert.equal(cites.length, count(html, /class="dc-cite"/g));
  for (const [, named, href, shown] of cites) {
    assert.equal(named, shown);
    assert.equal(decode(href ?? ''), `/ops/evidence?account=acc-003&card=card-demo-2&unit=${shown}`);
  }
  // Nine units, and the two customer sentences each cite their contact.
  assert.equal(cites.length, 9 + 2);
});
