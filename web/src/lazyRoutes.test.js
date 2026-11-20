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

let server, App, RouteContent, pageLoaders;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  RouteContent = (await server.ssrLoadModule('/src/RouteContent.jsx')).default;
  pageLoaders = (await server.ssrLoadModule('/src/lazyPages.js')).pageLoaders;
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
  assert.doesNotMatch(readFileSync(new URL('../vite.config.js', import.meta.url), 'utf8'), /chunkSizeWarningLimit/);
});
