import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState, derive, transition} from './workflow.js';
import {renewalEvidence} from './renewalEvidence.js';
import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';
import {
  buildPlaybookContext, buildTemplateWork, changedDraftFields, createPlaybooksSession, describeDocument,
  draftEditorValues, isDraftContextCurrent, playbookSearch, playbooksSessionReducer, preparePlaybookDraft,
  readPlaybookLocation, savePlaybookDraft, validateDraftEdits,
} from './playbookDrafts.js';

const templateIds = ['obtain_current_rider', 'review_renewal_condition', 'reconcile_approval_conflict'];
const at = '2026-11-20T06:00:00.000Z';
const context = (state = initialState, evidence = renewalEvidence, roles = {}) => buildPlaybookContext(state, evidence, roles);
const prepare = (templateId = templateIds[0], target = context()) => preparePlaybookDraft({templateId, context: target, confirmed: true, id: `test-${templateId}`, at});
const authorized = () => transition(transition(structuredClone(initialState), {type: 'request_review'}), {type: 'approve_plan'});
const verified = () => transition(transition(authorized(), {type: 'obtain_document'}), {type: 'verify_document'});
const completed = () => transition(transition(transition(verified(), {type: 'request_customer_review'}), {
  type: 'record_customer_approval', reviewedBy: 'Nina Patel', evidenceText: 'Synthetic approval of the current terms.',
}), {type: 'resolve_requirement'});
const start = (session = createPlaybooksSession('test-session'), templateId = templateIds[0], target = context()) => playbooksSessionReducer(session, {type: 'prepare', templateId, context: target, confirmed: true, at});

function assertTaskGraph(tasks) {
  const seen = new Set();
  for (const task of tasks) {
    assert.ok(!seen.has(task.id), `Task ID must be unique: ${task.id}`);
    assert.ok(task.title && task.inputs.length && task.output && task.completionRule);
    assert.equal(task.status, 'proposed');
    assert.ok(['actionOwner', 'internalReviewer'].includes(task.role));
    for (const dependency of task.dependsOn) assert.ok(seen.has(dependency), `Task ${task.id} must depend on earlier work, not ${dependency}`);
    seen.add(task.id);
  }
  assert.equal(tasks.at(-1).id, 'inspect_output');
}

function freeze(value) {
  if (value && typeof value === 'object') {Object.values(value).forEach(freeze); Object.freeze(value);}
  return value;
}

test('the three stable templates have distinct work, evidence needs, deliverables, and completion rules', () => {
  assert.deepEqual(playbookTemplates.map(template => template.id), templateIds);
  assert.equal(getPlaybookTemplate('unknown'), null);
  const drafts = templateIds.map(id => prepare(id).draft);
  assert.equal(new Set(drafts.map(draft => draft.output.id)).size, 3);
  assert.equal(new Set(drafts.map(draft => JSON.stringify(draft.tasks))).size, 3);
  assert.equal(new Set(drafts.map(draft => JSON.stringify(draft.completionRules))).size, 3);
  assert.equal(new Set(drafts.map(draft => draft.specifications)).size, 3);
  for (const draft of drafts) {
    assertTaskGraph(draft.tasks);
    assert.equal(draft.templateRef.version, '1.0');
    assert.equal(draft.revision, 1);
    assert.equal(draft.status, 'draft');
    assert.ok(draft.reviewBoundary);
    for (const need of [...draft.evidenceNeeds, ...draft.prerequisites]) assert.ok(draft.tasks.some(task => task.id === need.recoveryTaskId));
  }
  assert.ok(drafts[2].evidenceNeeds.some(need => need.ref === 'E-042'));
  assert.ok(!drafts[0].evidenceNeeds.some(need => need.ref === 'E-042'));
});

test('definitions are deeply immutable and drafts have their own task/output copies', () => {
  const template = getPlaybookTemplate(templateIds[0]);
  assert.throws(() => {template.tasks[0].inputs.push('Changed input');}, TypeError);
  assert.throws(() => {template.completionRules[0].text = 'Complete without proof';}, TypeError);
  const before = structuredClone(template);
  const one = prepare().draft;
  const two = prepare().draft;
  one.tasks.find(task => task.id === 'locate_document').inputs.push('Additional local instruction');
  one.output.title = 'Different local output';
  one.completionRules[0].text = 'Changed copy';
  assert.deepEqual(template, before);
  assert.notDeepEqual(one.tasks, two.tasks);
  assert.equal(two.output.title, template.output.title);
});

