import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {initialState, derive, transition} from './workflow.js';
import {renewalEvidence, renewalFixture, fixturePeople} from './renewalFixtures.js';
import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';
import {
  buildPlaybookContext, buildTemplateWork, changedDraftFields, createPlaybooksSession, createPlaybooksSessionId,
  describeDocument, draftAutomaticItems, draftEditorValues, hasUnsavedPlaybookEdits, inspectPlaybookDraft,
  isDraftContextCurrent, playbookSearch, playbooksSessionReducer, preparedDraftLocation, preparePlaybookDraft,
  readPlaybookLocation, savePlaybookDraft, validateDraftEdits,
} from './playbookDrafts.js';

const templateIds = ['obtain_current_rider', 'review_renewal_condition', 'reconcile_approval_conflict'];
const at = '2026-11-20T06:00:00.000Z';
const context = (state = initialState, evidence = renewalEvidence, roles = {}) => buildPlaybookContext(state, evidence, roles);
const prepare = (templateId = templateIds[0], target = context()) => preparePlaybookDraft({templateId, context: target, confirmed: true, id: `test-${templateId}`, at});
const act = (state, type, extra = {}) => transition(state, {type, ...extra});
const authorized = () => act(act(structuredClone(initialState), 'request_review'), 'approve_plan');
const verified = () => act(act(authorized(), 'obtain_document'), 'verify_document');
const completed = () => act(act(act(verified(), 'request_customer_review'), 'record_customer_approval', {
  reviewedBy: fixturePeople.customerCounsel, evidenceText: 'Synthetic approval of the current terms.',
}), 'resolve_requirement');
const start = (session = createPlaybooksSession('test-session'), templateId = templateIds[0], target = context()) => playbooksSessionReducer(session, {type: 'prepare', templateId, context: target, confirmed: true, at});
const save = session => playbooksSessionReducer(session, {type: 'save_revision', expectedRevision: session.editorRevision, at});
const item = (work, id) => work.items.find(item => item.id === id);
const basis = (document, version = document.version) => ({id: 'version-fact', documentId: document.id, version, source: {kind: 'unit', ref: 'separate-version-source'}});

function assertInputs(inputs) {
  assert.ok(inputs.length);
  for (const input of inputs) {
    assert.deepEqual(Object.keys(input).sort(), ['label', 'mode', 'ref']);
    assert.equal(typeof input.ref, 'string');
    assert.ok(input.label);
    assert.ok(['allowed', 'required'].includes(input.mode));
  }
}
function assertItemGraph(items, proposed = true) {
  const ids = new Set(items.map(item => item.id));
  assert.equal(ids.size, items.length);
  const seen = new Set();
  for (const item of items) {
    assert.ok(item.title && item.output);
    assertInputs(item.inputs);
    if (proposed) assert.equal(item.status, 'proposed');
    assert.ok(['actionOwner', 'internalReviewer'].includes(item.role));
    assert.ok(['internal_task', 'customer_request'].includes(item.kind));
    assert.ok(['unit', 'receipt', 'attestation'].includes(item.completionProof.kind));
    assert.ok(item.completionProof.description && item.completionProof.ref);
    assert.deepEqual(Object.keys(item.dependencies).sort(), ['all', 'excludes', 'oneOf']);
    for (const id of [...item.dependencies.all, ...item.dependencies.oneOf.flat()]) assert.ok(seen.has(id), `${item.id} requires earlier work: ${id}`);
    for (const group of item.dependencies.oneOf) assert.ok(group.length > 1);
    for (const id of item.dependencies.excludes) assert.ok(ids.has(id));
    if (item.addedBecauseOf) {
      const source = items.find(source => source.id === item.addedBecauseOf);
      assert.ok(source);
      assert.equal(source.addedBecauseOf, undefined, 'Auto-added items cannot generate more items.');
    }
    seen.add(item.id);
  }
  assert.equal(items.at(-1).id, 'inspect_output');
}
function freeze(value) {
  if (value && typeof value === 'object') {Object.values(value).forEach(freeze); Object.freeze(value);}
  return value;
}

