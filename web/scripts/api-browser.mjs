// Browser tests for the app in API mode, against the built app (web/dist) and
// a stub of /api/v1 that each test steers: delayed and refused answers, two
// accounts, blocked browser storage, a session that expires, hostile text in
// names, gaps and receipts, and a static host's answer. The stub keeps the Go server's
// shapes: the session view, the {"error": {code, message}} envelope, the CSRF
// header and Origin checks, 403 not_permitted for an ungranted account, and the
// security headers on every response, with the Content-Security-Policy built
// from basePolicy in internal/transport/http/static.go as the server builds it.
// The browser reaches nothing but the stub.
//
// It drives the installed Chrome, so it is not part of `npm test`. To run it:
//   npm run build
//   npm run test:browser [-- --channel chrome]
// The exit code is 1 when any check fails.
import {createServer} from 'node:http';
import {createHash} from 'node:crypto';
import {readFileSync, existsSync, statSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import {basename, extname, join, normalize} from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';
import {chromium} from 'playwright-core';

const {values: options} = parseArgs({options: {channel: {type: 'string', default: 'chrome'}}});
const dist = fileURLToPath(new URL('../dist/', import.meta.url));
if (!existsSync(join(dist, 'index.html'))) throw new Error('Run npm run build first.');

// The headers the Go server sends with every response. The policy allows each
// inline script of the build's index.html by its SHA-256, as policyFor does.
const goSource = readFileSync(new URL('../../internal/transport/http/static.go', import.meta.url), 'utf8');
const basePolicy = [...goSource.match(/const basePolicy = ([\s\S]*?)\n\n/)[1].matchAll(/"((?:[^"\\]|\\.)*)"/g)].map(match => match[1]).join('');
const inlineHashes = [...readFileSync(join(dist, 'index.html'), 'utf8').matchAll(/<script(\s[^>]*)?>([\s\S]*?)<\/script>/gi)]
  .filter(([, attributes = '', body]) => !/src=/i.test(attributes) && body.length > 0)
  .map(([, , body]) => ` 'sha256-${createHash('sha256').update(body).digest('base64')}'`).join('');
const securityHeaders = {
  'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer', 'X-Frame-Options': 'DENY',
  'Cross-Origin-Opener-Policy': 'same-origin', 'Cross-Origin-Resource-Policy': 'same-origin',
  'Content-Security-Policy': basePolicy.replace('%s', inlineHashes),
};
if (!basePolicy.includes("script-src 'self'%s") || !inlineHashes) throw new Error('The policy or the inline script hash could not be built.');

