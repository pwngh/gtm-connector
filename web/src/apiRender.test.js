import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {prerenderToNodeStream} from 'react-dom/static';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {fixturePeople, initialState, renewalFixture} from './renewalFixtures.js';

// The card is the connector demo's sealed acc-003 card from the oracle fixtures,
// shaped as GET /api/v1/cards/{card} returns it. Only IDs and times are made up.
const oracle = gunzipSync(readFileSync(new URL('../../testdata/oracle/cards/demo.jsonl.gz', import.meta.url))).toString().split('\n')
  .filter(Boolean).map(line => JSON.parse(line)).find(row => row.key === 'acc-003');
const sealed = JSON.parse(oracle.card);
const readId = tool => `receipt-${tool}`;
const card = {
  id: 'card-1', account: 'acc-003', brief_id: sealed.brief_id, kind: 'go', schema_version: 3, rules_version: '3',
  sha256: oracle.sha256, size: oracle.card.length, seal_receipt_id: 'receipt-seal', published_at: '2027-01-15T15:00:04.5Z',
  units: sealed.units.map(body => ({card: 'card-1', n: body.n, kind: body.kind, record_ref: body.record_ref, tool: body.tool,
    ...(body.kind === 'sentence' ? {shown: body.shown} : {}), receipt_id: readId(body.tool), record_version_id: `version-${body.n}`, body})),
  gaps: sealed.gaps,
};
const receipt = tool => ({id: readId(tool), operation_id: 'op#1', parent_id: 'receipt-brief', ordinal: 1, principal_id: 'p-1', token_id: 't', tool,
  scope: null, account: 'acc-003', connection_generation: 1, arguments: {}, request_id: null, brief_id: null, status: 'ok', http_status: 200,
  row_count: 2, rows: [], elapsed_ms: 1, error: null, started_at: '2027-01-15T15:00:00Z', finished_at: '2027-01-15T15:00:01.25Z', delivered_at: null});
const principal = {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};

let server, App, ServerContext, evidence, readiness, frame, walkthroughDays;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  ServerContext = (await server.ssrLoadModule('/src/ServerSwitch.jsx')).ServerContext;
  evidence = await server.ssrLoadModule('/src/ApiEvidence.jsx');
  readiness = await server.ssrLoadModule('/src/ApiReadiness.jsx');
  frame = await server.ssrLoadModule('/src/ApiFrame.jsx');
  walkthroughDays = renewalFixture.account.days;
});
after(async () => {await server?.close();});

function storeOf(state, settle = state) {
  let current = state;
  return {api: {}, subscribe: () => () => {}, getSnapshot: () => current, start: () => Promise.resolve().then(() => {current = settle; return current;})};
}
async function renderApp(path, store) {
  const {prelude} = await prerenderToNodeStream(React.createElement(ServerContext, {value: store},
    React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))));
  let html = '';
  for await (const chunk of prelude) html += chunk;
  return html;
}
const inRouter = (element, path = renewalFixture.paths.evidence) => renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [path]}, element));
const text = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&#x27;', '\'').replaceAll('&quot;', '"').replaceAll('&amp;', '&').replace(/\s+/g, ' ');

test('walkthrough mode: a static build keeps the walkthrough Evidence and Readiness, after the server question settles', async () => {
  const store = storeOf({mode: 'probing', session: null, error: null}, {mode: 'walkthrough', session: null, error: null});
  const page = await renderApp(renewalFixture.paths.evidence, store);
  assert.match(page, /class="[^"]*evidence-library/);
  assert.doesNotMatch(page, /api-frame/);
  const rd = await renderApp(renewalFixture.paths.root + '/readiness', storeOf({mode: 'walkthrough', session: null, error: null}));
  assert.match(rd, /class="rd-page /);
  assert.doesNotMatch(rd, /api-readiness/);
});

