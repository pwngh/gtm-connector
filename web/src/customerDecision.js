import {derive} from './workflow.js';

export function createCustomerDecisionInput(request) {
  return {
    documentId: request?.documentId || null, documentVersion: request?.documentVersion || null,
    planRevision: request?.planRevision ?? null, requestedAt: request?.requestedAt || null,
    reviewedBy: '', evidenceText: '', confirmedVersion: false,
  };
}

export function canSubmitCustomerDecision(state, input) {
  const request = state.customerReview;
  return Boolean(derive(state).canRecordCustomerApproval && request && input
    && ['documentId', 'documentVersion', 'planRevision', 'requestedAt'].every(field => input[field] === request[field])
    && input.confirmedVersion && input.reviewedBy?.trim() && input.evidenceText?.trim());
}
