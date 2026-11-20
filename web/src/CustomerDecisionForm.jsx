import React from 'react';
import {fixturePeople, renewalFixture} from './renewalFixtures.js';
import {canSubmitCustomerDecision} from './customerDecision.js';
import './customerDecision.css';

export default function CustomerDecisionForm({state, input, onChange, onSubmit, onCancel}) {
  const request = state.customerReview;
  const enabled = canSubmitCustomerDecision(state, input);
  return <form className="dialog-body reviewer-decision-form" onSubmit={event => {
    event.preventDefault();
    if (enabled) onSubmit({reviewedBy: input.reviewedBy.trim(), evidenceText: input.evidenceText.trim()});
  }}>
    <p>Record explicit reviewer input for the active request. This simulated decision does not establish customer authority or a cited approval receipt.</p>
    <div className="interpretation"><strong>Document version {request?.documentVersion || 'not recorded'} · {renewalFixture.agreement.label}</strong><p>Recorded by {fixturePeople.internalReviewer} in this browser-only walkthrough.</p></div>
    <label className="field">Decision for version {request?.documentVersion || 'not recorded'} (reviewer input)
      <textarea name="evidenceText" required rows={4} value={input.evidenceText} onChange={event => onChange({...input, evidenceText: event.target.value})}/>
    </label>
    <label className="field">Customer reviewer (reviewer input, not authority)
      <input name="reviewedBy" type="text" required autoComplete="off" value={input.reviewedBy} onChange={event => onChange({...input, reviewedBy: event.target.value})}/>
    </label>
    <label className="decision-version-confirmation"><input name="confirmedVersion" type="checkbox" required checked={input.confirmedVersion} onChange={event => onChange({...input, confirmedVersion: event.target.checked})}/>
      <span>This input concerns document version {request?.documentVersion || 'not recorded'} in the active review request.</span>
    </label>
    <div className="dialog-actions"><button className="button ghost" type="button" onClick={onCancel}>Cancel</button><button className="button primary" type="submit" disabled={!enabled}>Record customer approval</button></div>
  </form>;
}
