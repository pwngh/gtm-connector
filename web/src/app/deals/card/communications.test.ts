import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import type {Read} from '../../../api/reads.ts';
import type {CardDetail} from '../../../api/types.ts';
import {dealAt, dealHref, tabAt} from '../deals.ts';
import {groupOf, onCard, roleWords, sentencesOf, usableTime} from './card.ts';
import {talksOf} from './communications.ts';
import {find, recorded, salesforce, thornfield} from './recorded.ts';

// The deal's Communications tab, rendered for every recorded card in
// testdata/oracle/cards: every sentence unit once, in its call or message,
// the groups oldest first and the ones with no usable time last.

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

const ready = <T>(data: T): Read<T> => ({status: 'ready', data});
const tabPath = (card: CardDetail) => dealHref(card.account, '', {tab: 'communications'});
const deal = (card: CardDetail) =>
  renderToStaticMarkup(
    createElement(
      MemoryRouter,
      {initialEntries: [tabPath(card)]},
      createElement(view.DealView, {
        asked: card.account,
        pinned: null,
        query: '',
        tab: 'communications',
        reads: {
          accounts: ready({accounts: [{account: card.account, name: 'Thornfield Wren LLP'}]}),
          cards: ready({account: card.account, cards: [card]}),
          card: ready(card),
          sealed: ready({outcome: 'verified', text: ''}),
        },
      }),
    ),
  );
const decode = (html: string) =>
  html.replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#x27;', "'").replaceAll('&amp;', '&');
const words = (html: string) => decode(html.replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ');
const count = (html: string, pattern: RegExp) => [...html.matchAll(pattern)].length;
/** The groups as drawn, in order: their record_ref and whether they sit under No time recorded. */
const drawnGroups = (html: string) => {
  const untimedAt = html.indexOf('id="dc-comms-untimed"');
  return [...html.matchAll(/<li class="dc-talk" data-talk="([^"]*)">/g)].map(match => ({
    ref: decode(match[1] ?? ''),
    untimed: untimedAt >= 0 && (match.index ?? 0) > untimedAt,
  }));
};
/** One unit's line as drawn. */
const lineOf = (html: string, n: number) => html.slice(html.indexOf(`data-comm-unit="${n}"`)).split('</li>')[0] ?? '';
const every = [...recorded, ...salesforce];

