import assert from 'node:assert/strict';
import {readdirSync, readFileSync} from 'node:fs';
import {PassThrough} from 'node:stream';
import {after, before, test} from 'node:test';
import {fileURLToPath} from 'node:url';
import {createElement, lazy, type ReactElement, Suspense} from 'react';
import {renderToPipeableStream, renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {build, createServer, type ViteDevServer} from 'vite';
import {serverPaths} from './routes.ts';

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the modules are loaded through Vite at run time.
let frame: any, reads: any, routeContent: any, pages: any, lazyPages: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  frame = await server.ssrLoadModule('/src/app/frame/Frame.tsx');
  reads = await server.ssrLoadModule('/src/api/reads.ts');
  routeContent = await server.ssrLoadModule('/src/app/frame/RouteContent.tsx');
  pages = await server.ssrLoadModule('/src/app/lazyPages.ts');
  lazyPages = await server.ssrLoadModule('/src/app/lazyPage.ts');
});
after(async () => {
  await server?.close();
});

// A signed-in store, as the frame reads it.
const principal = {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const signedIn = {
  mode: 'api',
  session: {signed_in: true, principal, csrf_token: 't', expires_at: '2099-01-15T23:00:00Z'},
  error: null,
  ended: false,
  generation: 1,
  elsewhere: null,
};
const store = {api: {}, subscribe: () => () => {}, getSnapshot: () => signedIn};
const framed = (path: string) =>
  createElement(reads.ServerContext, {value: store}, createElement(MemoryRouter, {initialEntries: [path]}, createElement(frame.Frame)));

function stream(element: ReactElement) {
  const chunks: string[] = [];
  const output = new PassThrough();
  let shellReady: () => void = () => {};
  let fail: (error: unknown) => void = () => {};
  const firstChunk = new Promise(resolve => output.once('data', resolve));
  const shell = new Promise<void>(resolve => {
    shellReady = resolve;
  });
  const done = new Promise<string>((resolve, reject) => {
    fail = reject;
    output.on('data', chunk => chunks.push(chunk.toString()));
    output.on('end', () => resolve(chunks.join('')));
    output.on('error', reject);
  });
  const renderer = renderToPipeableStream(element, {
    onShellReady() {
      renderer.pipe(output);
      shellReady();
    },
    onShellError: error => fail(error),
    onError: error => fail(error),
  });
  return {done, shell, firstChunk, html: () => chunks.join(''), abort: () => renderer.abort()};
}

const routes = [
  {path: serverPaths.coverage, module: 'Coverage', marker: 'cv', subnav: true},
  {path: serverPaths.deals, module: 'Deals', marker: 'deals-list', subnav: false},
  {path: serverPaths.activity, module: 'Activity', marker: 'ac', subnav: true},
] as const;

for (const route of routes) {
  test(`a direct lazy route resolves its page and keeps shared chrome: ${route.path}`, async () => {
    const html = await stream(framed(route.path)).done;
    assert.match(html, new RegExp(`class="[^"]*${route.marker}`));
    assert.match(html, /class="fr-bar"/);
    // Ops lists its pages under the top bar; Deals has no sub-navigation.
    if (route.subnav) {
      assert.match(html, /class="fr-pages"/);
    } else {
      assert.doesNotMatch(html, /class="fr-pages"/);
    }
    assert.match(html, /aria-label="Dark theme" aria-pressed="false"/);
    assert.doesNotMatch(html, /Page unavailable/);
    const module = await pages.pageLoaders[route.module]();
    assert.equal(typeof module.default, 'function');
  });
}

test('a pending import exposes a live loading state, then resolves the page', async () => {
  let release: () => void = () => {};
  const gate = new Promise<void>(resolve => {
    release = resolve;
  });
  const Deferred = lazy(() => gate.then(() => ({default: () => createElement('p', {className: 'loaded-page'}, 'Loaded page')})));
  const pending = stream(
    createElement(
      MemoryRouter,
      {initialEntries: [serverPaths.activity]},
      createElement('main', null, createElement(routeContent.default, {path: serverPaths.activity, label: 'Activity'}, createElement(Deferred))),
    ),
  );
  try {
    await pending.shell;
    await pending.firstChunk;
    assert.match(pending.html(), /role="status" aria-live="polite" aria-atomic="true"/);
    assert.match(pending.html(), /Loading (<!-- -->)?Activity(<!-- -->)?…/);
    assert.doesNotMatch(pending.html(), /class="loaded-page"/);
    release();
    assert.match(await pending.done, /class="loaded-page"/);
  } finally {
    release();
    pending.done.catch(() => {});
    pending.abort();
  }
});

function attempt(element: ReactElement): Promise<{html: string; errors: string[]}> {
  return new Promise((resolve, reject) => {
    const errors: string[] = [];
    const output = new PassThrough();
    let html = '';
    output.on('data', chunk => {
      html += chunk;
    });
    output.on('end', () => resolve({html, errors}));
    const renderer = renderToPipeableStream(element, {
      onAllReady() {
        renderer.pipe(output);
      },
      onShellError: reject,
      onError(error) {
        errors.push((error as Error).message);
      },
    });
  });
}

test('a failed page import is tried again once its message has shown, not remembered for the session', async () => {
  let calls = 0;
  let fail = true;
  const Page = lazyPages.lazyPage(async () => {
    calls++;
    if (fail) {
      throw new Error('Failed to fetch a page module');
    }
    return {default: () => createElement('p', null, 'Loaded page')};
  });
  const page = () => createElement(Suspense, {fallback: 'Loading page'}, createElement(Page));
  let result = await attempt(page());
  assert.deepEqual(result.errors, ['Failed to fetch a page module']);
  assert.doesNotMatch(result.html, /Loaded page/);
  fail = false;
  // Until the failure has reached its message, it stands rather than looping on new requests.
  result = await attempt(page());
  assert.deepEqual(result.errors, ['Failed to fetch a page module']);
  assert.equal(calls, 1);
  lazyPages.forgetFailedPages();
  result = await attempt(page());
  assert.deepEqual(result.errors, []);
  assert.match(result.html, /<p>Loaded page<\/p>/);
  assert.equal(calls, 2);
});

test('a preloaded page renders in the same pass, without a loading state', async () => {
  let calls = 0;
  const Page = lazyPages.lazyPage(async () => {
    calls++;
    return {default: () => createElement('p', null, 'Page content')};
  });
  await Page.preload();
  const html = renderToStaticMarkup(createElement(Suspense, {fallback: 'Loading page'}, createElement(Page)));
  assert.equal(html, '<p>Page content</p>');
  assert.equal(calls, 1);
});

test('the frame can render without browser globals while the page is still loading', () => {
  assert.equal(typeof window, 'undefined');
  const html = renderToStaticMarkup(framed(serverPaths.activity));
  assert.match(html, /id="main" class="fr-main" tabindex="-1"/);
  assert.match(html, /^<div class="fr"><a class="fr-skip" href="#main">Skip to content<\/a>/);
});

test('no file under src is outside api, ui, app and help, except the entry', () => {
  const top = readdirSync(new URL('../', import.meta.url), {withFileTypes: true});
  assert.deepEqual(top.map(entry => `${entry.name}${entry.isDirectory() ? '/' : ''}`).sort(), ['api/', 'app/', 'entry.tsx', 'help/', 'ui/']);
});

// What the server's app downloads to start, JavaScript and CSS, before it had
// its own shell (main 7330759).
const startupBytesBefore = 737_028;
// The built stylesheet stays under this many bytes.
const cssBytesAtMost = 60_000;
// The modules the server's app starts with.
const serverStartup = [
  'entry.tsx',
  'app/boot.tsx',
  'app/ServerApp.tsx',
  'app/routes.ts',
  'app/lazyPage.ts',
  'app/lazyPages.ts',
  'app/frame/Frame.tsx',
  'app/frame/SignIn.tsx',
  'app/frame/NoPage.tsx',
  'app/frame/pages.ts',
  'app/frame/frame.css',
  'app/frame/RouteContent.tsx',
  'app/frame/focus.ts',
  'app/deals/deals.ts',
  'app/deals/deals.css',
  'app/deals/card/card.css',
  'app/deals/card/text.css',
  'app/deals/card/communications.css',
  'app/deals/card/lines.css',
  'app/deals/card/proof.css',
  'app/ops/coverage.css',
  'app/ops/activity.css',
  'api/client.ts',
  'api/session.ts',
  'api/reads.ts',
  'ui/Button.tsx',
  'ui/Field.tsx',
  'ui/Refusal.tsx',
  'ui/Placeholder.tsx',
  'ui/Dialog.tsx',
  'ui/Palette.tsx',
  'ui/theme.ts',
  'ui/base.css',
  'ui/tokens.css',
  'ui/help.css',
  'ui/button.css',
  'ui/field.css',
  'ui/refusal.css',
  'ui/placeholder.css',
  'ui/dialog.css',
  'ui/palette.css',
];
// The modules the server's pages load after it.
const serverPages = [
  'app/deals/Deals.tsx',
  'app/ops/Coverage.tsx',
  'app/ops/coverageRows.ts',
  'app/ops/pooledReads.ts',
  'app/ops/Activity.tsx',
  'app/ops/activity.ts',
  'api/selection.ts',
  'api/sealed.ts',
  ...[
    'DealCard.tsx',
    'Title.tsx',
    'sections.ts',
    'card.ts',
    'Cite.tsx',
    'Sentence.tsx',
    'Notices.tsx',
    'PeopleSaid.tsx',
    'MoreRead.tsx',
    'TheRecord.tsx',
    'NotRead.tsx',
    'Seal.tsx',
    'DealTabs.tsx',
    'PlainText.tsx',
    'Communications.tsx',
    'communications.ts',
    'SealFailed.tsx',
    'sealCheck.ts',
    'Proof.tsx',
    'proof.ts',
    'Focal.tsx',
    'Flags.tsx',
    'lines.ts',
  ].map(name => `app/deals/card/${name}`),
  'ui/Help.tsx',
  'help/help.json',
];

interface Chunk {
  type: 'chunk';
  fileName: string;
  isEntry: boolean;
  isDynamicEntry: boolean;
  code: string;
  imports: string[];
  modules: Record<string, unknown>;
  viteMetadata: {importedCss: Set<string>};
}

test('production chunks: the server app starts with its shell alone, each page loads on its own, and the stylesheet is small', async t => {
  const previous = process.env.NODE_ENV;
  process.env.NODE_ENV = 'production';
  let result: {output: ({type: 'asset'; fileName: string; source: string | Uint8Array} | Chunk)[]};
  try {
    result = (await build({
      configFile: fileURLToPath(new URL('../../vite.config.js', import.meta.url)),
      logLevel: 'silent',
      build: {write: false},
    })) as typeof result;
  } finally {
    if (previous === undefined) {
      delete process.env.NODE_ENV;
    } else {
      process.env.NODE_ENV = previous;
    }
  }
  const chunks = result.output.filter((asset): asset is Chunk => asset.type === 'chunk');
  const named = (name: string) => chunks.find(chunk => chunk.fileName === name) as Chunk;
  const sources = (chunk: Chunk) =>
    Object.keys(chunk.modules)
      .filter(id => id.includes('/web/src/'))
      .map(id => id.slice(id.indexOf('/web/src/') + 9).replace(/\?.*$/, ''));
  const holding = (path: string) => chunks.find(chunk => sources(chunk).includes(path)) as Chunk;
  const graph = (from: Chunk) => {
    const seen = new Set<string>();
    const visit = (name: string) => {
      if (seen.has(name)) {
        return;
      }
      seen.add(name);
      named(name).imports.forEach(visit);
    };
    visit(from.fileName);
    return seen;
  };
  const entry = chunks.find(chunk => chunk.isEntry) as Chunk;
  assert.ok(sources(entry).includes('entry.tsx'));
  assert.ok(Object.keys(entry.modules).some(id => id.includes('/react-router/')));

  // The build holds the server app's modules and nothing else of the source.
  for (const chunk of chunks) {
    for (const path of sources(chunk)) {
      assert.ok(serverStartup.includes(path) || serverPages.includes(path), `${path} is in the build`);
    }
  }

  // Only the shell's modules and styles at start-up, and fewer bytes than before it had its own shell.
  const startup = graph(entry);
  for (const path of [...startup].flatMap(name => sources(named(name)))) {
    assert.ok(serverStartup.includes(path), `${path} is in the server app’s start-up graph`);
  }
  const css = [...startup].flatMap(name => [...named(name).viteMetadata.importedCss]);
  assert.equal(css.length, 1);
  for (const chunk of chunks) {
    if (chunk !== entry) {
      assert.equal(chunk.viteMetadata.importedCss.size, 0, `${chunk.fileName} brings its own CSS`);
    }
  }
  const asset = (name: string) => result.output.find(item => item.fileName === name) as {source: string | Uint8Array};
  const sheet = Buffer.byteLength(asset(css[0] as string).source);
  const bytes = [...startup].reduce((sum, name) => sum + Buffer.byteLength(named(name).code), 0) + sheet;
  t.diagnostic(`server app start-up: ${bytes} bytes of JavaScript and CSS, ${startupBytesBefore} before; the stylesheet is ${sheet} bytes`);
  assert.ok(bytes <= startupBytesBefore - 120_000, `${bytes} bytes at start-up`);
  assert.ok(sheet < cssBytesAtMost, `the stylesheet is ${sheet} bytes`);
  // The base rules come first, so every component and page overrides them.
  const serverCss = String(asset(css[0] as string).source);
  assert.ok(serverCss.indexOf('box-sizing:border-box') < serverCss.indexOf('.fr{'));
  // The server pages load on their own.
  for (const page of ['app/deals/Deals.tsx', 'app/ops/Coverage.tsx', 'app/ops/Activity.tsx']) {
    assert.ok(holding(page).isDynamicEntry && !startup.has(holding(page).fileName), page);
  }
  for (const chunk of chunks) {
    assert.ok(Buffer.byteLength(chunk.code) < 500_000, `${chunk.fileName} exceeds 500 kB`);
  }
  // The palette ships with the shell, so the first keystrokes reach its input.
  assert.ok(sources(entry).includes('ui/Palette.tsx'));
  assert.doesNotMatch(readFileSync(new URL('../../vite.config.js', import.meta.url), 'utf8'), /chunkSizeWarningLimit/);
});
