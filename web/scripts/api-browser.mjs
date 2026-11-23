// Browser tests for the app in API mode, against the built app (web/dist) and
// a stub of /api/v1 and /healthz that each test steers: delayed and refused
// answers, two accounts, the demo book's thirty accounts on Deals, seven on
// Coverage, blocked browser storage, a session that expires, hostile text in
// names, gaps and receipts, a static host's answer, and wrong addresses. The
// company sign-in goes through the stub's own stand-in for the identity
// provider and back, and is refused. The
// frame, sign-in and theme, the deep links and the palette, Deals, Coverage,
// Activity and its receipt drawer, the Deal card's ledger, Text and
// Communications tabs, a seal that fails its check, and the proof drawer are
// each checked at every width and theme of the matrix: 320, 390, 768 and 1440
// wide, light and dark. The proof drawer is also checked at 1100: tab
// order, target sizes, focus outlines, Escape, overflow, text contrast and what
// browser storage holds. The Text tab is also read and copied for every
// recorded card, and shown for a card whose bytes do not match its seal, and
// the Communications tab lists every sentence unit of every recorded card once.
// Activity checks a card's seal against the stub's true bytes and against
// bytes with one byte changed. Every help button on the Deal card, the
// drawer, Coverage and Activity is opened by Tab, by hover at 1440 and by
// touch at 390. Every page is also opened under the policy without
// 'unsafe-inline' in style-src, and what that policy refuses is reported.
// The stub keeps the Go server's shapes: the session view, the {"error":
// {code, message}} envelope, the CSRF header and Origin checks, 403
// not_permitted for an ungranted account, the /healthz answers, and the
// security headers on every response, with the Content-Security-Policy built
// from basePolicy in internal/transport/http/static.go as the server builds it.
// The browser reaches nothing but the stub.
//
// It drives the installed Chrome, so it is not part of `npm test`. To run it:
//   npm run build
//   npm run test:browser [-- --browser chromium --channel chrome --widths 320,390,768,1440 --screenshots DIR --contrast]
// --browser is chromium (default), firefox or webkit; --channel picks Chromium's build (chrome is the
// installed Chrome, chromium the build playwright-core pins); --widths limits the matrix to those widths.
// Layout is checked by structure, not by pixel: nothing scrolls sideways, controls are at least 44 by 44
// and do not overlap one another or get clipped, text contrast holds, and focus shows. Where text wraps
// depends on the fonts a machine has, so no check depends on it.
// Build and run it with the same GTM_REVISION, which the guide links hold.
// With --screenshots, the frame's matrix, Coverage, Activity, the Deal card and its drawer save one picture per
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
import {chromium, firefox, webkit} from 'playwright-core';

const {values: options} = parseArgs({options: {browser: {type: 'string', default: 'chromium'}, channel: {type: 'string', default: 'chrome'},
  widths: {type: 'string', default: '320,390,768,1440'}, screenshots: {type: 'string'}, contrast: {type: 'boolean', default: false}}});
const engine = {chromium, firefox, webkit}[options.browser];
if (!engine) throw new Error(`--browser is chromium, firefox or webkit, not ${options.browser}.`);
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
// Every golden card in testdata/oracle/cards, each family the index lists, as cards of one more account. A row
// that holds inputs alone has no card.
const families = Object.keys(JSON.parse(readFileSync(new URL('../../testdata/oracle/cards/index.json', import.meta.url), 'utf8')).families);
const recorded = families.flatMap(file => gunzipSync(readFileSync(new URL(`../../testdata/oracle/cards/${file}.jsonl.gz`, import.meta.url))).toString()
  .split('\n').filter(Boolean).map(line => ({file, row: JSON.parse(line)})).filter(({row}) => row.card !== undefined))
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
  account, connection_generation: 1, arguments: {account_id: account, note: `${hostile} right\u202eleft`}, request_id: null, brief_id: null, status: 'ok', http_status: 200, row_count: 1, rows: [`row ${hostile}`],
  elapsed_ms: 1, error: `note ${hostile}`, started_at: '2027-01-15T15:00:00Z', finished_at: '2027-01-15T15:00:01Z', delivered_at: null});
// A card's seal as the Go server answers it: save_brief, its one row naming the digest of the card's bytes.
const sealReceiptOf = card => ({...receiptOf(card.seal_receipt_id, card.account, 'save_brief'), parent_id: null, ordinal: null, scope: null,
  arguments: {account_id: card.account, brief_id: card.brief_id}, rows: [`briefs/${card.brief_id}/${card.sha256}`], error: null, elapsed_ms: 2.5});
const principal = {id: 'p-1', display_name: `Demo reviewer ${hostile}`, kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const customer = {...principal, id: 'p-3', display_name: 'Customer reader', workspace_kind: 'customer', demo: false};
const elsewhere = {...principal, id: 'p-2', display_name: 'Another principal'};
// Who signs in through the company's identity provider, and the provider's own sign-out page.
const company = {id: 'p-4', display_name: 'Dana Reed', kind: 'person', workspace_id: 'w-2', workspace_kind: 'live', demo: false};
const providerSignOut = 'https://login.invented.example/logout?client_id=connector-invented-client';
const login = 'demo-reviewer', password = 'stub-password-for-browser-tests';

// The stub. `stub.steer` answers a request before the stub's own routes when it returns an answer.
// `stub.accounts` is the account list it serves, `stub.times` the publish time of each card it
// lists, `stub.empty` the accounts with no card, `stub.principal` who signs in, and `stub.health`
// the status /healthz answers. `stub.extra` holds cards served beside the demo's, and `stub.lists`
// an account's card list where it is not the demo's one card.
const stub = {sessions: new Map(), pre: new Set(), ttlMs: 3600_000, steer: null, log: [], files: [], pending: 0, granted: new Set(['acc-002', 'acc-003']),
  accounts, times: null, empty: new Set(), principal, cardLists: 0, mostCardLists: 0, health: 'ok', extra: [], lists: {},
  signIn: {oidc: false, demo: true}, companyRefusal: null};
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
  stub.extra = []; stub.lists = {}; stub.signIn = {oidc: false, demo: true}; stub.companyRefusal = null;
}
// Serves the demo book: thirty granted accounts, each card on its own day.
function serveBook() {stub.accounts = book; stub.granted = new Set(book.map(item => item.account)); stub.times = bookTime;}
const token = () => Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
const cookies = request => Object.fromEntries((request.headers.cookie || '').split(';').map(part => part.trim().split('=')).filter(([key]) => key));
const live = request => {const id = cookies(request).stub_session, session = id && stub.sessions.get(id); return session && session.expires > Date.now() ? [id, session] : [null, null];};
const view = (id, session) => ({...(session ? {signed_in: true, principal: session.principal, csrf_token: `c-${id}`, expires_at: new Date(session.expires).toISOString()}
  : {signed_in: false, principal: null, csrf_token: `c-${id}`, expires_at: new Date(Date.now() + 3600_000).toISOString()}), sign_in: stub.signIn});
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
  // The company sign-in as the Go server runs it, with the stub's stand-in for the provider in between:
  // the start keeps the page to return to and sends the browser to the provider, and the callback signs
  // the company person in, or sends the browser back with the reason it was refused.
  if (path === '/api/v1/session/oidc' || path === '/api/v1/session/oidc/callback') {
    if (!stub.signIn.oidc) return refuse(response, 404, 'not_found');
    if (path === '/api/v1/session/oidc') {
      const back = new URL(request.url, base).searchParams.get('return') || '/';
      response.writeHead(303, {...securityHeaders, 'Cache-Control': 'no-store', Location: '/stub-provider/authorize',
        'Set-Cookie': `stub_flow=${encodeURIComponent(back)}; Path=/; HttpOnly; SameSite=Lax`});
      return response.end();
    }
    const back = decodeURIComponent(cookies(request).stub_flow || '%2F');
    const cleared = 'stub_flow=; Path=/; Max-Age=0';
    if (stub.companyRefusal) {
      response.writeHead(303, {...securityHeaders, 'Cache-Control': 'no-store', 'Set-Cookie': cleared,
        Location: `${back}${back.includes('?') ? '&' : '?'}sign_in_error=${stub.companyRefusal}`});
      return response.end();
    }
    if (id) stub.sessions.delete(id);
    const next = token(); stub.sessions.set(next, {principal: company, company: true, expires: Date.now() + stub.ttlMs});
    response.writeHead(303, {...securityHeaders, 'Cache-Control': 'no-store', Location: back,
      'Set-Cookie': [cleared, `stub_session=${next}; Path=/; HttpOnly; SameSite=Lax`]});
    return response.end();
  }
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
      return send(response, 200, {signed_in: false, principal: null, ...(session.company ? {end_session_url: providerSignOut} : {})},
        {'Set-Cookie': 'stub_session=; Path=/; Max-Age=0'});
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
    const id = decodeURIComponent(match[1]);
    const card = [...Object.values(cards), ...stub.extra].find(item => stub.granted.has(item.account) && (item.seal_receipt_id === id || item.units.some(unit => unit.receipt_id === id)));
    if (!card) return refuse(response, 403, 'not_permitted');
    return send(response, 200, card.seal_receipt_id === id ? sealReceiptOf(card) : receiptOf(id, card.account, card.units.find(unit => unit.receipt_id === id).tool));
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
  if (path === '/stub-provider/authorize') {
    response.writeHead(303, {Location: '/api/v1/session/oidc/callback?code=invented-code&state=invented-state'});
    return response.end();
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
const activity = `${base}/ops/activity`, thornfield = `${base}/deals/acc-003`, thornfieldActivity = `${activity}?card=card-acc-003`;

// Checks.
const results = [];
// Every width and theme a page is checked at, limited by --widths.
const widths = options.widths.split(',').map(Number);
if (widths.some(width => ![320, 390, 768, 1440].includes(width))) throw new Error(`--widths takes 320, 390, 768 and 1440 (${options.widths}).`);
const matrix = widths.flatMap(width => [[width, 'light'], [width, 'dark']]);
const check = (ok, what) => {results.push(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) process.exitCode = 1;};
const browser = await engine.launch({headless: true, ...(options.browser === 'chromium' ? {channel: options.channel} : {})});
// Chromium lets the test read back what the page copied. Firefox and WebKit give a test no clipboard
// to read, so there the page's writeText is kept and read back instead.
const allowClipboard = tab => options.browser === 'chromium' ? tab.context().grantPermissions(['clipboard-read', 'clipboard-write'], {origin: base})
  : tab.context().addInitScript(() => Object.defineProperty(navigator, 'clipboard', {value: {writeText: text => {window.__copied = text; return Promise.resolve();}}}));
const copiedText = tab => tab.evaluate(() => window.__copied ?? navigator.clipboard.readText());
async function page({width = 1440, scheme = 'light', blockStorage = false, theme = null, touch = false} = {}) {
  const context = await browser.newContext({viewport: {width, height: 900}, colorScheme: scheme, reducedMotion: 'reduce', hasTouch: touch});
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
  // WebKit on macOS reaches links by Tab only with Option held, as Safari does.
  if (options.browser === 'webkit' && process.platform === 'darwin') {
    const press = tab.keyboard.press.bind(tab.keyboard);
    tab.keyboard.press = (key, ...rest) => press(/^(Shift\+)?Tab$/.test(key) ? `Alt+${key}` : key, ...rest);
  }
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
  // Two controls overlap when their boxes intersect and neither holds the other. A control is clipped when
  // an ancestor that hides its overflow cuts its box.
  const controls = all.filter(el => el.matches('a[href], button, input, select')).map(el => ({el, label: name(el), box: el.getBoundingClientRect()}));
  const crosses = (a, b) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > 0.5 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 0.5;
  // Only controls of the same layer are compared: the open dialog's, or the page's outside any dialog.
  const layer = el => el.closest('[role=dialog]');
  const shared = (a, b) => `${Math.round(Math.min(a.right, b.right) - Math.max(a.left, b.left))}x${Math.round(Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top))}`;
  const overlapping = controls.flatMap((a, i) => controls.slice(i + 1)
    .filter(b => layer(a.el) === layer(b.el) && crosses(a.box, b.box) && !a.el.contains(b.el) && !b.el.contains(a.el)).map(b => `${a.label} / ${b.label} by ${shared(a.box, b.box)}`));
  const cut = ({el, box}) => {
    for (let up = el.parentElement; up; up = up.parentElement) {
      const style = getComputedStyle(up), overflow = [style.overflowX, style.overflowY];
      // What a scrolling box holds is reached by scrolling it, so it is not clipped.
      if (overflow.some(value => value === 'auto' || value === 'scroll')) return false;
      if (!overflow.some(value => value === 'hidden' || value === 'clip')) continue;
      const outer = up.getBoundingClientRect();
      if (box.left < outer.left - 1 || box.right > outer.right + 1 || box.top < outer.top - 1 || box.bottom > outer.bottom + 1) return true;
    }
    return false;
  };
  const clipped = controls.filter(cut).map(({label, box}) => `${label} ${Math.round(box.width)}x${Math.round(box.height)}`);
  // A control's accessible name: its label, its aria-label or its text.
  const unnamed = all.filter(el => el.matches('a[href], button, input, select'))
    .filter(el => !(el.getAttribute('aria-label') || el.textContent.trim() || [...(el.labels ?? [])].some(label => label.textContent.trim()))).map(el => el.outerHTML.slice(0, 80));
  return {colours, lowest: Math.min(...Object.values(colours)), smallest: Math.min(...texts.map(el => parseFloat(getComputedStyle(el).fontSize))), small, unnamed,
    overlapping, clipped, overflow: document.documentElement.scrollWidth - innerWidth};
}, measured);
// What has focus: its name, its size and its outline.
const ring = tab => tab.evaluate(() => {
  const el = document.activeElement, box = el.getBoundingClientRect(), style = getComputedStyle(el);
  return {name: el.getAttribute('aria-label') || el.textContent.trim() || el.getAttribute('name') || el.tagName, width: box.width, height: box.height,
    outline: style.outlineStyle !== 'none' ? parseFloat(style.outlineWidth) : 0, inDialog: Boolean(el.closest('[role=dialog]'))};
});
const fits = focus => focus.width >= 44 && focus.height >= 44 && focus.outline >= 3;
const needsServer = 'This page needs its server. Open the address your gtm-connector server prints.';
const until = (tab, fn, arg) => tab.waitForFunction(fn, arg, {timeout: 8000})
  .catch(error => {throw new Error(`${String(error).split('\n')[0]} waiting for ${String(fn).replace(/\s+/g, ' ').slice(0, 160)}`);});

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
    await tab.goto(`${base}/ops/coverage`, {waitUntil: 'networkidle'});
    await tab.waitForSelector('[data-refusal]');
    check(await tab.locator(`text=${needsServer}`).count() === 0 && await tab.locator('[data-refusal]').count() === 1, `probe answered with ${label}: an error in API mode`);
    await tab.context().close();
  });
}

