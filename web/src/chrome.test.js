import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createServer} from 'vite';
import {renewalFixture, initialState} from './renewalFixtures.js';
import {buildWorklist} from './worklist.js';
import {ChromeHooks, byClass, find, named, nodes, keyboard, element, chromeDOM} from './testing/chromeHarness.js';

let server, App, Dialog, CommandPalette, pages;
const hookNames = ['useState', 'useReducer', 'useRef', 'useMemo', 'useId', 'useEffect', 'useLayoutEffect'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [{
    name: 'chrome-handlers', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:chrome-')) return '\0' + id;},
    load(id) {
      if (id === '\0virtual:chrome-react') return 'import React from \'react\'; export default React; ' + hookNames.map(name => 'export const ' + name + '=(...args)=>globalThis.__chromeHooks.' + name + '(...args);').join('\n');
      if (id === '\0virtual:chrome-router') return 'export * from \'react-router\'; ' + ['useNavigate', 'useLocation'].map(name => 'export const ' + name + '=(...args)=>globalThis.__chromeHooks.' + name + '(...args);').join('\n');
      if (id === '\0virtual:chrome-motion') return 'export const motion={section:\'section\'}; export const useReducedMotion=()=>false;';
    },
    transform(code, id) {
      if (['main', 'WorkspaceDialog', 'CommandPalette'].some(name => id.endsWith('/src/' + name + '.jsx'))) {
        code = code.replace("from 'react';", "from 'virtual:chrome-react';").replace("from 'react-router';", "from 'virtual:chrome-router';");
        if (id.endsWith('/WorkspaceDialog.jsx')) code = code.replace("from 'motion/react';", "from 'virtual:chrome-motion';");
        return code;
      }
    },
  }]});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  Dialog = (await server.ssrLoadModule('/src/WorkspaceDialog.jsx')).default;
  CommandPalette = (await server.ssrLoadModule('/src/CommandPalette.jsx')).default;
  pages = await server.ssrLoadModule('/src/lazyPages.js');
});
after(async () => {await server?.close();});
const globals = ['window', 'document', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame', 'getComputedStyle', '__chromeHooks'];
let originals, media, dom, harnesses;
beforeEach(() => {
  originals = Object.fromEntries(globals.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  media = Object.assign(new EventTarget(), {matches: true});
  dom = chromeDOM(media); harnesses = [];
});
afterEach(() => {
  harnesses.forEach(hooks => hooks.dispose());
  for (const name of globals) {if (originals[name]) Object.defineProperty(globalThis, name, originals[name]); else delete globalThis[name];}
});
function hooks(path) {const instance = new ChromeHooks(path); harnesses.push(instance); return instance;}
function bench(path = '/renewals') {
  const instance = hooks(path);
  return {hooks: instance, render() {const tree = instance.render(App); dom.attach(tree, instance.useLocation().pathname); instance.flush(); return tree;}};
}
function openNavigation(page) {let tree = page.render(); dom.get('menu').focus(); byClass(tree, 'mobile-menu').props.onClick(); return page.render();}
function key(target, value, options) {const event = keyboard(value, options); target.dispatchEvent(event); return event;}

test('mobile navigation opens as a modal, moves focus in, traps both Tab directions and returns on Escape', () => {
  const page = bench(); let tree = page.render();
  assert.equal(find(tree, node => node.props.id === 'workspace-navigation').props.inert, true);
  assert.equal(byClass(tree, 'mobile-menu').props['aria-expanded'], false);
  tree = openNavigation(page);
  const navigation = find(tree, node => node.props.id === 'workspace-navigation');
  assert.equal(navigation.props.role, 'dialog');
  assert.equal(navigation.props['aria-modal'], true);
  assert.equal(byClass(tree, 'mobile-menu').props['aria-expanded'], true);
  assert.equal(byClass(tree, 'main-shell').props.inert, true);
  assert.equal(document.body.style.overflow, 'hidden');
  assert.equal(document.activeElement, dom.get('close'));
  assert.equal(key(document, 'Tab', {shiftKey: true}).defaultPrevented, true);
  assert.equal(document.activeElement, dom.get('reset-link'));
  assert.equal(key(document, 'Tab').defaultPrevented, true);
  assert.equal(document.activeElement, dom.get('close'));
  dom.get('/queue').focus();
  assert.equal(key(document, 'Tab').defaultPrevented, false);
  document.activeElement = document.body;
  assert.equal(key(document, 'Tab').defaultPrevented, true);
  assert.equal(document.activeElement, dom.get('close'));
  assert.equal(key(document, 'Escape').defaultPrevented, true);
  tree = page.render();
  assert.equal(document.activeElement, dom.get('menu'));
  assert.equal(byClass(tree, 'main-shell').props.inert, false);
  assert.equal(byClass(tree, 'mobile-menu').props['aria-expanded'], false);
  assert.equal(document.body.style.overflow, '');
});

test('the close control and backdrop restore the opener without leaving a keyboard stop behind the modal', () => {
  for (const control of ['mobile-nav-close', 'navigation-backdrop']) {
    const page = bench(); let tree = openNavigation(page);
    assert.equal(byClass(tree, 'navigation-backdrop').props.tabIndex, -1);
    byClass(tree, control).props.onClick(); tree = page.render();
    assert.equal(document.activeElement, dom.get('menu'));
    assert.equal(find(tree, node => node.props.id === 'workspace-navigation').props['aria-hidden'], true);
    assert.equal(byClass(tree, 'navigation-backdrop'), undefined);
    page.hooks.dispose();
  }
});

test('crossing the mobile breakpoint closes the overlay and keeps focus on visible navigation', () => {
  const page = bench('/queue'); let tree = openNavigation(page);
  media.matches = false; media.dispatchEvent(new Event('change')); tree = page.render(); dom.flushFrames();
  assert.equal(find(tree, node => node.props.id === 'workspace-navigation').props.inert, false);
  assert.equal(find(tree, node => node.props.id === 'workspace-navigation').props.role, undefined);
  assert.equal(document.activeElement, dom.get('/queue'));
  assert.equal(document.body.style.overflow, '');
  media.matches = true; media.dispatchEvent(new Event('change')); tree = page.render(); dom.flushFrames();
  assert.equal(find(tree, node => node.props.id === 'workspace-navigation').props.inert, true);
  assert.equal(document.activeElement, dom.get('menu'));
});

test('closing navigation for a selected route does not steal the page selection focus', () => {
  const page = bench(); const tree = openNavigation(page);
  find(tree, node => node.props.to === '/queue').props.onClick();
  page.hooks.navigate('/queue?' + new URLSearchParams({selection: buildWorklist(initialState).workItems[0].selectionKey}));
  const selected = element('selected'); selected.focus();
  page.render(); dom.flushFrames();
  assert.equal(document.activeElement, selected);
});

test('theme is a stable named toggle with a persisted pressed state in both modes', () => {
  const page = bench(); let tree = page.render();
  const label = byClass(tree, 'theme-toggle').props['aria-label'];
  assert.ok(label);
  for (const [before, after] of [['dark', 'light'], ['light', 'dark']]) {
    assert.equal(byClass(tree, 'theme-toggle').props['aria-pressed'], before === 'dark');
    byClass(tree, 'theme-toggle').props.onClick(); tree = page.render();
    assert.equal(byClass(tree, 'theme-toggle').props['aria-label'], label);
    assert.equal(byClass(tree, 'theme-toggle').props['aria-pressed'], after === 'dark');
    assert.equal(document.documentElement.dataset.theme, after);
    assert.equal(dom.storage.get('gtm-workflow-theme'), after);
  }
  dom.storage.set('gtm-workflow-theme', 'light');
  const restored = bench('/operations').render();
  assert.equal(byClass(restored, 'theme-toggle').props['aria-pressed'], false);
  assert.equal(document.documentElement.dataset.theme, 'light');
});

test('theme changes remain usable when browser storage is unavailable', () => {
  localStorage.getItem = () => {throw new Error('Unavailable');};
  localStorage.setItem = () => {throw new Error('Unavailable');};
  const page = bench(); let tree = page.render();
  byClass(tree, 'theme-toggle').props.onClick(); tree = page.render();
  assert.equal(byClass(tree, 'theme-toggle').props['aria-pressed'], false);
  assert.equal(document.documentElement.dataset.theme, 'light');
});

test('the skip link focuses the main landmark without replacing worklist URL state', () => {
  const path = '/queue?scope=all&sort=changed', page = bench(path), tree = page.render();
  const skip = byClass(tree, 'skip'), main = find(tree, node => node.props.id === 'main');
  assert.equal(skip.props.href, '#' + main.props.id);
  assert.equal(main.props.tabIndex, -1);
  const event = {preventDefault() {this.prevented = true;}};
  skip.props.onClick(event);
  assert.equal(event.prevented, true);
  assert.equal(document.activeElement, dom.get('main'));
  assert.equal(dom.get('main').calls.at(-1)[0], 'scroll');
  assert.equal(page.hooks.entries[page.hooks.index], path);
  const opened = openNavigation(page);
  assert.equal(byClass(opened, 'skip').props.inert, true);
});

test('the search opener and both platform shortcuts open the same dialog and remember a usable opener', () => {
  for (const modifier of ['click', 'ctrlKey', 'metaKey']) {
    const page = bench(); let tree = page.render(); dom.get('search').focus();
    const trigger = byClass(tree, 'search-trigger');
    assert.equal(trigger.props['aria-haspopup'], 'dialog');
    assert.equal(trigger.props['aria-expanded'], false);
    if (modifier === 'click') trigger.props.onClick();
    else assert.equal(key(window, 'k', {[modifier]: true}).defaultPrevented, true);
    tree = page.render();
    const dialog = named(tree, 'WorkspaceDialog');
    assert.equal(dialog.props.command, true);
    assert.equal(dialog.props.returnFocusRef.current, dom.get('search'));
    assert.equal(byClass(tree, 'search-trigger').props['aria-expanded'], true);
    assert.equal(byClass(tree, 'main-shell').props.inert, true);
    assert.ok(find(tree, node => node.type === pages.CommandPalette));
    dialog.props.onClose(); page.render(); page.hooks.dispose();
  }
});

test('a command dialog opened from navigation returns to the menu rather than a now hidden link', () => {
  const page = bench(); openNavigation(page);
  key(window, 'k', {ctrlKey: true}); const tree = page.render();
  assert.equal(byClass(tree, 'mobile-menu').props['aria-expanded'], false);
  assert.equal(named(tree, 'WorkspaceDialog').props.returnFocusRef.current, dom.get('menu'));
});

test('a loading command dialog takes focus, contains Tab and returns focus on Escape', () => {
  const instance = hooks(), opener = dom.get('search'), dialog = dom.get('dialog'), close = dom.get('dialog-close');
  let closed = false;
  const tree = instance.render(Dialog, {title: 'Search', command: true, returnFocusRef: {current: opener}, onClose() {closed = true;}});
  const section = find(tree, node => node.props.role === 'dialog');
  section.props.ref.current = dialog;
  dialog.querySelector = () => null; dialog.querySelectorAll = () => [close]; dom.dialog = dialog;
  instance.flush();
  assert.equal(document.activeElement, dialog);
  assert.equal(key(document, 'Tab').defaultPrevented, true);
  assert.equal(document.activeElement, close);
  assert.equal(key(document, 'Escape').defaultPrevented, true);
  assert.equal(closed, true);
  dom.dialog = null; dialog.isConnected = false; close.isConnected = false;
  instance.dispose(); dom.flushFrames();
  assert.equal(document.activeElement, opener);
});

test('command search filters real destinations and Enter navigates the selected result', () => {
  const page = bench(); let tree = page.render(); byClass(tree, 'search-trigger').props.onClick(); tree = page.render();
  const props = find(tree, node => node.type === pages.CommandPalette).props;
  const instance = hooks(); let palette = instance.render(CommandPalette, props);
  const input = find(palette, node => node.props.role === 'combobox');
  assert.equal(input.props.autoFocus, true);
  input.props.onChange({target: {value: 'Work queue'}});
  palette = instance.render(CommandPalette, props);
  assert.equal([...nodes(palette)].filter(node => node.props.role === 'option').length, 1);
  const event = {key: 'Enter', preventDefault() {this.prevented = true;}};
  find(palette, node => node.props.role === 'combobox').props.onKeyDown(event);
  assert.equal(event.prevented, true);
  assert.equal(page.hooks.useLocation().pathname, '/queue');
  assert.equal(named(page.render(), 'WorkspaceDialog'), undefined);
});

test('an exact Work plan destination waits for its lazy content before consuming the focus target', () => {
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  const path = renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id});
  const page = bench(path); page.render(); let tree = page.render(); dom.flushFrames();
  assert.equal(document.activeElement, null);
  const target = element('work-item'); dom.targets.set(item.entryTargetId, target);
  find(tree, node => node.type?.name === 'RouteContent' && node.props.path === renewalFixture.paths.workplan).props.onReady(renewalFixture.paths.workplan);
  page.render(); dom.flushFrames();
  assert.equal(document.activeElement, target);
  assert.equal(target.calls[0][0], 'scroll');
  assert.equal(target.calls[1][0], 'focus');
  const calls = target.calls.length; page.render(); dom.flushFrames();
  assert.equal(target.calls.length, calls);
});

