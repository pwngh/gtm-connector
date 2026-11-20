import {approvalLetters, fixtureEvidence, initialState, fixturePeople, readinessFixture, renewalFixture} from './renewalFixtures.js';
import {createHistoryEntryId, recordHistoryInputs} from './historyRecording.js';
import {citationIssues, emptyEvidence, evaluateEvidenceCheck, resolveEvidenceReference, supportedAuthority, workflowEvidence} from './evidenceChecks.js';
export {fixtureEvidence, initialState} from './renewalFixtures.js';

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
const evidenceOf = state => ({...emptyEvidence, ...(state.evidence || {})});

// The letter the walkthrough's customer has signed for this exact document version, if any.
export function approvalLetterFor(document) {
  return document ? approvalLetters.find(letter => letter.approves.documentId === document.id && letter.approves.version === document.version) || null : null;
}

// The latest letter receipt for the current version that inspection has not rejected.
function coveringLetter(state) {
  const doc = state.document;
  if (!doc) return null;
  return evidenceOf(state).artifactReceipts.filter(receipt => receipt.kind === 'approval_letter' && receipt.approves?.documentId === doc.id
    && receipt.approves?.version === doc.version && receipt.inspection?.status !== 'rejected').at(-1) || null;
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

function localTransitions(state, d) {
  const gate = (met, name, reason, checkId, evidenceGap = false) => ({met, name, reason, checkId, ...(evidenceGap ? {evidenceGap} : {})});
  const versionMismatch = state.document && state.document.version !== state.document.currentVersion;
  const version = state.document?.version || 'unknown';
  const letter = d.approvalLetter;
  const evidence = id => gate(d.evidenceChecks[id].result === 'passed', readinessFixture.checks.find(check => check.id === id)?.title || id, d.evidenceChecks[id].explanation, id, true);
  const gates = {
    independent_reviewer: gate(d.isReviewerEligible, 'Independent internal reviewer', d.planReviewBlockedReason, 'submit_plan'),
    reviewable_plan: gate(['draft', 'changes_requested'].includes(state.plan.status), 'Plan ready for submission', 'The plan must be a draft or have changes requested before submission.', 'submit_plan'),
    plan_submission: gate(state.plan.status === 'in_review', 'Current plan submitted for review', 'Submit the current plan revision before it can be approved.', 'submit_plan'),
    plan_approval: gate(d.isPlanApproved, 'Current plan approval', 'The current plan revision needs internal approval.', 'approve_plan'),
    document_receipt: gate(Boolean(state.document), 'Document receipt', 'The applicable document has not been received.', 'receive_document'),
    document_not_rejected: gate(!d.documentRejected, 'Received version not rejected', `Version ${version} was rejected at inspection. Its receipt is kept. Upload a replacement before inspecting again.`, 'receive_document'),
    document_current_version: gate(!versionMismatch, 'Walkthrough version match', `Version ${version} is not current. Attach version ${state.document?.currentVersion || 'unknown'} before verification.`, 'receive_document'),
    inspection_needed: gate(!d.isDocumentVerified, 'Inspection not yet recorded', 'An inspection is already recorded for this version.', 'verify_document'),
    document_inspection: gate(d.isDocumentVerified, 'Document inspection', 'The received version needs a current walkthrough inspection.', 'verify_document'),
    customer_request: gate(d.customerReviewIsCurrent, 'Current customer review request', 'A review request must match the current walkthrough inputs.', 'request_customer_review'),
    new_customer_request: gate(!d.customerReviewIsCurrent, 'New customer review needed', 'A current customer review request is already recorded.', 'request_customer_review'),
    // A typed decision saved by an earlier version of this walkthrough is not a decision; the request stays open for a cited one.
    pending_customer_decision: gate(!d.customerReviewIsCurrent || state.customerReview?.status === 'pending' || !state.customerReview?.evidenceRef, 'Pending customer decision', 'The current customer request must be pending before a decision can be recorded.', 'record_customer_decision'),
    letter_needed: gate(!letter, 'Approval letter not yet received', `A signed approval letter for version ${version} is already received.`, 'receive_approval_letter'),
    letter_available: gate(Boolean(approvalLetterFor(state.document)), 'Signed letter supplied by the customer', `The customer has not supplied a signed approval letter for version ${version} in this walkthrough.`, 'receive_approval_letter'),
    letter_receipt: gate(Boolean(letter), 'Approval letter receipt', `No signed approval letter for version ${version} has been received.`, 'receive_approval_letter'),
    letter_inspection_needed: gate(Boolean(letter) && !letter.inspection, 'Letter inspection not yet recorded', 'An inspection is already recorded for this letter.', 'inspect_approval_letter'),
    letter_accepted: gate(letter?.inspection?.status === 'accepted', 'Inspected approval letter', 'The signed approval letter needs an accepted inspection.', 'inspect_approval_letter'),
    authority_needed: gate(d.evidenceChecks.customer_authority.result !== 'passed', 'Authority not yet recorded', 'An authority record already supports a contact for this agreement.', 'record_customer_authority'),
    customer_authority: evidence('customer_authority'),
    current_version_basis: evidence('current_version_basis'),
    decision_citation: evidence('decision_citation'),
    customer_approval: gate(d.hasCustomerApproval, 'Recorded customer approval', 'A current customer approval record is needed for this document.', 'record_customer_decision'),
    open_requirement: gate(state.requirement.status === 'open', 'Open requirement', 'The recorded requirement must be open for a resolution transition.', 'resolve_requirement'),
    resolved_requirement: gate(state.requirement.status === 'resolved', 'Recorded requirement resolution', 'This requirement is already open.', 'resolve_requirement'),
  };
  const customerReview = ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request'];
  const inputs = {
    request_review: ['independent_reviewer', 'reviewable_plan'],
    approve_plan: ['independent_reviewer', 'plan_submission'],
    request_changes: ['independent_reviewer', 'plan_submission'],
    obtain_document: ['plan_approval'],
    verify_document: ['plan_approval', 'document_receipt', 'document_not_rejected', 'document_current_version', 'inspection_needed'],
    reject_document: ['plan_approval', 'document_receipt', 'document_not_rejected', 'inspection_needed'],
    request_customer_review: ['plan_approval', 'document_receipt', 'document_inspection', 'new_customer_request'],
    upload_approval_letter: [...customerReview, 'pending_customer_decision', 'letter_needed', 'letter_available'],
    inspect_approval_letter: ['letter_receipt', 'letter_inspection_needed'],
    record_customer_authority: ['letter_accepted', 'authority_needed'],
    record_customer_approval: [...customerReview, 'pending_customer_decision', 'customer_authority', 'current_version_basis'],
    resolve_requirement: [...customerReview, 'customer_approval', 'customer_authority', 'decision_citation', 'current_version_basis', 'open_requirement'],
    reopen_requirement: ['resolved_requirement'],
  };
  const recorded = {
    request_review: d.isPlanApproved || state.plan.status === 'in_review' ? 'The current plan revision has already been submitted.' : null,
    approve_plan: d.isPlanApproved ? 'The current plan revision is already approved.' : null,
    verify_document: d.isDocumentVerified ? 'A walkthrough inspection is already recorded for this version. Its independent version basis is checked separately.' : null,
    reject_document: d.isDocumentVerified ? 'This version was accepted at inspection.' : null,
    request_customer_review: d.customerReviewIsCurrent ? 'A current customer review request is already recorded.' : null,
    upload_approval_letter: letter ? `A signed approval letter for version ${version} is already received.` : null,
    inspect_approval_letter: letter?.inspection ? 'An inspection is already recorded for this letter.' : null,
    record_customer_authority: d.evidenceChecks.customer_authority.result === 'passed' ? 'An authority record already supports a contact for this agreement.' : null,
    record_customer_approval: d.hasCustomerApproval ? 'A current customer decision is recorded with its citation and approving contact.' : null,
    resolve_requirement: state.requirement.status === 'resolved' ? 'A walkthrough resolution is already recorded. Reopening needs a reason.' : null,
  };
  const refusals = {
    request_review: d.planReviewBlockedReason || 'This plan is already in review or approved.',
    approve_plan: d.planReviewBlockedReason || 'Submit the current revision for review before approving it.',
    request_changes: d.planReviewBlockedReason || 'Changes can be requested while the plan is in review.',
    obtain_document: 'Authorize the acquisition plan before attaching the document.',
    verify_document: d.documentRejected ? gates.document_not_rejected.reason : versionMismatch ? gates.document_current_version.reason : 'Attach a document under an approved plan before verifying it.',
    reject_document: d.documentRejected ? gates.document_not_rejected.reason : 'Attach a document under an approved plan before inspecting it.',
    request_customer_review: 'Verify the current document before requesting a new customer counsel review.',
    upload_approval_letter: d.customerReviewIsCurrent && !gates.letter_available.met ? gates.letter_available.reason : 'Request customer counsel review of the inspected version before adding the customer’s letter.',
    inspect_approval_letter: 'Receive the signed approval letter before inspecting it.',
    record_customer_authority: 'Accept the signed approval letter at inspection before recording who decides.',
    record_customer_approval: 'Request customer counsel review of the verified document before recording approval.',
    resolve_requirement: 'The requirement needs approval evidence for the verified current document before it can be resolved.',
    reopen_requirement: 'This requirement is already open.',
  };
  return Object.fromEntries(Object.entries(inputs).map(([command, codes]) => {
    const unmet = codes.filter(code => !gates[code].met);
    const evidenceOnly = unmet.length > 0 && unmet.every(code => gates[code].evidenceGap);
    return [command, {
      command, checkId: workflowTransitionChecks[command] || null,
      allowed: !unmet.length, recorded: Boolean(recorded[command]),
      recordedReason: recorded[command] || null,
      refusal: evidenceOnly ? `${command === 'resolve_requirement' ? 'The requirement needs approval evidence' : 'Recording the decision needs approval evidence'}: ${unmet.map(code => gates[code].reason).join(' ')}` : refusals[command],
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
  const documentRejected = state.document?.status === 'rejected';
  const isDocumentVerified = Boolean(state.document
    && state.document.status === 'verified'
    && state.document.version === state.document.currentVersion);
  const hasCustomerApproval = Boolean(isPlanApproved && isDocumentVerified
    && state.customerReview?.status === 'approved'
    && state.customerReview.planRevision === state.plan.revision
    && state.customerReview.documentId === state.document.id
    && state.customerReview.documentVersion === state.document.version
    && state.customerReview.evidenceRef && state.customerReview.contactId);
  const customerReviewIsCurrent = Boolean(isPlanApproved && isDocumentVerified
    && state.customerReview?.planRevision === state.plan.revision
    && state.customerReview?.documentId === state.document.id
    && state.customerReview?.documentVersion === state.document.version);
  const context = workflowEvidence(state, fixture);
  const evidenceChecks = Object.fromEntries(['customer_authority', 'current_version_basis', 'decision_citation'].map(id => [id, evaluateEvidenceCheck(id, state, context)]));
  const approvalLetter = coveringLetter(state);
  const authorityContact = evidenceChecks.customer_authority.supportedContact || null;
  const transitions = localTransitions(state, {isReviewerEligible, planReviewBlockedReason, isPlanApproved, isDocumentVerified, documentRejected, hasCustomerApproval, customerReviewIsCurrent, evidenceChecks, approvalLetter});
  const needsReplacement = Boolean(state.document && (state.document.version !== state.document.currentVersion || documentRejected));
  const decisionCommand = !approvalLetter ? 'upload_approval_letter' : !approvalLetter.inspection ? 'inspect_approval_letter'
    : !authorityContact ? 'record_customer_authority' : 'record_customer_approval';
  const nextCommand = !isPlanApproved ? state.plan.status === 'in_review' ? 'approve_plan' : 'request_review'
    : !state.document || needsReplacement ? 'obtain_document'
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
    else if (!state.document || needsReplacement && documentRejected) taskStage = 'obtain_document';
    else if (!isDocumentVerified) taskStage = 'verify_document';
    else if (!customerReviewIsCurrent) taskStage = 'request_customer_review';
    else if (!hasCustomerApproval) taskStage = {upload_approval_letter: 'awaiting_customer_letter', inspect_approval_letter: 'inspect_customer_letter', record_customer_authority: 'record_customer_authority'}[decisionCommand] || 'awaiting_customer_approval';
    else if (state.requirement.status !== 'resolved') taskStage = 'ready_to_resolve';
    else taskStage = 'complete';
  }
  return {
    isPlanApproved,
    isDocumentVerified,
    documentRejected,
    hasCustomerApproval,
    customerReviewIsCurrent,
    isReviewerEligible,
    planReviewBlockedReason,
    evidenceChecks, approvalLetter, authorityContact,
    transitions, nextCommand, needsReplacement,
    canRequestReview: transitions.request_review.allowed,
    canApprovePlan: transitions.approve_plan.allowed,
    canRequestChanges: transitions.request_changes.allowed,
    canObtainDocument: transitions.obtain_document.allowed && !state.document,
    canReplaceDocument: transitions.obtain_document.allowed && Boolean(state.document),
    canVerifyDocument: transitions.verify_document.allowed,
    canRejectDocument: transitions.reject_document.allowed,
    canRequestCustomerReview: transitions.request_customer_review.allowed,
    canUploadApprovalLetter: transitions.upload_approval_letter.allowed,
    canInspectApprovalLetter: transitions.inspect_approval_letter.allowed,
    canRecordCustomerAuthority: transitions.record_customer_authority.allowed,
    canRecordCustomerApproval: transitions.record_customer_approval.allowed,
    canResolveRequirement: transitions.resolve_requirement.allowed,
    canReopenRequirement: transitions.reopen_requirement.allowed,
    acquisitionStatus,
    documentStatus: isDocumentVerified ? 'Current version verified' : documentRejected ? 'Rejected at inspection' : state.document ? 'Received · unverified' : 'Not yet received',
    customerApprovalStatus: hasCustomerApproval ? 'Approval recorded' : !customerReviewIsCurrent ? 'Not requested'
      : ({upload_approval_letter: 'Awaiting customer counsel', inspect_approval_letter: 'Letter received · inspection needed', record_customer_authority: 'Letter accepted · authority not recorded'})[decisionCommand] || 'Ready to record decision',
    commercialStatus: state.commercial.label,
    taskStage,
    taskStageLabel: ({
      not_created: 'Task not created',
      awaiting_plan_approval: 'Awaiting plan reapproval',
      obtain_document: 'Obtain current terms',
      verify_document: 'Verify document version',
      request_customer_review: 'Request customer counsel review',
      awaiting_customer_letter: 'Awaiting the customer’s signed letter',
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
  const at = new Date(Date.parse(renewalFixture.workflowClockStart) + state.history.length * 60000).toISOString();
  return transition(state, {...action, at});
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
  const at = action.at || new Date(Date.parse(renewalFixture.workflowClockStart) + state.history.length * 60000).toISOString();
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

// Receipts are appended and never replaced; an inspection is added to its own receipt.
function inspectReceipt(state, id, inspection) {
  const evidence = evidenceOf(state);
  return {...evidence, artifactReceipts: evidence.artifactReceipts.map(receipt => receipt.id === id ? {...receipt, inspection} : receipt)};
}

export function transition(state, action) {
  const ready = derive(state);
  const at = action.at || new Date(Date.parse(renewalFixture.workflowClockStart) + state.history.length * 60000).toISOString();
  const eligibility = ready.transitions[action.type];
  if (eligibility && !eligibility.allowed) return rejected(state, eligibility.refusal);
  switch (action.type) {
    case 'reset':
      return initialState;
    case 'save_plan': {
      const fields = ['title', 'owner', 'specifications', 'instructions'];
      const previous = planSnapshot(state.plan);
      const values = Object.fromEntries(fields.map(field => [field, (action[field] ?? previous[field]).trim()]));
      if (fields.some(field => !values[field])) return rejected(state, 'Give the work plan a title, owner, specifications, and instructions.');
      const changedFields = fields.filter(field => values[field] !== previous[field]);
      if (!changedFields.length) return { ...state, plan: { ...state.plan, ...values }, error: null };
      const plan = { ...state.plan, ...values, revision: state.plan.revision + 1, status: 'draft', approvedRevision: null, requestedAt: null, approvedAt: null, approver: null, reviewNote: '' };
      return changed(state, action, {
        plan,
        task: state.task ? { ...state.task, title: values.title, owner: values.owner, status: 'blocked' } : null,
        document: state.document && changedFields.includes('specifications') && state.document.status === 'verified'
          ? { ...state.document, status: 'received', verifiedAt: null, verifiedBy: null }
          : state.document,
        customerReview: null,
        requirement: { ...state.requirement, status: 'open', resolvedAt: null, resolvedWith: null },
      }, `Plan saved as revision ${state.plan.revision + 1}`, 'Changes require acquisition reapproval and a new customer counsel review.', null, { changedFields, planSnapshot: planSnapshot(plan) });
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
      const fixture = fixtureEvidence[2];
      const supplied = action.document || {};
      const id = supplied.id || fixture.id, version = supplied.version || fixture.version;
      const document = {
        id,
        title: supplied.title || supplied.name || fixture.name,
        name: supplied.name || supplied.title || fixture.name,
        version,
        currentVersion: supplied.currentVersion || fixture.currentVersion,
        source: supplied.source || fixture.source,
        status: 'received',
        receivedAt: at,
        verifiedAt: null,
        verifiedBy: null,
        receiptId: receiptId(state, id),
        checksum: supplied.checksum || (supplied.id ? syntheticChecksum(`${id}:${version}`) : fixture.checksum),
        uploadedBy: text(action.actor),
      };
      if (state.document?.id === document.id && state.document?.version === document.version
        && state.document?.currentVersion === document.currentVersion && state.document.status !== 'rejected') return { ...state, error: null };
      const receipt = {id: document.receiptId, artifactId: id, kind: 'document', title: document.name, documentId: id, documentVersion: version,
        status: 'received', receivedAt: at, uploadedBy: document.uploadedBy, checksum: document.checksum, inspection: null};
      const evidence = evidenceOf(state);
      return changed(state, action, {
        document,
        evidence: {...evidence, artifactReceipts: [...evidence.artifactReceipts, receipt]},
        customerReview: null,
        task: { ...state.task, status: 'open' },
        requirement: { ...state.requirement, status: 'open', resolvedAt: null, resolvedWith: null },
      }, state.document ? 'Replacement terms attached' : 'AI terms received', `Version ${document.version} is received as receipt ${receipt.id}. Earlier receipts are kept. Inspection is next.`);
    }
    case 'verify_document': {
      const verifiedBy = action.verifiedBy?.trim() || state.plan.owner;
      const inspection = {status: 'accepted', at, recordedBy: verifiedBy, reason: text(action.reason)};
      return changed(state, action, {
        document: { ...state.document, status: 'verified', verifiedAt: at, verifiedBy },
        ...(state.document.receiptId ? {evidence: inspectReceipt(state, state.document.receiptId, inspection)} : {}),
      }, 'Current document version verified', `Version ${state.document.version} is accepted at inspection and ready for customer counsel. This is not customer approval.`);
    }
    case 'reject_document': {
      const reason = text(action.reason);
      if (!reason) return rejected(state, 'Give a reason for rejecting this version. The rejected version is kept.');
      const rejectedBy = text(action.actor) || state.plan.owner;
      const inspection = {status: 'rejected', at, recordedBy: rejectedBy, reason};
      return changed(state, action, {
        document: { ...state.document, status: 'rejected', rejectedAt: at, rejectedBy, rejectionReason: reason },
        ...(state.document.receiptId ? {evidence: inspectReceipt(state, state.document.receiptId, inspection)} : {}),
        task: state.task ? { ...state.task, status: 'open' } : null,
      }, 'Document version rejected at inspection', `Version ${state.document.version} was rejected. ${reason} The receipt is kept; acquisition needs a replacement.`);
    }
    case 'request_customer_review':
      return changed(state, action, {
        customerReview: { status: 'pending', requestedAt: at, requestedBy: state.plan.owner, documentId: state.document.id, documentVersion: state.document.version, planRevision: state.plan.revision, reviewedBy: null, recordedBy: null, evidenceText: null, approvedAt: null },
      }, 'Customer counsel review requested', `Awaiting the customer’s decision on AI rider version ${state.document.version}.`);
    case 'upload_approval_letter': {
      const letter = approvalLetterFor(state.document);
      const evidence = evidenceOf(state);
      const receipt = {id: receiptId(state, letter.artifactId), artifactId: letter.artifactId, kind: 'approval_letter', title: letter.name,
        approves: {...letter.approves}, source: letter.source, status: 'received', receivedAt: at, uploadedBy: text(action.actor),
        checksum: letter.checksum, inspection: null};
      return changed(state, action, {
        evidence: {...evidence, artifactReceipts: [...evidence.artifactReceipts, receipt]},
      }, 'Signed approval letter received', `Receipt ${receipt.id} records the customer’s letter on version ${state.document.version}. Inspection is next. Nobody is named until a reviewer records who decides.`);
    }
    case 'inspect_approval_letter': {
      const outcome = ['accepted', 'rejected'].includes(action.outcome) ? action.outcome : null;
      const reason = text(action.reason);
      if (!outcome) return rejected(state, 'Accept or reject the signed letter at inspection.');
      if (outcome === 'rejected' && !reason) return rejected(state, 'Give a reason for rejecting the letter. Its receipt is kept.');
      const receipt = ready.approvalLetter;
      const inspector = text(action.actor) || fixturePeople.internalReviewer;
      const evidence = inspectReceipt(state, receipt.id, {status: outcome, at, recordedBy: inspector, reason});
      if (outcome === 'accepted') evidence.documentVersionBasis = {id: `version-basis-${receipt.id}`, revision: '1', status: 'accepted', recordedAt: at, recordedBy: inspector,
        agreementId: receipt.approves.agreementId, documentId: receipt.approves.documentId, version: receipt.approves.version, source: {kind: 'receipt', id: receipt.id}};
      return changed(state, action, {evidence}, outcome === 'accepted' ? 'Approval letter accepted at inspection' : 'Approval letter rejected at inspection',
        outcome === 'accepted' ? `The letter names version ${receipt.approves.version} for ${renewalFixture.agreement.label}. That is recorded as the version basis, independent of the document file.${reason ? ` ${reason}` : ''}`
          : `${reason} The receipt is kept. A new letter is needed.`);
    }
    case 'record_customer_authority': {
      const receipt = ready.approvalLetter;
      const letter = approvalLetters.find(item => item.artifactId === receipt.artifactId);
      const recordedBy = text(action.actor) || fixturePeople.internalReviewer;
      const evidence = evidenceOf(state);
      const contact = {id: letter.contactId, displayName: letter.signatory.name, role: letter.signatory.role, status: 'active',
        recordedAt: at, recordedBy, sourceRef: {kind: 'receipt', id: receipt.id}};
      const previous = evidence.customerAuthority;
      const customerAuthority = {id: `authority-${letter.contactId.replace(/^contact-/, '')}`, revision: String(Number(previous?.revision || 0) + 1), status: 'accepted',
        recordedAt: at, recordedBy, agreementId: receipt.approves.agreementId, contactId: contact.id, evidenceRef: {kind: 'receipt', id: receipt.id}};
      return changed(state, action, {
        evidence: {...evidence, contacts: [...evidence.contacts.filter(item => item.id !== contact.id), contact], customerAuthority},
      }, 'Customer authority recorded', `${contact.displayName}, ${contact.role.toLowerCase()}, is recorded as the contact who decides for ${renewalFixture.agreement.label}, from the signed approval letter (receipt ${receipt.id}).`);
    }
    case 'record_customer_approval': {
      if (!action.evidenceRef) return rejected(state, 'Typed text is not a customer decision. Cite an inspected receipt or a unit, and name the approving contact from the authority record.');
      const context = workflowEvidence(state);
      const reference = resolveEvidenceReference(action.evidenceRef, context);
      if (reference.issue) return rejected(state, `The decision cannot cite that evidence. ${reference.issue}`);
      if (!['receipt', 'unit'].includes(action.evidenceRef.kind)) return rejected(state, 'A customer decision must cite an inspected receipt or a unit.');
      const issues = citationIssues(reference, state.document, renewalFixture.agreement.id);
      if (issues.length) return rejected(state, `The decision cannot cite that evidence. ${issues.join(' ')}`);
      if (action.documentVersion && action.documentVersion !== state.document.version) return rejected(state, `The decision must be bound to version ${state.document.version}, the version under review.`);
      const contact = supportedAuthority(context);
      if (!contact || action.contactId !== contact.id) return rejected(state, 'Name the approving contact from the authority record for this agreement.');
      const recordedBy = text(action.actor) || text(action.recordedBy) || fixturePeople.internalReviewer;
      if (recordedBy === contact.displayName) return rejected(state, 'The person recording a decision cannot be its approver.');
      const evidenceRef = {...action.evidenceRef};
      const decision = {id: `decision-${state.history.length + 1}`, agreementId: renewalFixture.agreement.id, contactId: contact.id, reviewedBy: contact.displayName,
        recordedBy, evidenceRef, evidenceText: null, approvedAt: at};
      return changed(state, action, {
        customerReview: { ...state.customerReview, status: 'approved', ...decision },
      }, 'Customer decision recorded', `${recordedBy} recorded ${contact.displayName}’s approval of version ${state.document.version}, citing ${evidenceRef.kind === 'receipt' ? `receipt ${evidenceRef.id}` : `unit ${evidenceRef.n}`}.`, {
        reviewedBy: contact.displayName,
        contactId: contact.id,
        recordedBy,
        evidenceRef,
        documentId: state.document.id,
        documentVersion: state.document.version,
        planRevision: state.plan.revision,
        approvedAt: at,
      });
    }
    case 'resolve_requirement': {
      const context = workflowEvidence(state);
      return changed(state, action, {
        requirement: { ...state.requirement, status: 'resolved', resolvedAt: at, resolvedWith: {
          decisionId: state.customerReview.id || null, evidenceRef: state.customerReview.evidenceRef,
          authorityId: context.customerAuthority?.id || null, versionBasisId: context.documentVersionBasis?.id || null,
        } },
      }, 'Legal requirement resolved', 'Resolved from a cited customer decision, an authority record and an independent version basis. Renewal remains unconfirmed.');
    }
    case 'reopen_requirement':
      if (!action.reason?.trim()) return rejected(state, 'Give a reason for reopening the requirement.');
      return changed(state, action, {
        requirement: { ...state.requirement, status: 'open', resolvedAt: null, resolvedWith: null },
        task: state.task ? { ...state.task, status: 'open' } : null,
        customerReview: null,
      }, 'Legal requirement reopened', `${action.reason.trim()} A new customer counsel review is required before resolving it again.`);
    default:
      return rejected(state, 'That action is not available in this demo.');
  }
}