test('templates and drafts retain section 8 inputs, per-item dependency groups, kinds, and typed proof', () => {
  assert.deepEqual(playbookTemplates.map(template => template.id), templateIds);
  assert.equal(getPlaybookTemplate('unknown'), null);
  const drafts = templateIds.map(id => prepare(id).draft);
  assert.equal(new Set(drafts.map(draft => draft.output.id)).size, 3);
  assert.equal(new Set(drafts.map(draft => JSON.stringify(draft.items))).size, 3);
  for (const [index, draft] of drafts.entries()) {
    const template = playbookTemplates[index];
    assertInputs(template.inputs);
    assertInputs(draft.inputs);
    assertItemGraph([...template.automaticItems, ...template.items], false);
    assertItemGraph(draft.items);
    assert.deepEqual(draft.templateRef, {id: template.id, version: template.version});
    assert.equal(draft.revision, 1);
    assert.equal(draft.status, 'draft');
    for (const need of [...draft.evidenceNeeds, ...draft.prerequisites]) assert.ok(item(draft, need.recoveryTaskId));
    for (const need of draft.evidenceNeeds.filter(need => need.output)) {
      assert.equal(need.mode, 'allowed');
      assert.ok(!draft.requiredEvidence.includes(need.ref));
    }
  }
  const obtain = playbookTemplates[0];
  assert.ok(!obtain.requiredEvidence.includes('current_document'));
  assert.ok(obtain.output.evidence.includes('current_document'));
  assert.ok(drafts[0].items.some(item => item.kind === 'customer_request'));
});

test('reconciliation requires one clarification or a sourced no-conflict finding without flattening the alternatives', () => {
  const draft = prepare(templateIds[2]).draft;
  const reconciliation = item(draft, 'draft_reconciliation');
  assert.deepEqual(reconciliation.dependencies, {all: ['compare_claims', 'confirm_authority'], oneOf: [['seek_clarification', 'record_no_conflict']], excludes: []});
  const request = item(draft, 'seek_clarification');
  const noConflict = item(draft, 'record_no_conflict');
  assert.equal(request.kind, 'customer_request');
  assert.equal(request.completionProof.kind, 'unit');
  assert.deepEqual(request.dependencies.excludes, [noConflict.id]);
  assert.deepEqual(noConflict.dependencies.excludes, [request.id]);
  assert.equal(noConflict.kind, 'internal_task');
  assert.equal(noConflict.completionProof.kind, 'attestation');
  assert.deepEqual(noConflict.completionProof.cites, renewalEvidence.map(source => source.id));
});

test('template definitions are deeply immutable and every draft has independent item and output copies', () => {
  const template = getPlaybookTemplate(templateIds[0]);
  assert.throws(() => {template.items[0].inputs.push({ref: 'other'});}, TypeError);
  assert.throws(() => {template.items[0].completionProof.kind = 'attestation';}, TypeError);
  const before = structuredClone(template);
  const one = prepare().draft;
  const two = prepare().draft;
  item(one, 'locate_document').inputs[0].label = 'Local input';
  one.output.title = 'Local output';
  one.completionRules[0].text = 'Local rule';
  assert.deepEqual(template, before);
  assert.notDeepEqual(one.items, two.items);
  assert.equal(two.output.title, template.output.title);
});

