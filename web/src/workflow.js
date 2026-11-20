import {approvalLetters, fixtureEvidence, initialState, fixturePeople, readinessFixture, renewalFixture, walkthroughDocumentExamples, walkthroughPrincipals} from './renewalFixtures.js';
import {compactHistoryState, createHistoryEntryId, recordHistoryInputs} from './historyRecording.js';
import {citationIssues, emptyEvidence, evaluateEvidenceCheck, latestInspection, receiptInspections, supportedAuthority, workflowEvidence} from './evidenceChecks.js';
export {fixtureEvidence, initialState} from './renewalFixtures.js';

// The rules this reducer applies. A saved walkthrough records the version it was recorded under.
export const workflowPolicyVersion = initialState.policyVersion;

// One mapping from reducer command to readiness check, shared by Readiness, the Work queue and History.
export const workflowTransitionChecks = {
  request_review: 'submit_plan', approve_plan: 'approve_plan', obtain_document: 'receive_document',
  verify_document: 'verify_document', reject_document: 'verify_document', request_customer_review: 'request_customer_review',
  upload_approval_letter: 'receive_approval_letter', inspect_approval_letter: 'inspect_approval_letter',
  record_customer_authority: 'record_customer_authority',
  record_customer_approval: 'record_customer_decision', resolve_requirement: 'resolve_requirement',
};

// Evidence checks each transition requires. The reducer refuses the transition until they pass.
export const transitionEvidenceChecks = {
  record_customer_approval: ['customer_authority', 'current_version_basis'],
  resolve_requirement: ['customer_authority', 'decision_citation', 'current_version_basis'],
};

const text = value => typeof value === 'string' && value.trim() ? value.trim() : null;
const evidenceOf = state => ({...emptyEvidence, authorityRecords: [], ...(state.evidence || {})});
const clock = state => new Date(Date.parse(renewalFixture.workflowClockStart) + state.history.length * 60000).toISOString();

// Principals are compared by ID. A display name never establishes who acted.
export const principalById = id => typeof id === 'string' ? walkthroughPrincipals.find(person => person.id === id) || null : null;
const principalFor = action => principalById(action.actorId);
// Only for plan-owner selection and older saved records, which carry a name and no principal ID.
const principalNamed = name => walkthroughPrincipals.find(person => person.displayName === text(name)) || null;

// The walkthrough's letter file a receipt claims to be, if this walkthrough has it.
export function letterArtifact(receipt) {
  return receipt ? approvalLetters.find(letter => letter.artifactId === receipt.artifactId) || null : null;
}

// A letter receipt is usable only if its artifact, checksum and scope match a walkthrough letter for this version.
function letterReceiptIssue(receipt, document) {
  const letter = letterArtifact(receipt);
  if (!letter) return `Receipt ${receipt.id} names an artifact this walkthrough does not have, so its contents cannot be inspected.`;
  if (receipt.checksum !== letter.checksum) return `Receipt ${receipt.id} has a checksum that does not match the walkthrough’s file for that artifact, so its contents cannot be inspected.`;
  const scope = receipt.approves || {};
  if (scope.agreementId !== letter.approves.agreementId || scope.documentId !== letter.approves.documentId || scope.version !== letter.approves.version) return `Receipt ${receipt.id} records a scope that differs from its letter.`;
  if (scope.agreementId !== renewalFixture.agreement.id || scope.documentId !== document?.id || scope.version !== document?.version) return `Receipt ${receipt.id} does not cover the document version under review.`;
  return null;
}

// Receipts whose authority was withdrawn are not a basis again; a new letter is needed.
function withdrawnBases(state) {
  return new Set(evidenceOf(state).authorityRecords.filter(record => record.status === 'withdrawn').map(record => record.evidenceRef?.id));
}

// The latest letter receipt for the version under review that is neither rejected nor a withdrawn authority basis.
function coveringLetter(state) {
  const doc = state.document;
  if (!doc) return null;
  const withdrawn = withdrawnBases(state);
  return evidenceOf(state).artifactReceipts.filter(receipt => receipt.kind === 'approval_letter' && receipt.approves?.documentId === doc.id
    && receipt.approves?.version === doc.version && latestInspection(receipt)?.status !== 'rejected' && !withdrawn.has(receipt.id)).at(-1) || null;
}

// The next signed file the walkthrough's customer has supplied for this version and that has not been received.
export function approvalLetterFor(state) {
  const doc = state?.document;
  if (!doc) return null;
  const received = new Set(evidenceOf(state).artifactReceipts.map(receipt => receipt.checksum));
  return approvalLetters.find(letter => letter.approves.documentId === doc.id && letter.approves.version === doc.version && !received.has(letter.checksum)) || null;
}

// The next walkthrough document file not yet received. A rejected file never comes back as its own replacement.
export function documentFileFor(state) {
  const received = new Set(evidenceOf(state).artifactReceipts.map(receipt => receipt.checksum));
  return [fixtureEvidence[2], walkthroughDocumentExamples.rescan].find(file => !received.has(file.checksum)) || null;
}

function receiptId(state, artifactId) {
  const count = evidenceOf(state).artifactReceipts.filter(receipt => receipt.artifactId === artifactId).length;
  return `receipt-${artifactId.replace(/^artifact-/, '')}-${count + 1}`;
}

// Synthetic, labelled checksum for a walkthrough file that does not carry one.
function syntheticChecksum(value) {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i++) hash = Math.imul(hash ^ value.charCodeAt(i), 16777619);
  return `synthetic:${(hash >>> 0).toString(16).padStart(8, '0')}`;
}

// A file with the same checksum as an earlier receipt is the same file, never a new one.
function duplicateFile(state, checksum) {
  const match = evidenceOf(state).artifactReceipts.filter(receipt => receipt.checksum === checksum).at(-1);
  if (!match) return null;
  return latestInspection(match)?.status === 'rejected'
    ? `This file matches receipt ${match.id}, which was rejected at inspection (same checksum). Attach a distinct replacement file.`
    : `This file is already received as receipt ${match.id} (same checksum). Nothing was added.`;
}