test('header jumps requested during loading focus the matching section after the page is ready', () => {
  for (const [path, id] of [[renewalFixture.paths.root + '/readiness', 'readiness-coverage'], [renewalFixture.paths.history, 'history-guide']]) {
    const page = bench(path); let tree = page.render();
    find(byClass(tree, 'heading-actions'), node => node.type === 'button').props.onClick();
    const target = element(id); dom.targets.set(id, target);
    find(tree, node => node.type?.name === 'RouteContent' && node.props.path === path).props.onReady(path);
    page.render();
    assert.equal(document.activeElement, target);
    assert.equal(target.calls.at(-1)[1].behavior, 'instant');
    page.hooks.dispose();
  }
});

test('leaving a pending Work plan clears its old focus request rather than blocking later page focus', () => {
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  const page = bench(renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id}));
  page.render(); page.render(); dom.flushFrames();
  page.hooks.navigate('/operations'); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, dom.mainHeading);
  const stale = element('old-target'); dom.targets.set(item.entryTargetId, stale);
  page.hooks.navigate(renewalFixture.paths.workplan); const tree = page.render();
  find(tree, node => node.type?.name === 'RouteContent' && node.props.path === renewalFixture.paths.workplan).props.onReady(renewalFixture.paths.workplan);
  page.render(); dom.flushFrames();
  assert.equal(stale.calls.length, 0);
  assert.equal(document.activeElement, dom.mainHeading);
});

