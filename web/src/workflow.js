import {fixtureEvidence, initialState, fixturePeople, renewalFixture} from './renewalFixtures.js';
export {fixtureEvidence, initialState} from './renewalFixtures.js';

export function derive(state, reviewer = fixturePeople.internalReviewer) {
  const isReviewerEligible = Boolean(reviewer && state.plan.owner?.trim() && state.plan.owner.trim() !== reviewer);
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
    canRequestReview: isReviewerEligible && ['draft', 'changes_requested'].includes(state.plan.status),
    canApprovePlan: isReviewerEligible && state.plan.status === 'in_review',
    canRequestChanges: isReviewerEligible && state.plan.status === 'in_review',
    canObtainDocument: isPlanApproved && !state.document,
    canVerifyDocument: isPlanApproved && Boolean(state.document) && !isDocumentVerified,
    canRequestCustomerReview: isPlanApproved && isDocumentVerified && !customerReviewIsCurrent,
    canRecordCustomerApproval: isPlanApproved && isDocumentVerified
      && customerReviewIsCurrent && state.customerReview?.status === 'pending',
    canResolveRequirement: hasCustomerApproval && state.requirement.status === 'open',
    canReopenRequirement: state.requirement.status === 'resolved',
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
      if (!ready.canRequestReview) return rejected(state, ready.planReviewBlockedReason || 'This plan is already in review or approved.');
      return changed(state, action, {
        plan: { ...state.plan, status: 'in_review', requestedAt: at, reviewNote: '' },
      }, `Revision ${state.plan.revision} submitted for review`, 'Review authorizes obtaining the document. The document may still be missing.');
    case 'approve_plan':
      if (!ready.canApprovePlan) return rejected(state, ready.planReviewBlockedReason || 'Submit the current revision for review before approving it.');
      return changed(state, action, {
        plan: { ...state.plan, status: 'approved', approvedRevision: state.plan.revision, approvedAt: at, approver: action.approver?.trim() || fixturePeople.internalReviewer },
        task: state.task
          ? { ...state.task, title: state.plan.title, owner: state.plan.owner, status: 'open', planRevision: state.plan.revision }
          : { id: renewalFixture.taskId, title: state.plan.title, owner: state.plan.owner, status: 'open', createdAt: at, planRevision: state.plan.revision },
      }, `Acquisition authorized for revision ${state.plan.revision}`, state.task ? 'The existing acquisition task is ready to continue.' : 'One acquisition task was created. Customer approval remains outstanding.', null, { planSnapshot: planSnapshot(state.plan) });
    case 'request_changes':
      if (!ready.canRequestChanges) return rejected(state, ready.planReviewBlockedReason || 'Changes can be requested while the plan is in review.');
      return changed(state, action, {
        plan: { ...state.plan, status: 'changes_requested', approvedRevision: null, reviewNote: action.note?.trim() || 'Clarify the acquisition owner and the document source.' },
      }, 'Reviewer requested changes', action.note?.trim() || 'Clarify the acquisition owner and the document source.');
    case 'obtain_document': {
      if (!ready.isPlanApproved) return rejected(state, 'Authorize the acquisition plan before attaching the document.');
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
      if (!ready.canVerifyDocument) return rejected(state, 'Attach a document under an approved plan before verifying it.');
      if (state.document.version !== state.document.currentVersion) return rejected(state, `Version ${state.document.version} is not current. Attach version ${state.document.currentVersion} before verification.`);
      return changed(state, action, {
        document: { ...state.document, status: 'verified', verifiedAt: at, verifiedBy: action.verifiedBy?.trim() || state.plan.owner },
      }, 'Current document version verified', `Version ${state.document.version} is ready for customer counsel. This is not customer approval.`);
    case 'request_customer_review':
      if (!ready.canRequestCustomerReview) return rejected(state, 'Verify the current document before requesting a new customer counsel review.');
      return changed(state, action, {
        customerReview: { status: 'pending', requestedAt: at, requestedBy: state.plan.owner, documentId: state.document.id, documentVersion: state.document.version, planRevision: state.plan.revision, reviewedBy: null, recordedBy: null, evidenceText: null, approvedAt: null },
      }, 'Customer counsel review requested', `Awaiting a recorded decision on AI terms version ${state.document.version}.`);
    case 'record_customer_approval': {
      if (!ready.canRecordCustomerApproval) return rejected(state, 'Request customer counsel review of the verified document before recording approval.');
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
      if (!ready.canResolveRequirement) return rejected(state, 'The requirement needs approval evidence for the verified current document before it can be resolved.');
      return changed(state, action, {
        requirement: { ...state.requirement, status: 'resolved', resolvedAt: at },
        task: { ...state.task, status: 'complete' },
      }, 'Legal requirement resolved', 'Acquisition and customer approval evidence are complete. Renewal remains unconfirmed.');
    case 'reopen_requirement':
      if (!ready.canReopenRequirement) return rejected(state, 'This requirement is already open.');
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
