import {eagerRouteModules} from './testing/routeModules.js';
import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {MotionConfig} from 'motion/react';
import {createServer} from 'vite';
import {initialState, transition} from './workflow.js';
import {renewalEvidence, renewalFixture, fixturePeople} from './renewalFixtures.js';
import {playbookTemplates} from './playbookTemplates.js';
import {buildPlaybookContext, createPlaybooksSession, playbookSearch, playbooksSessionReducer} from './playbookDrafts.js';

// Markup checks use the installed JSX transform, without a listening server.
let server, Playbooks, App;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [eagerRouteModules()]});
  Playbooks = (await server.ssrLoadModule('/src/Playbooks.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});

const at = '2026-11-20T06:00:00.000Z';
const fresh = () => createPlaybooksSession('render-test');
function prepared(templateId = playbookTemplates[0].id, state = initialState) {
  return playbooksSessionReducer(fresh(), {type: 'prepare', templateId, context: buildPlaybookContext(state, renewalEvidence), confirmed: true, at});
}
function render(search, session = fresh(), state = initialState) {
  return renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [`/playbooks?${search}`]},
    React.createElement(Playbooks, {workflowState: state, evidence: renewalEvidence, session, dispatch: () => {}})));
}
const route = (session, view) => playbookSearch(session.draft.templateRef.id, view, session.draft.id);
const select = (html, name) => html.match(new RegExp(`<select[^>]*name="${name}"[^>]*>([\\s\\S]*?)</select>`))?.[1];
const stripTags = text => text.replace(/<[^>]+>/g, '');

test('each template route has one navigation selection and one context-confirmation control', () => {
  for (const template of playbookTemplates) {
    const html = render(playbookSearch(template.id));
    assert.ok(html.includes(template.output.title));
    assert.ok(html.includes(renewalFixture.agreement.label));
    assert.equal((html.match(/aria-current="page"/g) || []).length, 1);
    assert.doesNotMatch(html, /aria-pressed=/);
    assert.equal((html.match(/>Confirm context for a draft<\/button>/g) || []).length, 1);
  }
});

test('authority and reviewer selects start unassigned and offer fixture names only as deliberate options', () => {
  const state = {...initialState, plan: {...initialState.plan, owner: fixturePeople.otherAccountOwner}};
  const html = render(playbookSearch(playbookTemplates[0].id, 'context'), fresh(), state);
  const authority = select(html, 'customerAuthority');
  const reviewer = select(html, 'internalReviewer');
  const owner = select(html, 'actionOwner');
  assert.match(authority, /<option value="" selected="">Unidentified<\/option>/);
  assert.match(reviewer, /<option value="" selected="">Unassigned<\/option>/);
  assert.match(owner, /<option value="" selected="">/);
  assert.ok(!owner.includes(fixturePeople.otherAccountOwner));
  for (const name of renewalFixture.roles.actionOwners) assert.ok(owner.includes(`value="${name}"`));
  assert.ok(authority.includes(`${fixturePeople.customerCounsel} · walkthrough fixture, authority unconfirmed`));
  assert.doesNotMatch(authority, /<option value="[^\"]+" selected=/);
  assert.match(html, /type="checkbox"[^>]*required=""/);
  assert.match(html, /type="submit"[^>]*disabled=""/);
});

test('quoted sentences are unchanged and interpretations are set apart as workflow notes', () => {
  const html = render(playbookSearch(playbookTemplates[0].id));
  const quotes = [...html.matchAll(/<blockquote>([\s\S]*?)<\/blockquote>/g)].map(match => match[1]);
  assert.deepEqual(quotes, renewalEvidence.map(source => source.quote));
  for (const source of renewalEvidence) {
    assert.ok(!html.includes(source.context));
    const expected = renderToStaticMarkup(React.createElement('aside', {className: 'pb-workflow-note'},
      React.createElement('strong', null, 'Workflow note'), React.createElement('p', null, source.detail)));
    assert.ok(html.includes(expected));
  }
});

test('the retained draft disables the single confirmation control with its reason directly beside it', () => {
  const session = prepared();
  const html = render(playbookSearch(playbookTemplates[0].id), session);
  const start = html.match(/<div class="pb-actions pb-start">([\s\S]*?)<\/div>/)?.[1];
  const reasonId = start.match(/<p id="([^"]+)"/)?.[1];
  assert.ok(reasonId);
  assert.ok(start.includes(`aria-describedby="${reasonId}"`));
  assert.match(start, /disabled=""/);
  assert.match(stripTags(start), /already retained/);
  assert.equal((html.match(/>Confirm context for a draft<\/button>/g) || []).length, 1);
});

test('blank required fields do not disable saving and submit errors are associated with the invalid controls', () => {
  let session = prepared();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: ''});
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: ' '});
  session = playbooksSessionReducer(session, {type: 'save_revision', expectedRevision: 1, at});
  const html = render(route(session, 'edit'), session);
  assert.match(html, /<form[^>]*noValidate=""/i);
  const saveButton = html.match(/<button[^>]*>Save revision 2<\/button>/)?.[0];
  assert.ok(saveButton);
  assert.ok(!saveButton.includes('disabled'));
  for (const field of ['title', 'instructions']) {
    const control = html.match(new RegExp(`<(?:input|textarea)[^>]*name="${field}"[^>]*>`))?.[0];
    assert.match(control, /aria-invalid="true"/);
    const errorId = control.match(/aria-describedby="([^"]+)"/)?.[1];
    assert.ok(errorId);
    assert.ok(html.includes(`id="${errorId}">${session.fieldErrors[field]}`));
  }
  assert.match(html, /type="button"[^>]*>Cancel<\/button>/);
  assert.match(html, /type="button"[^>]*>Inspect draft<\/button>/);
});

