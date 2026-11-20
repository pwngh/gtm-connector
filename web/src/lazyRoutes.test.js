import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToPipeableStream, renderToStaticMarkup} from 'react-dom/server';
import {PassThrough} from 'node:stream';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createServer, build} from 'vite';
import {MemoryRouter, RouterProvider, createMemoryRouter} from 'react-router';
import {MotionConfig} from 'motion/react';
import {initialState, renewalFixture, historyFixture} from './renewalFixtures.js';
import {buildWorklist, selectWorklist} from './worklist.js';

let server, App, RouteContent, pageLoaders, lazyPage, forgetFailedPages;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  RouteContent = (await server.ssrLoadModule('/src/RouteContent.jsx')).default;
  ({pageLoaders, lazyPage, forgetFailedPages} = await server.ssrLoadModule('/src/lazyPages.js'));
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
  const renderer = renderToPipeableStream(React.createElement(MotionConfig, {isStatic: true}, element), {
    onShellReady() {renderer.pipe(output); shellReady();}, onShellError: fail, onError: fail,
  });
  return {done, shell, firstChunk, html: () => chunks.join(''), abort: () => renderer.abort()};
}
function render(path) {
  return stream(React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))).done;
}
const ids = html => [...html.matchAll(/data-record-id="([^"]+)"/g)].map(match => match[1]);
const routes = [
  {path: '/playbooks', module: 'Playbooks', marker: 'pb-page'},
  {path: '/renewals', module: 'Worklists', marker: 'wl-page'},
  {path: '/queue', module: 'Worklists', marker: 'wl-page'},
  {path: renewalFixture.paths.root + '/readiness', module: 'Readiness', marker: 'rd-page'},
  {path: renewalFixture.paths.workplan, module: 'WorkPlan', marker: 'work-grid'},
  {path: renewalFixture.paths.evidence, module: 'Evidence', marker: 'evidence-library'},
  {path: renewalFixture.paths.history, module: 'History', marker: 'hs-page'},
  {path: '/operations', module: 'FollowUpReport', marker: 'ops-page'},
];

for (const route of routes) test('a direct lazy route resolves its page and keeps shared chrome: ' + route.path, async () => {
  const html = await render(route.path);
  assert.match(html, new RegExp('class="[^"]*' + route.marker));
  assert.match(html, /id="workspace-navigation"/);
  assert.match(html, /class="topbar"/);
  assert.match(html, /class="page-footer"/);
  assert.match(html, /aria-label="Dark theme" aria-pressed="true"/);
  assert.doesNotMatch(html, /Page unavailable/);
  const module = await pageLoaders[route.module]();
  assert.equal(typeof module.default, 'function');
  if (route.path.startsWith(renewalFixture.paths.root + '/')) {
    assert.ok(html.includes('aria-current="page" class="selected" href="' + route.path + '"'));
  }
});

test('a pending import exposes a live loading state, then resolves actual filtered records', async () => {
  let release;
  const gate = new Promise(resolve => {release = resolve;});
  const Deferred = React.lazy(() => gate.then(pageLoaders.Worklists));
  const path = '/queue?scope=illustrative&sort=changed';
  const data = buildWorklist(initialState);
  const pending = stream(React.createElement(MemoryRouter, {initialEntries: [path]},
    React.createElement('main', null, React.createElement(RouteContent, {path: '/queue', label: 'Work queue'}, React.createElement(Deferred, {data, view: 'queue'})))));
  try {
    await pending.shell;
    await pending.firstChunk;
    assert.match(pending.html(), /role="status" aria-live="polite" aria-atomic="true"/);
    assert.equal(ids(pending.html()).length, 0);
    assert.doesNotMatch(pending.html(), /class="wl-page/);
    release();
    const html = await pending.done;
    const expected = selectWorklist(data, 'queue', 'scope=illustrative&sort=changed');
    assert.deepEqual(ids(html), expected.rows.map(row => row.id));
  } finally {release(); pending.done.catch(() => {}); pending.abort();}
});

test('Back and Forward restore the lazy route, filters, ordering and selected worklist row', async () => {
  const data = buildWorklist(initialState);
  const selection = data.workItems.find(row => row.mode === 'illustrative').selectionKey;
  const path = '/queue?' + new URLSearchParams({scope: 'all', sort: 'changed', direction: 'desc', selection});
  const router = createMemoryRouter([{path: '*', element: React.createElement(App)}], {initialEntries: [path]});
  const renderRouter = () => stream(React.createElement(RouterProvider, {router})).done;
  try {
    const before = await renderRouter();
    const entry = historyFixture.baseline.id;
    const history = renewalFixture.paths.history + '?' + new URLSearchParams({entry, view: 'effective', as_of: entry});
    await router.navigate(history);
    const after = await renderRouter();
    assert.match(after, /class="hs-page/);
    assert.ok(after.includes('data-history-id="' + entry + '"'));
    await router.navigate(-1);
    const returned = await renderRouter();
    assert.equal(router.state.location.pathname + router.state.location.search, path);
    assert.deepEqual(ids(returned), ids(before));
    assert.match(returned, /class="wl-row is-selected"/);
    assert.match(returned, /aria-current="true"/);
    await router.navigate(1);
    assert.equal(router.state.location.pathname + router.state.location.search, history);
    assert.ok((await renderRouter()).includes('data-history-id="' + entry + '"'));
  } finally {router.dispose();}
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
  const Page = lazyPage(async () => {calls++; return {default: () => React.createElement('p', null, 'Dialog content')};});
  await Page.preload();
  const html = renderToStaticMarkup(React.createElement(React.Suspense, {fallback: 'Loading dialog'}, React.createElement(Page)));
  assert.equal(html, '<p>Dialog content</p>');
  assert.equal(calls, 1);
});

test('the shell can render without browser globals while the page is still loading', () => {
  assert.equal(typeof window, 'undefined');
  const html = renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: ['/renewals']}, React.createElement(App)));
  assert.match(html, /id="main" class="main-content" tabindex="-1"/);
  assert.match(html, /class="skip" href="#main"/);
});

