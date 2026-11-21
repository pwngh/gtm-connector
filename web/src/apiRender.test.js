import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {prerenderToNodeStream} from 'react-dom/static';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {serverPaths} from './app/routes.js';

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

let server, ServerShell, ServerPalette, ServerContext, boot, RouteError, CommandPalette, evidence, readiness, frame;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  ({ServerShell, ServerPalette} = await server.ssrLoadModule('/src/app/ServerApp.jsx'));
  ({boot} = await server.ssrLoadModule('/src/app/boot.jsx'));
  ({RouteError} = await server.ssrLoadModule('/src/RouteContent.jsx'));
  CommandPalette = (await server.ssrLoadModule('/src/CommandPalette.jsx')).default;
  ServerContext = (await server.ssrLoadModule('/src/ServerSwitch.jsx')).ServerContext;
  evidence = await server.ssrLoadModule('/src/ApiEvidence.jsx');
  readiness = await server.ssrLoadModule('/src/ApiReadiness.jsx');
  frame = await server.ssrLoadModule('/src/ApiFrame.jsx');
});
after(async () => {await server?.close();});

function storeOf(state, settle = state) {
  let current = state;
  return {api: {}, subscribe: () => () => {}, getSnapshot: () => current, start: () => Promise.resolve().then(() => {current = settle; return current;})};
}
async function renderServer(path, store) {
  const {prelude} = await prerenderToNodeStream(React.createElement(ServerContext, {value: store},
    React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(ServerShell))));
  let html = '';
  for await (const chunk of prelude) html += chunk;
  return html;
}
const inRouter = (element, path = serverPaths.evidence) => renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [path]}, element));
const text = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&#x27;', '\'').replaceAll('&quot;', '"').replaceAll('&amp;', '&').replace(/\s+/g, ' ');