function localTransitions(state, d) {
  const gate = (met, name, reason, checkId, evidenceGap = false) => ({met, name, reason, checkId, ...(evidenceGap ? {evidenceGap} : {})});
  const version = state.document?.version || 'unknown';
  const rider = renewalFixture.agreement.applicableRider;
  const letter = d.approvalLetter;
  const letterIssue = letter ? letterReceiptIssue(letter, state.document) : null;
  const evidence = id => gate(d.evidenceChecks[id].result === 'passed', readinessFixture.checks.find(check => check.id === id)?.title || id, d.evidenceChecks[id].explanation, id, true);
  const gates = {
    independent_reviewer: gate(d.isReviewerEligible, 'Independent internal reviewer', d.planReviewBlockedReason, 'submit_plan'),
    reviewable_plan: gate(['draft', 'changes_requested'].includes(state.plan.status), 'Plan ready for submission', 'The plan must be a draft or have changes requested before submission.', 'submit_plan'),
    plan_submission: gate(state.plan.status === 'in_review', 'Current plan submitted for review', 'Submit the current plan revision before it can be approved.', 'submit_plan'),
    plan_approval: gate(d.isPlanApproved, 'Current plan approval', 'The current plan revision needs internal approval.', 'approve_plan'),
    plan_owner_principal: gate(Boolean(principalById(state.plan.ownerId)), 'Plan owner recorded as a principal', 'The plan owner is not a recorded principal, so separation of duties cannot be checked. Save the plan with a task owner first.', 'approve_plan'),
    document_receipt: gate(Boolean(state.document), 'Document receipt', 'The applicable document has not been received.', 'receive_document'),
    document_not_rejected: gate(!d.documentRejected, 'Received version not rejected', `Version ${version} was rejected at inspection. Its receipt is kept. Attach a distinct replacement before inspecting again.`, 'receive_document'),
    document_applies: gate(!state.document || d.versionApplies, 'Version named by the agreement record', `${renewalFixture.agreement.label} names version ${rider.version}. Version ${version} does not apply to it, so it cannot be accepted. Reject it with a reason, or attach version ${rider.version}.`, 'verify_document'),
    inspection_needed: gate(!state.document || state.document.status === 'received', 'Inspection not yet recorded', 'An inspection is already recorded for this version.', 'verify_document'),
    document_inspection: gate(d.isDocumentVerified, 'Document accepted at inspection', 'The received version needs an accepted walkthrough inspection.', 'verify_document'),
    customer_request: gate(d.customerReviewIsCurrent, 'Current customer review request', 'A review request must match the version accepted at inspection.', 'request_customer_review'),
    new_customer_request: gate(!d.customerReviewIsCurrent, 'New customer review needed', 'A current customer review request is already recorded.', 'request_customer_review'),
    pending_customer_decision: gate(!d.hasCustomerApproval, 'No current customer decision', 'A current customer decision is already recorded for this version.', 'record_customer_decision'),
    letter_needed: gate(!letter, 'Approval letter not yet received', `A signed approval letter for version ${version} is already received.`, 'receive_approval_letter'),
    letter_available: gate(Boolean(approvalLetterFor(state)), 'Signed letter supplied by the customer', `The customer has not supplied ${d.letterReceiptCount ? 'another' : 'a'} signed approval letter for version ${version} in this walkthrough.`, 'receive_approval_letter'),
    letter_receipt: gate(Boolean(letter), 'Approval letter receipt', `No usable signed approval letter for version ${version} has been received.`, 'receive_approval_letter'),
    letter_receipt_valid: gate(!letter || !letterIssue, 'Letter receipt matches its file and scope', letterIssue || 'No letter receipt is recorded.', 'inspect_approval_letter'),
    letter_inspection_needed: gate(Boolean(letter) && !latestInspection(letter), 'Letter inspection not yet recorded', 'An inspection is already recorded for this letter.', 'inspect_approval_letter'),
    letter_accepted: gate(latestInspection(letter)?.status === 'accepted', 'Inspected approval letter', 'The signed approval letter needs an accepted inspection.', 'inspect_approval_letter'),
    authority_needed: gate(d.evidenceChecks.customer_authority.result !== 'passed', 'Authority not yet recorded', 'An authority record already supports a contact for this version.', 'record_customer_authority'),
    authority_recorded: gate(evidenceOf(state).customerAuthority?.status === 'accepted', 'Accepted authority record', 'No accepted authority record is recorded to withdraw.', 'record_customer_authority'),
    customer_authority: evidence('customer_authority'),
    current_version_basis: evidence('current_version_basis'),
    decision_citation: evidence('decision_citation'),
    customer_approval: gate(d.hasCustomerApproval, 'Recorded customer approval', 'A current customer approval record is needed for this document.', 'record_customer_decision'),
    open_requirement: gate(state.requirement.status === 'open', 'Open requirement', 'The recorded requirement must be open for a resolution transition.', 'resolve_requirement'),
    resolved_requirement: gate(state.requirement.status === 'resolved', 'Recorded requirement resolution', 'This requirement is already open.', 'resolve_requirement'),
  };
  const customerReview = ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request'];
  const letterReceipt = ['letter_receipt', 'letter_receipt_valid'];
  const inputs = {
    request_review: ['independent_reviewer', 'reviewable_plan'],
    approve_plan: ['independent_reviewer', 'plan_submission'],
    request_changes: ['independent_reviewer', 'plan_submission'],
    obtain_document: ['plan_approval'],
    verify_document: ['plan_approval', 'document_receipt', 'document_not_rejected', 'inspection_needed', 'document_applies'],
    reject_document: ['plan_approval', 'document_receipt', 'document_not_rejected', 'inspection_needed'],
    request_customer_review: ['plan_approval', 'document_receipt', 'document_inspection', 'new_customer_request'],
    upload_approval_letter: [...customerReview, 'pending_customer_decision', 'letter_needed', 'letter_available'],
    inspect_approval_letter: [...customerReview, 'pending_customer_decision', ...letterReceipt, 'letter_inspection_needed', 'plan_owner_principal'],
    record_customer_authority: [...customerReview, 'pending_customer_decision', ...letterReceipt, 'letter_accepted', 'authority_needed', 'plan_owner_principal'],
    record_customer_approval: [...customerReview, 'pending_customer_decision', ...letterReceipt, 'letter_accepted', 'plan_owner_principal', 'customer_authority', 'current_version_basis'],
    resolve_requirement: [...customerReview, 'customer_approval', 'customer_authority', 'decision_citation', 'current_version_basis', 'open_requirement'],
    reopen_requirement: ['resolved_requirement'],
    withdraw_customer_authority: ['authority_recorded'],
  };
  const recorded = {
    request_review: d.isPlanApproved || state.plan.status === 'in_review' ? 'The current plan revision has already been submitted.' : null,
    approve_plan: d.isPlanApproved ? 'The current plan revision is already approved.' : null,
    verify_document: d.isDocumentVerified ? 'This version is accepted at inspection. Which version applies is checked separately, against the agreement record.' : null,
    reject_document: d.isDocumentVerified ? 'This version was accepted at inspection.' : null,
    request_customer_review: d.customerReviewIsCurrent ? 'A current customer review request is already recorded.' : null,
    upload_approval_letter: letter ? `A signed approval letter for version ${version} is already received.` : null,
    inspect_approval_letter: latestInspection(letter) ? 'An inspection is already recorded for this letter.' : null,
    record_customer_authority: d.evidenceChecks.customer_authority.result === 'passed' ? 'An authority record already supports a contact for this version.' : null,
    record_customer_approval: d.hasCustomerApproval ? 'A current customer decision is recorded with its citation, approving contact and authority record.' : null,
    resolve_requirement: state.requirement.status === 'resolved' ? 'A walkthrough resolution is already recorded. Reopening needs a reason.' : null,
  };
  // The refusal names the first gate that actually failed. Resolution keeps one lead so its reason reads as a whole.
  const leads = {resolve_requirement: 'The requirement needs approval evidence before it can be resolved.'};
  return Object.fromEntries(Object.entries(inputs).map(([command, codes]) => {
    const unmet = codes.filter(code => !gates[code].met);
    const evidenceOnly = unmet.length > 0 && unmet.every(code => gates[code].evidenceGap);
    const refusal = !unmet.length ? null : evidenceOnly
      ? `${command === 'resolve_requirement' ? 'The requirement needs approval evidence' : 'Recording the decision needs approval evidence'}: ${unmet.map(code => gates[code].reason).join(' ')}`
      : [leads[command], gates[unmet[0]].reason].filter(Boolean).join(' ');
    return [command, {
      command, checkId: workflowTransitionChecks[command] || null,
      allowed: !unmet.length, recorded: Boolean(recorded[command]),
      recordedReason: recorded[command] || null,
      refusal,
      replacement: command === 'obtain_document' && Boolean(state.document),
      blockedReasons: recorded[command] ? [] : unmet.map(code => {
        const {met, ...reason} = gates[code];
        return {code, action: command, ...reason};
      }),
    }];
  }));
}