test('reusable templates contain semantic bindings, not synthetic names or record IDs', () => {
  const templates = JSON.stringify(playbookTemplates);
  for (const value of [...Object.values(fixturePeople), renewalFixture.id, renewalFixture.account.name, renewalFixture.agreement.id, ...renewalEvidence.map(source => source.id)]) {
    assert.ok(!templates.includes(value), `Template must not contain ${value}`);
  }
  for (const path of ['Playbooks.jsx', 'playbookDrafts.js']) {
    const code = readFileSync(new URL(path, import.meta.url), 'utf8');
    for (const value of [...Object.values(fixturePeople), renewalFixture.id, renewalFixture.agreement.id, ...renewalEvidence.map(source => source.id)]) assert.ok(!code.includes(value));
  }
  assert.ok(playbookTemplates[0].inputs.some(input => input.ref === 'customer_statement'));
  assert.ok(prepare().draft.inputs.some(input => input.ref === renewalFixture.bindings.customer_statement));
});

test('the context adapter reuses the single fixture and strips presentation fields', () => {
  const snapshot = context(initialState, renewalEvidence.map(record => ({...record, icon: () => null})));
  assert.deepEqual(snapshot.refs, {accountId: renewalFixture.account.id, renewalId: renewalFixture.renewal.id, agreementId: renewalFixture.agreement.id, requirementId: initialState.requirement.id});
  assert.deepEqual(snapshot.evidence.map(record => [record.id, record.quote, record.context]), renewalEvidence.map(record => [record.id, record.quote, record.context]));
  assert.ok(snapshot.evidence.every(record => !Object.hasOwn(record, 'icon')));
  assert.doesNotThrow(() => structuredClone(snapshot));
  snapshot.evidence[0].quote = 'Local copy';
  snapshot.agreement.title = 'Local title';
  assert.notEqual(snapshot.evidence[0].quote, renewalEvidence[0].quote);
  assert.notEqual(snapshot.agreement.title, renewalFixture.agreement.title);
});

test('preparation requires explicit confirmation and the fixture renewal, not a typed authorization', () => {
  for (const confirmed of [undefined, false, 'true']) {
    assert.equal(preparePlaybookDraft({templateId: templateIds[0], context: context(), id: 'draft', at, confirmed}).ok, false);
  }
  for (const badContext of [null, {...context(), synthetic: false}, {...context(), synthetic: 'true'}, {...context(), refs: {...context().refs, agreementId: 'unknown-agreement'}}]) {
    assert.equal(preparePlaybookDraft({templateId: templateIds[0], context: badContext, id: 'draft', at, confirmed: true}).ok, false);
  }
  assert.equal(prepare('not-a-template').ok, false);
  assert.equal(preparePlaybookDraft({templateId: templateIds[0], context: context(), confirmed: true, at}).ok, false);
});

test('missing customer statements get source-and-context recovery before document collection', () => {
  const draft = prepare(templateIds[0], context(initialState, [])).draft;
  const need = draft.evidenceNeeds.find(need => need.id === 'customer_statement');
  const recovery = item(draft, need.recoveryTaskId);
  assert.equal(need.state, 'missing');
  assert.notEqual(recovery.id, 'locate_document');
  assert.equal(recovery.completionProof.kind, 'unit');
  assert.equal(recovery.completionProof.ref, renewalFixture.bindings.customer_statement);
  assert.equal(recovery.completionProof.requiresContext, true);
  assert.ok(item(draft, 'locate_document').dependencies.all.includes(recovery.id));
  assert.deepEqual(recovery.dependencies, {all: [], oneOf: [], excludes: []});
  assert.equal(recovery.inputs.find(input => input.ref === need.ref).mode, 'allowed');
  assertItemGraph(draft.items);
});

test('preserving statements does not wait for scope authorization, even when the owner is unknown', () => {
  for (const template of playbookTemplates) {
    const draft = prepare(template.id, context(initialState, [], {actionOwner: null})).draft;
    const firstSource = item(draft, template.recoveryTasks.customer_statement);
    assert.deepEqual(firstSource.dependencies, {all: [], oneOf: [], excludes: []});
    assert.equal(firstSource.completionProof.kind, 'unit');
    assert.equal(firstSource.completionProof.requiresContext, true);
    assertItemGraph(draft.items);
  }
});

