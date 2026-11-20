import {eagerRouteModules, expandRouteContent} from './testing/routeModules.js';
import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {historyFixture, historyTestRecords, initialState, readinessFixture, walkthroughDecisions, walkthroughDocumentExamples} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, readinessHref} from './readiness.js';
import {historyHref, selectHistory} from './history.js';

let server, History, Readiness, App;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect', 'useMemo'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [eagerRouteModules(), {
    name: 'history-handlers', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:hs-hooks-')) return `\0${id}`;},
    load(id) {
      if (id === '\0virtual:hs-hooks-react') return `import React from 'react'; export default React; ${hookNames.map(name => `export const ${name}=(...args)=>globalThis.__historyHooks.${name}(...args);`).join('\n')}`;
      if (id === '\0virtual:hs-hooks-router') return `export * from 'react-router'; ${['useSearchParams', 'useNavigate', 'useLocation'].map(name => `export const ${name}=(...args)=>globalThis.__historyHooks.${name}(...args);`).join('\n')}`;
    },
    transform(code, id) {
      if (/\/src\/(History|Readiness|main)\.jsx$/.test(id)) return code.replace("from 'react';", "from 'virtual:hs-hooks-react';").replace("from 'react-router';", "from 'virtual:hs-hooks-router';");
    },
  }]});
  History = (await server.ssrLoadModule('/src/History.jsx')).default;
  Readiness = (await server.ssrLoadModule('/src/Readiness.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});
let originals, harnesses, frames, scrolls, storage;
const globals = ['window', 'document', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame', '__historyHooks'];
beforeEach(() => {
  originals = Object.fromEntries(globals.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  harnesses = []; frames = []; scrolls = []; storage = new Map();
  globalThis.window = Object.assign(new EventTarget(), {location: new URL('http://local.test'), matchMedia: () => ({matches: false, addEventListener() {}, removeEventListener() {}}), scrollTo: options => scrolls.push(options)});
  globalThis.document = Object.assign(new EventTarget(), {documentElement: {dataset: {}}, body: {style: {}}, querySelector: () => null, getElementById: () => null, activeElement: null});
  globalThis.localStorage = {getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value)};
  globalThis.requestAnimationFrame = callback => {frames.push(callback); return frames.length;};
  globalThis.cancelAnimationFrame = () => {};
});
afterEach(() => {
  harnesses.forEach(hooks => hooks.dispose());
  for (const name of globals) {if (originals[name]) Object.defineProperty(globalThis, name, originals[name]); else delete globalThis[name];}
});

// Controlled hooks exercise the page's real handlers without another dependency.
class Hooks {
  constructor(path = historyFixture.paths.history, state = null) {
    this.slots = []; this.entries = [path]; this.index = 0; this.nextKey = 1; this.metadata = [{key: 'entry-0', state}]; this.elements = new Map();
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
  useMemo(create, deps) {const slot = this.slot('memo', () => ({})); if (!slot.deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {slot.value = create(); slot.deps = deps;} return slot.value;}
  useRef(value) {return this.slot('ref', () => ({ref: {current: value}})).ref;}
  useId() {return this.slot('id', () => ({id: `hs-test-${this.cursor}`})).id;}
  effect(kind, callback, deps) {const slot = this.slot(kind, () => ({})); if (!slot.deps || !deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {slot.deps = deps; this.pending[kind].push(() => {slot.cleanup?.(); slot.cleanup = callback();});}}
  useEffect(callback, deps) {this.effect('effect', callback, deps);}
  useLayoutEffect(callback, deps) {this.effect('layout', callback, deps);}
  useLocation() {const url = new URL(this.entries[this.index], 'http://local.test'); return {pathname: url.pathname, search: url.search, hash: url.hash, ...this.metadata[this.index]};}
  useSearchParams() {return [new URLSearchParams(this.useLocation().search), this.setParams];}
  useNavigate() {return this.navigate;}
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__historyHooks = this; return expandRouteContent(Component(props));}
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
const named = (tree, name) => find(tree, node => node.type?.name === name);
const field = (tree, name, value) => find(tree, node => node.props.name === name && (value === undefined || node.props.value === value));
const inspectable = new Set(['TimelineEntry', 'EntryDetails', 'Comparison', 'Impacts', 'Reference', 'Intervals', 'ValueList', 'TypedValue', 'EvaluationChanges']);
function expand(node) {
  if (Array.isArray(node)) return node.map(expand);
  if (!node?.props) return node;
  if (inspectable.has(node.type?.name)) return expand(node.type(node.props));
  return {...node, props: {...node.props, children: expand(node.props.children)}};
}
const element = key => ({dataset: {}, calls: [], isConnected: true, closest: () => null,
  focus(options) {this.calls.push(['focus', options]); globalThis.document.activeElement = this;},
  scrollIntoView(options) {this.calls.push(['scroll', options]);}, key});
function* domNodes(node, disclosures = [], summaryFor = null) {
  if (Array.isArray(node)) {for (const child of node) yield* domNodes(child, disclosures, summaryFor); return;}
  if (!node?.props) return;
  let chain = disclosures;
  if (node.type === 'details') {
    const parent = chain.at(-1);
    const disclosure = {open: Boolean(node.props.open), parentElement: {closest: () => parent},
      querySelector: () => ({contains: element => element.summaryFor === disclosure})};
    chain = [...chain, disclosure];
  }
  const summary = node.type === 'summary' ? chain.at(-1) : summaryFor;
  yield {node, disclosures: chain, summaryFor: summary};
  yield* domNodes(node.props.children, chain, summary);
}
function attach(tree, hooks) {
  const targets = [], controls = [], entries = [], headings = new Map();
  hooks.elements.forEach(element => {element.isConnected = false;});
  for (const {node, disclosures, summaryFor} of domNodes(tree)) {
    const focusKey = node.props['data-history-focus'], targetKey = node.props['data-history-target'], id = node.props.id;
    if (!focusKey && !targetKey && !id) continue;
    const key = focusKey ? `focus:${focusKey}` : targetKey ? `target:${targetKey}` : `id:${id}`;
    if (!hooks.elements.has(key)) hooks.elements.set(key, element(key));
    const dom = hooks.elements.get(key); dom.isConnected = true;
    dom.disclosures = disclosures; dom.summaryFor = summaryFor;
    dom.closest = selector => selector === 'details' ? disclosures.at(-1)
      : selector === 'details:not([open])' ? disclosures.findLast(disclosure => !disclosure.open) : null;
    dom.getClientRects = () => disclosures.every(disclosure => disclosure.open || disclosure === summaryFor) ? [{}] : [];
    if (focusKey) {dom.dataset.historyFocus = focusKey; controls.push(dom);}
    if (targetKey) {dom.dataset.historyTarget = targetKey; targets.push(dom); hooks.elements.set(`target:${targetKey}`, dom);}
    if (node.props['data-history-entry']) entries.push(dom);
    if (id) headings.set(id, dom);
  }
  if (!hooks.elements.has('heading')) hooks.elements.set('heading', element('heading'));
  hooks.elements.get('heading').isConnected = true;
  tree.props.ref.current = {querySelectorAll: selector => selector === '[data-history-target]' ? targets : selector === '[data-history-entry]' ? entries : controls,
    querySelector: selector => headings.get(selector.slice(1))};
  find(tree, node => node.type === 'h2' && node.props.ref).props.ref.current = hooks.elements.get('heading');
}

function bench(search = '', state = initialState, metadata = null) {
  const hooks = new Hooks(historyHref(search), metadata), exports = [];
  const page = {hooks, exports, state, render() {const tree = expand(hooks.render(History, {state: page.state, onExport: (data, filename) => exports.push({data, filename})})); attach(tree, hooks); hooks.flush(); return tree;}};
  return page;
}
const params = hooks => hooks.useSearchParams()[0];
const open = (hooks, link) => hooks.navigate(link.props.to, {state: link.props.state});
const link = (tree, key) => find(tree, node => node.props['data-history-focus'] === key);
const control = (hooks, key) => hooks.elements.get(`focus:${key}`);
const target = (hooks, id) => hooks.elements.get(`target:entry:${id}`);
const eventIds = tree => [...nodes(tree)].filter(node => node.props['data-history-id']).map(node => node.props['data-history-id']);
function escape(tree, target = null) {const event = {key: 'Escape', target, preventDefault() {this.prevented = true;}, stopPropagation() {this.stopped = true;}}; tree.props.onKeyDown(event); return event;}
const act = (state, type, extra = {}) => transitionWalkthrough(state, {type, ...extra});
function complete() {
  return ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', 'record_customer_approval', 'resolve_requirement'].reduce((state, type) => act(state, type, {reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step}), initialState);
}
const example = historyFixture.examples.find(item => item.mode === 'example');
const exampleId = type => example.steps.find(step => step.action?.type === type || step.correction?.type === type).id;

test('category filtering and recording order update real URL history and restore rows on Back and Forward', () => {
  const page = bench('', complete()); let tree = page.render();
  field(tree, 'category').props.onChange({target: {value: 'document'}}); tree = page.render();
  const firstOrder = eventIds(tree);
  assert.equal(firstOrder.length, 2);
  field(tree, 'order').props.onChange({target: {value: 'newest'}}); tree = page.render();
  assert.deepEqual(eventIds(tree), firstOrder.toReversed());
  assert.notEqual(page.hooks.lastOptions.replace, true);
  page.hooks.back(); tree = page.render();
  assert.equal(field(tree, 'order').props.value, 'oldest');
  assert.deepEqual(eventIds(tree), firstOrder);
  page.hooks.back(); tree = page.render(); assert.equal(field(tree, 'category').props.value, 'all');
  assert.ok(eventIds(tree).length > firstOrder.length);
  page.hooks.forward(); tree = page.render(); assert.deepEqual(eventIds(tree), firstOrder);
});

test('known-by and view controls apply actual corrections only after they are known and restore on Back', () => {
  const decisionId = exampleId('record_customer_approval'), point = exampleId('resolve_requirement');
  const page = bench(new URLSearchParams({example: example.id, entry: decisionId, as_of: point})); let tree = page.render();
  field(tree, 'view', 'effective').props.onChange(); tree = page.render();
  assert.equal(selectHistory(page.state, params(page.hooks)).selection.resource.appliedCorrections.length, 0);
  field(tree, 'as_of').props.onChange({target: {value: 'latest'}}); tree = page.render();
  assert.equal(selectHistory(page.state, params(page.hooks)).selection.resource.appliedCorrections.length, 1);
  assert.equal(params(page.hooks).get('entry'), decisionId);
  page.hooks.back(); tree = page.render();
  assert.equal(field(tree, 'as_of').props.value, point);
  assert.equal(field(tree, 'view', 'effective').props.checked, true);
  assert.equal(selectHistory(page.state, params(page.hooks)).selection.resource.appliedCorrections.length, 0);
  page.hooks.back(); tree = page.render(); assert.equal(field(tree, 'view', 'recorded').props.checked, true);
});

test('selecting an entry focuses its keyboard link and Escape restores the exact initiating summary link', () => {
  const page = bench('', complete()); let tree = page.render();
  open(page.hooks, link(tree, 'summary:requirement')); tree = page.render();
  const id = params(page.hooks).get('entry');
  assert.equal(globalThis.document.activeElement, target(page.hooks, id));
  assert.equal(target(page.hooks, id).calls[1][1].behavior, 'instant');
  assert.equal(escape(tree).prevented, true); tree = page.render();
  assert.equal(params(page.hooks).has('entry'), false);
  assert.equal(globalThis.document.activeElement, control(page.hooks, 'summary:requirement'));
});

test('view, category and sort changes preserve selection without stealing focus from controls', () => {
  const state = complete(), id = state.history.at(-1).id, page = bench(new URLSearchParams({entry: id}), state); let tree = page.render();
  const selected = target(page.hooks, id); selected.calls = [];
  field(tree, 'category').props.onChange({target: {value: 'document'}}); tree = page.render();
  field(tree, 'order').props.onChange({target: {value: 'newest'}}); tree = page.render();
  field(tree, 'view', 'effective').props.onChange(); tree = page.render();
  assert.equal(selected.calls.length, 0);
  assert.equal(params(page.hooks).get('entry'), id);
  assert.equal(selectHistory(state, params(page.hooks)).selection.outside, true);
  assert.equal(eventIds(tree).filter(entryId => entryId === id).length, 1);
});

test('Back between selections restores each entry’s own opener, including interval and summary links', () => {
  const state = complete(), page = bench('', state); let tree = page.render();
  const intervalId = selectHistory(state).intervals[0].intervals[0].id, opener = `interval:${intervalId}:start`;
  open(page.hooks, link(tree, opener)); tree = page.render();
  open(page.hooks, link(tree, 'summary:requirement')); tree = page.render();
  page.hooks.back(); tree = page.render();
  assert.equal(params(page.hooks).get('entry'), historyFixture.baseline.id);
  escape(tree); page.render();
  assert.equal(globalThis.document.activeElement, control(page.hooks, opener));
});

test('reload restores exact entry, point, view, filters and order, with a surviving opener on close', () => {
  const state = complete(), page = bench('category=document&order=newest&view=effective', state, {worklistOpener: {focusKey: 'original-row'}}); let tree = page.render();
  open(page.hooks, link(tree, 'summary:requirement')); tree = page.render();
  const id = params(page.hooks).get('entry');
  field(tree, 'as_of').props.onChange({target: {value: id}}); tree = page.render();
  const reloaded = bench(params(page.hooks), state, page.hooks.useLocation().state); tree = reloaded.render();
  assert.equal(globalThis.document.activeElement, target(reloaded.hooks, id));
  assert.equal(field(tree, 'category').props.value, 'document');
  assert.equal(field(tree, 'order').props.value, 'newest');
  assert.equal(field(tree, 'as_of').props.value, id);
  assert.equal(field(tree, 'view', 'effective').props.checked, true);
  escape(tree); reloaded.render();
  assert.equal(globalThis.document.activeElement, control(reloaded.hooks, 'summary:requirement'));
  assert.equal(reloaded.hooks.useLocation().state.worklistOpener.focusKey, 'original-row');
});

test('selected entry arrows follow the rendered recording order without changing the selected URL', () => {
  const state = complete(), id = state.history.at(-1).id, page = bench(new URLSearchParams({entry: id, order: 'newest'}), state); let tree = page.render();
  const list = find(tree, node => node.type === 'ol' && node.props.className === 'hs-timeline');
  const links = [...nodes(list)].filter(node => node.props['data-history-entry']);
  const currentTarget = {querySelectorAll: () => links.map(node => control(page.hooks, node.props['data-history-focus']))};
  const length = page.hooks.entries.length;
  const event = {key: 'ArrowDown', target: target(page.hooks, id), currentTarget, preventDefault() {this.prevented = true;}};
  list.props.onKeyDown(event);
  assert.equal(event.prevented, true);
  assert.equal(globalThis.document.activeElement, control(page.hooks, links[1].props['data-history-focus']));
  assert.equal(params(page.hooks).get('entry'), id);
  assert.equal(page.hooks.entries.length, length);
  open(page.hooks, links[1]); tree = page.render();
  assert.equal(globalThis.document.activeElement, target(page.hooks, params(page.hooks).get('entry')));
});

test('native select Escape does not clear history and unknown or future selection recovers to the heading', () => {
  const state = complete(), page = bench(new URLSearchParams({entry: state.history.at(-1).id, as_of: state.history[0].id}), state); let tree = page.render();
  assert.equal(globalThis.document.activeElement, target(page.hooks, state.history.at(-1).id));
  assert.equal(escape(tree, {closest: () => ({})}).prevented, undefined);
  assert.ok(params(page.hooks).has('entry'));
  const error = find(tree, node => node.props.className === 'hs-selection-error hs-paper');
  find(error, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(params(page.hooks).has('entry'), false);
  assert.equal(globalThis.document.activeElement, page.hooks.elements.get('heading'));
  assert.equal(params(page.hooks).get('as_of'), state.history[0].id);
});

test('invalid-point recovery changes only the time bound and focuses the known-by control', () => {
  const page = bench('as_of=missing&category=document&view=effective&order=newest', complete()); let tree = page.render();
  assert.equal(eventIds(tree).length, 0);
  const error = find(tree, node => node.props.className === 'hs-error hs-paper');
  find(error, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(field(tree, 'as_of').props.value, 'latest');
  assert.equal(field(tree, 'category').props.value, 'document');
  assert.equal(field(tree, 'view', 'effective').props.checked, true);
  assert.equal(field(tree, 'order').props.value, 'newest');
  assert.equal(globalThis.document.activeElement, control(page.hooks, 'point-control'));
  assert.equal(eventIds(tree).length, 2);
});

test('empty-filter recovery preserves the selected entry and point and restores the category control', () => {
  const state = complete(), id = state.history.at(-1).id, page = bench(new URLSearchParams({entry: id, as_of: id, category: 'correction'}), state); let tree = page.render();
  const empty = find(tree, node => node.props.className === 'hs-empty hs-paper');
  find(empty, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(params(page.hooks).get('entry'), id);
  assert.equal(params(page.hooks).get('as_of'), id);
  assert.equal(field(tree, 'category').props.value, 'all');
  assert.equal(globalThis.document.activeElement, control(page.hooks, 'category-control'));
});

test('the correction example is isolated and returning focuses its visible disclosure summary', () => {
  const state = complete(), before = structuredClone(state), page = bench(new URLSearchParams({entry: state.history.at(-1).id, as_of: state.history.at(-1).id}), state); let tree = page.render();
  field(tree, 'example').props.onChange({target: {value: example.id}}); tree = page.render();
  assert.equal(params(page.hooks).has('entry'), false);
  assert.equal(params(page.hooks).has('as_of'), false);
  assert.ok(eventIds(tree).includes(exampleId('correction')));
  const note = find(tree, node => node.props.className === 'hs-example-note');
  find(note, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(params(page.hooks).has('example'), false);
  assert.equal(globalThis.document.activeElement, control(page.hooks, 'example-summary'));
  assert.deepEqual(state, before);
  assert.equal(storage.size, 0);
});

test('a source inspection round-trip preserves history state and restores the exact citation opener', () => {
  const state = complete(), id = state.history.at(-1).id;
  const page = bench(new URLSearchParams({entry: id, as_of: id, view: 'effective', category: 'requirement', returnTo: '/queue?sort=changed'}), state, {worklistOpener: {focusKey: 'list-row'}});
  let tree = page.render();
  const ref = selectHistory(state, params(page.hooks)).selection.resource.references.find(reference => reference.kind === 'unit');
  const focusKey = `ref:${id}:unit:${ref.id}`, action = link(tree, focusKey);
  const destination = new Hooks(action.props.to, action.props.state);
  tree = destination.render(App); destination.flush();
  const context = find(tree, node => node.props.className === 'hs-return-context'), back = find(context, node => node.props.to);
  assert.equal(new URL(action.props.to, 'http://local.test').searchParams.get('unit'), ref.n);
  assert.equal(back.props.state.historyRestoreFocus, focusKey);
  assert.equal(back.props.to, historyHref(params(page.hooks)));
  const returned = bench(new URL(back.props.to, 'http://local.test').search, state, back.props.state); returned.render();
  assert.equal(globalThis.document.activeElement, control(returned.hooks, focusKey));
  assert.equal(target(returned.hooks, id).calls.length, 0);
  assert.equal(returned.hooks.useLocation().state.worklistOpener.focusKey, 'list-row');
});

test('Readiness links to the exact recording and known-by point and keeps its own return focus', () => {
  const state = complete(), readinessParams = new URLSearchParams({check: 'resolve_requirement', order: 'blockers', returnTo: '/queue?sort=changed'});
  const hooks = new Hooks(readinessHref(readinessParams)), journal = advanceReadinessJournal(advanceReadinessJournal(null, initialState), state);
  const tree = expand(hooks.render(Readiness, {journal, onAction() {}}));
  const trace = find(tree, node => node.props['data-readiness-focus'] === 'history-recording');
  const url = new URL(trace.props.to, 'http://local.test');
  assert.equal(url.pathname, historyFixture.paths.history);
  assert.equal(url.searchParams.get('entry'), state.history.at(-1).id);
  assert.equal(url.searchParams.get('as_of'), state.history.at(-1).id);
  assert.equal(url.searchParams.get('view'), 'recorded');
  assert.equal(url.searchParams.get('readinessReturn'), readinessHref(readinessParams));
  const destination = new Hooks(trace.props.to, trace.props.state);
  const app = destination.render(App); destination.flush();
  const context = find(app, node => node.props.className === 'rd-return-context'), back = find(context, node => node.props.to);
  assert.equal(back.props.state.readinessRestoreFocus, 'history-recording');
  assert.equal(back.props.to, readinessHref(readinessParams));
});

test('a replacement receipt retains a link to the earlier document record rather than substituting the live file', () => {
  const first = complete(), state = act(first, 'obtain_document', {document: walkthroughDocumentExamples.replacement});
  const page = bench(new URLSearchParams({entry: state.history.at(-1).id, category: 'document'}), state); let tree = page.render();
  const comparison = link(tree, `compare:${state.history.at(-1).id}`);
  open(page.hooks, comparison); tree = page.render();
  const selected = selectHistory(state, params(page.hooks)).selection.resource;
  assert.equal(selected.record.after.document.version, first.document.version);
  assert.equal(selected.type, 'obtain_document');
  assert.equal(globalThis.document.activeElement, target(page.hooks, selected.id));
});

test('exports use the actual time-bound filtered projection and a safe fixture filename', () => {
  const state = complete(), point = state.history.find(event => event.type === 'approve_plan').id;
  const page = bench(new URLSearchParams({as_of: point, category: 'plan'}), state); const tree = page.render();
  const intro = find(tree, node => node.props.className === 'hs-intro');
  find(intro, node => node.type === 'button').props.onClick();
  assert.equal(page.exports.length, 1);
  assert.equal(page.exports[0].filename, historyFixture.filename);
  assert.equal(page.exports[0].data.knownBy.id, point);
  assert.ok(page.exports[0].data.entries.every(entry => entry.category === 'plan'));
  assert.ok(!page.exports[0].data.entries.some(entry => entry.id === state.history.at(-1).id));
});

test('interval and guide jumps move focus without adding hash history or losing the selection opener', () => {
  const page = bench('', complete()); let tree = page.render();
  open(page.hooks, link(tree, 'summary:requirement')); tree = page.render();
  const context = find(tree, node => node.props.className === 'hs-view-context'), jump = find(context, node => node.type === 'button');
  const length = page.hooks.entries.length, metadata = page.hooks.useLocation().state;
  jump.props.onClick();
  assert.equal(globalThis.document.activeElement, page.hooks.elements.get('id:history-intervals'));
  assert.equal(page.hooks.entries.length, length);
  assert.equal(page.hooks.useLocation().state, metadata);
  assert.equal(page.hooks.useLocation().hash, '');
  escape(tree); page.render();
  assert.equal(globalThis.document.activeElement, control(page.hooks, 'summary:requirement'));
  const hooks = new Hooks(historyHref()), guide = element('guide');
  globalThis.document.getElementById = id => id === 'history-guide' ? guide : null;
  tree = hooks.render(App); hooks.flush();
  const heading = find(tree, node => node.props.className === 'heading-actions');
  find(heading, node => node.type === 'button').props.onClick();
  assert.equal(globalThis.document.activeElement, guide);
  assert.equal(hooks.entries.length, 1);
});

test('the main route focus effect yields to exact, invalid and restored history selections', () => {
  for (const query of ['entry=missing', `entry=${historyFixture.baseline.id}`, 'entry=&view=effective']) {
    const hooks = new Hooks(historyFixture.paths.workplan);
    hooks.render(App); hooks.flush(); frames.length = 0; scrolls.length = 0;
    hooks.navigate(historyHref(query));
    const tree = hooks.render(App); hooks.flush();
    assert.ok(named(tree, 'History'));
    assert.deepEqual(frames, []);
    assert.deepEqual(scrolls, []);
  }
});

test('App persists richer event inputs committed in Work plan and History restores them on reload', () => {
  const hooks = new Hooks(historyFixture.paths.workplan); let tree = hooks.render(App); hooks.flush();
  const actionBar = find(tree, node => node.props.className === 'action-buttons');
  find(actionBar, node => node.props.primary).props.onClick(); tree = hooks.render(App); hooks.flush();
  named(tree, 'ReviewDecision').props.onPerform('request_review'); tree = hooks.render(App); hooks.flush();
  const saved = JSON.parse(storage.get('gtm-workflow-demo-v1')), event = saved.history.at(-1);
  assert.equal(event.historyRecord.before.plan.status, 'draft');
  assert.equal(event.historyRecord.after.plan.status, 'in_review');
  assert.equal(event.actor, null);
  assert.equal(event.eventAt, null);
  assert.equal(event.recordedAt, event.at);
  const reload = new Hooks(historyHref('', {entry: event.id, as_of: event.id})); tree = reload.render(App); reload.flush();
  assert.deepEqual(named(tree, 'History').props.state.history.at(-1), event);
});

test('new recordings retain URL selection and focus the same entry with its newly known outcome', () => {
  const state = act(initialState, 'request_review'), id = state.history[0].id, page = bench(new URLSearchParams({entry: id}), state); let tree = page.render();
  const selected = target(page.hooks, id); selected.calls = [];
  page.state = act(state, 'approve_plan'); tree = page.render();
  assert.equal(params(page.hooks).get('entry'), id);
  assert.equal(selected.calls[0][0], 'focus');
  assert.equal(selectHistory(page.state, params(page.hooks)).selection.resource.outcomes[0].id, page.state.history.at(-1).id);
});


test('a nested evidence-check return reopens its remounted disclosure before restoring keyboard focus', () => {
  const state = complete(), id = state.history.find(event => event.type === 'record_customer_approval').id;
  const page = bench(new URLSearchParams({entry: id, view: 'effective'}), state);
  let tree = page.render();
  const focusKey = `evidence-check:${id}:customer_authority`, opener = control(page.hooks, focusKey);
  assert.ok(opener.disclosures.length > 0);
  assert.equal(opener.getClientRects().length, 0);
  opener.disclosures.forEach(disclosure => {disclosure.open = true;});
  opener.focus();
  const action = link(tree, focusKey), destination = new Hooks(action.props.to, action.props.state);
  tree = destination.render(App); destination.flush();
  const back = find(find(tree, node => node.props.className === 'hs-return-context'), node => node.props.to);
  const returned = bench(new URL(back.props.to, 'http://local.test').search, state, back.props.state);
  returned.render();
  const restored = control(returned.hooks, focusKey);
  assert.ok(restored.disclosures.every(disclosure => disclosure.open));
  assert.equal(restored.getClientRects().length, 1);
  assert.equal(globalThis.document.activeElement, restored);
  assert.equal(target(returned.hooks, id).calls.length, 0);
});

test('Readiness does not link an input-only change to a previous unrelated recording', () => {
  const state = complete(), prior = advanceReadinessJournal(null, state);
  const changed = {...state, plan: {...state.plan, title: historyTestRecords.revisedTitle}};
  const journal = advanceReadinessJournal(prior, changed), hooks = new Hooks(readinessHref());
  assert.equal(journal.changes.cause.id, null);
  assert.equal(journal.current.lastEventId, state.history.at(-1).id);
  const tree = expand(hooks.render(Readiness, {journal, onAction() {}}));
  assert.equal(find(tree, node => node.props['data-readiness-focus'] === 'history-recording'), undefined);
});

test('the History export handler includes the displayed outside-filter selection separately', () => {
  const state = complete(), selected = state.history.at(-1).id;
  const page = bench(new URLSearchParams({entry: selected, category: 'document'}), state), tree = page.render();
  const intro = find(tree, node => node.props.className === 'hs-intro');
  find(intro, node => node.type === 'button').props.onClick();
  assert.equal(page.exports[0].data.selectedOutside.outsideFilter, true);
  assert.equal(page.exports[0].data.selectedOutside.entry.id, selected);
  assert.ok(page.exports[0].data.entries.every(entry => entry.category === 'document'));
  assert.equal(eventIds(tree).filter(id => id === selected).length, 1);
});

test('History to a Work plan work-item link shows only supplied return paths, never a fabricated queue banner', () => {
  const state = complete(), id = state.history.at(-1).id;
  const page = bench(new URLSearchParams({entry: id}), state), tree = page.render();
  const action = link(tree, `inspect:${id}:workplan`), hooks = new Hooks(action.props.to, action.props.state);
  const destination = hooks.render(App); hooks.flush();
  assert.ok(find(destination, node => node.props.className === 'hs-return-context'));
  assert.equal(find(destination, node => node.props.className === 'wl-return-context'), undefined);
});

test('App exposes a failed browser save, keeps the new event in this tab and retries without duplicating it', () => {
  const setItem = globalThis.localStorage.setItem;
  let failed = false;
  globalThis.localStorage.setItem = (key, value) => {
    if (failed && key === 'gtm-workflow-demo-v1') throw new Error('Storage full');
    setItem(key, value);
  };
  const hooks = new Hooks(historyFixture.paths.workplan);
  let tree = hooks.render(App); hooks.flush();
  tree = hooks.render(App); hooks.flush();
  assert.equal(find(tree, node => node.props.className === 'save-indicator').props['data-save-status'], 'saved');
  find(find(tree, node => node.props.className === 'action-buttons'), node => node.props.primary).props.onClick();
  tree = hooks.render(App); hooks.flush();
  failed = true;
  named(tree, 'ReviewDecision').props.onPerform('request_review');
  tree = hooks.render(App); hooks.flush();
  hooks.navigate(historyHref()); tree = hooks.render(App); hooks.flush();
  const savedInTab = named(tree, 'History').props.state;
  assert.equal(savedInTab.history.length, 1);
  assert.equal(JSON.parse(storage.get('gtm-workflow-demo-v1')).history.length, 0);
  assert.equal(find(tree, node => node.props.className === 'save-indicator').props['data-save-status'], 'failed');
  const notice = find(tree, node => node.props.className === 'save-failure-notice');
  assert.equal(notice.props.role, 'alert');
  failed = false;
  find(notice, node => node.type === 'button').props.onClick();
  tree = hooks.render(App); hooks.flush();
  assert.equal(find(tree, node => node.props.className === 'save-indicator').props['data-save-status'], 'saved');
  assert.equal(find(tree, node => node.props.className === 'save-failure-notice'), undefined);
  assert.deepEqual(JSON.parse(storage.get('gtm-workflow-demo-v1')).history.map(event => event.id), savedInTab.history.map(event => event.id));
  assert.equal(named(tree, 'History').props.state, savedInTab);
});

test('History details use a named section and description lists contain terms and definitions only', () => {
  const state = complete(), id = state.history.at(-1).id, page = bench(new URLSearchParams({entry: id}), state), tree = page.render();
  const proof = find(tree, node => node.props.className === 'hs-proof');
  assert.equal(proof.type, 'section');
  assert.ok(proof.props['aria-label']);
  const clocks = find(proof, node => node.props.className === 'hs-clocks');
  for (const group of clocks.props.children) {
    assert.equal(group.type, 'div');
    assert.ok(group.props.children.every(node => ['dt', 'dd'].includes(node.type)));
  }
  assert.equal(find(tree, node => node.props.className === 'hs-view-context').props.role, undefined);
  assert.equal(find(tree, node => node.props.className === 'hs-result-count').props.role, 'status');
});
