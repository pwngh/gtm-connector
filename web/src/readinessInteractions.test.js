import {eagerRouteModules, expandRouteContent} from './testing/routeModules.js';
import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {fixturePeople, initialState, readinessFixture, readinessEvidenceFixture, walkthroughDecisions, walkthroughDocumentExamples} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, readinessHref, readinessStorageKey, selectReadiness} from './readiness.js';

// Controlled hooks exercise the real navigation and focus handlers.
let server, Readiness, App, CustomerDecisionForm;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect', 'useMemo'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [eagerRouteModules(), {
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
  CustomerDecisionForm = (await server.ssrLoadModule('/src/CustomerDecisionForm.jsx')).default;
});
after(async () => {await server?.close();});
let originals, harnesses, frames, scrolls, storage, storageWrites;
const globals = ['window', 'document', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame', '__readinessHooks'];
beforeEach(() => {
  originals = Object.fromEntries(globals.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  harnesses = []; frames = []; scrolls = []; storage = new Map(); storageWrites = [];
  globalThis.window = Object.assign(new EventTarget(), {location: new URL('http://local.test'), matchMedia: () => ({matches: false, addEventListener() {}, removeEventListener() {}}), scrollTo: options => scrolls.push(options)});
  globalThis.document = Object.assign(new EventTarget(), {documentElement: {dataset: {}}, body: {style: {}}, querySelector: () => null, getElementById: () => null, activeElement: null});
  globalThis.localStorage = {getItem: key => storage.get(key) || null, setItem: (key, value) => {storage.set(key, value); storageWrites.push(key);}};
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
  useMemo(create, deps) {
    const slot = this.slot('memo', () => ({}));
    if (!slot.deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {
      slot.value = create(); slot.deps = deps; this.memoComputations = (this.memoComputations || 0) + 1;
    }
    return slot.value;
  }
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
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__readinessHooks = this; return expandRouteContent(Component(props));}
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
  const targets = [], controls = [], entries = [], headings = new Map();
  hooks.elements.forEach(element => {element.isConnected = false;});
  for (const node of nodes(tree)) {
    const focusKey = node.props['data-readiness-focus'], targetKey = node.props['data-readiness-target'];
    const id = node.props.id;
    if (!focusKey && !targetKey && !id) continue;
    const key = focusKey ? `readinessFocus:${focusKey}` : targetKey ? `readinessTarget:${targetKey}` : `id:${id}`;
    if (!hooks.elements.has(key)) hooks.elements.set(key, element(key));
    const dom = hooks.elements.get(key);
    dom.isConnected = true;
    dom.getClientRects = () => node.props.name === 'evaluation' ? [] : [{}];
    if (focusKey) {dom.dataset.readinessFocus = focusKey; controls.push(dom);}
    if (targetKey) {dom.dataset.readinessTarget = targetKey; targets.push(dom); hooks.elements.set(`readinessTarget:${targetKey}`, dom);}
    if (node.props['data-readiness-entry']) {dom.dataset.readinessEntry = true; entries.push(dom);}
    if (id) headings.set(id, dom);
  }
  if (!hooks.elements.has('heading')) hooks.elements.set('heading', element('heading'));
  hooks.elements.get('heading').isConnected = true;
  tree.props.ref.current = {
    querySelectorAll: selector => selector === '[data-readiness-target]' ? targets : selector === '[data-readiness-entry]' ? entries : controls,
    querySelector: selector => headings.get(selector.slice(1)),
  };
  const heading = find(tree, node => node.type === 'h2' && node.props.ref);
  heading.props.ref.current = hooks.elements.get('heading');
}

function bench(search = '', state = initialState, metadata = null, fixture = readinessFixture) {
  const hooks = new Hooks(readinessHref(search), metadata), commands = [];
  let source = advanceReadinessJournal(null, state, fixture);
  const page = {hooks, commands, source: () => source, revalidate: state => {source = advanceReadinessJournal(source, state, fixture);}, render() {const tree = expand(hooks.render(Readiness, {journal: source, onAction: command => commands.push(command)})); attach(tree, hooks); hooks.flush(); return tree;}};
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

test('selecting a requirement focuses its keyboard entry and Escape returns to its initiating link', () => {
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
  controlElement(page.hooks, 'entry:check:verify_document').calls = [];
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
    assert.ok(controlElement(page.hooks, 'response-summary').calls.some(call => call[0] === 'focus'));
    assert.ok([...nodes(tree)].some(node => node.props.className === 'rd-button rd-primary'));
  }
  assert.deepEqual(page.source(), before);
  assert.deepEqual(page.commands, []);
});

test('empty requirement recovery changes only its filter, preserves selection, and focuses the filter', () => {
  const page = bench(new URLSearchParams({requirement: initialState.requirement.id, order: 'blockers'}), complete(), null, readinessEvidenceFixture); let tree = page.render();
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

test('the resolved walkthrough offers a reopen form separately from the still-open evidence correction', () => {
  const page = bench('requirements=all', complete()); let tree = page.render();
  const button = focusLink(tree, `requirement:${initialState.requirement.id}:reopen`);
  assert.equal(button.type, 'button');
  assert.deepEqual(page.commands, []);
  button.props.onClick();
  assert.deepEqual(page.commands, ['reopen_requirement']);
  assert.equal(page.source().current.requirements[0].recordedStatus, 'resolved');
  assert.equal(page.source().current.requirements[0].status, 'open');
});

test('after selection, arrow keys operate from the focused check entry without changing URL selection', () => {
  const page = bench('check=verify_document'); let tree = page.render();
  const selected = targetElement(page.hooks, 'check:verify_document');
  assert.equal(globalThis.document.activeElement, selected);
  assert.equal(selected, controlElement(page.hooks, 'entry:check:verify_document'));
  const list = find(tree, node => node.type === 'ol' && node.props.onKeyDown);
  const entries = [...nodes(list)].filter(node => node.props['data-readiness-entry']);
  const currentTarget = {querySelectorAll: () => entries.map(node => controlElement(page.hooks, node.props['data-readiness-focus']))};
  const before = page.hooks.entries.length;
  const event = {key: 'ArrowDown', target: selected, currentTarget, preventDefault() {this.prevented = true;}};
  list.props.onKeyDown(event);
  assert.equal(event.prevented, true);
  assert.equal(globalThis.document.activeElement, controlElement(page.hooks, 'entry:check:request_customer_review'));
  assert.equal(page.hooks.entries.length, before);
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'verify_document');
  open(page.hooks, focusLink(tree, 'entry:check:request_customer_review')); tree = page.render();
  assert.equal(globalThis.document.activeElement, targetElement(page.hooks, 'check:request_customer_review'));
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'request_customer_review');
});

test('direct preview recovery focuses the visible disclosure summary, never its hidden select', () => {
  for (const evaluation of ['stale', 'not-evaluated']) {
    const page = bench(`evaluation=${evaluation}&check=verify_document`); let tree = page.render();
    const response = find(tree, node => node.props.className === 'rd-response');
    assert.equal(controlElement(page.hooks, 'response-control').getClientRects().length, 0);
    find(response, node => node.type === 'button').props.onClick(); tree = page.render();
    assert.equal(globalThis.document.activeElement, controlElement(page.hooks, 'response-summary'));
    assert.equal(controlElement(page.hooks, 'response-control').calls.length, 0);
    assert.equal(page.hooks.useSearchParams()[0].get('check'), 'verify_document');
    assert.equal(page.hooks.useSearchParams()[0].has('evaluation'), false);
    assert.deepEqual(page.commands, []);
  }
});

test('comparison and header jumps focus their sections without creating hash history or losing opener state', () => {
  const page = bench(); let tree = page.render();
  const openerKey = `requirement:${initialState.requirement.id}:missing:document_receipt`;
  open(page.hooks, focusLink(tree, openerKey)); tree = page.render();
  const metadata = page.hooks.useLocation().state, length = page.hooks.entries.length;
  const comparison = find(tree, node => node.props.className === 'rd-evaluation-line');
  const jump = find(comparison, node => node.type === 'button');
  assert.equal(jump.props.href, undefined);
  jump.props.onClick();
  assert.equal(globalThis.document.activeElement, page.hooks.elements.get('id:readiness-changes'));
  assert.equal(page.hooks.entries.length, length);
  assert.equal(page.hooks.useLocation().state, metadata);
  assert.equal(page.hooks.useLocation().hash, '');
  controlElement(page.hooks, openerKey).calls = [];
  escape(tree); page.render();
  assert.equal(controlElement(page.hooks, openerKey).calls[0][0], 'focus');

  const hooks = new Hooks(readinessHref('check=verify_document'), metadata), target = element('coverage');
  globalThis.document.getElementById = id => id === 'readiness-coverage' ? target : null;
  tree = hooks.render(App); hooks.flush();
  const heading = find(tree, node => node.props.className === 'heading-actions');
  const sources = find(heading, node => node.type === 'button');
  sources.props.onClick();
  assert.equal(globalThis.document.activeElement, target);
  assert.equal(target.calls[1][0], 'scroll');
  assert.equal(hooks.entries.length, 1);
  assert.equal(hooks.useLocation().state, metadata);
  assert.equal(hooks.useLocation().hash, '');
});

test('the authority corrective step selects its missing check, preserves filters, and executes no decision', () => {
  const state = complete();
  const page = bench('check=resolve_requirement&order=blockers&checkState=failed', {...state, requirement: {...state.requirement, status: 'open', resolvedAt: null}});
  let tree = page.render();
  const action = focusLink(tree, 'next-action');
  assert.equal(new URL(action.props.to, 'http://local.test').pathname, readinessFixture.paths.readiness);
  open(page.hooks, action); tree = page.render();
  assert.equal(page.hooks.useSearchParams()[0].get('check'), 'customer_authority');
  assert.equal(page.hooks.useSearchParams()[0].get('order'), 'blockers');
  assert.equal(page.hooks.useSearchParams()[0].get('checkState'), 'failed');
  assert.equal(globalThis.document.activeElement, targetElement(page.hooks, 'check:customer_authority'));
  assert.equal(page.source().current.requirements[0].waitingOn.id, null);
  assert.deepEqual(page.commands, []);
  escape(tree); page.render();
  assert.equal(globalThis.document.activeElement, controlElement(page.hooks, 'next-action'));
});

function pendingReplacement() {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) {
    state = transitionWalkthrough(state, {type, ...(type === 'obtain_document' ? {document: walkthroughDocumentExamples.replacement} : {})});
  }
  return state;
}

test('every decision-form opener starts with explicit blank input for the active replacement review', () => {
  const state = pendingReplacement(), reviewItem = readinessFixture.worklist.workItems.find(item => item.kind === 'customer_review');
  for (const opener of ['next-action', 'exact-work-item', 'step']) {
    storage.set('gtm-workflow-demo-v1', JSON.stringify(state));
    const path = opener === 'exact-work-item' ? `${readinessFixture.paths.workplan}?workItem=${reviewItem.id}` : readinessFixture.paths.workplan;
    const hooks = new Hooks(path); let tree = hooks.render(App); hooks.flush();
    tree = hooks.render(App); hooks.flush();
    if (opener === 'next-action') {
      const actions = find(tree, node => node.props.className === 'action-buttons');
      find(actions, node => node.props.primary).props.onClick();
    } else if (opener === 'exact-work-item') {
      const entry = named(tree, 'WorklistStepEntry');
      assert.equal(entry.props.item.workflowAction.command, 'record_customer_approval');
      entry.props.onAction(entry.props.item.workflowAction.command);
    } else {
      named(tree, 'WorkProgress').props.onSelect(3);
      tree = hooks.render(App); hooks.flush();
      const step = find(tree, node => node.type?.name === 'Step' && node.props.num === 3);
      find(step, node => node.type?.name === 'Button').props.onClick();
    }
    tree = hooks.render(App); hooks.flush();
    const formNode = named(tree, 'CustomerDecisionForm');
    assert.ok(formNode);
    assert.equal(formNode.props.input.documentVersion, walkthroughDocumentExamples.replacement.version);
    assert.equal(formNode.props.input.documentId, state.document.id);
    const form = CustomerDecisionForm(formNode.props);
    assert.equal(field(form, 'evidenceText').props.value, '');
    assert.equal(field(form, 'reviewedBy').props.type, 'text');
    assert.equal(field(form, 'reviewedBy').props.value, '');
    assert.equal(field(form, 'confirmedVersion').props.checked, false);
    assert.equal(find(form, node => node.props.type === 'submit').props.disabled, true);
    form.props.onSubmit({preventDefault() {}});
    tree = hooks.render(App); hooks.flush();
    assert.equal(named(tree, 'CustomerDecisionForm').props.state.customerReview.status, 'pending');
  }
});

test('the actual decision form refuses blank input and records only explicitly confirmed input for the active version', () => {
  const state = pendingReplacement(), typed = walkthroughDocumentExamples.replacementDecision;
  storage.set('gtm-workflow-demo-v1', JSON.stringify(state));
  const hooks = new Hooks(readinessFixture.paths.workplan);
  let tree = hooks.render(App); hooks.flush();
  const actionBar = find(tree, node => node.props.className === 'action-buttons');
  find(actionBar, node => node.props.primary).props.onClick();
  const renderForm = () => {tree = hooks.render(App); hooks.flush(); return CustomerDecisionForm(named(tree, 'CustomerDecisionForm').props);};
  let form = renderForm();
  field(form, 'evidenceText').props.onChange({target: {value: typed.evidenceText}}); form = renderForm();
  assert.equal(find(form, node => node.props.type === 'submit').props.disabled, true);
  field(form, 'reviewedBy').props.onChange({target: {value: typed.reviewedBy}}); form = renderForm();
  assert.equal(find(form, node => node.props.type === 'submit').props.disabled, true);
  form.props.onSubmit({preventDefault() {}}); form = renderForm();
  assert.equal(named(tree, 'CustomerDecisionForm').props.state.history.length, state.history.length);
  field(form, 'confirmedVersion').props.onChange({target: {checked: true}}); form = renderForm();
  assert.equal(find(form, node => node.props.type === 'submit').props.disabled, false);
  form.props.onSubmit({preventDefault() {}});
  tree = hooks.render(App); hooks.flush();
  assert.equal(named(tree, 'CustomerDecisionForm'), undefined);
  const saved = JSON.parse(storage.get('gtm-workflow-demo-v1'));
  assert.equal(saved.customerReview.documentVersion, state.customerReview.documentVersion);
  assert.equal(saved.customerReview.documentId, state.document.id);
  assert.equal(saved.customerReview.evidenceText, typed.evidenceText);
  assert.equal(saved.customerReview.reviewedBy, typed.reviewedBy);
  assert.equal(saved.history.at(-1).evidenceSnapshot.documentVersion, walkthroughDocumentExamples.replacement.version);
  assert.equal(saved.history.length, state.history.length + 1);
  assert.equal(saved.requirement.status, 'open');
});

test('unrelated App renders neither re-evaluate readiness nor write its unchanged journal to storage', () => {
  const hooks = new Hooks(readinessHref('check=submit_plan'));
  let tree = hooks.render(App); hooks.flush();
  tree = hooks.render(App); hooks.flush();
  const source = named(tree, 'Readiness').props.journal, computations = hooks.memoComputations;
  const writes = storageWrites.filter(key => key === readinessStorageKey).length;
  find(tree, node => node.props.className === 'icon-button theme-toggle').props.onClick();
  tree = hooks.render(App); hooks.flush();
  find(tree, node => node.props.className === 'profile').props.onClick();
  tree = hooks.render(App); hooks.flush();
  named(tree, 'WorkspaceDialog').props.onClose();
  tree = hooks.render(App); hooks.flush();
  assert.equal(named(tree, 'Readiness').props.journal, source);
  assert.equal(hooks.memoComputations, computations);
  assert.equal(storageWrites.filter(key => key === readinessStorageKey).length, writes);
  assert.equal(JSON.parse(storage.get(readinessStorageKey)).current.inputKey.length, 16);
});

test('evidence checks are a sibling section, not counted or nested as transition checks', () => {
  const tree = bench().render();
  const transitions = find(tree, node => node.props.className === 'rd-transition-section');
  const evidence = find(tree, node => node.props.className === 'rd-evidence-checks');
  const parent = find(tree, node => node.props.className === 'rd-check-column');
  assert.ok(evidence);
  assert.equal(find(transitions, node => node === evidence), undefined);
  assert.ok(find(parent, node => node === evidence));
  assert.ok(find(parent, node => node === transitions));
  assert.equal(checks(transitions).length, 7);
  assert.equal(checks(evidence).length, 3);
});