test('document collection and inspection are separate items with different receipt proof', () => {
  const draft = prepare().draft;
  assert.equal(draft.contextSnapshot.document, null);
  const collect = item(draft, 'locate_document');
  const inspect = item(draft, 'verify_document');
  assert.deepEqual(item(draft, 'authorize_scope').dependencies.all, []);
  assert.ok(collect.dependencies.all.includes('authorize_scope'));
  assert.equal(collect.completionProof.kind, 'receipt');
  assert.equal(collect.completionProof.inspection, 'not_required');
  assert.equal(inspect.completionProof.inspection, 'accepted');
  assert.deepEqual(inspect.dependencies.all, [collect.id]);
  assert.ok(item(draft, 'prepare_handoff').dependencies.all.includes(inspect.id));
  assert.equal(item(draft, 'prepare_handoff').kind, 'customer_request');
  assert.ok(collect.inputs.some(input => input.ref === 'internal_document_source'));
});

test('the authority and reviewer defaults remain unknown even after a legacy customer approval', () => {
  for (const state of [initialState, completed()]) {
    const draft = prepare(templateIds[0], context(state)).draft;
    assert.equal(draft.contextSnapshot.roles.internalReviewer, null);
    assert.equal(draft.contextSnapshot.roles.customerAuthority, null);
    assert.equal(draft.contextSnapshot.roles.authorityBasis, null);
    assert.equal(draft.evidenceNeeds.find(need => need.id === 'customer_authority').state, 'unknown');
    assert.equal(item(draft, 'confirm_authority').completionProof.kind, 'unit');
    assert.match(item(draft, 'confirm_authority').title, /Record who decides, with evidence/);
  }
  const chosen = prepare(templateIds[0], context(initialState, renewalEvidence, {customerAuthority: fixturePeople.customerCounsel})).draft;
  assert.equal(chosen.contextSnapshot.roles.customerAuthority, fixturePeople.customerCounsel);
  assert.equal(chosen.contextSnapshot.roles.authorityBasis, null);
  assert.equal(chosen.contextSnapshot.customerReview, null);
});

test('owners from another account are not offered or silently adopted as the proposed owner', () => {
  const state = {...initialState, plan: {...initialState.plan, owner: fixturePeople.otherAccountOwner}};
  const target = context(state);
  assert.ok(!target.roleOptions.actionOwners.includes(fixturePeople.otherAccountOwner));
  assert.equal(target.roles.actionOwner, null);
  assert.equal(target.existingPlan.owner, fixturePeople.otherAccountOwner);
  assert.ok(item(prepare(templateIds[0], target).draft, 'assign_owner'));
});

test('self-review is refused when preparing, inspecting, saving, or using the session', () => {
  for (const name of renewalFixture.roles.internalReviewers) {
    const target = context(initialState, renewalEvidence, {actionOwner: name, internalReviewer: name});
    const result = prepare(templateIds[0], target);
    assert.equal(result.ok, false);
    assert.match(result.error, /cannot review their own draft/);
    const forged = {...prepare().draft, contextSnapshot: target};
    assert.equal(inspectPlaybookDraft(forged).ok, false);
    assert.equal(savePlaybookDraft(forged, draftEditorValues(forged), {expectedRevision: 1, at}).ok, false);
    const session = start(createPlaybooksSession('same-person'), templateIds[0], target);
    assert.equal(session.draft, null);
    assert.equal(session.nextDraftNumber, 1);
    assert.match(session.error, /cannot review their own draft/);
  }
  assert.equal(prepare(templateIds[0], context(initialState, renewalEvidence, {internalReviewer: fixturePeople.internalReviewer})).ok, true);
});

