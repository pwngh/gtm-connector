// Browser tests for the app in API mode, against the built app (web/dist) and
// a stub of /api/v1 that each test steers: delayed and refused answers, two
// accounts, blocked browser storage, a session that expires, hostile text in
// names, gaps and receipts, a static host's answer, and wrong addresses. The
// frame, sign-in and theme are checked at 320, 390, 768 and 1440 wide, light
// and dark: tab order, target sizes, focus outlines, Escape, overflow, text
// contrast and what browser storage holds. The stub keeps the Go server's
// shapes: the session view, the {"error": {code, message}} envelope, the CSRF
// header and Origin checks, 403 not_permitted for an ungranted account, and the
// security headers on every response, with the Content-Security-Policy built
// from basePolicy in internal/transport/http/static.go as the server builds it.
// The browser reaches nothing but the stub.
//
// It drives the installed Chrome, so it is not part of `npm test`. To run it:
//   npm run build
//   npm run test:browser [-- --channel chrome --screenshots DIR --contrast]
// With --screenshots, the frame's matrix saves one picture per width and theme
// in DIR; with --contrast, it prints the lowest contrast of each text colour.
// The exit code is 1 when any check fails.
import {createServer} from 'node:http';
import {createHash} from 'node:crypto';
import {readFileSync, existsSync, statSync, mkdirSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import {basename, extname, join, normalize} from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';
import {chromium} from 'playwright-core';

const {values: options} = parseArgs({options: {channel: {type: 'string', default: 'chrome'}, screenshots: {type: 'string'}, contrast: {type: 'boolean', default: false}}});
if (options.screenshots) mkdirSync(options.screenshots, {recursive: true});
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
  // As serveApp on the Go server: a reserved path is the API's 404, a file of
  // the build is served, "/" and the client routes get the app, any other path
  // without an extension outside /assets gets 404.html, and the rest plain text.
  if (/^\/(mcp|\.well-known|healthz)(\/|$)/.test(path)) return refuse(response, 404, 'not_found');
  const file = normalize(join(dist, path));
  const found = file.startsWith(dist) && existsSync(file) && statSync(file).isFile();
  const client = path === '/' || path === '/index.html' || (/^\/(deals|ops)(\/|$)/.test(path) && !extname(path));
  if (!found && !client) {
    const page = !extname(path) && !/^\/assets(\/|$)/.test(path) && existsSync(join(dist, '404.html'));
    response.writeHead(404, {...securityHeaders, 'Content-Type': `text/${page ? 'html' : 'plain'}; charset=utf-8`, ...(page ? {'Cache-Control': 'no-cache'} : {})});
    response.end(page ? readFileSync(join(dist, '404.html')) : 'Not found.\n');
    return;
  }
  const target = found ? file : join(dist, 'index.html');
  stub.files.push(basename(target));
  response.writeHead(200, {...securityHeaders, 'Content-Type': types[extname(target)] || 'application/octet-stream'});
  response.end(readFileSync(target));
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
base = `http://127.0.0.1:${server.address().port}`;
const evidence = `${base}/ops/evidence`, readiness = `${base}/ops/readiness`;

// Checks.
const results = [];
const check = (ok, what) => {results.push(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) process.exitCode = 1;};
const browser = await chromium.launch({channel: options.channel, headless: true});
async function page({width = 1440, scheme = 'light', blockStorage = false, theme = null} = {}) {
  const context = await browser.newContext({viewport: {width, height: 900}, colorScheme: scheme, reducedMotion: 'reduce'});
  // Offline: anything off the stub is refused. The origin of every request is kept.
  const origins = new Set();
  context.on('request', request => {const url = new URL(request.url()); if (!['data:', 'blob:'].includes(url.protocol)) origins.add(url.origin);});
  await context.route(url => !url.href.startsWith(base), route => route.abort());
  await context.addInitScript(([chosen, block]) => {
    document.addEventListener('securitypolicyviolation', event => console.error(`policy violation: ${event.violatedDirective} ${event.blockedURI}`));
    if (block) {for (const name of ['getItem', 'setItem', 'removeItem']) Storage.prototype[name] = () => {throw new DOMException('Blocked', 'SecurityError');};}
    else if (chosen && !sessionStorage.getItem('seeded')) try {localStorage.setItem('gtm-connector-theme', chosen); sessionStorage.setItem('seeded', '1');} catch {}
  }, [theme, blockStorage]);
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
  await tab.waitForSelector('.fr-signin');
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', password);
  if (how === 'enter') await tab.press('input[name=password]', 'Enter'); else await tab.click('.fr-signin__form button[type=submit]');
  await tab.waitForSelector('.fr-who', {state: 'attached'});
}
// Sign out from the top bar, or from the menu where the top bar has no room for it.
async function signOut(tab) {
  if (await tab.locator('.fr-menu-button').isVisible()) {
    await tab.click('.fr-menu-button');
    await tab.click('.fr-menu button:has-text("Sign out")');
  } else await tab.click('.fr-tools button:has-text("Sign out")');
}
const signedInNow = tab => tab.locator('.fr-who').count().then(count => count > 0);
const focused = tab => tab.evaluate(() => {const el = document.activeElement; return el === document.body ? 'body' : `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${el.name ? '[' + el.name + ']' : ''}`;});
// What is drawn inside the frame's own parts (the top bar, the page links, the
// sign-in screen and any open dialog): the contrast of every text colour on the
// first opaque background behind it, the smallest text, every control smaller
// than 44 by 44 or without a name, and how far the page scrolls sideways.
const frameParts = '.fr-chrome, .fr-signin, .fr-nopage, [role=dialog]';
const measure = tab => tab.evaluate(parts => {
  const luminance = rgb => {const [r, g, b] = rgb.match(/[\d.]+/g).slice(0, 3).map(Number).map(v => {v /= 255; return v <= .03928 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4;}); return .2126 * r + .7152 * g + .0722 * b;};
  const background = el => {while (el) {const color = getComputedStyle(el).backgroundColor; if (color && !/rgba\(.*, 0\)|transparent/.test(color)) return color; el = el.parentElement;} return 'rgb(255,255,255)';};
  const contrast = el => {const a = luminance(getComputedStyle(el).color), b = luminance(background(el)); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);};
  const shown = el => {const box = el.getBoundingClientRect(), style = getComputedStyle(el); return box.width > 0 && box.height > 0 && box.bottom > 0 && style.visibility !== 'hidden' && !el.closest('[inert]');};
  const roots = [...document.querySelectorAll(parts)];
  const all = roots.flatMap(root => [root, ...root.querySelectorAll('*')]).filter(shown);
  const texts = all.filter(el => [...el.childNodes].some(node => node.nodeType === 3 && node.textContent.trim()));
  const colours = {};
  for (const el of texts) {
    const key = getComputedStyle(el).color, ratio = contrast(el);
    colours[key] = Math.min(colours[key] ?? Infinity, ratio);
  }
  const name = el => el.getAttribute('aria-label') || el.textContent.trim() || el.name || el.tagName;
  const small = all.filter(el => el.matches('a[href], button, input, select')).map(el => ({name: name(el), box: el.getBoundingClientRect()}))
    .filter(({box}) => box.width < 44 || box.height < 44).map(({name: label, box}) => `${label} ${Math.round(box.width)}x${Math.round(box.height)}`);
  // A control's accessible name: its label, its aria-label or its text.
  const unnamed = all.filter(el => el.matches('a[href], button, input, select'))
    .filter(el => !(el.getAttribute('aria-label') || el.textContent.trim() || [...(el.labels ?? [])].some(label => label.textContent.trim()))).map(el => el.outerHTML.slice(0, 80));
  return {colours, lowest: Math.min(...Object.values(colours)), smallest: Math.min(...texts.map(el => parseFloat(getComputedStyle(el).fontSize))), small, unnamed,
    overflow: document.documentElement.scrollWidth - innerWidth};
}, frameParts);
// What has focus: its name, its size and its outline.
const ring = tab => tab.evaluate(() => {
  const el = document.activeElement, box = el.getBoundingClientRect(), style = getComputedStyle(el);
  return {name: el.getAttribute('aria-label') || el.textContent.trim() || el.getAttribute('name') || el.tagName, width: box.width, height: box.height,
    outline: style.outlineStyle !== 'none' ? parseFloat(style.outlineWidth) : 0, inDialog: Boolean(el.closest('[role=dialog]'))};
});
const fits = focus => focus.width >= 44 && focus.height >= 44 && focus.outline >= 3;
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
  await tab.waitForSelector('.fr-signin');
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', 'wrong');
  await tab.click('.fr-signin__form button[type=submit]');
  await tab.waitForSelector('.fr-signin [data-refusal="sign_in_refused"]');
  check((await focused(tab)).includes('password') && await tab.inputValue('input[name=password]') === '', `a refused sign-in from the button clears the password and puts focus there (${await focused(tab)})`);
  await signIn(tab, 'enter');
  await tab.waitForSelector('.api-renewal-header h1');
  await until(tab, () => document.activeElement?.matches('main h1'));
  check(true, 'after sign-in focus is on the page heading');
  check((await tab.textContent('.fr > .fr-announce[role=status]')).startsWith('Signed in as Demo reviewer'), 'the sign-in is announced in a live region that was already on the page');
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
  await signOut(tab);
  await tab.waitForSelector('.fr-signin');
  check((await focused(tab)) === 'h1#fr-signin-title', `after sign-out focus is on the sign-in heading (${await focused(tab)})`);
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
  await signOut(tab);
  await tab.waitForSelector('.fr-signin');
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
  ['the session read', evidence, path => path === '/api/v1/session', 'input[name=login]'],
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
  await tab.waitForSelector('.fr-signin', {timeout: 6000});
  const reads = stub.log.filter(entry => entry === 'GET /api/v1/session').length;
  await tab.waitForTimeout(3000);
  const left = await tab.evaluate(() => document.body.innerText);
  check(await tab.locator('.api-unit, .api-frame, .fr-who').count() === 0 && !/Third Account Co|acc-003|card-acc-003|Demo reviewer/.test(left),
    'after an expired session nothing that was read is on screen');
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
  await signOut(tab);
  await tab.waitForSelector('.fr-signin');
  check(stub.log.filter(entry => entry === 'DELETE /api/v1/session').length === 2 && stub.sessions.size === 0, 'sign-out retried with the fresh token and ended the session');
  await tab.context().close();
});

