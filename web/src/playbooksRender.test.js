import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {initialState} from './workflow.js';
import {renewalEvidence} from './renewalEvidence.js';
import {playbookTemplates} from './playbookTemplates.js';
import {buildPlaybookContext, createPlaybooksSession, playbookSearch, playbooksSessionReducer} from './playbookDrafts.js';

// Server-render smoke only: no browser, DOM interaction, layout, or focus claim.
// Uses the already-locked Vite JSX transform; no new test dependency or listening server.
let server, Playbooks;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true}, appType: 'custom', logLevel: 'error'});
  Playbooks = (await server.ssrLoadModule('/src/Playbooks.jsx')).default;
});
after(async () => {await server?.close();});

const fresh = () => createPlaybooksSession('render-test');
function prepared(templateId = playbookTemplates[0].id) {
  return playbooksSessionReducer(fresh(), {type: 'prepare', templateId, context: buildPlaybookContext(initialState, renewalEvidence), confirmed: true, at: '2026-11-20T06:00:00.000Z'});
}
function render(search, session = fresh()) {
  return renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [`/playbooks?${search}`]},
    React.createElement(Playbooks, {workflowState: initialState, evidence: renewalEvidence, session, dispatch: () => {}})));
}

test('all three template routes render their distinct work, context, and completion proof', () => {
  for (const template of playbookTemplates) {
    const html = render(playbookSearch(template.id));
    assert.ok(html.includes(template.title));
    assert.ok(html.includes(template.output.title));
    assert.ok(html.includes('Proof that completes this work'));
    assert.ok(html.includes('Agreement A-17'));
    assert.ok(html.includes('E-017'));
    assert.ok(html.includes('Reloading or closing the tab discards them.'));
    assert.ok(html.includes('Confirm context for a draft'));
    assert.equal((html.match(/aria-pressed="true"/g) || []).length, 1);
  }
});

test('confirmation renders a required explicit checkbox, separate roles, and unknown-role options', () => {
  const html = render(playbookSearch(playbookTemplates[0].id, 'context'));
  assert.match(html, /type="checkbox"[^>]*required=""/);
  assert.ok(html.includes('Not assigned — identify an owner'));
  assert.ok(html.includes('Not established — identify who can decide'));
  assert.ok(html.includes('Alex Chen is the proposed internal reviewer'));
  assert.match(html, /type="submit"[^>]*disabled=""/);
});

test('prepared drafts render editable required fields, while inspection has no approval control', () => {
  for (const template of playbookTemplates) {
    const session = prepared(template.id);
    const editor = render(playbookSearch(template.id, 'edit', session.draft.id), session);
    assert.equal((editor.match(/<textarea/g) || []).length, 2);
    assert.ok(editor.includes('Inspect draft'));
    assert.ok(editor.includes('Cancel draft'));
    const inspection = render(playbookSearch(template.id, 'inspect', session.draft.id), session);
    assert.ok(inspection.includes(template.output.title));
    assert.ok(inspection.includes('Inspection only. No review decision recorded.'));
    assert.ok(inspection.includes('Back to editing'));
    assert.equal((inspection.match(/<textarea/g) || []).length, 0);
    assert.doesNotMatch(inspection, /<button[^>]*>Approve/);
  }
});

test('Back to an inspection with unapplied edits explains which revision is shown', () => {
  let session = prepared();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'A pending human edit.'});
  const html = render(playbookSearch(session.draft.templateRef.id, 'inspect', session.draft.id), session);
  assert.ok(html.includes('Unapplied edits are still retained.'));
  assert.ok(html.includes('This inspection shows prepared revision 1'));
  assert.ok(!html.includes('A pending human edit.'));
});

test('a reloaded draft URL has an honest recovery view rather than fabricating a draft', () => {
  const old = prepared();
  const html = render(playbookSearch(old.draft.templateRef.id, 'edit', old.draft.id), fresh());
  assert.ok(html.includes('This draft is no longer available here.'));
  assert.ok(html.includes('Draft URLs do not store draft contents.'));
  assert.ok(html.includes('Return to template'));
  assert.equal((html.match(/<textarea/g) || []).length, 0);
});
