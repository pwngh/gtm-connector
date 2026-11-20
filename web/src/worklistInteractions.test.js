import {eagerRouteModules, expandRouteContent} from './testing/routeModules.js';
import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {buildWorklist, selectWorklist, worklistHref} from './worklist.js';
import {transition} from './workflow.js';
import {fixturePeople, initialState, renewalFixture, worklistFixture} from './renewalFixtures.js';

// Real handlers with controlled hooks; layout is not emulated.
let server, Worklists, App;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect', 'useMemo'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [eagerRouteModules(), {
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
  constructor(path = '/renewals', state = null) {
    this.slots = []; this.entries = [path]; this.index = 0; this.nextKey = 1; this.metadata = [{key: 'entry-0', state}];
    this.setParams = (params, options = {}) => {this.lastOptions = options; this.navigate(`${this.useLocation().pathname}?${params}`, options);};
    this.navigate = (path, options = {}) => {
      const metadata = {key: `entry-${this.nextKey++}`, state: options.state ?? null};
      if (options.replace) {this.entries[this.index] = path; this.metadata[this.index] = metadata;}
      else {this.entries = this.entries.slice(0, this.index + 1); this.metadata = this.metadata.slice(0, this.index + 1); this.entries.push(path); this.metadata.push(metadata); this.index++;}
    };
    harnesses.push(this);
  }
  slot(kind, init) {const index = this.cursor++; this.slots[index] ||= {kind, ...init()}; assert.equal(this.slots[index].kind, kind); return this.slots[index];}
  useState(initial) {const slot = this.slot('state', () => ({value: typeof initial === 'function' ? initial() : initial})); return [slot.value, value => {slot.value = typeof value === 'function' ? value(slot.value) : value;}];}
  useReducer(reducer, initial, init) {const slot = this.slot('reducer', () => ({value: init ? init(initial) : initial})); return [slot.value, action => {slot.value = reducer(slot.value, action);}];}
  useMemo(create, deps) {
    const slot = this.slot('memo', () => ({}));
    if (!slot.deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {
      slot.value = create(); slot.deps = deps;
    }
    return slot.value;
  }
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
  useLocation() {const url = new URL(this.entries[this.index], 'http://local.test'); return {pathname: url.pathname, search: url.search, hash: url.hash, ...this.metadata[this.index]};}
  useSearchParams() {return [new URLSearchParams(this.useLocation().search), this.setParams];}
  useNavigate() {return this.navigate;}
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__worklistHooks = this; return expandRouteContent(Component(props));}
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
  const list = find(tree, node => node.props.className === 'wl-table-wrap');
  if (list) list.props.ref.current = {querySelectorAll: () => candidates.filter(entry => entry.dataset.worklistFocus.endsWith('|entry'))};
  return {search, heading};
}

test('owner and stage handlers push URL entries so Back and Forward restore both results and controls', () => {
  const page = bench('renewals', 'scope=all'); let tree = page.render(); page.hooks.flush();
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
  const page = bench('renewals', 'scope=all'); let tree = page.render();
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
  const page = bench('renewals', 'scope=all'); let tree = page.render();
  let facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  facets[0].props.onClick(); tree = page.render(); assert.equal(rowIds(tree).length, 2);
  facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  facets[1].props.onClick(); tree = page.render();
  assert.equal(rowIds(tree).length, 2);
  assert.equal(new Set(rowIds(tree)).size, 2);
  assert.deepEqual(page.hooks.useSearchParams()[0].getAll('attention'), ['blockers', 'due-soon']);
  facets = [...nodes(tree)].filter(node => node.type === 'button' && 'aria-pressed' in node.props);
  assert.equal(facets[0].props['aria-pressed'], true);
  assert.equal(facets[1].props['aria-pressed'], true);
  facets[0].props.onClick(); tree = page.render();
  assert.deepEqual(page.hooks.useSearchParams()[0].getAll('attention'), ['due-soon']);
  assert.equal(rowIds(tree).length, 1);
});