test('auto-added items cite base items only, can be removed and restored, and never grant authorization', () => {
  for (const template of playbookTemplates) {
    let session = start(createPlaybooksSession(template.id), template.id, context(initialState, renewalEvidence, {actionOwner: null}));
    const additions = draftAutomaticItems(session.draft);
    assert.deepEqual(additions.map(item => item.id), ['assign_owner', 'authorize_scope']);
    for (const addition of additions) {
      assert.ok(!item(session.draft, addition.addedBecauseOf).addedBecauseOf);
      session = playbooksSessionReducer(session, {type: 'toggle_auto_item', id: addition.id});
    }
    assert.equal(hasUnsavedPlaybookEdits(session), true);
    assert.equal(session.draft.items.filter(item => item.addedBecauseOf).length, 2, 'Toggles remain unsaved.');
    session = save(session);
    assert.equal(session.draft.revision, 2);
    assert.equal(session.draft.items.filter(item => item.addedBecauseOf).length, 0);
    assertItemGraph(session.draft.items);
    assert.equal(session.draft.roles, undefined);
    assert.equal(session.draft.contextSnapshot.roles.actionOwner, null);
    assert.equal(session.draft.prerequisites.find(need => need.id === 'internal_authorization').recoveryTaskId, null);
    assert.deepEqual(session.draft.humanEdits[0].changes[0], {field: 'removedItemIds', before: [], after: ['assign_owner', 'authorize_scope']});
    assert.equal(playbooksSessionReducer(session, {type: 'toggle_auto_item', id: 'inspect_output'}), session);
    session = playbooksSessionReducer(session, {type: 'toggle_auto_item', id: 'authorize_scope'});
    session = save(session);
    assert.ok(item(session.draft, 'authorize_scope'));
    assert.equal(item(session.draft, 'assign_owner'), undefined);
    assertItemGraph(session.draft.items);
  }
});

test('a document cannot declare its own current version or upgrade its own verification label', () => {
  const document = verified().document;
  for (const candidate of [document, {...document, currentVersion: 'older'}, {...document, currentVersion: null}]) {
    const description = describeDocument(candidate);
    assert.equal(description.state, 'unknown');
    assert.equal(description.currentVersion, null);
    assert.match(description.label, /basis not recorded/);
  }
  assert.equal(Object.hasOwn(context(verified()).document, 'currentVersion'), false);
  for (const invalidBasis of [null, {...basis(document), source: null}, {...basis(document), source: {kind: 'unit', ref: document.id}}, {...basis(document), documentId: 'different-file'}, {...basis(document), source: {kind: 'receipt', ref: 'file-receipt', documentId: document.id}}]) {
    assert.equal(describeDocument(document, invalidBasis).state, 'unknown');
  }
});

test('a separate version fact can expose mismatches without changing the legacy workflow', () => {
  const state = verified();
  const before = structuredClone(state);
  const currentBasis = basis(state.document);
  assert.equal(describeDocument(state.document, currentBasis).state, 'verified');
  assert.equal(describeDocument({...state.document, status: 'received'}, currentBasis).state, 'received');
  assert.equal(describeDocument({...state.document, id: null}, currentBasis).state, 'unknown');
  const target = context({...state, documentVersionBasis: basis(state.document, 'v4')});
  for (const templateId of templateIds) {
    const draft = prepare(templateId, target).draft;
    assert.equal(draft.evidenceNeeds.find(need => need.id === 'current_document').state, 'stale');
    assertItemGraph(draft.items);
  }
  assert.match(item(prepare(templateIds[0], target).draft, 'locate_document').title, /replacement/);
  assert.deepEqual(state, before);
});

