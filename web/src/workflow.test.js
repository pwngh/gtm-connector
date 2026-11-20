import test from 'node:test';
import assert from 'node:assert/strict';
import { initialState, transition, derive } from './workflow.js';
import { customerDecisionSteps, walkthroughFields } from './customerDecision.js';
import { approvalLetters, fixtureEvidence, readinessFixture, walkthroughActors, walkthroughDocumentExamples } from './renewalFixtures.js';
import { walkthroughAction } from './testing/walkthroughInputs.js';

const act = (state, type, fields = {}) => transition(state, { type, ...fields });
// A walkthrough step with its signed-in principal and the reason the operator typed.
const step = (state, type, fields = {}) => transition(state, walkthroughAction(state, type, fields));
const authorize = () => act(act(initialState, 'request_review'), 'approve_plan');
const verify = () => step(step(authorize(), 'obtain_document'), 'verify_document');
const requested = () => act(verify(), 'request_customer_review');
const through = (state, last) => customerDecisionSteps.slice(0, customerDecisionSteps.indexOf(last) + 1).reduce(step, state);
const approveCustomer = () => through(requested(), 'record_customer_approval');
const counsel = approvalLetters[0].signatory.name;

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
  assert.equal(approved.customerReview.reviewedBy, counsel);
  assert.equal(approved.customerReview.recordedBy, 'Alex Chen');
  assert.deepEqual(approved.customerReview.evidenceRef, {kind: 'receipt', id: derive(approved).approvalLetter.id});
  const state = act(approved, 'resolve_requirement');
  assert.equal(state.requirement.status, 'resolved');
  assert.deepEqual(state.task, approved.task);
  assert.equal(state.task.status, 'open');
  assert.equal(derive(state).taskStage, 'complete');
  assert.equal(state.commercial.status, 'not_confirmed');
  assert.equal(state.commercial.label, 'Renewal not confirmed');
});

test('an owner edit needs plan reapproval but keeps the customer decision and the requirement', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const state = act(completed, 'save_plan', { owner: 'Maya Chen' });
  assert.equal(state.plan.revision, 4);
  assert.equal(state.plan.status, 'draft');
  assert.equal(state.plan.approvedRevision, null);
  assert.equal(state.plan.ownerId, 'principal-portfolio-2');
  assert.deepEqual(state.customerReview, completed.customerReview);
  assert.equal(state.requirement.status, 'resolved');
  assert.equal(state.document.status, 'verified');
  assert.equal(state.task.status, 'blocked');
  assert.equal(derive(state).hasCustomerApproval, true);
  assert.equal(derive(state).requirementStatus, 'resolved');
  assert.match(act(completed, 'save_plan', { owner: 'Jordan Ellis' }).error, /principals/);
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
  const edited = step(completed, 'save_plan', { specifications: `  ${updatedSpecifications}  ` });
  assert.equal(edited.plan.specifications, updatedSpecifications);
  assert.equal(edited.plan.instructions, initialState.plan.instructions);
  assert.equal(edited.plan.revision, 4);
  assert.equal(edited.plan.status, 'draft');
  assert.equal(edited.plan.approvedRevision, null);
  assert.equal(edited.customerReview.status, 'needs_re_review');
  assert.equal(edited.customerReview.id, completed.customerReview.id);
  assert.equal(edited.requirement.status, 'open');
  assert.equal(edited.requirement.reopenedBy, 'Morgan Reed');
  assert.match(edited.requirement.reopenReason, /specifications changed/);
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
  assert.equal(act(reapproved, 'request_customer_review').customerReview, edited.customerReview);
  const reverified = step(reapproved, 'verify_document');
  assert.equal(derive(reverified).canRequestCustomerReview, false);
  assert.equal(derive(reverified).nextCommand, 'record_customer_approval');
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
  assert.match(repeatedAttachment.error, /already received as receipt/);
  const edited = act(attached, 'save_plan', { title: 'Obtain current terms and source confirmation' });
  const reapproved = act(act(edited, 'request_review'), 'approve_plan');
  assert.equal(reapproved.task.id, approved.task.id);
  assert.equal(reapproved.task.createdAt, approved.task.createdAt);
  assert.equal(reapproved.task.planRevision, 4);
});

