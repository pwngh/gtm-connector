import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {prerenderToNodeStream} from 'react-dom/static';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';

// The pages that read from the server must render no walkthrough fixture value
// at all. Every human-readable string in the fixture modules is replaced by a
// marker, every date moves to 1999 and every number above 9 gains 7,000,000,
// so any fixture value that reaches an API page shows up in its markup.
// Routes, identifiers and enum values stay as they are: the app runs on them.
const fixtureModules = ['/src/renewalFixtures.js', '/src/playbookTemplates.js'];
const leak = /⟦fx\d+⟧|1999-\d\d-\d\d|\b1999\b|7,?0\d\d,?\d\d\d/;

function sentinelPlugin() {
  return {
    name: 'fixture-sentinels', enforce: 'pre',
    transform(code, id) {
      if (!fixtureModules.some(path => id.endsWith(path))) return null;
      const names = [...code.matchAll(/^export const (\w+)/gm)].map(match => match[1]);
      const body = code.replace(/^export const /gm, 'const ');
      return body + `
const __seen = new Map();
function __mark(value) {
  if (typeof value === 'string') {
    if (!value || value.startsWith('/') || /^[a-z0-9_:.-]+$/.test(value)) return value;
    if (/^\\d{4}-\\d\\d-\\d\\d/.test(value)) return '1999' + value.slice(4);
    if (!__seen.has(value)) __seen.set(value, '⟦fx' + __seen.size + '⟧');
    return __seen.get(value);
  }
  if (typeof value === 'number') return Number.isInteger(value) && value > 9 ? 7000000 + value : value;
  if (Array.isArray(value)) return value.map(__mark);
  if (value && typeof value === 'object' && Object.getPrototypeOf(value) === Object.prototype) return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, key === 'paths' ? item : __mark(item)]));
  return value;
}
${names.map(name => `const __${name} = __mark(${name});\nexport {__${name} as ${name}};`).join('\n')}
`;
    },
  };
}

const oracle = JSON.parse(gunzipSync(readFileSync(new URL('../../testdata/oracle/cards/demo.jsonl.gz', import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => JSON.parse(line)).find(row => row.key === 'acc-003').card);
const card = {id: 'card-1', account: 'acc-003', brief_id: oracle.brief_id, kind: 'go', schema_version: 3, rules_version: '3', sha256: 'abc', size: 1,
  seal_receipt_id: 'receipt-seal', published_at: '2027-01-15T15:00:04Z',
  units: oracle.units.map(body => ({card: 'card-1', n: body.n, kind: body.kind, record_ref: body.record_ref, tool: body.tool, shown: body.shown, receipt_id: `r-${body.tool}`, body})),
  gaps: oracle.gaps};
const receipt = {id: 'r-1', operation_id: 'op', tool: 'read_call_notes', account: 'acc-003', status: 'ok', http_status: 200, row_count: 2, rows: ['calls/call_notes/note-003'],
  started_at: '2027-01-15T15:00:00Z', finished_at: '2027-01-15T15:00:01Z'};
const principal = {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const session = {signed_in: true, principal, csrf_token: 't', expires_at: '2027-01-15T23:00:00Z'};
const states = {
  'signed in': {mode: 'api', session, error: null, ended: false, generation: 1},
  'signed out': {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: false, generation: 1},
  ended: {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: true, generation: 2},
  unreachable: {mode: 'unreachable', session: null, error: {kind: 'unreachable'}, generation: 0},
};

let server, App, ServerContext, fixtures, evidence, readiness, frame;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [sentinelPlugin()]});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  ServerContext = (await server.ssrLoadModule('/src/ServerSwitch.jsx')).ServerContext;
  fixtures = await server.ssrLoadModule('/src/renewalFixtures.js');
  evidence = await server.ssrLoadModule('/src/ApiEvidence.jsx');
  readiness = await server.ssrLoadModule('/src/ApiReadiness.jsx');
  frame = await server.ssrLoadModule('/src/ApiFrame.jsx');
});
after(async () => {await server?.close();});

const storeOf = state => ({api: {}, subscribe: () => () => {}, getSnapshot: () => state, start: () => Promise.resolve(state)});
async function renderApp(path, state) {
  const {prelude} = await prerenderToNodeStream(React.createElement(ServerContext, {value: storeOf(state)},
    React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))));
  let html = '';
  for await (const chunk of prelude) html += chunk;
  return html;
}
const found = html => html.match(leak)?.[0];

test('the markers are live: the walkthrough pages render fixture values', async () => {
  assert.match(fixtures.renewalFixture.account.name, /^⟦fx\d+⟧$/);
  const html = await renderApp(fixtures.renewalFixture.paths.evidence, {mode: 'walkthrough', session: null, error: null});
  assert.ok(found(html), 'the walkthrough Evidence page shows marked fixture values');
});

for (const [page, path] of [['evidence', '/renewals/thornfield/evidence'], ['readiness', '/renewals/thornfield/readiness']]) {
  for (const [name, state] of Object.entries(states)) test(`API ${page}, ${name}: no fixture value anywhere on the page`, async () => {
    const query = '?' + new URLSearchParams({account: 'acc-003', returnTo: '/queue', readinessReturn: '/renewals/thornfield/readiness', historyReturn: '/renewals/thornfield/history'});
    const html = await renderApp(path + query, state);
    assert.equal(found(html), undefined, `fixture value on the API ${page} page: ${found(html)}`);
  });
}

const params = new URLSearchParams('account=acc-003&card=card-1&unit=5');
const selectionOf = () => ({params, accounts: {status: 'ready', data: {accounts: [{account: 'acc-003', name: 'Third'}]}}, granted: [{account: 'acc-003', name: 'Third'}],
  account: 'acc-003', verified: true, invalid: false, accountName: 'Third', cards: {status: 'ready', data: {cards: [card]}}, listed: [card], cardId: 'card-1',
  cardMismatch: false, card: {status: 'ready', data: card}, generation: 1});
const wrap = element => renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: ['/renewals/thornfield/evidence?account=acc-003']}, element));
const views = {
  'renewal header, Evidence': () => React.createElement(frame.ApiRenewalHeader, {page: 'evidence', selection: selectionOf()}),
  'renewal header, Readiness': () => React.createElement(frame.ApiRenewalHeader, {page: 'readiness', selection: selectionOf()}),
  'card evidence': () => React.createElement(evidence.CardEvidence, {card, receiptFor: () => ({status: 'ready', data: receipt}), params, generation: 1}),
  'unit in full': () => React.createElement(evidence.UnitDetailView, {n: 5, cardId: 'card-1', params, unit: {status: 'ready', data: {...card.units[4], receipt}}}),
  'card readiness': () => React.createElement(readiness.CardReadiness, {card, evidenceHref: '/'}),
  'readiness from the server': () => React.createElement(readiness.ReadinessFromServer, {selection: selectionOf()}),
};
for (const [name, view] of Object.entries(views)) test(`API view, ${name}: no fixture value`, () => {
  const html = wrap(view());
  assert.equal(found(html), undefined, `fixture value: ${found(html)}`);
});
