import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {canSubmitCustomerDecision, createCustomerDecisionInput, customerDecisionSteps} from './customerDecision.js';
import {walkthroughInputs} from './testing/walkthroughInputs.js';

function requested(document) {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) {
    state = transitionWalkthrough(state, {type, ...walkthroughInputs(state, type), ...(type === 'obtain_document' ? {document} : {})});
  }
  return state;
}
const authorized = (state = requested()) => customerDecisionSteps.slice(0, 3).reduce((current, type) => transitionWalkthrough(current, {type, ...walkthroughInputs(current, type)}), state);

test('decision input is bound to the active request and starts unconfirmed, with no evidence before the letter is accepted', () => {
  // v5 can no longer be requested: the agreement record names v4. The request for v4 is bound the same way.
  const state = requested(), input = createCustomerDecisionInput(state);
  assert.equal(input.documentVersion, 'v4');
  assert.equal(input.documentId, state.customerReview.documentId);
  assert.equal(input.requestedAt, state.customerReview.requestedAt);
  assert.equal(input.evidenceRef, null);
  assert.equal(input.contactId, null);
  assert.equal(input.confirmedVersion, false);
  assert.equal('evidenceText' in input, false);
  assert.equal(canSubmitCustomerDecision(state, input), false);
});

test('the cited receipt, the recorded contact and version confirmation are required, and stale requests cannot be submitted', () => {
  const current = authorized(), input = {...createCustomerDecisionInput(current), confirmedVersion: true};
  assert.equal(input.evidenceRef.kind, 'receipt');
  assert.equal(input.contactId, current.evidence.customerAuthority.contactId);
  assert.equal(canSubmitCustomerDecision(current, input), true);
  for (const invalid of [{evidenceRef: null}, {contactId: null}, {confirmedVersion: false}]) {
    assert.equal(canSubmitCustomerDecision(current, {...input, ...invalid}), false);
  }
  assert.equal(canSubmitCustomerDecision(initialState, input), false);
  assert.equal(canSubmitCustomerDecision(requested(), input), false);
  assert.equal(canSubmitCustomerDecision(current, {...input, requestedAt: null}), false);
  assert.equal(canSubmitCustomerDecision(current, {...input, planRevision: current.plan.revision + 1}), false);
  const recorded = transitionWalkthrough(current, {type: 'record_customer_approval', ...walkthroughInputs(current, 'record_customer_approval')});
  assert.equal(recorded.error, null);
  assert.equal(canSubmitCustomerDecision(recorded, input), false);
});