test('an outdated document cannot be verified or sent to customer review', () => {
  const oldDocument = act(authorize(), 'obtain_document', { document: { version: 'v3', currentVersion: 'v4' } });
  const failedVerification = step(oldDocument, 'verify_document');
  assert.equal(failedVerification.document.status, 'received');
  assert.match(failedVerification.error, /Agreement A-17 names version v4/);
  assert.equal(derive(failedVerification).canRequestCustomerReview, false);
  assert.equal(derive(failedVerification).canRejectDocument, true);
  const replaced = act(failedVerification, 'obtain_document');
  const verified = step(replaced, 'verify_document');
  assert.equal(verified.document.version, 'v4');
  assert.equal(derive(verified).isDocumentVerified, true);
});

test('customer approval requires a review request, an authority record and a cited receipt, never typed text', () => {
  const premature = act(verify(), 'record_customer_approval', { reviewedBy: counsel, evidenceText: 'Approved.' });
  assert.equal(premature.customerReview, null);
  const pending = requested();
  for (const fields of [{}, { reviewedBy: counsel }, { reviewedBy: ' ', evidenceText: 'Approved.' }]) {
    const invalid = act(pending, 'record_customer_approval', fields);
    assert.equal(invalid.customerReview.status, 'pending');
    assert.match(invalid.error, /No usable signed approval letter/);
  }
  const blockers = derive(pending).transitions.record_customer_approval.blockedReasons.map(reason => reason.code);
  assert.deepEqual(blockers, ['letter_receipt', 'letter_accepted', 'customer_authority']);
  const authorized = through(pending, 'record_customer_authority');
  assert.equal(derive(authorized).canRecordCustomerApproval, true);
  const typed = act(authorized, 'record_customer_approval', { reviewedBy: counsel, evidenceText: 'Counsel approved rider v4.' });
  assert.match(typed.error, /Typed text is not a customer decision/);
  assert.equal(typed.customerReview.status, 'pending');
  const fields = walkthroughFields(authorized, 'record_customer_approval');
  const ownReceipt = act(authorized, 'record_customer_approval', { ...fields, evidenceRef: { kind: 'receipt', id: authorized.document.receiptId } });
  assert.match(ownReceipt.error, /document receipt cannot record/);
  assert.match(act(authorized, 'record_customer_approval', { ...fields, contactId: 'contact-unknown' }).error, /not signed by the approving contact/);
  assert.match(act(authorized, 'record_customer_approval', { ...fields, actorId: undefined, actor: counsel }).error, /Name the principal/);
  assert.match(act(authorized, 'record_customer_approval', { ...fields, documentVersion: 'v3' }).error, /version under review/);
  assert.equal(act(authorized, 'record_customer_approval', fields).customerReview.status, 'approved');
});

test('an upload records a receipt, inspection rejects with a reason, and a replacement never overwrites it', () => {
  const received = step(authorize(), 'obtain_document');
  const [receipt] = received.evidence.artifactReceipts;
  assert.deepEqual({ id: receipt.id, version: receipt.documentVersion, uploadedBy: receipt.uploadedBy, uploadedById: receipt.uploadedById, receivedAt: receipt.receivedAt, inspections: receipt.inspections },
    { id: received.document.receiptId, version: 'v4', uploadedBy: 'Morgan Reed', uploadedById: walkthroughActors.acquisitionId, receivedAt: received.document.receivedAt, inspections: [] });
  assert.match(receipt.checksum, /^sha256:/);
  assert.equal(derive(received).canRequestCustomerReview, false);
  assert.match(act(received, 'reject_document', { reason: ' ' }).error, /reason/);
  const rejectedVersion = step(received, 'reject_document', { reason: 'The file is unsigned.' });
  assert.equal(rejectedVersion.document.status, 'rejected');
  assert.deepEqual(rejectedVersion.evidence.artifactReceipts[0].inspections, [{ status: 'rejected', at: rejectedVersion.history.at(-1).at, recordedBy: 'Morgan Reed', recordedById: walkthroughActors.acquisitionId, reason: 'The file is unsigned.' }]);
  assert.equal(derive(rejectedVersion).canVerifyDocument, false);
  assert.equal(derive(rejectedVersion).nextCommand, 'obtain_document');
  assert.match(step(rejectedVersion, 'obtain_document', { document: fixtureEvidence[2] }).error, /rejected at inspection \(same checksum\)/);
  // Without a named file, the customer's distinct copy is the replacement.
  const again = step(rejectedVersion, 'obtain_document');
  assert.equal(again.document.checksum, walkthroughDocumentExamples.rescan.checksum);
  assert.equal(again.evidence.artifactReceipts.length, 2);
  assert.notEqual(again.document.receiptId, receipt.id);
  assert.deepEqual(again.evidence.artifactReceipts[0], rejectedVersion.evidence.artifactReceipts[0]);
  assert.match(act(again, 'verify_document').error, /typed reason/);
  const accepted = step(again, 'verify_document');
  assert.equal(accepted.evidence.artifactReceipts[1].inspections[0].status, 'accepted');
  assert.equal(derive(accepted).isDocumentVerified, true);
});