export function derive(state, reviewer = fixturePeople.internalReviewer, fixture = readinessFixture) {
  // A supplied reviewer cannot relax the walkthrough's independent-review rule.
  const isReviewerEligible = Boolean(reviewer && state.plan.owner?.trim() && state.plan.owner.trim() !== reviewer
    && state.plan.owner.trim() !== fixturePeople.internalReviewer);
  const planReviewBlockedReason = isReviewerEligible ? null
    : 'An independent reviewer is needed. The plan owner cannot review their own work.';
  const isPlanApproved = state.plan.status === 'approved'
    && state.plan.approvedRevision === state.plan.revision;
  const doc = state.document, review = state.customerReview;
  const rider = (fixture.agreement || renewalFixture.agreement).applicableRider || renewalFixture.agreement.applicableRider;
  const documentRejected = doc?.status === 'rejected';
  // The agreement record, never the file, says which version applies.
  const versionApplies = Boolean(doc && doc.id === rider.documentId && doc.version === rider.version);
  const isDocumentVerified = Boolean(doc && doc.status === 'verified' && versionApplies);
  // The customer decision is bound to the document version, not to our plan's revision.
  const customerReviewIsCurrent = Boolean(isDocumentVerified && review
    && review.documentId === doc.id && review.documentVersion === doc.version);
  const context = workflowEvidence(state, fixture);
  const evidenceChecks = Object.fromEntries(['customer_authority', 'current_version_basis', 'decision_citation'].map(id => [id, evaluateEvidenceCheck(id, state, context)]));
  const approvalLetter = coveringLetter(state);
  const authorityContact = evidenceChecks.customer_authority.supportedContact || null;
  const authority = context.customerAuthority;
  const hasCustomerApproval = Boolean(customerReviewIsCurrent && review.status === 'approved'
    && review.evidenceRef && review.contactId && authority?.status === 'accepted'
    && review.authorityRef?.id === authority.id && review.authorityRef?.revision === authority.revision);
  const decisionNeedsReview = Boolean(review && (review.status === 'needs_re_review' || review.status === 'approved' && !hasCustomerApproval));
  const evidenceEstablished = Object.values(evidenceChecks).every(check => check.result === 'passed');
  const recordedResolved = state.requirement.status === 'resolved';
  // One effective status for every view: a recorded resolution counts only while its evidence still supports it.
  const resolutionSupported = Boolean(recordedResolved && hasCustomerApproval && evidenceEstablished && state.requirement.resolvedWith?.decisionId === review?.id);
  const resolutionNeedsReview = recordedResolved && !resolutionSupported;
  const requirementStatus = resolutionSupported ? 'resolved' : 'open';
  const transitions = localTransitions(state, {isReviewerEligible, planReviewBlockedReason, isPlanApproved, isDocumentVerified, versionApplies, documentRejected, hasCustomerApproval, customerReviewIsCurrent, evidenceChecks, approvalLetter,
    letterReceiptCount: evidenceOf(state).artifactReceipts.filter(receipt => receipt.kind === 'approval_letter').length});
  const needsReplacement = Boolean(doc && (!versionApplies || documentRejected));
  const decisionCommand = !approvalLetter ? 'upload_approval_letter' : !latestInspection(approvalLetter) ? 'inspect_approval_letter'
    : !authorityContact ? 'record_customer_authority' : 'record_customer_approval';
  const nextCommand = !isPlanApproved ? state.plan.status === 'in_review' ? 'approve_plan' : 'request_review'
    : resolutionNeedsReview ? 'reopen_requirement'
    : !doc || needsReplacement ? 'obtain_document'
    : !isDocumentVerified ? 'verify_document'
    : !customerReviewIsCurrent ? 'request_customer_review'
    : !hasCustomerApproval ? decisionCommand
    : state.requirement.status === 'open' ? 'resolve_requirement' : null;
  const acquisitionStatus = ({
    draft: 'Draft',
    in_review: 'Awaiting reviewer',
    changes_requested: 'Changes requested',
    approved: isPlanApproved ? 'Acquisition authorized' : 'Reapproval required',
  })[state.plan.status] || 'Draft';
  let taskStage = 'not_created';
  if (state.task) {
    if (!isPlanApproved) taskStage = 'awaiting_plan_approval';
    else if (resolutionNeedsReview) taskStage = 'resolution_needs_review';
    else if (!doc || needsReplacement && documentRejected) taskStage = 'obtain_document';
    else if (!isDocumentVerified) taskStage = 'verify_document';
    else if (!customerReviewIsCurrent) taskStage = 'request_customer_review';
    else if (!hasCustomerApproval) taskStage = {upload_approval_letter: 'awaiting_customer_letter', inspect_approval_letter: 'inspect_customer_letter', record_customer_authority: 'record_customer_authority'}[decisionCommand] || 'awaiting_customer_approval';
    else if (!recordedResolved) taskStage = 'ready_to_resolve';
    else taskStage = 'complete';
  }
  return {
    isPlanApproved,
    isDocumentVerified,
    versionApplies,
    documentRejected,
    hasCustomerApproval,
    decisionNeedsReview,
    customerReviewIsCurrent,
    isReviewerEligible,
    planReviewBlockedReason,
    evidenceChecks, evidenceEstablished, approvalLetter, authorityContact,
    requirementStatus, resolutionNeedsReview,
    requirementStatusLabel: resolutionNeedsReview ? 'Resolution needs review' : requirementStatus === 'resolved' ? 'Resolved' : 'Open',
    transitions, nextCommand, needsReplacement,
    canRequestReview: transitions.request_review.allowed,
    canApprovePlan: transitions.approve_plan.allowed,
    canRequestChanges: transitions.request_changes.allowed,
    canObtainDocument: transitions.obtain_document.allowed && !doc,
    canReplaceDocument: transitions.obtain_document.allowed && Boolean(doc),
    canVerifyDocument: transitions.verify_document.allowed,
    canRejectDocument: transitions.reject_document.allowed,
    canRequestCustomerReview: transitions.request_customer_review.allowed,
    canUploadApprovalLetter: transitions.upload_approval_letter.allowed,
    canInspectApprovalLetter: transitions.inspect_approval_letter.allowed,
    canRecordCustomerAuthority: transitions.record_customer_authority.allowed,
    canRecordCustomerApproval: transitions.record_customer_approval.allowed,
    canResolveRequirement: transitions.resolve_requirement.allowed,
    canReopenRequirement: transitions.reopen_requirement.allowed,
    canWithdrawAuthority: transitions.withdraw_customer_authority.allowed,
    acquisitionStatus,
    documentStatus: isDocumentVerified ? 'Accepted at inspection' : documentRejected ? 'Rejected at inspection' : doc && !versionApplies ? `Received · ${renewalFixture.agreement.label} names ${rider.version}` : doc ? 'Received · not yet inspected' : 'Not yet received',
    customerApprovalStatus: hasCustomerApproval ? 'Approval recorded' : !customerReviewIsCurrent ? decisionNeedsReview ? 'Decision needs re-review' : 'Not requested'
      : ({upload_approval_letter: 'Signed letter not yet received', inspect_approval_letter: 'Letter received · inspection needed', record_customer_authority: 'Letter accepted · authority not recorded'})[decisionCommand]
        || (decisionNeedsReview ? 'Decision needs re-review' : 'Ready to record decision'),
    commercialStatus: state.commercial.label,
    taskStage,
    taskStageLabel: ({
      not_created: 'Task not created',
      awaiting_plan_approval: 'Awaiting plan reapproval',
      resolution_needs_review: 'Resolution needs review',
      obtain_document: 'Obtain current terms',
      verify_document: 'Inspect the document version',
      request_customer_review: 'Request customer counsel review',
      awaiting_customer_letter: 'Add the customer’s signed letter',
      inspect_customer_letter: 'Inspect the signed letter',
      record_customer_authority: 'Record who decides',
      awaiting_customer_approval: 'Record the cited customer decision',
      ready_to_resolve: 'Ready to resolve requirement',
      complete: 'Requirement resolved',
    })[taskStage],
  };
}

