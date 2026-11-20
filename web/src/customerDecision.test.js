import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState, walkthroughDecisions, walkthroughDocumentExamples} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {canSubmitCustomerDecision, createCustomerDecisionInput} from './customerDecision.js';

function requested(document) {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) {
    state = transitionWalkthrough(state, {type, ...(type === 'obtain_document' ? {document} : {})});
  }
  return state;
}

test('decision input starts blank and is bound to the active request, not the default document version', () => {
  const state = requested(walkthroughDocumentExamples.replacement), input = createCustomerDecisionInput(state.customerReview);
  assert.equal(input.documentVersion, walkthroughDocumentExamples.replacement.version);
  assert.equal(input.documentId, state.customerReview.documentId);
  assert.equal(input.requestedAt, state.customerReview.requestedAt);
  assert.equal(input.evidenceText, '');
  assert.equal(input.reviewedBy, '');
  assert.equal(input.confirmedVersion, false);
  assert.equal(canSubmitCustomerDecision(state, input), false);
});

test('nonblank explicit input and version confirmation are required, and stale requests cannot be submitted', () => {
  const current = requested(), input = {...createCustomerDecisionInput(current.customerReview),
    reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step, confirmedVersion: true};
  assert.equal(canSubmitCustomerDecision(current, input), true);
  for (const invalid of [{reviewedBy: ''}, {reviewedBy: ' '}, {evidenceText: ''}, {evidenceText: ' '}, {confirmedVersion: false}]) {
    assert.equal(canSubmitCustomerDecision(current, {...input, ...invalid}), false);
  }
  assert.equal(canSubmitCustomerDecision(initialState, input), false);
  assert.equal(canSubmitCustomerDecision(requested(walkthroughDocumentExamples.replacement), input), false);
  assert.equal(canSubmitCustomerDecision(current, {...input, requestedAt: null}), false);
  assert.equal(canSubmitCustomerDecision(current, {...input, planRevision: current.plan.revision + 1}), false);
  const recorded = transitionWalkthrough(current, {type: 'record_customer_approval', ...input});
  assert.equal(recorded.error, null);
  assert.equal(canSubmitCustomerDecision(recorded, input), false);
});