test('the counsel is named only by the authority record, attributed to the inspected letter', () => {
  const pending = requested();
  assert.equal(derive(pending).nextCommand, 'upload_approval_letter');
  assert.equal(act(pending, 'record_customer_authority').error !== null, true);
  const received = step(pending, 'upload_approval_letter');
  assert.equal(derive(received).nextCommand, 'inspect_approval_letter');
  assert.match(step(received, 'inspect_approval_letter', { reason: ' ' }).error, /reason/);
  const inspected = step(received, 'inspect_approval_letter');
  const letter = derive(inspected).approvalLetter;
  assert.equal(letter.inspections.at(-1).status, 'accepted');
  // The version basis is the agreement record, already passing before any letter; the letter does not establish it.
  assert.equal(inspected.evidence.documentVersionBasis, undefined);
  assert.equal(derive(pending).evidenceChecks.current_version_basis.result, 'passed');
  assert.equal(derive(inspected).evidenceChecks.current_version_basis.result, 'passed');
  for (const state of [pending, received, inspected]) assert.equal(JSON.stringify(state).includes(counsel), false);
  const recorded = step(inspected, 'record_customer_authority');
  const [contact] = recorded.evidence.contacts;
  assert.deepEqual({ name: contact.displayName, recordedBy: contact.recordedBy, sourceRef: contact.sourceRef }, { name: counsel, recordedBy: 'Alex Chen', sourceRef: { kind: 'receipt', id: letter.id } });
  assert.deepEqual(recorded.evidence.customerAuthority.evidenceRef, { kind: 'receipt', id: letter.id });
  assert.equal(recorded.evidence.customerAuthority.agreementId, 'A-17');
  assert.deepEqual([recorded.evidence.customerAuthority.documentId, recorded.evidence.customerAuthority.version], ['rider-v4', 'v4']);
  assert.equal(derive(recorded).authorityContact.displayName, counsel);
  assert.equal(derive(recorded).nextCommand, 'record_customer_approval');
});

test('resolution needs the authority record, the cited decision and an independent version basis', () => {
  const approved = approveCustomer();
  const blockers = state => derive(state).transitions.resolve_requirement.blockedReasons.map(reason => reason.code);
  assert.deepEqual(blockers(approved), []);
  const withoutBasis = derive(approved, undefined, { ...readinessFixture, documentVersionBasis: null });
  assert.deepEqual(withoutBasis.transitions.resolve_requirement.blockedReasons.map(reason => reason.code), ['current_version_basis']);
  assert.match(withoutBasis.transitions.resolve_requirement.refusal, /approval evidence/);
  const withoutAuthority = { ...approved, evidence: { ...approved.evidence, customerAuthority: null } };
  assert.deepEqual(blockers(withoutAuthority), ['customer_approval', 'customer_authority', 'decision_citation']);
  // Authority covers v4 only: a v5 replacement is not current under the agreement record, and nobody is named for it.
  const replaced = step(act(approved, 'resolve_requirement'), 'obtain_document', { document: walkthroughDocumentExamples.replacement });
  assert.match(step(replaced, 'verify_document').error, /Agreement A-17 names version v4/);
  assert.equal(derive(replaced).evidenceChecks.current_version_basis.result, 'failed');
  assert.equal(derive(replaced).authorityContact, null);
  assert.equal(derive(replaced).canRecordCustomerApproval, false);
});

test('replacing an approved document reopens work and marks the customer decision for re-review', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const replaced = step(completed, 'obtain_document', { document: { id: 'terms-v4', version: 'v4', currentVersion: 'v4' } });
  assert.equal(replaced.requirement.status, 'open');
  assert.equal(replaced.customerReview.status, 'needs_re_review');
  assert.match(replaced.requirement.reopenReason, /replaced the inspected document/);
  assert.equal(replaced.document.status, 'received');
  assert.equal(derive(replaced).canResolveRequirement, false);
  assert.equal(derive(replaced).isPlanApproved, true);
});

