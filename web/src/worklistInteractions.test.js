import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {buildWorklist, worklistHref} from './worklist.js';
import {initialState, renewalFixture, worklistFixture} from './renewalFixtures.js';

// Real handlers with controlled hooks; layout is not emulated.
let server, Worklists, App;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [{
    name: 'worklist-handlers', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:wl-hooks-')) return `\0${id}`;},
    load(id) {
      if (id === '\0virtual:wl-hooks-react') return `import React from 'react'; export default React; ${hookNames.map(name => `export const ${name}=(...args)=>globalThis.__worklistHooks.${name}(...args);`).join('\n')}`;
      if (id === '\0virtual:wl-hooks-router') return `export * from 'react-router'; ${['useSearchParams', 'useNavigate', 'useLocation'].map(name => `export const ${name}=(...args)=>globalThis.__worklistHooks.${name}(...args);`).join('\n')}`;
    },
    transform(code, id) {
      if (/\/src\/(Worklists|main)\.jsx$/.test(id)) return code.replace("from 'react';", "from 'virtual:wl-hooks-react';").replace("from 'react-router';", "from 'virtual:wl-hooks-router';");
    },
  }]});
  Worklists = (await server.ssrLoadModule('/src/Worklists.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});
let globalsBefore, harnesses, frames, scrolls;
const globals = ['window', 'document', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame', '__worklistHooks'];
beforeEach(() => {
  globalsBefore = Object.fromEntries(globals.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  harnesses = []; frames = []; scrolls = [];
  globalThis.window = Object.assign(new EventTarget(), {location: new URL('http://local.test/renewals'), matchMedia: () => ({matches: false, addEventListener() {}, removeEventListener() {}}), scrollTo: options => scrolls.push(options)});
  globalThis.document = Object.assign(new EventTarget(), {documentElement: {dataset: {}}, body: {style: {}}, querySelector: () => null, getElementById: () => null, activeElement: null});
  globalThis.localStorage = {getItem: () => null, setItem() {}};
  globalThis.requestAnimationFrame = callback => {frames.push(callback); return frames.length;};
  globalThis.cancelAnimationFrame = () => {};
});
afterEach(() => {
  harnesses.forEach(hooks => hooks.dispose());
  for (const name of globals) {
    if (globalsBefore[name]) Object.defineProperty(globalThis, name, globalsBefore[name]);
    else delete globalThis[name];
  }
});

class Hooks {
  constructor(path = '/renewals') {
    this.slots = []; this.entries = [path]; this.index = 0;
    this.setParams = (params, options = {}) => {this.lastOptions = options; this.navigate(`${this.useLocation().pathname}?${params}`, options);};
    this.navigate = (path, options = {}) => {
      if (options.replace) this.entries[this.index] = path;
      else {this.entries = this.entries.slice(0, this.index + 1); this.entries.push(path); this.index++;}
    };
    harnesses.push(this);
  }
  slot(kind, init) {const index = this.cursor++; this.slots[index] ||= {kind, ...init()}; assert.equal(this.slots[index].kind, kind); return this.slots[index];}
  useState(initial) {const slot = this.slot('state', () => ({value: typeof initial === 'function' ? initial() : initial})); return [slot.value, value => {slot.value = typeof value === 'function' ? value(slot.value) : value;}];}
  useReducer(reducer, initial, init) {const slot = this.slot('reducer', () => ({value: init ? init(initial) : initial})); return [slot.value, action => {slot.value = reducer(slot.value, action);}];}
  useRef(value) {return this.slot('ref', () => ({ref: {current: value}})).ref;}
  useId() {return this.slot('id', () => ({id: `wl-test-${this.cursor}`})).id;}
  effect(kind, callback, deps) {
    const slot = this.slot(kind, () => ({}));
    if (!slot.deps || !deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {
      slot.deps = deps; this.pending[kind].push(() => {slot.cleanup?.(); slot.cleanup = callback();});
    }
  }
  useEffect(callback, deps) {this.effect('effect', callback, deps);}
  useLayoutEffect(callback, deps) {this.effect('layout', callback, deps);}
  useLocation() {const url = new URL(this.entries[this.index], 'http://local.test'); return {pathname: url.pathname, search: url.search};}
  useSearchParams() {return [new URLSearchParams(this.useLocation().search), this.setParams];}
  useNavigate() {return this.navigate;}
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__worklistHooks = this; return Component(props);}
  flush() {for (const kind of ['layout', 'effect']) for (const callback of this.pending[kind].splice(0)) callback();}
  back() {if (this.index) this.index--;}
  forward() {if (this.index < this.entries.length - 1) this.index++;}
  dispose() {this.slots.forEach(slot => slot.cleanup?.());}
}
function* nodes(node) {
  if (Array.isArray(node)) for (const child of node) yield* nodes(child);
  else if (node?.props) {yield node; yield* nodes(node.props.children);}
}
const find = (tree, predicate) => [...nodes(tree)].find(predicate);
const text = node => Array.isArray(node) ? node.map(text).join('') : node?.props ? text(node.props.children) : typeof node === 'string' || typeof node === 'number' ? String(node) : '';
const button = (tree, label) => find(tree, node => node.type === 'button' && text(node) === label);
const field = (tree, name) => find(tree, node => node.props.name === name);
const named = (tree, name) => find(tree, node => node.type?.name === name);
const rowIds = tree => [...nodes(tree)].filter(node => node.props['data-record-id']).map(node => node.props['data-record-id']);
const model = buildWorklist(initialState);
function bench(view = 'renewals', search = '') {
  const hooks = new Hooks(worklistHref(view, search));
  return {hooks, render: () => hooks.render(Worklists, {data: model, view})};
}
function target(key) {
  const calls = [];
  return {dataset: {worklistFocus: key}, tabIndex: 0, calls, closest: () => null, focus: options => calls.push(['focus', options]), scrollIntoView: options => calls.push(['scroll', options])};
}
function attach(tree, candidates = []) {
  const search = target('search'), heading = target('heading');
  tree.props.ref.current = {querySelectorAll: () => candidates, querySelector: () => heading};
  const input = field(tree, 'q'); if (input) input.props.ref.current = search;
  const selected = named(tree, 'SelectedItem'); if (selected) selected.props.headingRef.current = heading;
  return {search, heading};
}

test('owner and stage handlers push URL entries so Back and Forward restore both results and controls', () => {
  const page = bench(); let tree = page.render(); page.hooks.flush();
  field(tree, 'owner').props.onChange({target: {value: worklistFixture.renewals[1].ownerId}});
  tree = page.render(); page.hooks.flush();
  assert.deepEqual(rowIds(tree), [worklistFixture.renewals[1].id]);
  field(tree, 'stage').props.onChange({target: {value: worklistFixture.renewals[2].crm.stage}});
  tree = page.render(); page.hooks.flush(); assert.deepEqual(rowIds(tree), []);
  assert.notEqual(page.hooks.lastOptions.replace, true);
  page.hooks.back(); tree = page.render(); page.hooks.flush();
  assert.deepEqual(rowIds(tree), [worklistFixture.renewals[1].id]);
  assert.equal(field(tree, 'stage').props.value, '');
  assert.equal(field(tree, 'owner').props.value, worklistFixture.renewals[1].ownerId);
  page.hooks.forward(); tree = page.render(); page.hooks.flush(); assert.deepEqual(rowIds(tree), []);
});

test('search submits one URL update and a reloaded component restores search, selection and sorting', () => {
  const page = bench(); let tree = page.render(); page.hooks.flush();
  field(tree, 'q').props.onChange({target: {value: worklistFixture.renewals[0].accountName}});
  tree = page.render(); assert.equal(page.hooks.entries.length, 1);
  find(tree, node => node.type === 'form').props.onSubmit({preventDefault() {}});
  tree = page.render(); page.hooks.flush();
  assert.deepEqual(rowIds(tree), [renewalFixture.id]);
  assert.equal(page.hooks.entries.length, 2);
  field(tree, 'sort').props.onChange({target: {value: 'changed'}});
  tree = page.render();
  const link = find(tree, node => node.props['data-row-entry']);
  page.hooks.navigate(link.props.to);
  const reload = new Hooks(page.hooks.entries[page.hooks.index]);
  const restored = reload.render(Worklists, {data: model, view: 'renewals'});
  assert.equal(field(restored, 'q').props.value, worklistFixture.renewals[0].accountName);
  assert.equal(field(restored, 'sort').props.value, 'changed');
  assert.equal(named(restored, 'SelectedItem').props.selection.resource.id, renewalFixture.id);
  assert.deepEqual(rowIds(restored), [renewalFixture.id]);
});

test('sort handlers choose sensible defaults, toggle directions, and header sorting uses the same URL state', () => {
  const page = bench(); let tree = page.render();
  field(tree, 'sort').props.onChange({target: {value: 'blockers'}});
  tree = page.render();
  assert.equal(rowIds(tree)[0], worklistFixture.renewals[1].id);
  find(tree, node => node.props.className === 'wl-button wl-direction').props.onClick();
  tree = page.render();
  assert.equal(rowIds(tree)[0], worklistFixture.renewals[2].id);
  const changedHeader = find(tree, node => node.type === 'th' && text(node).includes('Last change'));
  find(changedHeader, node => node.type === 'button').props.onClick();
  tree = page.render();
  assert.equal(field(tree, 'sort').props.value, 'changed');
  assert.equal(page.hooks.useSearchParams()[0].get('direction'), 'desc');
});

test('facet handlers preserve overlap counts and select any matching facet without counting records twice', () => {
  const page = bench(); let tree = page.render();
  let facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  facets[0].props.onClick(); tree = page.render(); assert.equal(rowIds(tree).length, 2);
  facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  facets[1].props.onClick(); tree = page.render();
  assert.equal(rowIds(tree).length, 3);
  assert.equal(new Set(rowIds(tree)).size, 3);
  assert.deepEqual(page.hooks.useSearchParams()[0].getAll('attention'), ['blockers', 'due-soon']);
  facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  assert.equal(facets[0].props['aria-pressed'], true);
  assert.equal(facets[1].props['aria-pressed'], true);
  facets[0].props.onClick(); tree = page.render();
  assert.deepEqual(page.hooks.useSearchParams()[0].getAll('attention'), ['due-soon']);
  assert.equal(rowIds(tree).length, 3);
});

test('opening exact details focuses the heading and Escape restores the actual initiating link', () => {
  const page = bench(); let tree = page.render(); page.hooks.flush();
  const entry = find(tree, node => node.props['data-row-entry']);
  const opener = target(entry.props['data-worklist-focus']);
  entry.props.onClick({currentTarget: opener}); page.hooks.navigate(entry.props.to);
  tree = page.render(); const selected = attach(tree, [opener]); page.hooks.flush();
  assert.equal(selected.heading.calls[0][0], 'focus');
  assert.equal(selected.heading.calls[1][1].behavior, 'instant');
  let prevented = false;
  tree.props.onKeyDown({key: 'Escape', preventDefault() {prevented = true;}, stopPropagation() {}});
  tree = page.render(); attach(tree, [opener]); page.hooks.flush();
  assert.equal(prevented, true);
  assert.equal(named(tree, 'SelectedItem'), undefined);
  assert.equal(opener.calls[0][0], 'focus');
  assert.equal(page.hooks.useSearchParams()[0].has('selection'), false);
});

test('filtered-away selection stays inspectable and closing it falls back to the search field', () => {
  const page = bench('renewals', new URLSearchParams({selection: model.renewals[0].selectionKey}));
  let tree = page.render(); attach(tree); page.hooks.flush();
  field(tree, 'owner').props.onChange({target: {value: worklistFixture.renewals[1].ownerId}});
  tree = page.render(); const current = attach(tree); page.hooks.flush();
  assert.equal(named(tree, 'SelectedItem').props.selection.visible, false);
  assert.equal(current.heading.calls.length, 0, 'Filtering does not steal focus back to the inspector.');
  named(tree, 'SelectedItem').props.onClose();
  tree = page.render(); const closed = attach(tree); page.hooks.flush();
  assert.equal(closed.search.calls[0][0], 'focus');
  assert.deepEqual(rowIds(tree), [worklistFixture.renewals[1].id]);
});

test('clear-filter recovery restores focus, keeps the sort and keeps an explicit selection', () => {
  const page = bench('renewals', new URLSearchParams({q: 'missing', sort: 'changed', selection: model.renewals[0].selectionKey}));
  let tree = page.render(); attach(tree); page.hooks.flush(); assert.deepEqual(rowIds(tree), []);
  button(tree, 'Clear filters and show records').props.onClick();
  tree = page.render(); const refs = attach(tree); page.hooks.flush();
  assert.equal(rowIds(tree).length, model.renewals.length);
  assert.equal(refs.search.calls[0][0], 'focus');
  assert.equal(field(tree, 'q').props.value, '');
  assert.equal(field(tree, 'sort').props.value, 'changed');
  assert.equal(named(tree, 'SelectedItem').props.selection.resource.id, renewalFixture.id);
});

test('response and expansion handlers affect URL presentation only and recover to a tabbable search control', () => {
  const before = structuredClone(model);
  const page = bench('renewals', 'response=truncated'); let tree = page.render(); page.hooks.flush();
  assert.equal(rowIds(tree).length, 2);
  button(tree, 'Show remaining records').props.onClick(); tree = page.render(); page.hooks.flush();
  assert.equal(rowIds(tree).length, 4);
  assert.equal(page.hooks.useSearchParams()[0].get('limit'), 'all');
  field(tree, 'response').props.onChange({target: {value: 'denied'}});
  tree = page.render(); attach(tree); page.hooks.flush();
  assert.equal(field(tree, 'q'), undefined);
  assert.equal(rowIds(tree).length, 0);
  const state = named(tree, 'ResponseState'); assert.equal(state.props.response.code, 403);
  state.props.onRecover(); tree = page.render(); const refs = attach(tree); page.hooks.flush();
  assert.equal(rowIds(tree).length, 4);
  assert.equal(refs.search.calls[0][0], 'focus');
  assert.equal(refs.search.tabIndex, 0);
  assert.deepEqual(model, before);
});

test('main restores the requested work step on a direct link and preserves the return URL', () => {
  for (const item of model.workItems.filter(row => row.mode === 'walkthrough')) {
    const returnTo = worklistHref('queue', '', {sort: 'changed', selection: item.selectionKey});
    const hooks = new Hooks(`${renewalFixture.paths.workplan}?${new URLSearchParams({workItem: item.id, returnTo})}`);
    let tree = hooks.render(App); hooks.flush();
    tree = hooks.render(App); hooks.flush();
    assert.equal(named(tree, 'WorkProgress').props.selected, item.step);
    const returnContext = find(tree, node => node.props.className === 'wl-return-context');
    assert.equal(find(returnContext, node => node.props.to).props.to, returnTo);
  }
});

test('the generic page focus effect does not override worklist selection focus on entry or return', () => {
  const hooks = new Hooks(renewalFixture.paths.history);
  hooks.render(App); hooks.flush();
  scrolls.length = 0; frames.length = 0;
  hooks.navigate(worklistHref('queue', '', {selection: model.workItems[0].selectionKey}));
  const tree = hooks.render(App); hooks.flush();
  assert.equal(named(tree, 'Worklists').props.view, 'queue');
  assert.deepEqual(scrolls, []);
  assert.deepEqual(frames, []);
});