// Walkthrough commands never use the device clock.
export function transitionWalkthrough(state, action) {
  return transition(state, {...action, at: clock(state)});
}

function rejected(state, message) {
  return { ...state, error: message };
}

function planSnapshot(plan) {
  return {
    id: plan.id,
    revision: plan.revision,
    title: plan.title,
    owner: plan.owner,
    specifications: plan.specifications ?? initialState.plan.specifications,
    instructions: plan.instructions ?? initialState.plan.instructions,
  };
}

function changed(state, action, patch, title, detail = '', evidenceSnapshot = null, eventDetails = {}) {
  const at = action.at || clock(state);
  const id = createHistoryEntryId();
  const {historyRecord, historyContexts} = recordHistoryInputs(state, patch, action, id);
  return {
    ...state,
    ...patch,
    error: null,
    historyContexts,
    history: [...state.history, { id, type: action.type, at, recordedAt: at, eventAt: historyRecord.eventAt, actor: historyRecord.actor, reason: historyRecord.reason, title, detail, historyRecord, ...(evidenceSnapshot ? { evidenceSnapshot } : {}), ...eventDetails }],
  };
}

// Receipts are appended and never replaced; each inspection is appended to its own receipt.
function appendInspection(state, id, inspection) {
  const evidence = evidenceOf(state);
  return {...evidence, artifactReceipts: evidence.artifactReceipts.map(receipt => {
    if (receipt.id !== id) return receipt;
    const {inspection: legacy, ...rest} = receipt;
    return {...rest, inspections: [...receiptInspections(receipt), inspection]};
  })};
}