test('a pending exact destination never takes focus behind an open dialog', () => {
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  const page = bench(renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id}));
  page.render(); let tree = page.render(); dom.flushFrames();
  byClass(tree, 'search-trigger').props.onClick(); tree = page.render();
  const target = element('work-item'); dom.targets.set(item.entryTargetId, target);
  find(tree, node => node.type?.name === 'RouteContent' && node.props.path === renewalFixture.paths.workplan).props.onReady(renewalFixture.paths.workplan);
  tree = page.render(); dom.flushFrames();
  assert.equal(target.calls.length, 0);
  named(tree, 'WorkspaceDialog').props.onClose(); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, target);
});

const css = readFileSync(new URL('./chrome.css', import.meta.url), 'utf8');
const cssRules = [...css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(match => ({selector: match[1].trim(), declarations: Object.fromEntries(match[2].split(';').filter(part => part.includes(':')).map(part => {const index = part.indexOf(':'); return [part.slice(0, index).trim(), part.slice(index + 1).trim()];}))}));
const rule = part => cssRules.find(rule => rule.selector.includes(part))?.declarations;

test('one shared CSS contract gives every chrome control a 44px target and 16px control text', () => {
  const targets = rule(':is(.sidebar,.topbar,.page-heading,.detail-nav) :is(a,button),.skip');
  assert.equal(targets['min-height'], '44px'); assert.equal(targets['min-width'], '44px');
  const header = rule('.page-heading .context-link,.page-heading .heading-actions .text-button');
  assert.equal(header['min-height'], '44px'); assert.equal(header['min-width'], '44px');
  const text = rule('.sidebar .nav-item,.sidebar .reset-link');
  assert.equal(text['font-size'], '1rem'); assert.equal(text['line-height'], '1.5');
  for (const selector of ['.topbar :is(.mobile-menu,.theme-toggle,.profile)', '.sidebar .mobile-nav-close']) {
    assert.equal(rule(selector).width, '44px'); assert.equal(rule(selector).height, '44px');
  }
  assert.equal(cssRules.find(rule => rule.selector === '.detail-nav nav a').declarations['min-height'], '44px');
  const legacy = ['styles', 'refinements', 'modern', 'atelier'].map(name => readFileSync(new URL('./' + name + '.css', import.meta.url), 'utf8')).join('\n');
  assert.doesNotMatch(legacy, /\.mobile-menu\s*\{[^}]*!important/);
});

test('page styles cannot resize the shared shell, renewal heading or tabs after a lazy import', () => {
  for (const name of ['worklists', 'readiness', 'history', 'playbooks', 'operations', 'evidence-flow']) {
    const source = readFileSync(new URL('./' + name + '.css', import.meta.url), 'utf8');
    assert.doesNotMatch(source, /\.(sidebar|topbar|main-content|main-shell|page-heading|detail-nav|nav-item|brand)(?=[\s.:>{,#])/);
    assert.doesNotMatch(source, /\.(wl|rd|hs)-return-context/);
  }
  assert.ok(css.includes('.wl-return-context,.rd-return-context,.hs-return-context'));
  assert.equal(rule('.main-shell,.main-content')['min-width'], '0');
  assert.equal(cssRules.find(rule => rule.selector === '.detail-nav').declarations['flex-wrap'], 'wrap');
});

test('focus outlines use visible theme tokens and reduced motion covers the navigation overlay', () => {
  assert.equal(rule(':root')['--chrome-focus'], '#315ad9');
  assert.equal(rule("html[data-theme='dark']")['--chrome-focus'], '#a9c1ff');
  assert.equal(rule('.sidebar')['--chrome-focus'], '#a9c1ff');
  assert.equal(rule(':focus-visible').outline, '3px solid var(--chrome-focus)');
  assert.equal(rule(':focus-visible')['outline-offset'], '3px');
  const reduced = css.slice(css.indexOf('@media(prefers-reduced-motion:reduce)'));
  for (const selector of ['.sidebar', '.topbar', '.page-heading', '.detail-nav']) assert.ok(reduced.includes(selector));
  assert.ok(reduced.includes('transition:none!important'));
  assert.ok(reduced.includes('animation:none!important'));
});
