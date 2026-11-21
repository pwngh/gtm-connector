import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToPipeableStream, renderToStaticMarkup} from 'react-dom/server';
import {PassThrough} from 'node:stream';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createServer, build} from 'vite';
import {MemoryRouter} from 'react-router';
import {serverPaths} from './app/routes.js';

let server, ServerShell, RouteContent, pageLoaders, lazyPage, forgetFailedPages;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  ServerShell = (await server.ssrLoadModule('/src/app/ServerApp.jsx')).ServerShell;
  RouteContent = (await server.ssrLoadModule('/src/RouteContent.jsx')).default;
  ({pageLoaders} = await server.ssrLoadModule('/src/lazyPages.js'));
  ({lazyPage, forgetFailedPages} = await server.ssrLoadModule('/src/app/lazyPage.js'));
});
after(async () => {await server?.close();});

function stream(element) {
  const chunks = [], output = new PassThrough();
  let shellReady, fail;
  const firstChunk = new Promise(resolve => output.once('data', resolve));
  const shell = new Promise(resolve => {shellReady = resolve;});
  const done = new Promise((resolve, reject) => {
    fail = reject;
    output.on('data', chunk => chunks.push(chunk.toString()));
    output.on('end', () => resolve(chunks.join('')));
    output.on('error', reject);
  });
  const renderer = renderToPipeableStream(element, {
    onShellReady() {renderer.pipe(output); shellReady();}, onShellError: fail, onError: fail,
  });
  return {done, shell, firstChunk, html: () => chunks.join(''), abort: () => renderer.abort()};
}
const routes = [
  {path: serverPaths.evidence, module: 'ApiEvidence', marker: 'api-frame'},
  {path: serverPaths.readiness, module: 'ApiReadiness', marker: 'api-readiness'},
];

for (const route of routes) test('a direct lazy route resolves its page and keeps shared chrome: ' + route.path, async () => {
  const html = await stream(React.createElement(MemoryRouter, {initialEntries: [route.path]}, React.createElement(ServerShell))).done;
  assert.match(html, new RegExp('class="[^"]*' + route.marker));
  assert.match(html, /id="workspace-navigation"/);
  assert.match(html, /class="topbar"/);
  assert.match(html, /class="page-footer"/);
  assert.match(html, /aria-label="Dark theme" aria-pressed="true"/);
  assert.doesNotMatch(html, /Page unavailable/);
  const module = await pageLoaders[route.module]();
  assert.equal(typeof module.default, 'function');
});

test('a pending import exposes a live loading state, then resolves the page', async () => {
  let release;
  const gate = new Promise(resolve => {release = resolve;});
  const Deferred = React.lazy(() => gate.then(() => ({default: () => React.createElement('p', {className: 'loaded-page'}, 'Loaded page')})));
  const pending = stream(React.createElement(MemoryRouter, {initialEntries: [serverPaths.evidence]},
    React.createElement('main', null, React.createElement(RouteContent, {path: serverPaths.evidence, label: 'Evidence'}, React.createElement(Deferred)))));
  try {
    await pending.shell;
    await pending.firstChunk;
    assert.match(pending.html(), /role="status" aria-live="polite" aria-atomic="true"/);
    assert.match(pending.html(), /Loading (<!-- -->)?Evidence(<!-- -->)?…/);
    assert.doesNotMatch(pending.html(), /class="loaded-page"/);
    release();
    assert.match(await pending.done, /class="loaded-page"/);
  } finally {release(); pending.done.catch(() => {}); pending.abort();}
});

function attempt(element) {
  return new Promise((resolve, reject) => {
    const errors = [], output = new PassThrough();
    let html = '';
    output.on('data', chunk => {html += chunk;});
    output.on('end', () => resolve({html, errors}));
    const renderer = renderToPipeableStream(element, {onAllReady() {renderer.pipe(output);}, onShellError: reject, onError(error) {errors.push(error.message);}});
  });
}

test('a failed page import is tried again once its message has shown, not remembered for the session', async () => {
  let calls = 0, fail = true;
  const Page = lazyPage(async () => {
    calls++;
    if (fail) throw new Error('Failed to fetch a page module');
    return {default: () => React.createElement('p', null, 'Loaded page')};
  });
  const page = () => React.createElement(React.Suspense, {fallback: 'Loading page'}, React.createElement(Page));
  let result = await attempt(page());
  assert.deepEqual(result.errors, ['Failed to fetch a page module']);
  assert.doesNotMatch(result.html, /Loaded page/);
  fail = false;
  // Until the failure has reached its message, it stands rather than looping on new requests.
  result = await attempt(page());
  assert.deepEqual(result.errors, ['Failed to fetch a page module']);
  assert.equal(calls, 1);
  forgetFailedPages();
  result = await attempt(page());
  assert.deepEqual(result.errors, []);
  assert.match(result.html, /<p>Loaded page<\/p>/);
  assert.equal(calls, 2);
});