test('opening exact details focuses the heading and Escape restores the actual initiating link', () => {
  const page = bench(); let tree = page.render(); page.hooks.flush();
  const entry = find(tree, node => node.props['data-row-entry']);
  const opener = target(entry.props['data-worklist-focus']);
  page.hooks.navigate(entry.props.to, {state: entry.props.state});
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
  const page = bench('renewals', new URLSearchParams({scope: 'all', selection: model.renewals[0].selectionKey}));
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
  assert.deepEqual(rowIds(tree), ['thornfield-a17']);
  assert.equal(refs.search.calls[0][0], 'focus');
  assert.equal(field(tree, 'q').props.value, '');
  assert.equal(field(tree, 'sort').props.value, 'changed');
  assert.equal(named(tree, 'SelectedItem').props.selection.resource.id, renewalFixture.id);
});

test('response and expansion handlers affect URL presentation only and recover to a tabbable search control', () => {
  const before = structuredClone(model);
  const page = bench('renewals', 'scope=all&response=truncated'); let tree = page.render(); page.hooks.flush();
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

test('scope radio handlers keep scope, counts, and literal results in URL history', () => {
  const page = bench('queue'); let tree = page.render(); page.hooks.flush();
  assert.deepEqual(rowIds(tree), ['work-plan-review-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms', 'task-obtain-ai-terms']);
  const illustrative = find(tree, node => node.props.name === 'scope' && node.props.value === 'illustrative');
  illustrative.props.onChange(); tree = page.render(); page.hooks.flush();
  assert.deepEqual(rowIds(tree), ['work-calder-exception', 'work-ashbourne-package', 'work-mercer-owner']);
  assert.equal(find(tree, node => node.props.name === 'scope' && node.props.value === 'illustrative').props.checked, true);
  assert.equal(page.hooks.useSearchParams()[0].get('scope'), 'illustrative');
  page.hooks.back(); tree = page.render(); page.hooks.flush();
  assert.equal(find(tree, node => node.props.name === 'scope' && node.props.value === 'walkthrough').props.checked, true);
  assert.equal(rowIds(tree)[0], 'work-plan-review-ai-terms');
  page.hooks.forward(); tree = page.render(); page.hooks.flush();
  assert.equal(rowIds(tree).length, 3);
  const reload = new Hooks(page.hooks.entries[page.hooks.index]);
  assert.deepEqual(rowIds(reload.render(Worklists, {data: model, view: 'queue'})), ['work-calder-exception', 'work-ashbourne-package', 'work-mercer-owner']);
});

test('each same-account queue link has its own focus key and Escape restores the fourth row opener', () => {
  const page = bench('queue'); let tree = page.render(); page.hooks.flush();
  const accountLinks = [...nodes(tree)].filter(node => node.props.className === 'wl-account-link');
  const keys = accountLinks.map(link => link.props['data-worklist-focus']);
  assert.equal(new Set(keys).size, 4);
  const allKeys = [...nodes(tree)].filter(node => node.props['data-worklist-focus']).map(node => node.props['data-worklist-focus']);
  assert.equal(new Set(allKeys).size, allKeys.length);
  const targets = keys.map(target), entry = accountLinks[3];
  page.hooks.navigate(entry.props.to, {state: entry.props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(named(tree, 'SelectedItem').props.selection.visible, true);
  tree.props.onKeyDown({key: 'Escape', preventDefault() {}, stopPropagation() {}});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(targets[3].calls[0][0], 'focus');
  assert.ok(targets.slice(0, 3).every(element => element.calls.length === 0));
});

test('Back from item B to item A restores the opener stored with A, not the last clicked row', () => {
  const page = bench('queue'); let tree = page.render(); page.hooks.flush();
  const links = [...nodes(tree)].filter(node => node.props['data-row-entry']);
  const first = links[0], fourth = links[3], targets = links.map(link => target(link.props['data-worklist-focus']));
  page.hooks.navigate(first.props.to, {state: first.props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  const next = find(tree, node => node.props['data-worklist-focus'] === fourth.props['data-worklist-focus']);
  page.hooks.navigate(next.props.to, {state: next.props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(named(tree, 'SelectedItem').props.selection.resource.id, 'task-obtain-ai-terms');
  page.hooks.back(); tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(named(tree, 'SelectedItem').props.selection.resource.id, 'work-plan-review-ai-terms');
  named(tree, 'SelectedItem').props.onClose();
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(targets[0].calls[0][0], 'focus');
  assert.equal(targets[3].calls.length, 0);
});

test('same-selection history entries and reload keep distinct row openers', () => {
  const page = bench('queue'); let tree = page.render(); page.hooks.flush();
  let links = [...nodes(tree)].filter(node => node.props.className === 'wl-account-link');
  const targets = links.map(link => target(link.props['data-worklist-focus']));
  page.hooks.navigate(links[0].props.to, {state: links[0].props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  links = [...nodes(tree)].filter(node => node.props.className === 'wl-account-link');
  page.hooks.navigate(links[3].props.to, {state: links[3].props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  page.hooks.back();
  const reload = new Hooks(page.hooks.entries[page.hooks.index], page.hooks.useLocation().state);
  tree = reload.render(Worklists, {data: model, view: 'queue'}); attach(tree, targets); reload.flush();
  named(tree, 'SelectedItem').props.onClose();
  tree = reload.render(Worklists, {data: model, view: 'queue'}); attach(tree, targets); reload.flush();
  assert.equal(targets[0].calls[0][0], 'focus');
  assert.equal(targets[3].calls.length, 0);
});

test('showing remaining records focuses the first newly revealed row rather than the removed button', () => {
  const page = bench('renewals', 'scope=all&response=truncated');
  let tree = page.render(); attach(tree); page.hooks.flush();
  assert.deepEqual(rowIds(tree), ['calder-renewal', 'ashbourne-renewal']);
  button(tree, 'Show remaining records').props.onClick();
  tree = page.render();
  const entries = [...nodes(tree)].filter(node => node.props['data-row-entry']).map(node => target(node.props['data-worklist-focus']));
  attach(tree, entries); page.hooks.flush();
  assert.deepEqual(rowIds(tree), ['calder-renewal', 'ashbourne-renewal', 'thornfield-a17', 'mercer-renewal']);
  assert.equal(entries[2].calls[0][0], 'focus');
  assert.equal(entries[2].calls[1][1].block, 'nearest');
  assert.ok(entries.filter((entry, index) => index !== 2).every(entry => entry.calls.length === 0));
  assert.equal(button(tree, 'Show remaining records'), undefined);
});

test('clear filters strips invalid list settings and restores a valid default without discarding selection', () => {
  for (const [view, query] of [['renewals', 'sort=due'], ['queue', 'direction=bad&status=bad&limit=bad']]) {
    const page = bench(view, `${query}&q=missing&selection=renewal%3Athornfield-a17`);
    let tree = page.render(); attach(tree); page.hooks.flush();
    assert.equal(selectWorklist(model, view, page.hooks.useLocation().search).invalid, true);
    button(tree, 'Clear filters').props.onClick();
    tree = page.render(); const refs = attach(tree); page.hooks.flush();
    const params = page.hooks.useSearchParams()[0];
    for (const key of new URLSearchParams(query).keys()) assert.equal(params.has(key), false);
    assert.equal(selectWorklist(model, view, params).invalid, false);
    assert.equal(named(tree, 'SelectedItem').props.selection.resource.id, renewalFixture.id);
    assert.equal(refs.search.calls[0][0], 'focus');
  }
});

test('the real main command handler uses the walkthrough clock even under a far-future device clock', () => {
  const OriginalDate = globalThis.Date;
  const future = '2027-03-01T12:00:00.000Z';
  globalThis.Date = class extends OriginalDate {
    constructor(...args) {super(...(args.length ? args : [future]));}
    static now() {return OriginalDate.parse(future);}
  };
  try {
    const hooks = new Hooks(renewalFixture.paths.workplan);
    let tree = hooks.render(App); hooks.flush();
    const actionBar = find(tree, node => node.props.className === 'action-buttons');
    find(actionBar, node => node.props.primary).props.onClick();
    tree = hooks.render(App); hooks.flush();
    named(tree, 'ReviewDecision').props.onPerform('request_review', {at: future});
    tree = hooks.render(App); hooks.flush();
    find(find(tree, node => node.props.className === 'action-buttons'), node => node.props.primary).props.onClick();
    tree = hooks.render(App); hooks.flush();
    const submitted = named(tree, 'ReviewDecision').props.state;
    assert.equal(submitted.history[0].at, renewalFixture.workflowClockStart);
    named(tree, 'ReviewDecision').props.onPerform('approve_plan', {at: future});
    hooks.navigate('/queue?scope=all');
    tree = hooks.render(App); hooks.flush();
    const after = named(tree, 'Worklists').props.data;
    assert.equal(OriginalDate.parse(after.asOf) - OriginalDate.parse(renewalFixture.workflowClockStart), 60000);
    assert.equal(after.renewals.find(row => row.id === 'calder-renewal').daysLeft, 8);
    assert.deepEqual(after.renewals.filter(row => row.mode === 'illustrative'), model.renewals.filter(row => row.mode === 'illustrative'));
    assert.deepEqual(after.workItems.filter(row => row.mode === 'illustrative'), model.workItems.filter(row => row.mode === 'illustrative'));
  } finally {globalThis.Date = OriginalDate;}
});

test('main and the queue use the same submitted-plan reviewer eligibility, including command refusal', () => {
  const submitted = transition(initialState, {type: 'request_review'});
  const state = {...submitted, plan: {...submitted.plan, owner: fixturePeople.internalReviewer}};
  globalThis.localStorage.getItem = key => key === 'gtm-workflow-demo-v1' ? JSON.stringify(state) : null;
  const hooks = new Hooks(renewalFixture.paths.workplan);
  let tree = hooks.render(App); hooks.flush();
  find(find(tree, node => node.props.className === 'action-buttons'), node => node.props.primary).props.onClick();
  tree = hooks.render(App); hooks.flush();
  const review = named(tree, 'ReviewDecision');
  assert.equal(review.props.derived.canApprovePlan, false);
  assert.equal(buildWorklist(review.props.state).workItems[0].canAct, false);
  review.props.onPerform('approve_plan');
  tree = hooks.render(App); hooks.flush();
  assert.equal(named(tree, 'ReviewDecision').props.state.history.length, 1);
  assert.equal(named(tree, 'ReviewDecision').props.state.plan.status, 'in_review');
  assert.ok(named(tree, 'ReviewDecision').props.state.error);
});

test('main exact document destinations focus different targets and open only their own action', () => {
  let received = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document']) received = transition(received, {type});
  for (const [state, kind, modal] of [[initialState, 'inspection', null], [received, 'collection', 'documentDetail'], [received, 'inspection', 'verify']]) {
    globalThis.localStorage.getItem = key => key === 'gtm-workflow-demo-v1' ? JSON.stringify(state) : null;
    const item = buildWorklist(state).workItems.find(row => row.kind === kind);
    const hooks = new Hooks(item.allowedActions[0].href);
    const destination = target(item.entryTargetId);
    globalThis.document.getElementById = id => id === item.entryTargetId ? destination : null;
    frames.length = 0;
    let tree = hooks.render(App); hooks.flush();
    tree = hooks.render(App); hooks.flush();
    for (const frame of frames.splice(0)) frame();
    assert.equal(destination.calls.find(call => call[0] === 'focus')[0], 'focus');
    const exact = named(tree, 'WorklistStepEntry');
    assert.equal(exact.props.item.id, item.id);
    if (modal) {
      exact.props.onAction(exact.props.item.workflowAction.command);
      tree = hooks.render(App); hooks.flush();
      if (modal === 'verify') assert.equal(named(tree, 'ReviewDecision').props.type, modal);
      else assert.equal(named(tree, 'WorkspaceDialog').key, 'documentDetail');
    } else {
      assert.equal(exact.props.item.workflowAction, null);
      assert.equal(exact.props.item.blockedReasons[0].code, 'document_receipt');
    }
  }
});

test('readiness and evidence keep the originating return link through tabs and work-plan navigation', () => {
  const returnTo = '/queue?scope=all&sort=changed&attention=blocked&selection=requirement%3Areq-ai-counsel';
  for (const section of ['readiness', 'evidence']) {
    const hooks = new Hooks(`${renewalFixture.paths.root}/${section}?${new URLSearchParams({returnTo})}`);
    let tree = hooks.render(App); hooks.flush();
    const context = find(tree, node => node.props.className === 'wl-return-context');
    assert.equal(find(context, node => node.props.to).props.to, returnTo);
    const tabs = find(tree, node => node.props['aria-label'] === 'Renewal sections');
    for (const link of [...nodes(tabs)].filter(node => node.props.to)) {
      assert.equal(new URL(link.props.to, 'http://local.test').searchParams.get('returnTo'), returnTo);
    }
    if (section === 'evidence') named(tree, 'EvidenceFlow').props.onStep(2);
    else find(tree, node => node.type?.name === 'Button' && text(node) === 'Open work plan').props.onClick();
    assert.equal(hooks.useLocation().pathname, renewalFixture.paths.workplan);
    assert.equal(hooks.useSearchParams()[0].get('returnTo'), returnTo);
  }
});

test('selecting an account in the actual command palette keeps current list filters and sort', () => {
  for (const view of ['renewals', 'queue']) {
    const query = 'scope=illustrative&owner=unassigned&sort=changed&direction=asc&attention=unassigned&limit=all';
    const hooks = new Hooks(`/${view}?${query}`);
    let tree = hooks.render(App); hooks.flush();
    find(tree, node => node.props['aria-label'] === 'Search workspace').props.onClick();
    tree = hooks.render(App); hooks.flush();
    const item = named(tree, 'CommandPalette').props.items.find(item => item.group === 'Accounts' && item.label === worklistFixture.renewals[3].accountName);
    item.onSelect();
    assert.equal(hooks.useLocation().pathname, `/${view}`);
    const params = hooks.useSearchParams()[0];
    for (const [key, value] of new URLSearchParams(query)) assert.equal(params.get(key), value);
    assert.equal(params.get('selection'), 'renewal:mercer-renewal');
  }
});

test('returning through readiness keeps the original row-and-purpose opener for Escape', () => {
  const page = bench('queue'); let tree = page.render(); page.hooks.flush();
  const entries = [...nodes(tree)].filter(node => node.props.className === 'wl-account-link');
  const entry = entries[3], targets = entries.map(link => target(link.props['data-worklist-focus']));
  page.hooks.navigate(entry.props.to, {state: entry.props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  const selected = named(tree, 'SelectedItem');
  const inspector = selected.type(selected.props);
  const action = find(inspector, node => node.props.className === 'wl-button wl-primary');
  const detail = new Hooks(action.props.to, action.props.state);
  tree = detail.render(App); detail.flush();
  const context = find(tree, node => node.props.className === 'wl-return-context');
  const returnLink = find(context, node => node.props.to);
  page.hooks.navigate(returnLink.props.to, {state: returnLink.props.state});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  tree.props.onKeyDown({key: 'Escape', preventDefault() {}, stopPropagation() {}});
  tree = page.render(); attach(tree, targets); page.hooks.flush();
  assert.equal(targets[3].calls[0][0], 'focus');
  assert.ok(targets.slice(0, 3).every(element => element.calls.length === 0));
});
