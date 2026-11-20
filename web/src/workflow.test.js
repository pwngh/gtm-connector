import test from 'node:test';
import assert from 'node:assert/strict';
import { initialState, transition, derive } from './workflow.js';

const act = (state, type, fields = {}) => transition(state, { type, ...fields });
const authorize = () => act(act(initialState, 'request_review'), 'approve_plan');
const verify = () => act(act(authorize(), 'obtain_document'), 'verify_document');
const approveCustomer = () => act(act(verify(), 'request_customer_review'), 'record_customer_approval', {
  reviewedBy: 'Nina Patel, customer counsel',
  recordedBy: 'Alex Chen',
  evidenceText: 'Demo written approval of AI Terms version v3.',
});

test('a missing document does not block review and acquisition authorization', () => {
  const state = authorize();
  assert.equal(state.document, null);
  assert.equal(state.plan.approvedRevision, 3);
  assert.equal(state.plan.approver, 'Alex Chen');
  assert.equal(derive(state).isPlanApproved, true);
  assert.equal(derive(state).documentStatus, 'Not yet received');
  assert.equal(state.task.id, 'task-obtain-ai-terms');
});

test('the requirement cannot resolve at any stage before customer approval', () => {
  for (const state of [initialState, authorize(), verify(), act(verify(), 'request_customer_review')]) {
    const result = act(state, 'resolve_requirement');
    assert.equal(result.requirement.status, 'open');
    assert.match(result.error, /approval evidence/);
    assert.equal(result.history.length, state.history.length);
  }
});

test('the full workflow resolves legal work while keeping renewal unconfirmed', () => {
  const approved = approveCustomer();
  assert.equal(derive(approved).canResolveRequirement, true);
  assert.equal(approved.customerReview.reviewedBy, 'Nina Patel, customer counsel');
  assert.equal(approved.customerReview.recordedBy, 'Alex Chen');
  const state = act(approved, 'resolve_requirement');
  assert.equal(state.requirement.status, 'resolved');
  assert.equal(state.task.status, 'complete');
  assert.equal(derive(state).taskStage, 'complete');
  assert.equal(state.commercial.status, 'not_confirmed');
  assert.equal(state.commercial.label, 'Renewal not confirmed');
});

test('editing an approved plan increments revision and invalidates dependent approval', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const state = act(completed, 'save_plan', { owner: 'Jordan Ellis' });
  assert.equal(state.plan.revision, 4);
  assert.equal(state.plan.status, 'draft');
  assert.equal(state.plan.approvedRevision, null);
  assert.equal(state.customerReview, null);
  assert.equal(state.requirement.status, 'open');
  assert.equal(state.document.status, 'verified');
  assert.equal(state.task.status, 'blocked');
  assert.equal(derive(state).canResolveRequirement, false);
});

test('work prescriptions require both specifications and instructions', () => {
  for (const field of ['specifications', 'instructions']) {
    const invalid = act(initialState, 'save_plan', { [field]: '   ' });
    assert.match(invalid.error, /specifications, and instructions/);
    assert.equal(invalid.plan, initialState.plan);
    assert.equal(invalid.history.length, 0);
  }
});

