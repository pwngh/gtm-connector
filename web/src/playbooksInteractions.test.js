import {eagerRouteModules, expandRouteContent} from './testing/routeModules.js';
import test, {before, after, beforeEach, afterEach} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
import {
  buildPlaybookContext, createPlaybooksSession, playbookSearch, playbooksSessionReducer,
} from './playbookDrafts.js';
import {initialState, transition} from './workflow.js';
import {renewalEvidence, renewalFixture, fixturePeople} from './renewalFixtures.js';
import {walkthroughCommands} from './customerDecision.js';
import {walkthroughInputs} from './testing/walkthroughInputs.js';

// Run real component handlers with controlled hooks, not a layout or browser emulator.
let server, Playbooks, App;
const hookNames = ['useState', 'useReducer', 'useRef', 'useId', 'useEffect', 'useLayoutEffect', 'useMemo'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [eagerRouteModules(), {
    name: 'playbooks-handler-harness', enforce: 'pre',
    resolveId(id) {if (id.startsWith('virtual:pb-test-')) return `\0${id}`;},
    load(id) {
      if (id === '\0virtual:pb-test-react') return `import React from 'react'; export default React; ${hookNames.map(name => `export const ${name}=(...args)=>globalThis.__playbooksTestHooks.${name}(...args);`).join('\n')}`;
      if (id === '\0virtual:pb-test-router') return `export * from 'react-router'; ${['useSearchParams', 'useNavigate', 'useLocation'].map(name => `export const ${name}=(...args)=>globalThis.__playbooksTestHooks.${name}(...args);`).join('\n')}`;
    },
    transform(code, id) {
      if (/\/src\/(Playbooks|main)\.jsx$/.test(id)) return code.replace("from 'react';", "from 'virtual:pb-test-react';").replace("from 'react-router';", "from 'virtual:pb-test-router';");
    },
  }]});
  Playbooks = (await server.ssrLoadModule('/src/Playbooks.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});

const globalNames = ['window', 'document', 'localStorage', 'confirm', 'requestAnimationFrame', 'cancelAnimationFrame', '__playbooksTestHooks'];
let originalGlobals, storage, harnesses;
beforeEach(() => {
  originalGlobals = Object.fromEntries(globalNames.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  harnesses = [];
  storage = new Map();
  globalThis.window = Object.assign(new EventTarget(), {
    matchMedia: () => ({matches: false, addEventListener() {}, removeEventListener() {}}),
    location: new URL('http://local.test/playbooks'), scrollTo() {},
  });
  globalThis.document = Object.assign(new EventTarget(), {
    documentElement: {dataset: {}}, body: {style: {}}, querySelector: () => null, getElementById: () => null, activeElement: null,
  });
  globalThis.localStorage = {getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value)};
  globalThis.confirm = () => true;
  globalThis.requestAnimationFrame = () => 1;
  globalThis.cancelAnimationFrame = () => {};
});
afterEach(() => {
  for (const harness of harnesses) harness.dispose();
  for (const name of globalNames) {
    if (originalGlobals[name]) Object.defineProperty(globalThis, name, originalGlobals[name]);
    else delete globalThis[name];
  }
});

class Hooks {
  constructor(path = '/playbooks', search = '') {
    this.slots = [];
    this.pathname = path;
    this.search = String(search);
    this.navigations = [];
    this.setParams = params => {this.search = String(params); this.navigations.push(`?${this.search}`);};
    this.navigate = path => {const url = new URL(path, 'http://local.test'); this.pathname = url.pathname; this.search = url.search.slice(1); this.navigations.push(path);};
    harnesses.push(this);
  }
  slot(kind, init) {
    const index = this.cursor++;
    this.slots[index] ||= {kind, ...init()};
    assert.equal(this.slots[index].kind, kind);
    return this.slots[index];
  }
  useState(initial) {
    const slot = this.slot('state', () => ({value: typeof initial === 'function' ? initial() : initial}));
    slot.set ||= value => {slot.value = typeof value === 'function' ? value(slot.value) : value;};
    return [slot.value, slot.set];
  }
  useReducer(reducer, initial, init) {
    const slot = this.slot('reducer', () => ({value: init ? init(initial) : initial}));
    slot.reducer = reducer;
    slot.dispatch ||= action => {slot.value = slot.reducer(slot.value, action);};
    return [slot.value, slot.dispatch];
  }
  useMemo(create, deps) {
    const slot = this.slot('memo', () => ({}));
    if (!slot.deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {
      slot.value = create(); slot.deps = deps;
    }
    return slot.value;
  }
  useRef(value) {return this.slot('ref', () => ({ref: {current: value}})).ref;}
  useId() {return this.slot('id', () => ({id: `test-id-${this.cursor}`})).id;}
  effect(kind, effect, deps) {
    const slot = this.slot(kind, () => ({}));
    if (!slot.deps || !deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {
      slot.deps = deps;
      this.pending[kind].push(() => {slot.cleanup?.(); slot.cleanup = effect();});
    }
  }
  useEffect(effect, deps) {this.effect('effect', effect, deps);}
  useLayoutEffect(effect, deps) {this.effect('layout', effect, deps);}
  useSearchParams() {return [new URLSearchParams(this.search), this.setParams];}
  useNavigate() {return this.navigate;}
  useLocation() {return {pathname: this.pathname, search: this.search ? `?${this.search}` : ''};}
  render(Component, props) {
    this.cursor = 0;
    this.pending = {layout: [], effect: []};
    globalThis.__playbooksTestHooks = this;
    return expandRouteContent(Component(props));
  }
  flushLayout() {for (const callback of this.pending.layout.splice(0)) callback();}
  flushEffects() {for (const callback of this.pending.effect.splice(0)) callback();}
  dispose() {for (const slot of this.slots) slot.cleanup?.();}
}

function* elements(node) {
  if (Array.isArray(node)) {for (const child of node) yield* elements(child);}
  else if (node && typeof node === 'object' && node.props) {yield node; yield* elements(node.props.children);}
}
const find = (tree, predicate) => [...elements(tree)].find(predicate);
const named = (tree, name) => find(tree, node => node.type?.name === name);
const text = node => Array.isArray(node) ? node.map(text).join('') : node?.props ? text(node.props.children) : typeof node === 'string' || typeof node === 'number' ? String(node) : '';
const button = (tree, label) => find(tree, node => (node.type === 'button' || node.type?.name === 'Button') && text(node) === label);
const at = '2026-11-20T06:00:00.000Z';
const templateId = 'obtain_current_rider';
const target = () => buildPlaybookContext(initialState, renewalEvidence);
const preparation = context => ({type: 'prepare', templateId, context, confirmed: true, at});
function workbench(view = 'context', prepared = false) {
  let session = createPlaybooksSession('handler-test');
  if (prepared) session = playbooksSessionReducer(session, preparation(target()));
  const hooks = new Hooks('/playbooks', playbookSearch(templateId, view, session.draft?.id));
  const actions = [];
  const dispatch = action => {actions.push(action); session = playbooksSessionReducer(session, action);};
  return {hooks, actions, dispatch, get session() {return session;}, render: () => hooks.render(Playbooks, {workflowState: initialState, evidence: renewalEvidence, session, dispatch})};
}

function finishedWorkflow() {
  let state = initialState;
  for (const type of walkthroughCommands.slice(0, -1)) state = transition(state, {type, ...walkthroughInputs(state, type)});
  return transition(state, {type: 'resolve_requirement'});
}

test('main.jsx passes only workflow context and the isolated draft dispatcher to Playbooks', () => {
  const workflow = finishedWorkflow();
  storage.set('gtm-workflow-demo-v1', JSON.stringify(workflow));
  const hooks = new Hooks();
  let tree = hooks.render(App);
  hooks.flushEffects();
  let props = named(tree, 'Playbooks').props;
  assert.deepEqual(Object.keys(props).sort(), ['dispatch', 'evidence', 'session', 'workflowState']);
  assert.equal(props.session.draft, null);
  const approvedWork = props.workflowState;
  const before = structuredClone(approvedWork);
  props.dispatch(preparation(buildPlaybookContext(approvedWork, props.evidence)));
  tree = hooks.render(App);
  props = named(tree, 'Playbooks').props;
  const draftId = props.session.draft.id;
  assert.ok(draftId);
  props.dispatch({type: 'edit_field', field: 'title', value: 'Local draft only'});
  props.dispatch({type: 'save_revision', expectedRevision: 1, at});
  for (const type of ['save_plan', 'request_review', 'approve_plan', 'resolve_requirement']) props.dispatch({type});
  tree = hooks.render(App);
  hooks.flushEffects();
  props = named(tree, 'Playbooks').props;
  assert.equal(props.session.draft.id, draftId);
  assert.equal(props.session.draft.revision, 2);
  assert.equal(props.session.draft.title, 'Local draft only');
  assert.equal(props.workflowState, approvedWork);
  assert.deepEqual(props.workflowState, before);
  assert.deepEqual(JSON.parse(storage.get('gtm-workflow-demo-v1')), before);
  assert.ok([...storage.keys()].every(key => !key.includes('playbook')));
});

test('main.jsx reset clears retained drafts and buffers as well as the walkthrough', () => {
  const hooks = new Hooks();
  let tree = hooks.render(App);
  let props = named(tree, 'Playbooks').props;
  props.dispatch(preparation(target()));
  props.dispatch({type: 'edit_field', field: 'title', value: 'Unsaved title to reset'});
  tree = hooks.render(App);
  const oldId = named(tree, 'Playbooks').props.session.draft.id;
  button(tree, ' Reset walkthrough').props.onClick();
  tree = hooks.render(App);
  button(tree, 'Reset demo').props.onClick();
  assert.equal(hooks.pathname, renewalFixture.paths.workplan);
  hooks.pathname = '/playbooks';
  tree = hooks.render(App);
  props = named(tree, 'Playbooks').props;
  assert.equal(props.session.draft, null);
  assert.equal(props.session.editor, null);
  props.dispatch(preparation(target()));
  tree = hooks.render(App);
  assert.notEqual(named(tree, 'Playbooks').props.session.draft.id, oldId);
});

test('main.jsx keeps the beforeunload warning active for edits retained on another page', () => {
  const hooks = new Hooks();
  let tree = hooks.render(App);
  hooks.flushEffects();
  const props = named(tree, 'Playbooks').props;
  props.dispatch(preparation(target()));
  props.dispatch({type: 'edit_field', field: 'title', value: 'Unsaved title'});
  hooks.pathname = '/operations';
  hooks.render(App);
  hooks.flushEffects();
  const dirtyEvent = new Event('beforeunload', {cancelable: true});
  window.dispatchEvent(dirtyEvent);
  assert.equal(dirtyEvent.defaultPrevented, true);
  props.dispatch({type: 'cancel_edits'});
  hooks.render(App);
  hooks.flushEffects();
  const cleanEvent = new Event('beforeunload', {cancelable: true});
  window.dispatchEvent(cleanEvent);
  assert.equal(cleanEvent.defaultPrevented, false);
});

test('the prepare handler changes the URL only after the reducer accepts the draft', () => {
  const work = workbench();
  const tree = work.render();
  named(tree, 'ConfirmContext').props.onPrepare(target());
  assert.equal(work.hooks.navigations.length, 0, 'Do not navigate optimistically.');
  work.render();
  work.hooks.flushLayout();
  const search = new URLSearchParams(work.hooks.search);
  assert.equal(search.get('view'), 'edit');
  assert.equal(search.get('draft'), work.session.draft.id);
  assert.equal(search.get('template'), work.session.draft.templateRef.id);
  assert.equal(work.actions.filter(action => action.type === 'prepare').length, 1);
  assert.equal(work.actions[0].id, undefined, 'Only the reducer computes the draft ID.');
});

test('failed preparation stays on context and exposes the self-review error', () => {
  const work = workbench();
  const initialSearch = work.hooks.search;
  const bad = target();
  bad.roles.internalReviewer = bad.roles.actionOwner;
  named(work.render(), 'ConfirmContext').props.onPrepare(bad);
  work.render();
  work.hooks.flushLayout();
  assert.equal(work.hooks.search, initialSearch);
  assert.equal(work.hooks.navigations.length, 0);
  assert.equal(work.session.draft, null);
  assert.match(work.session.error, /cannot review their own draft/);
});

test('Inspect navigation calls no save action and shows only the last saved revision', () => {
  const work = workbench('edit', true);
  work.dispatch({type: 'edit_field', field: 'title', value: 'Unsaved title'});
  const draft = work.session.draft;
  named(work.render(), 'DraftEdits').props.onInspect();
  assert.equal(new URLSearchParams(work.hooks.search).get('view'), 'inspect');
  assert.ok(!work.actions.some(action => action.type === 'save_revision'));
  assert.equal(work.session.draft, draft);
  assert.equal(work.session.draft.revision, 1);
  assert.equal(work.session.editor.title, 'Unsaved title');
});

test('the form saves explicitly with its loaded revision and rejects a later stale submit', () => {
  const work = workbench('edit', true);
  work.dispatch({type: 'edit_field', field: 'title', value: 'Saved by the form'});
  const editorElement = named(work.render(), 'DraftEdits');
  const editorHooks = new Hooks();
  const editor = editorHooks.render(editorElement.type, editorElement.props);
  const form = find(editor, element => element.type === 'form');
  assert.equal(form.props.noValidate, true);
  assert.equal(button(editor, 'Save revision 2').props.type, 'submit');
  assert.equal(button(editor, 'Inspect draft').props.type, 'button');
  form.props.onSubmit({preventDefault() {}});
  assert.equal(work.actions.at(-1).expectedRevision, 1);
  assert.equal(work.session.draft.revision, 2);
  assert.match(work.session.notice, /^Saved as revision 2/);
  const saved = work.session.draft;
  work.dispatch({type: 'edit_field', field: 'title', value: 'Attempt from an old form'});
  form.props.onSubmit({preventDefault() {}});
  assert.equal(work.session.draft, saved);
  assert.equal(work.session.editor.title, 'Attempt from an old form');
  assert.match(work.session.error, /current saved revision is 2/);
});

test('submitting blank fields keeps Save usable, shows errors, and focuses the first invalid field', () => {
  const work = workbench('edit', true);
  work.dispatch({type: 'edit_field', field: 'title', value: ''});
  work.dispatch({type: 'edit_field', field: 'instructions', value: ''});
  let editorElement = named(work.render(), 'DraftEdits');
  const editorHooks = new Hooks();
  let editor = editorHooks.render(editorElement.type, editorElement.props);
  const form = find(editor, element => element.type === 'form');
  const calls = [];
  form.props.ref.current = {querySelector: selector => ({focus: () => calls.push(`focus:${selector}`), scrollIntoView: () => calls.push(`scroll:${selector}`)})};
  assert.equal(button(editor, 'Save revision 2').props.disabled, undefined);
  form.props.onSubmit({preventDefault() {}});
  editorElement = named(work.render(), 'DraftEdits');
  editor = editorHooks.render(editorElement.type, editorElement.props);
  editorHooks.flushLayout();
  assert.deepEqual(calls, ['focus:[name="title"]', 'scroll:[name="title"]']);
  assert.equal(find(editor, node => node.props.name === 'title').props['aria-invalid'], true);
  assert.equal(work.session.draft.revision, 1);
});

test('Keep draft and Escape close the delete confirmation and restore visible focus without deleting', () => {
  for (const closeWithEscape of [false, true]) {
    const work = workbench('edit', true);
    let tree = work.render();
    const calls = [];
    const opener = {isConnected: true, focus: () => calls.push('focus opener'), scrollIntoView: () => calls.push('scroll opener')};
    named(tree, 'DraftEdits').props.onDelete({currentTarget: opener});
    tree = work.render();
    work.hooks.flushLayout();
    work.hooks.flushEffects();
    assert.ok(button(tree, 'Keep draft'));
    if (closeWithEscape) {
      const event = new Event('keydown', {cancelable: true});
      Object.defineProperty(event, 'key', {value: 'Escape'});
      document.dispatchEvent(event);
      assert.equal(event.defaultPrevented, true);
    } else button(tree, 'Keep draft').props.onClick();
    tree = work.render();
    work.hooks.flushLayout();
    work.hooks.flushEffects();
    assert.equal(button(tree, 'Keep draft'), undefined);
    assert.deepEqual(calls, ['focus opener', 'scroll opener']);
    assert.ok(work.session.draft);
    assert.ok(!work.actions.some(action => action.type === 'delete_confirmed'));
  }
});

test('Delete is separate from Cancel and requires the inline confirmation handler', () => {
  const work = workbench('edit', true);
  work.dispatch({type: 'edit_field', field: 'title', value: 'Unsaved title'});
  let element = named(work.render(), 'DraftEdits');
  const editorHooks = new Hooks();
  let editor = editorHooks.render(element.type, element.props);
  const original = work.session.draft;
  button(editor, 'Cancel').props.onClick();
  assert.equal(work.session.draft, original);
  assert.equal(work.session.editor.title, original.title);
  element = named(work.render(), 'DraftEdits');
  editor = editorHooks.render(element.type, element.props);
  button(editor, 'Delete draft').props.onClick({currentTarget: {isConnected: true}});
  assert.equal(work.session.draft, original);
  const confirmation = work.render();
  button(confirmation, 'Delete draft permanently').props.onClick();
  assert.equal(work.session.draft, null);
  assert.equal(new URLSearchParams(work.hooks.search).get('view'), null);
});