// A decision that is no longer current keeps its record, marked for re-review with the reason and actor.
function markReReview(review, at, actor, reason, cause) {
  if (review?.status !== 'approved') return review;
  return {...review, status: 'needs_re_review', reReview: {at, reason, cause, actor: actor?.displayName || null, actorId: actor?.id || null}};
}

// Every reopening records its reason and actor. The earlier resolution is kept.
function reopened(requirement, at, actor, reason, cause) {
  if (requirement.status !== 'resolved') return requirement;
  const {resolvedAt, resolvedWith, resolvedBy, resolvedById} = requirement;
  return {...requirement, status: 'open', resolvedAt: null, resolvedWith: null, resolvedBy: null, resolvedById: null,
    priorResolutions: [...(requirement.priorResolutions || []), {resolvedAt, resolvedWith: resolvedWith || null, resolvedBy: resolvedBy || null, resolvedById: resolvedById || null}],
    reopenedAt: at, reopenedBy: actor?.displayName || null, reopenedById: actor?.id || null, reopenReason: reason, reopenCause: cause};
}

// The request fields of a customer review, without any decision recorded against it.
const requestFields = review => ({requestedAt: review.requestedAt, requestedBy: review.requestedBy, documentId: review.documentId,
  documentVersion: review.documentVersion, planRevision: review.planRevision});
const priorDecisionsOf = review => {
  const {priorDecisions = [], ...decision} = review || {};
  return [...priorDecisions, ...(decision.evidenceRef || decision.contactId ? [decision] : [])];
};

// Separation of duties, by principal ID: neither the plan owner nor the uploader of the receipt acted on.
function dutyConflict(state, actor, receipt, verb) {
  if (!actor) return `Name the principal who ${verb}. A display name alone is not accepted.`;
  if (!principalById(state.plan.ownerId)) return 'The plan owner is not a recorded principal, so separation of duties cannot be checked. Save the plan with a task owner first.';
  if (actor.id === state.plan.ownerId) return `The plan owner cannot be the principal who ${verb}.`;
  if (receipt && !receipt.uploadedById) return `The uploader of receipt ${receipt.id} is not recorded as a principal, so separation of duties cannot be checked. Upload a distinct copy.`;
  if (receipt && actor.id === receipt.uploadedById) return `The principal who uploaded receipt ${receipt.id} cannot be the principal who ${verb}.`;
  return null;
}