test('API mode: Evidence and Readiness read from the server', async () => {
  const session = {signed_in: true, principal, csrf_token: 't', expires_at: '2027-01-15T23:00:00Z'};
  const page = await renderServer(serverPaths.evidence, storeOf({mode: 'api', session, error: null, ended: false}));
  assert.match(page, /class="plain-panel evidence-library api-evidence"/);
  assert.match(text(page), /Signed in as a demo principal Demo reviewer Demo principal · demo workspace/);
  // Without ?account= the page waits for the principal's accounts before it reads any cards.
  assert.match(text(page), /Reading your accounts…/);
  assert.doesNotMatch(text(page), /Reading cards for/);
  assert.doesNotMatch(page, /class="evidence-flow|data-record-id|library-row/);
  const rd = await renderServer(serverPaths.readiness + '?account=acc-001', storeOf({mode: 'api', session, error: null, ended: false}));
  assert.match(rd, /class="rd-page api-readiness"/);
  assert.match(text(rd), /Reading your accounts… Reading cards for acc-001/);
  assert.doesNotMatch(rd, /data-check-result="(passed|failed)"/);
});

test('API mode: signed out, ended, and unreachable states show no evidence', async () => {
  const out = await renderServer(serverPaths.evidence, storeOf({mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: false}));
  assert.match(out, /name="login"/);
  assert.match(out, /type="password"/);
  assert.match(out, /autoComplete="current-password"|autocomplete="current-password"/);
  assert.doesNotMatch(out, /ended or was revoked|library-row/);
  const ended = await renderServer(serverPaths.evidence, storeOf({mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: true}));
  assert.match(text(ended), /Your session ended or was revoked. You are signed out./);
  const gone = await renderServer(serverPaths.evidence, storeOf({mode: 'unreachable', session: null, error: {kind: 'unreachable'}}));
  assert.match(text(gone), /The server did not answer. Nothing was read, and nothing is shown in its place./);
  assert.doesNotMatch(gone, /library-row|name="login"/);
  const refused = await renderServer(serverPaths.readiness, storeOf({mode: 'api', session: null, error: {kind: 'server', code: 'internal'}}));
  assert.match(text(refused), /The server could not answer. .* Server code: internal/);
});

test('a card renders every unit with its number, exact text, speaker, role, basis and receipt, and its gaps by code', () => {
  const html = inRouter(React.createElement(evidence.CardEvidence, {card, receiptFor: id => ({status: 'ready', data: receipt(id.replace('receipt-', ''))}), params: new URLSearchParams('account=acc-003'), generation: 1}));
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
  assert.match(html, /href="\/ops\/evidence\?account=acc-003&amp;card=card-1&amp;unit=5"/);
  const gaps = [...html.matchAll(/data-gap-code="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(gaps, sealed.gaps.map(gap => gap.code));
  for (const gap of sealed.gaps) assert.ok(text(html).includes(gap.text));
  assert.doesNotMatch(html, /Partial excerpt|Not shown on the card/);
});

test('quotations stay data: markup is escaped, cut and unshown units are labelled', () => {
  const quoted = structuredClone(card.units[3]);
  quoted.body.text = '<img src=x onerror=alert(1)> [mention channel] said yes.';
  const cut = structuredClone(card.units[4]);
  cut.shown = 'cut'; cut.body.shown = 'cut'; cut.body.partial = true;
  const later = structuredClone(card.units[5]);
  later.shown = 'later'; later.body.shown = 'later';
  const html = inRouter(React.createElement(evidence.CardEvidence, {card: {...card, units: [quoted, cut, later]}, receiptFor: () => ({status: 'loading'}), params: new URLSearchParams(), generation: 1}));
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt; [mention channel] said yes.'));
  assert.doesNotMatch(html, /<img/);
  assert.match(text(html), /Partial excerpt · cut, not a whole quotation Not shown on the card · Cut at a pause/);
  assert.match(text(html), /Not shown on the card · Dated after the card’s day/);
});

test('a refused receipt or card list says Not permitted, never an empty list', () => {
  const html = inRouter(React.createElement(evidence.UnitRow, {unit: card.units[4], receipt: {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}}, open: false, params: new URLSearchParams(), account: 'acc-003', cardId: 'card-1'}));
  assert.match(text(html), /Read receipt Not permitted\. The signed-in principal holds no grant for this/);
  assert.match(html, /data-refusal="not_permitted"/);
  const selection = {account: 'acc-001', params: new URLSearchParams('account=acc-001'), cardId: null, listed: null, cards: {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}}};
  const list = inRouter(React.createElement(frame.CardList, {selection}));
  assert.match(list, /data-refusal="not_permitted"/);
  assert.doesNotMatch(text(list), /No card is published/);
  const empty = inRouter(React.createElement(frame.CardList, {selection: {...selection, listed: [], cards: {status: 'ready', data: {cards: []}}}}));
  assert.match(text(empty), /No card is published for acc-001\. Nothing about its sources is inferred from that\./);
});

test('Readiness from the card: unread or unshown gaps as open items, notes apart, and no rule evaluated', () => {
  const html = inRouter(React.createElement(readiness.CardReadiness, {card, evidenceHref: '/ops/evidence?account=acc-003'}), serverPaths.readiness);
  const open = [...html.matchAll(/data-open-gap="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(open, sealed.gaps.filter(gap => gap.disposition !== 'note').map(gap => gap.code));
  const results = [...html.matchAll(/data-check-result="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(results, ['not_evaluated']);
  assert.doesNotMatch(html, /0 blockers|rd-result passed|rd-result failed/);
  assert.match(text(html), /CRM stage Not read here .* Requirement readiness Not evaluated 2 open items from the card’s gaps\. .* Commercial outcome Not read here/);
  assert.match(text(html), /9 numbered units: 6 sentences and 3 records\. 2 sentences by a speaker in the customer role\. 0 units not shown on the card, 0 units cut\./);
  assert.deepEqual(readiness.unitCounts(card.units), {total: 9, sentences: 6, records: 3, notShown: 0, partial: 0, customer: 2});
});

// What the shell shows on a server page.
const returns = '?account=acc-003';
const signedIn = {mode: 'api', session: {signed_in: true, principal, csrf_token: 't', expires_at: '2099-01-15T23:00:00Z'}, error: null, ended: false};
const apiStates = {
  'signed in': signedIn,
  'signed out': {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: false},
  ended: {mode: 'api', session: {signed_in: false, principal: null, csrf_token: 'p'}, error: null, ended: true},
  unreachable: {mode: 'unreachable', session: null, error: {kind: 'unreachable'}},
};

for (const [page, path] of [['evidence', serverPaths.evidence], ['readiness', serverPaths.readiness]]) {
  for (const [name, state] of Object.entries(apiStates)) test(`API mode, ${page}, ${name}: the shell shows only what the server supports`, async () => {
    const html = await renderServer(path + returns, storeOf(state));
    const shown = text(html);
    assert.deepEqual(shell(html), serverShell(state, 'Renewal Read from the server', '?account=acc-003'));
    // The account from the link is not verified until the account list is read.
    assert.match(html, /<h1>Account not verified<span class="company-dot">/);
    assert.match(shown, /Account ID acc-003 ?, not verified/);
    assert.match(shown, /Renewal date, agreement and owner: not available from the server yet/);
    assert.match(shown, /Renewal Read from the server/);
    const tabs = [...html.slice(html.indexOf('aria-label="Renewal sections"')).split('</nav>')[0].matchAll(/<a [^>]*>([^<]+)<\/a>/g)].map(match => match[1]);
    assert.deepEqual(tabs, ['Readiness', 'Evidence']);
  });
}

// Paths the server sends to the app that are not a server page.
const otherPaths = ['/deals', '/deals/acc-003', '/ops/nothing', '/ops/workplan', '/ops/history', '/ops/evidence/extra'];
// What the navigation, the header and the footer offer.
function shell(html) {
  const between = (open, close) => html.slice(html.indexOf(open)).split(close)[0];
  const sidebar = between('id="workspace-navigation"', '</aside>');
  const links = [...between('<nav aria-label="Workspace"', '</nav>').matchAll(/<a [^>]*href="([^"]+)"[^>]*>.*?<span>([^<]+)<\/span>/g)].map(match => [match[2], match[1].replaceAll('&amp;', '&')]);
  const brand = sidebar.match(/<a [^>]*class="brand[^"]*"[^>]*>[\s\S]*?<\/a>/)[0];
  return {label: sidebar.match(/class="nav-label">([^<]*)</)[1], links, brand: brand.match(/href="([^"]+)"/)[1], name: brand.replace(/<[^>]+>/g, ''),
    beforeNav: text(sidebar.slice(sidebar.indexOf(brand) + brand.length).split('<nav')[0]).trim(),
    belowNav: text(sidebar.split('</nav>')[1]).trim(), crumbs: text(between('class="breadcrumbs', '</div>').split('</button>')[1]).trim(),
    profile: between('class="workspace-toolbar"', '</header>').match(/class="profile"[^>]*aria-label="([^"]+)"/)[1], footer: html.match(/<span>([^<]*)<\/span><\/footer>/)[1],
    site: text(between('<footer', '</footer>').replace(/^[^>]*>/, '')).trim()};
}
// The whole of it when the server serves the app: its own name, the two server pages and the signed-in principal.
const serverShell = (state, crumbs, query = '') => ({label: 'READ FROM THE SERVER', brand: serverPaths.evidence, name: 'gtm-connector', beforeNav: 'READ FROM THE SERVER',
  belowNav: '', crumbs, footer: 'Read from the server', site: 'gtm-connector Read from the server',
  links: [['Evidence', serverPaths.evidence + query], ['Readiness', serverPaths.readiness + query]],
  profile: state.session?.signed_in ? `Signed in as ${principal.display_name}` : 'Not signed in'});

for (const path of otherPaths) {
  for (const [name, state] of Object.entries(apiStates)) test(`API mode, ${path}, ${name}: no page at this address, with one link home`, async () => {
    const html = await renderServer(path, storeOf(state));
    const main = html.slice(html.indexOf('<main'));
    assert.match(main, /<h1>There is no page at this address<span class="company-dot">\.<\/span><\/h1>/);
    assert.match(text(main), name === 'unreachable' ? /The server did not answer, so nothing is shown\. Reload the page to ask it again\./ : /The server provides Evidence and Readiness\. Check the address, or start again from Evidence\./);
    assert.deepEqual([...main.matchAll(/<a [^>]*href="([^"]*)"[^>]*>([^<]*)<\/a>/g)].map(match => [match[1], match[2]]), [[serverPaths.evidence, 'Go to Evidence']]);
    assert.doesNotMatch(html, /api-frame|name="login"|api-renewal-header/);
    assert.doesNotMatch(text(html), /Revenue Workflow Lab|prototype|Demo workspace|Invented|walkthrough|Work queue|Playbooks|Operations|Work plan|History/i);
    assert.deepEqual(shell(html), serverShell(state, 'No page here'));
  });
}

test('API mode: a path that only resembles a server page has no page either', async () => {
  for (const path of [serverPaths.evidence + '/extra', '/deals/evidence', serverPaths.readiness + '/']) {
    const html = await renderServer(path, storeOf(signedIn));
    assert.match(html, /<h1>There is no page at this address</, path);
    assert.doesNotMatch(html, /api-frame/, path);
  }
});

// The entry asks the server once. Until it answers, only the loading state
// shows; then the server's app mounts, or, for a static host, a sentence says
// the page needs its server.
function booting(mode) {
  let answer;
  const store = {start: () => new Promise(resolve => {answer = () => resolve({mode});})};
  const shown = [];
  const done = boot({store, show: view => shown.push(view)});
  return {store, shown, done, answer: () => answer()};
}
for (const mode of ['api', 'unreachable', 'static']) test(`the entry shows only the loading state while it asks, then what fits ${mode}`, async () => {
  const run = booting(mode);
  assert.equal(run.shown.length, 1);
  const html = renderToStaticMarkup(run.shown[0]);
  assert.match(html, /^<div class="route-loading" role="status"[^>]*><p>Loading page…<\/p><\/div>$/);
  run.answer();
  assert.equal(await run.done, mode === 'static' ? 'static' : 'server');
  assert.equal(run.shown.length, 2);
  if (mode === 'static') assert.equal(text(renderToStaticMarkup(run.shown[1])).trim(), 'This page needs its server. Open the address your gtm-connector server prints.');
  else assert.equal(run.shown[1].props.store, run.store);
});

test('the server app names itself, and its palette and page failure use its own words', () => {
  const palette = renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [serverPaths.evidence + '?account=acc-003']},
    React.createElement(ServerContext, {value: storeOf(signedIn)}, React.createElement(ServerPalette, {onClose() {}, opener: {current: null}}))));
  assert.match(palette, /<h2 id="[^"]+">Go to<\/h2>/);
  assert.match(palette, /aria-label="Find a page"[^>]*placeholder="Find a page"/);
  assert.deepEqual([...palette.matchAll(/<strong>([^<]+)<\/strong>/g)].map(match => match[1]), ['Evidence', 'Readiness']);
  assert.doesNotMatch(text(palette), /record/i);
  const empty = renderToStaticMarkup(React.createElement(CommandPalette, {items: [], placeholder: 'Find a page', inputLabel: 'Find a page', empty: React.createElement('h3', null, 'No page matches.')}));
  assert.match(text(empty), /No page matches\./);
  const failed = new RouteError({children: null});
  Object.assign(failed.state, RouteError.getDerivedStateFromError(new Error('offline')));
  assert.match(text(renderToStaticMarkup(failed.render())), /^ ?This page could not load\. Nothing was lost\. Try again ?$/);
});

test('the API header shows the open card, and Sources and gaps targets the gaps section on each page', () => {
  const selection = {account: 'acc-003', verified: true, cardId: card.id, card: {status: 'ready', data: card}};
  for (const [page, View, props] of [['evidence', evidence.CardEvidence, {card, receiptFor: () => ({status: 'loading'}), params: new URLSearchParams(), generation: 1}],
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
  const named = inRouter(React.createElement(frame.ApiRenewalHeader, {page: 'evidence', selection: {account: 'acc-002', verified: true, accountName: 'Named by the server', cardId: null, card: {status: 'idle'}}}));
  assert.match(named, /<h1>Named by the server<span/);
  assert.match(text(named), /Account ID acc-002/);
  const none = inRouter(React.createElement(frame.ApiRenewalHeader, {page: 'evidence', selection: {account: null, cardId: null, card: {status: 'idle'}}}));
  assert.match(none, /<h1>No account chosen<span/);
  assert.match(none, /href="\/ops\/evidence"/);
});

// The picker's states, from what GET /api/v1/accounts returns.
const pickerOf = (accounts, account) => ({account, params: new URLSearchParams(account ? {account} : {}), accounts, granted: accounts.status === 'ready' ? accounts.data.accounts : null});
test('the account picker lists the granted accounts by name and ID, with the chosen one selected', () => {
  const granted = {status: 'ready', data: {accounts: [{account: 'acc-002', name: 'Second <b>account</b>'}, {account: 'acc-003', name: 'Third account'}, {account: 'acc-004', name: ''}]}};
  const html = inRouter(React.createElement(frame.AccountPicker, {selection: pickerOf(granted, 'acc-003')}));
  const options = [...html.matchAll(/<option value="([^"]+)"( selected="")?>([^<]*)<\/option>/g)].map(match => [match[1], Boolean(match[2]), match[3]]);
  assert.deepEqual(options, [['acc-002', false, 'Second &lt;b&gt;account&lt;/b&gt; · acc-002'], ['acc-003', true, 'Third account · acc-003'], ['acc-004', false, 'acc-004']]);
  assert.match(html, /<label class="field" for="[^"]+">Account<select/);
  assert.match(text(html), /3 accounts granted to you/);
  // An account in the URL that is not granted stays visible as such; the server's refusal of it shows with the cards.
  const outside = inRouter(React.createElement(frame.AccountPicker, {selection: pickerOf(granted, 'acc-001')}));
  assert.match(outside, /<option value="acc-001" selected="">acc-001 · not in your accounts<\/option>/);
});

test('an empty account list says so, never an empty picker; a refused list shows the refusal', () => {
  const empty = inRouter(React.createElement(frame.AccountPicker, {selection: pickerOf({status: 'ready', data: {accounts: []}}, null)}));
  assert.match(text(empty), /^ ?No accounts are granted to you\. ?$/);
  assert.doesNotMatch(empty, /<select|<option/);
  const cards = inRouter(React.createElement(frame.CardList, {selection: {...pickerOf({status: 'ready', data: {accounts: []}}, 'acc-003'), who: 'p-1', cards: {status: 'idle'}, listed: null}}));
  assert.equal(cards, '');
  const refused = inRouter(React.createElement(frame.AccountPicker, {selection: pickerOf({status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}}, null)}));
  assert.match(refused, /data-refusal="not_permitted"/);
  assert.doesNotMatch(refused, /<select|No accounts are granted/);
  const loading = inRouter(React.createElement(frame.AccountPicker, {selection: pickerOf({status: 'loading'}, null)}));
  assert.match(text(loading), /Reading your accounts…/);
});
