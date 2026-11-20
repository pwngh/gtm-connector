import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createServer} from 'vite';
import {renewalFixture, initialState} from './renewalFixtures.js';
import {buildWorklist} from './worklist.js';
import {ChromeHooks, byClass, find, named, nodes, keyboard, element, chromeDOM} from './testing/chromeHarness.js';

let server, App, Dialog, CommandPalette, RouteLoading, RouteError, pages;
const hookNames = ['useState', 'useReducer', 'useRef', 'useMemo', 'useId', 'useEffect', 'useLayoutEffect'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [{
    name: 'chrome-handlers', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:chrome-')) return '\0' + id;},
    load(id) {
      if (id === '\0virtual:chrome-react') return 'export * from \'react\'; import React from \'react\'; export default React; ' + hookNames.map(name => 'export const ' + name + '=(...args)=>globalThis.__chromeHooks.' + name + '(...args);').join('\n');
      if (id === '\0virtual:chrome-router') return 'export * from \'react-router\'; ' + ['useNavigate', 'useLocation'].map(name => 'export const ' + name + '=(...args)=>globalThis.__chromeHooks.' + name + '(...args);').join('\n');
      if (id === '\0virtual:chrome-motion') return 'export const motion={section:\'section\'}; export const useReducedMotion=()=>false;';
    },
    transform(code, id) {
      if (['main', 'WorkspaceDialog', 'CommandPalette', 'RouteContent'].some(name => id.endsWith('/src/' + name + '.jsx'))) {
        code = code.replace("from 'react';", "from 'virtual:chrome-react';").replace("from 'react-router';", "from 'virtual:chrome-router';");
        if (id.endsWith('/WorkspaceDialog.jsx')) code = code.replace("from 'motion/react';", "from 'virtual:chrome-motion';");
        return code;
      }
    },
  }]});
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
  Dialog = (await server.ssrLoadModule('/src/WorkspaceDialog.jsx')).default;
  CommandPalette = (await server.ssrLoadModule('/src/CommandPalette.jsx')).default;
  ({RouteLoading, RouteError} = await server.ssrLoadModule('/src/RouteContent.jsx'));
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
    const toggle = byClass(tree, 'theme-toggle');
    assert.equal(toggle.props['aria-pressed'], before === 'dark');
    assert.equal(toggle.props.title, label);
    assert.equal(toggle.props.children.type.render.displayName, 'Moon');
    assert.equal(toggle.props.children.props.fill, before === 'dark' ? 'currentColor' : 'none');
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
  assert.deepEqual(dom.get('main').calls.at(-1), ['scroll', {block: 'start', behavior: 'instant'}]);
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
    // The palette ships with the shell, so its input exists in the same render.
    assert.ok(find(tree, node => node.type === CommandPalette));
    assert.equal(find(tree, node => node.type?.name === 'RouteContent' && node.props.path === 'search'), undefined);
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
  const props = find(tree, node => node.type === CommandPalette).props;
  const instance = hooks(); let palette = instance.render(CommandPalette, props);
  const input = find(palette, node => node.props.role === 'combobox');
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

const pageDone = (tree, path) => find(tree, node => node.type?.name === 'RouteContent' && node.props.path === path).props.onReady(path);
const dialogContent = tree => find(tree, node => node.props.modal && node.props.openStep);
function stepTarget(step) {const button = element('step-' + step + '-button'); dom.targets.set('step-' + step, {querySelector: () => button}); return button;}

test('a Work plan target that arrives late takes focus only if focus has not moved elsewhere', () => {
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  for (const moved of [false, true]) {
    dom.targets.clear();
    const page = bench(renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id}));
    page.render(); let tree = page.render(); dom.flushFrames();
    // The link that asked is gone, so the heading holds focus while the page loads.
    dom.mainHeading.focus();
    if (moved) dom.get('search').focus();
    const target = element('work-item'); dom.targets.set(item.entryTargetId, target);
    pageDone(tree, renewalFixture.paths.workplan); tree = page.render(); dom.flushFrames();
    assert.equal(document.activeElement, moved ? dom.get('search') : target);
    // A dropped target stays dropped.
    page.render(); dom.flushFrames();
    assert.equal(target.calls.length, moved ? 0 : 2);
    page.hooks.dispose();
  }
});

test('a step asked for from a dialog takes focus when the dialog returns focus to its opener', () => {
  for (const moved of [false, true]) {
    dom.targets.clear();
    const page = bench(renewalFixture.paths.workplan); pageDone(page.render(), renewalFixture.paths.workplan); let tree = page.render();
    const opener = element('inspect-source'); opener.focus();
    byClass(tree, 'context-link').props.onClick(); tree = page.render();
    dialogContent(tree).props.openStep(3); tree = page.render();
    // The closing dialog puts focus back on its opener, unless the user has gone on.
    (moved ? dom.get('search') : opener).focus();
    const target = stepTarget(3); page.render(); dom.flushFrames();
    assert.equal(document.activeElement, moved ? dom.get('search') : target);
    page.hooks.dispose();
  }
});