test('the context adapter reuses E-017 and E-042 identities and strips presentation-only fields', () => {
  const input = renewalEvidence.map(record => ({...record, icon: () => null}));
  const snapshot = context(initialState, input);
  assert.deepEqual(snapshot.refs, {accountId: 'thornfield', renewalId: '003', agreementId: 'A-17', requirementId: 'req-ai-counsel'});
  assert.deepEqual(snapshot.evidence.map(record => [record.id, record.quote, record.context]), renewalEvidence.map(record => [record.id, record.quote, record.context]));
  assert.equal(snapshot.requirement.id, initialState.requirement.id);
  assert.equal(snapshot.existingPlan.id, initialState.plan.id);
  assert.ok(snapshot.evidence.every(record => !Object.hasOwn(record, 'icon')));
  assert.doesNotThrow(() => structuredClone(snapshot));
  snapshot.evidence[0].quote = 'Local copy';
  assert.notEqual(snapshot.evidence[0].quote, renewalEvidence[0].quote);
});

test('draft preparation requires an explicit confirmation and supported synthetic target', () => {
  for (const confirmed of [undefined, false, 'true']) {
    const result = preparePlaybookDraft({templateId: templateIds[0], context: context(), id: 'draft', at, confirmed});
    assert.equal(result.ok, false);
    assert.match(result.error, /Confirm the synthetic context/);
  }
  for (const badContext of [null, {...context(), synthetic: false}, {...context(), synthetic: 'true'}, {...context(), refs: {...context().refs, agreementId: 'A-99'}}]) {
    assert.equal(preparePlaybookDraft({templateId: templateIds[0], context: badContext, id: 'draft', at, confirmed: true}).ok, false);
  }
  assert.equal(prepare('not-a-template').ok, false);
  assert.equal(preparePlaybookDraft({templateId: templateIds[0], context: context(), confirmed: true, at}).ok, false);
});

test('a missing document permits bounded acquisition without circular approval prerequisites', () => {
  const draft = prepare().draft;
  assert.equal(draft.contextSnapshot.document, null);
  assert.equal(draft.evidenceNeeds.find(need => need.id === 'current_document').state, 'missing');
  assert.deepEqual(draft.tasks.find(task => task.id === 'authorize_scope').dependsOn, []);
  assert.deepEqual(draft.tasks.find(task => task.id === 'locate_document').dependsOn, ['authorize_scope']);
  assert.deepEqual(draft.tasks.find(task => task.id === 'verify_document').dependsOn, ['locate_document']);
  assert.ok(draft.tasks.find(task => task.id === 'prepare_handoff').dependsOn.includes('verify_document'));
  assertTaskGraph(draft.tasks);
  assert.match(draft.output.description, /not customer approval/);
});

test('missing source statements have acquisition work, not invented statements or a blocked draft', () => {
  for (const templateId of templateIds) {
    const result = prepare(templateId, context(initialState, []));
    assert.equal(result.ok, true);
    assert.deepEqual(result.draft.contextSnapshot.evidence, []);
    const missing = result.draft.evidenceNeeds.filter(need => need.id.includes('statement'));
    assert.ok(missing.every(need => need.state === 'missing'));
    assert.ok(missing.every(need => result.draft.tasks.some(task => task.id === need.recoveryTaskId)));
    assertTaskGraph(result.draft.tasks);
  }
});

test('unknown action owner and customer authority create owned discovery steps for every template', () => {
  const target = context(initialState, renewalEvidence, {actionOwner: '', customerAuthority: null});
  for (const templateId of templateIds) {
    const draft = prepare(templateId, target).draft;
    assert.equal(draft.contextSnapshot.roles.actionOwner, null);
    assert.equal(draft.contextSnapshot.roles.customerAuthority, null);
    assert.equal(draft.tasks[0].id, 'assign_owner');
    assert.equal(draft.tasks[0].role, 'internalReviewer');
    assert.deepEqual(draft.tasks[1].dependsOn, ['assign_owner']);
    const authority = draft.tasks.find(task => task.id === 'confirm_authority');
    assert.match(authority.title, /Identify/);
    assert.ok(authority.inputs.some(input => input.includes('not established')));
    assertTaskGraph(draft.tasks);
  }
});

