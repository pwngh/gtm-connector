import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {initialState, readinessFixture, walkthroughDecisions} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, readinessHref, readinessStorageKey, selectReadiness} from './readiness.js';

// Controlled hooks exercise the real navigation and focus handlers.
let server, Readiness, App;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [{
    name: 'readiness-handlers', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:rd-hooks-')) return `\0${id}`;},
    load(id) {
      if (id === '\0virtual:rd-hooks-react') return `import React from 'react'; export default React; ${hookNames.map(name => `export const ${name}=(...args)=>globalThis.__readinessHooks.${name}(...args);`).join('\n')}`;
      if (id === '\0virtual:rd-hooks-router') return `export * from 'react-router'; ${['useSearchParams', 'useNavigate', 'useLocation'].map(name => `export const ${name}=(...args)=>globalThis.__readinessHooks.${name}(...args);`).join('\n')}`;
    },
    transform(code, id) {
      if (/\/src\/(Readiness|main)\.jsx$/.test(id)) return code.replace("from 'react';", "from 'virtual:rd-hooks-react';").replace("from 'react-router';", "from 'virtual:rd-hooks-router';");
    },
  }]});
  Readiness = (await server.ssrLoadModule('/src/Readiness.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});
let originals, harnesses, frames, scrolls, storage;
const globals = ['window', 'document', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame', '__readinessHooks'];
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
  for (const name of globals) {
    if (originals[name]) Object.defineProperty(globalThis, name, originals[name]); else delete globalThis[name];
  }
});

class Hooks {
  constructor(path = readinessFixture.paths.readiness, state = null) {
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
  useRef(value) {return this.slot('ref', () => ({ref: {current: value}})).ref;}
  useId() {return this.slot('id', () => ({id: `rd-test-${this.cursor}`})).id;}
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
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__readinessHooks = this; return Component(props);}
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
const field = (tree, name) => find(tree, node => node.props.name === name);
const text = node => Array.isArray(node) ? node.map(text).join('') : node?.props ? text(node.props.children) : typeof node === 'string' || typeof node === 'number' ? String(node) : '';
const inspectable = new Set(['Requirement', 'CheckItem', 'UnitInspector', 'Coverage', 'EvaluationChanges', 'ResultBadge', 'UnitContent', 'InputVersions']);
function expand(node) {
  if (Array.isArray(node)) return node.map(expand);
  if (!node?.props) return node;
  if (inspectable.has(node.type?.name)) return expand(node.type(node.props));
  return {...node, props: {...node.props, children: expand(node.props.children)}};
}
function element(key, dataset = {}) {
  return {dataset, calls: [], tabIndex: 0, isConnected: true, closest: () => null,
    focus(options) {this.calls.push(['focus', options]); globalThis.document.activeElement = this;},
    scrollIntoView(options) {this.calls.push(['scroll', options]);}, key};
}
function attach(tree, hooks) {
  const targets = [], controls = [];
  for (const node of nodes(tree)) {
    for (const [attribute, dataKey, list] of [['data-readiness-focus', 'readinessFocus', controls], ['data-readiness-target', 'readinessTarget', targets]]) {
      const value = node.props[attribute];
      if (!value) continue;
      const key = `${dataKey}:${value}`;
      if (!hooks.elements.has(key)) hooks.elements.set(key, element(key, {[dataKey]: value}));
      list.push(hooks.elements.get(key));
    }
  }
  if (!hooks.elements.has('heading')) hooks.elements.set('heading', element('heading'));
  tree.props.ref.current = {querySelectorAll: selector => selector === '[data-readiness-target]' ? targets : controls};
  const heading = find(tree, node => node.type === 'h2' && node.props.ref);
  heading.props.ref.current = hooks.elements.get('heading');
}
function bench(search = '', state = initialState, metadata = null) {
  const hooks = new Hooks(readinessHref(search), metadata), commands = [];
  let source = advanceReadinessJournal(null, state);
  const page = {hooks, commands, source: () => source, revalidate: state => {source = advanceReadinessJournal(source, state);}, render() {const tree = expand(hooks.render(Readiness, {journal: source, onAction: command => commands.push(command)})); attach(tree, hooks); hooks.flush(); return tree;}};
  return page;
}
const open = (hooks, link) => hooks.navigate(link.props.to, {state: link.props.state});
const focusLink = (tree, key) => find(tree, node => node.props['data-readiness-focus'] === key);
const controlElement = (hooks, key) => hooks.elements.get(`readinessFocus:${key}`);
const targetElement = (hooks, key) => hooks.elements.get(`readinessTarget:${key}`);
const escape = tree => {const event = {key: 'Escape', preventDefault() {this.prevented = true;}, stopPropagation() {this.stopped = true;}}; tree.props.onKeyDown(event); assert.equal(event.prevented, true);};
const checks = tree => [...nodes(tree)].filter(node => node.props['data-check-id']).map(node => node.props['data-check-id']);

function complete() {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', 'record_customer_approval', 'resolve_requirement']) {
    state = transitionWalkthrough(state, {type, reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
  }
  return state;
}

test('result filtering and ordering use actual URL history and restore controls and rows on Back and Forward', () => {
  const page = bench(); let tree = page.render();
  field(tree, 'checkState').props.onChange({target: {value: 'failed'}}); tree = page.render();
  assert.equal(checks(tree).includes('submit_plan'), false);
  assert.equal(field(tree, 'checkState').props.value, 'failed');
  field(tree, 'order').props.onChange({target: {value: 'blockers'}}); tree = page.render();
  assert.equal(field(tree, 'order').props.value, 'blockers');
  assert.notEqual(page.hooks.lastOptions.replace, true);
  page.hooks.back(); tree = page.render(); assert.equal(field(tree, 'order').props.value, 'workflow');
  page.hooks.back(); tree = page.render(); assert.equal(checks(tree)[0], 'submit_plan');
  assert.equal(field(tree, 'checkState').props.value, 'all');
  page.hooks.forward(); tree = page.render(); assert.equal(checks(tree).includes('submit_plan'), false);
});

test('selecting a requirement focuses its exact heading and Escape returns to its initiating link', () => {
  const page = bench(), key = `requirement:${initialState.requirement.id}`; let tree = page.render();
  open(page.hooks, focusLink(tree, `entry:${key}`)); tree = page.render();
  assert.equal(targetElement(page.hooks, key).calls[0][0], 'focus');
  assert.equal(targetElement(page.hooks, key).calls[1][1].behavior, 'instant');
  escape(tree); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].has('requirement'), false);
  assert.equal(controlElement(page.hooks, `entry:${key}`).calls[0][0], 'focus');
});

test('Back between check selections restores each selection’s own opener, including requirement-to-check links', () => {
  const page = bench(); let tree = page.render();
  const firstKey = `requirement:${initialState.requirement.id}:missing:document_receipt`;
  open(page.hooks, focusLink(tree, firstKey)); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'receive_document');
  open(page.hooks, focusLink(tree, 'entry:check:verify_document')); tree = page.render();
  page.hooks.back(); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'receive_document');
  escape(tree); page.render();
  assert.equal(controlElement(page.hooks, firstKey).calls[0][0], 'focus');
  assert.equal(controlElement(page.hooks, 'entry:check:verify_document').calls.length, 0);
});

