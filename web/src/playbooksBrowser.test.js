import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {confirmDraftNavigation, focusFirstDraftError, focusInView, warnBeforeDraftUnload} from './playbooksBrowser.js';
import {buildPlaybookContext, createPlaybooksSession, playbooksSessionReducer} from './playbookDrafts.js';
import {initialState, renewalEvidence} from './renewalFixtures.js';

const prepared = () => playbooksSessionReducer(createPlaybooksSession('browser-test'), {
  type: 'prepare', templateId: 'obtain_current_rider', context: buildPlaybookContext(initialState, renewalEvidence), confirmed: true, at: '2026-11-20T06:00:00.000Z',
});

test('focus restoration explicitly scrolls the initiating control into view without animation', () => {
  const calls = [];
  focusInView({focus: options => calls.push(['focus', options]), scrollIntoView: options => calls.push(['scroll', options])});
  assert.deepEqual(calls, [['focus', {preventScroll: true}], ['scroll', {block: 'nearest', behavior: 'instant'}]]);
  assert.doesNotThrow(() => focusInView(null));
});

test('submit validation focuses the first invalid text field rather than the alert or a later field', () => {
  const queried = [];
  let focused = false, scrolled = false;
  const form = {querySelector: selector => {queried.push(selector); return {focus: () => {focused = true;}, scrollIntoView: () => {scrolled = true;}};}};
  focusFirstDraftError(form, {instructions: 'Missing', title: 'Missing', specifications: 'Missing'});
  assert.deepEqual(queried, ['[name="title"]']);
  assert.equal(focused && scrolled, true);
  focusFirstDraftError(form, {});
  assert.equal(queried.length, 1);
});

test('beforeunload warns only while dirty and cleanup removes the warning', () => {
  const listeners = new Map();
  const target = {addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: (name, fn) => {assert.equal(listeners.get(name), fn); listeners.delete(name);}};
  warnBeforeDraftUnload(target, false)();
  assert.equal(listeners.size, 0);
  const cleanup = warnBeforeDraftUnload(target, true);
  let prevented = false;
  const event = {preventDefault: () => {prevented = true;}, returnValue: undefined};
  listeners.get('beforeunload')(event);
  assert.equal(prevented, true);
  assert.equal(event.returnValue, '');
  cleanup();
  assert.equal(listeners.size, 0);
});

test('leaving dirty drafts warns and never saves or discards the retained edits', () => {
  const clean = prepared();
  let prompts = 0;
  const deny = () => {prompts += 1; return false;};
  assert.equal(confirmDraftNavigation(clean, deny), true);
  assert.equal(prompts, 0);
  const dirty = playbooksSessionReducer(clean, {type: 'edit_field', field: 'title', value: 'Unsaved draft title'});
  const before = structuredClone(dirty);
  assert.equal(confirmDraftNavigation(dirty, deny), false);
  assert.equal(prompts, 1);
  assert.equal(confirmDraftNavigation(dirty, () => true), true);
  assert.deepEqual(dirty, before);
});

test('retained titles wrap and the removed modal selectors cannot affect other pages', () => {
  const css = readFileSync(new URL('./playbooks.css', import.meta.url), 'utf8');
  assert.match(css, /\.pb-retained p\s*\{[^}]*overflow-wrap:anywhere/);
  assert.doesNotMatch(css, /pb-library\s*\{[^}]*position:/);
  for (const name of ['atelier', 'modern', 'refinements', 'styles']) {
    const shared = readFileSync(new URL(`./${name}.css`, import.meta.url), 'utf8');
    assert.doesNotMatch(shared, /\.(playbook-card|playbook-grid|sculptural-intro)\b/);
  }
});