test('second review rounds at one plan revision invalidate the snapshot by note and timing', () => {
  const first = act(act(initialState, 'request_review', {at}), 'request_changes', {at: '2026-11-20T06:01:00.000Z', note: 'Check the internal file source.'});
  const draft = prepare(templateIds[0], context(first)).draft;
  const second = act(act(first, 'request_review', {at: '2026-11-20T06:02:00.000Z'}), 'request_changes', {at: '2026-11-20T06:03:00.000Z', note: 'Keep the receipt with the source.'});
  assert.equal(first.plan.revision, second.plan.revision);
  assert.equal(first.plan.status, second.plan.status);
  assert.equal(isDraftContextCurrent(draft, context(first)), true);
  assert.equal(isDraftContextCurrent(draft, context(second)), false);
  assert.equal(draft.contextSnapshot.existingPlan.reviewNote, first.plan.reviewNote);
  assert.equal(draft.contextSnapshot.existingPlan.requestedAt, first.plan.requestedAt);
  assert.equal(draft.contextSnapshot.planReviews.at(-1).at, first.history.at(-1).at);
  const sameNote = act(act(first, 'request_review'), 'request_changes', {note: first.plan.reviewNote});
  assert.equal(isDraftContextCurrent(draft, context(sameNote)), false, 'A repeated note still represents a later review round.');
  const onlyFeedback = {...first, plan: {...first.plan, reviewNote: 'Different feedback at the same revision.'}};
  assert.equal(isDraftContextCurrent(draft, context(onlyFeedback)), false);
});

test('snapshots detect source, decision timing, and version-basis changes without silently rebasing', () => {
  const target = context(completed());
  const draft = prepare(templateIds[0], target).draft;
  assert.equal(isDraftContextCurrent(draft, context(completed())), true);
  assert.equal(isDraftContextCurrent(draft, {...context(completed()), roles: {...target.roles, actionOwner: null}}), true);
  const changed = context(completed());
  changed.customerReview.approvedAt = at;
  assert.equal(isDraftContextCurrent(draft, changed), false);
  changed.customerReview.approvedAt = target.customerReview.approvedAt;
  changed.documentVersionBasis = basis(target.document);
  assert.equal(isDraftContextCurrent(draft, changed), false);
  target.evidence[0].quote = 'Changed caller copy';
  assert.equal(draft.contextSnapshot.evidence[0].quote, renewalEvidence[0].quote);
});

test('explicit saves carry the expected revision and refuse stale writes without losing unsaved text', () => {
  let session = start();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: 'Human draft title'});
  session = save(session);
  assert.equal(session.draft.revision, 2);
  assert.equal(session.draft.title, 'Human draft title');
  assert.match(session.notice, /^Saved as revision 2\./);
  const saved = session.draft;
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: 'Uncommitted title'});
  const stale = playbooksSessionReducer(session, {type: 'save_revision', expectedRevision: 1, at});
  assert.equal(stale.draft, saved);
  assert.equal(stale.editor.title, 'Uncommitted title');
  assert.match(stale.error, /current saved revision is 2/);
  assert.equal(savePlaybookDraft(saved, draftEditorValues(saved), {at}).code, 'stale_revision');
  assert.equal(savePlaybookDraft(saved, draftEditorValues(saved), {expectedRevision: 1, at}).code, 'stale_revision');
  assert.equal(savePlaybookDraft(saved, draftEditorValues(saved), {expectedRevision: '2', at}).ok, false);
});

test('Inspect is read-only and keeps both the saved revision and unsaved buffer unchanged', () => {
  let session = start();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'Unfinished instructions.'});
  const draft = freeze(session.draft), editor = freeze(session.editor);
  const inspected = playbooksSessionReducer(session, {type: 'inspect'});
  assert.equal(inspected.draft, draft);
  assert.equal(inspected.editor, editor);
  assert.equal(inspected.draft.revision, 1);
  assert.equal(inspected.draft.humanEdits.length, 0);
  assert.equal(inspectPlaybookDraft(draft).draft, draft);
});