test('changing only document specifications reopens resolved work and preserves approved scope', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const originalApproval = completed.history.find(event => event.type === 'approve_plan');
  const originalSnapshot = { ...originalApproval.planSnapshot };
  const updatedSpecifications = 'Confirm Agreement A-17 and include the customer source timestamp.';
  const edited = act(completed, 'save_plan', { specifications: `  ${updatedSpecifications}  ` });
  assert.equal(edited.plan.specifications, updatedSpecifications);
  assert.equal(edited.plan.instructions, initialState.plan.instructions);
  assert.equal(edited.plan.revision, 4);
  assert.equal(edited.plan.status, 'draft');
  assert.equal(edited.plan.approvedRevision, null);
  assert.equal(edited.customerReview, null);
  assert.equal(edited.requirement.status, 'open');
  assert.equal(edited.task.status, 'blocked');
  assert.equal(edited.document.status, 'received');
  assert.equal(edited.document.id, completed.document.id);
  assert.equal(edited.document.version, completed.document.version);
  assert.equal(edited.document.receivedAt, completed.document.receivedAt);
  assert.equal(edited.document.verifiedAt, null);
  assert.equal(edited.document.verifiedBy, null);
  assert.equal(derive(edited).canResolveRequirement, false);
  const saveEvent = edited.history.at(-1);
  assert.deepEqual(saveEvent.changedFields, ['specifications']);
  assert.equal(saveEvent.planSnapshot.specifications, updatedSpecifications);
  assert.equal(saveEvent.planSnapshot.revision, 4);
  assert.notEqual(saveEvent.planSnapshot, edited.plan);
  const reapproved = act(act(edited, 'request_review'), 'approve_plan');
  assert.equal(derive(reapproved).taskStage, 'verify_document');
  assert.equal(derive(reapproved).canRequestCustomerReview, false);
  assert.equal(act(reapproved, 'request_customer_review').customerReview, null);
  const reverified = act(reapproved, 'verify_document');
  assert.equal(derive(reverified).canRequestCustomerReview, true);
  const newApproval = reapproved.history.at(-1);
  assert.equal(newApproval.planSnapshot.specifications, updatedSpecifications);
  assert.equal(newApproval.planSnapshot.instructions, initialState.plan.instructions);
  assert.equal(newApproval.planSnapshot.revision, 4);
  assert.deepEqual(originalApproval.planSnapshot, originalSnapshot);
  assert.equal(originalSnapshot.specifications, initialState.plan.specifications);
  const furtherEdit = act(reapproved, 'save_plan', { instructions: 'Ask Rachel Ellis for a replacement before requesting review.' });
  assert.equal(furtherEdit.plan.status, 'draft');
  assert.deepEqual(furtherEdit.history.at(-1).changedFields, ['instructions']);
  assert.equal(saveEvent.planSnapshot.instructions, initialState.plan.instructions);
  assert.equal(newApproval.planSnapshot.instructions, initialState.plan.instructions);
});

test('older saved plans gain prescription defaults without inventing a new version or invalidating approval', () => {
  const approved = authorize();
  const { specifications, instructions, ...oldPlan } = approved.plan;
  const oldState = { ...approved, plan: oldPlan };
  const saved = act(oldState, 'save_plan', { specifications, instructions });
  assert.equal(saved.error, null);
  assert.equal(saved.plan.revision, oldPlan.revision);
  assert.equal(saved.history, oldState.history);
  assert.equal(saved.plan.approvedRevision, oldPlan.approvedRevision);
  assert.equal(derive(saved).isPlanApproved, true);
  assert.equal(saved.plan.specifications, specifications);
  assert.equal(saved.plan.instructions, instructions);
});

test('duplicate approval and repeated attachment do not duplicate tasks or evidence', () => {
  const approved = authorize();
  const repeatedApproval = act(approved, 'approve_plan');
  assert.equal(repeatedApproval.task, approved.task);
  assert.equal(repeatedApproval.history.length, approved.history.length);
  const attached = act(approved, 'obtain_document');
  const repeatedAttachment = act(attached, 'obtain_document');
  assert.equal(repeatedAttachment.document, attached.document);
  assert.equal(repeatedAttachment.history.length, attached.history.length);
  const edited = act(attached, 'save_plan', { title: 'Obtain current terms and source confirmation' });
  const reapproved = act(act(edited, 'request_review'), 'approve_plan');
  assert.equal(reapproved.task.id, approved.task.id);
  assert.equal(reapproved.task.createdAt, approved.task.createdAt);
  assert.equal(reapproved.task.planRevision, 4);
});

