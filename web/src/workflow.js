import {fixtureEvidence, initialState, fixturePeople, renewalFixture} from './renewalFixtures.js';
export {fixtureEvidence, initialState} from './renewalFixtures.js';

export const workflowTransitionChecks = {
  request_review: 'submit_plan', approve_plan: 'approve_plan', obtain_document: 'receive_document',
  verify_document: 'verify_document', request_customer_review: 'request_customer_review',
  record_customer_approval: 'record_customer_decision', resolve_requirement: 'resolve_requirement',
};

export const transitionEvidenceChecks = {
  record_customer_approval: ['customer_authority', 'decision_citation', 'current_version_basis'],
  resolve_requirement: ['customer_authority', 'decision_citation', 'current_version_basis'],
};

function localTransitions(state, d) {
  const gate = (met, name, reason, checkId) => ({met, name, reason, checkId});
  const versionMismatch = state.document && state.document.version !== state.document.currentVersion;
  const gates = {
    independent_reviewer: gate(d.isReviewerEligible, 'Independent internal reviewer', d.planReviewBlockedReason, 'submit_plan'),
    reviewable_plan: gate(['draft', 'changes_requested'].includes(state.plan.status), 'Plan ready for submission', 'The plan must be a draft or have changes requested before submission.', 'submit_plan'),
    plan_submission: gate(state.plan.status === 'in_review', 'Current plan submitted for review', 'Submit the current plan revision before it can be approved.', 'submit_plan'),
    plan_approval: gate(d.isPlanApproved, 'Current plan approval', 'The current plan revision needs internal approval.', 'approve_plan'),
    document_receipt: gate(Boolean(state.document), 'Document receipt', 'The applicable document has not been received.', 'receive_document'),
    document_current_version: gate(!versionMismatch, 'Walkthrough version match', `Version ${state.document?.version || 'unknown'} is not current. Attach version ${state.document?.currentVersion || 'unknown'} before verification.`, 'receive_document'),
    inspection_needed: gate(!d.isDocumentVerified, 'Inspection not yet recorded', 'An inspection is already recorded for this version.', 'verify_document'),
    document_inspection: gate(d.isDocumentVerified, 'Document inspection', 'The received version needs a current walkthrough inspection.', 'verify_document'),
    customer_request: gate(d.customerReviewIsCurrent, 'Current customer review request', 'A review request must match the current walkthrough inputs.', 'request_customer_review'),
    new_customer_request: gate(!d.customerReviewIsCurrent, 'New customer review needed', 'A current customer review request is already recorded.', 'request_customer_review'),
    pending_customer_decision: gate(!d.customerReviewIsCurrent || state.customerReview?.status === 'pending', 'Pending customer decision', 'The current customer request must be pending before a decision can be recorded.', 'record_customer_decision'),
    customer_approval: gate(d.hasCustomerApproval, 'Recorded customer approval', 'A current customer approval record is needed for this document.', 'record_customer_decision'),
    open_requirement: gate(state.requirement.status === 'open', 'Open requirement', 'The recorded requirement must be open for a resolution transition.', 'resolve_requirement'),
    resolved_requirement: gate(state.requirement.status === 'resolved', 'Recorded requirement resolution', 'This requirement is already open.', 'resolve_requirement'),
  };
  const inputs = {
    request_review: ['independent_reviewer', 'reviewable_plan'],
    approve_plan: ['independent_reviewer', 'plan_submission'],
    request_changes: ['independent_reviewer', 'plan_submission'],
    obtain_document: ['plan_approval'],
    verify_document: ['plan_approval', 'document_receipt', 'document_current_version', 'inspection_needed'],
    request_customer_review: ['plan_approval', 'document_receipt', 'document_inspection', 'new_customer_request'],
    record_customer_approval: ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'pending_customer_decision'],
    resolve_requirement: ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'customer_approval', 'open_requirement'],
    reopen_requirement: ['resolved_requirement'],
  };
  const recorded = {
    request_review: d.isPlanApproved || state.plan.status === 'in_review' ? 'The current plan revision has already been submitted.' : null,
    approve_plan: d.isPlanApproved ? 'The current plan revision is already approved.' : null,
    verify_document: d.isDocumentVerified ? 'A walkthrough inspection is already recorded for this version. Its independent version basis is checked separately.' : null,
    request_customer_review: d.customerReviewIsCurrent ? 'A current customer review request is already recorded.' : null,
    record_customer_approval: d.hasCustomerApproval ? 'The walkthrough already has a current decision record. Its evidence is checked separately.' : null,
    resolve_requirement: state.requirement.status === 'resolved' ? 'A walkthrough resolution is already recorded. Evidence checks remain separate, and reopening needs a reason.' : null,
  };
  const refusals = {
    request_review: d.planReviewBlockedReason || 'This plan is already in review or approved.',
    approve_plan: d.planReviewBlockedReason || 'Submit the current revision for review before approving it.',
    request_changes: d.planReviewBlockedReason || 'Changes can be requested while the plan is in review.',
    obtain_document: 'Authorize the acquisition plan before attaching the document.',
    verify_document: versionMismatch ? gates.document_current_version.reason : 'Attach a document under an approved plan before verifying it.',
    request_customer_review: 'Verify the current document before requesting a new customer counsel review.',
    record_customer_approval: 'Request customer counsel review of the verified document before recording approval.',
    resolve_requirement: 'The requirement needs approval evidence for the verified current document before it can be resolved.',
    reopen_requirement: 'This requirement is already open.',
  };
  return Object.fromEntries(Object.entries(inputs).map(([command, codes]) => [command, {
    command, checkId: workflowTransitionChecks[command] || null,
    allowed: codes.every(code => gates[code].met), recorded: Boolean(recorded[command]),
    recordedReason: recorded[command] || null, refusal: refusals[command],
    replacement: command === 'obtain_document' && Boolean(state.document),
    blockedReasons: recorded[command] ? [] : codes.filter(code => !gates[code].met).map(code => {
      const {met, ...reason} = gates[code];
      return {code, action: command, ...reason};
    }),
  }]));
}

