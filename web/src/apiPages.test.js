import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync, readdirSync} from 'node:fs';
import {join, relative} from 'node:path';
import {fileURLToPath} from 'node:url';
import {gunzipSync} from 'node:zlib';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {serverPaths} from './app/routes.ts';

// Cards are the connector demo's sealed cards from the oracle fixtures, shaped
// as GET /api/v1/cards/{card} returns them. Only IDs and times are made up.
const oracle = Object.fromEntries(gunzipSync(readFileSync(new URL('../../testdata/oracle/cards/demo.jsonl.gz', import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => JSON.parse(line)).map(row => [row.key, row]));
function cardOf(account, id = `card-${account}`) {
  const sealed = JSON.parse(oracle[account].card);
  return {id, account, brief_id: sealed.brief_id, kind: 'go', schema_version: 3, rules_version: '3', sha256: oracle[account].sha256,
    size: oracle[account].card.length, seal_receipt_id: 'receipt-seal', published_at: '2027-01-15T15:00:04Z',
    units: sealed.units.map(body => ({card: id, n: body.n, kind: body.kind, record_ref: body.record_ref, tool: body.tool,
      ...(body.kind === 'sentence' ? {shown: body.shown} : {}), receipt_id: `receipt-${body.tool}`, record_version_id: `version-${body.n}`, body})),
    gaps: sealed.gaps};
}
const notPermitted = {status: 'failed', error: {kind: 'not_permitted', code: 'not_permitted'}};
const signedInState = {mode: 'api', error: null, ended: false, generation: 1, elsewhere: null, session: {signed_in: true, csrf_token: 't', expires_at: '2099-01-15T23:00:00Z',
  principal: {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true}}};
const ready = data => ({status: 'ready', data});

let server, frame, evidence, readiness, selection, pages, shell, reads;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  // Each module loads on its own, so a missing one fails only the tests that use it.
  const load = path => server.ssrLoadModule(path).catch(error => new Proxy({}, {get(target, name) {if (name === 'then') return undefined; throw error;}}));
  [frame, evidence, readiness, selection, pages, shell, reads] = await Promise.all(['/src/ApiFrame.jsx', '/src/ApiEvidence.jsx', '/src/ApiReadiness.jsx', '/src/api/selection.ts',
    '/src/app/frame/pages.ts', '/src/app/frame/Frame.tsx', '/src/api/reads.ts'].map(load));
});
after(async () => {await server?.close();});

const inRouter = (element, path = serverPaths.evidence) => renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [path]}, element));
const text = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&#x27;', '\'').replaceAll('&quot;', '"').replaceAll('&amp;', '&').replace(/\s+/g, ' ');
// A selection as useCardSelection gives it, from read results.
function chosen({account = 'acc-003', verified = true, invalid = false, accountName = null, cards = null, cardId = null, cardMismatch = false, card = {status: 'idle'}, granted} = {}) {
  const listed = cards?.status === 'ready' ? cards.data.cards : null;
  granted ??= [{account: 'acc-002', name: 'Second'}, {account: 'acc-003', name: 'Third'}];
  return {params: new URLSearchParams({account: account || ''}), accounts: ready({accounts: granted}), granted, account, verified, invalid, accountName,
    cards: cards || {status: 'loading'}, listed, cardId, cardMismatch, card, generation: 1};
}

// 1. A refusal is never an empty result: the page paths the earlier tests missed.
test('a refused card on Evidence shows the refusal, never an empty card', () => {
  const card = cardOf('acc-003');
  const html = inRouter(React.createElement(evidence.EvidenceFromServer, {selection: chosen({cards: ready({cards: [card]}), cardId: card.id, card: notPermitted})}));
  assert.match(html, /data-refusal="not_permitted"/);
  assert.doesNotMatch(text(html), /numbered unit|has no unit|states no gaps|No card is published/);
});

test('a refused card on Readiness shows the refusal, never zero gaps or units', () => {
  const card = cardOf('acc-003');
  const html = inRouter(React.createElement(readiness.ReadinessFromServer, {selection: chosen({cards: ready({cards: [card]}), cardId: card.id, card: notPermitted})}), serverPaths.readiness);
  assert.match(html, /data-refusal="not_permitted"/);
  assert.doesNotMatch(text(html), /states no gaps|numbered unit|open item|0 units/);
});

test('a refused unit read shows the refusal, never "no receipt"', () => {
  const html = inRouter(React.createElement(evidence.UnitDetailView, {n: 5, cardId: 'card-1', params: new URLSearchParams('account=acc-003&card=card-1&unit=5'), unit: notPermitted}));
  assert.match(html, /data-refusal="not_permitted"/);
  assert.doesNotMatch(text(html), /No receipt is bound/);
});