test('production chunks keep page implementations out of the shell and stay below the default warning limit', async () => {
  const previous = process.env.NODE_ENV;
  process.env.NODE_ENV = 'production';
  let result;
  try {result = await build({configFile: fileURLToPath(new URL('../vite.config.js', import.meta.url)), logLevel: 'silent', build: {write: false}});}
  finally {if (previous === undefined) delete process.env.NODE_ENV; else process.env.NODE_ENV = previous;}
  const chunks = result.output.filter(asset => asset.type === 'chunk');
  const main = chunks.find(chunk => Object.keys(chunk.modules).some(id => id.endsWith('/src/main.jsx')));
  assert.ok(main.isEntry);
  assert.ok(Object.keys(main.modules).some(id => id.includes('/react-router/')));
  for (const name of new Set(routes.map(route => route.module))) {
    const chunk = chunks.find(chunk => Object.keys(chunk.modules).some(id => id.endsWith('/src/' + name + '.jsx')));
    assert.ok(chunk, name + ' has an emitted chunk');
    assert.notEqual(chunk.fileName, main.fileName);
    assert.ok(chunk.isDynamicEntry, name + ' is dynamically imported');
  }
  for (const chunk of chunks) assert.ok(Buffer.byteLength(chunk.code) < 500_000, chunk.fileName + ' exceeds 500 kB');
  // The palette ships with the shell, so the first keystrokes reach its input. Dialog content loads on its own.
  assert.ok(Object.keys(main.modules).some(id => id.endsWith('/src/CommandPalette.jsx')));
  const dialog = chunks.find(chunk => Object.keys(chunk.modules).some(id => id.endsWith('/src/WorkflowDialogContent.jsx')));
  assert.ok(dialog.isDynamicEntry);
  assert.notEqual(dialog.fileName, main.fileName);
  // Animation code loads with the pages that use it, never at startup.
  const startup = new Set(), visit = name => {if (startup.has(name)) return; startup.add(name); chunks.find(chunk => chunk.fileName === name).imports.forEach(visit);};
  visit(main.fileName);
  const animated = chunks.filter(chunk => Object.keys(chunk.modules).some(id => /\/node_modules\/(motion|motion-dom|motion-utils|framer-motion)\//.test(id)));
  assert.ok(animated.length > 0);
  for (const chunk of animated) assert.ok(!startup.has(chunk.fileName), chunk.fileName + ' loads animation code at startup');
  // All styles ship with the shell, page styles before shared ones, so the cascade
  // is the same whichever page loads first.
  const stylesheets = result.output.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css'));
  assert.equal(stylesheets.length, 1);
  assert.deepEqual([...main.viteMetadata.importedCss], [stylesheets[0].fileName]);
  for (const chunk of chunks) if (chunk !== main) assert.equal(chunk.viteMetadata.importedCss.size, 0, chunk.fileName + ' brings its own CSS');
  const cssText = String(stylesheets[0].source), at = marker => {const index = cssText.indexOf(marker); assert.ok(index >= 0, marker); return index;};
  const shared = at('--ink:#17243a');
  for (const marker of ['.wl-page{', '.wl-step-entry{', '.evidence-flow{', '.planes-segmented{', '.ops-page{', '.pb-page{', '.rd-page{', '.hs-page{', '.reviewer-decision-form']) assert.ok(at(marker) < shared, marker + ' comes after the shared styles');
  assert.ok(at(':root{--chrome-focus') > shared);
  // Equal-weight rules: the shared dark primary button comes last and wins on Operations.
  const operationsButton = cssText.search(/\.ops-page \.button\.primary\{/), sharedButton = cssText.search(/\.main-content \.button\.primary\{[^}]*linear-gradient\(#355be3/);
  assert.ok(operationsButton >= 0 && sharedButton > operationsButton);
  assert.doesNotMatch(readFileSync(new URL('../vite.config.js', import.meta.url), 'utf8'), /chunkSizeWarningLimit/);
});