test('requesting changes, saving, and resubmitting preserves the review gate', () => {
  const review = act(initialState, 'request_review');
  const returned = act(review, 'request_changes', { note: 'Confirm legal owner.' });
  assert.equal(returned.plan.reviewNote, 'Confirm legal owner.');
  assert.equal(act(returned, 'approve_plan').task, null);
  const saved = act(returned, 'save_plan', { owner: 'Maya Chen' });
  assert.equal(saved.plan.revision, 4);
  const approved = act(act(saved, 'request_review'), 'approve_plan');
  assert.equal(approved.plan.approvedRevision, 4);
});

test('reopening needs a reason and an actor, keeps the decision for re-review, and reset restores the clean demo', () => {
  const completed = act(approveCustomer(), 'resolve_requirement');
  const approvalEvent = completed.history.find(event => event.type === 'record_customer_approval');
  assert.deepEqual(approvalEvent.evidenceSnapshot, {
    reviewedBy: counsel,
    contactId: approvalLetters[0].contactId,
    recordedBy: 'Alex Chen',
    evidenceRef: completed.customerReview.evidenceRef,
    documentId: completed.document.id,
    documentVersion: 'v4',
    planRevision: 3,
    approvedAt: completed.customerReview.approvedAt,
  });
  for (const reason of [undefined, ' ']) {
    const invalid = step(completed, 'reopen_requirement', { reason });
    assert.equal(invalid.requirement.status, 'resolved');
    assert.equal(invalid.history.length, completed.history.length);
    assert.match(invalid.error, /reason/);
  }
  assert.match(act(completed, 'reopen_requirement', { reason: 'Another review.' }).error, /Name the principal reopening/);
  const reopened = step(completed, 'reopen_requirement', { reason: 'Customer counsel requested another review.', at: '2026-11-20T10:00:00Z' });
  assert.equal(reopened.customerReview.status, 'needs_re_review');
  assert.equal(reopened.requirement.reopenedById, walkthroughActors.reviewerId);
  assert.equal(derive(reopened).canRequestCustomerReview, false);
  assert.equal(derive(reopened).canRecordCustomerApproval, true);
  assert.equal(derive(reopened).canResolveRequirement, false);
  assert.equal(reopened.history.at(-1).at, '2026-11-20T10:00:00Z');
  assert.match(reopened.history.at(-1).detail, /Customer counsel requested another review/);
  const edited = act(reopened, 'save_plan', { owner: 'Maya Chen' });
  assert.equal(edited.customerReview, reopened.customerReview);
  assert.equal(edited.plan.revision, 4);
  const exportedHistory = JSON.parse(JSON.stringify(edited.history));
  assert.deepEqual(exportedHistory.find(event => event.type === 'record_customer_approval').evidenceSnapshot, approvalEvent.evidenceSnapshot);
  assert.equal(initialState.history.length, 0);
  assert.deepEqual(act(reopened, 'reset'), initialState);
});

test('resolution preserves the stored task and commercial records for every task status', () => {
  const approved = approveCustomer();
  for (const status of ['open', 'blocked', 'complete']) {
    const state = {...approved, task: {...approved.task, status}};
    const result = act(state, 'resolve_requirement');
    assert.equal(result.error, null);
    assert.equal(result.requirement.status, 'resolved');
    assert.equal(result.task, state.task);
    assert.equal(result.commercial, state.commercial);
    assert.equal(result.customerReview, state.customerReview);
    assert.equal(result.document, state.document);
  }
  const noTask = {...approved, task: null};
  assert.equal(act(noTask, 'resolve_requirement').task, null);
});

test('a typed decision saved by the earlier walkthrough is not a decision and does not strand the review', () => {
  const pending = requested();
  const saved = { ...pending, customerReview: { ...pending.customerReview, status: 'approved', reviewedBy: counsel, recordedBy: 'Alex Chen', evidenceText: 'Approved.', approvedAt: pending.customerReview.requestedAt } };
  assert.equal(derive(saved).hasCustomerApproval, false);
  assert.equal(derive(saved).canResolveRequirement, false);
  assert.equal(derive(saved).nextCommand, 'upload_approval_letter');
  const decided = through(saved, 'record_customer_approval');
  assert.equal(decided.error, null);
  assert.equal(decided.customerReview.evidenceText, null);
  assert.equal(derive(decided).hasCustomerApproval, true);
});