// 2. A card shows only under its own account.
test('a card in the URL that is not in the account list is not read or shown', () => {
  const two = cardOf('acc-002', 'card-two'), three = cardOf('acc-003', 'card-three');
  const html = inRouter(React.createElement(evidence.EvidenceFromServer, {selection: chosen({account: 'acc-002', accountName: 'Second', cards: ready({cards: [two]}), cardMismatch: true, card: {status: 'idle'}})}));
  assert.match(text(html), /The card in this link is not one of acc-002’s published cards\. Nothing from it is shown\./);
  assert.doesNotMatch(html, /data-unit-n|card-three/);
  // A card the server returns for another account is refused here too.
  const checked = selection.checkCard(ready(three), 'acc-002', 'card-three');
  const shown = inRouter(React.createElement(evidence.EvidenceFromServer, {selection: chosen({account: 'acc-002', cards: ready({cards: [two]}), cardId: 'card-three', card: checked})}));
  assert.match(shown, /data-refusal="mismatch"/);
  assert.doesNotMatch(shown, /data-unit-n/);
});

test('unit links pin the open card, so a later publication cannot change what a link opens', () => {
  const card = cardOf('acc-003');
  const html = inRouter(React.createElement(evidence.CardEvidence, {card, receiptFor: () => ({status: 'loading'}), params: new URLSearchParams('account=acc-003'), generation: 1}));
  assert.match(html, /href="\/ops\/evidence\?account=acc-003&amp;card=card-acc-003&amp;unit=5"/);
});