test('inspection shows the saved revision, never the unsaved editor or approval controls', () => {
  for (const template of playbookTemplates) {
    let session = prepared(template.id);
    session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'Unsaved buffer only.'});
    const before = structuredClone(session);
    const html = render(route(session, 'inspect'), session);
    assert.ok(!html.includes('Unsaved buffer only.'));
    assert.match(html, /read-only inspection shows saved revision 1/);
    assert.equal((html.match(/<textarea/g) || []).length, 0);
    assert.doesNotMatch(html, /<button[^>]*>Approve/);
    assert.ok(html.includes(template.output.title));
    assert.deepEqual(session, before);
  }
});

test('a later review round at the same plan revision renders a stale-context callout and the old feedback', () => {
  const first = transition(transition(initialState, {type: 'request_review', at}), {type: 'request_changes', note: 'First review feedback.', at});
  const session = prepared(playbookTemplates[0].id, first);
  const unchanged = render(route(session, 'inspect'), session, first);
  assert.doesNotMatch(unchanged, /data-context-stale/);
  const second = transition(transition(first, {type: 'request_review', at: '2026-11-20T06:02:00.000Z'}), {type: 'request_changes', note: 'Second review feedback.', at: '2026-11-20T06:03:00.000Z'});
  assert.equal(second.plan.revision, first.plan.revision);
  const changed = render(route(session, 'inspect'), session, second);
  assert.match(changed, /role="status" data-context-stale="true"/);
  assert.ok(changed.includes(first.plan.reviewNote));
  assert.ok(!changed.includes(second.plan.reviewNote));
});

test('a direct inspection URL cannot bypass the self-review refusal', () => {
  const session = prepared();
  session.draft.contextSnapshot.roles.internalReviewer = session.draft.contextSnapshot.roles.actionOwner;
  const html = render(route(session, 'inspect'), session);
  assert.match(html, /role="alert"[^>]*>The action owner cannot review their own draft/);
  assert.ok(!html.includes('class="pb-tasks"'));
  assert.ok(!html.includes('Inspect draft provenance and human edits'));
});

test('legacy verified files still show a missing independent current-version basis', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document']) state = transition(state, {type});
  const session = prepared(playbookTemplates[0].id, state);
  const html = render(route(session, 'inspect'), session, state);
  assert.match(html, /Current version: basis not recorded/);
  assert.ok(!html.includes('Inspected with a separate version basis'));
});

test('reconciliation displays separate require-one and exclusion groups and typed customer requests', () => {
  const session = prepared(playbookTemplates[2].id);
  const html = render(route(session, 'inspect'), session);
  assert.match(html, /<dt>Requires one, group 1<\/dt>/);
  assert.match(html, /<dt>Excludes<\/dt>/);
  assert.match(html, /Customer-facing request/);
  assert.match(html, /Labelled reviewer attestation/);
  assert.match(html, /Cited source unit/);
});

test('a reloaded URL has a recovery view instead of rendering a fabricated draft', () => {
  const old = prepared();
  const html = render(route(old, 'edit'), fresh());
  assert.match(html, /This draft is no longer available here\./);
  assert.match(html, /Draft URLs do not store draft contents\./);
  assert.equal((html.match(/<textarea/g) || []).length, 0);
});

function renderApp(path, state) {
  const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const oldStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
  globalThis.window = {matchMedia: () => ({matches: false})};
  globalThis.localStorage = {getItem: key => key === 'gtm-workflow-demo-v1' ? JSON.stringify(state) : null};
  try {
    return renderToStaticMarkup(React.createElement(MotionConfig, {isStatic: true},
      React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))));
  } finally {
    if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow); else delete globalThis.window;
    if (oldStorage) Object.defineProperty(globalThis, 'localStorage', oldStorage); else delete globalThis.localStorage;
  }
}

test('fixture extraction keeps the existing renewal, Evidence, Operations, work-plan, and History pages renderable', () => {
  for (const path of ['/renewals', '/operations', renewalFixture.paths.evidence, renewalFixture.paths.workplan, renewalFixture.paths.history]) {
    const html = renderApp(path, initialState);
    assert.ok(html.includes(renewalFixture.account.name));
    assert.match(html, /Invented data/);
    assert.ok(!html.includes('undefined'));
  }
  const plan = stripTags(renderApp(renewalFixture.paths.workplan, initialState));
  for (const source of renewalEvidence) assert.ok(plan.includes(source.quote));
  const evidence = stripTags(renderApp(renewalFixture.paths.evidence, initialState));
  for (const source of renewalEvidence) assert.ok(evidence.includes(source.quote));
});

test('shared fixtures still render the approved workflow and its recorded decisions without alteration', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) state = transition(state, {type});
  state = transition(state, {type: 'record_customer_approval', reviewedBy: fixturePeople.customerCounsel, evidenceText: 'Recorded walkthrough decision.'});
  state = transition(state, {type: 'resolve_requirement'});
  const before = structuredClone(state);
  for (const path of ['/operations', renewalFixture.paths.evidence, renewalFixture.paths.workplan, renewalFixture.paths.history]) {
    const html = renderApp(path, state);
    assert.ok(html.includes(renewalFixture.account.name));
    assert.ok(html.includes(state.document.version) || path === '/operations');
  }
  assert.deepEqual(state, before);
});