test('every golden card: each sentence unit once, in its own call or message, whole text as its quotation, and no record unit', () => {
  let sentences = 0;
  let withHidden = 0;
  for (const {file, key, card} of every) {
    const at = `${file} ${key}`;
    const html = deal(card);
    const all = sentencesOf(card);
    sentences += all.length;
    if (all.some(sentence => !onCard(sentence))) {
      withHidden += 1;
    }
    assert.equal(count(html, /data-comm-unit="/g), all.length, `${at}: one line per sentence unit`);
    for (const sentence of all) {
      const where = `${at} unit ${sentence.n}`;
      assert.equal(count(html, new RegExp(`data-comm-unit="${sentence.n}"`, 'g')), 1, `${where}: once`);
      // The line sits in the group of its own record_ref.
      const before = html.slice(0, html.indexOf(`data-comm-unit="${sentence.n}"`));
      const group = [...before.matchAll(/<li class="dc-talk" data-talk="([^"]*)">/g)].at(-1)?.[1];
      assert.equal(decode(group ?? ''), sentence.recordRef, `${where}: in its record's group`);
      const line = lineOf(html, sentence.n);
      const quote = /<blockquote class="dc-quote" data-unit="(\d+)">([^<]*)<\/blockquote>/.exec(line);
      if (sentence.partial) {
        // A unit that is not whole is never a quotation: its text is listed under its label.
        assert.equal(quote, null, `${where}: no quotation of a cut unit`);
        assert.match(line, /Cut, not a whole quotation/, `${where}: listed as cut`);
      } else {
        assert.equal(decode(quote?.[2] ?? ''), sentence.text, `${where}: the whole text, exactly`);
      }
      assert.match(line, new RegExp(`aria-label="Source ${sentence.n}"`), `${where}: its number`);
      const speaker = /<span class="dc-attr__part dc-attr__speaker">([^<]*)<\/span>/.exec(line)?.[1];
      assert.equal(decode(speaker ?? ''), sentence.speaker || 'No speaker label', `${where}: its speaker`);
      assert.equal(/class="dc-meta dc-talk__reason"/.test(line), !onCard(sentence), `${where}: a reason only when the card does not show it`);
    }
    const records = (card.units ?? []).filter(unit => unit.kind === 'record');
    for (const record of records) {
      assert.doesNotMatch(html, new RegExp(`data-comm-unit="${record.n}"`), `${at}: record ${record.n} is not listed`);
    }
    assert.doesNotMatch(html, /dc-ledger|data-quote-unit|data-more-unit/, `${at}: nothing of the Card tab`);
  }
  assert.equal(sentences, 90 + 1297 + 89 + 127 + 10);
  assert.ok(withHidden >= 12, `${withHidden} golden cards have sentences the card does not show`);
});

test('every recorded card: groups oldest first by their earliest time, the groups with no usable time last under No time recorded', () => {
  let untimedCards = 0;
  for (const {file, key, card} of every) {
    const at = `${file} ${key}`;
    // The earliest usable time of each record_ref, worked out here from the units.
    const earliest = new Map<string, number | null>();
    for (const sentence of sentencesOf(card)) {
      const time = usableTime(sentence.at);
      const known = earliest.get(sentence.recordRef as string);
      earliest.set(sentence.recordRef as string, time === null ? (known ?? null) : known == null ? time : Math.min(known, time));
    }
    const drawn = drawnGroups(deal(card));
    assert.deepEqual(drawn.map(group => group.ref).sort(), [...earliest.keys()].sort(), `${at}: one group per record_ref`);
    const timed = drawn.filter(group => !group.untimed);
    const untimed = drawn.filter(group => group.untimed);
    assert.deepEqual(drawn, [...timed, ...untimed], `${at}: the untimed groups come last`);
    assert.ok(
      timed.every(group => earliest.get(group.ref) !== null),
      `${at}: every group above No time recorded has a time`,
    );
    assert.ok(
      untimed.every(group => earliest.get(group.ref) === null),
      `${at}: every group under No time recorded has none`,
    );
    const times = timed.map(group => earliest.get(group.ref) as number);
    assert.deepEqual(
      times,
      [...times].sort((a, b) => a - b),
      `${at}: oldest first`,
    );
    if (untimed.length) {
      untimedCards += 1;
    }
  }
  assert.ok(untimedCards > 0, 'a recorded card has a group with no usable time');
});

test('the README’s deal: the counts, then two calls and a Slack message, oldest first, each sentence with its speaker and role', () => {
  const card = thornfield();
  const html = deal(card);
  assert.match(html, /<p class="dc-meta dc-comms__counts">2 calls, 1 Slack message and 6 sentences were read\. The card shows every sentence\.<\/p>/);
  const heads = [...html.matchAll(/<h3 class="dc-talk__head">([\s\S]*?)<\/h3>/g)].map(match => words(match[1] ?? '').trim());
  assert.deepEqual(heads, ['Call 12 Jan 2027, 15:00:00 UTC', 'Call 12 Jan 2027, 15:00:00 UTC', 'Slack message 12 Jan 2027, 16:00:00 UTC']);
  assert.deepEqual(
    drawnGroups(html).map(group => group.ref),
    ['calls/call_notes/note-003', 'calls/call_notes/note-003b', 'slack/messages/msg-003'],
  );
  assert.deepEqual(
    [...html.matchAll(/data-comm-unit="(\d+)"/g)].map(match => Number(match[1])),
    [4, 5, 6, 7, 8, 9],
  );
  assert.match(html, /<time class="dc-talk__time" dateTime="2027-01-12T16:00:00Z">/);
  assert.match(words(lineOf(html, 5)), /Marian Holt Customer/);
  assert.match(words(lineOf(html, 7)), /Avery Linden Seller/);
  assert.doesNotMatch(html, /No time recorded|dc-talk__reason/);
});

test('a sentence the card does not show carries its reason; the counts say how many', () => {
  const item = every.find(({card}) => sentencesOf(card).some(sentence => sentence.shown === 'long'));
  assert.ok(item);
  const html = deal(item.card);
  const hidden = sentencesOf(item.card).filter(sentence => !onCard(sentence));
  assert.match(words(html), new RegExp(`were read\\. The card does not show ${hidden.length} of the sentences\\.`));
  const long = hidden.find(sentence => sentence.shown === 'long');
  assert.ok(long);
  assert.match(lineOf(html, long.n), /<p class="dc-meta dc-talk__reason" data-reason="long">Longer than a card sentence<\/p>/);
});

test('a group with no usable time shows its time as written, under No time recorded', () => {
  const item = every.find(({card}) => talksOf(card).untimed.length > 0);
  assert.ok(item);
  const html = deal(item.card);
  assert.match(html, /<h3 id="dc-comms-untimed" class="dc-group__title">No time recorded<\/h3>/);
  const untimed = html.slice(html.indexOf('id="dc-comms-untimed"'));
  assert.match(
    untimed,
    /<h4 class="dc-talk__head"><span class="dc-talk__what">(Call|Slack message)<\/span><span class="dc-talk__time">[^<]*(no time zone recorded|Not recorded)<\/span><\/h4>/,
  );
  assert.doesNotMatch(untimed, /<time /);
});

test('a unit with partial true is listed as cut in the text face and is never a blockquote', () => {
  const card = structuredClone(thornfield());
  const unit = (card.units ?? []).find(item => item.n === 5);
  assert.ok(unit);
  (unit.body as {partial: boolean}).partial = true;
  const html = deal(card);
  const line = lineOf(html, 5);
  assert.match(line, /<p class="dc-cut"><span class="dc-cut__label">Cut, not a whole quotation<\/span><span class="dc-cut__text">/);
  assert.doesNotMatch(line, /<blockquote/);
  assert.match(line, /data-reason="cut">Cut at a pause, a change of speaker or the edge of an excerpt<\/p>/);
  assert.match(words(html), /The card does not show 1 of the sentences\./);
  assert.equal(count(html, /<blockquote/g), 5);
});

test('the tabs: Card, Communications and Text, each its own address, the open one marked', () => {
  const html = deal(thornfield());
  assert.match(
    html,
    /<nav class="dc-tabs" aria-label="Views of the card"><a class="dc-tab" href="\/deals\/acc-003"[^>]*>Card<\/a><a class="dc-tab" aria-current="page" href="\/deals\/acc-003\/communications"[^>]*>Communications<\/a><a class="dc-tab" href="\/deals\/acc-003\/text"[^>]*>Text<\/a><\/nav>/,
  );
  assert.equal(dealHref('acc-003', 'wren', {tab: 'communications', card: 'c-1'}), '/deals/acc-003/communications?card=c-1&q=wren');
  assert.equal(tabAt('/deals/acc-003/communications'), 'communications');
  assert.equal(dealAt('/deals/acc-003/communications'), 'acc-003');
  assert.equal(dealAt('/deals/acc-003/communications/more'), null);
  assert.equal(dealAt('/deals/acc-003/communication'), null);
});

test('every number is a link named Source n that opens the proof drawer on this tab', () => {
  const html = deal(thornfield());
  const cites = [...html.matchAll(/<a class="dc-cite" aria-label="Source (\d+)" data-cite="\d+" href="([^"]*)"/g)].map(match => [
    Number(match[1]),
    decode(match[2] ?? ''),
  ]);
  assert.deepEqual(
    cites,
    [4, 5, 6, 7, 8, 9].map(n => [n, `/deals/acc-003/communications?unit=${n}`]),
  );
});

