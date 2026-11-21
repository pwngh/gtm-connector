// Browser tests for the app in API mode, against the built app (web/dist) and
// a stub of /api/v1 and /healthz that each test steers: delayed and refused
// answers, two accounts, the demo book's thirty accounts on Deals, seven on
// Coverage, blocked browser storage, a session that expires, hostile text in
// names, gaps and receipts, a static host's answer, and wrong addresses. The
// frame, sign-in and theme are checked at 320, 390, 768 and 1440 wide, light
// and dark, Coverage at 320, 390 and 1440, the Deal card's ledger and Text
// tab at 320, 390 and 1440, and its proof drawer at 390, 1100 and 1440: tab
// order, target sizes, focus outlines, Escape, overflow, text contrast and what
// browser storage holds. The Text tab is also read and copied for every
// recorded card, and shown for a card whose bytes do not match its seal.
// The stub keeps the Go server's shapes: the session view, the {"error":
// {code, message}} envelope, the CSRF header and Origin checks, 403
// not_permitted for an ungranted account, the /healthz answers, and the
// security headers on every response, with the Content-Security-Policy built
// from basePolicy in internal/transport/http/static.go as the server builds it.
// The browser reaches nothing but the stub.
//
// It drives the installed Chrome, so it is not part of `npm test`. To run it:
//   npm run build
//   npm run test:browser [-- --channel chrome --screenshots DIR --contrast]
// With --screenshots, the frame's matrix, Coverage, the Deal card and its drawer save one picture per
// width and theme in DIR; with --contrast, it prints the lowest contrast of
// each text colour.
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
// The sealed files the stub serves at /bytes and /text, by card ID: the recorded bytes, their digest and the text they hold.
const sealedFiles = new Map();
const fileOf = (id, row) => sealedFiles.set(id, {bytes: Buffer.from(row.card, 'utf8'), sha256: row.sha256, text: JSON.parse(row.card).text});
// The lines the server builds for recorded cards, by family and key, from testdata/card_lines.jsonl.gz, which a Go test holds to its builder.
const recordedLines = new Map(gunzipSync(readFileSync(new URL('../../testdata/card_lines.jsonl.gz', import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => JSON.parse(line)).map(row => [`${row.family} ${row.key}`, row.lines]));
function cardOf(account, row = oracle[account], id = `card-${account}`, lines = recordedLines.get(`demo ${account}`) ?? null) {
  const sealed = JSON.parse(row.card);
  fileOf(id, row);
  return {id, account, brief_id: sealed.brief_id, kind: 'go', schema_version: 3, rules_version: '3', sha256: row.sha256, size: 1,
    seal_receipt_id: `seal-${account}`, published_at: '2027-01-15T15:00:04Z', as_of: sealed.as_of, sources: null, lines,
    units: sealed.units.map(body => ({card: id, n: body.n, kind: body.kind, record_ref: body.record_ref, tool: body.tool, ...(body.kind === 'sentence' ? {shown: body.shown} : {}),
      receipt_id: `receipt-${account}-${body.tool}`, record_version_id: `version-${body.n}`, body: {...body, speaker: body.speaker && `${body.speaker} ${hostile}`}})),
    gaps: sealed.gaps.map(gap => ({...gap, text: `${gap.text} ${hostile}`}))};
}
const cards = Object.fromEntries(Object.keys(oracle).filter(key => /^acc-\d+$/.test(key)).map(key => [key, cardOf(key)]));
// Every recorded card in testdata/oracle/cards, each family the index lists, as cards of one more account.
const families = Object.keys(JSON.parse(readFileSync(new URL('../../testdata/oracle/cards/index.json', import.meta.url), 'utf8')).families);
const recorded = families.flatMap(file => gunzipSync(readFileSync(new URL(`../../testdata/oracle/cards/${file}.jsonl.gz`, import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => ({file, row: JSON.parse(line)})))
  .map(({file, row}, at) => cardOf('acc-900', row, `rec-${at}`, recordedLines.get(`${file} ${row.key}`) ?? null));
// Serves the recorded cards under acc-900, newest first in file order.
function serveRecorded() {
  stub.accounts = [...accounts, {account: 'acc-900', name: 'Recorded cards'}]; stub.granted.add('acc-900');
  stub.extra = recorded; stub.lists['acc-900'] = recorded.map(({units, gaps, as_of, sources, lines, ...head}) => head);
}
// The demo book's thirty accounts with their recorded names, by ID as the server lists them.
const book = JSON.parse(readFileSync(new URL('../../testdata/oracle/cards/demo_book.json', import.meta.url), 'utf8')).crm.accounts.rows
  .map(([account, name]) => ({account, name})).sort((a, b) => a.account < b.account ? -1 : 1);
// In the book each account's card has its own day, so a row can only show its own time.
const bookTime = account => `2027-01-${account.slice(-2)}T15:00:04Z`;
const receiptOf = (id, account, tool) => ({id, operation_id: `op-${id}`, parent_id: 'brief', ordinal: 1, principal_id: 'p-1', token_id: 't', tool, scope: `${tool.includes('call') ? 'calls' : tool.includes('slack') ? 'slack' : 'crm'}:read`,
  account, connection_generation: 1, arguments: {}, request_id: null, brief_id: null, status: 'ok', http_status: 200, row_count: 1, rows: [`row ${hostile}`],
  elapsed_ms: 1, error: `note ${hostile}`, started_at: '2027-01-15T15:00:00Z', finished_at: '2027-01-15T15:00:01Z', delivered_at: null});
const principal = {id: 'p-1', display_name: `Demo reviewer ${hostile}`, kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const customer = {...principal, id: 'p-3', display_name: 'Customer reader', workspace_kind: 'customer', demo: false};
const elsewhere = {...principal, id: 'p-2', display_name: 'Another principal'};
const login = 'demo-reviewer', password = 'stub-password-for-browser-tests';

// The stub. `stub.steer` answers a request before the stub's own routes when it returns an answer.
// `stub.accounts` is the account list it serves, `stub.times` the publish time of each card it
// lists, `stub.empty` the accounts with no card, `stub.principal` who signs in, and `stub.health`
// the status /healthz answers. `stub.extra` holds cards served beside the demo's, and `stub.lists`
// an account's card list where it is not the demo's one card.
const stub = {sessions: new Map(), pre: new Set(), ttlMs: 3600_000, steer: null, log: [], files: [], pending: 0, granted: new Set(['acc-002', 'acc-003']),
  accounts, times: null, empty: new Set(), principal, cardLists: 0, mostCardLists: 0, health: 'ok', extra: [], lists: {}};
let base = '';
// Resolves once no API request has been open for 300 ms.
async function apiSettled() {
  for (let quiet = 0; quiet < 3;) {
    await new Promise(resolve => setTimeout(resolve, 100));
    quiet = stub.pending ? 0 : quiet + 1;
  }
}
function reset() {
  stub.sessions.clear(); stub.pre.clear(); stub.ttlMs = 3600_000; stub.steer = null; stub.log = []; stub.files = []; stub.granted = new Set(['acc-002', 'acc-003']);
  stub.accounts = accounts; stub.times = null; stub.empty = new Set(); stub.principal = principal; stub.cardLists = 0; stub.mostCardLists = 0; stub.health = 'ok';
  stub.extra = []; stub.lists = {};
}
// Serves the demo book: thirty granted accounts, each card on its own day.
function serveBook() {stub.accounts = book; stub.granted = new Set(book.map(item => item.account)); stub.times = bookTime;}
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
    if (steered.raw !== undefined) {response.writeHead(steered.status ?? 200, {...securityHeaders, ...(steered.type ? {'Content-Type': steered.type} : {}), ...steered.headers}); response.end(steered.raw); return;}
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
    const next = token(); stub.sessions.set(next, {principal: stub.principal, expires: Date.now() + stub.ttlMs});
    return send(response, 200, view(next, stub.sessions.get(next)), {'Set-Cookie': `stub_session=${next}; Path=/; HttpOnly; SameSite=Lax`});
  }
  if (!session) return refuse(response, 401, 'sign_in_required');
  let match;
  if (path === '/api/v1/accounts') return send(response, 200, {accounts: stub.accounts.filter(item => stub.granted.has(item.account))});
  if ((match = path.match(/^\/api\/v1\/accounts\/([^/]+)\/cards$/))) {
    const account = decodeURIComponent(match[1]);
    if (stub.granted.has(account) && stub.lists[account]) return send(response, 200, {account, cards: stub.lists[account]});
    if (!stub.granted.has(account) || !cards[account]) return refuse(response, 403, 'not_permitted');
    const {units, gaps, as_of, sources, lines, ...head} = cards[account];
    if (stub.empty.has(account)) return send(response, 200, {account, cards: []});
    return send(response, 200, {account, cards: [{...head, published_at: stub.times?.(account) ?? head.published_at}]});
  }
  const cardFor = cardId => [...Object.values(cards), ...stub.extra].find(card => card.id === cardId && stub.granted.has(card.account));
  if ((match = path.match(/^\/api\/v1\/cards\/([^/]+)$/))) {
    const card = cardFor(decodeURIComponent(match[1]));
    return card ? send(response, 200, card) : refuse(response, 403, 'not_permitted');
  }
  // As the Go server: the sealed bytes exactly with their digest in X-Content-SHA256, or the text card as text/plain.
  if ((match = path.match(/^\/api\/v1\/cards\/([^/]+)\/(bytes|text)$/))) {
    const card = cardFor(decodeURIComponent(match[1])), file = card && sealedFiles.get(card.id);
    if (!file) return refuse(response, 403, 'not_permitted');
    response.writeHead(200, {...securityHeaders, 'Cache-Control': 'no-store', ...(match[2] === 'bytes'
      ? {'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment', 'X-Content-SHA256': file.sha256} : {'Content-Type': 'text/plain; charset=utf-8'})});
    response.end(match[2] === 'bytes' ? file.bytes : file.text);
    return;
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
    // How many card lists are open at once, at most.
    const cardList = /^\/api\/v1\/accounts\/[^/]+\/cards$/.test(path);
    if (cardList) {stub.cardLists++; stub.mostCardLists = Math.max(stub.mostCardLists, stub.cardLists);}
    try {return await api(request, response, path);} finally {stub.pending--; if (cardList) stub.cardLists--;}
  }
  // /healthz as the Go server answers it: 200 with ok, 503 with any other status.
  if (path === '/healthz') return send(response, stub.health === 'ok' ? 200 : 503, {status: stub.health});
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
const evidence = `${base}/ops/evidence`, thornfield = `${base}/deals/acc-003`;

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
const measure = (tab, measured = frameParts) => tab.evaluate(parts => {
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
}, measured);
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
  await tab.goto(evidence, {waitUntil: 'networkidle'});
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
  await tab.waitForSelector('.api-evidence [data-gap-code]');
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
  await until(tab, () => new URL(location.href).searchParams.get('account') === 'acc-002' && document.querySelector('.api-renewal-header h1')?.textContent.startsWith('Second') && document.querySelector('.api-evidence [data-gap-code]'));
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
  await tab.context().close();
});

// 5. Ten refusals, each shown as a refusal and never as an empty result.
const refusals = [
  ['the account list', evidence, path => path === '/api/v1/accounts', '.api-no-accounts'],
  ['the card list on Evidence', `${evidence}?account=acc-003`, path => path.endsWith('/cards'), 'text=No card is published'],
  ['the card list on a deal', thornfield, path => path.endsWith('/cards'), '.dc-section, .dc-empty'],
  ['the card on Evidence', `${evidence}?account=acc-003`, path => path === '/api/v1/cards/card-acc-003', '.api-section-title'],
  ['the card on a deal', thornfield, path => path === '/api/v1/cards/card-acc-003', '.dc-section, .dc-values-note, blockquote'],
  ['the sealed bytes on a deal', `${thornfield}/text`, path => path === '/api/v1/cards/card-acc-003/bytes', '.dc-section, .dc-values-note, blockquote, .dc-plain, [data-seal]'],
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
    await tab.waitForSelector('.ui-palette__group:has-text("Deals")');
    const groups = await tab.locator('.ui-palette__group').allTextContents();
    const options = await tab.locator('.ui-palette__label').allTextContents();
    check(groups.join() === 'Deals,Ops' && options.join('|') === `Second ${hostile} Co.|Third Account Co.|Coverage|Evidence`,
      `${width} ${scheme}: the palette lists the deals by name, as text, then the Ops pages (${groups.join()}: ${options.join('|')})`);
    await tab.locator('.ui-palette__option', {hasText: 'Third Account Co.'}).first().click();
    await until(tab, () => location.pathname === '/deals/acc-003' && document.querySelector('.dc-title')?.textContent === 'Third Account Co.');
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
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.dc-ledger');
    await tab.reload({waitUntil: 'networkidle'});
    await tab.waitForSelector('.dc-ledger');
    // Long enough for anything loaded when the page is idle.
    await tab.waitForTimeout(2500);
    check(stub.files.some(name => name.startsWith('Deals-')), `${width}: the deal loaded the Deals chunk`);
    const shown = await tab.evaluate(() => `${document.title}\n${document.body.innerText}`);
    check(!/Revenue Workflow Lab|walkthrough/i.test(shown), `${width}: no screen names the prototype (title ${await tab.title()})`);
    await tab.keyboard.press('Control+k');
    await tab.waitForSelector('.ui-palette__option');
    const palette = await tab.evaluate(() => ({title: document.querySelector('.ui-dialog__title').textContent, placeholder: document.querySelector('.ui-palette__field').placeholder}));
    await tab.keyboard.type('nothing like this');
    await tab.waitForSelector('.ui-palette__empty');
    const empty = await tab.textContent('.ui-palette__empty');
    check(palette.title === 'Find a deal' && palette.placeholder === 'A deal’s name, or Evidence' && empty === 'No deal or page matches.', `${width}: the palette is titled ${palette.title}, suggests ${palette.placeholder} and says ${empty}`);
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
    check(title === 'Third Account Co.' && icons.map(icon => icon.entry).join() === 'icon /favicon.svg,alternate icon /favicon.ico,default /favicon.ico' && icons.every(icon => icon.status === 200 && icon.type.startsWith('image/')),
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

// 13. Addresses: "/" opens Deals and /ops opens Coverage; a wrong address inside the app
// says so with links to both; any other address gets the static page, 404.
await scenario('home addresses', async () => {
  const tab = await page();
  for (const [path, opens] of [['/', '/deals'], ['/ops', '/ops/coverage'], ['/?account=acc-003', '/deals']]) {
    await tab.goto(base + path, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.fr-signin');
    check(new URL(tab.url()).pathname === opens, `${path} opens ${opens} (${new URL(tab.url()).pathname})`);
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
  check(shown.heading === 'There is no page at this address.' && shown.links.join() === 'Go to Deals /deals,Go to Ops /ops/coverage',
    `/ops/nothing shows the in-app page and its links (${shown.heading}; ${shown.links.join(', ')})`);
  await tab.click('main a');
  await tab.waitForSelector('.deals-list');
  check(new URL(tab.url()).pathname === '/deals' && tab.errors.length === 0, `its first link leads to Deals (${tab.url()}; ${tab.errors.join(' | ')})`);
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
    check(facts.heading === 'There is no page at this address.' && facts.body.includes('gtm-connector') && facts.link === 'Go to Deals /' && facts.links === 1 &&
      facts.scripts === 0 && !facts.root, `${scheme}: the static page names the product, says there is no page and links to / (${facts.link}), with no script`);
    check(facts.size[0] >= 44 && facts.size[1] >= 44 && facts.smallest >= 14 && facts.contrast >= 4.5 && facts.overflow <= 0,
      `${scheme} 320: link ${facts.size.map(Math.round).join('x')}, smallest text ${facts.smallest}px, lowest contrast ${facts.contrast.toFixed(2)}:1, overflow ${facts.overflow}px`);
    await tab.keyboard.press('Tab');
    const ring = await tab.evaluate(() => {const el = document.activeElement, style = getComputedStyle(el); return {tag: el.tagName, outline: style.outlineStyle, width: parseFloat(style.outlineWidth)};});
    check(ring.tag === 'A' && ring.outline !== 'none' && ring.width >= 2, `${scheme}: the link shows keyboard focus (${ring.tag} ${ring.outline} ${ring.width}px)`);
    await tab.keyboard.press('Enter');
    await tab.waitForSelector('.fr-signin');
    check(new URL(tab.url()).pathname === '/deals' && violations(tab).length === 0 && tab.errors.length === 0,
      `${scheme}: the link leads back to Deals (${tab.url()}; ${violations(tab).join(' | ')})`);
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

// What the frame and Coverage measure: the lowest contrast of each text colour, by width, theme and screen.
const measured = [];
const reported = (what, facts) => {
  measured.push(...Object.entries(facts.colours).map(([colour, ratio]) => `${what} ${colour} ${ratio.toFixed(2)}`));
  return `lowest contrast ${facts.lowest.toFixed(2)}:1, smallest text ${facts.smallest}px, small controls [${facts.small.join(', ')}], unnamed controls [${facts.unnamed.join(', ')}], overflow ${facts.overflow}px`;
};
const healthy = facts => facts.lowest >= 4.5 && facts.smallest >= 14 && facts.small.length === 0 && facts.unnamed.length === 0 && facts.overflow <= 0;

// 13b. Coverage: seven deals whose card lists are slow, one refused. At most
// four lists are read at once, a list not in yet says Reading, each count
// filters the table by the address and equals its rows, sorting moves rows
// and aria-sort, each health answer has its sentence, and a refused account
// list replaces the table. Rows are blocks under 720 and a table above it.
const coverageUrl = `${base}/ops/coverage`;
const many = ['c-01', 'c-02', 'c-03', 'c-04', 'c-05', 'c-06', 'c-07'];
const manyAccounts = many.map((id, i) => ({account: id, name: i === 0 ? `Alder ${hostile} Ltd` : ['Birch', 'Cedar', 'Dogwood', 'Elm', 'Fir', 'Gum'][i - 1] + ' Co.'}));
const listOf = id => {
  const at = many.indexOf(id), head = account => ({...cards['acc-003'], units: undefined, gaps: undefined, as_of: undefined, sources: undefined, lines: undefined, account});
  const made = Array.from({length: [2, 0, 1, 3, 0, 1, 0][at]}, (_, n) => ({...head(id), id: `card-${id}-${n}`, published_at: `2027-01-${String(15 - n - at).padStart(2, '0')}T15:00:04Z`}));
  return {account: id, cards: made};
};
const steerMany = (method, path) => {
  if (path === '/api/v1/accounts') return {json: {accounts: manyAccounts}};
  const match = path.match(/^\/api\/v1\/accounts\/([^/]+)\/cards$/);
  if (!match) return null;
  if (match[1] === 'c-07') return {delay: 400, status: 403, code: 'not_permitted'};
  return {delay: match[1] === 'c-06' ? 2500 : 400, json: listOf(match[1])};
};
const coverageFacts = tab => tab.evaluate(() => ({
  counts: Object.fromEntries([...document.querySelectorAll('.cv-count')].map(button => [button.dataset.filter, Number(button.querySelector('strong').textContent)])),
  pressed: document.querySelector('.cv-count[aria-pressed="true"]')?.dataset.filter,
  rows: [...document.querySelectorAll('.cv-table tbody tr')].map(row => row.dataset.account),
  sorted: [...document.querySelectorAll('.cv-table th[aria-sort]')].map(th => `${th.querySelector('button').dataset.sort} ${th.getAttribute('aria-sort')}`),
  display: document.querySelector('.cv-table tbody tr') && getComputedStyle(document.querySelector('.cv-table tbody tr')).display,
  overflow: document.documentElement.scrollWidth - innerWidth,
}));
const allRead = tab => until(tab, () => document.querySelectorAll('.cv-count').length === 4 && !document.querySelector('.cv-reading'));

await scenario('coverage reads', async () => {
  stub.steer = steerMany;
  const tab = await page();
  await tab.goto(coverageUrl, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.cv-table');
  await until(tab, () => document.querySelector('.cv-reading'));
  const early = await coverageFacts(tab);
  check(early.rows.length === 7 && await tab.locator('.cv-reading', {hasText: 'Reading'}).count() > 0, `a row whose list is not in yet says Reading (${early.rows.length} rows)`);
  await allRead(tab);
  check(stub.mostCardLists <= 4 && stub.mostCardLists > 1, `at most four card lists are read at once (${stub.mostCardLists} at most)`);
  const facts = await coverageFacts(tab);
  check(JSON.stringify(facts.counts) === '{"all":7,"card":4,"none":2,"unread":1}' && facts.pressed === 'all', `the summary counts 7 deals, 4 with a card, 2 with none, 1 not read (${JSON.stringify(facts.counts)})`);
  const refusedRow = await tab.textContent('.cv-table tr[data-account="c-07"]');
  check(refusedRow.includes('Not permitted') && refusedRow.includes('not_permitted'), `a refused list says Not permitted in its row (${refusedRow})`);
  const none = await tab.textContent('.cv-table tr[data-account="c-02"]');
  check(none.includes('No card') && none.includes('Nothing about its sources is inferred from that.'), 'No card is followed by the sentence that nothing is inferred');
  const injected = await tab.evaluate(() => Boolean(window.__injected) || Boolean(document.querySelector('main img')));
  check(!injected && (await tab.textContent('.cv-table tr[data-account="c-01"] th')).includes('<img src=x'), 'a hostile deal name renders as text');
  // Each count filters the table through the address, and equals its rows.
  for (const filter of ['card', 'none', 'unread', 'all']) {
    await tab.click(`.cv-count[data-filter="${filter}"]`);
    await until(tab, wanted => document.querySelector('.cv-count[aria-pressed="true"]')?.dataset.filter === wanted, filter);
    const shown = await coverageFacts(tab);
    const show = new URL(tab.url()).searchParams.get('show');
    check(shown.rows.length === facts.counts[filter] && show === (filter === 'all' ? null : filter), `the ${filter} count (${facts.counts[filter]}) equals the rows it shows (${shown.rows.length}), with show=${show} in the address`);
  }
  await tab.goto(`${coverageUrl}?show=none`, {waitUntil: 'networkidle'});
  await allRead(tab);
  const linked = await coverageFacts(tab);
  check(linked.pressed === 'none' && linked.rows.join() === 'c-02,c-05', `a link with show=none opens filtered (${linked.rows.join()})`);
  // Sorting changes the order and aria-sort.
  await tab.goto(coverageUrl, {waitUntil: 'networkidle'});
  await allRead(tab);
  const byDeal = await coverageFacts(tab);
  await tab.click('.cv-sort[data-sort="newest"]');
  const newest = await coverageFacts(tab);
  await tab.click('.cv-sort[data-sort="newest"]');
  const newestDown = await coverageFacts(tab);
  await tab.click('.cv-sort[data-sort="cards"]');
  const cardsUp = await coverageFacts(tab);
  check(byDeal.sorted.join() === 'deal ascending' && byDeal.rows.join() === 'c-01,c-02,c-03,c-04,c-05,c-06,c-07', `by default rows sort by deal, ascending (${byDeal.rows.join()})`);
  check(newest.sorted.join() === 'newest ascending' && newest.rows.join() === 'c-02,c-05,c-06,c-04,c-03,c-01,c-07', `Newest card sorts ascending with aria-sort (${newest.rows.join()})`);
  check(newestDown.sorted.join() === 'newest descending' && newestDown.rows.join() === 'c-01,c-03,c-04,c-06,c-02,c-05,c-07', `pressed again it sorts descending (${newestDown.rows.join()})`);
  check(cardsUp.sorted.join() === 'cards ascending' && cardsUp.rows.join() === 'c-02,c-05,c-03,c-06,c-01,c-04,c-07', `Cards sorts by count (${cardsUp.rows.join()})`);
  // The link opens the newest card on Evidence.
  await tab.click('.cv-table tr[data-account="c-04"] .cv-open');
  await until(tab, () => location.pathname === '/ops/evidence');
  const opened = new URL(tab.url()).searchParams;
  check(opened.get('account') === 'c-04' && opened.get('card') === 'card-c-04-0', `Open the card leads to Evidence with its account and newest card (${opened})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `coverage: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('coverage health and refusal', async () => {
  const tab = await page();
  await tab.goto(coverageUrl, {waitUntil: 'networkidle'});
  await signIn(tab);
  const said = {
    ok: 'The database answers and its migrations are this binary’s.',
    migrations_behind: 'The database answers, but its schema is not this binary’s.',
    database_unavailable: 'The database did not answer.',
  };
  for (const [status, sentence] of Object.entries(said)) {
    stub.health = status;
    await tab.reload({waitUntil: 'networkidle'});
    await tab.waitForSelector(`.cv-server[data-health="${status}"]`);
    const line = (await tab.textContent('.cv-server')).replace(/\s+/g, ' ').trim();
    check(line === `Server${sentence} ${status}` || line === `Server ${sentence} ${status}`, `/healthz ${status} shows its sentence (${line})`);
  }
  stub.health = 'ok';
  stub.steer = (method, path) => path === '/api/v1/accounts' ? {status: 403, code: 'not_permitted'} : null;
  await tab.reload({waitUntil: 'networkidle'});
  await tab.waitForSelector('.cv [data-refusal="not_permitted"]');
  const refused = await tab.evaluate(() => ({text: document.querySelector('.cv [data-refusal]').textContent, table: document.querySelectorAll('.cv-table, .cv-count').length}));
  check(refused.text.startsWith('Not permitted.') && refused.table === 0, `a refused account list shows Not permitted in place of the table (${refused.text})`);
  await tab.context().close();
});

for (const [width, scheme] of [[320, 'light'], [390, 'light'], [390, 'dark'], [1440, 'light'], [1440, 'dark']]) {
  await scenario(`coverage layout ${width} ${scheme}`, async () => {
    stub.steer = steerMany;
    const tab = await page({width, scheme});
    await tab.goto(coverageUrl, {waitUntil: 'networkidle'});
    await signIn(tab);
    await allRead(tab);
    const facts = await coverageFacts(tab);
    const phone = width < 720;
    check(facts.display === (phone ? 'grid' : 'table-row') && facts.overflow <= 0, `${width} ${scheme}: rows are ${phone ? 'blocks' : 'a table'} (${facts.display}) and nothing scrolls sideways (${facts.overflow}px)`);
    const seen = await measure(tab, '.cv');
    check(healthy(seen), `${width} ${scheme}: Coverage, ${reported(`${width} ${scheme} coverage`, seen)}`);
    await tab.focus('.cv-sort[data-sort="deal"]');
    await tab.keyboard.press('Tab');
    const sortRing = await ring(tab);
    check(sortRing.name.startsWith('Newest card') && fits(sortRing), `${width} ${scheme}: a sort header takes focus with its ring (${sortRing.name} ${Math.round(sortRing.width)}x${Math.round(sortRing.height)} ${sortRing.outline}px)`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `coverage-${width}-${scheme}.png`), fullPage: true});
    await tab.context().close();
  });
}

// 14. The frame, sign-in and theme at 320, 390, 768 and 1440 wide, light and dark.
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
      for (let i = 0; i < (phone ? 2 : 6); i++) {
        await tab.keyboard.press('Tab');
        order.push(await ring(tab));
      }
      const expected = phone ? 'Skip to content,Menu' : 'Skip to content,Deals,Ops,Find a deal,Dark theme,Sign out';
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
        check(items.join() === 'Deals,Ops,Coverage,Evidence,Dark theme,Sign out' && trapped.every(focus => focus.inDialog) && trapped.every(fits) && box.width === width,
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

// 15. Deals: the granted accounts by name, each row with its own newest card
// time, read four at a time; refusals per list and per row; the empty states;
// the first-run hint; the keys and the filter; and the layout at every width.
const collator = new Intl.Collator('en', {sensitivity: 'base', numeric: true});
const byName = list => [...list].sort((a, b) => collator.compare(a.name, b.name) || collator.compare(a.account, b.account));
// A time as the page writes it: the day and the time to the second, in UTC.
const utc = value => `${new Date(value).toLocaleDateString('en-GB', {day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC'})}, ${value.slice(11, 19)} UTC`;
const rowFacts = tab => tab.$$eval('.deals-row', rows => rows.map(row => {
  const box = row.getBoundingClientRect(), who = row.querySelector('.deals-row__who').getBoundingClientRect(), card = row.querySelector('.deals-row__card').getBoundingClientRect();
  const name = row.querySelector('.deals-row__name').getBoundingClientRect(), id = row.querySelector('.deals-row__id').getBoundingClientRect();
  return {account: row.dataset.account, name: row.querySelector('.deals-row__name').textContent, id: row.querySelector('.deals-row__id').textContent,
    card: row.querySelector('.deals-row__card').innerText.replace(/\s+/g, ' ').trim(), href: row.getAttribute('href'), height: box.height, width: box.width,
    current: row.getAttribute('aria-current'), tabIndex: row.tabIndex, cardBelow: card.top >= who.bottom - 1, oneLine: Math.abs(name.top - id.top) < 8 || Math.abs(name.bottom - id.bottom) < 8};
}));
const dealParts = '.fr-chrome, .deals-list, .deals-choose';

await scenario('deals: thirty accounts in name order, each opening its address', async () => {
  serveBook();
  stub.empty = new Set(['acc-030']);
  stub.steer = (method, path) => path.endsWith('/cards') ? {delay: 120} : null;
  const tab = await page({width: 1440});
  await tab.goto(`${base}/`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await until(tab, () => location.pathname === '/deals' && document.querySelectorAll('.deals-row').length === 30 && ![...document.querySelectorAll('.deals-row__card')].some(card => card.textContent === 'Reading'));
  const rows = await rowFacts(tab);
  const expected = byName(book);
  check(rows.map(row => row.account).join() === expected.map(row => row.account).join() && rows.every((row, i) => row.name === expected[i].name && row.id === row.account && row.href === `/deals/${row.account}`),
    `"/" opens Deals with 30 rows in name order, each named and linked as the server sent it (${rows.slice(0, 3).map(row => row.name).join(', ')}, ...)`);
  const wrong = rows.filter(row => row.card !== (row.account === 'acc-030' ? 'No card' : `Card published ${utc(bookTime(row.account))}`));
  check(wrong.length === 0, `each row shows its own newest card time in UTC, or No card (${wrong.map(row => `${row.account}: ${row.card}`).join('; ') || rows[0].card})`);
  check(rows.every(row => row.height >= 56), `every row is at least 56 high (lowest ${Math.min(...rows.map(row => row.height))})`);
  const lists = stub.log.filter(entry => /\/cards$/.test(entry)).length;
  check(lists === 30 && stub.mostCardLists <= 4 && stub.mostCardLists >= 2, `the 30 card lists are read with at most four in flight (${lists} reads, ${stub.mostCardLists} at once at most)`);
  const head = await tab.evaluate(() => {
    const list = document.querySelector('.deals-rail .deals-rows');
    return {title: document.querySelector('.deals-head__title').textContent, tag: document.querySelector('.deals-head__title').tagName, meta: document.querySelector('.deals-head__meta').textContent,
      choose: document.querySelector('.deals-choose')?.textContent, rail: document.querySelector('.deals-rail').getBoundingClientRect().width,
      overflow: getComputedStyle(list).overflowY, scrolls: list.scrollHeight > list.clientHeight, tabbable: [...document.querySelectorAll('.deals-row')].filter(row => row.tabIndex === 0).length};
  });
  check(head.title === 'Deals30' && head.tag === 'H1' && head.meta === 'Listed by name. This server does not rank deals yet.' && head.choose === 'Choose a deal.',
    `1440: the title and count, the line under it, and "Choose a deal." beside the list (${head.title}, ${head.meta}, ${head.choose})`);
  check(head.rail === 336 && head.overflow === 'auto' && head.scrolls && head.tabbable === 1, `1440: the list is a rail ${head.rail} wide whose rows scroll on their own, with one row in the Tab order`);
  stub.steer = null;
  let opened = 0;
  for (const row of rows) {
    await tab.click(`.deals-row[data-account="${row.account}"]`);
    await until(tab, ([href, name]) => location.pathname === href && document.querySelector('.deals-row[aria-current="page"]')?.getAttribute('href') === href &&
      document.querySelector('.deals-pane .dc-title')?.textContent === name, [row.href, row.name]);
    opened++;
  }
  const open = await tab.evaluate(() => ({rail: document.querySelector('.deals-head__title').tagName, heading: document.activeElement?.closest('.deals-pane') !== null, rows: document.querySelectorAll('.deals-row').length}));
  check(opened === 30 && open.rows === 30 && open.rail === 'H2', `each of the 30 rows opens its address beside the rail, which stays (${opened} opened; the rail title is ${open.rail})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('deals: a slow card list', async () => {
  serveBook();
  stub.steer = (method, path) => path === '/api/v1/accounts/acc-003/cards' ? {delay: 1000} : null;
  const tab = await page({width: 768});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await until(tab, () => document.querySelectorAll('.deals-row').length === 30);
  const row = '.deals-row[data-account="acc-003"] .deals-row__card';
  const seen = new Set();
  for (let i = 0; i < 60; i++) {
    seen.add((await tab.innerText(row)).replace(/\s+/g, ' ').trim());
    if (![...seen].at(-1).startsWith('Reading') && i > 0) break;
    await tab.waitForTimeout(50);
  }
  const others = (await rowFacts(tab)).filter(item => item.account !== 'acc-003');
  check([...seen].join('|') === `Reading|Card published ${utc(bookTime('acc-003'))}`, `the slow row says Reading, then only its own time (${[...seen].join(' | ')})`);
  check(others.every(item => item.card === `Card published ${utc(bookTime(item.account))}`), 'every other row shows its own time');
  await tab.context().close();
});

await scenario('deals: reading', async () => {
  stub.steer = (method, path) => path === '/api/v1/accounts' ? {delay: 1500} : null;
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-reading');
  const shown = await tab.evaluate(() => ({placeholders: document.querySelectorAll('.deals-placeholder').length, status: document.querySelector('.deals-reading [role=status]')?.textContent,
    rows: document.querySelectorAll('.deals-row').length}));
  check(shown.placeholders === 6 && shown.status === 'Reading your deals' && shown.rows === 0, `while the list is read: ${shown.placeholders} placeholder rows and "${shown.status}"`);
  await tab.waitForSelector('.deals-row');
  await tab.context().close();
});

await scenario('deals: a refused account list', async () => {
  stub.steer = (method, path) => path === '/api/v1/accounts' ? {status: 403, code: 'not_permitted'} : null;
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-list [data-refusal="not_permitted"]');
  const shown = await tab.evaluate(() => ({text: document.querySelector('.deals-list').innerText, rows: document.querySelectorAll('.deals-row, .deals-placeholder').length}));
  check(shown.text.includes('Not permitted.') && shown.text.includes('Server code: not_permitted') && shown.rows === 0 && !/No deal/.test(shown.text),
    `a refused account list shows "Not permitted" with its code and no row (${shown.rows} rows)`);
  await tab.context().close();
});

await scenario('deals: one refused card list', async () => {
  serveBook();
  stub.steer = (method, path) => path === '/api/v1/accounts/acc-005/cards' ? {status: 403, code: 'not_permitted'} : null;
  const tab = await page({width: 1440});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await until(tab, () => document.querySelectorAll('.deals-row').length === 30 && ![...document.querySelectorAll('.deals-row__card')].some(card => card.textContent === 'Reading'));
  const rows = await rowFacts(tab);
  const refused = rows.find(row => row.account === 'acc-005');
  check(refused.card === 'Not permitted Server code: not_permitted' && rows.filter(row => row.card.startsWith('Card published')).length === 29,
    `a refused card list shows "Not permitted" on that row only (${refused.card})`);
  await tab.context().close();
});

await scenario('deals: empty for a demo principal, then read again', async () => {
  stub.accounts = [];
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-empty');
  const text = (await tab.innerText('.deals-empty')).replace(/\s+/g, ' ').trim();
  check(text === 'No deals yet. This demo install has no cards. Run make demo, then read again. Read again', `a demo principal with no deals is told to run make demo (${text})`);
  await tab.evaluate(() => {window.__sameDocument = true;});
  const before = stub.log.filter(entry => entry === 'GET /api/v1/accounts').length;
  serveBook();
  await tab.click('.deals-empty button');
  await until(tab, () => document.querySelectorAll('.deals-row').length === 30);
  const after = stub.log.filter(entry => entry === 'GET /api/v1/accounts').length;
  check(after === before + 1 && await tab.evaluate(() => window.__sameDocument === true), `Read again reads the list once more without reloading, and the rows appear (${before} then ${after} reads)`);
  await tab.context().close();
});

await scenario('deals: empty for any other principal', async () => {
  stub.accounts = [];
  stub.principal = customer;
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-empty');
  const text = (await tab.innerText('.deals-empty')).replace(/\s+/g, ' ').trim();
  check(text === 'No deals are granted to you. A grant names the deals a person reads.' && await tab.locator('.fr-demo:visible').count() === 0,
    `a principal that is not a demo one with no deals is told what a grant is (${text})`);
  await tab.context().close();
});

await scenario('deals: the first-run hint', async () => {
  const named = `Wren ${hostile} LLP`;
  stub.accounts = [{account: 'acc-002', name: `Second ${hostile} Co.`}, {account: 'acc-003', name: named}];
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-hint');
  const hint = await tab.evaluate(() => ({text: document.querySelector('.deals-hint__text').textContent, link: document.querySelector('.deals-hint__link').textContent,
    href: document.querySelector('.deals-hint__link').getAttribute('href'), injected: Boolean(window.__injected) || Boolean(document.querySelector('main img')),
    above: document.querySelector('.deals-hint').getBoundingClientRect().bottom <= document.querySelector('.deals-rows').getBoundingClientRect().top}));
  check(hint.text === `New here? Open ${named}, the deal the README walks through.` && hint.link === named && hint.href === '/deals/acc-003' && !hint.injected && hint.above,
    `the hint names the README's deal by the server's string, as text, above the rows (${hint.text})`);
  const facts = await measure(tab, dealParts);
  check(healthy(facts), `390: the list with the hint, ${reported('390 hint', facts)}`);
  await tab.click('.deals-hint__dismiss');
  await tab.waitForSelector('.deals-hint', {state: 'detached'});
  await tab.reload({waitUntil: 'networkidle'});
  await tab.waitForSelector('.deals-row');
  const kept = await tab.evaluate(() => ({hint: document.querySelectorAll('.deals-hint').length, local: Object.keys(localStorage), session: Object.keys(sessionStorage)}));
  check(kept.hint === 0 && kept.local.join() === 'gtm-connector-deals-hint' && kept.session.length === 0,
    `Dismiss hides the hint across a reload, and storage holds only that display key (local ${kept.local.join()}; session ${kept.session.join()})`);
  await tab.context().close();
  stub.principal = customer;
  const other = await page({width: 390});
  await other.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(other);
  await other.waitForSelector('.deals-row');
  check(await other.locator('.deals-hint').count() === 0, 'a principal with demo false sees no hint');
  await other.context().close();
});

await scenario('deals: keys and the filter', async () => {
  serveBook();
  const tab = await page({width: 768});
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await until(tab, () => document.querySelectorAll('.deals-row').length === 30);
  const names = byName(book).map(item => item.account);
  const at = () => tab.evaluate(() => document.activeElement?.dataset.account ?? document.activeElement?.tagName);
  await tab.focus('input[name=q]');
  const moves = [];
  for (const key of ['ArrowDown', 'ArrowDown', 'End', 'ArrowDown', 'Home', 'ArrowUp']) {
    await tab.keyboard.press(key);
    moves.push(await at());
  }
  check(moves.join() === [names[0], names[1], names[29], names[29], names[0], names[0]].join(), `Down, End, Home and Up move between rows (${moves.join(', ')})`);
  await tab.keyboard.press('ArrowDown');
  const focus = await ring(tab);
  const row = await tab.evaluate(() => document.activeElement.getBoundingClientRect().width);
  check(focus.height >= 56 && focus.outline >= 3 && row >= 600, `focus shows on the whole row (${Math.round(row)}x${Math.round(focus.height)}, ${focus.outline}px outline)`);
  await tab.keyboard.press('Enter');
  await until(tab, href => location.pathname === href && document.querySelector('.dc-title'), `/deals/${names[1]}`);
  check(await tab.locator('.deals-back').isVisible() && await tab.locator('.deals-row').count() === 0, 'Enter opens the row; under 1100 the deal is the whole page, with a link back');
  await tab.click('.deals-back');
  await until(tab, () => location.pathname === '/deals' && document.querySelectorAll('.deals-row').length === 30);
  await tab.focus('input[name=q]');
  await tab.keyboard.type('thorn');
  await until(tab, () => document.querySelectorAll('.deals-row').length === 1);
  const narrowed = await tab.evaluate(() => ({q: new URL(location.href).searchParams.get('q'), value: document.querySelector('input[name=q]').value, count: document.querySelector('.deals-head__count').textContent,
    name: document.querySelector('.deals-row__name').textContent, href: document.querySelector('.deals-row').getAttribute('href')}));
  check(narrowed.q === 'thorn' && narrowed.value === 'thorn' && narrowed.count === '1 of 30' && narrowed.name === 'Thornfield Wren LLP' && narrowed.href === '/deals/acc-003?q=thorn',
    `typing narrows the rows as typed and keeps the filter in the address (?q=${narrowed.q}, ${narrowed.count}, ${narrowed.name})`);
  await tab.click('.fr-areas .fr-link:has-text("Deals")');
  await until(tab, () => !location.search && document.querySelector('input[name=q]').value === '' && document.querySelectorAll('.deals-row').length === 30);
  check(true, 'the Deals link in the top bar opens the list unfiltered, and the field follows the address');
  await tab.fill('input[name=q]', 'no deal by this name');
  await tab.waitForSelector('.deals-empty');
  const none = (await tab.innerText('.deals-empty')).replace(/\s+/g, ' ').trim();
  await tab.click('.deals-empty button');
  await until(tab, () => document.querySelectorAll('.deals-row').length === 30);
  const cleared = await tab.evaluate(() => ({q: new URL(location.href).searchParams.has('q'), focus: document.activeElement?.name}));
  check(none === 'No deal matches. Clear the filter' && !cleared.q && cleared.focus === 'q', `a filter that matches nothing says so, and Clear the filter brings the rows back (${none})`);
  check(tab.errors.length === 0, `no page errors (${tab.errors.join(' | ')})`);
  await tab.context().close();
});

for (const width of [320, 390, 768, 1440]) {
  for (const scheme of ['light', 'dark']) {
    await scenario(`deals layout ${width} ${scheme}`, async () => {
      serveBook();
      const at = `deals ${width} ${scheme}`;
      const tab = await page({width, scheme});
      await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
      await signIn(tab);
      await until(tab, () => document.querySelectorAll('.deals-row').length === 30 && ![...document.querySelectorAll('.deals-row__card')].some(card => card.textContent === 'Reading'));
      const facts = await measure(tab, dealParts);
      check(healthy(facts), `${at}: ${reported(at, facts)}`);
      const rows = await rowFacts(tab);
      const readme = rows.find(row => row.account === 'acc-003');
      if (width < 720) check(rows.every(row => row.cardBelow) && readme.oneLine, `${at}: a row is two lines, the name and ID, then the card time`);
      else if (width < 1100) check(rows.every(row => !row.cardBelow), `${at}: a row holds the card time at its right`);
      else check(rows.every(row => row.cardBelow && row.width <= 336), `${at}: in the rail a row is two lines`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `deals-${width}-${scheme}.png`)});
      check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
      await tab.context().close();
    });
  }
}

// 16. The Deal card: the stub's acc-003 card, with hostile speakers and gaps, at
// 320, 390, 768 and 1440 wide, light and dark; the countdown and ARR, their
// help, a changed clock, a card with no lines; refusals; a card of another
// account; an older card.
const dealCard = cards['acc-003'];
const shownUnits = dealCard.units.filter(unit => unit.kind === 'sentence' && unit.shown === 'yes' && !unit.body.partial);
const cardParts = '.fr-chrome, .dc';
for (const [width, scheme] of [320, 390, 768, 1440].flatMap(width => [[width, 'light'], [width, 'dark']])) {
  await scenario(`deal card ${width} ${scheme}`, async () => {
    const at = `deal card ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    const facts = await tab.evaluate(() => {
      const box = selector => document.querySelector(selector).getBoundingClientRect();
      const lefts = selector => [...new Set([...document.querySelectorAll(selector)].map(el => Math.round(el.getBoundingClientRect().left)))];
      const rights = selector => [...new Set([...document.querySelectorAll(selector)].map(el => Math.round(el.getBoundingClientRect().right)))];
      const record = box('.dc-record'), words = box('.dc-words');
      return {title: document.title, heading: document.querySelector('.dc-title').textContent, id: document.querySelector('.dc-id').textContent,
        note: document.querySelector('.dc-values-note')?.textContent ?? null,
        focal: [...document.querySelectorAll('.dc-focal__item')].map(el => ({
          kind: el.dataset.focal, number: el.querySelector('.dc-focal__number').textContent, size: getComputedStyle(el.querySelector('.dc-focal__number')).fontSize,
          figure: el.querySelector('.dc-focal__figure').innerText.replace(/\s+/g, ' ').trim(), subs: [...el.querySelectorAll('.dc-focal__sub')].map(sub => sub.innerText.replace(/\s+/g, ' ').trim()),
          left: Math.round(el.getBoundingClientRect().left), right: Math.round(el.getBoundingClientRect().right),
          top: Math.round(el.querySelector('.dc-focal__number').getBoundingClientRect().top),
          lines: Math.round(el.querySelector('.dc-focal__number').getBoundingClientRect().height / parseFloat(getComputedStyle(el.querySelector('.dc-focal__number')).lineHeight))})),
        cardWidth: document.querySelector('.dc').getBoundingClientRect().width,
        focalSizes: [...document.querySelectorAll('.dc *')].filter(el => ['40px', '32px'].includes(getComputedStyle(el).fontSize)).map(el => el.className),
        focalFirst: box('.dc-focal').top < box('.dc-ledger').top && box('.dc-head').bottom <= box('.dc-focal').top,
        values: [...document.querySelectorAll('.dc-the-record [data-value]')].map(el => `${el.dataset.value}: ${el.querySelector('.dc-value__label').textContent} ${el.querySelector('.dc-value__text').textContent}`),
        flags: [...document.querySelectorAll('.dc-flags .dc-flag')].map(el => el.innerText.replace(/\s+/g, ' ').trim()),
        titles: [...document.querySelectorAll('[data-group="customer"] .dc-attr__title')].map(el => el.textContent),
        quotes: [...document.querySelectorAll('blockquote')].map(el => ({n: Number(el.dataset.unit), text: el.textContent, children: el.children.length,
          face: getComputedStyle(el).fontFamily.split(',')[0], size: getComputedStyle(el).fontSize, rule: `${getComputedStyle(el).borderLeftWidth} ${getComputedStyle(el).borderLeftStyle}`})),
        injected: Boolean(window.__injected) || Boolean(document.querySelector('.dc img')),
        rule: Math.round(record.right), border: getComputedStyle(document.querySelector('.dc-record')).borderRightWidth,
        recordLeft: Math.round(record.left), wordsLeft: Math.round(words.left), recordRights: rights('.dc-record .dc-line__n'), wordLefts: lefts('.dc-words .dc-people .dc-line__n'),
        peopleFirst: box('.dc-people').top < box('.dc-the-record').top, recordBeforeNotRead: box('.dc-the-record').bottom <= box('.dc-not-read').top,
        local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
    });
    check(facts.heading === 'Third Account Co.' && facts.title === 'Third Account Co.' && facts.id === 'acc-003' && facts.note === null,
      `${at}: the title is the server’s name, the tab is titled with it, and the ID is under it`);
    const [countdown, arr] = facts.focal;
    const phone = facts.cardWidth < 720, narrow = width < 720;
    check(facts.focalFirst && countdown?.kind === 'countdown' && countdown.figure === '24 days [1]' && countdown.subs.join(' | ') === 'to the renewal, 2027-02-08 | as of 2027-01-15' &&
      arr?.kind === 'arr' && arr.figure === 'USD 450,000.00 [1]' && arr.subs.join() === 'ARR, from the mapped field',
      `${at}: under the title, the countdown and ARR with their numbers and sub-lines (${JSON.stringify(facts.focal.map(item => [item.figure, item.subs]))})`);
    // ARR sits beside the countdown on its first line, and goes under it only on a card too narrow for its figure whole
    // (under 330 wide for USD 450,000.00 at 32); its figure breaks only at its space.
    const beside = arr.left >= countdown.right && Math.abs(arr.top - countdown.top) <= 2;
    check((beside || facts.cardWidth < 330) && arr.lines <= 2 && facts.focal.every(item => item.size === (phone ? '32px' : '40px')),
      `${at}: ARR sits ${beside ? 'right of' : 'under'} the countdown, its figure on ${arr.lines} lines, both at ${phone ? 32 : 40} (card ${Math.round(facts.cardWidth)}; ${countdown.right}..${arr.left}; tops ${countdown.top} ${arr.top}; ${facts.focal.map(item => item.size)})`);
    check(facts.focalSizes.length === 2 && facts.focalSizes.every(name => name.startsWith('dc-focal__number')), `${at}: only the countdown and ARR use the focal step (${facts.focalSizes.join()})`);
    check(facts.values.join(' | ') === dealCard.lines.filter(line => line.section === 'value').map(line => `${line.code}: ${line.label} ${line.text}`).join(' | '),
      `${at}: the record column is every value line in the card's order (${facts.values.length})`);
    check(facts.flags.join() === 'The renewal is in 24 days and this opportunity is still open at stage "negotiation". [1] [3]' && facts.titles.join() === 'Deputy general counsel,Deputy general counsel',
      `${at}: Flags holds the fixed signal with its numbers, and each buyer quote the contact's title (${facts.flags.join()}; ${facts.titles.join()})`);
    const expected = shownUnits.map(unit => `${unit.n} ${unit.body.text}`).sort().join('|');
    check(facts.quotes.map(quote => `${quote.n} ${quote.text}`).sort().join('|') === expected && facts.quotes.every(quote => quote.children === 0) && !facts.injected,
      `${at}: each shown sentence is one blockquote holding exactly its text, and hostile speakers and gaps stay text`);
    check(facts.quotes.every(quote => quote.face === 'Charter' || quote.face.includes('Charter') || quote.face.includes('Georgia')) && facts.quotes.every(quote => quote.size === '18px' && quote.rule === '3px solid'),
      `${at}: a quotation is in the quotation face at 18 with a 3px rule (${facts.quotes[0].face} ${facts.quotes[0].size} ${facts.quotes[0].rule})`);
    if (facts.recordLeft < facts.wordsLeft) {
      // The record's numbers end at the 1px rule, and the words' numbers start where it ends.
    check(facts.recordLeft < facts.wordsLeft && facts.border === '1px' && facts.recordRights.length === 1 && facts.rule - facts.recordRights[0] === 1 && facts.wordLefts.join() === String(facts.rule),
        `${at}: the record and the words sit either side of the rule, their numbers in a line against it (rule ${facts.rule}; record numbers end ${facts.recordRights}; words numbers start ${facts.wordLefts})`);
    } else {
      check(facts.peopleFirst && facts.recordBeforeNotRead, `${at}: People said comes before The record, then Not read`);
    }
    const measured = await measure(tab, cardParts);
    check(healthy(measured), `${at}: ${reported(at, measured)}`);
    // Every number takes keyboard focus with the 3px ring, and is 44 by 44.
    await tab.focus('.dc-cite');
    await tab.keyboard.press('Shift+Tab');
    await tab.keyboard.press('Tab');
    const focus = await ring(tab);
    const sizes = await tab.$$eval('.dc-cite', els => els.map(el => el.getBoundingClientRect()).map(box => [box.width, box.height]));
    check(fits(focus) && focus.name.startsWith('Source') && sizes.every(([w, h]) => w >= 44 && h >= 44), `${at}: each of ${sizes.length} numbers is at least 44 by 44, and focus shows a ${focus.outline}px ring`);
    check(facts.local.every(key => ['gtm-connector-theme', 'gtm-connector-deals-hint', 'gtm-connector-proof-seen'].includes(key)) && facts.session.length === 0,
      `${at}: storage holds only display keys (local ${facts.local.join()}; session ${facts.session.join()})`);
    // The countdown's help: focus shows it below its line, Escape hides it and focus stays on its button.
    const help = '[data-focal="countdown"] .ui-help__button';
    await tab.focus(help);
    const opened = await tab.evaluate(selector => {
      const button = document.querySelector(selector), panel = document.getElementById(button.getAttribute('aria-controls'));
      const line = button.closest('.dc-focal__sub').getBoundingClientRect(), box = panel.getBoundingClientRect(), own = button.getBoundingClientRect();
      return {expanded: button.getAttribute('aria-expanded'), shown: !panel.hidden && box.height > 0, role: panel.getAttribute('role'), text: panel.querySelector('.ui-help__text').textContent,
        below: box.top >= own.bottom - 1, inFlow: getComputedStyle(panel).position === 'static', lineShown: line.height > 0, name: button.getAttribute('aria-label'), size: [own.width, own.height]};
    }, help);
    const focusRing = await ring(tab);
    await tab.keyboard.press('Escape');
    const closed = await tab.evaluate(selector => {const button = document.querySelector(selector); return {expanded: button.getAttribute('aria-expanded'), hidden: document.getElementById(button.getAttribute('aria-controls')).hidden, focused: document.activeElement === button};}, help);
    check(opened.expanded === 'true' && opened.shown && opened.role === 'note' && opened.below && opened.inFlow === narrow && opened.name === 'About the countdown' &&
      opened.text.startsWith('Days from the card') && fits(focusRing) && closed.expanded === 'false' && closed.hidden && closed.focused,
      `${at}: the countdown's help opens on focus below its line${narrow ? ', in the flow' : ''}, and Escape closes it with focus on its button (${JSON.stringify(opened)}; ${JSON.stringify(closed)})`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `deal-card-${width}-${scheme}.png`), fullPage: true});
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

// 17. The proof drawer: a number opens the unit behind it, and ?unit= in the address opens the same drawer.
const unitText = n => dealCard.units.find(unit => unit.n === n).body.text;
const drawerFacts = tab => tab.evaluate(() => {
  const dialog = document.querySelector('[role=dialog]'), box = dialog.getBoundingClientRect(), close = document.querySelector('.ui-dialog__close').getBoundingClientRect();
  const card = document.querySelector('.dc').getBoundingClientRect(), style = getComputedStyle(dialog);
  return {title: document.querySelector('.ui-dialog__title').textContent, focus: document.activeElement === document.querySelector('.ui-dialog__title'), modal: dialog.getAttribute('aria-modal'),
    quote: dialog.querySelector('blockquote')?.textContent ?? null, quoteChildren: dialog.querySelector('blockquote')?.children.length ?? null, quotes: dialog.querySelectorAll('blockquote').length,
    text: dialog.querySelector('.ui-dialog__body').innerText.replace(/\s+/g, ' ').trim(), read: dialog.querySelector('.dc-proof__read')?.textContent ?? null,
    injected: Boolean(window.__injected) || Boolean(dialog.querySelector('img')), unit: new URL(location.href).searchParams.get('unit'),
    box: {left: box.left, top: box.top, width: box.width, height: box.height}, close: {width: close.width, height: close.height},
    background: style.backgroundColor, shadow: style.boxShadow, cardRight: card.right, cardWidth: card.width, viewport: {width: innerWidth, height: innerHeight},
    rail: Boolean(document.querySelector('.deals-rail')?.getClientRects().length),
    local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
});
const atCite = (tab, n) => until(tab, number => document.activeElement?.matches(`.dc-line__n > .dc-cite[data-cite="${number}"]`), String(n));
for (const [width, scheme] of [[390, 'light'], [1440, 'light'], [390, 'dark'], [1440, 'dark']]) {
  await scenario(`proof drawer ${width} ${scheme}`, async () => {
    const at = `proof drawer ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await tab.goto(`${thornfield}?unit=5`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('[role=dialog] [data-proof="unit"]');
    await until(tab, () => document.activeElement?.matches('.ui-dialog__title'));
    const facts = await drawerFacts(tab);
    check(facts.title === 'Source [5]' && facts.focus && facts.quote === unitText(5) && facts.quoteChildren === 0 && facts.quotes === 1,
      `${at}: /deals/acc-003?unit=5 opens the drawer on the recorded sentence of unit 5, character for character, with focus on the head (${facts.title})`);
    check(!facts.injected && facts.text.includes(`Marian Holt ${hostile}`) && facts.text.includes('Customer (label matches contact [2])') && facts.text.includes('On the card'),
      `${at}: the hostile speaker name renders as text, with the role and its basis as recorded`);
    check(facts.read === 'Read 15 Jan 2027, 15:00:00 UTC from calls' && !/receipt-|read_call_notes|op-|calls:read|[0-9a-f]{64}/.test(facts.text),
      `${at}: the read line gives the read's start and source, and no receipt ID, digest or tool name (${facts.read})`);
    check(/After \[6\] Please do not put a date on the order form\./.test(facts.text) && !facts.text.includes('Before'), `${at}: Around it shows unit 6 after it and nothing before it`);
    if (width < 720) {
      check(facts.box.left === 0 && facts.box.top === 0 && facts.box.width === facts.viewport.width && facts.box.height === facts.viewport.height && facts.close.width >= 44 && facts.close.height >= 44 && facts.modal === 'true',
        `${at}: the sheet covers the viewport (${facts.box.width}x${facts.box.height}) and its close button is ${facts.close.width}x${facts.close.height}`);
    } else {
      check(facts.box.width === 480 && facts.box.height === facts.viewport.height && facts.box.left + facts.box.width === facts.viewport.width && facts.shadow !== 'none' && facts.modal === 'false',
        `${at}: a panel 480 wide on the right, full height, with the shadow, beside the card (${facts.box.width}x${facts.box.height})`);
      check(facts.cardRight <= facts.box.left && facts.cardWidth >= 760 && !facts.rail, `${at}: the list rail steps aside and the card, ${Math.round(facts.cardWidth)} wide, ends at ${Math.round(facts.cardRight)}, left of the drawer at ${facts.box.left}`);
    }
    const measured = await measure(tab, `${cardParts}, [role=dialog]`);
    check(healthy(measured), `${at}: ${reported(at, measured)}`);
    check(facts.local.includes('gtm-connector-proof-seen') && facts.local.every(key => ['gtm-connector-theme', 'gtm-connector-deals-hint', 'gtm-connector-proof-seen'].includes(key)) && facts.session.length === 0,
      `${at}: opening a drawer is kept as a display choice, and storage holds only display keys (${facts.local.join()})`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `proof-${width}-${scheme}.png`)});
    // Escape closes it, removes ?unit= and puts focus back on the [5] box.
    await tab.keyboard.press('Escape');
    await tab.waitForSelector('[role=dialog]', {state: 'detached'});
    await atCite(tab, 5);
    check(!new URL(tab.url()).searchParams.has('unit'), `${at}: Escape closes the drawer, removes ?unit= and returns focus to the [5] box`);
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

await scenario('deal card: changing the browser’s clock changes nothing on the page', async () => {
  const read = async time => {
    const tab = await page({width: 1440});
    await tab.clock.setFixedTime(new Date(time));
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-focal');
    const shown = await tab.innerText('.dc');
    const now = await tab.evaluate(() => new Date().toISOString());
    await tab.context().close();
    return {shown, now};
  };
  // The stub's session runs an hour from now, so both clocks sit before its end.
  const early = await read('2020-02-29T09:00:00Z'), late = await read(new Date(Date.now() + 30 * 60_000).toISOString());
  check(early.now.startsWith('2020-02-29') && early.shown === late.shown && /\b24\s+days\b/.test(early.shown),
    `the card reads the same with the clock at ${early.now} and at ${late.now}`);
});

await scenario('deal card: help opens on hover and on a press, one at a time, and a press outside closes it', async () => {
  const tab = await page({width: 1440});
  await tab.goto(thornfield, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.dc-focal');
  const state = () => tab.$$eval('.dc-focal .ui-help__button', buttons => buttons.map(button => button.getAttribute('aria-expanded')).join());
  await tab.hover('[data-focal="arr"] .ui-help__button');
  const hovered = await state();
  await tab.hover('[data-focal="arr"] .ui-help__panel');
  const onPanel = await state();
  await tab.click('[data-focal="countdown"] .ui-help__button');
  const pressed = await state();
  await tab.mouse.move(5, 5);
  const pinned = await state();
  const guide = await tab.getAttribute('[data-focal="countdown"] .ui-help__guide', 'href');
  await tab.mouse.click(5, 300);
  const outside = await state();
  check(hovered === 'false,true' && onPanel === 'false,true' && pressed === 'true,false' && pinned === 'true,false' && outside === 'false,false' &&
    guide === 'https://github.com/pwngh/gtm-connector/blob/main/docs/config.md#field-mapping',
    `hover shows ARR's help and the pointer may move onto it, a press pins the countdown's and closes ARR's, a press outside closes it (${[hovered, onPanel, pressed, pinned, outside].join(' / ')})`);
  await tab.context().close();
});

await scenario('deal card: a card published before its lines were kept', async () => {
  stub.steer = (method, path) => path === '/api/v1/cards/card-acc-003' ? {json: {...dealCard, lines: null}} : null;
  const tab = await page({width: 390});
  await tab.goto(thornfield, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.dc-ledger');
  const note = (await tab.innerText('.dc-values-note')).replace(/\s+/g, ' ').trim();
  const link = await tab.getAttribute('.dc-values-note a', 'href');
  const drawn = await tab.locator('.dc-focal, .dc-flags, [data-value], .dc-attr__title').count();
  check(note === 'The countdown and ARR are on the plain-text card.' && link === '/deals/acc-003/text' && drawn === 0,
    `the card says where its values are and links to its Text tab (${note}; ${link}; ${drawn} line parts drawn)`);
  await tab.context().close();
});

await scenario('proof drawer at 1100: pressing numbers, Left and Right, Before and After, and the close button', async () => {
  const tab = await page({width: 1100});
  await tab.goto(thornfield, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.dc-ledger');
  const hint = await tab.evaluate(() => ({text: document.querySelector('.dc-proof-hint')?.textContent,
    above: document.querySelector('.dc-proof-hint')?.getBoundingClientRect().bottom <= document.querySelector('.dc-ledger').getBoundingClientRect().top}));
  check(hint.text === 'Select a number to see its source.' && hint.above, 'before any drawer is opened, one line above the ledger says what a number does');
  await tab.click('.dc-line__n > .dc-cite[data-cite="5"]');
  await tab.waitForSelector('[role=dialog] [data-proof="unit"]');
  await until(tab, () => document.activeElement?.matches('.ui-dialog__title'));
  check(new URL(tab.url()).searchParams.get('unit') === '5' && await tab.locator('.dc-proof-hint').count() === 0 && await tab.locator('.dc-cite[data-open]').count() === 1,
    'pressing [5] puts ?unit=5 in the address, opens the drawer with focus on its head, marks [5] and drops the hint');
  const beside = await tab.evaluate(() => ({card: document.querySelector('.dc').getBoundingClientRect().toJSON(), drawer: document.querySelector('[role=dialog]').getBoundingClientRect().left}));
  check(beside.card.right <= beside.drawer && beside.card.width >= 520, `at 1100 the card, ${Math.round(beside.card.width)} wide, stays readable left of the drawer`);
  const walk = [];
  for (const [key, n] of [['ArrowRight', 6], ['ArrowRight', 7], ['ArrowLeft', 6]]) {
    await tab.keyboard.press(key);
    await until(tab, number => new URL(location.href).searchParams.get('unit') === number && document.querySelector('.ui-dialog__title')?.textContent === `Source [${number}]`
      && document.querySelector('[role=dialog] [data-proof="unit"]'), String(n));
    walk.push(`${new URL(tab.url()).searchParams.get('unit')}:${await tab.textContent('.ui-dialog__title')}`);
  }
  check(walk.join() === '6:Source [6],7:Source [7],6:Source [6]', `Right, Right, Left move through the card's units and the address follows (${walk.join(', ')})`);
  await tab.click('[role=dialog] [data-near="before"] a');
  await until(tab, () => new URL(location.href).searchParams.get('unit') === '5' && document.querySelector('[role=dialog] [data-proof="unit"] blockquote[data-unit="5"]'));
  check(await tab.textContent('[role=dialog] blockquote') === unitText(5), 'Before shows unit 5 in place');
  // Beside the card, pressing another number on the card replaces the content and keeps the drawer open.
  await tab.click('.dc-line__n > .dc-cite[data-cite="8"]');
  await until(tab, () => new URL(location.href).searchParams.get('unit') === '8' && document.querySelector('[role=dialog] [data-proof="unit"] blockquote[data-unit="8"]'));
  check(await tab.textContent('[role=dialog] blockquote') === unitText(8) && await tab.locator('[role=dialog]').count() === 1, 'pressing [8] on the card shows unit 8 in the same drawer');
  // A record: its reference and the line on its fields.
  await tab.click('.dc-line__n > .dc-cite[data-cite="2"]');
  await until(tab, () => document.querySelector('[role=dialog] .dc-proof__record'));
  const record = (await tab.innerText('[role=dialog] [data-proof="unit"]')).replace(/\s+/g, ' ').trim();
  check(record === 'Record crm/contacts/con-003 This server returns a record’s reference, not its fields yet. On the card Read 15 Jan 2027, 15:00:00 UTC from the CRM',
    `a record unit shows its reference and says its fields are not served yet (${record})`);
  await tab.click('.ui-dialog__close');
  await tab.waitForSelector('[role=dialog]', {state: 'detached'});
  await atCite(tab, 2);
  check(!new URL(tab.url()).searchParams.has('unit'), 'the close button closes it, removes ?unit= and returns focus to the number last pressed');
  await tab.reload({waitUntil: 'networkidle'});
  await tab.waitForSelector('.dc-ledger');
  check(await tab.locator('.dc-proof-hint').count() === 0, 'after a drawer has been opened, the hint stays away across a reload');
  check(tab.errors.length === 0 && violations(tab).length === 0, `no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('proof drawer: a unit the card lacks, reading, and refusals', async () => {
  const tab = await page({width: 390});
  await tab.goto(`${thornfield}?unit=12`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('[role=dialog] [data-proof="missing"]');
  await apiSettled();
  const missing = await tab.evaluate(() => ({text: document.querySelector('[role=dialog] .ui-dialog__body').innerText.trim(), quotes: document.querySelectorAll('[role=dialog] blockquote, [role=dialog] .dc-ref').length}));
  check(missing.text === 'This card has no unit 12. No other unit is shown in its place.' && missing.quotes === 0 && !stub.log.some(entry => entry.includes('/units/')),
    `a number the card lacks says so, shows no other unit and reads nothing (${missing.text})`);
  await tab.context().close();
  // While the unit is read, the drawer says so.
  stub.steer = (method, path) => path.endsWith('/units/5') ? {delay: 1500} : null;
  const slow = await page({width: 1440});
  await slow.goto(`${thornfield}?unit=5`, {waitUntil: 'domcontentloaded'});
  await signIn(slow);
  await slow.waitForSelector('[role=dialog] [data-proof="reading"]');
  check((await slow.innerText('[role=dialog] [data-proof="reading"]')).trim() === 'Reading unit 5', 'while the unit is read, the drawer says Reading unit 5');
  await slow.waitForSelector('[role=dialog] [data-proof="unit"]');
  await slow.context().close();
  // A read refused as not permitted: the unit the card holds shows, and the read line alone says so.
  stub.steer = (method, path) => path.endsWith('/units/5') ? {status: 403, code: 'not_permitted'} : null;
  const denied = await page({width: 1440});
  await denied.goto(`${thornfield}?unit=5`, {waitUntil: 'networkidle'});
  await signIn(denied);
  await denied.waitForSelector('[role=dialog] [data-read="refused"]');
  const refusedRead = await denied.evaluate(() => ({read: document.querySelector('.dc-proof__read').textContent, quote: document.querySelector('[role=dialog] blockquote')?.textContent, refusal: document.querySelectorAll('[role=dialog] [data-refusal]').length}));
  check(refusedRead.read === 'Read Not permitted' && refusedRead.quote === unitText(5) && refusedRead.refusal === 0, `a refused read shows the unit with Not permitted on the read line only (${refusedRead.read})`);
  await denied.context().close();
  // Any other refusal is shown as a refusal with the server's code.
  stub.steer = (method, path) => path.endsWith('/units/5') ? {status: 500, code: 'internal'} : null;
  const failed = await page({width: 1440});
  await failed.goto(`${thornfield}?unit=5`, {waitUntil: 'networkidle'});
  await signIn(failed);
  await failed.waitForSelector('[role=dialog] [data-refusal="server"]');
  check((await failed.innerText('[role=dialog] [data-refusal]')).includes('Server code: internal') && await failed.locator('[role=dialog] blockquote').count() === 0,
    'a server error is a refusal with its code, and no unit');
  await failed.context().close();
});

await scenario('deal card: a card of another account in ?card= is not read', async () => {
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals/acc-002?card=card-acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('[data-notice="mismatch"]');
  await apiSettled();
  check(!stub.log.includes('GET /api/v1/cards/card-acc-003') && await tab.locator('.dc-section, blockquote').count() === 0,
    'the other account’s card is neither requested nor shown, and a notice says so');
  await tab.context().close();
});

await scenario('deal card: an older card pinned in ?card=', async () => {
  const {units, gaps, as_of, sources, lines, ...head} = dealCard;
  stub.steer = (method, path) => path === '/api/v1/accounts/acc-003/cards'
    ? {json: {account: 'acc-003', cards: [{...head, id: 'card-acc-003-newer', published_at: '2027-01-16T09:00:00Z'}, head]}} : null;
  const tab = await page({width: 390});
  await tab.goto(`${thornfield}?card=card-acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('[data-notice="older"]');
  const notice = (await tab.innerText('[data-notice="older"]')).replace(/\s+/g, ' ').trim();
  const link = await tab.getAttribute('[data-notice="older"] a', 'href');
  check(notice === 'You are reading the card of 15 Jan 2027, 15:00:04 UTC. A newer card was published 16 Jan 2027, 09:00:00 UTC. Open the newest' && link === '/deals/acc-003',
    `an older card says so and links to the newest (${notice}; ${link})`);
  check(!stub.log.includes('GET /api/v1/cards/card-acc-003-newer'), 'only the pinned card is read');
  await tab.context().close();
});

// 17. The Text tab: the plain-text card whole and copied exactly, at 320, 390
// and 1440 wide; every recorded card byte for byte; a card whose bytes do not
// match its seal; a copy the browser refuses.
const sealedThornfield = sealedFiles.get('card-acc-003');
const sunkenOf = tab => tab.evaluate(() => {const probe = document.createElement('div'); probe.style.background = 'var(--gc-sunken)'; document.body.append(probe); const colour = getComputedStyle(probe).backgroundColor; probe.remove(); return colour;});
const openInApp = (tab, path) => tab.evaluate(to => {history.pushState(null, '', to); dispatchEvent(new PopStateEvent('popstate'));}, path);
for (const [width, scheme] of [[320, 'light'], [390, 'light'], [1440, 'light'], [390, 'dark'], [1440, 'dark']]) {
  await scenario(`text tab ${width} ${scheme}`, async () => {
    const at = `text tab ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await tab.context().grantPermissions(['clipboard-read', 'clipboard-write'], {origin: base});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    await tab.click('.dc-tab[href="/deals/acc-003/text"]');
    await tab.waitForSelector('.dc-plain__text');
    const sunken = await sunkenOf(tab);
    const facts = await tab.evaluate(() => {
      const pre = document.querySelector('.dc-plain__text'), style = getComputedStyle(pre);
      return {path: location.pathname, text: pre.textContent, nodes: pre.childNodes.length, face: style.fontFamily, size: style.fontSize, space: style.whiteSpace, background: style.backgroundColor,
        tabs: [...document.querySelectorAll('.dc-tab')].map(el => `${el.textContent} ${el.getAttribute('href')}${el.getAttribute('aria-current') ? ' open' : ''}`).join(', '),
        overflow: document.documentElement.scrollWidth - innerWidth, wider: pre.scrollWidth - pre.clientWidth, injected: Boolean(window.__injected),
        local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
    });
    check(facts.path === '/deals/acc-003/text' && facts.tabs === 'Card /deals/acc-003, Text /deals/acc-003/text open', `${at}: the Text tab opens the plain-text card (${facts.path}; ${facts.tabs})`);
    check(facts.text === sealedThornfield.text && facts.nodes === 1 && !facts.injected, `${at}: the Text tab holds the sealed text byte for byte, as one text node`);
    check(facts.face.startsWith('ui-monospace') && facts.size === '14px' && facts.space === 'pre-wrap' && facts.background === sunken,
      `${at}: the text is in the reference face at 14 on the sunken surface, white-space kept (${facts.face.split(',')[0]} ${facts.size} ${facts.space} ${facts.background})`);
    check(facts.overflow <= 0 && facts.wider <= 0, `${at}: long lines wrap and the page does not scroll sideways (page ${facts.overflow}px, text ${facts.wider}px)`);
    const measured = await measure(tab, cardParts);
    check(healthy(measured), `${at}: ${reported(at, measured)}`);
    await tab.focus('.dc-plain__bar button');
    await tab.keyboard.press('Shift+Tab');
    await tab.keyboard.press('Tab');
    const focus = await ring(tab);
    check(fits(focus) && focus.name === 'Copy the plain-text card', `${at}: the copy button is 44 by 44 and shows a ${focus.outline}px focus ring`);
    await tab.keyboard.press('Enter');
    await until(tab, () => document.querySelector('.dc-plain__status').textContent !== '');
    const copied = await tab.evaluate(() => navigator.clipboard.readText());
    check(await tab.textContent('.dc-plain__status') === 'Copied' && copied === sealedThornfield.text, `${at}: the button copies the exact string and says Copied`);
    check(facts.local.every(key => ['gtm-connector-theme', 'gtm-connector-deals-hint'].includes(key)) && facts.session.length === 0,
      `${at}: storage holds only display keys (local ${facts.local.join()}; session ${facts.session.join()})`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `deal-text-${width}-${scheme}.png`), fullPage: true});
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

await scenario('text tab: every recorded card byte for byte, and copied the same', async () => {
  serveRecorded();
  const tab = await page({width: 390});
  await tab.context().grantPermissions(['clipboard-read', 'clipboard-write'], {origin: base});
  await tab.goto(`${base}/deals/acc-900/text?card=${recorded[0].id}`, {waitUntil: 'networkidle'});
  await signIn(tab);
  const wrong = [];
  for (const card of recorded) {
    const {text} = sealedFiles.get(card.id);
    const path = `/deals/acc-900/text?card=${card.id}`;
    await openInApp(tab, path);
    await until(tab, to => document.querySelector('.dc-tab[aria-current="page"]')?.getAttribute('href') === to && document.querySelector('.dc-plain__text'), path);
    const shown = await tab.textContent('.dc-plain__text');
    await tab.click('.dc-plain__bar button');
    await until(tab, () => document.querySelector('.dc-plain__status').textContent !== '');
    const said = await tab.textContent('.dc-plain__status');
    const copied = await tab.evaluate(() => navigator.clipboard.readText());
    if (shown !== text || copied !== text || said !== 'Copied') wrong.push(card.id);
  }
  check(recorded.length === 450 && wrong.length === 0, `each of ${recorded.length} recorded cards shows its sealed text byte for byte and copies the same string (wrong: ${wrong.slice(0, 5).join(', ') || 'none'})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `recorded cards: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

for (const [width, scheme] of [[390, 'light'], [1440, 'dark']]) {
  await scenario(`seal check fails ${width} ${scheme}`, async () => {
    const at = `seal check fails ${width} ${scheme}`;
    const altered = Buffer.from(sealedThornfield.bytes);
    altered[40] ^= 1;
    const {units, gaps, as_of, sources, lines, ...head} = dealCard;
    const older = {...head, id: 'card-acc-003-older', published_at: '2027-01-12T09:10:00Z'};
    fileOf(older.id, oracle['acc-003']);
    stub.extra = [{...dealCard, ...older}];
    stub.lists['acc-003'] = [head, older];
    stub.steer = (method, path) => path === '/api/v1/cards/card-acc-003/bytes'
      ? {raw: altered, type: 'application/octet-stream', headers: {'X-Content-SHA256': sealedThornfield.sha256}} : null;
    const tab = await page({width, scheme});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('[data-seal="failed"]');
    await apiSettled();
    const facts = await tab.evaluate(() => ({panel: document.querySelector('[data-seal="failed"]').innerText.replace(/\s+/g, ' ').trim(), page: document.querySelector('.dc').innerText,
      link: document.querySelector('[data-seal="failed"] a')?.getAttribute('href'), drawn: document.querySelectorAll('.dc-section, .dc-tabs, .dc-plain, .dc-values-note, .dc-focal, blockquote, .dc-ledger').length}));
    check(facts.panel === 'This card failed its seal check. Nothing from it is shown. Card card-acc-003, published 15 Jan 2027, 15:00:04 UTC Open the card before it' &&
      facts.link === '/deals/acc-003?card=card-acc-003-older' && facts.drawn === 0, `${at}: one panel names the card and links to the one before it, and nothing of the card is drawn (${facts.panel}; ${facts.link})`);
    const pieces = [...units.map(unit => unit.body.text).filter(Boolean), ...gaps.map(gap => gap.text), ...sealedThornfield.text.split('\n').map(line => line.trim()).filter(line => line.length > 3)];
    const leaked = pieces.filter(piece => facts.page.includes(piece));
    check(leaked.length === 0, `${at}: none of ${pieces.length} sentences, gaps and text lines of the card is on the page (${leaked.slice(0, 3).join(' | ')})`);
    const measured = await measure(tab, cardParts);
    check(healthy(measured), `${at}: ${reported(at, measured)}`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `seal-failed-${width}-${scheme}.png`), fullPage: true});
    // The failure is remembered for the session: the right bytes later change nothing, and are not read.
    stub.steer = null;
    const reads = stub.log.filter(entry => entry === 'GET /api/v1/cards/card-acc-003/bytes').length;
    await openInApp(tab, '/deals/acc-003/text');
    await apiSettled();
    check(await tab.locator('[data-seal="failed"]').count() === 1 && await tab.locator('.dc-plain').count() === 0 &&
      stub.log.filter(entry => entry === 'GET /api/v1/cards/card-acc-003/bytes').length === reads, `${at}: the failure stays for the session on the Text tab too, and the bytes are not read again`);
    await tab.click('[data-seal="failed"] a');
    await tab.waitForSelector('.dc-plain__text');
    check(new URL(tab.url()).search === '?card=card-acc-003-older' && await tab.textContent('.dc-plain__text') === sealedThornfield.text, `${at}: the card before it opens on the same tab and verifies`);
    // A new session checks the card again.
    await signOut(tab);
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    check(await tab.locator('[data-seal="failed"]').count() === 0, `${at}: a new session checks the card again`);
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

await scenario('text tab: a copy the browser refuses', async () => {
  const tab = await page({width: 390});
  await tab.context().addInitScript(() => Object.defineProperty(navigator, 'clipboard', {value: {writeText: () => Promise.reject(new DOMException('Denied', 'NotAllowedError'))}}));
  await tab.goto(`${thornfield}/text`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.dc-plain__text');
  await tab.click('.dc-plain__bar button');
  await until(tab, () => document.querySelector('.dc-plain__status').textContent !== '');
  const said = await tab.textContent('.dc-plain__status');
  check(said === 'This browser refused to copy. Select the text and copy it with the keyboard.', `a refused copy says so (${said})`);
  await tab.context().close();
});

await browser.close();
server.close();
console.log(results.join('\n'));
// The lowest contrast measured for each text colour in the frame, by width, theme and screen.
if (options.contrast) console.log(`\n${measured.join('\n')}`);
console.log(process.exitCode ? `\n${results.filter(line => line.startsWith('FAIL')).length} problems` : '\nNo problems');
