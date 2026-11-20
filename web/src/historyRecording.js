import {readinessFixture} from './renewalFixtures.js';

const clone = value => JSON.parse(JSON.stringify(value));
const resources = ['plan', 'document', 'customerReview', 'requirement', 'requirements', 'task', 'commercial'];

export function historyWorkflowSnapshot(state) {
  return clone(Object.fromEntries(resources.map(key => [key, state[key] ?? null])));
}

export function historyEvidenceSnapshot(fixture = readinessFixture) {
  const keys = ['agreement', 'card', 'documentVersionBasis', 'customerAuthority', 'decisionReceipt', 'contacts', 'artifactReceipts', 'policyVersion'];
  return clone(Object.fromEntries(keys.map(key => [key, fixture[key] ?? null])));
}

// Observe committed inputs only. This does not decide a transition.
export function recordHistoryInputs(before, patch, action) {
  const after = {...before, ...patch};
  const actor = action.actor || (action.type === 'approve_plan' ? patch.plan?.approver
    : action.type === 'verify_document' ? patch.document?.verifiedBy
    : action.type === 'request_customer_review' ? patch.customerReview?.requestedBy
    : action.type === 'record_customer_approval' ? patch.customerReview?.recordedBy : null);
  return {
    schema: '1', before: historyWorkflowSnapshot(before), after: historyWorkflowSnapshot(after),
    contextBefore: historyEvidenceSnapshot(), contextAfter: historyEvidenceSnapshot(),
    actor: typeof actor === 'string' && actor.trim() ? actor.trim() : null,
    eventAt: action.eventAt || null,
    reason: action.reason?.trim() || action.note?.trim() || null,
  };
}
