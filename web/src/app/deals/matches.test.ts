import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer, type ViteDevServer} from 'vite';
import type {MatchView} from '../../api/types.ts';

// The rows of the matches page, rendered from what GET /v1/matches sends.

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let page: any, client: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  page = await server.ssrLoadModule('/src/app/deals/Matches.tsx');
  client = await server.ssrLoadModule('/src/api/client.ts');
});
after(async () => {
  await server?.close();
});

const hostile = '<img src=x onerror=alert(1)>';
const match = (id: string, over: Partial<MatchView> = {}): MatchView => ({
  id,
  kind: 'call',
  item: 'note-1',
  account: 'acc-007',
  account_name: 'Juniper Freight',
  at: null,
  candidates: ['juniper-2026-aaaaaa', 'juniper-2027-aaaaaa'],
  state: 'waiting',
  deal: null,
  basis: 'time_unknown',
  decided_by: null,
  decided_at: '2027-01-15T15:00:00Z',
  supersedes: null,
  receipt: 'r-1',
  superseded: false,
  stale: false,
  ...over,
});
const rows = (matches: MatchView[], state = {}) => renderToStaticMarkup(createElement(page.MatchRows, {matches, rows: state, decide: () => undefined}));

test('a waiting match says what it is, whose, when and why, and offers each deal it fits and to leave it off', () => {
  const html = rows([
    match('m-1'),
    match('m-2', {kind: 'thread', account_name: `Hostile ${hostile}`, at: '2027-01-12T15:00:00Z', basis: 'deal_closed', candidates: ['hostile-2027-bbbbbb']}),
  ]);
  const [first = '', second = ''] = html.split('data-match="m-2"');
  assert.match(first, /<strong[^>]*>Call<\/strong>/);
  assert.match(first, /Juniper Freight/);
  assert.match(first, /No time recorded/);
  assert.match(first, /fits more than one deal of this account/);
  assert.match(first, /Add to juniper-2026-aaaaaa/);
  assert.match(first, /Add to juniper-2027-aaaaaa/);
  assert.equal(first.split('Leave off').length - 1, 1);
  assert.match(second, /<strong[^>]*>Slack thread<\/strong>/);
  assert.match(second, /<time[^>]*datetime="2027-01-12T15:00:00Z"/i);
  assert.match(second, /The deal it was on is closed/);
  assert.equal(second.split('Add to ').length - 1, 1);
  // A name from the server is text, never markup.
  assert.ok(!html.includes(hostile));
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'));
  // No word of the call or thread, and nothing of the item's own ID.
  assert.ok(!html.includes('note-1'));
});

test('with nothing waiting the page says so, and a refused decision shows on its own row with the server’s code', () => {
  assert.match(rows([]), /Nothing waits\./);
  const refused = new client.ApiError({status: 409, kind: 'unexpected', code: 'match_decided'});
  const html = rows([match('m-1'), match('m-2')], {'m-2': {pending: null, error: refused}});
  const [first = '', second = ''] = html.split('data-match="m-2"');
  assert.ok(!first.includes('match_decided'));
  assert.match(second, /Server code: match_decided/);
});

test('while a decision is sent, its row’s buttons do not take another', () => {
  const html = rows([match('m-1')], {'m-1': {pending: 'juniper-2027-aaaaaa', error: null}});
  assert.equal(html.split('disabled=""').length - 1, 3);
});
