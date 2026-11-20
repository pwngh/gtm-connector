import {derive} from './workflow.js';
import {walkthroughActors} from './renewalFixtures.js';
import {citationIssues, latestInspection, workflowEvidence} from './evidenceChecks.js';

// The receipts a decision may cite: only an accepted signed letter for this exact agreement, document and
// version, signed by the contact the authority record supports. The same rule the reducer and checks apply.
export function decisionCitationOptions(state) {
  const d = state ? derive(state) : null;
  const letter = d?.approvalLetter;
  if (!letter || latestInspection(letter)?.status !== 'accepted') return [];
  return citationIssues({kind: 'receipt', id: letter.id}, workflowEvidence(state), state.document, d.authorityContact?.id || null).length ? [] : [letter];
}

// The decision cites evidence and names the recorded contact. No field takes typed approval text.
export function createCustomerDecisionInput(state) {
  const request = state?.customerReview, d = state ? derive(state) : null;
  const [letter] = decisionCitationOptions(state);
  return {
    documentId: request?.documentId || null, documentVersion: request?.documentVersion || null,
    planRevision: request?.planRevision ?? null, requestedAt: request?.requestedAt || null,
    evidenceRef: letter ? {kind: 'receipt', id: letter.id} : null,
    contactId: d?.authorityContact?.id || null, confirmedVersion: false,
  };
}

export function canSubmitCustomerDecision(state, input) {
  const request = state.customerReview;
  return Boolean(derive(state).canRecordCustomerApproval && request && input
    && ['documentId', 'documentVersion', 'planRevision', 'requestedAt'].every(field => input[field] === request[field])
    && input.confirmedVersion && decisionCitationOptions(state).some(letter => letter.id === input.evidenceRef?.id) && input.contactId);
}

// The command fields for each walkthrough step: the signed-in demo principal who acts, by principal ID.
// The plan owner handles collection and uploads; the reviewer inspects, records and resolves. Reasons and
// outcomes are never filled in here: the operator types them in the dialog.
export function walkthroughFields(state, type) {
  const {reviewer, reviewerId} = walkthroughActors;
  const acquisition = state?.plan?.owner?.trim() || walkthroughActors.acquisition;
  const acquisitionId = state?.plan?.ownerId || walkthroughActors.acquisitionId;
  const decision = createCustomerDecisionInput(state);
  return ({
    save_plan: {actorId: acquisitionId},
    obtain_document: {actorId: acquisitionId},
    verify_document: {verifiedBy: acquisition, actorId: acquisitionId},
    reject_document: {actorId: acquisitionId},
    upload_approval_letter: {actorId: acquisitionId},
    inspect_approval_letter: {actorId: reviewerId},
    record_customer_authority: {actorId: reviewerId},
    record_customer_approval: {actorId: reviewerId, evidenceRef: decision.evidenceRef, contactId: decision.contactId, documentVersion: decision.documentVersion},
    resolve_requirement: {actorId: reviewerId},
    reopen_requirement: {actorId: reviewerId},
    withdraw_customer_authority: {actorId: reviewerId},
    approve_plan: {approver: reviewer},
  })[type] || {};
}

// The steps between a customer review request and a recorded decision.
export const customerDecisionSteps = ['upload_approval_letter', 'inspect_approval_letter', 'record_customer_authority', 'record_customer_approval'];

// Every command of the walkthrough in order, from plan review to resolution.
export const walkthroughCommands = ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', ...customerDecisionSteps, 'resolve_requirement'];