test('reload restores exact check, filter and order; closing a filter-hidden selection focuses its surviving opener', () => {
  const origin = {worklistOpener: {selection: `requirement:${initialState.requirement.id}`, focusKey: 'original-row'}};
  const page = bench('order=blockers', initialState, origin); let tree = page.render();
  open(page.hooks, focusLink(tree, `requirement:${initialState.requirement.id}:missing:document_receipt`)); tree = page.render();
  field(tree, 'checkState').props.onChange({target: {value: 'passed'}}); tree = page.render();
  const params = page.hooks.useSearchParams()[0];
  assert.equal(selectReadiness(page.source(), params).selection.outside, true);
  const reload = bench(params, initialState, page.hooks.useLocation().state); tree = reload.render();
  assert.equal(field(tree, 'checkState').props.value, 'passed');
  assert.equal(field(tree, 'order').props.value, 'blockers');
  assert.equal(targetElement(reload.hooks, 'check:receive_document').calls[0][0], 'focus');
  escape(tree); reload.render();
  assert.equal(controlElement(reload.hooks, `requirement:${initialState.requirement.id}:missing:document_receipt`).calls[0][0], 'focus');
  assert.deepEqual(reload.hooks.useLocation().state.worklistOpener, origin.worklistOpener);
});

test('filter and sort changes preserve selection without stealing focus back to the selected heading', () => {
  const page = bench('check=verify_document'); let tree = page.render();
  const selected = targetElement(page.hooks, 'check:verify_document'); selected.calls = [];
  field(tree, 'checkState').props.onChange({target: {value: 'passed'}}); tree = page.render();
  field(tree, 'order').props.onChange({target: {value: 'blockers'}}); tree = page.render();
  assert.equal(selected.calls.length, 0);
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'verify_document');
  assert.ok(checks(tree).includes('verify_document'));
});

