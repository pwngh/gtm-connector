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

// What the server's app downloads to start, JavaScript and CSS, before it had
// its own shell (main 7330759): the walkthrough's shell, fixtures and styles.
const startupBytesBefore = 737_028;
// The modules the server's app starts with. Nothing else of the source is allowed there.
const serverStartup = ['entry.jsx', 'app/boot.jsx', 'app/ServerApp.jsx', 'app/routes.js', 'app/lazyPage.js', 'ServerSwitch.jsx', 'serverSession.js', 'api.js',
  'RouteContent.jsx', 'focusElement.js', 'CommandPalette.jsx', 'WorkspaceDialog.jsx', 'WorkspaceControls.jsx',
  'readiness.css', 'styles.css', 'refinements.css', 'modern.css', 'atelier.css', 'workplan.css', 'chrome.css', 'api.css'];
const walkthroughModules = ['main.jsx', 'lazyPages.js', 'renewalFixtures.js', 'renewalEvidence.js', 'playbookTemplates.js', 'workflow.js', 'workflowStorage.js', 'worklist.js',
  'readiness.js', 'playbookDrafts.js', 'playbooksBrowser.js', 'history.js', 'customerDecision.js', ...new Set(routes.map(route => route.module + '.jsx')), 'WorkflowDialogContent.jsx'];

test('production chunks: the server app starts with no walkthrough module, and the walkthrough keeps its pages and styles', async t => {
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

  // The server's app: only its own modules and styles at start-up, and fewer bytes.
  const startup = graph(entry);
  const started = [...startup].flatMap(name => sources(named(name)));
  for (const path of started) assert.ok(serverStartup.includes(path), path + ' is in the server app’s start-up graph');
  for (const path of walkthroughModules) assert.ok(!started.includes(path), path + ' loads with the server app');
  const css = [...startup].flatMap(name => [...named(name).viteMetadata.importedCss]);
  assert.equal(css.length, 1);
  const asset = name => result.output.find(item => item.fileName === name);
  const bytes = [...startup].reduce((sum, name) => sum + Buffer.byteLength(named(name).code), 0) + Buffer.byteLength(asset(css[0]).source);
  t.diagnostic(`server app start-up: ${bytes} bytes of JavaScript and CSS, ${startupBytesBefore} before`);
  assert.ok(bytes <= startupBytesBefore - 120_000, `${bytes} bytes at start-up`);
  const serverCss = String(asset(css[0]).source);
  for (const marker of ['.wl-page{', '.wl-step-entry{', '.evidence-flow{', '.planes-segmented{', '.ops-page{', '.pb-page{', '.hs-page{']) assert.ok(!serverCss.includes(marker), marker + ' ships with the server app');
  assert.ok(serverCss.indexOf('.rd-page{') < serverCss.indexOf('--ink:#17243a'));
  // The server pages load on their own.
  for (const page of ['ApiEvidence.jsx', 'ApiReadiness.jsx']) assert.ok(holding(page).isDynamicEntry && !startup.has(holding(page).fileName), page);

  // The walkthrough: one chunk loaded only for a static host, with its pages after it.
  const main = holding('main.jsx');
  assert.ok(main.isDynamicEntry && !startup.has(main.fileName));
  for (const name of new Set(routes.map(route => route.module))) {
    const chunk = holding(name + '.jsx');
    assert.ok(chunk, name + ' has an emitted chunk');
    assert.notEqual(chunk.fileName, main.fileName);
    assert.ok(chunk.isDynamicEntry, name + ' is dynamically imported');
  }
  for (const chunk of chunks) assert.ok(Buffer.byteLength(chunk.code) < 500_000, chunk.fileName + ' exceeds 500 kB');
  // The palette ships with each shell, so the first keystrokes reach its input. Dialog content loads on its own.
  assert.ok(sources(entry).includes('CommandPalette.jsx'));
  const dialog = holding('WorkflowDialogContent.jsx');
  assert.ok(dialog.isDynamicEntry);
  assert.notEqual(dialog.fileName, main.fileName);
  // Animation code loads with the pages that use it, never at start-up of either app.
  const walkthroughStartup = new Set([...startup, ...graph(main)]);
  const animated = chunks.filter(chunk => Object.keys(chunk.modules).some(id => /\/node_modules\/(motion|motion-dom|motion-utils|framer-motion)\//.test(id)));
  assert.ok(animated.length > 0);
  for (const chunk of animated) assert.ok(!walkthroughStartup.has(chunk.fileName), chunk.fileName + ' loads animation code at startup');
  // All of the walkthrough's styles ship with its shell, page styles before shared ones,
  // so the cascade is the same whichever page loads first.
  assert.equal(main.viteMetadata.importedCss.size, 1);
  for (const chunk of chunks) if (chunk !== main && chunk !== entry) assert.equal(chunk.viteMetadata.importedCss.size, 0, chunk.fileName + ' brings its own CSS');
  const cssText = String(asset([...main.viteMetadata.importedCss][0]).source), at = marker => {const index = cssText.indexOf(marker); assert.ok(index >= 0, marker); return index;};
  const shared = at('--ink:#17243a');
  for (const marker of ['.wl-page{', '.wl-step-entry{', '.evidence-flow{', '.planes-segmented{', '.ops-page{', '.pb-page{', '.rd-page{', '.hs-page{', '.reviewer-decision-form']) assert.ok(at(marker) < shared, marker + ' comes after the shared styles');
  assert.ok(at(':root{--chrome-focus') > shared);
  // Equal-weight rules: the shared dark primary button comes last and wins on Operations.
  const operationsButton = cssText.search(/\.ops-page \.button\.primary\{/), sharedButton = cssText.search(/\.main-content \.button\.primary\{[^}]*linear-gradient\(#355be3/);
  assert.ok(operationsButton >= 0 && sharedButton > operationsButton);
  // The server app's styles load first on a static host too. Each of them comes again in
  // the walkthrough's own sheet, in its place, so the later copy decides the cascade.
  for (const rule of serverCss.split('}').filter(rule => rule.trim())) assert.ok(cssText.includes(rule + '}'), 'the walkthrough’s sheet repeats ' + rule.slice(0, 60));
  assert.doesNotMatch(readFileSync(new URL('../vite.config.js', import.meta.url), 'utf8'), /chunkSizeWarningLimit/);
});