test('naming fixture counsel never authenticates authority or records a customer decision', () => {
  const draft = prepare().draft;
  assert.deepEqual(draft.contextSnapshot.roles, {
    actionOwner: 'Morgan Reed', internalReviewer: 'Alex Chen', customerAuthority: 'Nina Patel',
    authorityBasis: 'Synthetic counsel role only; not authenticated decision authority.',
  });
  assert.equal(draft.contextSnapshot.customerReview, null);
  assert.equal(draft.evidenceNeeds.find(need => need.id === 'customer_authority').state, 'unknown');
  assert.match(draft.tasks.find(task => task.id === 'confirm_authority').completionRule, /not authenticated/);
});

test('stale or unidentified documents produce version recovery work without verification claims', () => {
  const stale = transition(authorized(), {type: 'obtain_document', document: {version: 'v2', currentVersion: 'v3'}});
  for (const templateId of templateIds) {
    const draft = prepare(templateId, context(stale)).draft;
    const need = draft.evidenceNeeds.find(need => need.id === 'current_document');
    assert.equal(need.state, 'stale');
    assert.match(need.detail, /Received v2/);
    assert.ok(draft.tasks.some(task => task.id === need.recoveryTaskId));
    assertTaskGraph(draft.tasks);
  }
  assert.match(prepare(templateIds[0], context(stale)).draft.tasks.find(task => task.id === 'locate_document').title, /replacement/);
  assert.equal(describeDocument({...stale.document, currentVersion: null, status: 'verified'}).state, 'unknown');
  assert.equal(describeDocument({...stale.document, id: null, status: 'verified'}).state, 'unknown');
  assert.equal(describeDocument({...stale.document, status: 'verified'}).state, 'stale');
  assert.equal(derive(stale).canRequestCustomerReview, false);
});

test('receipt, verification, existing customer approval, resolution and renewal remain separate', () => {
  const received = transition(authorized(), {type: 'obtain_document'});
  assert.equal(describeDocument(received.document).state, 'received');
  assert.equal(describeDocument(verified().document).state, 'verified');
  const state = completed();
  const draft = prepare(templateIds[1], context(state)).draft;
  assert.equal(draft.contextSnapshot.requirement.status, 'resolved');
  assert.equal(draft.contextSnapshot.customerReview.status, 'approved');
  assert.equal(draft.contextSnapshot.commercialStatus, 'not_confirmed');
  assert.equal(draft.status, 'draft');
  assert.equal(draft.prerequisites.find(item => item.id === 'internal_authorization').status, 'Not requested for this draft');
  assert.ok(draft.tasks.every(task => task.status === 'proposed'));
});

test('the reconciliation example compares an expectation, not a fabricated second approval', () => {
  const draft = prepare(templateIds[2]).draft;
  assert.equal(draft.contextSnapshot.evidence[1].id, 'E-042');
  assert.equal(draft.contextSnapshot.evidence[1].meaning, 'Seller expectation');
  assert.match(draft.instructions, /seller forecast, not a second approval/);
  assert.ok(draft.tasks.find(task => task.id === 'draft_reconciliation').completionRule.includes('unresolved'));
  assert.match(draft.reviewBoundary, /does not create or revoke customer approval/);
  assert.equal(Object.hasOwn(draft, 'reconciled'), false);
});

test('selection, preparation, editing and cancellation never mutate approved or resolved work', () => {
  const state = freeze(completed());
  const before = structuredClone(state);
  const gates = derive(state);
  for (const templateId of templateIds) {
    const target = context(state);
    buildTemplateWork(templateId, target);
    let session = start(createPlaybooksSession(templateId), templateId, target);
    const url = playbookSearch(templateId, 'edit', session.draft.id);
    assert.equal(readPlaybookLocation(url, session).view, 'edit');
    session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'A human refinement to the proposed work.'});
    session = playbooksSessionReducer(session, {type: 'save_edits', at});
    session = playbooksSessionReducer(session, {type: 'discard_confirmed'});
    assert.equal(session.draft, null);
    assert.deepEqual(state, before);
    assert.deepEqual(derive(state), gates);
  }
});

test('draft, template, existing plan, and document versions are independent', () => {
  const state = verified();
  const original = prepare(templateIds[0], context(state)).draft;
  const edited = savePlaybookDraft(original, {...draftEditorValues(original), specifications: 'Keep the applicable file and a precise source reference.'}, at).draft;
  assert.equal(edited.revision, 2);
  assert.equal(original.revision, 1);
  assert.equal(edited.templateRef.version, '1.0');
  assert.equal(edited.contextSnapshot.existingPlan.revision, 3);
  assert.equal(edited.contextSnapshot.document.version, 'v3');
  assert.equal(state.plan.approvedRevision, 3);
});