test('source-unit selection retains its parent requirement and both Escape and Back restore the citation opener', () => {
  for (const closeWithBack of [false, true]) {
    const page = bench(new URLSearchParams({requirement: initialState.requirement.id})); let tree = page.render();
    const citationKey = `requirement:${initialState.requirement.id}:unit:1`;
    open(page.hooks, focusLink(tree, citationKey)); tree = page.render();
    const key = `unit:${readinessFixture.card.id}:1`;
    assert.equal(targetElement(page.hooks, key).calls[0][0], 'focus');
    assert.equal(page.hooks.useSearchParams()[0].get('requirement'), initialState.requirement.id);
    if (closeWithBack) page.hooks.back(); else escape(tree);
    tree = page.render();
    assert.equal(page.hooks.useSearchParams()[0].has('unit'), false);
    assert.equal(page.hooks.useSearchParams()[0].get('requirement'), initialState.requirement.id);
    assert.equal(controlElement(page.hooks, citationKey).calls[0][0], 'focus');
  }
});

test('response controls withhold executable entries and recover to their own control without altering evaluations', () => {
  const page = bench(), before = structuredClone(page.source()); let tree = page.render();
  for (const value of ['not-evaluated', 'stale']) {
    field(tree, 'evaluation').props.onChange({target: {value}}); tree = page.render();
    assert.equal([...nodes(tree)].filter(node => node.props.className === 'rd-button rd-primary').length, 0);
    const response = find(tree, node => node.props.className === 'rd-response');
    find(response, node => node.type === 'button').props.onClick(); tree = page.render();
    assert.equal(page.hooks.useSearchParams()[0].has('evaluation'), false);
    assert.ok(controlElement(page.hooks, 'response-control').calls.some(call => call[0] === 'focus'));
    assert.ok([...nodes(tree)].some(node => node.props.className === 'rd-button rd-primary'));
  }
  assert.deepEqual(page.source(), before);
  assert.deepEqual(page.commands, []);
});

test('empty requirement recovery changes only its filter, preserves selection, and focuses the filter', () => {
  const page = bench(new URLSearchParams({requirement: initialState.requirement.id, order: 'blockers'}), complete()); let tree = page.render();
  assert.equal(selectReadiness(page.source(), page.hooks.useLocation().search).visibleRequirements.length, 0);
  const empty = find(tree, node => node.props.className === 'rd-empty rd-paper');
  find(empty, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(field(tree, 'requirements').props.value, 'all');
  assert.equal(page.hooks.useSearchParams()[0].get('requirement'), initialState.requirement.id);
  assert.equal(page.hooks.useSearchParams()[0].get('order'), 'blockers');
  assert.equal(controlElement(page.hooks, 'requirement-filter').calls[0][0], 'focus');
});

test('unknown selection recovery does not select another item and returns focus to the page heading', () => {
  const page = bench('check=missing'); let tree = page.render();
  const error = find(tree, node => node.props.className === 'rd-selection-error rd-paper');
  assert.equal(targetElement(page.hooks, 'check:missing').calls[0][0], 'focus');
  find(error, node => node.type === 'button').props.onClick(); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].has('check'), false);
  assert.equal(page.hooks.elements.get('heading').calls[0][0], 'focus');
  assert.equal([...nodes(tree)].filter(node => node.props['aria-current'] === 'true').length, 0);
});

