import React from 'react';
import {renewalFixture, walkthroughActors} from './renewalFixtures.js';
import {canSubmitCustomerDecision} from './customerDecision.js';
import {derive} from './workflow.js';
import {ReceiptFacts} from './CustomerEvidence.jsx';
import './customerDecision.css';

// The decision cites an inspected receipt and names the contact from the authority record.
// There is no field for approval text, and the recorder is the signed-in demo principal.
export default function CustomerDecisionForm({state, input, onChange, onSubmit, onCancel}) {
  const request = state.customerReview, d = derive(state);
  const letter = d.approvalLetter?.inspection?.status === 'accepted' ? d.approvalLetter : null;
  const contact = d.authorityContact;
  const enabled = canSubmitCustomerDecision(state, input);
  return <form className="dialog-body reviewer-decision-form" onSubmit={event => {
    event.preventDefault();
    if (enabled) onSubmit({evidenceRef: input.evidenceRef, contactId: input.contactId, documentVersion: input.documentVersion});
  }}>
    <p>Record the customer’s decision from the evidence already inspected. Typed approval text is not accepted as a decision.</p>
    <div className="interpretation"><strong>Document version {request?.documentVersion || 'not recorded'} · {renewalFixture.agreement.label}</strong><p>Recorded by {walkthroughActors.reviewer}, the signed-in demo principal. The approver never records their own decision.</p></div>
    <fieldset className="decision-citation">
      <legend>Cited evidence</legend>
      {letter ? <label className="decision-version-confirmation"><input name="evidenceRef" type="radio" required checked={input.evidenceRef?.id === letter.id} onChange={() => onChange({...input, evidenceRef: {kind: 'receipt', id: letter.id}})}/>
        <span>{letter.title} · receipt {letter.id}, accepted at inspection</span></label>
        : <p>No inspected receipt records a decision on this version.</p>}
      {letter && <ReceiptFacts receipt={letter}/>}
    </fieldset>
    <dl className="detail-list decision-contact">
      <div><dt>Approving contact</dt><dd>{contact ? `${contact.displayName} · ${contact.role || 'role not recorded'}` : 'Unidentified'}</dd></div>
      <div><dt>Authority basis</dt><dd>{contact ? `Authority record from receipt ${state.evidence?.customerAuthority?.evidenceRef?.id}` : 'Not recorded'}</dd></div>
    </dl>
    <label className="decision-version-confirmation"><input name="confirmedVersion" type="checkbox" required checked={input.confirmedVersion} onChange={event => onChange({...input, confirmedVersion: event.target.checked})}/>
      <span>This decision is bound to document version {request?.documentVersion || 'not recorded'} in the active review request.</span>
    </label>
    <div className="dialog-actions"><button className="button ghost" type="button" onClick={onCancel}>Cancel</button><button className="button primary" type="submit" disabled={!enabled}>Record the customer decision</button></div>
  </form>;
}