// Minor: the URL's account is shown only from the granted list, or marked unverified.
test('an account from the URL is never the heading unless it is granted, and a malformed one is not shown', () => {
  const header = props => inRouter(React.createElement(frame.ApiRenewalHeader, {page: 'evidence', selection: chosen(props)}));
  const forged = header({account: 'acc-777', verified: false});
  assert.match(forged, /<h1>Account not verified<span/);
  assert.match(text(forged), /Account ID acc-777, not verified/);
  const phishing = header({account: null, verified: false, invalid: true});
  assert.match(phishing, /<h1>No account chosen<span/);
  assert.match(text(phishing), /The account in this link is not an account ID\. It was not read\./);
  assert.match(header({account: 'acc-003', verified: true, accountName: 'Third'}), /<h1>Third<span/);
  // The frame names no account: the link's text is not trusted, and the page heading shows the verified one.
  const store = {api: {}, subscribe: () => () => {}, getSnapshot: () => signedInState};
  const framed = renderToStaticMarkup(React.createElement(reads.ServerContext, {value: store},
    React.createElement(MemoryRouter, {initialEntries: [serverPaths.evidence + '?account=Your%20account%20is%20locked']}, React.createElement(shell.Frame))));
  const chrome = framed.slice(0, framed.indexOf('<main'));
  assert.doesNotMatch(chrome.replace(/href="[^"]*"/g, ''), /locked/);
});

// 4. Readiness claims only what the card supports.
test('Readiness evaluates no rule, counts only unread or unshown gaps as open, and scopes its claims', () => {
  const card = cardOf('acc-003');
  const html = inRouter(React.createElement(readiness.CardReadiness, {card, evidenceHref: '/'}), serverPaths.readiness);
  assert.doesNotMatch(text(html), /\d+ blockers?\b|\d+ transition checks?/);
  assert.match(text(html), /Not evaluated/);
  const open = [...html.matchAll(/data-open-gap="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(open, ['calls_outside_records', 'slack_channel']);
  const notes = [...html.matchAll(/data-note-gap="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(notes, ['neutralized']);
  const note = html.slice(html.indexOf('data-note-gap="neutralized"')).split('</li>')[0];
  assert.doesNotMatch(note, /Open|What closes it/);
  assert.doesNotMatch(text(html), /No requirement is recorded on the server|Commercial outcome Not recorded/);
  assert.match(text(html), /Commercial outcome Not read here/);
  assert.match(text(html), /This page reads no requirement records/);
  // A card whose only gaps are notes has no open items, and says so without claiming nothing is open.
  const quiet = {...card, gaps: card.gaps.filter(gap => gap.disposition === 'note')};
  const calm = inRouter(React.createElement(readiness.CardReadiness, {card: quiet, evidenceHref: '/'}), serverPaths.readiness);
  assert.doesNotMatch(calm, /data-open-gap/);
  assert.match(text(calm), /The card states nothing it did not read or show\. That is not evidence that nothing is open\./);
});

// 5. Server text is data everywhere: every server string comes out escaped.
const hostile = '<img src=x onerror=alert(1)>';
test('every server string renders as text, never markup', () => {
  const card = cardOf('acc-003');
  card.id = `card${hostile}`; card.brief_id = `brief${hostile}`; card.sha256 = `sha${hostile}`; card.seal_receipt_id = `seal${hostile}`;
  card.gaps = card.gaps.map(gap => ({...gap, text: `${gap.text}${hostile}`, code: `${gap.code}${hostile}`}));
  card.units = card.units.map(unit => ({...unit, record_ref: `${unit.record_ref}${hostile}`, tool: `${unit.tool}${hostile}`,
    body: {...unit.body, text: unit.body.text && `${unit.body.text}${hostile}`, speaker: unit.body.speaker && `${unit.body.speaker}${hostile}`,
      role: unit.body.role && `${unit.body.role}${hostile}`, basis: unit.body.basis && `${unit.body.basis}${hostile}`}}));
  const receipt = {id: `r${hostile}`, operation_id: `op${hostile}`, parent_id: `p${hostile}`, ordinal: 1, tool: `tool${hostile}`, scope: `scope${hostile}`,
    account: `acc${hostile}`, status: `ok${hostile}`, http_status: 200, row_count: 1, rows: [`row${hostile}`], error: `error${hostile}`,
    started_at: `2027${hostile}`, finished_at: `2027${hostile}`};
  const named = [{account: 'acc-003', name: `Name${hostile}`}];
  const pages = [
    inRouter(React.createElement(evidence.CardEvidence, {card, receiptFor: () => ready(receipt), params: new URLSearchParams('account=acc-003'), generation: 1})),
    inRouter(React.createElement(evidence.UnitDetailView, {n: 5, cardId: card.id, params: new URLSearchParams(), unit: ready({...card.units[4], card: card.id, receipt})})),
    inRouter(React.createElement(readiness.CardReadiness, {card, evidenceHref: '/'})),
    inRouter(React.createElement(frame.AccountPicker, {selection: chosen({granted: named})})),
    inRouter(React.createElement(frame.ApiRenewalHeader, {page: 'evidence', selection: chosen({accountName: `Name${hostile}`, cardId: card.id, card: ready(card)})})),
    inRouter(React.createElement(frame.CardList, {selection: chosen({cards: ready({cards: [{...card, published_at: `2027${hostile}`}]}), cardId: card.id})})),
    renderToStaticMarkup(React.createElement(reads.ServerContext, {value: {api: {}, subscribe: () => () => {}, getSnapshot: () => ({...signedInState,
      session: {...signedInState.session, principal: {...signedInState.session.principal, display_name: `Reviewer${hostile}`}}})}},
    React.createElement(MemoryRouter, {initialEntries: [serverPaths.readiness]}, React.createElement(shell.Frame)))),
    inRouter(React.createElement(frame.Refusal, {error: {kind: 'server', code: `code${hostile}`}})),
  ];
  for (const html of pages) {
    assert.doesNotMatch(html, /<img/);
    assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
  }
});

test('no app source renders HTML from a string', () => {
  const dir = fileURLToPath(new URL('.', import.meta.url));
  const names = readdirSync(dir, {recursive: true}).map(String).filter(name => /\.(jsx?|mjs|tsx?)$/.test(name) && !name.includes('.test.'));
  assert.ok(names.some(name => name.endsWith('Frame.tsx')) && names.some(name => name.endsWith('client.ts')), 'the TypeScript sources are checked too');
  for (const name of names) {
    const source = readFileSync(join(dir, name), 'utf8');
    assert.doesNotMatch(source, /dangerouslySetInnerHTML|\.innerHTML|outerHTML|insertAdjacentHTML|document\.write/, relative(dir, join(dir, name)));
  }
});

// 4. The command palette offers the server pages and nothing else.
test('the server palette offers only the server pages, with the chosen account and open card kept', () => {
  const visited = [];
  const items = pages.pageItems(to => visited.push(to), '?account=acc-002&card=card-two&unit=4');
  assert.deepEqual(items.map(item => [item.group, item.label]), [['Ops', 'Coverage'], ['Ops', 'Evidence'], ['Ops', 'Readiness']]);
  for (const item of items) item.onSelect();
  assert.deepEqual(visited, [serverPaths.coverage + '?account=acc-002&card=card-two', serverPaths.evidence + '?account=acc-002&card=card-two', serverPaths.readiness + '?account=acc-002&card=card-two']);
});

// Minor: the picker commits a choice, it does not navigate on every change.
test('the account picker commits with a button and keeps the current account until then', () => {
  const html = inRouter(React.createElement(frame.AccountPicker, {selection: chosen({})}));
  assert.match(html, /<form class="api-account"/);
  assert.match(html, /<button type="submit" class="button">Open account<\/button>/);
});