test('an outdated document cannot be verified or sent to customer review', () => {
  const oldDocument = act(authorize(), 'obtain_document', { document: { version: 'v2', currentVersion: 'v3' } });
  const failedVerification = act(oldDocument, 'verify_document');
  assert.equal(failedVerification.document.status, 'received');
  assert.match(failedVerification.error, /not current/);
  assert.equal(derive(failedVerification).canRequestCustomerReview, false);
  const replaced = act(failedVerification, 'obtain_document');
  const verified = act(replaced, 'verify_document');
  assert.equal(verified.document.version, 'v3');
  assert.equal(derive(verified).isDocumentVerified, true);
});

test('customer approval requires a review request and explicit reviewer evidence', () => {
  const premature = act(verify(), 'record_customer_approval', { reviewedBy: 'Nina Patel', evidenceText: 'Approved.' });
  assert.equal(premature.customerReview, null);
  const pending = act(verify(), 'request_customer_review');
  for (const fields of [{}, { reviewedBy: 'Nina Patel' }, { reviewedBy: ' ', evidenceText: 'Approved.' }]) {
    const invalid = act(pending, 'record_customer_approval', fields);
    assert.equal(invalid.customerReview.status, 'pending');
    assert.match(invalid.error, /approval evidence/);
  }
});

test('replacing an approved document reopens work and invalidates customer approval', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const replaced = act(completed, 'obtain_document', { document: { id: 'terms-v4', version: 'v4', currentVersion: 'v4' } });
  assert.equal(replaced.requirement.status, 'open');
  assert.equal(replaced.customerReview, null);
  assert.equal(replaced.document.status, 'received');
  assert.equal(derive(replaced).canResolveRequirement, false);
  assert.equal(derive(replaced).isPlanApproved, true);
});

test('requesting changes, saving, and resubmitting preserves the review gate', () => {
  const review = act(initialState, 'request_review');
  const returned = act(review, 'request_changes', { note: 'Confirm legal owner.' });
  assert.equal(returned.plan.reviewNote, 'Confirm legal owner.');
  assert.equal(act(returned, 'approve_plan').task, null);
  const saved = act(returned, 'save_plan', { owner: 'Jordan Ellis' });
  assert.equal(saved.plan.revision, 4);
  const approved = act(act(saved, 'request_review'), 'approve_plan');
  assert.equal(approved.plan.approvedRevision, 4);
});

test('reopening requires a new counsel review and reset restores the clean demo', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const approvalEvent = completed.history.find(event => event.type === 'record_customer_approval');
  assert.deepEqual(approvalEvent.evidenceSnapshot, {
    evidenceText: completed.customerReview.evidenceText,
    reviewedBy: 'Nina Patel, customer counsel',
    recordedBy: 'Alex Chen',
    documentId: completed.document.id,
    documentVersion: 'v3',
    planRevision: 3,
    approvedAt: completed.customerReview.approvedAt,
  });
  for (const reason of [undefined, ' ']) {
    const invalid = act(completed, 'reopen_requirement', { reason });
    assert.equal(invalid.requirement.status, 'resolved');
    assert.equal(invalid.history.length, completed.history.length);
    assert.match(invalid.error, /reason/);
  }
  const reopened = act(completed, 'reopen_requirement', { reason: 'Customer counsel requested another review.', at: '2026-11-20T10:00:00Z' });
  assert.equal(reopened.customerReview, null);
  assert.equal(derive(reopened).canRequestCustomerReview, true);
  assert.equal(derive(reopened).canResolveRequirement, false);
  assert.equal(reopened.history.at(-1).at, '2026-11-20T10:00:00Z');
  assert.match(reopened.history.at(-1).detail, /Customer counsel requested another review/);
  const edited = act(reopened, 'save_plan', { owner: 'Maya Chen' });
  assert.equal(edited.customerReview, null);
  assert.equal(edited.plan.revision, 4);
  const exportedHistory = JSON.parse(JSON.stringify(edited.history));
  assert.deepEqual(exportedHistory.find(event => event.type === 'record_customer_approval').evidenceSnapshot, approvalEvent.evidenceSnapshot);
  assert.equal(initialState.history.length, 0);
  assert.deepEqual(act(reopened, 'reset'), initialState);
});