test('human edits preserve before/after, provenance, context refs, tasks and completion rules', () => {
  const original = prepare().draft;
  const before = structuredClone(original);
  const values = {...draftEditorValues(original), title: '  Collect the applicable AI terms with source proof  ', instructions: 'Ask for a receipt timestamp as well as the file.'};
  const result = savePlaybookDraft(original, values, at);
  assert.equal(result.ok, true);
  const edited = result.draft;
  assert.equal(edited.title, values.title.trim());
  assert.deepEqual(edited.templateRef, original.templateRef);
  assert.deepEqual(edited.contextSnapshot.refs, original.contextSnapshot.refs);
  assert.deepEqual(edited.completionRules, original.completionRules);
  assert.deepEqual(edited.tasks, original.tasks);
  assert.deepEqual(edited.humanEdits[0].changes.map(change => change.field), ['title', 'instructions']);
  assert.equal(edited.humanEdits[0].changes[1].before, original.instructions);
  assert.equal(edited.humanEdits[0].changes[1].after, values.instructions);
  assert.equal(edited.humanEdits[0].actor, 'Local user; not authenticated');
  assert.deepEqual(original, before);
  assert.equal(getPlaybookTemplate(templateIds[0]).instructions, original.instructions);
});

test('whitespace-only no-op edits do not manufacture revisions or history', () => {
  const draft = prepare().draft;
  const values = Object.fromEntries(Object.entries(draftEditorValues(draft)).map(([key, value]) => [key, `  ${value}  `]));
  assert.deepEqual(changedDraftFields(draft, values), []);
  const result = savePlaybookDraft(draft, values, at);
  assert.equal(result.draft, draft);
  assert.equal(result.draft.humanEdits.length, 0);
});

test('blank, overlong, non-text and scope-changing edits are rejected without altering the draft', () => {
  const draft = freeze(prepare().draft);
  for (const values of [
    {...draftEditorValues(draft), title: ' '},
    {...draftEditorValues(draft), specifications: ''},
    {...draftEditorValues(draft), instructions: '\n\t'},
    {...draftEditorValues(draft), title: 'x'.repeat(161)},
    {...draftEditorValues(draft), instructions: 'x'.repeat(1201)},
    {...draftEditorValues(draft), specifications: null},
    {...draftEditorValues(draft), status: 'approved'},
    {...draftEditorValues(draft), completionRules: []},
  ]) {
    assert.ok(Object.keys(validateDraftEdits(values)).length > 0);
    assert.equal(savePlaybookDraft(draft, values, at).ok, false);
    assert.equal(draft.revision, 1);
  }
  assert.equal(savePlaybookDraft({...draft, status: 'approved'}, draftEditorValues(draft), at).ok, false);
});

test('draft context is a snapshot; live changes are detected without silently rebasing it', () => {
  const target = context();
  const draft = prepare(templateIds[0], target).draft;
  assert.equal(isDraftContextCurrent(draft, context()), true);
  assert.equal(isDraftContextCurrent(draft, context(initialState, renewalEvidence, {actionOwner: null, customerAuthority: null})), true);
  assert.equal(isDraftContextCurrent(draft, context(verified())), false);
  target.evidence[0].quote = 'Changed caller copy';
  target.roles.actionOwner = 'Different proposed owner';
  assert.equal(draft.contextSnapshot.evidence[0].quote, renewalEvidence[0].quote);
  assert.equal(draft.contextSnapshot.roles.actionOwner, 'Morgan Reed');
});

test('the session retains unapplied edits during selection and navigation; Back can inspect the saved revision', () => {
  let session = start();
  const draftId = session.draft.id;
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'An unfinished human instruction.'});
  const before = structuredClone(session);
  readPlaybookLocation(playbookSearch(templateIds[1]), session);
  readPlaybookLocation(playbookSearch(templateIds[0], 'context'), session);
  assert.equal(readPlaybookLocation(playbookSearch(templateIds[0], 'edit', draftId), session).view, 'edit');
  assert.equal(readPlaybookLocation(playbookSearch(templateIds[0], 'inspect', draftId), session).view, 'inspect');
  assert.equal(session.editor.instructions, 'An unfinished human instruction.');
  assert.notEqual(session.draft.instructions, session.editor.instructions);
  assert.deepEqual(session, before);
});