// 2. Sign-in, focus, and hostile text in names and receipts.
await scenario('sign-in and hostile text', async () => {
  const tab = await page();
  await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.fr-signin');
  await tab.fill('input[name=login]', login);
  await tab.fill('input[name=password]', 'wrong');
  await tab.click('.fr-signin__form button[type=submit]');
  await tab.waitForSelector('.fr-signin [data-refusal="sign_in_refused"]');
  check((await focused(tab)).includes('password') && await tab.inputValue('input[name=password]') === '', `a refused sign-in from the button clears the password and puts focus there (${await focused(tab)})`);
  await signIn(tab, 'enter');
  await tab.waitForSelector('.ac-table tbody .ac-ref');
  await until(tab, () => document.activeElement?.matches('main h1'));
  check(true, 'after sign-in focus is on the page heading');
  check((await tab.textContent('.fr > .fr-announce[role=status]')).startsWith('Signed in as Demo reviewer'), 'the sign-in is announced in a live region that was already on the page');
  await tab.click('.ac-table tbody tr:nth-child(2) .ac-open');
  await tab.waitForSelector('[role=dialog] [data-field="arguments"]');
  const shown = await tab.evaluate(() => ({injected: Boolean(window.__injected) || Boolean(document.querySelector('main img, [role=dialog] img')), text: document.querySelector('[role=dialog]').textContent}));
  check(!shown.injected && shown.text.includes('row <img src=x onerror') && shown.text.includes('right\\u202eleft') && !shown.text.includes('\u202e'),
    'hostile receipt rows and arguments render as inert text, a format character as its escape');
  check(tab.errors.length === 0, `no page errors (${tab.errors.join(' | ')})`);
  await tab.keyboard.press('Escape');
  await signOut(tab);
  await tab.waitForSelector('.fr-signin');
  check((await focused(tab)) === 'h1#fr-signin-title', `after sign-out focus is on the sign-in heading (${await focused(tab)})`);
  await tab.context().close();
});

// 3. A new session reads afresh: nothing read under the earlier session is shown again.
await scenario('a second sign-in in the same tab', async () => {
  const tab = await page();
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.deals-row[data-account="acc-002"]');
  await signOut(tab);
  await tab.waitForSelector('.fr-signin');
  // Between the sessions the principal loses its grant on acc-002, and the new session's reads are slow.
  stub.granted.delete('acc-002');
  stub.steer = (method, path) => path === '/api/v1/accounts' || path.endsWith('/cards') ? {delay: 1500} : null;
  await signIn(tab);
  await tab.waitForTimeout(400);
  check(await tab.locator('.deals-row').count() === 0, 'while the new session reads, nothing read under the earlier session is shown');
  await until(tab, () => document.querySelectorAll('.deals-row').length > 0);
  const rows = await tab.locator('.deals-row').evaluateAll(els => els.map(el => el.dataset.account));
  check(JSON.stringify(rows) === '["acc-003"]', `the second session's account list is read afresh (${rows.join(', ')})`);
  await tab.context().close();
});

// 5. Ten refusals, each shown as a refusal and never as an empty result.
const refusals = [
  ['the account list', `${base}/deals`, path => path === '/api/v1/accounts', '.deals-row, .deals-empty'],
  ['the card list on a deal', thornfield, path => path.endsWith('/cards'), '.dc-section, .dc-empty'],
  ['the card on a deal', thornfield, path => path === '/api/v1/cards/card-acc-003', '.dc-section, .dc-values-note, blockquote'],
  ['the sealed bytes on a deal', `${thornfield}/text`, path => path === '/api/v1/cards/card-acc-003/bytes', '.dc-section, .dc-values-note, blockquote, .dc-plain, [data-seal]'],
  ['the card on Activity', thornfieldActivity, path => path === '/api/v1/cards/card-acc-003', '.ac-table, .ac-card'],
  ['the receipts on Activity', thornfieldActivity, path => path.startsWith('/api/v1/receipts/'), '.ac-table tbody .ac-ref'],
  ['a receipt in its drawer', `${thornfieldActivity}&receipt=seal-acc-003`, path => path.startsWith('/api/v1/receipts/'), '[role=dialog] [data-field]'],
  ['a card of an ungranted account', `${activity}?card=card-acc-001`, () => false, '.ac-table, .ac-card'],
  ['Coverage', `${base}/ops/coverage`, path => path === '/api/v1/accounts', '.cv-table, .cv-count'],
  ['the session read', `${base}/deals`, path => path === '/api/v1/session', 'input[name=login]'],
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
  await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.ac-table tbody .ac-ref');
  stub.steer = (method, path) => path === '/api/v1/session' && method === 'GET' ? {status: 503, code: 'internal'} : null;
  await tab.waitForSelector('.fr-signin', {timeout: 6000});
  const reads = stub.log.filter(entry => entry === 'GET /api/v1/session').length;
  await tab.waitForTimeout(3000);
  const left = await tab.evaluate(() => document.body.innerText);
  check(await tab.locator('.ac-table, .ac-card, .fr-who').count() === 0 && !/Third Account Co|acc-003|card-acc-003|Demo reviewer/.test(left),
    'after an expired session nothing that was read is on screen');
  check(stub.log.filter(entry => entry === 'GET /api/v1/session').length - reads === 0, 'no session polling after a failed refresh');
  await tab.context().close();
});