test('a target waiting behind a dialog takes focus only if the dialog returns focus to a place that has not moved on', () => {
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  for (const moved of [false, true]) {
    dom.targets.clear();
    const page = bench(renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id}));
    page.render(); let tree = page.render(); dom.flushFrames();
    const opener = moved ? dom.get('search') : dom.mainHeading; opener.focus();
    byClass(tree, 'search-trigger').props.onClick(); tree = page.render();
    const target = element('work-item'); dom.targets.set(item.entryTargetId, target);
    pageDone(tree, renewalFixture.paths.workplan); tree = page.render(); dom.flushFrames();
    named(tree, 'WorkspaceDialog').props.onClose(); page.render();
    opener.focus(); dom.flushFrames();
    assert.equal(document.activeElement, moved ? opener : target);
    page.hooks.dispose();
  }
});

test('a late Work plan target stays dropped once focus has moved on, even after focus falls back to the body', () => {
  // From a Work queue link: the heading holds focus, the user moves to Search, then clicks the page background.
  const item = buildWorklist(initialState).workItems.find(row => row.kind === 'inspection');
  let page = bench(renewalFixture.paths.workplan + '?' + new URLSearchParams({workItem: item.id}));
  page.render(); let tree = page.render(); dom.flushFrames();
  dom.mainHeading.focus(); dom.get('search').focus(); document.activeElement = document.body;
  const target = element('work-item'); dom.targets.set(item.entryTargetId, target);
  pageDone(tree, renewalFixture.paths.workplan); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, document.body);
  assert.equal(target.calls.length, 0);
  page.hooks.dispose();
  // From Evidence: the opener is gone, the heading holds focus, then Search, then the background.
  dom.targets.clear();
  const evidence = renewalFixture.paths.evidence, workplan = renewalFixture.paths.workplan;
  page = bench(evidence); pageDone(page.render(), evidence); tree = page.render();
  const opener = element('inspect-source'); opener.focus();
  byClass(tree, 'context-link').props.onClick(); tree = page.render();
  dialogContent(tree).props.openStep(3);
  opener.isConnected = false; document.activeElement = document.body;
  tree = page.render(); dom.flushFrames();
  assert.equal(document.activeElement, dom.mainHeading);
  dom.get('search').focus(); document.activeElement = document.body;
  const step = stepTarget(3);
  pageDone(tree, workplan); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, document.body);
  assert.equal(step.calls.length, 0);
});

test('View affected task from Evidence focuses the step once the Work plan arrives', () => {
  const evidence = renewalFixture.paths.evidence, workplan = renewalFixture.paths.workplan;
  const page = bench(evidence); pageDone(page.render(), evidence); let tree = page.render();
  const opener = element('inspect-source'); opener.focus();
  byClass(tree, 'context-link').props.onClick(); tree = page.render();
  dialogContent(tree).props.openStep(3);
  // Leaving Evidence removes the opener; the heading holds focus while Work plan loads.
  opener.isConnected = false; document.activeElement = document.body;
  tree = page.render(); dom.flushFrames();
  assert.equal(page.hooks.useLocation().pathname, workplan);
  assert.equal(document.activeElement, dom.mainHeading);
  const target = stepTarget(3);
  pageDone(tree, workplan); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, target);
});

test('a link removed while its page loads leaves focus on the page heading until the page target takes it', () => {
  const readiness = renewalFixture.paths.root + '/readiness', history = renewalFixture.paths.history;
  const page = bench(readiness); pageDone(page.render(), readiness); page.render();
  const link = element('trace-in-history'); link.focus();
  page.hooks.navigate(history + '?' + new URLSearchParams({entry: 'history-1', view: 'recorded'}));
  link.isConnected = false;
  let tree = page.render();
  assert.equal(document.activeElement, dom.mainHeading);
  assert.equal(dom.mainHeading.tabIndex, -1);
  // History focuses its selected entry when it mounts. The shell leaves that focus alone.
  const entry = element('history-entry'); entry.focus();
  pageDone(tree, history); tree = page.render(); dom.flushFrames();
  assert.equal(document.activeElement, entry);
});

test('while a page loads, focus on a control that is still there is never moved', () => {
  const page = bench(renewalFixture.paths.workplan); page.render();
  page.hooks.navigate(renewalFixture.paths.history + '?' + new URLSearchParams({entry: 'history-1'}));
  const control = dom.get('search'); control.focus();
  page.render(); dom.flushFrames();
  assert.equal(document.activeElement, control);
});

test('the first load of a page does not move focus to its heading', () => {
  const page = bench(renewalFixture.paths.history + '?' + new URLSearchParams({entry: 'history-1'}));
  document.activeElement = document.body;
  page.render(); page.render(); dom.flushFrames();
  assert.equal(document.activeElement, document.body);
});