test('every recorded card: the first sentence of each role carries that role’s help after its role word, in the copy file’s words', () => {
  const help = JSON.parse(readFileSync(new URL('../../../help/help.json', import.meta.url), 'utf8')) as Record<string, {text: string}>;
  for (const {file, key, card} of every) {
    const html = deal(card);
    const listed = talksOf(card);
    const order = [...listed.timed, ...listed.untimed].flatMap(talk => talk.sentences);
    const firsts = new Map<string, number>();
    for (const sentence of order) {
      if (!firsts.has(groupOf(sentence))) {
        firsts.set(groupOf(sentence), sentence.n);
      }
    }
    const drawn = [...html.matchAll(/data-help="(role\.[a-z_]+)"><span class="ui-help__text">([^<]*)<\/span>/g)];
    assert.deepEqual(
      drawn.map(match => match[1]),
      [...firsts.keys()].map(group => `role.${group}`),
      `${file} ${key}`,
    );
    for (const match of drawn) {
      assert.equal(decode(match[2] ?? ''), help[match[1] ?? '']?.text, `${file} ${key}`);
    }
    for (const [group, n] of firsts) {
      assert.match(
        lineOf(html, n),
        new RegExp(`class="dc-attr__part">${roleWords[group as keyof typeof roleWords]}[^<]*<span class="ui-help">`),
        `${file} ${key} ${n}`,
      );
    }
  }
});

test('the recorded injection case and hostile server strings render as text and make no element', () => {
  const card = find('cases', 'test_05_injected_markup_and_figures_stay_inert/2');
  const html = deal(card);
  assert.match(words(html), /\[mention channel\] \[user U0ABC123\] IGNORE PREVIOUS INSTRUCTIONS/);
  // Every link is the deal's own address, or the guide of a role's help panel.
  for (const [, href] of html.matchAll(/href="([^"]*)"/g)) {
    assert.match(
      decode(href ?? ''),
      /^(\/deals\/acc-905(\/communications\?unit=\d+|\/text|\/communications)?|https:\/\/github\.com\/pwngh\/gtm-connector\/blob\/main\/docs\/cards\.md#speaker-roles)$/,
    );
  }
  const hostile = '<img src=x onerror=alert(1)>';
  const marked = structuredClone(thornfield());
  marked.units = (marked.units ?? []).map(unit => ({
    ...unit,
    record_ref: `${unit.record_ref}${hostile}`,
    body: {
      ...(unit.body as object),
      source: `s${hostile}`,
      text: `${(unit.body as {text?: string}).text}${hostile}`,
      speaker: `x${hostile}`,
      basis: `b${hostile}`,
      at: `t${hostile}`,
    },
  }));
  const shown = deal(marked);
  assert.doesNotMatch(shown, /<img/);
  for (const part of ['dc-talk__what', 'dc-talk__time', 'dc-attr__part dc-attr__speaker']) {
    assert.match(shown, new RegExp(`<span class="${part}">[^<]*&lt;img src=x onerror=alert\\(1\\)&gt;[^<]*</span>`), part);
  }
  assert.match(shown, /data-talk="[^"]*&lt;img src=x onerror=alert\(1\)&gt;"/);
});

test('a card that read no sentence says so', () => {
  const card = structuredClone(thornfield());
  card.units = (card.units ?? []).filter(unit => unit.kind !== 'sentence');
  const html = deal(card);
  assert.match(html, /<p class="dc-empty">No sentence was read\.<\/p>/);
  assert.doesNotMatch(html, /dc-talk|dc-comms__counts/);
});