test('saving text edits keeps the draft unapproved and reverting only discards unapplied edits', () => {
  let session = start();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: 'Human draft title'});
  session = playbooksSessionReducer(session, {type: 'save_edits', at});
  assert.equal(session.draft.revision, 2);
  assert.equal(session.draft.title, 'Human draft title');
  assert.equal(session.draft.status, 'draft');
  const applied = session.draft;
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'title', value: ''});
  session = playbooksSessionReducer(session, {type: 'save_edits', at});
  assert.ok(session.fieldErrors.title);
  assert.equal(session.draft, applied);
  session = playbooksSessionReducer(session, {type: 'revert_edits'});
  assert.equal(session.editor.title, 'Human draft title');
  assert.equal(session.draft, applied);
  assert.equal(session.error, null);
});

test('preparing a second draft cannot silently replace the retained draft or its edits', () => {
  let session = start();
  session = playbooksSessionReducer(session, {type: 'edit_field', field: 'instructions', value: 'Keep this unsaved edit.'});
  const result = start(session, templateIds[2]);
  assert.equal(result.draft, session.draft);
  assert.equal(result.editor, session.editor);
  assert.equal(result.nextDraftNumber, session.nextDraftNumber);
  assert.match(result.error, /Resume or discard/);
});

test('cancel removes only the draft; a new draft has a distinct identity and old URLs cannot bind to it', () => {
  const first = start();
  const oldUrl = playbookSearch(templateIds[0], 'edit', first.draft.id);
  const cancelled = playbooksSessionReducer(first, {type: 'discard_confirmed'});
  assert.equal(cancelled.draft, null);
  assert.equal(cancelled.editor, null);
  assert.equal(readPlaybookLocation(oldUrl, cancelled).view, 'unavailable');
  const second = start(cancelled);
  assert.notEqual(second.draft.id, first.draft.id);
  assert.equal(readPlaybookLocation(oldUrl, second).view, 'unavailable');
  assert.equal(second.draft.humanEdits.length, 0);
  assert.equal(second.draft.revision, 1);
});

test('reload loses temporary drafts and buffers; selection URLs survive but draft links recover honestly', () => {
  const current = start();
  const editUrl = playbookSearch(templateIds[0], 'edit', current.draft.id);
  const reloaded = createPlaybooksSession('new-page-mount');
  assert.equal(reloaded.draft, null);
  assert.equal(reloaded.editor, null);
  assert.equal(readPlaybookLocation(editUrl, reloaded).view, 'unavailable');
  const sameTemplateNewDraft = start(reloaded);
  assert.notEqual(sameTemplateNewDraft.draft.id, current.draft.id);
  assert.equal(readPlaybookLocation(editUrl, sameTemplateNewDraft).view, 'unavailable');
  assert.deepEqual(readPlaybookLocation(playbookSearch(templateIds[2]), reloaded), {templateId: templateIds[2], view: 'template', invalidSelection: false});
});

test('invalid locations and mismatched template/draft URLs never select a different draft silently', () => {
  const session = start();
  const mismatch = playbookSearch(templateIds[1], 'inspect', session.draft.id);
  assert.equal(readPlaybookLocation(mismatch, session).view, 'unavailable');
  assert.equal(readPlaybookLocation(playbookSearch('not-supported', 'edit', session.draft.id), session).view, 'unavailable');
  assert.equal(readPlaybookLocation(playbookSearch(templateIds[0], 'inspect', 'missing'), session).view, 'unavailable');
  const invalid = readPlaybookLocation('template=not-supported&view=publish', session);
  assert.equal(invalid.templateId, templateIds[0]);
  assert.equal(invalid.view, 'template');
  assert.equal(invalid.invalidSelection, true);
});

test('this session exposes no execution, approval, send, persistence, or renewal transition', () => {
  const session = freeze(start());
  for (const type of ['approve_plan', 'request_review', 'send', 'verify_document', 'record_customer_approval', 'resolve_requirement', 'publish', 'persist']) {
    assert.equal(playbooksSessionReducer(session, {type}), session);
  }
  assert.equal(playbooksSessionReducer(session, {type: 'edit_field', field: 'status', value: 'approved'}), session);
});