test('Cancel restores saved text and removable items but does not delete the draft', () => {
  let session = start();
  session = save(playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: 'Saved human title'}));
  const saved = session.draft;
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: ''});
  session = playbooksSessionReducer(session, {type: 'toggle_auto_item', id: 'authorize_scope'});
  session = save(session);
  assert.ok(session.fieldErrors.title);
  session = playbooksSessionReducer(session, {type: 'cancel_edits'});
  assert.equal(session.draft, saved);
  assert.deepEqual(session.editor, draftEditorValues(saved));
  assert.equal(session.editorRevision, saved.revision);
  assert.equal(session.error, null);
  assert.equal(hasUnsavedPlaybookEdits(session), false);
});

test('saved human edits keep before/after values and all version identities independent', () => {
  const original = prepare(templateIds[0], context(verified())).draft;
  const before = structuredClone(original);
  const values = {...draftEditorValues(original), title: '  Refined collection scope  ', instructions: 'Preserve the file receipt timestamp.'};
  const edited = savePlaybookDraft(original, values, {expectedRevision: 1, at}).draft;
  assert.equal(edited.revision, 2);
  assert.equal(edited.title, values.title.trim());
  assert.deepEqual(edited.templateRef, original.templateRef);
  assert.equal(edited.contextSnapshot.existingPlan.revision, initialState.plan.revision);
  assert.equal(edited.contextSnapshot.document.version, verified().document.version);
  assert.deepEqual(edited.contextSnapshot, original.contextSnapshot);
  assert.deepEqual(edited.items, original.items);
  assert.deepEqual(edited.completionRules, original.completionRules);
  assert.deepEqual(edited.humanEdits[0].changes.map(change => change.field), ['title', 'instructions']);
  assert.equal(edited.humanEdits[0].changes[1].before, original.instructions);
  assert.equal(edited.humanEdits[0].changes[1].after, values.instructions);
  assert.deepEqual(original, before);
});

test('no-op text normalization does not manufacture revisions or history', () => {
  const draft = prepare().draft;
  const values = {...draftEditorValues(draft), title: `  ${draft.title}  `};
  assert.deepEqual(changedDraftFields(draft, values), []);
  assert.equal(savePlaybookDraft(draft, values, {expectedRevision: 1, at}).draft, draft);
});

test('blank, overlong, non-text and unauthorized shape edits preserve the saved draft', () => {
  const draft = freeze(prepare().draft);
  for (const patch of [
    {title: ' '}, {specifications: ''}, {instructions: '\n\t'}, {title: 'x'.repeat(161)}, {instructions: 'x'.repeat(1201)},
    {specifications: null}, {status: 'approved'}, {completionRules: []}, {items: []}, {removedItemIds: ['verify_document']},
    {removedItemIds: ['authorize_scope', 'authorize_scope']},
  ]) {
    const values = {...draftEditorValues(draft), ...patch};
    assert.ok(Object.keys(validateDraftEdits(values, draft)).length > 0);
    assert.equal(savePlaybookDraft(draft, values, {expectedRevision: 1, at}).ok, false);
    assert.equal(draft.revision, 1);
  }
  assert.equal(savePlaybookDraft({...draft, status: 'approved'}, draftEditorValues(draft), {expectedRevision: 1, at}).ok, false);
});

test('every draft operation leaves approved and resolved workflow records byte-for-byte unchanged', () => {
  const state = freeze(completed());
  const before = structuredClone(state);
  const gates = derive(state);
  for (const templateId of templateIds) {
    let session = start(createPlaybooksSession(templateId), templateId, context(state));
    session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'Refined proposed instructions.'});
    session = playbooksSessionReducer(session, {type: 'toggle_auto_item', id: 'authorize_scope'});
    session = save(session);
    session = playbooksSessionReducer(session, {type: 'inspect'});
    session = playbooksSessionReducer(session, {type: 'cancel_edits'});
    session = playbooksSessionReducer(session, {type: 'delete_confirmed'});
    assert.equal(session.draft, null);
    assert.deepEqual(state, before);
    assert.deepEqual(derive(state), gates);
  }
});

