import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {after, before, test} from 'node:test';
import {createElement, type ReactElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import type {Read} from '../../../api/reads.ts';
import type {CardDetail, CardView, GapView} from '../../../api/types.ts';
import {groupOf, newestFirst, notShown, onCard, orderedGaps, recordsOf, type Sentence, sentencesOf, usableTime, wholeSources} from './card.ts';
import {arrOf, type CardLine, linesOf, renewalOf, signalsOf, valuesOf} from './lines.ts';
import {files, find, recorded, salesforce, thornfield, unlined} from './recorded.ts';

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
// The text a reader sees: without the help hints and panels, which are in the document but hidden until opened.
const hiddenHelp = /<span [^>]*class="ui-help__panel"[^>]*hidden=""[^>]*><span class="ui-help__text">[^<]*<\/span>(?:<a [^>]*>[^<]*<\/a>)?<\/span>/g;
const text = (html: string) => decode(html.replace(hiddenHelp, '').replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ');
const count = (html: string, pattern: RegExp) => [...html.matchAll(pattern)].length;
/** The record column's rows as drawn: data-value, label, value and the numbers in its gutter. */
const recordColumn = (html: string) =>
  [
    ...html.matchAll(
      /<li class="dc-line" data-value="([a-z_]+)"><span class="dc-line__n">([\s\S]*?)<\/span><p class="dc-line__body dc-value"><span class="dc-value__label">([^<]*)<\/span><span class="dc-value__text">([^<]*)<\/span><\/p><\/li>/g,
    ),
  ].map(match => ({
    code: match[1],
    cites: [...(match[2] ?? '').matchAll(/aria-label="Source (\d+)"/g)].map(n => Number(n[1])),
    label: decode(match[3] ?? ''),
    text: decode(match[4] ?? ''),
  }));
const valueRow = (line: CardLine) => ({code: line.code, cites: line.cites, label: line.label ?? '', text: line.text});
/** The focal group's markup, or the empty string. */
const focal = (html: string) => html.match(/<section class="dc-focal"[\s\S]*?<\/section>/)?.[0] ?? '';
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
    // Every value line once, in the card's order: its label, its value as sent, its numbers.
    assert.deepEqual(recordColumn(html), valuesOf(linesOf(card) ?? []).map(valueRow), `${at}: the record column is the value lines in order`);
    assert.equal(count(html, /data-record-unit=/g), 0, `${at}: with lines, no reference list`);
    // The same card published before its lines were kept lists each record by its reference.
    const old = body(unlined(card));
    for (const record of recordsOf(card)) {
      const line = old.match(new RegExp(`data-record-unit="${record.n}">[\\s\\S]*?<span class="dc-ref">([^<]*)</span>`));
      assert.equal(count(old, new RegExp(`data-record-unit="${record.n}"`, 'g')), 1, `${at}: record ${record.n} once`);
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
  assert.equal(sentences, 90 + 1297 + 390);
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
  // The speaker as recorded, the title from the contact's line, the contact's number, the role, the source and the time.
  assert.match(text(customer), /Marian Holt Deputy general counsel \[2\] Customer call 12 Jan 2027, 15:00:00 UTC/);
  assert.match(text(body(unlined(card))), /Marian Holt \[2\] Customer call 12 Jan 2027, 15:00:00 UTC/);
  assert.match(customer, /aria-label="Source 2" data-cite="2" href="\/deals\/acc-003\?unit=2"/);
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

test('a sentence its author quoted in a Slack message is labelled as quoted, under no established role', () => {
  const card = structuredClone(thornfield());
  const unit = card.units?.find(unit => unit.n === 8);
  assert.ok(unit?.body && typeof unit.body === 'object');
  Object.assign(unit.body, {quoted: true, role: 'unknown', basis: "quoted by the message's author", contact: null});
  const html = body(card);
  const unknown = html.slice(html.indexOf('data-group="unknown"'), html.indexOf('data-group="seller"'));
  assert.match(text(unknown), /Avery Linden Quoted in the author's message Unknown \(quoted by the message's author\) Slack/);
  assert.equal(count(decode(html), /Quoted in the author's message/g), 1);
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
  // Every link on the card is a unit's number on the deal's own address, the Text tab, or a help panel's guide.
  for (const [, href] of [...html.matchAll(/href="([^"]*)"/g), ...body(unlined(card)).matchAll(/href="([^"]*)"/g)]) {
    assert.match(
      decode(href ?? ''),
      /^(\/deals\/acc-003\?unit=\d+|\/deals\/acc-905\/text|https:\/\/github\.com\/pwngh\/gtm-connector\/blob\/main\/(README|docs\/[a-z]+)\.md#[a-z-]+)$/,
    );
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
  marked.lines = (marked.lines ?? []).map(line => {
    const {
      label,
      text: words,
      contact,
      countdown,
    } = line as {label: string | null; text: string; contact: {name: string; title: string} | null; countdown: object | null};
    return {
      ...(line as object),
      label: label && `${label}${hostile}`,
      text: `${words}${hostile}`,
      contact: contact && {...contact, title: `${contact.title}${hostile}`},
      countdown: countdown && {...countdown, date: `2027-02-08${hostile}`},
    };
  });
  const shown = body(marked);
  assert.doesNotMatch(shown, /<img/);
  assert.match(shown, /&lt;img src=x onerror=alert\(1\)&gt;/);
  // In the countdown, ARR, Flags, the record column and a buyer quote's title.
  for (const part of ['dc-focal__amount', 'dc-flag__text', 'dc-value__label', 'dc-value__text', 'dc-attr__title']) {
    assert.match(shown, new RegExp(`<span class="[^"]*${part}">[^<]*&lt;img src=x onerror=alert\\(1\\)&gt;</span>`), part);
  }
  assert.equal(count(shown, /USD 450,000\.00&lt;img src=x onerror=alert\(1\)&gt;/g), 2, 'ARR in the focal group and the record column');
  assert.match(shown, /<span class="dc-attr__title">Deputy general counsel&lt;img src=x onerror=alert\(1\)&gt;<\/span>/);
  assert.match(shown, /<time dateTime="2027-02-08&lt;img src=x onerror=alert\(1\)&gt;">2027-02-08&lt;img/);
});

test('the title is the server’s name and the ID, then the countdown and ARR; the seal names the card', () => {
  const card = thornfield();
  const html = deal(card);
  assert.match(
    html,
    /<h1 id="dc-title" class="dc-title">Thornfield Wren LLP<\/h1><p class="dc-id">acc-003<\/p><\/header><nav[\s\S]*?<\/nav><section class="dc-focal" aria-label="Countdown and ARR">/,
  );
  assert.doesNotMatch(html, /dc-values-note|plain-text card/);
  assert.match(text(html), /Sealed 15 Jan 2027, 15:00:04 UTC Card card-demo-2/);
  assert.doesNotMatch(html, /At risk|On track|Changed/);
  // A card published before its lines were kept keeps the line on where the values are, with the link to its text.
  const old = deal(unlined(card));
  assert.match(
    old,
    /<\/nav><p class="dc-values-note" data-focal="none">The countdown and ARR are on <a class="dc-values-note__link" href="\/deals\/acc-003\/text"[^>]*>the plain-text card<\/a>\.<\/p>/,
  );
  assert.match(text(old), /This page shows each record’s reference\. Its values are on the plain-text card\./);
  assert.doesNotMatch(old, /dc-focal__|dc-flags|data-value=|dc-attr__title|days/);
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

test('every number is a link named Source n that puts ?unit=n in the deal’s address, keeping the rest', () => {
  const card = thornfield();
  const opened = (shown: CardDetail) =>
    inRouter(createElement(view.CardBody, {account: 'acc-003', card: shown, listed: [shown], query: 'thorn'}), '/deals/acc-003?q=thorn&unit=6');
  for (const [html, total] of [
    // Nine units, and the two customer sentences each cite their contact.
    [opened(unlined(card)), 9 + 2],
    // With lines, the six sentences and two contacts, the countdown's and ARR's numbers, the flag's two, and the
    // six value lines' in the record column, which take the place of the three record references.
    [opened(card), 6 + 2 + 2 + 2 + 6],
  ] as const) {
    const cites = [
      ...html.matchAll(
        /<a class="dc-cite" aria-label="Source (\d+)" data-cite="(\d+)"(?: data-open="")? href="([^"]+)"[^>]*><span class="dc-cite__box">\[(\d+)\]<\/span><\/a>/g,
      ),
    ];
    assert.equal(cites.length, count(html, /class="dc-cite"/g));
    for (const [, named, data, href, shown] of cites) {
      assert.equal(named, shown);
      assert.equal(data, shown);
      assert.equal(decode(href ?? ''), `/deals/acc-003?q=thorn&unit=${shown}`);
    }
    // The number whose unit is open is marked, and only it.
    assert.equal(count(html, /<a class="dc-cite"[^>]*data-open=""/g), 1);
    assert.match(html, /data-cite="6" data-open=""/);
    assert.equal(cites.length, total);
  }
});

const escaped = (value: string) => value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const citesIn = (html: string) => [...html.matchAll(/aria-label="Source (\d+)"/g)].map(match => Number(match[1]));

test('every recorded card with lines: the countdown is the typed days and date with the card’s date, and ARR is its line’s words', () => {
  const seen = {counted: 0, passed: 0, contractEnd: 0, missing: 0, arr: 0, unmapped: 0};
  for (const {file, key, card} of [...recorded, ...salesforce]) {
    const at = `${file} ${key}`;
    const lines = linesOf(card);
    assert.ok(lines, `${at}: the recorded lines read`);
    const shown = focal(body(card));
    const [countdownHtml = '', arrHtml = ''] = shown.split('data-focal="arr"');
    const renewal = renewalOf(lines);
    const countdown = renewal?.countdown;
    if (countdown) {
      seen.counted++;
      seen.passed += countdown.days < 0 ? 1 : 0;
      seen.contractEnd += countdown.countsTo === 'contract_end' ? 1 : 0;
      const number = countdownHtml.match(/<span class="dc-focal__number" data-days="(-?\d+)">(\d+)<\/span><span class="dc-focal__unit">([^<]*)<\/span>/);
      assert.equal(Number(number?.[1]), countdown.days, `${at}: the server's days`);
      assert.equal(number?.[2], String(Math.abs(countdown.days)), `${at}: the number shown`);
      assert.equal(number?.[3], `${Math.abs(countdown.days) === 1 ? 'day' : 'days'}${countdown.days < 0 ? ' ago' : ''}`, `${at}: the word beside it`);
      const what = countdown.countsTo === 'renewal' ? 'the renewal' : 'the contract end';
      assert.ok(
        countdownHtml.includes(`${countdown.days < 0 ? 'since ' : 'to '}${what}, <time dateTime="${countdown.date}">${countdown.date}</time>`),
        `${at}: what it counts to and its date`,
      );
      assert.deepEqual(citesIn(countdownHtml.split('dc-focal__sub')[0] ?? ''), renewal.cites, `${at}: the countdown's numbers`);
    } else {
      seen.missing++;
      assert.match(countdownHtml, /<span>No renewal date<\/span>/, `${at}: no renewal date`);
      assert.doesNotMatch(countdownHtml, /data-days/, `${at}: and no number`);
      assert.ok(renewal && countdownHtml.includes(`<span class="dc-focal__words">${escaped(renewal.text)}</span>`), `${at}: the connector's words under it`);
    }
    assert.ok(countdownHtml.includes(`as of <time dateTime="${card.as_of}">${card.as_of}</time>`), `${at}: the card's date`);
    const arr = arrOf(lines);
    if (arr) {
      seen.arr++;
      assert.ok(arrHtml.includes(`<span class="dc-focal__number dc-focal__amount">${escaped(arr.text)}</span>`), `${at}: ARR in the connector's words`);
      assert.deepEqual(citesIn(arrHtml.split('dc-focal__sub')[0] ?? ''), arr.cites, `${at}: ARR's numbers`);
      assert.match(arrHtml, /<span>ARR, from the mapped field<\/span>/);
    } else {
      seen.unmapped++;
      assert.match(arrHtml, /<span>ARR is not mapped<\/span>/, `${at}: ARR is not mapped`);
      assert.doesNotMatch(arrHtml, /dc-focal__number|aria-label="Source/, `${at}: and no figure stands in`);
    }
  }
  assert.deepEqual(seen, {counted: 163, passed: 15, contractEnd: 65, missing: 13, arr: 98, unmapped: 78});
});

test('a card with no renewal line shows No renewal date and no number', () => {
  const card = structuredClone(thornfield());
  card.lines = (card.lines ?? []).filter(line => (line as {code?: string}).code !== 'renewal_date');
  const shown = focal(body(card));
  assert.match(text(shown), /No renewal date The record has no end date\. Nothing is inferred from the close date\. as of 2027-01-15/);
  assert.doesNotMatch(shown, /data-days|dc-focal__words/);
  assert.match(text(shown), /USD 450,000\.00 \[1\] ARR, from the mapped field/);
});

test('the focal step is used by the countdown and ARR and nothing else', () => {
  const sheets = [
    'ui/base.css',
    'ui/tokens.css',
    'ui/help.css',
    'app/deals/card/card.css',
    'app/deals/card/lines.css',
    'app/deals/deals.css',
    'app/ops/activity.css',
  ];
  const users: string[] = [];
  for (const sheet of sheets) {
    const css = readFileSync(new URL(`../../../${sheet}`, import.meta.url), 'utf8');
    for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (/var\(--gc-focal\)|font-size:\s*2\.5rem/.test(match[2] ?? '')) {
        users.push(`${sheet} ${(match[1] ?? '').trim()}`);
      }
    }
  }
  assert.deepEqual(users, ['app/deals/card/lines.css .dc-focal__number']);
  const html = body(thornfield());
  assert.equal(count(html, /class="dc-focal__number/g), 2, 'the countdown and ARR');
});

test('changing the browser’s clock changes nothing on the page', t => {
  const cards = [
    thornfield(),
    recorded.find(item => (renewalOf(linesOf(item.card) ?? [])?.countdown?.days ?? 0) < 0)?.card as CardDetail,
    salesforce[0]?.card as CardDetail,
  ];
  const at = (now: string) => {
    t.mock.timers.enable({apis: ['Date'], now: Date.parse(now)});
    const html = cards.map(card => deal(card));
    t.mock.timers.reset();
    return html;
  };
  assert.deepEqual(at('2026-03-01T00:00:00Z'), at('2031-12-31T23:59:59Z'));
});

test('a customer quotation shows the title from its contact’s line; a quotation with no contact or no title shows none', () => {
  let titled = 0;
  let untitled = 0;
  for (const {file, key, card} of recorded) {
    const html = body(card);
    const contacts = new Map((linesOf(card) ?? []).filter(line => line.code === 'contact').map(line => [line.cites[0], line.contact?.title ?? null]));
    for (const sentence of sentencesOf(card).filter(onCard)) {
      const line = html.slice(html.indexOf(`data-quote-unit="${sentence.n}"`)).split('</li>')[0] ?? '';
      const title = groupOf(sentence) === 'customer' && sentence.contact !== null ? (contacts.get(sentence.contact) ?? null) : null;
      if (title) {
        titled++;
        assert.ok(
          line.includes(`<span class="dc-attr__title">${escaped(title)}</span><a class="dc-cite" aria-label="Source ${sentence.contact}"`),
          `${file} ${key} unit ${sentence.n}: the title, then the contact's number`,
        );
      } else {
        untitled++;
        assert.doesNotMatch(line, /dc-attr__title/, `${file} ${key} unit ${sentence.n}: no title`);
      }
    }
  }
  assert.ok(titled > 0 && untitled > 0, `${titled} titled, ${untitled} untitled`);
});

test('the fixed signals are the first lines of Flags, each with its numbers; a card with none has no Flags', () => {
  let flagged = 0;
  for (const {file, key, card} of [...recorded, ...salesforce]) {
    const html = body(card);
    const signals = signalsOf(linesOf(card) ?? []);
    if (!signals.length) {
      assert.doesNotMatch(html, /dc-flags/, `${file} ${key}`);
      continue;
    }
    flagged++;
    const rows = [
      ...html.matchAll(
        /<li class="dc-flag" data-signal="([a-z_]+)"><span class="dc-flag__text">([^<]*)<\/span><span class="dc-flag__cites">([\s\S]*?)<\/span><\/li>/g,
      ),
    ].map(match => ({
      code: match[1],
      text: decode(match[2] ?? ''),
      cites: citesIn(match[3] ?? ''),
      more: Number(match[3]?.match(/and (\d+) more/)?.[1] ?? 0),
    }));
    assert.deepEqual(
      rows,
      signals.map(signal => ({code: signal.code, text: signal.text, cites: signal.cites, more: signal.more})),
      `${file} ${key}`,
    );
    assert.ok(html.indexOf('dc-flags') < html.indexOf('dc-ledger'), `${file} ${key}: Flags comes before the ledger`);
  }
  assert.equal(flagged, 92);
});

test('lines that are not a line’s shape are not read, and the card says where its values are', () => {
  const card = structuredClone(thornfield());
  card.lines = [...(card.lines ?? []), {code: 'arr', section: 'value'}];
  assert.equal(linesOf(card), null);
  const html = body(card);
  assert.match(html, /data-focal="none"/);
  assert.doesNotMatch(html, /dc-focal__|dc-flags|data-value=/);
});

const helpFile = JSON.parse(readFileSync(new URL('../../../help/help.json', import.meta.url), 'utf8')) as Record<
  string,
  {title: string; text: string; guide?: {label: string; path: string}}
>;
const guideFile = (path: string) => readFileSync(new URL(`../../../../../${path}`, import.meta.url), 'utf8');
// Each entry and the guide it is drawn from, with words the entry and the guide both hold.
const drawnFrom: Record<string, [string, string]> = {
  citation: ['README.md', 'one CRM record, or one sentence of a call or a Slack message'],
  quotation: ['README.md', 'A sentence too long to show is counted, never cut.'],
  record: ['README.md', 'typed CRM fields and connector arithmetic'],
  'role.customer': ['docs/cards.md', 'Slack authors are never customers.'],
  'role.seller': ['docs/cards.md', "equals a name or ID on the workspace's seller list."],
  'role.unknown': ['docs/cards.md', 'two or more contacts with the name, a name shared by a seller and a contact, or a label over 200 characters'],
  not_read: ['docs/deals.md', 'was not read'],
  seal: ['README.md', 'A card is sealed when it is published: a receipt names the SHA-256 of its bytes.'],
  countdown: ['docs/config.md', 'renewal date'],
  arr: ['docs/config.md', 'Opportunity amount never stands in for'],
  'ops.coverage': ['docs/config.md', 'Which deals have a current card'],
  'ops.receipt': ['docs/config.md', 'A read gets its receipt before it runs.'],
  'ops.activity': ['docs/audit.md', 'every card seal leaves a receipt'],
  'receipt.arguments': ['docs/audit.md', 'The arguments as the call sent them'],
};
// GitHub's anchor for a heading.
const anchorOf = (heading: string) =>
  heading
    .toLowerCase()
    .replace(/[^a-z0-9 -]/g, '')
    .replaceAll(' ', '-');

test('every help entry is fixed copy drawn from a guide, and each guide link names a section the guide has', () => {
  assert.deepEqual(Object.keys(helpFile).sort(), Object.keys(drawnFrom).sort());
  for (const [key, entry] of Object.entries(helpFile)) {
    const [file, words] = drawnFrom[key] ?? ['', ''];
    assert.ok(entry.title && entry.text, key);
    assert.ok(
      entry.text.toLowerCase().includes(words.toLowerCase()) && guideFile(file).replace(/\s+/g, ' ').toLowerCase().includes(words.toLowerCase()),
      `${key}: ${file}`,
    );
    if (entry.guide) {
      const [path = '', anchor = ''] = entry.guide.path.split('#');
      assert.equal(path, file, `${key}: the guide it links to`);
      const anchors = [...guideFile(path).matchAll(/^#{1,4} (.+)$/gm)].map(match => anchorOf(match[1] ?? ''));
      assert.ok(anchors.includes(anchor), `${key}: ${path} has #${anchor}`);
    }
  }
});

test('the card’s help: each shown sentence is the copy file’s, a hint describes its button and a panel is a note the button controls', () => {
  const html = body(thornfield());
  const helps = [
    ...html.matchAll(
      /<span class="ui-help"><button type="button"([^>]*)>[\s\S]*?<\/button><span id="([^"]+)" class="ui-help__panel" role="(tooltip|note)"[^>]*hidden="" data-help="([a-z_.]+)"><span class="ui-help__text">([^<]*)<\/span>(?:<a class="ui-help__guide" href="([^"]*)" target="_blank" rel="noopener noreferrer">([^<]*)<\/a>)?<\/span><\/span>/g,
    ),
  ].map(match => ({
    attributes: match[1] ?? '',
    id: match[2],
    role: match[3],
    key: match[4] ?? '',
    text: decode(match[5] ?? ''),
    href: match[6] ? decode(match[6]) : null,
    label: match[7] ? decode(match[7]) : null,
  }));
  assert.equal(count(html, /class="ui-help__button"/g), helps.length);
  // The countdown, ARR's number box and ARR, the heads, one role word of each group the card shows, and the seal line.
  assert.deepEqual(
    helps.map(help => help.key),
    ['countdown', 'citation', 'arr', 'quotation', 'role.customer', 'role.seller', 'record', 'not_read', 'seal'],
  );
  for (const help of helps) {
    const entry = helpFile[help.key];
    assert.ok(entry, help.key);
    assert.equal(help.text, entry.text, `${help.key}: the sentence shown`);
    assert.match(help.attributes, new RegExp(`aria-label="About ${escaped(entry.title)}"`), help.key);
    if (entry.guide) {
      assert.equal(help.role, 'note', help.key);
      assert.match(
        help.attributes,
        new RegExp(`id="([^"]+)" class="ui-help__button" aria-label="[^"]*" aria-expanded="false" aria-controls="${help.id}"`),
        help.key,
      );
      assert.ok(
        html.includes(`aria-labelledby="${help.attributes.match(/id="([^"]+)"/)?.[1]}" hidden="" data-help="${help.key}"`),
        `${help.key}: the note is labelled by its button`,
      );
      assert.equal(help.href, `https://github.com/pwngh/gtm-connector/blob/main/${entry.guide.path}`, help.key);
      assert.equal(help.label, `Guide: ${entry.guide.label}`, help.key);
    } else {
      assert.equal(help.role, 'tooltip', help.key);
      assert.match(help.attributes, new RegExp(`aria-describedby="${help.id}"`), help.key);
      assert.doesNotMatch(help.attributes, /aria-expanded|aria-controls/, help.key);
      assert.equal(help.href, null, help.key);
    }
  }
  // On every recorded card, the help of a role sits after the role word on the first line of each group shown, and nowhere else.
  for (const {file, key, card} of recorded) {
    const each = body(card);
    const shown = new Set(sentencesOf(card).filter(onCard).map(groupOf));
    const roles = [...each.matchAll(/class="dc-attr__part">(Customer|Seller|Unknown)[^<]*<span class="ui-help">[\s\S]*?data-help="(role\.[a-z]+)"/g)];
    assert.deepEqual(
      roles.map(match => `${match[1]?.toLowerCase()} ${match[2]}`).sort(),
      [...shown].map(group => `${group} role.${group}`).sort(),
      `${file} ${key}`,
    );
    for (const match of each.matchAll(/data-help="([a-z_.]+)"><span class="ui-help__text">([^<]*)<\/span>/g)) {
      assert.equal(decode(match[2] ?? ''), helpFile[match[1] ?? '']?.text, `${file} ${key} ${match[1]}`);
    }
  }
  // With no ARR line, the help for a number is on the countdown's first number box.
  const noArr = structuredClone(thornfield());
  noArr.lines = (noArr.lines ?? []).filter(line => (line as {code?: string}).code !== 'arr');
  assert.match(
    focal(body(noArr)),
    /data-focal="countdown"><p class="dc-focal__figure">[\s\S]*?aria-label="Source 1"[\s\S]*?<\/a><span class="ui-help">[\s\S]*?data-help="citation"/,
  );
  assert.equal(count(body(noArr), /data-help="citation"/g), 1);
  // A card published before its lines were kept has no number box above the ledger, and no citation help.
  assert.doesNotMatch(body(unlined(thornfield())), /data-help="citation"/);
});
