import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';
import type {AccountCards, AccountView, CardView} from '../../api/types.ts';
import {serverPaths} from '../routes.ts';

// Coverage rendered from answers the read layer already holds, as a page that
// comes back within a session shows them: the account list, each card list and
// /healthz, in the shapes the Go server sends.

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let coverage: any, reads: any, client: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  coverage = await server.ssrLoadModule('/src/app/ops/Coverage.tsx');
  reads = await server.ssrLoadModule('/src/api/reads.ts');
  client = await server.ssrLoadModule('/src/api/client.ts');
});
after(async () => {
  await server?.close();
});

const hostile = '<img src=x onerror=alert(1)>';
const signedIn = {
  mode: 'api',
  error: null,
  ended: false,
  generation: 1,
  elsewhere: null,
  session: {
    signed_in: true,
    csrf_token: 't',
    expires_at: '2099-01-15T23:00:00Z',
    principal: {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true},
  },
};
const card = (account: string, id: string, published_at: string): CardView => ({
  id,
  account,
  brief_id: 'b',
  kind: 'go',
  schema_version: 3,
  rules_version: '3',
  sha256: 'x',
  size: 1,
  seal_receipt_id: 's',
  published_at,
});
const granted: AccountView[] = [
  {account: 'acc-001', name: `First ${hostile} Co.`},
  {account: 'acc-002', name: 'Second Co.'},
  {account: 'acc-003', name: 'Third Co.'},
  {account: 'acc-004', name: 'Fourth Co.'},
];
const lists: Record<string, AccountCards> = {
  'acc-001': {account: 'acc-001', cards: [card('acc-001', 'card-1b', '2027-01-15T15:00:04Z'), card('acc-001', 'card-1a', '2027-01-14T08:00:00Z')]},
  'acc-002': {account: 'acc-002', cards: []},
  'acc-003': {account: 'acc-003', cards: [card('acc-003', 'card-3', '2027-01-12T10:30:00Z')]},
};
const refusal = (kind: string) =>
  new client.ApiError({status: kind === 'not_permitted' ? 403 : 503, kind, code: kind === 'not_permitted' ? 'not_permitted' : 'internal'});

interface Answers {
  accounts?: {accounts: AccountView[]};
  cards?: Record<string, AccountCards>;
  health?: {status: string};
}

// A store whose read layer holds each answer, read once through it.
async function storeWith({accounts = {accounts: granted}, cards = lists, health = {status: 'ok'}}: Answers = {}) {
  const api = {
    accounts: () => Promise.resolve(accounts),
    accountCards: (account: string) => (cards[account] ? Promise.resolve(cards[account]) : Promise.reject(refusal('not_permitted'))),
    health: () => Promise.resolve(health),
  };
  const store = {api, subscribe: () => () => {}, getSnapshot: () => signedIn, expire: () => Promise.resolve(signedIn)};
  const held = reads.readsFor(store);
  const keys = ['accounts', 'health', ...accounts.accounts.map(item => `cards:${item.account}`)];
  const load = (key: string) =>
    key === 'accounts' ? (a: typeof api) => a.accounts() : key === 'health' ? (a: typeof api) => a.health() : (a: typeof api) => a.accountCards(key.slice(6));
  await Promise.all(keys.map(key => new Promise(resolve => held.request(`1|${key}`, load(key), resolve))));
  return store;
}
const render = (store: unknown, query = '') =>
  renderToStaticMarkup(
    createElement(
      reads.ServerContext,
      {value: store},
      createElement(MemoryRouter, {initialEntries: [serverPaths.coverage + query]}, createElement(coverage.default)),
    ),
  );
const text = (html: string) =>
  html
    .replace(/<[^>]+>/g, ' ')
    .replaceAll('&#x27;', "'")
    .replaceAll('&quot;', '"')
    .replaceAll('&amp;', '&')
    .replaceAll('&lt;', '<')
    .replaceAll('&gt;', '>')
    .replace(/\s+/g, ' ')
    .trim();
const bodyRows = (html: string) => [...(html.split('<tbody>')[1] ?? '').matchAll(/<tr data-account="([^"]+)"/g)].map(match => match[1]);
const counts = (html: string) =>
  Object.fromEntries(
    [...html.matchAll(/data-filter="(\w+)" aria-pressed="(true|false)"[^>]*><strong>(\d+)<\/strong>/g)].map(match => [match[1], Number(match[3])]),
  );
const rowOf = (html: string, account: string) => html.slice(html.indexOf(`<tr data-account="${account}"`)).split('</tr>')[0] ?? '';

// The read layer keeps answers, not refusals, so acc-004's refused list is
// still Reading on the page; the rows below show it refused.
test('the page counts deals, with a card and with none, each count equal to the rows its filter shows, and a list not in yet says Reading', async () => {
  const store = await storeWith();
  const all = render(store);
  assert.deepEqual(counts(all), {all: 4, card: 2, none: 1});
  assert.match(text(all), /4 deals 2 have a card 1 has none Reading the cards of 1 deal\./);
  for (const [show, expected] of [
    ['', ['acc-001', 'acc-004', 'acc-002', 'acc-003']],
    ['card', ['acc-001', 'acc-003']],
    ['none', ['acc-002']],
  ] as const) {
    const html = render(store, show ? `?show=${show}` : '');
    const shown = bodyRows(html);
    assert.deepEqual(shown, expected, show);
    assert.equal(shown.length, counts(html)[show || 'all'], `the ${show || 'all'} count is its row count`);
    assert.match(html, new RegExp(`data-filter="${show || 'all'}" aria-pressed="true"`));
  }
  assert.match(text(rowOf(all, 'acc-004')), /^Fourth Co\. acc-004 Newest card Reading$/);
});