test('a header jump made during loading is dropped once focus has moved elsewhere', () => {
  const path = renewalFixture.paths.root + '/readiness';
  for (const moved of [false, true]) {
    const page = bench(path); const tree = page.render();
    const button = element('sources-and-gaps'); button.focus();
    find(byClass(tree, 'heading-actions'), node => node.type === 'button').props.onClick();
    if (moved) dom.get('search').focus();
    const target = element('readiness-coverage'); dom.targets.set('readiness-coverage', target);
    pageDone(tree, path); page.render();
    assert.equal(document.activeElement, moved ? dom.get('search') : target);
    page.hooks.dispose();
  }
});

test('a page or dialog that fails to load gives focus to its message only when focus has no better place', () => {
  const dialog = element('dialog');
  for (const [inDialog, start, takes] of [[false, 'heading', true], [false, 'body', true], [false, 'control', false], [true, 'dialog', true], [true, 'control', false]]) {
    const boundary = new RouteError({dialog: inDialog, children: null});
    const panel = element('panel'); panel.ownerDocument = document; panel.closest = selector => inDialog && selector === '[role="dialog"]' ? dialog : null;
    boundary.panel.current = panel;
    if (start === 'heading') dom.mainHeading.focus();
    else if (start === 'body') document.activeElement = document.body;
    else if (start === 'dialog') dialog.focus();
    else dom.get('search').focus();
    const before = document.activeElement;
    boundary.componentDidCatch(new Error('Failed to fetch a page module'));
    assert.equal(document.activeElement, takes ? panel : before, inDialog + ' ' + start);
  }
});

test('Try again clears the failure, keeps focus on a lasting spot and remounts the content', () => {
  for (const inDialog of [false, true]) {
    const dialog = element('dialog'), boundary = new RouteError({dialog: inDialog, children: 'page content'});
    boundary.setState = update => Object.assign(boundary.state, typeof update === 'function' ? update(boundary.state) : update);
    Object.assign(boundary.state, RouteError.getDerivedStateFromError(new Error('Failed to fetch a page module')));
    const view = boundary.render();
    assert.equal(view.props.tabIndex, -1);
    assert.equal(find(view, node => node.props.role === 'alert').props.children.join(''), `This ${inDialog ? 'dialog' : 'page'} could not load. Your saved walkthrough has not been reset.`);
    const panel = element('panel'); panel.closest = selector => inDialog && selector === '[role="dialog"]' ? dialog : null;
    boundary.panel.current = panel;
    const button = find(view, node => node.type === 'button');
    assert.equal(button.props.children, 'Try again');
    dom.get('retry').focus();
    button.props.onClick();
    assert.equal(document.activeElement, inDialog ? dialog : dom.mainHeading);
    assert.equal(boundary.state.failed, false);
    const content = boundary.render();
    assert.equal(content.key, '1');
    assert.equal(content.props.children, 'page content');
  }
});

test('the command palette input takes focus from the dialog itself, never from a control the user moved to', () => {
  const dialog = element('dialog'), close = element('dialog-close');
  for (const [start, expected] of [[dialog, 'input'], [document.body, 'input'], [close, close]]) {
    const instance = hooks(), tree = instance.render(CommandPalette, {items: []});
    const input = element('palette-input'); input.ownerDocument = document;
    input.closest = selector => selector === '[role="dialog"]' ? dialog : null;
    find(tree, node => node.props.role === 'combobox').props.ref.current = input;
    if (start === document.body) document.activeElement = document.body; else start.focus();
    instance.flush();
    assert.equal(document.activeElement, expected === 'input' ? input : expected);
    instance.dispose();
  }
});

test('the workflow dialog content preloads when the browser is idle', () => {
  const preload = pages.WorkflowDialogContent.preload; let calls = 0, idle = null;
  pages.WorkflowDialogContent.preload = () => {calls++; return Promise.resolve();};
  window.requestIdleCallback = callback => {idle = callback; return 7;};
  window.cancelIdleCallback = () => {};
  try {
    bench().render();
    assert.equal(calls, 0);
    idle();
    assert.equal(calls, 1);
  } finally {pages.WorkflowDialogContent.preload = preload;}
});

test('the loading state holds the rest of the viewport, so the footer does not jump, but not inside a dialog', () => {
  for (const inDialog of [false, true]) {
    const instance = hooks(), tree = instance.render(RouteLoading, {label: 'History'}), set = [];
    tree.props.ref.current = {closest: () => inDialog ? {} : null, getBoundingClientRect: () => ({top: 300}), style: {setProperty: (...args) => set.push(args)}};
    window.scrollY = 120; instance.flush();
    assert.deepEqual(set, inDialog ? [] : [['--route-top', '420px']]);
    assert.equal(tree.props.role, 'status');
    instance.dispose();
  }
  assert.ok(css.includes('.main-content>.route-loading:not(.route-error) {min-height:max(160px, calc(100dvh - var(--route-top, 0px)))}'));
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
  for (const name of ['worklists', 'worklistEntry', 'readiness', 'history', 'playbooks', 'operations', 'evidence-flow', 'customerDecision', 'vendor/planes']) {
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