test('a preloaded page renders in the same pass, without a loading state', async () => {
  let calls = 0;
  const Page = lazyPage(async () => {calls++; return {default: () => React.createElement('p', null, 'Page content')};});
  await Page.preload();
  const html = renderToStaticMarkup(React.createElement(React.Suspense, {fallback: 'Loading page'}, React.createElement(Page)));
  assert.equal(html, '<p>Page content</p>');
  assert.equal(calls, 1);
});

test('the shell can render without browser globals while the page is still loading', () => {
  assert.equal(typeof window, 'undefined');
  const html = renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [serverPaths.evidence]}, React.createElement(ServerShell)));
  assert.match(html, /id="main" class="main-content" tabindex="-1"/);
  assert.match(html, /class="skip" href="#main"/);
});

// What the server's app downloads to start, JavaScript and CSS, before it had
// its own shell (main 7330759).
const startupBytesBefore = 737_028;
// The modules the server's app starts with.
const serverStartup = ['entry.jsx', 'app/boot.jsx', 'app/ServerApp.jsx', 'app/routes.js', 'app/lazyPage.js', 'lazyPages.js', 'ServerSwitch.jsx', 'serverSession.js', 'api.js',
  'RouteContent.jsx', 'focusElement.js', 'CommandPalette.jsx', 'WorkspaceDialog.jsx', 'WorkspaceControls.jsx',
  'readiness.css', 'styles.css', 'refinements.css', 'modern.css', 'atelier.css', 'workplan.css', 'chrome.css', 'api.css'];
// The modules the server's pages load after it.
const serverPages = ['ApiEvidence.jsx', 'ApiReadiness.jsx', 'ApiFrame.jsx', 'selection.js'];

test('production chunks: the server app starts with its shell alone, and each page loads on its own', async t => {
  const previous = process.env.NODE_ENV;
  process.env.NODE_ENV = 'production';
  let result;
  try {result = await build({configFile: fileURLToPath(new URL('../vite.config.js', import.meta.url)), logLevel: 'silent', build: {write: false}});}
  finally {if (previous === undefined) delete process.env.NODE_ENV; else process.env.NODE_ENV = previous;}
  const chunks = result.output.filter(asset => asset.type === 'chunk');
  const named = name => chunks.find(chunk => chunk.fileName === name);
  const sources = chunk => Object.keys(chunk.modules).filter(id => id.includes('/web/src/')).map(id => id.slice(id.indexOf('/web/src/') + 9).replace(/\?.*$/, ''));
  const holding = path => chunks.find(chunk => sources(chunk).includes(path));
  const graph = from => {const seen = new Set(), visit = name => {if (seen.has(name)) return; seen.add(name); named(name).imports.forEach(visit);}; visit(from.fileName); return seen;};
  const entry = chunks.find(chunk => chunk.isEntry);
  assert.ok(sources(entry).includes('entry.jsx'));
  assert.ok(Object.keys(entry.modules).some(id => id.includes('/react-router/')));

  // The build holds the server app's modules and nothing else of the source.
  for (const chunk of chunks) for (const path of sources(chunk)) assert.ok(serverStartup.includes(path) || serverPages.includes(path), path + ' is in the build');

  // Only the shell's modules and styles at start-up, and fewer bytes than before it had its own shell.
  const startup = graph(entry);
  const started = [...startup].flatMap(name => sources(named(name)));
  for (const path of started) assert.ok(serverStartup.includes(path), path + ' is in the server app’s start-up graph');
  const css = [...startup].flatMap(name => [...named(name).viteMetadata.importedCss]);
  assert.equal(css.length, 1);
  for (const chunk of chunks) if (chunk !== entry) assert.equal(chunk.viteMetadata.importedCss.size, 0, chunk.fileName + ' brings its own CSS');
  const asset = name => result.output.find(item => item.fileName === name);
  const bytes = [...startup].reduce((sum, name) => sum + Buffer.byteLength(named(name).code), 0) + Buffer.byteLength(asset(css[0]).source);
  t.diagnostic(`server app start-up: ${bytes} bytes of JavaScript and CSS, ${startupBytesBefore} before`);
  assert.ok(bytes <= startupBytesBefore - 120_000, `${bytes} bytes at start-up`);
  const serverCss = String(asset(css[0]).source);
  assert.ok(serverCss.indexOf('.rd-page{') < serverCss.indexOf('--ink:#17243a'));
  // The server pages load on their own.
  for (const page of ['ApiEvidence.jsx', 'ApiReadiness.jsx']) assert.ok(holding(page).isDynamicEntry && !startup.has(holding(page).fileName), page);
  for (const chunk of chunks) assert.ok(Buffer.byteLength(chunk.code) < 500_000, chunk.fileName + ' exceeds 500 kB');
  // The palette ships with the shell, so the first keystrokes reach its input.
  assert.ok(sources(entry).includes('CommandPalette.jsx'));
  assert.doesNotMatch(readFileSync(new URL('../vite.config.js', import.meta.url), 'utf8'), /chunkSizeWarningLimit/);
});