test('returning from an exact corrective step restores its opener and all readiness and worklist query state', () => {
  const returnTo = '/queue?sort=changed&attention=blocked&selection=work-item%3Awork-plan-review-ai-terms';
  const page = bench(new URLSearchParams({check: 'submit_plan', checkState: 'passed', returnTo}), initialState, {worklistOpener: {focusKey: 'list-row'}});
  let tree = page.render();
  const action = focusLink(tree, 'check:submit_plan:action'), destination = new Hooks(action.props.to, action.props.state);
  tree = destination.render(App); destination.flush();
  const context = find(tree, node => node.props.className === 'rd-return-context'), back = find(context, node => node.props.to);
  assert.equal(new URL(back.props.to, 'http://local.test').searchParams.get('returnTo'), returnTo);
  assert.equal(back.props.state.readinessRestoreFocus, 'check:submit_plan:action');
  const returned = bench(new URL(back.props.to, 'http://local.test').search, initialState, back.props.state);
  tree = returned.render();
  assert.equal(controlElement(returned.hooks, 'check:submit_plan:action').calls[0][0], 'focus');
  assert.equal(targetElement(returned.hooks, 'check:submit_plan').calls.length, 0);
  assert.equal(returned.hooks.useLocation().state.worklistOpener.focusKey, 'list-row');
});

test('revalidation keeps URL selection and focuses the same item with its recomputed blockers', () => {
  const page = bench('check=verify_document'); let tree = page.render();
  const selected = targetElement(page.hooks, 'check:verify_document'); selected.calls = [];
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document']) state = transitionWalkthrough(state, {type});
  page.revalidate(state); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'verify_document');
  assert.equal(selected.calls[0][0], 'focus');
  const view = selectReadiness(page.source(), page.hooks.useLocation().search);
  assert.equal(view.selection.resource.result, 'passed');
  assert.equal(view.selection.resource.blockerCount, 0);
  assert.ok(focusLink(tree, 'check:verify_document:action'));
  assert.equal(page.source().previous.checks.find(item => item.id === 'verify_document').result, 'failed');
});

test('the main focus effect yields to direct requirement, check, source and invalid readiness selections', () => {
  for (const query of [`requirement=${initialState.requirement.id}`, 'check=verify_document', `card=${readinessFixture.card.id}&unit=1`, 'check=missing']) {
    const hooks = new Hooks(readinessFixture.paths.history);
    hooks.render(App); hooks.flush(); frames.length = 0; scrolls.length = 0;
    hooks.navigate(readinessHref(query));
    const tree = hooks.render(App); hooks.flush();
    assert.ok(named(tree, 'Readiness'));
    assert.deepEqual(frames, []);
    assert.deepEqual(scrolls, []);
  }
});

test('main evaluates changes made on Work plan, retains prior results, persists them, and restores them on reload', () => {
  const hooks = new Hooks(readinessHref('check=submit_plan')); let tree = hooks.render(App); hooks.flush();
  const before = structuredClone(named(tree, 'Readiness').props.journal.current);
  hooks.navigate(readinessFixture.paths.workplan); tree = hooks.render(App); hooks.flush();
  const actionBar = find(tree, node => node.props.className === 'action-buttons');
  find(actionBar, node => node.props.primary).props.onClick(); tree = hooks.render(App); hooks.flush();
  named(tree, 'ReviewDecision').props.onPerform('request_review'); tree = hooks.render(App); hooks.flush();
  hooks.navigate(readinessHref('check=submit_plan')); tree = hooks.render(App); hooks.flush();
  const current = named(tree, 'Readiness').props.journal;
  assert.deepEqual(current.previous, before);
  assert.equal(current.current.checks.find(item => item.id === 'submit_plan').result, 'not_applicable');
  assert.equal(current.changes.cause.id, JSON.parse(storage.get('gtm-workflow-demo-v1')).history.at(-1).id);
  assert.equal(JSON.parse(storage.get(readinessStorageKey)).current.evaluationId, current.current.evaluationId);
  const reload = new Hooks(readinessHref('check=submit_plan')); tree = reload.render(App); reload.flush();
  assert.deepEqual(named(tree, 'Readiness').props.journal, current);
});

test('the resolved requirement offers only the permitted reopen form, without running a transition on inspection', () => {
  const page = bench('requirements=all', complete()); let tree = page.render();
  const button = focusLink(tree, `requirement:${initialState.requirement.id}:action`);
  assert.equal(button.type, 'button');
  assert.deepEqual(page.commands, []);
  button.props.onClick();
  assert.deepEqual(page.commands, ['reopen_requirement']);
  assert.equal(page.source().current.requirements[0].status, 'resolved');
});
