import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import type {Read} from '../../../api/reads.ts';
import type {Sealed} from '../../../api/sealed.ts';
import {type SealedCard, sealedCards} from '../../../api/sealedCards.ts';
import type {CardView} from '../../../api/types.ts';
import {dealAt, dealHref, tabAt} from '../deals.ts';

// The deal's Text tab and its seal check, rendered for every golden card in
// testdata/oracle/cards. The card's JSON here holds only what the list sends;
// the IDs and times are made up, as the server makes its own.

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

const cardOf = (sealed: SealedCard, id: string, published_at = '2027-01-15T15:00:04Z'): CardView => ({
  id,
  account: 'acc-003',
  brief_id: 'brief',
  kind: 'go',
  schema_version: 3,
  rules_version: '3',
  sha256: sealed.sha256,
  size: sealed.bytes.length,
  seal_receipt_id: `seal-${id}`,
  published_at,
});
const ready = <T>(data: T): Read<T> => ({status: 'ready', data});
const thornfield = sealedCards.find(card => card.file === 'demo' && card.key === 'acc-003') as SealedCard;

interface Shown {
  card?: CardView;
  listed?: CardView[];
  sealed?: Read<Sealed>;
  tab?: 'card' | 'text';
  pinned?: string | null;
}
function deal({
  card = cardOf(thornfield, 'card-1'),
  listed = [card],
  sealed = ready({outcome: 'verified', text: thornfield.text}),
  tab = 'text',
  pinned = null,
}: Shown = {}) {
  const path = dealHref('acc-003', '', {tab, card: pinned});
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      {initialEntries: [path]},
      createElement(view.DealView, {
        asked: 'acc-003',
        pinned,
        query: '',
        tab,
        reads: {
          accounts: ready({accounts: [{account: 'acc-003', name: 'Thornfield Wren LLP'}]}),
          cards: ready({account: 'acc-003', cards: listed}),
          card: ready({...card, as_of: null, sources: null, units: [], gaps: [], lines: null}),
          sealed,
        },
      }),
    ),
  );
}
const decode = (html: string) =>
  html.replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#x27;', "'").replaceAll('&amp;', '&');
const plain = (html: string) => {
  const match = /<pre class="dc-plain__text">([^<]*)<\/pre>/.exec(html);
  return match ? decode(match[1] as string) : null;
};
const words = (html: string) => decode(html.replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ');

test('for every golden card the Text tab holds the sealed text byte for byte, as one text node', () => {
  let compared = 0;
  for (const sealed of sealedCards) {
    const html = deal({card: cardOf(sealed, 'card-1'), sealed: ready({outcome: 'verified', text: sealed.text})});
    assert.equal(plain(html), sealed.text, `${sealed.file} ${sealed.key}`);
    compared += 1;
  }
  assert.equal(compared, 90);
});

test('the Text tab has the copy button and an empty status line, and none of the card tab', () => {
  const html = deal();
  assert.match(html, /<button[^>]*class="ui-button ui-button--plain"[^>]*>Copy the plain-text card<\/button><p class="dc-plain__status" role="status"><\/p>/);
  assert.doesNotMatch(html, /dc-ledger|dc-section|dc-values-note/);
});

test('the tabs are links to the card, its communications and its text, the open one marked, a pinned card kept', () => {
  const html = deal({tab: 'card', pinned: 'card-1'});
  assert.match(
    html,
    /<nav class="dc-tabs" aria-label="Views of the card"><a class="dc-tab" aria-current="page" href="\/deals\/acc-003\?card=card-1"[^>]*>Card<\/a><a class="dc-tab" href="\/deals\/acc-003\/communications\?card=card-1"[^>]*>Communications<\/a><a class="dc-tab" href="\/deals\/acc-003\/text\?card=card-1"[^>]*>Text<\/a><\/nav>/,
  );
  assert.match(deal(), /<a class="dc-tab" aria-current="page" href="\/deals\/acc-003\/text"[^>]*>Text<\/a>/);
  assert.match(html, /The countdown and ARR are on <a class="dc-values-note__link" href="\/deals\/acc-003\/text\?card=card-1"[^>]*>the plain-text card<\/a>\./);
});

test('a card that fails its seal check shows one panel on either tab and nothing of the card', () => {
  const failed = ready<Sealed>({outcome: 'failed'});
  const lines = thornfield.text.split('\n').filter(line => line.trim().length > 3);
  for (const tab of ['card', 'text'] as const) {
    const html = deal({sealed: failed, tab});
    assert.match(words(html), /This card failed its seal check\. Nothing from it is shown\. Card card-1, published 15 Jan 2027, 15:00:04 UTC/);
    assert.match(html, /data-seal="failed"/);
    assert.doesNotMatch(html, /dc-tabs|dc-plain|dc-ledger|dc-section|dc-values-note|<blockquote|Open the card before it/);
    for (const line of lines) {
      assert.ok(!words(html).includes(line.trim().replace(/\s+/g, ' ')), `${tab}: the card's line "${line}" is not on the page`);
    }
  }
});

test('the panel links to the card published before the failed one, on the same tab', () => {
  const newer = cardOf(thornfield, 'card-newer', '2027-01-16T09:00:00Z');
  const older = cardOf(thornfield, 'card-older', '2027-01-12T09:10:00Z');
  const failed = ready<Sealed>({outcome: 'failed'});
  assert.match(
    deal({card: newer, listed: [newer, older], sealed: failed, tab: 'text'}),
    /<a class="dc-link" href="\/deals\/acc-003\/text\?card=card-older"[^>]*>Open the card before it<\/a>/,
  );
  assert.match(
    deal({card: newer, listed: [newer, older], sealed: failed, tab: 'card'}),
    /<a class="dc-link" href="\/deals\/acc-003\?card=card-older"[^>]*>Open the card before it<\/a>/,
  );
  assert.doesNotMatch(deal({card: older, listed: [newer, older], sealed: failed, pinned: 'card-older'}), /Open the card before it/);
});

test('while the seal is checked nothing of the card is drawn, and a refused check is a refusal', () => {
  for (const tab of ['card', 'text'] as const) {
    const reading = deal({sealed: {status: 'loading'}, tab});
    assert.match(reading, /Reading the card/);
    assert.doesNotMatch(reading, /dc-tabs|dc-plain|dc-ledger|dc-values-note/);
    const refused = deal({sealed: {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}} as Read<Sealed>, tab});
    assert.match(refused, /data-refusal="not_permitted"/);
    assert.doesNotMatch(refused, /dc-tabs|dc-plain|dc-ledger|data-seal/);
  }
});

test('a deal’s addresses: the card at its own, the text at /text, a pinned card and the filter kept', () => {
  assert.equal(dealHref('acc-003', '', {tab: 'text'}), '/deals/acc-003/text');
  assert.equal(dealHref('acc-003', 'wren', {tab: 'text', card: 'c 1'}), '/deals/acc-003/text?card=c+1&q=wren');
  assert.equal(dealHref('acc-003', '', {card: 'c-1'}), '/deals/acc-003?card=c-1');
  assert.equal(dealAt('/deals/acc-003/text'), 'acc-003');
  assert.equal(tabAt('/deals/acc-003/text'), 'text');
  assert.equal(tabAt('/deals/acc-003'), 'card');
  assert.equal(dealAt('/deals/acc-003/texts'), null);
  assert.equal(dealAt('/deals/acc-003/text/more'), null);
});