export function derive(state, reviewer = fixturePeople.internalReviewer) {
  // A supplied reviewer cannot relax the walkthrough's independent-review rule.
  const isReviewerEligible = Boolean(reviewer && state.plan.owner?.trim() && state.plan.owner.trim() !== reviewer
    && state.plan.owner.trim() !== fixturePeople.internalReviewer);
  const planReviewBlockedReason = isReviewerEligible ? null
    : 'An independent reviewer is needed. The plan owner cannot review their own work.';
  const isPlanApproved = state.plan.status === 'approved'
    && state.plan.approvedRevision === state.plan.revision;
  const isDocumentVerified = Boolean(state.document
    && state.document.status === 'verified'
    && state.document.version === state.document.currentVersion);
  const hasCustomerApproval = Boolean(isPlanApproved && isDocumentVerified
    && state.customerReview?.status === 'approved'
    && state.customerReview.planRevision === state.plan.revision
    && state.customerReview.documentId === state.document.id
    && state.customerReview.documentVersion === state.document.version
    && state.customerReview.evidenceText?.trim()
    && state.customerReview.reviewedBy?.trim());
  const customerReviewIsCurrent = Boolean(isPlanApproved && isDocumentVerified
    && state.customerReview?.planRevision === state.plan.revision
    && state.customerReview?.documentId === state.document.id
    && state.customerReview?.documentVersion === state.document.version);
  const transitions = localTransitions(state, {isReviewerEligible, planReviewBlockedReason, isPlanApproved, isDocumentVerified, hasCustomerApproval, customerReviewIsCurrent});
  const needsReplacement = Boolean(state.document && state.document.version !== state.document.currentVersion);
  const nextCommand = !isPlanApproved ? state.plan.status === 'in_review' ? 'approve_plan' : 'request_review'
    : !state.document || needsReplacement ? 'obtain_document'
    : !isDocumentVerified ? 'verify_document'
    : !customerReviewIsCurrent ? 'request_customer_review'
    : !hasCustomerApproval ? 'record_customer_approval'
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
    else if (!state.document) taskStage = 'obtain_document';
    else if (!isDocumentVerified) taskStage = 'verify_document';
    else if (!customerReviewIsCurrent) taskStage = 'request_customer_review';
    else if (!hasCustomerApproval) taskStage = 'awaiting_customer_approval';
    else if (state.requirement.status !== 'resolved') taskStage = 'ready_to_resolve';
    else taskStage = 'complete';
  }
  return {
    isPlanApproved,
    isDocumentVerified,
    hasCustomerApproval,
    customerReviewIsCurrent,
    isReviewerEligible,
    planReviewBlockedReason,
    transitions, nextCommand, needsReplacement,
    canRequestReview: transitions.request_review.allowed,
    canApprovePlan: transitions.approve_plan.allowed,
    canRequestChanges: transitions.request_changes.allowed,
    canObtainDocument: transitions.obtain_document.allowed && !state.document,
    canReplaceDocument: transitions.obtain_document.allowed && Boolean(state.document),
    canVerifyDocument: transitions.verify_document.allowed,
    canRequestCustomerReview: transitions.request_customer_review.allowed,
    canRecordCustomerApproval: transitions.record_customer_approval.allowed,
    canResolveRequirement: transitions.resolve_requirement.allowed,
    canReopenRequirement: transitions.reopen_requirement.allowed,
    acquisitionStatus,
    documentStatus: isDocumentVerified ? 'Current version verified' : state.document ? 'Received · unverified' : 'Not yet received',
    customerApprovalStatus: hasCustomerApproval ? 'Approval recorded' : customerReviewIsCurrent ? 'Awaiting customer counsel' : 'Not requested',
    commercialStatus: state.commercial.label,
    taskStage,
    taskStageLabel: ({
      not_created: 'Task not created',
      awaiting_plan_approval: 'Awaiting plan reapproval',
      obtain_document: 'Obtain current terms',
      verify_document: 'Verify document version',
      request_customer_review: 'Request customer counsel review',
      awaiting_customer_approval: 'Awaiting customer counsel approval',
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
  return {
    ...state,
    ...patch,
    error: null,
    history: [...state.history, { id: `activity-${state.history.length + 1}`, type: action.type, at, title, detail, ...(evidenceSnapshot ? { evidenceSnapshot } : {}), ...eventDetails }],
  };
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
        document: state.document && changedFields.includes('specifications')
          ? { ...state.document, status: 'received', verifiedAt: null, verifiedBy: null }
          : state.document,
        customerReview: null,
        requirement: { ...state.requirement, status: 'open', resolvedAt: null },
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
      const document = {
        id: supplied.id || fixture.id,
        title: supplied.title || supplied.name || fixture.name,
        name: supplied.name || supplied.title || fixture.name,
        version: supplied.version || fixture.version,
        currentVersion: supplied.currentVersion || fixture.currentVersion,
        source: supplied.source || fixture.source,
        status: 'received',
        receivedAt: at,
        verifiedAt: null,
        verifiedBy: null,
      };
      if (state.document?.id === document.id && state.document?.version === document.version
        && state.document?.currentVersion === document.currentVersion) return { ...state, error: null };
      return changed(state, action, {
        document,
        customerReview: null,
        task: { ...state.task, status: 'open' },
        requirement: { ...state.requirement, status: 'open', resolvedAt: null },
      }, state.document ? 'Replacement terms attached' : 'AI terms received', `Version ${document.version} is attached and still needs verification.`);
    }
    case 'verify_document':
      return changed(state, action, {
        document: { ...state.document, status: 'verified', verifiedAt: at, verifiedBy: action.verifiedBy?.trim() || state.plan.owner },
      }, 'Current document version verified', `Version ${state.document.version} is ready for customer counsel. This is not customer approval.`);
    case 'request_customer_review':
      return changed(state, action, {
        customerReview: { status: 'pending', requestedAt: at, requestedBy: state.plan.owner, documentId: state.document.id, documentVersion: state.document.version, planRevision: state.plan.revision, reviewedBy: null, recordedBy: null, evidenceText: null, approvedAt: null },
      }, 'Customer counsel review requested', `Awaiting a recorded decision on AI terms version ${state.document.version}.`);
    case 'record_customer_approval': {
      const evidenceText = action.evidenceText?.trim();
      const reviewedBy = action.reviewedBy?.trim();
      const recordedBy = action.recordedBy?.trim() || fixturePeople.internalReviewer;
      if (!evidenceText || !reviewedBy) return rejected(state, 'Record the approving customer reviewer and their approval evidence.');
      return changed(state, action, {
        customerReview: { ...state.customerReview, status: 'approved', reviewedBy, recordedBy, evidenceText, approvedAt: at },
      }, 'Customer counsel approval recorded', `${recordedBy} recorded ${reviewedBy}'s approval of document version ${state.document.version}.`, {
        evidenceText,
        reviewedBy,
        recordedBy,
        documentId: state.document.id,
        documentVersion: state.document.version,
        planRevision: state.plan.revision,
        approvedAt: at,
      });
    }
    case 'resolve_requirement':
      return changed(state, action, {
        requirement: { ...state.requirement, status: 'resolved', resolvedAt: at },
      }, 'Legal requirement resolved', 'Acquisition and customer approval evidence are complete. Renewal remains unconfirmed.');
    case 'reopen_requirement':
      if (!action.reason?.trim()) return rejected(state, 'Give a reason for reopening the requirement.');
      return changed(state, action, {
        requirement: { ...state.requirement, status: 'open', resolvedAt: null },
        task: state.task ? { ...state.task, status: 'open' } : null,
        customerReview: null,
      }, 'Legal requirement reopened', `${action.reason.trim()} A new customer counsel review is required before resolving it again.`);
    default:
      return rejected(state, 'That action is not available in this demo.');
  }
}