export function transition(state, action) {
  const ready = derive(state);
  const at = action.at || clock(state);
  const eligibility = ready.transitions[action.type];
  if (eligibility && !eligibility.allowed) return rejected(state, eligibility.refusal);
  const actor = principalFor(action);
  const acting = actor ? {...action, actor: actor.displayName} : action;
  switch (action.type) {
    case 'reset':
      return initialState;
    case 'save_plan': {
      const fields = ['title', 'owner', 'specifications', 'instructions'];
      const previous = planSnapshot(state.plan);
      const ownerChoice = action.ownerId != null ? principalById(action.ownerId) : action.owner != null ? principalNamed(action.owner) : null;
      if ((action.ownerId != null || action.owner != null && text(action.owner)) && !ownerChoice) return rejected(state, 'Choose the task owner from the workspace’s principals.');
      const values = Object.fromEntries(fields.map(field => [field, (field === 'owner' && ownerChoice ? ownerChoice.displayName : action[field] ?? previous[field]).trim()]));
      if (fields.some(field => !values[field])) return rejected(state, 'Give the work plan a title, owner, specifications, and instructions.');
      const changedFields = fields.filter(field => values[field] !== previous[field]);
      const ownerId = ownerChoice ? ownerChoice.id : state.plan.ownerId ?? null;
      if (!changedFields.length) return { ...state, plan: { ...state.plan, ...values, ownerId }, error: null };
      const nextRevision = state.plan.revision + 1;
      const plan = { ...state.plan, ...values, ownerId, revision: nextRevision, status: 'draft', approvedRevision: null, requestedAt: null, approvedAt: null, approver: null, reviewNote: '' };
      // Title and owner edits keep the customer decision and the requirement. A specifications edit needs
      // the document inspected again, so a decision resting on the old inspection needs re-review.
      const specifications = changedFields.includes('specifications');
      const marksDecision = specifications && state.customerReview?.status === 'approved';
      const reopens = specifications && state.requirement.status === 'resolved';
      const reason = `Plan specifications changed in revision ${nextRevision}. The document needs inspection again, so the customer decision rests on an earlier inspection.`;
      if ((marksDecision || reopens) && !actor) return rejected(state, 'Name the principal editing the plan. This specifications edit marks the customer decision for re-review and reopens a resolved requirement, and each needs an actor.');
      return changed(state, marksDecision || reopens ? {...acting, reason} : acting, {
        plan,
        task: state.task ? { ...state.task, title: values.title, owner: values.owner, status: 'blocked' } : null,
        document: state.document && specifications && state.document.status === 'verified'
          ? { ...state.document, status: 'received', verifiedAt: null, verifiedBy: null }
          : state.document,
        customerReview: marksDecision ? markReReview(state.customerReview, at, actor, reason, 'plan_specifications') : state.customerReview,
        requirement: reopens ? reopened(state.requirement, at, actor, reason, 'plan_specifications') : state.requirement,
      }, `Plan saved as revision ${nextRevision}`, specifications
        ? `The plan needs reapproval and the document needs inspection again.${marksDecision ? ' The customer decision is kept and marked for re-review.' : ''}${reopens ? ' The requirement is reopened with this reason.' : ''}`
        : 'The plan needs reapproval. The customer decision and the requirement are unchanged.', null, { changedFields, planSnapshot: planSnapshot(plan) });
    }
    case 'request_review':
      return changed(state, action, {
        plan: { ...state.plan, status: 'in_review', requestedAt: at, reviewNote: '' },
      }, `Revision ${state.plan.revision} submitted for review`, 'Review authorizes obtaining the document. The document may still be missing.');
    case 'approve_plan':
      return changed(state, action, {
        plan: { ...state.plan, status: 'approved', approvedRevision: state.plan.revision, approvedAt: at, approver: action.approver?.trim() || fixturePeople.internalReviewer },
        task: state.task
          ? { ...state.task, title: state.plan.title, owner: state.plan.owner, status: 'open', planRevision: state.plan.revision }
          : { id: renewalFixture.taskId, title: state.plan.title, owner: state.plan.owner, status: 'open', createdAt: at, planRevision: state.plan.revision },
      }, `Acquisition authorized for revision ${state.plan.revision}`, state.task ? 'The existing acquisition task is ready to continue.' : 'One acquisition task was created. Customer approval remains outstanding.', null, { planSnapshot: planSnapshot(state.plan) });
    case 'request_changes':
      return changed(state, action, {
        plan: { ...state.plan, status: 'changes_requested', approvedRevision: null, reviewNote: action.note?.trim() || 'Clarify the acquisition owner and the document source.' },
      }, 'Reviewer requested changes', action.note?.trim() || 'Clarify the acquisition owner and the document source.');
    case 'obtain_document': {
      // Without a named file, the walkthrough's customer supplies its file, or the distinct copy once that one was rejected.
      const rejectedFiles = new Set(evidenceOf(state).artifactReceipts.filter(receipt => latestInspection(receipt)?.status === 'rejected').map(receipt => receipt.checksum));
      const fixture = fixtureEvidence[2], supplied = action.document || [fixture, walkthroughDocumentExamples.rescan].find(file => !rejectedFiles.has(file.checksum)) || fixture;
      const id = supplied.id || fixture.id, version = supplied.version || fixture.version;
      const checksum = supplied.checksum || syntheticChecksum(`${id}:${version}`);
      const duplicate = duplicateFile(state, checksum);
      if (duplicate) return rejected(state, duplicate);
      const marksDecision = state.customerReview?.status === 'approved', reopens = state.requirement.status === 'resolved';
      if ((marksDecision || reopens) && !actor) return rejected(state, 'Name the principal attaching the replacement. It marks the customer decision for re-review and reopens a resolved requirement, and each needs an actor.');
      const document = {
        id,
        title: supplied.title || supplied.name || fixture.name,
        name: supplied.name || supplied.title || fixture.name,
        version,
        source: supplied.source || fixture.source,
        status: 'received',
        receivedAt: at,
        verifiedAt: null,
        verifiedBy: null,
        receiptId: receiptId(state, id),
        checksum,
        uploadedBy: actor?.displayName || text(action.actor),
        uploadedById: actor?.id || null,
      };
      const receipt = {id: document.receiptId, artifactId: id, kind: 'document', title: document.name, documentId: id, documentVersion: version,
        status: 'received', receivedAt: at, uploadedBy: document.uploadedBy, uploadedById: document.uploadedById, checksum, inspections: []};
      const evidence = evidenceOf(state);
      const reason = `Receipt ${receipt.id} (version ${version}) replaced the inspected document. The customer decision on the earlier receipt needs re-review.`;
      return changed(state, marksDecision || reopens ? {...acting, reason} : acting, {
        document,
        evidence: {...evidence, artifactReceipts: [...evidence.artifactReceipts, receipt]},
        customerReview: marksDecision ? markReReview(state.customerReview, at, actor, reason, 'document_replaced') : state.customerReview,
        task: { ...state.task, status: 'open' },
        requirement: reopens ? reopened(state.requirement, at, actor, reason, 'document_replaced') : state.requirement,
      }, state.document ? 'Replacement terms attached' : 'AI terms received', `Version ${document.version} is received as receipt ${receipt.id}. Earlier receipts are kept. Inspection is next.${marksDecision ? ' The earlier customer decision is kept and marked for re-review.' : ''}`);
    }
    case 'verify_document': {
      const reason = text(action.reason);
      if (!reason) return rejected(state, 'Give a typed reason for accepting this version at inspection. The reason is kept with the receipt.');
      const verifiedBy = text(action.verifiedBy) || actor?.displayName || state.plan.owner;
      const inspection = {status: 'accepted', at, recordedBy: verifiedBy, recordedById: actor?.id || null, reason,
        applies: {agreementId: renewalFixture.agreement.id, documentId: state.document.id, version: state.document.version}};
      return changed(state, {...acting, reason}, {
        document: { ...state.document, status: 'verified', verifiedAt: at, verifiedBy },
        ...(state.document.receiptId ? {evidence: appendInspection(state, state.document.receiptId, inspection)} : {}),
      }, 'Document version accepted at inspection', `Version ${state.document.version} is accepted at inspection as applicable to ${renewalFixture.agreement.label}. ${reason} This is not customer approval.`);
    }
    case 'reject_document': {
      const reason = text(action.reason);
      if (!reason) return rejected(state, 'Give a reason for rejecting this version. The rejected version is kept.');
      const rejectedBy = actor?.displayName || text(action.actor) || state.plan.owner;
      const inspection = {status: 'rejected', at, recordedBy: rejectedBy, recordedById: actor?.id || null, reason};
      return changed(state, acting, {
        document: { ...state.document, status: 'rejected', rejectedAt: at, rejectedBy, rejectionReason: reason },
        ...(state.document.receiptId ? {evidence: appendInspection(state, state.document.receiptId, inspection)} : {}),
        task: state.task ? { ...state.task, status: 'open' } : null,
      }, 'Document version rejected at inspection', `Version ${state.document.version} was rejected. ${reason} The receipt is kept; acquisition needs a distinct replacement.`);
    }
    case 'request_customer_review':
      return changed(state, action, {
        customerReview: { status: 'pending', requestedAt: at, requestedBy: state.plan.owner, documentId: state.document.id, documentVersion: state.document.version, planRevision: state.plan.revision, reviewedBy: null, recordedBy: null, evidenceText: null, approvedAt: null,
          priorDecisions: priorDecisionsOf(state.customerReview) },
      }, 'Customer counsel review requested', `Awaiting the customer’s decision on AI rider version ${state.document.version}.`);
    case 'upload_approval_letter': {
      if (!actor) return rejected(state, 'Name the principal who uploads the letter. A display name alone is not accepted.');
      const letter = action.artifactId ? approvalLetters.find(item => item.artifactId === action.artifactId) : approvalLetterFor(state);
      if (!letter) return rejected(state, `The customer has not supplied that signed letter for version ${state.document.version}.`);
      if (letter.approves.documentId !== state.document.id || letter.approves.version !== state.document.version) return rejected(state, `That letter does not approve version ${state.document.version}, the version under review.`);
      const checksum = action.checksum || letter.checksum;
      const duplicate = duplicateFile(state, checksum);
      if (duplicate) return rejected(state, duplicate);
      const evidence = evidenceOf(state);
      const receipt = {id: receiptId(state, letter.artifactId), artifactId: letter.artifactId, kind: 'approval_letter', title: letter.name,
        approves: {...letter.approves}, contactId: letter.contactId, source: letter.source, status: 'received', receivedAt: at,
        uploadedBy: actor.displayName, uploadedById: actor.id, checksum, inspections: []};
      return changed(state, acting, {
        evidence: {...evidence, artifactReceipts: [...evidence.artifactReceipts, receipt]},
      }, 'Signed approval letter received', `Receipt ${receipt.id} records the customer’s letter on version ${state.document.version}. Inspection is next. Nobody is named as a contact until a reviewer records who decides.`);
    }
    case 'inspect_approval_letter': {
      const outcome = ['accepted', 'rejected'].includes(action.outcome) ? action.outcome : null;
      const reason = text(action.reason);
      if (!outcome) return rejected(state, 'Accept or reject the signed letter at inspection.');
      if (!reason) return rejected(state, `Give a typed reason for ${outcome === 'accepted' ? 'accepting' : 'rejecting'} the letter. It is kept with the receipt.`);
      const receipt = ready.approvalLetter;
      const conflict = dutyConflict(state, actor, receipt, 'inspects the letter');
      if (conflict) return rejected(state, conflict);
      const evidence = appendInspection(state, receipt.id, {status: outcome, at, recordedBy: actor.displayName, recordedById: actor.id, reason});
      return changed(state, acting, {evidence}, outcome === 'accepted' ? 'Approval letter accepted at inspection' : 'Approval letter rejected at inspection',
        outcome === 'accepted' ? `The letter approves version ${receipt.approves.version} for ${renewalFixture.agreement.label}. ${reason} Which version applies comes from the agreement record, not from this letter.`
          : `${reason} The receipt is kept. A distinct letter is needed.`);
    }
    case 'record_customer_authority': {
      const receipt = ready.approvalLetter;
      const conflict = dutyConflict(state, actor, receipt, 'records who decides');
      if (conflict) return rejected(state, conflict);
      const letter = letterArtifact(receipt);
      const evidence = evidenceOf(state);
      if (actor.id === letter.contactId) return rejected(state, 'The signatory cannot record their own authority.');
      const contact = {id: letter.contactId, displayName: letter.signatory.name, role: letter.signatory.role, status: 'active',
        recordedAt: at, recordedBy: actor.displayName, recordedById: actor.id, sourceRef: {kind: 'receipt', id: receipt.id}};
      const previous = evidence.customerAuthority;
      const customerAuthority = {id: `authority-${letter.contactId.replace(/^contact-/, '')}`, revision: String(Number(previous?.revision || 0) + 1), status: 'accepted',
        recordedAt: at, recordedBy: actor.displayName, recordedById: actor.id, agreementId: receipt.approves.agreementId, documentId: receipt.approves.documentId,
        version: receipt.approves.version, signatory: {...letter.signatory}, contactId: contact.id, evidenceRef: {kind: 'receipt', id: receipt.id}};
      return changed(state, acting, {
        evidence: {...evidence, contacts: [...evidence.contacts.filter(item => item.id !== contact.id), contact], customerAuthority,
          authorityRecords: [...evidence.authorityRecords, customerAuthority]},
      }, 'Customer authority recorded', `${contact.displayName}, ${contact.role.toLowerCase()}, is recorded as the contact who decides for ${renewalFixture.agreement.label} on ${receipt.approves.documentId} ${receipt.approves.version}, from the signed approval letter (receipt ${receipt.id}).`);
    }
    case 'record_customer_approval': {
      if (!action.evidenceRef) return rejected(state, 'Typed text is not a customer decision. Cite the accepted signed letter, and name the approving contact from the authority record.');
      const context = workflowEvidence(state);
      const issues = citationIssues(action.evidenceRef, context, state.document, action.contactId);
      if (issues.length) return rejected(state, `The decision cannot cite that evidence. ${issues.join(' ')}`);
      if (action.documentVersion && action.documentVersion !== state.document.version) return rejected(state, `The decision must be bound to version ${state.document.version}, the version under review.`);
      const contact = supportedAuthority(context, state.document);
      if (!contact || action.contactId !== contact.id) return rejected(state, 'Name the approving contact from the authority record for this version.');
      const receipt = context.artifactReceipts.find(item => item.id === action.evidenceRef.id);
      const conflict = dutyConflict(state, actor, receipt, 'records the decision');
      if (conflict) return rejected(state, conflict);
      if (actor.id === contact.id) return rejected(state, 'The person recording a decision cannot be its approver.');
      const authority = context.customerAuthority;
      const evidenceRef = {kind: 'receipt', id: receipt.id};
      const decision = {id: `decision-${state.history.length + 1}`, agreementId: renewalFixture.agreement.id, contactId: contact.id, reviewedBy: contact.displayName,
        recordedBy: actor.displayName, recordedById: actor.id, evidenceRef, authorityRef: {id: authority.id, revision: authority.revision}, evidenceText: null, approvedAt: at};
      return changed(state, acting, {
        customerReview: {...requestFields(state.customerReview), status: 'approved', ...decision, priorDecisions: priorDecisionsOf(state.customerReview)},
      }, 'Customer decision recorded', `${actor.displayName} recorded ${contact.displayName}’s approval of version ${state.document.version}, citing receipt ${evidenceRef.id} and authority record ${authority.id} revision ${authority.revision}.`, {
        reviewedBy: contact.displayName,
        contactId: contact.id,
        recordedBy: actor.displayName,
        evidenceRef,
        documentId: state.document.id,
        documentVersion: state.document.version,
        planRevision: state.plan.revision,
        approvedAt: at,
      });
    }
    case 'resolve_requirement': {
      const context = workflowEvidence(state);
      return changed(state, acting, {
        requirement: { ...state.requirement, status: 'resolved', resolvedAt: at, resolvedBy: actor?.displayName || null, resolvedById: actor?.id || null, resolvedWith: {
          decisionId: state.customerReview.id || null, evidenceRef: state.customerReview.evidenceRef,
          authorityId: context.customerAuthority?.id || null, authorityRevision: context.customerAuthority?.revision || null, versionBasisId: context.documentVersionBasis?.id || null,
        } },
      }, 'Legal requirement resolved', 'Resolved from a decision citing the accepted signed letter, an authority record for that version and the agreement record’s version basis. Renewal remains unconfirmed.');
    }
    case 'reopen_requirement': {
      const reason = text(action.reason);
      if (!reason) return rejected(state, 'Give a reason for reopening the requirement.');
      if (!actor) return rejected(state, 'Name the principal reopening the requirement. Every reopening records its actor.');
      return changed(state, {...acting, reason}, {
        requirement: reopened(state.requirement, at, actor, reason, 'explicit'),
        task: state.task ? { ...state.task, status: 'open' } : null,
        customerReview: markReReview(state.customerReview, at, actor, reason, 'requirement_reopened'),
      }, 'Legal requirement reopened', `${reason} The customer decision is kept and marked for re-review before the requirement can be resolved again.`);
    }
    case 'withdraw_customer_authority': {
      const reason = text(action.reason);
      if (!reason) return rejected(state, 'Give a reason for withdrawing the authority record.');
      if (!actor) return rejected(state, 'Name the principal withdrawing the authority record.');
      const evidence = evidenceOf(state), authority = evidence.customerAuthority;
      const withdrawn = {...authority, revision: String(Number(authority.revision) + 1), status: 'withdrawn', withdrawnAt: at, withdrawnBy: actor.displayName, withdrawnById: actor.id, withdrawReason: reason};
      const why = `Authority record ${authority.id} was withdrawn: ${reason}`;
      return changed(state, {...acting, reason}, {
        evidence: {...evidence, customerAuthority: withdrawn, authorityRecords: [...evidence.authorityRecords, withdrawn]},
        customerReview: markReReview(state.customerReview, at, actor, why, 'authority_withdrawn'),
        requirement: reopened(state.requirement, at, actor, why, 'authority_withdrawn'),
      }, 'Customer authority withdrawn', `${why} A decision that relied on it needs re-review, and a new signed letter is needed before anyone is recorded as deciding again.`);
    }
    default:
      return rejected(state, 'That action is not available in this demo.');
  }
}