// 7. Sign-out with a token made stale by another tab is retried once with the fresh one.
await scenario('stale sign-out token', async () => {
  const tab = await page();
  await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
  await signIn(tab);
  // The page's own reads finish first, so none of them meets the rotated session.
  await tab.waitForSelector('.ac-table tbody .ac-ref');
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
  await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'});
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
for (const width of [320, 1440].filter(width => widths.includes(width))) {
  await scenario(`blocked storage ${width}`, async () => {
    const tab = await page({width, blockStorage: true});
    await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.ac-table tbody .ac-ref');
    await tab.waitForTimeout(300);
    check(tab.errors.length === 0, `${width}: no page errors with storage blocked (${tab.errors.join(' | ')})`);
    const theme = await tab.evaluate(() => document.documentElement.dataset.theme);
    check(theme === 'light', `${width}: with storage blocked the theme follows the system (${theme})`);
    await tab.context().close();
  });
}

// 10. Deep links, the palette, contrast and type sizes, at every width and theme.
for (const [width, scheme] of matrix) {
  await scenario(`layout ${width} ${scheme}`, async () => {
    const tab = await page({width, scheme});
    await tab.goto(`${thornfieldActivity}&receipt=receipt-acc-003-read_call_notes`, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('[role=dialog] [data-field="rows"]');
    await until(tab, () => document.activeElement?.matches('.ui-dialog__title'));
    check(await tab.textContent('.ui-dialog__title') === 'Read receipt', `${width} ${scheme}: a ?receipt= link opens that receipt's drawer with focus on its title`);
    const sizes = await tab.evaluate(() => ({small: [...document.querySelectorAll('.ac-ref, .ac-json')].filter(el => parseFloat(getComputedStyle(el).fontSize) < 14).length,
      face: getComputedStyle(document.querySelector('[role=dialog] .ac-ref')).fontFamily, overflow: document.documentElement.scrollWidth - innerWidth}));
    check(sizes.small === 0 && /mono|Menlo|Consolas/i.test(sizes.face), `${width} ${scheme}: IDs, rows and arguments are in the reference face at 14 or more (${sizes.face})`);
    check(sizes.overflow <= 1, `${width} ${scheme}: no horizontal overflow (${sizes.overflow}px)`);
    await tab.keyboard.press('Escape');
    await tab.waitForSelector('[role=dialog]', {state: 'detached'});
    await tab.keyboard.press('Control+k');
    await tab.waitForSelector('.ui-palette__option');
    await tab.waitForSelector('.ui-palette__group:has-text("Deals")');
    const groups = await tab.locator('.ui-palette__group').allTextContents();
    const options = await tab.locator('.ui-palette__label').allTextContents();
    check(groups.join() === 'Deals,Ops' && options.join('|') === `Second ${hostile} Co.|Third Account Co.|Coverage|Activity`,
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
    await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.ac-table tbody .ac-ref');
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
    check(palette.title === 'Find a deal' && palette.placeholder === 'A deal’s name, or Activity' && empty === 'No deal or page matches.', `${width}: the palette is titled ${palette.title}, suggests ${palette.placeholder} and says ${empty}`);
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
  for (const path of ['/', new URL(activity).pathname, '/ops/nothing']) {
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
  return `lowest contrast ${facts.lowest.toFixed(2)}:1, smallest text ${facts.smallest}px, small controls [${facts.small.join(', ')}], unnamed controls [${facts.unnamed.join(', ')}], `
    + `overlapping controls [${facts.overlapping.join(', ')}], clipped controls [${facts.clipped.join(', ')}], overflow ${facts.overflow}px`;
};
const healthy = facts => facts.lowest >= 4.5 && facts.smallest >= 14 && facts.small.length === 0 && facts.unnamed.length === 0 && facts.overlapping.length === 0 && facts.clipped.length === 0 && facts.overflow <= 0;

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
  // The row's links: the deal's card, and what ran for its newest card.
  const links = await tab.$$eval('.cv-table tr[data-account="c-04"] .cv-open', els => els.map(el => `${el.textContent} ${el.getAttribute('href')}`));
  check(links.join() === 'Open the card of Dogwood Co. /deals/c-04,What ran for the card of Dogwood Co. /ops/activity?card=card-c-04-0',
    `a row links its deal's card and what ran for its newest card (${links.join(', ')})`);
  await tab.click('.cv-table tr[data-account="c-04"] .cv-open:has-text("What ran")');
  await until(tab, () => location.pathname === '/ops/activity' && document.querySelector('.ac'));
  check(new URL(tab.url()).searchParams.get('card') === 'card-c-04-0', `What ran opens Activity for that card (${tab.url()})`);
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

for (const [width, scheme] of matrix) {
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

// 13b. The company sign-in: one button that goes to the provider and back to the address that was open,
// signed in as the company person with no demo tag; a sign-out that offers the provider's own sign-out
// page; and a refusal that says why and leaves the address clean.
for (const [width, scheme] of [[320, 'dark'], [390, 'light'], [1440, 'light'], [1440, 'dark']]) {
  await scenario(`company sign-in ${width} ${scheme}`, async () => {
    const at = `${width} ${scheme}`;
    stub.signIn = {oidc: true, demo: false};
    const tab = await page({width, scheme});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await tab.waitForSelector('.fr-signin__company');
    check(await tab.locator('input[name=password]').count() === 0 && !(await tab.textContent('.fr-signin')).includes('demo-credentials.json'),
      `${at}: with the demo sign-in off, the sign-in screen has no password field and no demo help`);
    let facts = await measure(tab);
    check(healthy(facts), `${at}: the company sign-in screen, ${reported(`${at} company sign-in`, facts)}`);
    await tab.keyboard.press('Tab');
    await tab.keyboard.press('Tab');
    const button = await ring(tab);
    check(button.name === 'Sign in with your company account' && fits(button),
      `${at}: Tab reaches Sign in with your company account, at least 44 by 44 with a 3px outline (${button.name} ${Math.round(button.width)}x${Math.round(button.height)} ${button.outline}px)`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `company-sign-in-${width}-${scheme}.png`), fullPage: true});
    await tab.keyboard.press('Enter');
    await tab.waitForSelector('.fr-who', {state: 'attached'});
    await until(tab, () => document.querySelector('.fr > .fr-announce[role=status]')?.textContent.startsWith('Signed in as Dana Reed'));
    check(new URL(tab.url()).pathname === '/deals/acc-003' && await tab.locator('.fr-demo').count() === 0,
      `${at}: the sign-in comes back to ${new URL(tab.url()).pathname} as Dana Reed, with no demo tag`);
    await signOut(tab);
    await tab.waitForSelector('.fr-signin__provider');
    const offered = await tab.getAttribute('.fr-signin__provider a', 'href');
    check(offered === providerSignOut && (await tab.textContent('.fr-signin__provider')).includes('You are signed out here.'),
      `${at}: signing out offers the provider's own sign-out page (${offered})`);
    facts = await measure(tab);
    check(healthy(facts), `${at}: signed out with the provider's page offered, ${reported(`${at} company signed out`, facts)}`);
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

await scenario('company sign-in refused', async () => {
  stub.signIn = {oidc: true, demo: false};
  stub.companyRefusal = 'not_allowed';
  const tab = await page({width: 390});
  await tab.goto(`${base}/ops/coverage`, {waitUntil: 'networkidle'});
  await tab.click('.fr-signin__company');
  await tab.waitForSelector('.fr-signin [data-refusal="sign_in_not_allowed"]');
  const url = new URL(tab.url());
  check(url.pathname === '/ops/coverage' && !url.search.includes('sign_in_error') && !(await signedInNow(tab)) &&
    (await tab.textContent('[data-refusal]')).includes('Your company account may not sign in here.'),
    `a refused company sign-in says why on ${url.pathname}${url.search}, signed out, and the reason leaves the address`);
  await tab.reload({waitUntil: 'networkidle'});
  await tab.waitForSelector('.fr-signin__company');
  check(await tab.locator('[data-refusal]').count() === 0, 'after a reload the refusal is gone');
  check(tab.errors.length === 0 && violations(tab).length === 0, `refused: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('company and demo sign-in side by side', async () => {
  stub.signIn = {oidc: true, demo: true};
  const tab = await page({width: 1440});
  await tab.goto(activity, {waitUntil: 'networkidle'});
  await tab.waitForSelector('.fr-signin__company');
  const order = [];
  for (let i = 0; i < 5; i++) {
    await tab.keyboard.press('Tab');
    order.push(await ring(tab));
  }
  check(order.map(focus => focus.name).join() === 'Skip to content,Sign in with your company account,login,password,Sign in' && order.every(fits),
    `both ways in: Tab order is ${order.map(focus => focus.name).join(', ')}`);
  check(await tab.locator('.fr-signin__subtitle', {hasText: 'The demo workspace'}).count() === 1 &&
    await tab.locator('.fr-signin__form button.ui-button--plain').count() === 1, 'the demo form sits under its own heading, with the one primary action the company button');
  await signIn(tab, 'click');
  check((await tab.textContent('.fr > .fr-announce[role=status]')).startsWith('Signed in as Demo reviewer'), 'the demo sign-in still signs in beside the company one');
  await tab.context().close();
});

// 14. The frame, sign-in and theme at 320, 390, 768 and 1440 wide, light and dark.
for (const width of widths) {
  for (const scheme of ['light', 'dark']) {
    await scenario(`frame ${width} ${scheme}`, async () => {
      const phone = width < 720, other = scheme === 'light' ? 'dark' : 'light', at = `${width} ${scheme}`;
      const tab = await page({width, scheme});
      const asked = thornfieldActivity;
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
      await until(tab, () => document.querySelector('.ac-table tbody .ac-ref'));
      check(new URL(tab.url()).searchParams.get('card') === 'card-acc-003' && new URL(tab.url()).pathname === '/ops/activity' && (await tab.textContent('.ac-card')).includes('card-acc-003'),
        `${at}: after sign-in ${new URL(tab.url()).pathname}${new URL(tab.url()).search} renders with that card`);
      const demo = phone ? '.fr-demo-row .fr-demo' : '.fr-bar .fr-demo';
      check(await tab.locator(demo).isVisible() && (await tab.textContent(demo)) === 'Demo, invented records', `${at}: the demo tag shows for a demo principal`);
      facts = await measure(tab);
      check(healthy(facts), `${at}: the signed-in frame, ${reported(`${at} frame`, facts)}`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `frame-${width}-${scheme}.png`)});
      // Tab order from the top of the page.
      await tab.reload({waitUntil: 'networkidle'});
      await tab.waitForSelector('.ac-table tbody .ac-ref');
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
        check(items.join() === 'Deals,Ops,Coverage,Activity,Dark theme,Sign out' && trapped.every(focus => focus.inDialog) && trapped.every(fits) && box.x <= 0 && box.x + box.width >= await tab.evaluate(() => document.documentElement.clientWidth),
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
      await tab.waitForSelector('.ac-table tbody .ac-ref');
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
      check(await tab.locator('.ac, [role=dialog]').count() === 0, `${at}: after sign-out focus is on the sign-in heading and nothing read is on screen`);
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
  const box = row.getBoundingClientRect(), card = row.querySelector('.deals-row__card').getBoundingClientRect();
  const name = row.querySelector('.deals-row__name').getBoundingClientRect(), id = row.querySelector('.deals-row__id').getBoundingClientRect();
  return {account: row.dataset.account, name: row.querySelector('.deals-row__name').textContent, id: row.querySelector('.deals-row__id').textContent,
    card: row.querySelector('.deals-row__card').innerText.replace(/\s+/g, ' ').trim(), href: row.getAttribute('href'), height: box.height, width: box.width,
    current: row.getAttribute('aria-current'), tabIndex: row.tabIndex,
    apart: [[name, id], [name, card], [id, card]].every(([a, b]) => Math.min(a.right, b.right) - Math.max(a.left, b.left) <= 0.5 || Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) <= 0.5),
    inside: [name, id, card].every(part => part.left >= box.left - 1 && part.right <= box.right + 1 && part.top >= box.top - 1 && part.bottom <= box.bottom + 1)};
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
  const row = await tab.evaluate(() => document.activeElement.matches('.deals-row'));
  check(row && focus.height >= 56 && focus.outline >= 3, `focus shows on the whole row (${row ? 'the row' : 'not the row'}, ${Math.round(focus.height)} high, ${focus.outline}px outline)`);
  await tab.keyboard.press('Enter');
  await until(tab, href => location.pathname === href && document.querySelector('.dc-title'), `/deals/${names[1]}`);
  check(await tab.locator('.deals-back').isVisible() && await tab.locator('.deals-row').count() === 0, 'Enter opens the row; under 1100 the deal is the whole page, with a link back');
  await tab.click('.deals-back');
  await until(tab, () => location.pathname === '/deals' && document.querySelectorAll('.deals-row').length === 30);
  await tab.focus('input[name=q]');
  await tab.keyboard.type('thorn');
  // This wait has timed out in WebKit a few times with no change to the page. When it does, say what the page held.
  await until(tab, () => document.querySelectorAll('.deals-row').length === 1).catch(async error => {
    const held = await tab.evaluate(() => ({value: document.querySelector('input[name=q]')?.value, q: new URL(location.href).searchParams.get('q'),
      rows: document.querySelectorAll('.deals-row').length, focus: document.activeElement?.getAttribute('name') ?? document.activeElement?.className ?? document.activeElement?.tagName}));
    throw new Error(`${error.message}; the field held ${JSON.stringify(held.value)}, the address ?q=${held.q}, ${held.rows} rows showed, and focus was on ${held.focus}`);
  });
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

for (const width of widths) {
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
      const crowded = rows.filter(row => !row.apart || !row.inside).map(row => row.account);
      check(crowded.length === 0, `${at}: in every row the name, the ID and the card time do not overlap and stay inside the row (crowded: ${crowded.join() || 'none'})`);
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
for (const [width, scheme] of matrix) {
  await scenario(`deal card ${width} ${scheme}`, async () => {
    const at = `deal card ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    const facts = await tab.evaluate(() => {
      const box = selector => document.querySelector(selector).getBoundingClientRect();
      const crosses = (a, b) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > 0.5 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 0.5;
      const [countdown, arr] = [...document.querySelectorAll('.dc-focal__item')].map(el => el.getBoundingClientRect());
      const sections = ['.dc-people', '.dc-the-record', '.dc-not-read'].map(box);
      return {title: document.title, heading: document.querySelector('.dc-title').textContent, id: document.querySelector('.dc-id').textContent,
        note: document.querySelector('.dc-values-note')?.textContent ?? null,
        focal: [...document.querySelectorAll('.dc-focal__item')].map(el => ({
          kind: el.dataset.focal, number: el.querySelector('.dc-focal__number').textContent,
          figure: el.querySelector('.dc-focal__figure').innerText.replace(/\s+/g, ' ').trim(), subs: [...el.querySelectorAll('.dc-focal__sub')].map(sub => sub.innerText.replace(/\s+/g, ' ').trim())})),
        focalApart: Boolean(countdown && arr) && !crosses(countdown, arr),
        focalFirst: box('.dc-focal').top < box('.dc-ledger').top && box('.dc-head').bottom <= box('.dc-focal').top,
        values: [...document.querySelectorAll('.dc-the-record [data-value]')].map(el => `${el.dataset.value}: ${el.querySelector('.dc-value__label').textContent} ${el.querySelector('.dc-value__text').textContent}`),
        flags: [...document.querySelectorAll('.dc-flags .dc-flag')].map(el => el.innerText.replace(/\s+/g, ' ').trim()),
        titles: [...document.querySelectorAll('[data-group="customer"] .dc-attr__title')].map(el => el.textContent),
        quotes: [...document.querySelectorAll('blockquote')].map(el => ({n: Number(el.dataset.unit), text: el.textContent, children: el.children.length,
          face: getComputedStyle(el).fontFamily.split(',')[0], rule: `${getComputedStyle(el).borderLeftWidth} ${getComputedStyle(el).borderLeftStyle}`})),
        injected: Boolean(window.__injected) || Boolean(document.querySelector('.dc img')),
        sectionsApart: sections.every((a, i) => sections.slice(i + 1).every(b => !crosses(a, b))),
        local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
    });
    check(facts.heading === 'Third Account Co.' && facts.title === 'Third Account Co.' && facts.id === 'acc-003' && facts.note === null,
      `${at}: the title is the server’s name, the tab is titled with it, and the ID is under it`);
    const [countdown, arr] = facts.focal;
    const narrow = width < 720;
    check(facts.focalFirst && countdown?.kind === 'countdown' && countdown.figure === '24 days [1]' && countdown.subs.join(' | ') === 'to the renewal, 2027-02-08 | as of 2027-01-15' &&
      arr?.kind === 'arr' && arr.figure === 'USD 450,000.00 [1]' && arr.subs.join() === 'ARR, from the mapped field',
      `${at}: under the title, the countdown and ARR with their numbers and sub-lines (${JSON.stringify(facts.focal.map(item => [item.figure, item.subs]))})`);
    // Whether ARR sits beside the countdown or under it depends on the fonts the machine has; the two never overlap.
    check(facts.focalApart, `${at}: the countdown and ARR do not overlap`);
    check(facts.values.join(' | ') === dealCard.lines.filter(line => line.section === 'value').map(line => `${line.code}: ${line.label} ${line.text}`).join(' | '),
      `${at}: the record column is every value line in the card's order (${facts.values.length})`);
    check(facts.flags.join() === 'The renewal is in 24 days and this opportunity is still open at stage "negotiation". [1] [3]' && facts.titles.join() === 'Deputy general counsel,Deputy general counsel',
      `${at}: Flags holds the fixed signal with its numbers, and each buyer quote the contact's title (${facts.flags.join()}; ${facts.titles.join()})`);
    const expected = shownUnits.map(unit => `${unit.n} ${unit.body.text}`).sort().join('|');
    check(facts.quotes.map(quote => `${quote.n} ${quote.text}`).sort().join('|') === expected && facts.quotes.every(quote => quote.children === 0) && !facts.injected,
      `${at}: each shown sentence is one blockquote holding exactly its text, and hostile speakers and gaps stay text`);
    check(facts.quotes.every(quote => quote.face === 'Charter' || quote.face.includes('Charter') || quote.face.includes('Georgia')) && facts.quotes.every(quote => quote.rule === '3px solid'),
      `${at}: a quotation is in the quotation face with a 3px rule (${facts.quotes[0].face} ${facts.quotes[0].rule})`);
    check(facts.sectionsApart, `${at}: People said, The record and Not read do not overlap`);
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
        clear: Math.min(box.right, own.right) - Math.max(box.left, own.left) <= 0.5 || Math.min(box.bottom, own.bottom) - Math.max(box.top, own.top) <= 0.5, inFlow: getComputedStyle(panel).position === 'static', lineShown: line.height > 0, name: button.getAttribute('aria-label'), size: [own.width, own.height]};
    }, help);
    const focusRing = await ring(tab);
    await tab.keyboard.press('Escape');
    const closed = await tab.evaluate(selector => {const button = document.querySelector(selector); return {expanded: button.getAttribute('aria-expanded'), hidden: document.getElementById(button.getAttribute('aria-controls')).hidden, focused: document.activeElement === button};}, help);
    check(opened.expanded === 'true' && opened.shown && opened.role === 'note' && opened.clear && opened.inFlow === narrow && opened.name === 'About the countdown' &&
      opened.text.startsWith('Days from the card') && fits(focusRing) && closed.expanded === 'false' && closed.hidden && closed.focused,
      `${at}: the countdown's help opens on focus clear of its button${narrow ? ', in the flow' : ''}, and Escape closes it with focus on its button (${JSON.stringify(opened)}; ${JSON.stringify(closed)})`);
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
    text: dialog.querySelector('.ui-dialog__body').innerText.replace(/\s+/g, ' ').trim(), read: dialog.querySelector('.dc-proof__read')?.innerText.replace(/\s+/g, ' ').trim() ?? null,
    injected: Boolean(window.__injected) || Boolean(dialog.querySelector('img')), unit: new URL(location.href).searchParams.get('unit'),
    box: {left: box.left, top: box.top, right: box.right, bottom: box.bottom}, close: {width: close.width, height: close.height},
    background: style.backgroundColor, shadow: style.boxShadow, cardRight: card.right, viewport: {width: document.documentElement.clientWidth, height: innerHeight},
    rail: Boolean(document.querySelector('.deals-rail')?.getClientRects().length),
    local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
});
const atCite = (tab, n) => until(tab, number => document.activeElement?.matches(`.dc-line__n > .dc-cite[data-cite="${number}"]`), String(n));
for (const [width, scheme] of matrix) {
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
    check(!facts.injected && facts.text.includes(`Marian Holt ${hostile}`) && facts.text.includes('Customer (by name)') && facts.text.includes('On the card'),
      `${at}: the hostile speaker name renders as text, with the role and its basis as recorded`);
    check(facts.read === 'Read 15 Jan 2027, 15:00:00 UTC from calls' && !/receipt-|read_call_notes|op-|calls:read|[0-9a-f]{64}/.test(facts.text),
      `${at}: the read line gives the read's start and source, and no receipt ID, digest or tool name (${facts.read})`);
    check(/After \[6\] Please do not put a date on the order form\./.test(facts.text) && !facts.text.includes('Before'), `${at}: Around it shows unit 6 after it and nothing before it`);
    const receiptLink = await tab.evaluate(() => {const link = document.querySelector('[role=dialog] .dc-proof__receipt'), read = document.querySelector('[role=dialog] .dc-proof__read');
      const [a, b] = [link, read].map(el => el?.getBoundingClientRect());
      return {text: link?.textContent, href: link?.getAttribute('href'), apart: Boolean(a && b) && (Math.min(a.right, b.right) - Math.max(a.left, b.left) <= 0.5 || Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) <= 0.5)};});
    check(receiptLink.text === 'Open the receipt in Ops' && receiptLink.href === '/ops/activity?card=card-acc-003&receipt=receipt-acc-003-read_call_notes' && receiptLink.apart,
      `${at}: beside Read, not over it, a link opens the receipt in Ops (${receiptLink.href})`);
    const covers = facts.box.left <= 0 && facts.box.top <= 0 && facts.box.right >= facts.viewport.width && facts.box.bottom >= facts.viewport.height;
    const inView = facts.box.left >= 0 && facts.box.top >= 0 && facts.box.right <= facts.viewport.width + 1 && facts.box.bottom <= facts.viewport.height + 1;
    const placed = `${Math.round(facts.box.left)},${Math.round(facts.box.top)} to ${Math.round(facts.box.right)},${Math.round(facts.box.bottom)} in ${facts.viewport.width}x${facts.viewport.height}`;
    if (width < 720) {
      check(covers && facts.close.width >= 44 && facts.close.height >= 44 && facts.modal === 'true',
        `${at}: the sheet covers the viewport (${placed}) and its close button is ${facts.close.width}x${facts.close.height}`);
    } else if (width < 1100) {
      check(inView && facts.box.top <= 0 && facts.box.bottom >= facts.viewport.height - 1 && facts.shadow !== 'none' && facts.modal === 'true',
        `${at}: a modal panel in view at full height, with the shadow, over the card (${placed})`);
    } else {
      check(inView && facts.box.top <= 0 && facts.box.bottom >= facts.viewport.height - 1 && facts.shadow !== 'none' && facts.modal === 'false',
        `${at}: a panel in view at full height, with the shadow, beside the card (${placed})`);
      check(facts.cardRight <= facts.box.left + 0.5 && !facts.rail, `${at}: the list rail steps aside and the card ends at ${Math.round(facts.cardRight)}, left of the drawer at ${Math.round(facts.box.left)}`);
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
  const state = () => tab.$$eval('.dc-focal .ui-help__button[aria-controls]', buttons => buttons.map(button => button.getAttribute('aria-expanded')).join());
  await tab.hover('[data-focal="arr"] .ui-help__button[aria-controls]');
  const hovered = await state();
  await tab.hover('[data-focal="arr"] .ui-help__panel[role=note]');
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
  const note = (await tab.textContent('.dc-values-note')).replace(/\s+/g, ' ').trim();
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
  check(beside.card.right <= beside.drawer + 0.5, `at 1100 the card ends at ${Math.round(beside.card.right)}, left of the drawer at ${Math.round(beside.drawer)}`);
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
  check(record === 'Record crm/contacts/con-003 This server returns a record’s reference, not its fields yet. On the card Read 15 Jan 2027, 15:00:00 UTC from the CRM Open the receipt in Ops',
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
// WebKit refuses more than 100 history entries in 10 seconds, so there each move waits 110 ms.
const openInApp = async (tab, path) => {
  if (options.browser === 'webkit') await tab.waitForTimeout(110);
  await tab.evaluate(to => {history.pushState(null, '', to); dispatchEvent(new PopStateEvent('popstate'));}, path);
};
for (const [width, scheme] of matrix) {
  await scenario(`text tab ${width} ${scheme}`, async () => {
    const at = `text tab ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await allowClipboard(tab);
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    await tab.click('.dc-tab[href="/deals/acc-003/text"]');
    await tab.waitForSelector('.dc-plain__text');
    // A new address moves focus to the page heading on the next frame; wait for it before moving focus.
    await until(tab, () => document.activeElement?.matches('main h1'));
    const sunken = await sunkenOf(tab);
    const facts = await tab.evaluate(() => {
      const pre = document.querySelector('.dc-plain__text'), style = getComputedStyle(pre);
      return {path: location.pathname, text: pre.textContent, nodes: pre.childNodes.length, face: style.fontFamily, space: style.whiteSpace, background: style.backgroundColor,
        tabs: [...document.querySelectorAll('.dc-tab')].map(el => `${el.textContent} ${el.getAttribute('href')}${el.getAttribute('aria-current') ? ' open' : ''}`).join(', '),
        overflow: document.documentElement.scrollWidth - innerWidth, wider: pre.scrollWidth - pre.clientWidth, injected: Boolean(window.__injected),
        local: Object.keys(localStorage), session: Object.keys(sessionStorage)};
    });
    check(facts.path === '/deals/acc-003/text' && facts.tabs === 'Card /deals/acc-003, Communications /deals/acc-003/communications, Text /deals/acc-003/text open', `${at}: the Text tab opens the plain-text card (${facts.path}; ${facts.tabs})`);
    check(facts.text === sealedThornfield.text && facts.nodes === 1 && !facts.injected, `${at}: the Text tab holds the sealed text byte for byte, as one text node`);
    check(facts.face.startsWith('ui-monospace') && facts.space === 'pre-wrap' && facts.background === sunken,
      `${at}: the text is in the reference face on the sunken surface, white-space kept (${facts.face.split(',')[0]} ${facts.space} ${facts.background})`);
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
    const copied = await copiedText(tab);
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
  await allowClipboard(tab);
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
    const copied = await copiedText(tab);
    if (shown !== text || copied !== text || said !== 'Copied') wrong.push(card.id);
  }
  check(recorded.length === 91 && wrong.length === 0, `each of ${recorded.length} golden cards shows its sealed text byte for byte and copies the same string (wrong: ${wrong.slice(0, 5).join(', ') || 'none'})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `recorded cards: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

for (const [width, scheme] of matrix) {
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
    await tab.waitForSelector('.fr-signin');
    await apiSettled();
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

// 19. Help on the page: every help button of the Deal card, the proof drawer
// and Coverage, at 390 by touch and 1440 by mouse. Each is reached by Tab and
// opens on focus, on hover or on a tap; Escape closes it and focus stays on
// its button; each sentence is the copy file's, and a hint's is its button's
// accessible description; a panel opens under its line at 390, in the flow,
// and never over its line at 1440; no button's hit area overlaps another
// control's; one is open at a time; and a pinned panel's guide link is
// reached by Tab and opens the guide at the build's revision.
const helpFile = JSON.parse(readFileSync(new URL('../src/help/help.json', import.meta.url), 'utf8'));
// The revision vite.config.js builds in from GTM_REVISION: build and run this with the same value.
const builtRevision = (() => {const named = (process.env.GTM_REVISION ?? '').replace(/-modified$/, ''); return /^[0-9A-Za-z][0-9A-Za-z._-]{0,99}$/.test(named) ? named : 'main';})();
const helpButtons = (tab, scope) => tab.locator(`${scope} .ui-help__button`);
// One help's state, as drawn: shown, where its panel is against its line and the viewport, and what its button overlaps.
const helpState = (tab, scope, index) => tab.evaluate(([scope, index]) => {
  const button = document.querySelectorAll(`${scope} .ui-help__button`)[index], wrapper = button.parentElement, panel = wrapper.querySelector('.ui-help__panel');
  const line = wrapper.parentElement.closest('p, li, h1, h2, h3, h4, div'), box = panel.getBoundingClientRect(), own = button.getBoundingClientRect(), held = line.getBoundingClientRect();
  // What else the line draws, outside its panel.
  const others = [...line.querySelectorAll('*')].filter(el => !panel.contains(el) && el !== panel && el.getClientRects().length).map(el => el.getBoundingClientRect());
  const crosses = (a, b) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > 0.5 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 0.5;
  // The controls of the same layer: the open dialog's, or the page's outside any dialog.
  const layer = button.closest('[role=dialog]');
  const controls = [...document.querySelectorAll('a[href], button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])')]
    .filter(el => el !== button && !wrapper.contains(el) && el.getClientRects().length && el.closest('[role=dialog]') === layer);
  return {key: panel.dataset.help, role: panel.getAttribute('role'), shown: !panel.hidden && box.height > 0, text: panel.querySelector('.ui-help__text').textContent,
    expanded: button.getAttribute('aria-expanded'), focused: document.activeElement === button, position: getComputedStyle(panel).position,
    ring: getComputedStyle(button).outlineStyle !== 'none' ? parseFloat(getComputedStyle(button).outlineWidth) : 0,
    size: [own.width, own.height], overlaps: controls.filter(el => crosses(own, el.getBoundingClientRect())).map(el => el.getAttribute('aria-label') || el.textContent.trim().slice(0, 30)),
    clearOfLine: [own, ...others].every(rect => !crosses(rect, box)),
    panelVisible: box.top >= 0 && box.top < innerHeight, overLine: crosses(box, held), inViewport: box.left >= 0 && box.right <= document.documentElement.clientWidth,
    open: [...document.querySelectorAll('.ui-help__panel')].filter(el => !el.hidden).length};
}, [scope, index]);
// Each help button's accessible name and description, from Chromium's accessibility tree. Firefox and
// WebKit give a page no way to read theirs, so there they come from aria-label and aria-describedby.
async function accessible(tab, scope) {
  if (options.browser !== 'chromium') {
    return tab.$$eval(`${scope} .ui-help__button`, buttons => buttons.map(button => {
      const described = (button.getAttribute('aria-describedby') ?? '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.textContent.trim() ?? '').join(' ');
      return {name: button.getAttribute('aria-label') ?? button.textContent.trim(), description: described || null};
    }));
  }
  const cdp = await tab.context().newCDPSession(tab);
  await cdp.send('DOM.enable');
  await cdp.send('Accessibility.enable');
  const {root} = await cdp.send('DOM.getDocument', {depth: -1});
  const {nodeIds} = await cdp.send('DOM.querySelectorAll', {nodeId: root.nodeId, selector: `${scope} .ui-help__button`});
  const named = [];
  for (const nodeId of nodeIds) {
    const {nodes} = await cdp.send('Accessibility.getPartialAXTree', {nodeId, fetchRelatives: false});
    named.push({name: nodes[0]?.name?.value ?? null, description: nodes[0]?.description?.value ?? null});
  }
  await cdp.detach();
  return named;
}
// Every help in the scope, by keyboard, then by hover or tap, then the pinned guide link and one at a time.
async function exerciseHelp(tab, at, scope, {touch, outside, expected}) {
  const buttons = helpButtons(tab, scope);
  const total = await buttons.count();
  const keys = await tab.$$eval(`${scope} .ui-help__panel`, panels => panels.map(panel => panel.dataset.help));
  check(keys.join() === expected.join(), `${at}: help on ${keys.join(', ')}`);
  // Tab reaches every help button, and each opens as it takes focus.
  await tab.evaluate(() => {document.activeElement?.blur(); window.scrollTo(0, 0);});
  const reached = new Map(), path = [];
  for (let press = 0; press < 400 && reached.size < total; press++) {
    await tab.keyboard.press('Tab');
    if (path.length < 12) path.push(await focused(tab));
    const index = await tab.evaluate(scope => [...document.querySelectorAll(`${scope} .ui-help__button`)].indexOf(document.activeElement), scope);
    if (index >= 0 && !reached.has(index)) reached.set(index, await helpState(tab, scope, index));
  }
  check(reached.size === total && [...reached.values()].every(state => state.shown && state.focused && state.ring >= 3),
    `${at}: Tab reaches all ${total} help buttons, and each opens on focus with a 3px ring (${[...reached.values()].map(state => `${state.key} ${state.shown} ${state.ring}`).join(', ')}${reached.size < total ? `; focus went ${path.join(' > ')}` : ''})`);
  const named = await accessible(tab, scope);
  for (let index = 0; index < total; index++) {
    const entry = helpFile[keys[index]];
    await buttons.nth(index).focus();
    const opened = await helpState(tab, scope, index);
    check(opened.shown && opened.text === entry.text && named[index].name === `About ${entry.title}` && opened.size[0] >= 44 && opened.size[1] >= 44 && opened.overlaps.length === 0,
      `${at} ${opened.key}: focus shows the copy file's sentence; the button is "${named[index].name}", ${opened.size.map(Math.round).join('x')}, and overlaps no control (${opened.overlaps.join() || 'none'})`);
    if (entry.guide) check(opened.role === 'note' && opened.expanded === 'true', `${at} ${opened.key}: a panel is a note its button expands`);
    else check(opened.role === 'tooltip' && opened.expanded === null && named[index].description === entry.text, `${at} ${opened.key}: the hint's sentence is its button's accessible description (${named[index].description})`);
    if (touch) check(opened.position === 'static' && opened.clearOfLine && opened.panelVisible, `${at} ${opened.key}: in the flow, over nothing else on its line, and scrolled into view (${JSON.stringify({p: opened.position, clear: opened.clearOfLine, visible: opened.panelVisible})})`);
    else check(opened.position === 'absolute' && !opened.overLine && opened.inViewport, `${at} ${opened.key}: placed off its line and inside the viewport's sides (${JSON.stringify({over: opened.overLine, inside: opened.inViewport})})`);
    await tab.keyboard.press('Escape');
    const closed = await helpState(tab, scope, index);
    check(!closed.shown && closed.focused, `${at} ${opened.key}: Escape closes it and focus stays on its button`);
    // Hover, and the pointer onto the panel; or a tap, then a tap outside.
    await tab.evaluate(() => document.activeElement?.blur());
    if (touch) {
      await buttons.nth(index).tap();
      const tapped = await helpState(tab, scope, index);
      await tab.tap(outside);
      const away = await helpState(tab, scope, index);
      check(tapped.shown && !away.shown, `${at} ${opened.key}: a tap opens it and a tap outside closes it`);
    } else {
      await buttons.nth(index).hover();
      const hovered = await helpState(tab, scope, index);
      await tab.locator(`${scope} .ui-help__panel`).nth(index).hover();
      await tab.waitForTimeout(500);
      const onPanel = await helpState(tab, scope, index);
      await tab.mouse.move(2, 450);
      await tab.waitForTimeout(500);
      const left = await helpState(tab, scope, index);
      check(hovered.shown && onPanel.shown && !left.shown, `${at} ${opened.key}: hover opens it, the pointer may move onto it, and leaving closes it`);
    }
    // A press pins a panel; Tab reaches its guide link at the build's revision; Escape puts focus back on the button.
    if (entry.guide) {
      if (touch) await buttons.nth(index).tap(); else await buttons.nth(index).click();
      await tab.keyboard.press('Tab');
      const link = await tab.evaluate(() => ({href: document.activeElement.getAttribute('href'), target: document.activeElement.target, rel: document.activeElement.rel, guide: document.activeElement.matches('.ui-help__guide')}));
      check(link.guide && link.href === `https://github.com/pwngh/gtm-connector/blob/${builtRevision}/${entry.guide.path}` && link.target === '_blank' && link.rel === 'noopener noreferrer',
        `${at} ${opened.key}: pinned, Tab reaches its guide link ${link.href}`);
      await tab.keyboard.press('Escape');
      const back = await helpState(tab, scope, index);
      check(!back.shown && back.focused, `${at} ${opened.key}: Escape from the guide link closes it and focus returns to its button`);
    }
  }
  // One open at a time: pinning one and then focusing another closes the first.
  if (total > 1) {
    if (touch) await buttons.nth(0).tap(); else await buttons.nth(0).click();
    await buttons.nth(1).focus();
    const [first, second] = [await helpState(tab, scope, 0), await helpState(tab, scope, 1)];
    check(!first.shown && second.shown && second.open === 1, `${at}: one help is open at a time (${JSON.stringify({first: first.shown, second: second.shown, open: second.open})})`);
    await tab.keyboard.press('Escape');
  }
}
for (const [width, touch] of [[390, true], [1440, false]]) {
  await scenario(`help on the deal card ${width}`, async () => {
    const at = `help ${width}`;
    const tab = await page({width, touch});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    const roles = await tab.$$eval('.dc-people [data-group]', groups => groups.filter(group => group.querySelector('.dc-attr')).map(group => `role.${group.dataset.group}`));
    await exerciseHelp(tab, `${at} deal card`, '.dc', {touch, outside: '.dc-title', expected: ['countdown', 'citation', 'arr', 'quotation', ...roles, 'record', 'not_read', 'seal']});
    if (options.screenshots) {
      await tab.evaluate(() => document.activeElement?.blur());
      await tab.locator('.dc-not-read .ui-help__button').focus();
      await tab.screenshot({path: join(options.screenshots, `help-not-read-${width}.png`)});
      await tab.keyboard.press('Escape');
    }
    // The proof drawer: "Read" carries the receipt's hint; Escape closes the hint before the drawer.
    await openInApp(tab, '/deals/acc-003?unit=5');
    await tab.waitForSelector('[role=dialog] [data-proof="unit"]');
    await exerciseHelp(tab, `${at} drawer`, '[role=dialog]', {touch, outside: '.ui-dialog__title', expected: ['ops.receipt']});
    await tab.locator('[role=dialog] .ui-help__button').focus();
    await tab.keyboard.press('Escape');
    const drawer = await tab.locator('[role=dialog]').count();
    await tab.keyboard.press('Escape');
    await tab.waitForSelector('[role=dialog]', {state: 'detached'});
    check(drawer === 1, `${at} drawer: the first Escape closes the receipt's hint and the drawer stays; the second closes the drawer`);
    // The Communications tab: the first sentence of each role carries that role's help.
    await openInApp(tab, '/deals/acc-003/communications');
    await tab.waitForSelector('.dc-comms');
    const talkRoles = await tab.$$eval('.dc-comms .dc-line', lines => [...new Set(lines.map(line => line.querySelector('.dc-attr__part:nth-child(2)').firstChild.textContent.split(' ')[0].toLowerCase()))].map(role => `role.${role}`));
    await exerciseHelp(tab, `${at} communications`, '.dc-comms', {touch, outside: '.dc-title', expected: talkRoles});
    // Coverage: its title carries the help.
    await openInApp(tab, '/ops/coverage');
    await tab.waitForSelector('.cv-title');
    await exerciseHelp(tab, `${at} coverage`, '.cv', {touch, outside: '.cv-meta', expected: ['ops.coverage']});
    if (options.screenshots) {
      await tab.evaluate(() => document.activeElement?.blur());
      await tab.locator('.cv .ui-help__button').focus();
      await tab.screenshot({path: join(options.screenshots, `help-coverage-${width}.png`)});
    }
    // Activity: its title carries the help, and the seal's drawer the seal's and the arguments'.
    await openInApp(tab, '/ops/activity?card=card-acc-003');
    await tab.waitForSelector('.ac-table tbody .ac-ref');
    await exerciseHelp(tab, `${at} activity`, '.ac', {touch, outside: '.ac-meta', expected: ['ops.activity']});
    await openInApp(tab, '/ops/activity?card=card-acc-003&receipt=seal-acc-003');
    await tab.waitForSelector('[role=dialog] [data-field="arguments"]');
    await exerciseHelp(tab, `${at} receipt drawer`, '[role=dialog]', {touch, outside: '.ui-dialog__title', expected: ['seal', 'receipt.arguments']});
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

// 18. The Communications tab: every sentence the card read, call by call and
// message by message, oldest first, at 320, 390 and 1440 wide, light and dark;
// a number opens the proof drawer there; and for every recorded card each
// sentence unit is listed once, the groups in time order, the ones with no
// usable time last.
const commsFacts = tab => tab.evaluate(() => ({
  path: location.pathname,
  tabs: [...document.querySelectorAll('.dc-tab')].map(el => `${el.textContent} ${el.getAttribute('href')}${el.getAttribute('aria-current') ? ' open' : ''}`).join(', '),
  counts: document.querySelector('.dc-comms__counts')?.textContent ?? null,
  heads: [...document.querySelectorAll('.dc-talk__head')].map(el => [...el.children].map(part => part.textContent).join(' ')),
  refs: [...document.querySelectorAll('.dc-talk')].map(el => el.dataset.talk),
  untimed: [...document.querySelectorAll('.dc-comms__untimed .dc-talk')].map(el => el.dataset.talk),
  units: [...document.querySelectorAll('[data-comm-unit]')].map(el => Number(el.dataset.commUnit)),
  quotes: [...document.querySelectorAll('.dc-comms blockquote')].map(el => ({n: Number(el.dataset.unit), text: el.textContent, children: el.children.length})),
  speakers: [...document.querySelectorAll('.dc-comms .dc-attr')].map(el => el.textContent),
  ledger: document.querySelectorAll('.dc-ledger, .dc-plain').length,
  overflow: document.documentElement.scrollWidth - innerWidth,
  injected: Boolean(window.__injected) || Boolean(document.querySelector('.dc img')),
  local: Object.keys(localStorage), session: Object.keys(sessionStorage)}));
const sentenceUnits = card => card.units.filter(unit => unit.kind === 'sentence');
for (const [width, scheme] of matrix) {
  await scenario(`communications ${width} ${scheme}`, async () => {
    const at = `communications ${width} ${scheme}`;
    const tab = await page({width, scheme});
    await tab.goto(thornfield, {waitUntil: 'networkidle'});
    await signIn(tab);
    await tab.waitForSelector('.dc-ledger');
    await tab.click('.dc-tab[href="/deals/acc-003/communications"]');
    await tab.waitForSelector('.dc-comms');
    await until(tab, () => document.activeElement?.matches('main h1'));
    const facts = await commsFacts(tab);
    check(facts.path === '/deals/acc-003/communications' && facts.tabs === 'Card /deals/acc-003, Communications /deals/acc-003/communications open, Text /deals/acc-003/text' && facts.ledger === 0,
      `${at}: the Communications tab opens at its own address beside Card and Text (${facts.path}; ${facts.tabs})`);
    check(facts.counts === '2 calls, 1 Slack message and 6 sentences were read. The card shows every sentence.', `${at}: one line counts the calls, messages and sentences (${facts.counts})`);
    check(facts.heads.join(' | ') === 'Call 12 Jan 2027, 15:00:00 UTC | Call 12 Jan 2027, 15:00:00 UTC | Slack message 12 Jan 2027, 16:00:00 UTC' &&
      facts.refs.join() === 'calls/call_notes/note-003,calls/call_notes/note-003b,slack/messages/msg-003' && facts.untimed.length === 0,
      `${at}: one group per call or message, oldest first, each headed by what it is and its time (${facts.heads.join(' | ')})`);
    check(facts.units.join() === sentenceUnits(dealCard).map(unit => unit.n).join() &&
      facts.quotes.map(quote => `${quote.n} ${quote.text}`).join('|') === sentenceUnits(dealCard).map(unit => `${unit.n} ${unit.body.text}`).join('|') && facts.quotes.every(quote => quote.children === 0),
      `${at}: each sentence once, in unit order, as one blockquote holding exactly its text (${facts.units.join()})`);
    check(!facts.injected && facts.speakers.every(line => line.includes(hostile)), `${at}: hostile speakers stay text`);
    check(facts.overflow <= 0, `${at}: the page does not scroll sideways (${facts.overflow}px)`);
    const measured = await measure(tab, cardParts);
    check(healthy(measured), `${at}: ${reported(at, measured)}`);
    await tab.focus('.dc-comms .dc-cite');
    await tab.keyboard.press('Shift+Tab');
    await tab.keyboard.press('Tab');
    const focus = await ring(tab);
    check(fits(focus) && focus.name === 'Source 4', `${at}: a number takes focus at 44 by 44 with a ${focus.outline}px ring (${focus.name})`);
    if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `deal-communications-${width}-${scheme}.png`), fullPage: true});
    await tab.click('.dc-comms .dc-cite[data-cite="5"]');
    await tab.waitForSelector('[role=dialog] [data-proof="unit"]');
    const url = new URL(tab.url());
    check(url.pathname === '/deals/acc-003/communications' && url.searchParams.get('unit') === '5' && await tab.textContent('[role=dialog] blockquote') === unitText(5),
      `${at}: pressing [5] opens the proof drawer on unit 5 on this tab (${url.pathname}${url.search})`);
    await tab.keyboard.press('Escape');
    await tab.waitForSelector('[role=dialog]', {state: 'detached'});
    await atCite(tab, 5);
    check(!new URL(tab.url()).searchParams.has('unit') && new URL(tab.url()).pathname === '/deals/acc-003/communications', `${at}: Escape closes it and returns focus to [5]`);
    check(facts.local.every(key => ['gtm-connector-theme', 'gtm-connector-deals-hint', 'gtm-connector-proof-seen'].includes(key)) && facts.session.length === 0,
      `${at}: storage holds only display keys (local ${facts.local.join()}; session ${facts.session.join()})`);
    check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
    await tab.context().close();
  });
}

await scenario('communications: every recorded card lists each sentence unit once, groups in time order', async () => {
  serveRecorded();
  const tab = await page({width: 390});
  await tab.goto(`${base}/deals/acc-900/communications?card=${recorded[0].id}`, {waitUntil: 'networkidle'});
  await signIn(tab);
  // A time that can be ordered has an offset that parses, as the app reads it.
  const usable = at => typeof at === 'string' && /(?:Z|[+-]\d\d:?\d\d)$/i.test(at) && Number.isFinite(Date.parse(at)) ? Date.parse(at) : null;
  const wrong = [];
  let units = 0, untimedCards = 0, hidden = 0, shot = null;
  for (const card of recorded) {
    const path = `/deals/acc-900/communications?card=${card.id}`;
    await openInApp(tab, path);
    await until(tab, to => document.querySelector('.dc-tab[aria-current="page"]')?.getAttribute('href') === to && document.querySelector('.dc-comms'), path);
    const facts = await commsFacts(tab);
    const sentences = sentenceUnits(card);
    units += sentences.length;
    hidden += sentences.filter(unit => unit.shown !== 'yes' || unit.body.partial).length;
    const earliest = new Map();
    for (const unit of sentences) {
      const time = usable(unit.body.at), known = earliest.get(unit.record_ref);
      earliest.set(unit.record_ref, time === null ? (known ?? null) : known == null ? time : Math.min(known, time));
    }
    const timed = facts.refs.slice(0, facts.refs.length - facts.untimed.length);
    const times = timed.map(ref => earliest.get(ref));
    const once = [...facts.units].sort((a, b) => a - b).join() === sentences.map(unit => unit.n).sort((a, b) => a - b).join();
    const ordered = times.every((time, i) => time !== null && (i === 0 || times[i - 1] <= time)) && facts.untimed.every(ref => earliest.get(ref) === null) &&
      facts.refs.slice(timed.length).join() === facts.untimed.join() && [...facts.refs].sort().join() === [...earliest.keys()].sort().join();
    if (!once || !ordered || facts.overflow > 0) wrong.push(card.id);
    if (facts.untimed.length) untimedCards += 1;
    // One card with sentences the card leaves out and a group with no usable time, pictured.
    if (options.screenshots && facts.untimed.length && sentences.some(unit => unit.shown !== 'yes') && !shot) {
      shot = card.id;
      await tab.screenshot({path: join(options.screenshots, 'deal-communications-recorded-390.png'), fullPage: true});
    }
  }
  check(recorded.length === 91 && wrong.length === 0 && untimedCards > 0 && hidden > 0,
    `each of ${recorded.length} golden cards lists its ${units} sentence units once (${hidden} not on the card), groups oldest first, ${untimedCards} cards with groups under No time recorded (wrong: ${wrong.slice(0, 5).join(', ') || 'none'})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `recorded cards: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

// 20. Activity: what ran for one card. The seal first, then one row per
// distinct read receipt; a row opens the receipt whole; Check the seal against
// the true bytes and against bytes with one byte changed; a refused receipt;
// no card in the address; and the layout at 320, 390, 768 and 1440, light and dark.
const readsOfCard = card => [...new Set(card.units.map(unit => unit.receipt_id).filter(Boolean))];
const activityRows = card => [card.seal_receipt_id, ...readsOfCard(card)];
const rowsRead = tab => until(tab, count => document.querySelectorAll('.ac-table tbody tr').length === count &&
  ![...document.querySelectorAll('.ac-table tbody tr')].some(row => row.textContent.includes('Reading')), activityRows(cards['acc-003']).length);

await scenario('activity: no card in the address', async () => {
  const tab = await page({width: 390});
  await tab.goto(activity, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('.ac-choose');
  await apiSettled();
  const said = (await tab.innerText('.ac-choose')).replace(/\s+/g, ' ').trim();
  const href = await tab.getAttribute('.ac-choose a', 'href');
  check(said === 'Choose a deal in Coverage to see what ran for its card. Go to Coverage' && href === '/ops/coverage' && !stub.log.some(entry => /\/receipts\/|\/cards\/[^/]+$/.test(entry)),
    `with no card in the address Activity asks for a deal in Coverage and reads no card or receipt (${said})`);
  await tab.context().close();
});

await scenario('activity: the seal, then one row per distinct read receipt, each opening its receipt', async () => {
  const card = cards['acc-003'], expected = activityRows(card);
  const tab = await page({width: 1440});
  await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
  await signIn(tab);
  await rowsRead(tab);
  const facts = await tab.evaluate(() => ({rows: [...document.querySelectorAll('.ac-table tbody tr')].map(row => row.dataset.receipt),
    seal: [...document.querySelectorAll('.ac-table tbody tr[data-seal]')].map(row => row.dataset.receipt),
    head: [...document.querySelectorAll('.ac-table thead th')].map(th => th.textContent), first: document.querySelector('.ac-table tbody tr').innerText.replace(/\s+/g, ' ').trim(),
    card: document.querySelector('.ac-card__facts').innerText.replace(/\s+/g, ' ').trim()}));
  check(facts.rows.join() === expected.join() && facts.seal.join() === card.seal_receipt_id && expected.length === 5,
    `the table lists the seal receipt, then one row per distinct read receipt (${facts.rows.join(', ')})`);
  check(facts.head.join() === 'Started,Tool,Status,Rows,Elapsed' && facts.first === '15 Jan 2027, 15:00:00 UTC , open the seal receipt Seal save_brief ok 200 1 2.50 ms',
    `the columns, and the seal's row (${facts.first})`);
  check(facts.card === 'Card card-acc-003 Account acc-003 Published 15 Jan 2027, 15:00:04 UTC', `the card's ID, account and publish time (${facts.card})`);
  const reads = stub.log.filter(entry => entry.startsWith('GET /api/v1/receipts/')).length;
  check(reads === expected.length, `each receipt is read once (${reads} reads)`);
  const second = expected[1];
  await tab.click(`.ac-table tr[data-receipt="${second}"] .ac-open`);
  await tab.waitForSelector('[role=dialog] [data-field="delivered_at"]');
  await until(tab, () => document.activeElement?.matches('.ui-dialog__title'));
  const drawer = await tab.evaluate(() => ({fields: [...document.querySelectorAll('[role=dialog] [data-field]')].map(el => el.dataset.field),
    title: document.querySelector('.ui-dialog__title').textContent, receipt: new URL(location.href).searchParams.get('receipt'), seal: document.querySelectorAll('[role=dialog] .ac-seal').length,
    tool: document.querySelector('[role=dialog] [data-field="tool"] dd').textContent}));
  check(drawer.title === 'Read receipt' && drawer.receipt === second && drawer.fields.length === 22 && drawer.seal === 0 && drawer.tool === card.units.find(unit => unit.receipt_id === second).tool,
    `a row opens its receipt whole, ${drawer.fields.length} fields, with ?receipt= in the address (${drawer.receipt})`);
  await tab.keyboard.press('Escape');
  await tab.waitForSelector('[role=dialog]', {state: 'detached'});
  await until(tab, id => document.activeElement?.closest('tr')?.dataset.receipt === id, second);
  check(!new URL(tab.url()).searchParams.has('receipt'), 'Escape closes the drawer, removes ?receipt= and returns focus to its row');
  check(tab.errors.length === 0 && violations(tab).length === 0, `activity: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('activity: the proof drawer opens the receipt in Ops', async () => {
  const tab = await page({width: 1440});
  await tab.goto(`${thornfield}?unit=5`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('[role=dialog] .dc-proof__receipt');
  await tab.click('[role=dialog] .dc-proof__receipt');
  await tab.waitForSelector('[role=dialog] [data-field="rows"]');
  const opened = await tab.evaluate(() => ({path: location.pathname, card: new URL(location.href).searchParams.get('card'), receipt: new URL(location.href).searchParams.get('receipt'),
    shown: document.querySelector('[role=dialog] [data-field="id"] dd')?.textContent}));
  check(opened.path === '/ops/activity' && opened.card === 'card-acc-003' && opened.receipt === 'receipt-acc-003-read_call_notes' && opened.shown === 'receipt-acc-003-read_call_notes',
    `Open the receipt in Ops opens Activity for the card with that receipt's drawer (${JSON.stringify(opened)})`);
  await tab.context().close();
});

await scenario('activity: Check the seal', async () => {
  const sealed = sealedFiles.get('card-acc-003');
  const tab = await page({width: 1440});
  await tab.goto(`${thornfieldActivity}&receipt=seal-acc-003`, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector('[role=dialog] .ac-seal button');
  const title = await tab.textContent('.ui-dialog__title');
  await tab.click('[role=dialog] .ac-seal button');
  await tab.waitForSelector('[data-seal-check="match"]');
  const match = await tab.textContent('.ac-seal__said');
  // The stub changes one byte of the stored card and keeps the digest it sends with it.
  stub.steer = (method, path) => path === '/api/v1/cards/card-acc-003/bytes'
    ? {raw: Buffer.from(sealed.bytes).map((byte, at) => (at === 40 ? byte ^ 1 : byte)), type: 'application/octet-stream', headers: {'Cache-Control': 'no-store', 'X-Content-SHA256': sealed.sha256}} : null;
  await tab.click('[role=dialog] .ac-seal button');
  await tab.waitForSelector('[data-seal-check="mismatch"]');
  const mismatch = await tab.textContent('.ac-seal__said');
  check(title === 'Seal receipt' && match === 'These bytes match the digest the seal names.' && mismatch === 'These bytes do not match the digest the seal names.',
    `Check the seal says match for the true bytes and no match with one byte changed (${match} / ${mismatch})`);
  check(tab.errors.length === 0 && violations(tab).length === 0, `seal check: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
  await tab.context().close();
});

await scenario('activity: a refused receipt', async () => {
  const refusedId = 'receipt-acc-003-read_call_notes';
  stub.steer = (method, path) => path === `/api/v1/receipts/${refusedId}` ? {status: 403, code: 'not_permitted'} : null;
  const tab = await page({width: 390});
  await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
  await signIn(tab);
  await tab.waitForSelector(`.ac-table tr[data-receipt="${refusedId}"] [data-refusal="not_permitted"]`);
  const row = (await tab.innerText(`.ac-table tr[data-receipt="${refusedId}"]`)).replace(/\s+/g, ' ').trim();
  await tab.click(`.ac-table tr[data-receipt="${refusedId}"] .ac-open`);
  await tab.waitForSelector('[role=dialog] [data-refusal="not_permitted"]');
  const drawer = await tab.evaluate(() => ({text: document.querySelector('[role=dialog] .ui-dialog__body').innerText.replace(/\s+/g, ' ').trim(),
    fields: document.querySelectorAll('[role=dialog] [data-field]').length, rows: document.querySelectorAll('.ac-table tbody tr').length}));
  check(row.includes('Not permitted') && row.includes('Server code: not_permitted') && drawer.text.includes('Not permitted.') && drawer.text.includes('Server code: not_permitted') &&
    drawer.fields === 0 && drawer.rows === activityRows(cards['acc-003']).length, `a refused receipt says Not permitted in its row and its drawer, and the table stays (${drawer.text.slice(0, 60)})`);
  await tab.context().close();
});

for (const width of widths) {
  for (const scheme of ['light', 'dark']) {
    await scenario(`activity layout ${width} ${scheme}`, async () => {
      const at = `activity ${width} ${scheme}`;
      const tab = await page({width, scheme});
      await tab.goto(thornfieldActivity, {waitUntil: 'networkidle'});
      await signIn(tab);
      await rowsRead(tab);
      const display = await tab.evaluate(() => getComputedStyle(document.querySelector('.ac-table tbody tr')).display);
      check(display === (width < 720 ? 'grid' : 'table-row'), `${at}: receipts are ${width < 720 ? 'blocks' : 'table rows'} (${display})`);
      const facts = await measure(tab, '.fr-chrome, .ac');
      check(healthy(facts), `${at}: Activity, ${reported(at, facts)}`);
      await tab.focus('.ac-card .ac-link');
      await tab.keyboard.press('Tab');
      const rowRing = await ring(tab);
      check(rowRing.name.startsWith('15 Jan 2027') && fits(rowRing), `${at}: a row's link takes focus with its ring (${rowRing.name} ${Math.round(rowRing.width)}x${Math.round(rowRing.height)} ${rowRing.outline}px)`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `activity-${width}-${scheme}.png`), fullPage: true});
      await tab.keyboard.press('Enter');
      await tab.waitForSelector('[role=dialog] [data-field="delivered_at"]');
      await tab.click('[role=dialog] .ac-seal button');
      await tab.waitForSelector('[data-seal-check="match"]');
      const drawer = await measure(tab, '[role=dialog]');
      check(healthy(drawer), `${at}: the seal's drawer, ${reported(`${at} drawer`, drawer)}`);
      const box = await tab.locator('[role=dialog]').boundingBox(), view = await tab.evaluate(() => [document.documentElement.clientWidth, innerHeight]);
      const placed = width < 720 ? box.x <= 0 && box.y <= 0 && box.x + box.width >= view[0] && box.y + box.height >= view[1]
        : box.x >= 0 && box.y >= 0 && box.x + box.width <= view[0] + 1 && box.y + box.height <= view[1] + 1;
      check(placed, `${at}: the drawer ${width < 720 ? 'covers the viewport' : 'is in view'} (${Math.round(box.x)},${Math.round(box.y)} ${Math.round(box.width)}x${Math.round(box.height)} in ${view.join('x')})`);
      if (options.screenshots) await tab.screenshot({path: join(options.screenshots, `activity-drawer-${width}-${scheme}.png`)});
      check(tab.errors.length === 0 && violations(tab).length === 0, `${at}: no page errors or policy violations (${[...tab.errors, ...violations(tab)].join(' | ')})`);
      await tab.context().close();
    });
  }
}

// 21. The server's policy without 'unsafe-inline' in style-src: every page
// opened under it, and what it refuses. The pages of the app are checked; the
// static 404 page is reported, as it holds its own style element.
const servedPolicy = securityHeaders['Content-Security-Policy'];
const strictPolicy = servedPolicy.replace("style-src 'self' 'unsafe-inline'", "style-src 'self'");
await scenario('every page under a style policy without unsafe-inline', async () => {
  securityHeaders['Content-Security-Policy'] = strictPolicy;
  try {
    check(strictPolicy !== servedPolicy && !strictPolicy.includes('unsafe-inline'), `the strict policy: ${strictPolicy}`);
    const tab = await page({width: 390});
    const refusedOn = {};
    const visit = async (label, open) => {
      const before = violations(tab).length;
      await open();
      await tab.waitForTimeout(300);
      refusedOn[label] = violations(tab).slice(before);
    };
    await visit('sign-in', async () => {await tab.goto(`${base}/deals`, {waitUntil: 'networkidle'}); await tab.waitForSelector('.fr-signin');});
    await visit('deals', async () => {await signIn(tab); await tab.waitForSelector('.deals-row');});
    await visit('deal card', async () => {await openInApp(tab, '/deals/acc-003'); await tab.waitForSelector('.dc-ledger');});
    await visit('help', async () => {await tab.click('.dc-seal .ui-help__button'); await tab.waitForSelector('.dc-seal .ui-help__panel:not([hidden])');});
    await visit('proof drawer', async () => {await openInApp(tab, '/deals/acc-003?unit=5'); await tab.waitForSelector('[role=dialog] [data-proof="unit"]');});
    await visit('text tab', async () => {await openInApp(tab, '/deals/acc-003/text'); await tab.waitForSelector('.dc-plain');});
    await visit('communications tab', async () => {await openInApp(tab, '/deals/acc-003/communications'); await tab.waitForSelector('.dc-comms');});
    await visit('coverage', async () => {await openInApp(tab, '/ops/coverage'); await tab.waitForSelector('.cv-table');});
    await visit('activity and the seal check', async () => {
      await openInApp(tab, '/ops/activity?card=card-acc-003&receipt=seal-acc-003');
      await tab.waitForSelector('[role=dialog] .ac-seal button');
      await tab.click('[role=dialog] .ac-seal button');
      await tab.waitForSelector('[data-seal-check="match"]');
      await tab.keyboard.press('Escape');
      await tab.waitForSelector('[role=dialog]', {state: 'detached'});
    });
    await visit('menu and palette', async () => {
      await tab.click('.fr-menu-button');
      await tab.waitForSelector('[role=dialog] .fr-menu');
      await tab.keyboard.press('Escape');
      await tab.waitForSelector('[role=dialog]', {state: 'detached'});
      await tab.keyboard.press('Control+k');
      await tab.waitForSelector('.ui-palette__option');
      await tab.keyboard.press('Escape');
    });
    await visit('no page in the app', async () => {await openInApp(tab, '/ops/nothing'); await tab.waitForSelector('.fr-nopage__title');});
    const app = Object.entries(refusedOn).filter(([, refused]) => refused.length);
    check(app.length === 0 && tab.errors.length === 0,
      `every page of the app renders with nothing refused under the strict policy (${Object.keys(refusedOn).join(', ')}; refused: ${app.map(([label, refused]) => `${label}: ${refused.join(' | ')}`).join('; ') || 'none'})`);
    const styled = await tab.evaluate(() => getComputedStyle(document.querySelector('.fr-bar')).borderBottomStyle);
    check(styled === 'solid', `the app's stylesheet applies under the strict policy (the bar's rule is ${styled})`);
    await tab.context().close();
    const missing = await page({width: 390});
    await missing.goto(`${base}/history`, {waitUntil: 'networkidle'});
    await missing.waitForTimeout(300);
    const staticRefused = violations(missing);
    results.push(`note the static 404 page under the strict policy: ${staticRefused.length ? `refused ${staticRefused.join(' | ')}` : 'nothing refused'}`);
    await missing.context().close();
  } finally {
    securityHeaders['Content-Security-Policy'] = servedPolicy;
  }
});

await browser.close();
server.close();
console.log(results.join('\n'));
// The lowest contrast measured for each text colour in the frame, by width, theme and screen.
if (options.contrast) console.log(`\n${measured.join('\n')}`);
console.log(process.exitCode ? `\n${results.filter(line => line.startsWith('FAIL')).length} problems` : '\nNo problems');