// 8. A sign-in that finds this browser signed in elsewhere says so and does not adopt it silently.
await scenario('signed in elsewhere', async () => {
  const tab = await page();
  await tab.goto(evidence, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.fr-signin');
  stub.pre.clear();
  const other = token(); stub.sessions.set(other, {principal: elsewhere, expires: Date.now() + 3600_000});
  await tab.context().addCookies([{name: 'stub_session', value: other, url: base}]);
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', 'typed-but-never-checked');
  await tab.press('input[name=password]', 'Enter');
  await tab.waitForSelector('.fr-signin__elsewhere');
  check(!await signedInNow(tab) && (await tab.textContent('.fr-signin__elsewhere')).includes('Another principal'), 'the other session is named, and not adopted without a choice');
  await tab.click('.fr-signin__elsewhere button');
  await tab.waitForSelector('.fr-who', {state: 'attached'});
  check((await tab.textContent('.fr-who')).includes('Another principal'), 'continuing adopts it by choice');
  await tab.context().close();
});

// 9. Blocked browser storage: the server pages still work, and the theme follows the system.
for (const width of [320, 1440]) {
  await scenario(`blocked storage ${width}`, async () => {
    const tab = await page({width, blockStorage: true});
    await tab.goto(`${evidence}?account=acc-003`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.api-unit');
    await tab.waitForTimeout(300);
    check(tab.errors.length === 0, `${width}: no page errors with storage blocked (${tab.errors.join(' | ')})`);
    const theme = await tab.evaluate(() => document.documentElement.dataset.theme);
    check(theme === 'light', `${width}: with storage blocked the theme follows the system (${theme})`);
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
    await tab.waitForSelector('.ui-palette__option');
    const groups = await tab.locator('.ui-palette__group').allTextContents();
    const options = await tab.locator('.ui-palette__label').allTextContents();
    check(groups.join() === 'Ops' && options.join() === 'Evidence,Readiness', `${width} ${scheme}: the palette offers the server pages only (${options.join()})`);
    await tab.locator('.ui-palette__option', {hasText: 'Readiness'}).first().click();
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
    await tab.waitForSelector('.ui-palette__option');
    const palette = await tab.evaluate(() => ({title: document.querySelector('.ui-dialog__title').textContent, placeholder: document.querySelector('.ui-palette__field').placeholder}));
    await tab.keyboard.type('nothing like this');
    await tab.waitForSelector('.ui-palette__empty');
    const empty = await tab.textContent('.ui-palette__empty');
    check(palette.title === 'Find a page' && palette.placeholder === 'Evidence or Readiness' && empty === 'No page matches.', `${width}: the palette is titled ${palette.title}, suggests ${palette.placeholder} and says ${empty}`);
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
  for (const path of ['/', new URL(evidence).pathname, '/ops/nothing']) {
    await tab.goto(base + path, {waitUntil: 'networkidle'});
    await tab.waitForSelector('[role=alert]');
    seen.push((await tab.evaluate(() => document.body.innerText)).trim());
  }
  check(seen.every(shown => shown === needsServer) && tab.errors.length === 0 && violations(tab).length === 0,
    `a static host's answer shows only that the page needs its server (${seen.join(' | ')}; ${tab.errors.join(' | ')})`);
  await tab.context().close();
});

// 13. Addresses: "/" and /ops open Evidence; a wrong address inside the app
// says so with one link back; any other address gets the static page, 404.
await scenario('home addresses', async () => {
  const tab = await page();
  for (const path of ['/', '/ops', '/?account=acc-003']) {
    await tab.goto(base + path, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.fr-signin');
    check(new URL(tab.url()).pathname === '/ops/evidence', `${path} opens Evidence (${new URL(tab.url()).pathname})`);
  }
  await tab.context().close();
});
await scenario('a wrong address in the app', async () => {
  const tab = await page({width: 390});
  await tab.goto(`${base}/ops/nothing`, {waitUntil: 'networkidle'});
  // Signed out, any address shows the sign-in screen; after sign-in the address says there is no page.
  await signIn(tab);
  await tab.waitForSelector('main h1.fr-nopage__title');
  const shown = await tab.evaluate(() => ({heading: document.querySelector('main h1').textContent,
    links: [...document.querySelectorAll('main a')].map(link => `${link.textContent} ${link.getAttribute('href')}`)}));
  check(shown.heading === 'There is no page at this address.' && shown.links.join() === 'Go to Evidence /ops/evidence',
    `/ops/nothing shows the in-app page and its one link (${shown.heading}; ${shown.links.join(', ')})`);
  await tab.click('main a');
  await tab.waitForSelector('.api-renewal-header');
  check(new URL(tab.url()).pathname === '/ops/evidence' && tab.errors.length === 0, `its link leads to Evidence (${tab.url()}; ${tab.errors.join(' | ')})`);
  await tab.context().close();
});
for (const scheme of ['light', 'dark']) {
  await scenario(`a wrong address on the server ${scheme}`, async () => {
    const tab = await page({width: 320, scheme});
    const answer = await tab.goto(`${base}/history`, {waitUntil: 'networkidle'});
    const headers = answer.headers();
    check(answer.status() === 404 && headers['content-type'] === 'text/html; charset=utf-8' && headers['x-frame-options'] === 'DENY' &&
      headers['content-security-policy'] === securityHeaders['Content-Security-Policy'],
      `${scheme}: /history answers ${answer.status()} ${headers['content-type']} with the security headers`);
    const facts = await tab.evaluate(() => {
      const luminance = rgb => {const [r, g, b] = rgb.match(/\d+/g).slice(0, 3).map(Number).map(v => {v /= 255; return v <= .03928 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4;}); return .2126 * r + .7152 * g + .0722 * b;};
      const background = el => {while (el) {const color = getComputedStyle(el).backgroundColor; if (color && !/rgba\(.*, 0\)|transparent/.test(color)) return color; el = el.parentElement;} return 'rgb(255,255,255)';};
      const contrast = el => {const a = luminance(getComputedStyle(el).color), b = luminance(background(el)); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);};
      const texts = [...document.querySelectorAll('h1, p, a')];
      const link = document.querySelector('a'), box = link.getBoundingClientRect();
      return {heading: document.querySelector('h1').textContent, body: document.body.innerText, scripts: document.scripts.length, root: Boolean(document.getElementById('root')),
        link: `${link.textContent} ${link.getAttribute('href')}`, links: document.querySelectorAll('a').length, size: [box.width, box.height],
        smallest: Math.min(...texts.map(el => parseFloat(getComputedStyle(el).fontSize))), contrast: Math.min(...texts.map(contrast)),
        overflow: document.documentElement.scrollWidth - innerWidth};
    });
    check(facts.heading === 'There is no page at this address.' && facts.body.includes('gtm-connector') && facts.link === 'Go to Evidence /' && facts.links === 1 &&
      facts.scripts === 0 && !facts.root, `${scheme}: the static page names the product, says there is no page and links to / (${facts.link}), with no script`);
    check(facts.size[0] >= 44 && facts.size[1] >= 44 && facts.smallest >= 14 && facts.contrast >= 4.5 && facts.overflow <= 0,
      `${scheme} 320: link ${facts.size.map(Math.round).join('x')}, smallest text ${facts.smallest}px, lowest contrast ${facts.contrast.toFixed(2)}:1, overflow ${facts.overflow}px`);
    await tab.keyboard.press('Tab');
    const ring = await tab.evaluate(() => {const el = document.activeElement, style = getComputedStyle(el); return {tag: el.tagName, outline: style.outlineStyle, width: parseFloat(style.outlineWidth)};});
    check(ring.tag === 'A' && ring.outline !== 'none' && ring.width >= 2, `${scheme}: the link shows keyboard focus (${ring.tag} ${ring.outline} ${ring.width}px)`);
    await tab.keyboard.press('Enter');
    await tab.waitForSelector('.fr-signin');
    check(new URL(tab.url()).pathname === '/ops/evidence' && violations(tab).length === 0 && tab.errors.length === 0,
      `${scheme}: the link leads back to Evidence (${tab.url()}; ${violations(tab).join(' | ')})`);
    await tab.context().close();
  });
}
await scenario('answers that keep their form', async () => {
  const tab = await page();
  const seen = [];
  for (const path of ['/mcp/x', '/assets/missing.js', '/missing.png', '/healthz/x']) {
    const answer = await tab.request.get(base + path);
    seen.push(`${path} ${answer.status()} ${answer.headers()['content-type']}`);
  }
  check(seen.join() === ['/mcp/x 404 application/json', '/assets/missing.js 404 text/plain; charset=utf-8', '/missing.png 404 text/plain; charset=utf-8',
    '/healthz/x 404 application/json'].join(), `a reserved path, a missing asset and a missing file keep their answers (${seen.join(', ')})`);
  await tab.context().close();
});

// 14. The frame, sign-in and theme at 320, 390, 768 and 1440 wide, light and dark.
const measured = [];
const reported = (what, facts) => {
  measured.push(...Object.entries(facts.colours).map(([colour, ratio]) => `${what} ${colour} ${ratio.toFixed(2)}`));
  return `lowest contrast ${facts.lowest.toFixed(2)}:1, smallest text ${facts.smallest}px, small controls [${facts.small.join(', ')}], unnamed controls [${facts.unnamed.join(', ')}], overflow ${facts.overflow}px`;
};
const healthy = facts => facts.lowest >= 4.5 && facts.smallest >= 14 && facts.small.length === 0 && facts.unnamed.length === 0 && facts.overflow <= 0;
for (const width of [320, 390, 768, 1440]) {
  for (const scheme of ['light', 'dark']) {
    await scenario(`frame ${width} ${scheme}`, async () => {
      const phone = width < 720, other = scheme === 'light' ? 'dark' : 'light', at = `${width} ${scheme}`;
      const tab = await page({width, scheme});
      const asked = `${evidence}?account=acc-003`;
      await tab.goto(asked, {waitUntil: 'networkidle'});
      await tab.waitForSelector('.fr-signin');
      const theme = await tab.evaluate(() => document.documentElement.dataset.theme);
      check(theme === scheme, `${at}: with nothing stored the theme follows the system (${theme})`);
      const help = tab.locator('.fr-signin__text', {hasText: 'demo-credentials.json'});
      check(await help.isVisible() && new URL(tab.url()).href === asked, `${at}: signed out, the address stays and the sign-in screen shows where the demo password is`);
      let facts = await measure(tab);
      check(healthy(facts), `${at}: the sign-in screen, ${reported(`${at} sign-in`, facts)}`);
      // The skip link comes first, then the fields and the button.
      await tab.keyboard.press('Tab');
      const skip = await ring(tab);
      const fields = [];
      for (let i = 0; i < 3; i++) {
        await tab.keyboard.press('Tab');
        fields.push(await ring(tab));
      }
      check(skip.name === 'Skip to content' && fits(skip) && fields.map(field => field.name).join() === 'login,password,Sign in' && fields.every(fits),
        `${at}: signed out, Tab reaches the skip link, Login, Password and Sign in, each at least 44 by 44 with a 3px outline (${[skip, ...fields].map(field => `${field.name} ${Math.round(field.width)}x${Math.round(field.height)} ${field.outline}px`).join(', ')})`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `sign-in-${width}-${scheme}.png`), fullPage: true});
      // A wrong password is refused, cleared and focused.
      await tab.fill('input[name=login]', login);
      await tab.fill('input[name=password]', 'wrong');
      await tab.press('input[name=password]', 'Enter');
      await tab.waitForSelector('.fr-signin [data-refusal="sign_in_refused"]');
      check((await focused(tab)).includes('password') && await tab.inputValue('input[name=password]') === '' && (await tab.textContent('.fr-signin [data-refusal]')).includes('The login or password is not right.'),
        `${at}: a wrong password shows the refusal and focus is in the cleared password field`);
      // After sign-in the address that was asked for renders, with its account.
      await signIn(tab);
      await until(tab, () => document.querySelector('.api-renewal-header h1')?.textContent.startsWith('Third') && document.querySelector('.api-unit'));
      check(new URL(tab.url()).searchParams.get('account') === 'acc-003' && new URL(tab.url()).pathname === '/ops/evidence' && await tab.inputValue('select[name=account]') === 'acc-003',
        `${at}: after sign-in ${new URL(tab.url()).pathname}${new URL(tab.url()).search} renders with that account`);
      const demo = phone ? '.fr-demo-row .fr-demo' : '.fr-bar .fr-demo';
      check(await tab.locator(demo).isVisible() && (await tab.textContent(demo)) === 'Demo, invented records', `${at}: the demo tag shows for a demo principal`);
      facts = await measure(tab);
      check(healthy(facts), `${at}: the signed-in frame, ${reported(`${at} frame`, facts)}`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `frame-${width}-${scheme}.png`)});
      // Tab order from the top of the page.
      await tab.reload({waitUntil: 'networkidle'});
      await tab.waitForSelector('.api-unit');
      const order = [];
      for (let i = 0; i < (phone ? 2 : 5); i++) {
        await tab.keyboard.press('Tab');
        order.push(await ring(tab));
      }
      const expected = phone ? 'Skip to content,Menu' : 'Skip to content,Ops,Find a page,Dark theme,Sign out';
      check(order.map(focus => focus.name).join() === expected && order.every(fits),
        `${at}: Tab order is ${order.map(focus => `${focus.name} ${Math.round(focus.width)}x${Math.round(focus.height)} ${focus.outline}px`).join(', ')}`);
      // Escape closes the palette and focus goes back to what opened it.
      const opener = phone ? '.fr-menu-button' : '.fr-find';
      await tab.focus(opener);
      if (phone) await tab.keyboard.press('Control+k'); else await tab.keyboard.press('Enter');
      await tab.waitForSelector('[role=dialog] .ui-palette__field');
      const paletteFocus = await focused(tab);
      facts = await measure(tab);
      check(healthy(facts), `${at}: the palette, ${reported(`${at} palette`, facts)}`);
      await tab.keyboard.press('Escape');
      await tab.waitForSelector('[role=dialog]', {state: 'detached'});
      await until(tab, selector => document.activeElement?.matches(selector), opener);
      check(paletteFocus.startsWith('input'), `${at}: the palette opens with focus in its field, and Escape closes it and returns focus to ${opener}`);
      if (phone) {
        // The phone menu: a full-screen dialog that holds focus, closed by Escape.
        await tab.click('.fr-menu-button');
        await tab.waitForSelector('[role=dialog] .fr-menu');
        const items = await tab.locator('[role=dialog] .fr-menu__item').allTextContents();
        const trapped = [];
        for (let i = 0; i < 9; i++) {
          await tab.keyboard.press('Tab');
          trapped.push(await ring(tab));
        }
        const box = await tab.locator('[role=dialog]').boundingBox();
        facts = await measure(tab);
        check(items.join() === 'Ops,Evidence,Readiness,Dark theme,Sign out' && trapped.every(focus => focus.inDialog) && trapped.every(fits) && box.width === width,
          `${at}: the menu fills the screen with ${items.join(', ')}, and Tab stays in it (${trapped.map(focus => focus.name).join(', ')})`);
        check(healthy(facts), `${at}: the menu, ${reported(`${at} menu`, facts)}`);
        if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `menu-${width}-${scheme}.png`)});
        await tab.keyboard.press('Escape');
        await tab.waitForSelector('[role=dialog]', {state: 'detached'});
        await until(tab, () => document.activeElement?.matches('.fr-menu-button'));
        check(true, `${at}: Escape closes the menu and returns focus to the menu button`);
      }
      // Choosing the other theme is kept across a reload, and is all that storage holds.
      if (phone) {
        await tab.click('.fr-menu-button');
        await tab.click('[role=dialog] button[aria-pressed]');
        await tab.keyboard.press('Escape');
      } else await tab.click('.fr-tools button[aria-label="Dark theme"]');
      await tab.reload({waitUntil: 'networkidle'});
      await tab.waitForSelector('.api-unit');
      const kept = await tab.evaluate(() => ({theme: document.documentElement.dataset.theme, local: Object.keys(localStorage), session: Object.keys(sessionStorage),
        canvas: getComputedStyle(document.querySelector('.fr')).backgroundColor}));
      check(kept.theme === other, `${at}: after choosing ${other} and reloading, the page is ${kept.theme} (${kept.canvas})`);
      check(kept.local.join() === 'gtm-connector-theme' && kept.session.length === 0, `${at}: storage holds only gtm-connector-theme (local ${kept.local.join()}; session ${kept.session.join()})`);
      facts = await measure(tab);
      check(healthy(facts), `${at}: the frame in ${other}, ${reported(`${at} chosen ${other}`, facts)}`);
      // Signing out, from the top bar or the menu, leaves the sign-in screen with focus on its heading and nothing read on screen.
      await signOut(tab);
      await tab.waitForSelector('.fr-signin');
      await until(tab, () => document.activeElement?.id === 'fr-signin-title');
      check(await tab.locator('.api-frame, .api-renewal-header, [role=dialog]').count() === 0, `${at}: after sign-out focus is on the sign-in heading and nothing read is on screen`);
      check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
      await tab.context().close();
    });
  }
}

await browser.close();
server.close();
console.log(results.join('\n'));
// The lowest contrast measured for each text colour in the frame, by width, theme and screen.
if (options.contrast) console.log(`\n${measured.join('\n')}`);
console.log(process.exitCode ? `\n${results.filter(line => line.startsWith('FAIL')).length} problems` : '\nNo problems');
