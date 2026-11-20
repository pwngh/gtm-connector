import {derive} from './workflow.js';
import {walkthroughActors} from './renewalFixtures.js';

// The decision cites evidence and names the recorded contact. No field takes typed approval text.
export function createCustomerDecisionInput(state) {
  const request = state?.customerReview, d = state ? derive(state) : null;
  return {
    documentId: request?.documentId || null, documentVersion: request?.documentVersion || null,
    planRevision: request?.planRevision ?? null, requestedAt: request?.requestedAt || null,
    evidenceRef: d?.approvalLetter?.inspection?.status === 'accepted' ? {kind: 'receipt', id: d.approvalLetter.id} : null,
    contactId: d?.authorityContact?.id || null, confirmedVersion: false,
  };
}

export function canSubmitCustomerDecision(state, input) {
  const request = state.customerReview;
  return Boolean(derive(state).canRecordCustomerApproval && request && input
    && ['documentId', 'documentVersion', 'planRevision', 'requestedAt'].every(field => input[field] === request[field])
    && input.confirmedVersion && input.evidenceRef?.id && input.contactId);
}

// The command fields for each walkthrough step, with its explicit actor. The signed-in demo
// principal records; the account owner handles collection. The approver never records.
export function walkthroughFields(state, type) {
  const {reviewer} = walkthroughActors, acquisition = state?.plan?.owner?.trim() || walkthroughActors.acquisition;
  const decision = createCustomerDecisionInput(state);
  return ({
    obtain_document: {actor: acquisition},
    verify_document: {verifiedBy: acquisition},
    reject_document: {actor: acquisition},
    upload_approval_letter: {actor: acquisition},
    inspect_approval_letter: {outcome: 'accepted', actor: reviewer, reason: 'Signed and legible. It names the agreement and the version under review.'},
    record_customer_authority: {actor: reviewer},
    record_customer_approval: {actor: reviewer, evidenceRef: decision.evidenceRef, contactId: decision.contactId, documentVersion: decision.documentVersion},
    approve_plan: {approver: reviewer},
  })[type] || {};
}

// The steps between a customer review request and a recorded decision.
export const customerDecisionSteps = ['upload_approval_letter', 'inspect_approval_letter', 'record_customer_authority', 'record_customer_approval'];

// Every command of the walkthrough in order, from plan review to resolution.
export const walkthroughCommands = ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', ...customerDecisionSteps, 'resolve_requirement'];