test('successful preparation navigates using the reducer-owned ID; failure returns no new location', () => {
  const empty = createPlaybooksSession('url-session');
  const session = start(empty);
  const location = preparedDraftLocation(empty, session);
  assert.equal(location.get('draft'), session.draft.id);
  assert.equal(location.get('view'), 'edit');
  assert.equal(readPlaybookLocation(location, session).view, 'edit');
  const failed = playbooksSessionReducer(empty, {type: 'prepare', templateId: templateIds[0], context: context(), confirmed: false, at});
  assert.equal(preparedDraftLocation(empty, failed), null);
  assert.equal(preparedDraftLocation(session, start(session)), null);
});

test('selection and navigation preserve unsaved edits and never save or replace a retained draft', () => {
  let session = start();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'Keep this unsaved instruction.'});
  const before = structuredClone(session);
  readPlaybookLocation(playbookSearch(templateIds[1]), session);
  assert.equal(readPlaybookLocation(playbookSearch(templateIds[0], 'inspect', session.draft.id), session).view, 'inspect');
  assert.deepEqual(session, before);
  const second = start(session, templateIds[2]);
  assert.equal(second.draft, session.draft);
  assert.equal(second.editor, session.editor);
  assert.equal(second.nextDraftNumber, session.nextDraftNumber);
  assert.ok(second.error);
});

test('confirmed deletion and reset clear the session without reusing an old URL identity', () => {
  for (const type of ['delete_confirmed', 'reset_session']) {
    let first = start();
    first = playbooksSessionReducer(first, {type: 'edit_field', field: 'title', value: 'Temporary edit'});
    const oldUrl = playbookSearch(templateIds[0], 'edit', first.draft.id);
    const cleared = playbooksSessionReducer(first, {type});
    assert.equal(cleared.draft, null);
    assert.equal(cleared.editor, null);
    assert.equal(readPlaybookLocation(oldUrl, cleared).view, 'unavailable');
    const second = start(cleared);
    assert.notEqual(second.draft.id, first.draft.id);
    assert.equal(readPlaybookLocation(oldUrl, second).view, 'unavailable');
  }
});

test('reload, missing drafts, and mismatched locations recover without fabricating draft contents', () => {
  const session = start();
  const reloaded = createPlaybooksSession('new-page-mount');
  for (const target of [reloaded, start(reloaded)]) assert.equal(readPlaybookLocation(playbookSearch(templateIds[0], 'edit', session.draft.id), target).view, 'unavailable');
  for (const search of [playbookSearch(templateIds[1], 'inspect', session.draft.id), playbookSearch('unknown', 'edit', session.draft.id), playbookSearch(templateIds[0], 'inspect', 'missing')]) assert.equal(readPlaybookLocation(search, session).view, 'unavailable');
  assert.deepEqual(readPlaybookLocation('template=unknown&view=publish', session), {templateId: templateIds[0], view: 'template', invalidSelection: true});
});

test('session nonces work when the browser has no randomUUID or the method throws', () => {
  assert.equal(createPlaybooksSessionId({randomUUID: () => 'available-uuid'}), 'available-uuid');
  const ids = [createPlaybooksSessionId(null), createPlaybooksSessionId({}), createPlaybooksSessionId({randomUUID: () => {throw new Error('unavailable');}})];
  assert.equal(new Set(ids).size, ids.length);
  assert.ok(ids.every(id => /^local-/.test(id)));
});

test('the session exposes no execution, approval, send, persistence, or workflow transition', () => {
  const session = freeze(start());
  for (const type of ['approve_plan', 'request_review', 'send', 'verify_document', 'record_customer_approval', 'resolve_requirement', 'save_plan', 'publish', 'persist']) {
    assert.equal(playbooksSessionReducer(session, {type}), session);
  }
  assert.equal(playbooksSessionReducer(session, {type: 'edit_field', field: 'status', value: 'approved'}), session);
});