test('API mode: Evidence and Readiness read from the server and show no walkthrough evidence', async () => {
  const session = {signed_in: true, principal, csrf_token: 't', expires_at: '2027-01-15T23:00:00Z'};
  const store = storeOf({mode: 'probing', session: null, error: null}, {mode: 'api', session, error: null, ended: false});
  const page = await renderApp(renewalFixture.paths.evidence, store);
  assert.match(page, /class="plain-panel evidence-library api-evidence"/);
  assert.match(text(page), /Signed in as a demo principal Demo reviewer Demo principal · demo workspace/);
  assert.match(text(page), /Reading cards for acc-003/);
  assert.doesNotMatch(page, /class="evidence-flow|data-record-id|library-row/);
  const rd = await renderApp(renewalFixture.paths.root + '/readiness?account=acc-001', storeOf({mode: 'api', session, error: null, ended: false}));
  assert.match(rd, /class="rd-page api-readiness"/);
  assert.match(text(rd), /Reading cards for acc-001/);
  assert.doesNotMatch(rd, /data-check-result="(passed|failed)"/);
});

test('API mode: signed out, ended, and unreachable states never fall back to walkthrough data', async () => {
  const out = await renderApp(renewalFixture.paths.evidence, storeOf({mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: false}));
  assert.match(out, /name="login"/);
  assert.match(out, /type="password"/);
  assert.match(out, /autoComplete="current-password"|autocomplete="current-password"/);
  assert.doesNotMatch(out, /ended or was revoked|library-row/);
  const ended = await renderApp(renewalFixture.paths.evidence, storeOf({mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: true}));
  assert.match(text(ended), /Your session ended or was revoked. You are signed out./);
  const gone = await renderApp(renewalFixture.paths.evidence, storeOf({mode: 'unreachable', session: null, error: {kind: 'unreachable'}}));
  assert.match(text(gone), /The server did not answer. Nothing was read. No walkthrough data is shown in its place./);
  assert.doesNotMatch(gone, /library-row|name="login"/);
  const refused = await renderApp(renewalFixture.paths.root + '/readiness', storeOf({mode: 'api', session: null, error: {kind: 'server', code: 'internal'}}));
  assert.match(text(refused), /The server could not answer. .* Server code: internal/);
});

test('a card renders every unit with its number, exact text, speaker, role, basis and receipt, and its gaps by code', () => {
  const html = inRouter(React.createElement(evidence.CardEvidence, {card, receiptFor: id => ({status: 'ready', data: receipt(id.replace('receipt-', ''))}), params: new URLSearchParams('account=acc-003'), who: 'p-1'}));
  const units = [...html.matchAll(/data-unit-n="(\d+)"/g)].map(match => Number(match[1]));
  assert.deepEqual(units, [1, 2, 3, 4, 5, 6, 7, 8, 9]);
  const unit = n => text(html.slice(html.indexOf(`data-unit-n="${n}"`)).split('</article>')[0]);
  for (const body of sealed.units.filter(item => item.kind === 'sentence')) {
    assert.ok(unit(body.n).includes(body.text), `unit ${body.n} shows the server text exactly`);
    assert.ok(unit(body.n).includes(`Speaker ${body.speaker} Role ${body.role} Role basis ${body.basis}`));
    assert.ok(unit(body.n).includes('Customer decision authority Unidentified'));
  }
  assert.match(unit(1), /Record crm\/accounts\/acc-003 ?\. A record unit carries its reference, not field values\./);
  assert.match(unit(5), /Read by read_call_notes Status ok · HTTP 200 Started 15 Jan 2027, 15:00:00 UTC Finished 15 Jan 2027, 15:00:01 UTC Rows 2/);
  assert.match(unit(5), /Sentence unit · Call · 12 Jan 2027, 15:00:00 UTC/);
  assert.match(html, /href="\/renewals\/thornfield\/evidence\?account=acc-003&amp;unit=5"/);
  const gaps = [...html.matchAll(/data-gap-code="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(gaps, sealed.gaps.map(gap => gap.code));
  for (const gap of sealed.gaps) assert.ok(text(html).includes(gap.text));
  assert.doesNotMatch(html, /dangerouslySetInnerHTML|Partial excerpt|Not shown on the card/);
});

test('quotations stay data: markup is escaped, cut and unshown units are labelled', () => {
  const quoted = structuredClone(card.units[3]);
  quoted.body.text = '<img src=x onerror=alert(1)> [mention channel] said yes.';
  const cut = structuredClone(card.units[4]);
  cut.shown = 'cut'; cut.body.shown = 'cut'; cut.body.partial = true;
  const later = structuredClone(card.units[5]);
  later.shown = 'later'; later.body.shown = 'later';
  const html = inRouter(React.createElement(evidence.CardEvidence, {card: {...card, units: [quoted, cut, later]}, receiptFor: () => ({status: 'loading'}), params: new URLSearchParams(), who: 'p-1'}));
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt; [mention channel] said yes.'));
  assert.doesNotMatch(html, /<img/);
  assert.match(text(html), /Partial excerpt · cut, not a whole quotation Not shown on the card · Cut at a pause/);
  assert.match(text(html), /Not shown on the card · Dated after the card’s day/);
});

test('a refused receipt or card list says Not permitted, never an empty list', () => {
  const html = inRouter(React.createElement(evidence.UnitRow, {unit: card.units[4], receipt: {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}}, open: false, params: new URLSearchParams(), who: 'p-1', cardId: 'card-1'}));
  assert.match(text(html), /Read receipt Not permitted\. The signed-in principal holds no grant for this/);
  assert.match(html, /data-refusal="not_permitted"/);
  const selection = {account: 'acc-001', params: new URLSearchParams('account=acc-001'), cardId: null, listed: null, cards: {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}}};
  const list = inRouter(React.createElement(frame.CardList, {selection}));
  assert.match(list, /data-refusal="not_permitted"/);
  assert.doesNotMatch(text(list), /No card is published/);
  const empty = inRouter(React.createElement(frame.CardList, {selection: {...selection, listed: [], cards: {status: 'ready', data: {cards: []}}}}));
  assert.match(text(empty), /No card is published for acc-001\. Nothing about its sources is inferred from that\./);
});

test('Readiness from the card: its gaps as open items and every workflow rule not evaluated', () => {
  const html = inRouter(React.createElement(readiness.CardReadiness, {card, evidenceHref: '/renewals/thornfield/evidence?account=acc-003'}), renewalFixture.paths.root + '/readiness');
  const open = [...html.matchAll(/class="rd-requirement rd-paper" data-gap-code="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(open, sealed.gaps.map(gap => gap.code));
  const results = [...html.matchAll(/data-check-result="([^"]+)"/g)].map(match => match[1]);
  assert.equal(results.length, readiness.workflowRules.length);
  assert.ok(results.every(result => result === 'not_evaluated'));
  assert.doesNotMatch(html, /0 blockers|rd-result passed|rd-result failed/);
  assert.match(text(html), /CRM stage Not read here .* Requirement readiness Not evaluated 3 stated gaps open on this card\. .* Commercial outcome Not recorded/);
  assert.match(text(html), /9 numbered units: 6 sentences and 3 records\. 2 sentences by a speaker in the customer role\. 0 units not shown on the card, 0 units cut\./);
  assert.deepEqual(readiness.unitCounts(card.units), {total: 9, sentences: 6, records: 3, notShown: 0, partial: 0, customer: 2});
});

// Every walkthrough fact the renewal header, breadcrumb, sidebar or avatar shows.
const initials = name => name.split(' ').map(part => part[0]).slice(0, 2).join('');
const walkthroughHeader = () => [
  renewalFixture.account.name, renewalFixture.agreement.label, `OPPORTUNITY ${renewalFixture.renewal.id}`, initialState.plan.owner,
  `Renewal in ${walkthroughDays} days`, 'Customer condition open', 'Condition resolved', 'Resolution needs review', 'missing document',
  'View sources checked', 'Saved in this browser', 'Saving in this browser', 'Not saved in this browser', 'Append-only walkthrough',
  'Opened from your worklist', 'Return to worklist', 'Return to readiness', 'Return to history',
];
const returns = '?' + new URLSearchParams({returnTo: '/queue', readinessReturn: renewalFixture.paths.root + '/readiness', historyReturn: renewalFixture.paths.history});
const signedIn = {mode: 'api', session: {signed_in: true, principal, csrf_token: 't', expires_at: '2027-01-15T23:00:00Z'}, error: null, ended: false};
const apiStates = {
  'signed in': signedIn,
  'signed out': {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: false},
  ended: {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: true},
  unreachable: {mode: 'unreachable', session: null, error: {kind: 'unreachable'}},
};

test('the walkthrough header facts this check looks for are on the walkthrough pages', async () => {
  const html = text(await renderApp(renewalFixture.paths.evidence + returns, storeOf({mode: 'walkthrough', session: null, error: null})));
  for (const fact of [renewalFixture.account.name, renewalFixture.agreement.label, `OPPORTUNITY ${renewalFixture.renewal.id}`, initialState.plan.owner, 'Customer condition open', 'missing document', 'Saving in this browser', 'Opened from your worklist']) {
    assert.ok(html.includes(fact), fact);
  }
  assert.ok(html.includes('WORKSPACE'));
});

for (const [page, path] of [['evidence', renewalFixture.paths.evidence], ['readiness', renewalFixture.paths.root + '/readiness']]) {
  for (const [name, state] of Object.entries(apiStates)) test(`API mode, ${page}, ${name}: no walkthrough header fact appears`, async () => {
    const html = await renderApp(path + returns, storeOf(state));
    const shown = text(html);
    for (const fact of walkthroughHeader()) assert.ok(!shown.includes(fact), `walkthrough fact on screen: ${fact}`);
    assert.doesNotMatch(html, /class="nav-count"/);
    assert.doesNotMatch(html, />Evidence<span>\d/);
    assert.ok(!html.includes(`>${initials(fixturePeople.workspaceProfile)}<`), 'walkthrough profile initials');
    assert.match(html, /class="nav-label">WALKTHROUGH PAGES</);
    // What the API supports, and walkthrough pages marked as such.
    assert.match(html, /<h1>acc-003<span class="company-dot">/);
    assert.match(shown, /Renewal date, agreement and owner: not available from the server yet/);
    assert.match(shown, /Renewal acc-003/);
    // The header links only the pages that read from the server; the sidebar marks the rest.
    const tabs = [...html.slice(html.indexOf('aria-label="Renewal sections"')).split('</nav>')[0].matchAll(/<a [^>]*>([^<]+)<\/a>/g)].map(match => match[1]);
    assert.deepEqual(tabs, ['Readiness', 'Evidence']);
  });
}

test('the API header shows the open card, and Sources and gaps targets the gaps section on each page', () => {
  const selection = {account: 'acc-003', cardId: card.id, card: {status: 'ready', data: card}};
  for (const [page, View, props] of [['evidence', evidence.CardEvidence, {card, receiptFor: () => ({status: 'loading'}), params: new URLSearchParams(), who: 'p-1'}],
    ['readiness', readiness.CardReadiness, {card, evidenceHref: '/'}]]) {
    const header = inRouter(React.createElement(frame.ApiRenewalHeader, {page, selection}));
    assert.match(text(header), /Card card-1 · published 15 Jan 2027, 15:00:04 UTC/);
    const target = header.match(/data-target="([^"]+)"[^>]*>.*?Sources and gaps/)?.[1];
    assert.equal(target, frame.gapsTarget[page]);
    const body = inRouter(React.createElement(View, props));
    assert.match(body, new RegExp(`id="${target}" tabindex="-1"`, 'i'));
    assert.match(header, /href="[^"]*\?account=acc-003&amp;card=card-1"/);
  }
  const closed = inRouter(React.createElement(frame.ApiRenewalHeader, {page: 'readiness', selection: {account: 'acc-001', cardId: null, card: {status: 'idle'}}}));
  assert.match(text(closed), /No card open/);
  assert.doesNotMatch(closed, /Sources and gaps/);
});