// A walkthrough saved under earlier rules loads with what it had. A decision those rules accepted
// but these do not becomes a retained record that is not current, and a resolution resting on it
// reopens with a system reason. One entry in History records the change of rules.
export function restoreWorkflowState(saved) {
  if (!saved || typeof saved !== 'object' || !saved.plan || !Array.isArray(saved.history)) return initialState;
  const compact = compactHistoryState(saved);
  const plan = {...initialState.plan, ...saved.plan, ownerId: saved.plan.ownerId ?? principalNamed(saved.plan.owner)?.id ?? null};
  let state = {...initialState, ...compact, plan, error: null};
  if (saved.policyVersion === workflowPolicyVersion) return state;
  const previousPolicy = text(saved.policyVersion) || 'an earlier policy without a recorded version';
  const {documentVersionBasis, ...ownEvidence} = saved.evidence || {};
  const evidence = {...emptyEvidence, authorityRecords: [], ...ownEvidence};
  evidence.artifactReceipts = (evidence.artifactReceipts || []).map(receipt => {
    const {inspection, ...rest} = receipt;
    return {...rest, uploadedById: receipt.uploadedById ?? principalNamed(receipt.uploadedBy)?.id ?? null,
      inspections: receiptInspections(receipt).map(item => ({...item, recordedById: item.recordedById ?? principalNamed(item.recordedBy)?.id ?? null}))};
  });
  if (evidence.customerAuthority && !evidence.authorityRecords.length) evidence.authorityRecords = [evidence.customerAuthority];
  state = {...state, evidence, policyVersion: workflowPolicyVersion};
  if (!state.history.length && !state.customerReview && state.requirement.status === 'open') return state;
  const at = clock(state), system = {displayName: 'Walkthrough rules (on restore)', id: null};
  const review = state.customerReview, d = derive(state);
  const typed = review?.status === 'approved' && !review.evidenceRef;
  const reason = `This walkthrough was saved under ${previousPolicy}. Under ${workflowPolicyVersion}, a decision cites the accepted signed letter for the version, names the contact from an authority record for that version, and records which authority record it relied on${typed ? '. The saved decision was typed text, so it is kept as a retained record that is not a decision' : ''}.`;
  const patch = {};
  if (review?.status === 'approved' && !d.hasCustomerApproval) {
    patch.customerReview = typed
      ? {...requestFields(review), status: 'needs_re_review', reReview: {at, reason, cause: 'policy_restore', actor: null, actorId: null},
        priorDecisions: [...(review.priorDecisions || []), {...review, legacy: true, current: false, policyVersion: saved.policyVersion || null}]}
      : markReReview(review, at, system, reason, 'policy_restore');
  }
  if (state.requirement.status === 'resolved' && derive({...state, ...patch}).resolutionNeedsReview) {
    patch.requirement = reopened(state.requirement, at, null, reason, 'policy_restore');
  }
  const reopens = Boolean(patch.requirement), marks = Boolean(patch.customerReview);
  return changed(state, {type: 'restore_policy', at, actor: system.displayName, reason: reopens || marks ? reason : null}, patch,
    `Walkthrough restored under ${workflowPolicyVersion}`,
    `Saved under ${previousPolicy}.${marks ? ' The saved decision is kept as a record that is not current.' : ''}${reopens ? ' The resolution resting on it is reopened with this reason.' : ''} Earlier entries keep the rules they were recorded under.`);
}