// Server data: the oracle's sealed demo cards, with hostile text added to what the server sends.
const hostile = '<img src=x onerror=window.__injected=1>';
const oracle = Object.fromEntries(gunzipSync(readFileSync(new URL('../../testdata/oracle/cards/demo.jsonl.gz', import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => JSON.parse(line)).map(row => [row.key, row]));
const accounts = [{account: 'acc-002', name: `Second ${hostile} Co.`}, {account: 'acc-003', name: 'Third Account Co.'}];
function cardOf(account) {
  const sealed = JSON.parse(oracle[account].card), id = `card-${account}`;
  return {id, account, brief_id: sealed.brief_id, kind: 'go', schema_version: 3, rules_version: '3', sha256: oracle[account].sha256, size: 1,
    seal_receipt_id: `seal-${account}`, published_at: '2027-01-15T15:00:04Z',
    units: sealed.units.map(body => ({card: id, n: body.n, kind: body.kind, record_ref: body.record_ref, tool: body.tool, ...(body.kind === 'sentence' ? {shown: body.shown} : {}),
      receipt_id: `receipt-${account}-${body.tool}`, record_version_id: `version-${body.n}`, body: {...body, speaker: body.speaker && `${body.speaker} ${hostile}`}})),
    gaps: sealed.gaps.map(gap => ({...gap, text: `${gap.text} ${hostile}`}))};
}
const cards = {'acc-002': cardOf('acc-002'), 'acc-003': cardOf('acc-003'), 'acc-001': cardOf('acc-001')};
const receiptOf = (id, account, tool) => ({id, operation_id: `op-${id}`, parent_id: 'brief', ordinal: 1, principal_id: 'p-1', token_id: 't', tool, scope: `${tool.includes('call') ? 'calls' : tool.includes('slack') ? 'slack' : 'crm'}:read`,
  account, connection_generation: 1, arguments: {}, request_id: null, brief_id: null, status: 'ok', http_status: 200, row_count: 1, rows: [`row ${hostile}`],
  elapsed_ms: 1, error: `note ${hostile}`, started_at: '2027-01-15T15:00:00Z', finished_at: '2027-01-15T15:00:01Z', delivered_at: null});
const principal = {id: 'p-1', display_name: `Demo reviewer ${hostile}`, kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const elsewhere = {...principal, id: 'p-2', display_name: 'Another principal'};
const login = 'demo-reviewer', password = 'stub-password-for-browser-tests';

// The stub. `stub.steer` answers a request before the stub's own routes when it returns an answer.
const stub = {sessions: new Map(), pre: new Set(), ttlMs: 3600_000, steer: null, log: [], files: [], pending: 0, granted: new Set(['acc-002', 'acc-003'])};
let base = '';
// Resolves once no API request has been open for 300 ms.
async function apiSettled() {
  for (let quiet = 0; quiet < 3;) {
    await new Promise(resolve => setTimeout(resolve, 100));
    quiet = stub.pending ? 0 : quiet + 1;
  }
}
function reset() {stub.sessions.clear(); stub.pre.clear(); stub.ttlMs = 3600_000; stub.steer = null; stub.log = []; stub.files = []; stub.granted = new Set(['acc-002', 'acc-003']);}
const token = () => Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
const cookies = request => Object.fromEntries((request.headers.cookie || '').split(';').map(part => part.trim().split('=')).filter(([key]) => key));
const live = request => {const id = cookies(request).stub_session, session = id && stub.sessions.get(id); return session && session.expires > Date.now() ? [id, session] : [null, null];};
const view = (id, session) => session ? {signed_in: true, principal: session.principal, csrf_token: `c-${id}`, expires_at: new Date(session.expires).toISOString()}
  : {signed_in: false, principal: null, csrf_token: `c-${id}`, expires_at: new Date(Date.now() + 3600_000).toISOString()};
function send(response, status, body, headers = {}) {
  response.writeHead(status, {...securityHeaders, 'Content-Type': 'application/json', 'Cache-Control': 'no-store', ...headers});
  response.end(body === undefined ? '' : JSON.stringify(body));
}
const refuse = (response, status, code) => send(response, status, {error: {code, message: 'Refused.'}});

async function api(request, response, path) {
  const steered = stub.steer?.(request.method, path);
  if (steered) {
    if (steered.delay) await new Promise(resolve => setTimeout(resolve, steered.delay));
    if (steered.raw !== undefined) {response.writeHead(steered.status ?? 200, {...securityHeaders, ...(steered.type ? {'Content-Type': steered.type} : {})}); response.end(steered.raw); return;}
    if (steered.code) return refuse(response, steered.status, steered.code);
    if (steered.json !== undefined) return send(response, steered.status ?? 200, steered.json);
  }
  const [id, session] = live(request);
  if (path === '/api/v1/session') {
    if (request.method === 'GET') {
      if (session) return send(response, 200, view(id, session));
      const pre = token(); stub.pre.add(pre);
      return send(response, 200, view(pre, null), {'Set-Cookie': `stub_pre=${pre}; Path=/; HttpOnly; SameSite=Lax`});
    }
    if (request.headers.origin !== base) return refuse(response, 403, 'origin_refused');
    const csrf = request.headers['x-csrf-token'];
    if (request.method === 'DELETE') {
      if (!session) return refuse(response, 401, 'sign_in_required');
      if (csrf !== `c-${id}`) return refuse(response, 403, 'csrf_invalid');
      stub.sessions.delete(id);
      return send(response, 200, {signed_in: false, principal: null}, {'Set-Cookie': 'stub_session=; Path=/; Max-Age=0'});
    }
    const pre = cookies(request).stub_pre;
    if (!(session && csrf === `c-${id}`) && !(pre && stub.pre.has(pre) && csrf === `c-${pre}`)) return refuse(response, 403, 'csrf_invalid');
    let body = '';
    for await (const chunk of request) body += chunk;
    const given = JSON.parse(body || '{}');
    if (given.login !== login || given.password !== password) return refuse(response, 401, 'sign_in_refused');
    stub.pre.delete(pre); if (id) stub.sessions.delete(id);
    const next = token(); stub.sessions.set(next, {principal, expires: Date.now() + stub.ttlMs});
    return send(response, 200, view(next, stub.sessions.get(next)), {'Set-Cookie': `stub_session=${next}; Path=/; HttpOnly; SameSite=Lax`});
  }
  if (!session) return refuse(response, 401, 'sign_in_required');
  let match;
  if (path === '/api/v1/accounts') return send(response, 200, {accounts: accounts.filter(item => stub.granted.has(item.account))});
  if ((match = path.match(/^\/api\/v1\/accounts\/([^/]+)\/cards$/))) {
    const account = decodeURIComponent(match[1]);
    if (!stub.granted.has(account) || !cards[account]) return refuse(response, 403, 'not_permitted');
    const {units, gaps, ...head} = cards[account];
    return send(response, 200, {account, cards: [head]});
  }
  const cardFor = cardId => Object.values(cards).find(card => card.id === cardId && stub.granted.has(card.account));
  if ((match = path.match(/^\/api\/v1\/cards\/([^/]+)$/))) {
    const card = cardFor(decodeURIComponent(match[1]));
    return card ? send(response, 200, card) : refuse(response, 403, 'not_permitted');
  }
  if ((match = path.match(/^\/api\/v1\/cards\/([^/]+)\/units\/(\d+)$/))) {
    const card = cardFor(decodeURIComponent(match[1])), unit = card?.units.find(item => item.n === Number(match[2]));
    return unit ? send(response, 200, {...unit, receipt: receiptOf(unit.receipt_id, card.account, unit.tool)}) : refuse(response, 403, 'not_permitted');
  }
  if ((match = path.match(/^\/api\/v1\/receipts\/(.+)$/))) {
    const id = decodeURIComponent(match[1]), card = Object.values(cards).find(item => item.units.some(unit => unit.receipt_id === id) && stub.granted.has(item.account));
    return card ? send(response, 200, receiptOf(id, card.account, card.units.find(unit => unit.receipt_id === id).tool)) : refuse(response, 403, 'not_permitted');
  }
  return refuse(response, 404, 'not_found');
}

const types = {'.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.ico': 'image/x-icon', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json'};
const server = createServer(async (request, response) => {
  const path = new URL(request.url, 'http://stub').pathname;
  if (path.startsWith('/api/')) {
    stub.log.push(`${request.method} ${path}`);
    stub.pending++;
    try {return await api(request, response, path);} finally {stub.pending--;}
  }
  const file = normalize(join(dist, path));
  const found = file.startsWith(dist) && existsSync(file) && statSync(file).isFile();
  // As on the Go server, a missing file is not found and only a path without an extension gets the app.
  if (!found && extname(path)) {response.writeHead(404, {...securityHeaders, 'Content-Type': 'text/plain; charset=utf-8'}); response.end('Not found.'); return;}
  const target = found ? file : join(dist, 'index.html');
  stub.files.push(basename(target));
  response.writeHead(200, {...securityHeaders, 'Content-Type': types[extname(target)] || 'application/octet-stream'});
  response.end(readFileSync(target));
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
base = `http://127.0.0.1:${server.address().port}`;
const evidence = `${base}/renewals/thornfield/evidence`, readiness = `${base}/renewals/thornfield/readiness`;

// Checks.
const results = [];
const check = (ok, what) => {results.push(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) process.exitCode = 1;};
const browser = await chromium.launch({channel: options.channel, headless: true});
async function page({width = 1440, scheme = 'light', blockStorage = false} = {}) {
  const context = await browser.newContext({viewport: {width, height: 900}, colorScheme: scheme, reducedMotion: 'reduce'});
  // Offline: anything off the stub is refused. The origin of every request is kept.
  const origins = new Set();
  context.on('request', request => {const url = new URL(request.url()); if (!['data:', 'blob:'].includes(url.protocol)) origins.add(url.origin);});
  await context.route(url => !url.href.startsWith(base), route => route.abort());
  await context.addInitScript(([theme, block]) => {
    document.addEventListener('securitypolicyviolation', event => console.error(`policy violation: ${event.violatedDirective} ${event.blockedURI}`));
    if (block) {for (const name of ['getItem', 'setItem', 'removeItem']) Storage.prototype[name] = () => {throw new DOMException('Blocked', 'SecurityError');};}
    else try {localStorage.setItem('gtm-workflow-theme', theme);} catch {}
  }, [scheme, blockStorage]);
  const tab = await context.newPage();
  tab.setDefaultTimeout(8000);
  tab.origins = origins;
  tab.errors = [];
  tab.console = [];
  tab.on('console', message => tab.console.push(message.text()));
  tab.on('pageerror', error => tab.errors.push(String(error)));
  tab.on('dialog', dialog => {tab.errors.push('dialog: ' + dialog.message()); dialog.dismiss();});
  return tab;
}
async function signIn(tab, how = 'enter') {
  await tab.waitForSelector('.api-signin');
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', password);
  if (how === 'enter') await tab.press('input[name=password]', 'Enter'); else await tab.click('.api-signin-form button[type=submit]');
  await tab.waitForSelector('.api-session');
}
const focused = tab => tab.evaluate(() => {const el = document.activeElement; return el === document.body ? 'body' : `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${el.name ? '[' + el.name + ']' : ''}`;});
const needsServer = 'This page needs its server. Open the address your gtm-connector server prints.';
const until = (tab, fn, arg) => tab.waitForFunction(fn, arg, {timeout: 8000});

async function scenario(name, run) {
  reset();
  try {await run();} catch (error) {check(false, `${name}: ${String(error).split('\n')[0]}`);}
}

// 1. The probe: only a static host's answer means no server.
for (const [label, answer] of [['431 text/plain', {status: 431, type: 'text/plain', raw: 'Request Header Fields Too Large'}], ['502 text/html', {status: 502, type: 'text/html', raw: '<html>Bad gateway</html>'}],
  ['a null body', {status: 200, type: 'application/json', raw: 'null'}], ['a cut-off JSON body', {status: 200, type: 'application/json', raw: '{"signed_in": fal'}]]) {
  await scenario(`probe ${label}`, async () => {
    stub.steer = (method, path) => path === '/api/v1/session' && method === 'GET' ? answer : null;
    const tab = await page();
    await tab.goto(evidence, {waitUntil: 'networkidle'});
    await tab.waitForSelector('[data-refusal]');
    check(await tab.locator(`text=${needsServer}`).count() === 0 && await tab.locator('[data-refusal]').count() === 1, `probe answered with ${label}: an error in API mode`);
    await tab.context().close();
  });
}

// 2. Sign-in, focus, picker commits, hostile text.
await scenario('sign-in and the picker', async () => {
  const tab = await page();
  await tab.goto(readiness, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.api-signin');
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', 'wrong');
  await tab.click('.api-signin-form button[type=submit]');
  await tab.waitForSelector('.api-signin .api-problem');
  check(await focused(tab) === 'input[password]' || (await focused(tab)).includes('password'), `a refused sign-in from the button puts focus on the password field (${await focused(tab)})`);
  await signIn(tab, 'enter');
  check(!['body'].includes(await focused(tab)) && (await focused(tab)).includes('api-session'), `after sign-in focus is on the session (${await focused(tab)})`);
  check((await tab.textContent('.api-frame > .api-announce[role=status]')).startsWith('Signed in as Demo reviewer'), 'the sign-in is announced in a live region that was already on the page');
  await tab.waitForSelector('.api-readiness [data-open-gap]');
  const injected = await tab.evaluate(() => Boolean(window.__injected) || Boolean(document.querySelector('main img')));
  check(!injected && (await tab.textContent('main')).includes('<img src=x onerror'), 'hostile names, gaps and speakers render as text');
  const select = tab.locator('select[name=account]');
  const before = {url: tab.url(), history: await tab.evaluate(() => history.length), reads: stub.log.filter(entry => entry.includes('/cards')).length};
  await select.focus();
  await select.selectOption('acc-002');
  await tab.waitForTimeout(400);
  check(tab.url() === before.url && stub.log.filter(entry => entry.includes('/cards')).length === before.reads, 'changing the picker reads nothing until the choice is committed');
  await tab.click('.api-account button[type=submit]');
  // The URL changes first; the router renders the new account in a transition, so wait for the page itself.
  await until(tab, () => new URL(location.href).searchParams.get('account') === 'acc-002' && document.querySelector('.api-renewal-header h1')?.textContent.startsWith('Second') && document.querySelector('.api-readiness [data-open-gap]'));
  check(await tab.evaluate(() => history.length) === before.history, 'a committed choice replaces the history entry');
  check((await tab.textContent('.api-renewal-header h1')).startsWith('Second <img'), 'the header shows the granted account by its server name, as text');
  check(tab.errors.length === 0, `no page errors (${tab.errors.join(' | ')})`);
  await tab.click('.api-session button');
  await tab.waitForSelector('.api-signin');
  check((await focused(tab)).startsWith('h2'), `after sign-out focus is on the sign-in heading (${await focused(tab)})`);
  await tab.context().close();
});

// 3. Two accounts answered out of order: the later choice wins, the earlier answer is dropped.
await scenario('out-of-order answers', async () => {
  const tab = await page();
  await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.api-unit');
  stub.steer = (method, path) => path.includes('acc-002') ? {delay: 1200} : null;
  await tab.selectOption('select[name=account]', 'acc-002');
  await tab.click('.api-account button[type=submit]');
  await tab.waitForTimeout(100);
  await tab.selectOption('select[name=account]', 'acc-003');
  await tab.click('.api-account button[type=submit]');
  await tab.waitForTimeout(1800);
  const facts = await tab.textContent('.api-facts');
  check(facts.includes('acc-003') && !facts.includes('acc-002') && (await tab.textContent('.api-renewal-header h1')).startsWith('Third'), 'a slow answer for an account no longer chosen is dropped');
  await tab.context().close();
});

// 3b. A new session reads afresh: nothing read under the earlier session is shown again.
await scenario('a second sign-in in the same tab', async () => {
  const tab = await page();
  await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('select[name=account] option[value="acc-002"]', {state: 'attached'});
  await tab.click('.api-session button');
  await tab.waitForSelector('.api-signin');
  // Between the sessions the principal loses its grant on acc-002, and the new session's reads are slow.
  stub.granted.delete('acc-002');
  stub.steer = (method, path) => path === '/api/v1/accounts' || path.endsWith('/cards') ? {delay: 1500} : null;
  await signIn(tab);
  await tab.waitForTimeout(400);
  check(await tab.locator('select[name=account] option[value="acc-002"], .api-unit').count() === 0, 'while the new session reads, nothing read under the earlier session is shown');
  await until(tab, () => document.querySelectorAll('select[name=account] option').length > 0 && document.querySelector('.api-unit'));
  const options = await tab.locator('select[name=account] option').evaluateAll(els => els.map(el => el.value));
  check(JSON.stringify(options) === '["acc-003"]', `the second session's account list is read afresh (${options.join(', ')})`);
  await tab.context().close();
});

// 4. A card from another account in the URL is not read or shown.
await scenario('account and card out of step', async () => {
  const tab = await page();
  await tab.goto(`${evidence}?account=acc-002&card=card-acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.api-card-mismatch');
  check(await tab.locator('.api-unit').count() === 0 && !stub.log.includes('GET /api/v1/cards/card-acc-003'), 'the other account’s card is neither read nor shown');
  await tab.goto(`${readiness}?account=acc-002&card=card-acc-003`, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.api-card-mismatch');
  check(await tab.locator('[data-open-gap]').count() === 0, 'Readiness shows no gap of the other account’s card');
  await tab.context().close();
});

// 5. Nine refusals, each shown as a refusal and never as an empty result.
const refusals = [
  ['the account list', readiness, path => path === '/api/v1/accounts', '.api-no-accounts'],
  ['the card list on Evidence', `${evidence}?account=acc-003`, path => path.endsWith('/cards'), 'text=No card is published'],
  ['the card list on Readiness', `${readiness}?account=acc-003`, path => path.endsWith('/cards'), 'text=No card is published'],
  ['the card on Evidence', `${evidence}?account=acc-003`, path => path === '/api/v1/cards/card-acc-003', '.api-section-title'],
  ['the card on Readiness', `${readiness}?account=acc-003`, path => path === '/api/v1/cards/card-acc-003', '[data-open-gap], .api-unit-summary'],
  ['a receipt', `${evidence}?account=acc-003`, path => path.startsWith('/api/v1/receipts/'), 'text=No receipt is bound'],
  ['a unit in full', `${evidence}?account=acc-003&card=card-acc-003&unit=5`, path => path.includes('/units/'), '.api-unit-detail >> text=No receipt is bound'],
  ['an ungranted account', `${evidence}?account=acc-001`, () => false, 'text=No card is published'],
  ['the session read', evidence, path => path === '/api/v1/session', '.api-signin'],
];
for (const [what, url, refused, empty] of refusals) {
  await scenario(`refused ${what}`, async () => {
    const tab = await page();
    if (what !== 'the session read') {await tab.goto(url, {waitUntil: 'networkidle'}); await signIn(tab);}
    stub.steer = (method, path) => method === 'GET' && refused(path) ? {status: what === 'the session read' ? 503 : 403, code: what === 'the session read' ? 'internal' : 'not_permitted'} : null;
    await tab.goto(url, {waitUntil: 'networkidle'});
    await tab.waitForSelector('[data-refusal]');
    check(await tab.locator('[data-refusal]').count() >= 1 && await tab.locator(empty).count() === 0, `a refused read of ${what} shows the refusal, not an empty result`);
    await tab.context().close();
  });
}

// 6. Expiry: the view clears at once, a failed refresh stays signed out, and nothing polls.
await scenario('expiry', async () => {
  stub.ttlMs = 2500;
  const tab = await page();
  await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.api-unit');
  stub.steer = (method, path) => path === '/api/v1/session' && method === 'GET' ? {status: 503, code: 'internal'} : null;
  await tab.waitForSelector('.api-signin', {timeout: 6000});
  const reads = stub.log.filter(entry => entry === 'GET /api/v1/session').length;
  await tab.waitForTimeout(3000);
  check(await tab.locator('.api-unit, .api-session').count() === 0, 'an expired session shows signed out, with no evidence on screen');
  check(stub.log.filter(entry => entry === 'GET /api/v1/session').length - reads === 0, 'no session polling after a failed refresh');
  await tab.context().close();
});

// 7. Sign-out with a token made stale by another tab is retried once with the fresh one.
await scenario('stale sign-out token', async () => {
  const tab = await page();
  await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  // The page's own reads finish first, so none of them meets the rotated session.
  await tab.waitForSelector('.api-unit');
  await apiSettled();
  // Another tab signs out and in again: the session, and its CSRF token, rotate under the same cookie name.
  const [id] = [...stub.sessions.keys()];
  const next = token(); stub.sessions.set(next, stub.sessions.get(id)); stub.sessions.delete(id);
  await tab.context().addCookies([{name: 'stub_session', value: next, url: base}]);
  await tab.click('.api-session button');
  await tab.waitForSelector('.api-signin');
  check(stub.log.filter(entry => entry === 'DELETE /api/v1/session').length === 2 && stub.sessions.size === 0, 'sign-out retried with the fresh token and ended the session');
  await tab.context().close();
});

// 8. A sign-in that finds this browser signed in elsewhere says so and does not adopt it silently.
await scenario('signed in elsewhere', async () => {
  const tab = await page();
  await tab.goto(evidence, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.api-signin');
  stub.pre.clear();
  const other = token(); stub.sessions.set(other, {principal: elsewhere, expires: Date.now() + 3600_000});
  await tab.context().addCookies([{name: 'stub_session', value: other, url: base}]);
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', 'typed-but-never-checked');
  await tab.press('input[name=password]', 'Enter');
  await tab.waitForSelector('.api-elsewhere');
  check(await tab.locator('.api-session').count() === 0 && (await tab.textContent('.api-elsewhere')).includes('Another principal'), 'the other session is named, and not adopted without a choice');
  await tab.click('.api-elsewhere button');
  await tab.waitForSelector('.api-session');
  check((await tab.textContent('.api-session')).includes('Another principal'), 'continuing adopts it by choice');
  await tab.context().close();
});

// 9. Blocked browser storage: the server pages still work. (The theme falls back to dark.)
for (const width of [320, 1440]) {
  await scenario(`blocked storage ${width}`, async () => {
    const tab = await page({width, blockStorage: true});
    await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.api-unit');
    await tab.waitForTimeout(300);
    check(tab.errors.length === 0, `${width}: no page errors with storage blocked (${tab.errors.join(' | ')})`);
    await tab.context().close();
  });
}

// 10. Deep links, the palette, contrast and type sizes, at 320 and 1440, light and dark.
for (const [width, scheme] of [[320, 'light'], [1440, 'light'], [320, 'dark'], [1440, 'dark']]) {
  await scenario(`layout ${width} ${scheme}`, async () => {
    const tab = await page({width, scheme});
    await tab.goto(`${evidence}?account=acc-003&card=card-acc-003&unit=8`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.api-unit-detail .api-receipt-full');
    const deep = await tab.evaluate(() => {const el = document.activeElement, box = el.getBoundingClientRect(); return {id: el.id, inView: box.top >= 0 && box.bottom <= innerHeight};});
    check(deep.id === 'api-unit-8-detail' && deep.inView, `${width} ${scheme}: a ?unit= link focuses the unit in view`);
    const sizes = await tab.evaluate(() => {
      const luminance = rgb => {const [r, g, b] = rgb.match(/\d+/g).slice(0, 3).map(Number).map(v => {v /= 255; return v <= .03928 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4;}); return .2126 * r + .7152 * g + .0722 * b;};
      const background = el => {while (el) {const color = getComputedStyle(el).backgroundColor; if (color && !/rgba\(.*, 0\)|transparent/.test(color)) return color; el = el.parentElement;} return 'rgb(255,255,255)';};
      const contrast = el => {const a = luminance(getComputedStyle(el).color), b = luminance(background(el)); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);};
      const meta = document.querySelector('.api-unit .library-meta span:not(.pill)');
      const small = [...document.querySelectorAll('.api-mono, .api-digest, .api-rows li, .api-record')].filter(el => parseFloat(getComputedStyle(el).fontSize) < 16).length;
      return {metaContrast: contrast(meta), small, overflow: document.documentElement.scrollWidth - innerWidth};
    });
    check(sizes.metaContrast >= 4.5, `${width} ${scheme}: unit kind, source and time contrast ${sizes.metaContrast.toFixed(2)}:1`);
    check(sizes.small === 0, `${width} ${scheme}: record references, receipt rows and the digest are at least 16px`);
    check(sizes.overflow <= 1, `${width} ${scheme}: no horizontal overflow (${sizes.overflow}px)`);
    await tab.keyboard.press('Control+k');
    await tab.waitForSelector('.command-option');
    const groups = await tab.locator('.command-group-title').allTextContents();
    const options = await tab.locator('.command-option strong').allTextContents();
    check(groups.join() === 'Read from the server' && options.join() === 'Evidence,Readiness', `${width} ${scheme}: the palette offers the server pages only (${options.join()})`);
    await tab.locator('.command-option', {hasText: 'Readiness'}).first().click();
    await until(tab, () => location.pathname.endsWith('/readiness') && new URL(location.href).searchParams.get('account') === 'acc-003');
    check(tab.errors.length === 0, `${width} ${scheme}: no page errors (${tab.errors.join(' | ')})`);
    await tab.context().close();
  });
}

// 11. The server's app at 390 and 1440: after sign-in, both pages and a reload,
// nothing broke the policy, no screen names the prototype, and the palette uses its own words.
const violations = tab => tab.console.filter(line => /policy violation|Content Security Policy|Refused to/i.test(line));
for (const width of [390, 1440]) {
  await scenario(`the server app alone ${width}`, async () => {
    const tab = await page({width});
    await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.api-unit');
    await tab.goto(`${readiness}?account=acc-003`, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.api-readiness [data-open-gap]');
    await tab.reload({waitUntil: 'networkidle'});
    await tab.waitForSelector('.api-readiness [data-open-gap]');
    // Long enough for anything loaded when the page is idle.
    await tab.waitForTimeout(2500);
    check(stub.files.some(name => name.startsWith('ApiReadiness-')), `${width}: the Readiness page loaded its own chunk`);
    const shown = await tab.evaluate(() => `${document.title}\n${document.body.innerText}`);
    check(!/Revenue Workflow Lab|walkthrough/i.test(shown), `${width}: no screen names the prototype (title ${await tab.title()})`);
    await tab.keyboard.press('Control+k');
    await tab.waitForSelector('.command-option');
    const palette = await tab.evaluate(() => ({title: document.querySelector('.dialog-head h2').textContent, placeholder: document.querySelector('.command-input input').placeholder}));
    await tab.keyboard.type('nothing like this');
    await tab.waitForSelector('.command-empty');
    const empty = await tab.textContent('.command-empty');
    check(palette.title === 'Go to' && palette.placeholder === 'Find a page' && empty === 'No page matches.', `${width}: the palette is titled ${palette.title}, asks to ${palette.placeholder} and says ${empty}`);
    check(violations(tab).length === 0, `${width}: no policy violation (${violations(tab).join(' | ')})`);
    check(tab.errors.length === 0, `${width}: no page errors (${tab.errors.join(' | ')})`);
    // The icons the page links, and the one a browser asks for by default, answer with an image.
    // They are read from here: Playwright aborts a page's request for /favicon.ico while routes are set.
    const linked = await tab.evaluate(() => [...document.querySelectorAll('link[rel~=icon]')].map(link => `${link.rel} ${link.getAttribute('href')}`));
    const icons = await Promise.all([...linked, 'default /favicon.ico'].map(async entry => {
      const answer = await tab.request.get(base + entry.split(' ').at(-1));
      return {entry, status: answer.status(), type: answer.headers()['content-type']};
    }));
    const title = await tab.title();
    check(title === 'gtm-connector' && icons.map(icon => icon.entry).join() === 'icon /favicon.svg,alternate icon /favicon.ico,default /favicon.ico' && icons.every(icon => icon.status === 200 && icon.type.startsWith('image/')),
      `${width}: the tab is titled ${title}, and its icons answer (${icons.map(icon => `${icon.entry} ${icon.status} ${icon.type}`).join(', ')})`);
    check(tab.origins.size === 1 && tab.origins.has(base), `${width}: no request left the page's origin (requested origins: ${[...tab.origins].join(', ')})`);
    await tab.context().close();
  });
}

// 12. A static host's answer: one sentence, at any path, that the page needs its server.
await scenario('a static host', async () => {
  stub.steer = (method, path) => path === '/api/v1/session' ? {status: 404, type: 'text/html', raw: '<html>Not found</html>'} : null;
  const tab = await page({width: 390});
  const seen = [];
  for (const path of ['/', new URL(evidence).pathname, '/queue']) {
    await tab.goto(base + path, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.route-error');
    seen.push((await tab.evaluate(() => document.body.innerText)).trim());
  }
  check(seen.every(shown => shown === needsServer) && tab.errors.length === 0 && violations(tab).length === 0,
    `a static host's answer shows only that the page needs its server (${seen.join(' | ')}; ${tab.errors.join(' | ')})`);
  await tab.context().close();
});

await browser.close();
server.close();
console.log(results.join('\n'));
console.log(process.exitCode ? `\n${results.filter(line => line.startsWith('FAIL')).length} problems` : '\nNo problems');