test('rows: a refused list says Not permitted and counts as not read; each count equals the rows its filter shows', () => {
  const pooled = new Map<string, unknown>([
    ['cards:acc-001', {status: 'ready', data: {account: 'acc-001', cards: [card('acc-001', 'card-1b', '2027-01-15T15:00:04Z')]}}],
    ['cards:acc-002', {status: 'ready', data: {account: 'acc-002', cards: []}}],
    ['cards:acc-003', {status: 'failed', error: refusal('server')}],
    ['cards:acc-004', {status: 'failed', error: refusal('not_permitted')}],
  ]);
  const rows = coverage.rowsOf(granted, pooled);
  const summary = renderToStaticMarkup(createElement(coverage.SummaryLine, {rows, filter: 'all', choose: () => {}}));
  assert.deepEqual(counts(summary), {all: 4, card: 1, none: 1, unread: 2});
  assert.match(text(summary), /^4 deals 1 has a card 1 has none 2 could not be read$/);
  const sort = {key: 'deal', direction: 'ascending'};
  const table = (filter: string, shown = rows) =>
    renderToStaticMarkup(createElement(MemoryRouter, null, createElement(coverage.CoverageTable, {rows: shown, filter, sort, choose: () => {}})));
  for (const [filter, expected] of [
    ['all', 4],
    ['card', 1],
    ['none', 1],
    ['unread', 2],
  ] as const) {
    assert.equal(bodyRows(table(filter)).length, expected, filter);
  }
  const html = table('unread');
  assert.match(text(rowOf(html, 'acc-004')), /^Fourth Co\. acc-004 Newest card Not permitted Server code: not_permitted$/);
  assert.match(rowOf(html, 'acc-004'), /data-refusal="not_permitted"/);
  assert.match(text(rowOf(html, 'acc-003')), /The server could not answer\. Server code: internal/);
  assert.match(html, /<caption>Deals whose cards could not be read<\/caption>/);
  assert.equal(text(table('card', rows.slice(2))), 'No deal is in this view.');
});

test('a row says No card, then that nothing is inferred, and a card row links its deal and what ran for its newest card', async () => {
  const html = render(await storeWith());
  assert.match(text(rowOf(html, 'acc-002')), /No card Nothing about its sources is inferred from that\./);
  assert.doesNotMatch(rowOf(html, 'acc-002'), /<a /);
  assert.match(rowOf(html, 'acc-001'), /<time dateTime="2027-01-15T15:00:04Z">15 Jan 2027, 15:00:04 UTC<\/time>/);
  assert.match(text(rowOf(html, 'acc-001')), /Cards 2/);
  const links = [...rowOf(html, 'acc-001').matchAll(/<a [^>]*href="([^"]+)"[^>]*>(.*?)<\/a>/g)].map(match => `${text(match[2] ?? '')} ${match[1]}`);
  assert.deepEqual(links, [`Open the card of First ${hostile} Co. /deals/acc-001`, `What ran for the card of First ${hostile} Co. /ops/activity?card=card-1b`]);
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /<caption>Every granted deal, with its newest card<\/caption>/);
  assert.match(html, /<th scope="col" aria-sort="ascending"><button type="button" class="cv-sort" data-sort="deal">Deal/);
  assert.equal((html.match(/aria-sort=/g) || []).length, 1);
});

test('no granted deal says so, and shows no table', async () => {
  const html = render(await storeWith({accounts: {accounts: []}}));
  assert.match(text(html), /No deal is granted to you\. A grant names the deals a person reads\./);
  assert.doesNotMatch(html, /<table|data-filter/);
});

test('each health answer shows its sentence and the status the server sent', async () => {
  const said = {
    ok: 'The database answers and its migrations are this binary’s.',
    migrations_behind: 'The database answers, but its schema is not this binary’s.',
    migrations_ahead: 'The database answers, but its schema is not this binary’s.',
    migrations_changed: 'The database answers, but its schema is not this binary’s.',
    database_unavailable: 'The database did not answer.',
    something_new: 'The server answered with a status this page does not know.',
  };
  for (const [status, sentence] of Object.entries(said)) {
    const html = render(await storeWith({health: {status}}));
    const line = html.slice(html.indexOf('<p class="cv-server"')).split('</p>')[0] ?? '';
    assert.match(line, new RegExp(`data-health="${status}"`));
    assert.equal(text(line), `Server ${sentence} ${status}`);
  }
  const failed = renderToStaticMarkup(createElement(coverage.ServerLine, {health: {status: 'failed', error: new client.ApiError({kind: 'unreachable'})}}));
  assert.equal(text(failed), 'Server The health check could not be read. The server did not answer.');
  const reading = renderToStaticMarkup(createElement(coverage.ServerLine, {health: {status: 'loading'}}));
  assert.equal(text(reading), 'Server Reading the server’s health.');
});
