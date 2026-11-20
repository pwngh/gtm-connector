import React, {useId, useState} from 'react';
import {CheckCircle2, Clock, FileSignature, ShieldCheck, XCircle} from 'lucide-react';
import {renewalFixture, walkthroughActors} from './renewalFixtures.js';
import {approvalLetterFor, letterArtifact} from './workflow.js';
import {latestInspection, receiptInspections} from './evidenceChecks.js';
import {formatWorklistTime} from './worklist.js';
import {awaitedCounsel, awaitedCounselNote} from './renewalEvidence.js';

// The signed letter, its inspection and the authority record. A name appears as a contact only
// once a reviewer has recorded the signatory from this letter's receipt. The inspection and
// authority dialogs show the letter's own text, which names its signatory, labelled as letter text.

const inspectionLabel = inspection => !inspection ? 'Needs inspection' : inspection.status === 'accepted' ? 'Accepted at inspection' : 'Rejected at inspection';
const StateIcon = ({inspection}) => !inspection ? <Clock size={14}/> : inspection.status === 'accepted' ? <CheckCircle2 size={14}/> : <XCircle size={14}/>;
const inspectionText = inspection => `${inspection.status === 'accepted' ? 'Accepted' : 'Rejected'} by ${inspection.recordedBy || 'an unrecorded reviewer'}, ${formatWorklistTime(inspection.at)}. Reason: ${inspection.reason || 'not recorded'}`;

// Receipt facts in one place: version, uploader, received time, checksum, and every inspection in order.
export function ReceiptFacts({receipt, version}) {
  if (!receipt) return null;
  const inspections = receiptInspections(receipt);
  return <dl className="receipt-meta">
    <div><dt>Receipt</dt><dd>{receipt.id}</dd></div>
    {version && <div><dt>Version</dt><dd>{version}</dd></div>}
    <div><dt>Uploaded by</dt><dd>{receipt.uploadedBy || 'Not recorded'}</dd></div>
    <div><dt>Received</dt><dd>{formatWorklistTime(receipt.receivedAt)}</dd></div>
    <div><dt>Checksum</dt><dd>{receipt.checksum ? `${receipt.checksum} (synthetic)` : 'Not recorded'}</dd></div>
    <div><dt>{inspections.length > 1 ? 'Inspections' : 'Inspection'}</dt><dd>{!inspections.length ? 'Not yet inspected'
      : inspections.length === 1 ? inspectionText(inspections[0])
        : <ol className="receipt-inspections">{inspections.map((inspection, index) => <li key={index}>{inspectionText(inspection)}</li>)}</ol>}</dd></div>
  </dl>;
}

// The contact the authority record names from this receipt. Null until it is recorded.
export function recordedSignatory(state, receipt) {
  const authority = state.evidence?.customerAuthority;
  if (!receipt || authority?.status !== 'accepted' || authority.evidenceRef?.id !== receipt.id) return null;
  return state.evidence.contacts?.find(contact => contact.id === authority.contactId) || null;
}

// The letter's own words, as a synthetic excerpt. It is evidence, so it may name its signatory.
export function LetterText({receipt}) {
  const letter = letterArtifact(receipt), id = useId();
  if (!letter || receipt.checksum !== letter.checksum) return <p className="decision-boundary" role="status">This walkthrough has no text for receipt {receipt?.id || 'not recorded'}, so it cannot be read here.</p>;
  return <section className="letter-text" tabIndex={0} aria-labelledby={id}>
    <span className="mini-label" id={id}>LETTER TEXT · SYNTHETIC EXCERPT</span>
    {letter.excerpt.map(line => <p key={line}>{line}</p>)}
    <p className="letter-text-note">{letter.provenance}</p>
  </section>;
}

export function LetterHandoff({state, receipt, onInspect, inspectLabel = 'Inspect the letter'}) {
  if (!receipt) return <article className="handoff-card" data-status="missing"><div className="handoff-header"><FileSignature size={18}/><strong>Signed letter not received</strong></div><p>The customer’s signed decision on version {state.document?.version || 'not recorded'} has not been received. Nobody is recorded as the decision owner yet.</p></article>;
  const contact = recordedSignatory(state, receipt), inspection = latestInspection(receipt);
  return <article className="handoff-card" data-status={inspection?.status === 'accepted' ? 'verified' : inspection?.status === 'rejected' ? 'rejected' : 'received'}>
    <div className="handoff-header"><span className="mini-label">SIGNED APPROVAL LETTER · WALKTHROUGH DATA</span><span className="handoff-state"><StateIcon inspection={inspection}/> {inspectionLabel(inspection)}</span></div>
    <div className="handoff-file"><FileSignature size={24}/><div><strong>{receipt.title}</strong><span>Approves version {receipt.approves?.version} · {renewalFixture.agreement.label}</span></div></div>
    <ReceiptFacts receipt={receipt}/>
    <p className="letter-signatory">{contact ? <>Signed by <strong>{contact.displayName}</strong>, {contact.role.toLowerCase()}. Recorded as a contact from this letter’s receipt.</> : 'No contact is recorded from this receipt yet.'}</p>
    {onInspect && <div className="handoff-actions"><button type="button" className="button" onClick={onInspect}>{inspectLabel}</button></div>}
  </article>;
}

// Every letter receipt the walkthrough keeps, latest first, rejected ones included.
function letterReceipts(state) {
  return (state.evidence?.artifactReceipts || []).filter(receipt => receipt.kind === 'approval_letter');
}

export function LetterUpload({state, derived: d, onCancel, onPerform}) {
  const letter = approvalLetterFor(state);
  return <div className="dialog-body review-decision decision-letter">
    <div className="decision-context"><span className="mini-label">CUSTOMER REVIEW · WALKTHROUGH DATA</span><span>Version {state.document?.version || 'not recorded'}</span></div>
    <h3 className="decision-title">Add the customer’s signed letter.</h3>
    {letter ? <div className="decision-binding"><FileSignature size={18}/><div><strong>{letter.name}</strong><p>{letter.source} · {renewalFixture.agreement.label} · Synthetic checksum {letter.checksum}</p></div></div>
      : <p className="decision-boundary" role="status">{d.transitions.upload_approval_letter.refusal}</p>}
    <p className="decision-boundary">The upload records a receipt with its received time, uploader and checksum. Earlier receipts are kept, and a file matching a rejected receipt is refused. Inspection comes next, by someone other than the uploader.</p>
    <div className="decision-people"><div><span>Uploaded by</span><strong>{state.plan.owner}</strong><small>Customer review request owner</small></div><div><span>Awaited</span><strong>{d.authorityContact ? d.authorityContact.displayName : awaitedCounsel.label}</strong><small>{d.authorityContact ? 'Recorded contact (authority record)' : awaitedCounselNote}</small></div></div>
    <div className="dialog-actions decision-actions"><button type="button" className="button ghost" onClick={onCancel}>Cancel</button><button type="button" className="button primary" disabled={!d.canUploadApprovalLetter} onClick={() => onPerform('upload_approval_letter', {}, 'Signed letter received. Inspection is next.')}>Record the letter’s receipt</button></div>
  </div>;
}

// One typed reason for either outcome. Nothing is filled in for the reviewer.
export function LetterInspection({state, derived: d, onCancel, onPerform}) {
  const receipt = d.approvalLetter, [reason, setReason] = useState(''), reasonId = useId(), hintId = useId();
  const ready = Boolean(reason.trim()) && d.canInspectApprovalLetter;
  const inspect = outcome => {if (ready) onPerform('inspect_approval_letter', {outcome, reason: reason.trim()}, outcome === 'accepted' ? 'Letter accepted at inspection' : 'Letter rejected. Its receipt is kept.');};
  return <form className="dialog-body review-decision decision-letter-inspection" onSubmit={event => {event.preventDefault(); inspect('accepted');}}>
    <div className="decision-context"><span className="mini-label">LETTER INSPECTION</span><span>{renewalFixture.agreement.label}</span></div>
    <h3 className="decision-title">Check the letter before anyone relies on it.</h3>
    <LetterHandoff state={state} receipt={receipt}/>
    {receipt && <LetterText receipt={receipt}/>}
    <p className="decision-boundary">Accept only a signed, legible letter that names this agreement and version {receipt?.approves?.version || 'not recorded'}. Which version applies comes from the agreement record, not from this letter. Inspected by <strong>{walkthroughActors.reviewer}</strong>, who did not upload it and does not own the plan.</p>
    {!d.canInspectApprovalLetter && <p className="decision-boundary" role="status">{d.transitions.inspect_approval_letter.refusal}</p>}
    <label className="field" htmlFor={reasonId}>Inspection reason <span className="field-required">(required)</span><span className="field-hint" id={hintId}>Kept with the receipt for either outcome. A rejected letter is kept, and a distinct letter is needed.</span></label>
    <textarea id={reasonId} className="review-reason" required maxLength={1200} rows={3} aria-describedby={hintId} value={reason} onChange={event => setReason(event.target.value)}/>
    <div className="dialog-actions decision-actions">
      <button type="button" className="button ghost" onClick={onCancel}>Cancel</button>
      <button type="button" className="button" disabled={!ready} onClick={() => inspect('rejected')}>Reject the letter</button>
      <button type="submit" className="button primary" disabled={!ready}>Accept the letter</button>
    </div>
  </form>;
}

export function AuthorityRecord({state, derived: d, onCancel, onPerform}) {
  const receipt = d.approvalLetter, letter = letterArtifact(receipt);
  return <div className="dialog-body review-decision decision-authority">
    <div className="decision-context"><span className="mini-label"><ShieldCheck size={14}/> AUTHORITY RECORD</span><span>{renewalFixture.agreement.label}</span></div>
    <h3 className="decision-title">Record who decides, from the letter.</h3>
    <LetterHandoff state={state} receipt={receipt}/>
    {receipt && <LetterText receipt={receipt}/>}
    <div className="decision-people"><div><span>Contact to record</span><strong>The letter’s signatory</strong><small>{letter ? `${letter.signatory.role}, as signed in the letter text` : 'Not recorded'}</small></div><div><span>Authority basis</span><strong>Receipt {receipt?.id || 'not recorded'}</strong><small>{receipt ? `Accepted signed letter · ${receipt.approves?.documentId} ${receipt.approves?.version}` : 'Not recorded'}</small></div></div>
    <p className="decision-boundary">The contact is recorded with this receipt as the basis of their authority for {renewalFixture.agreement.label}, version {receipt?.approves?.version || 'not recorded'} only, and recorded by <strong>{walkthroughActors.reviewer}</strong>. A new version needs a letter that covers it. A role or a typed name is never an authority basis.</p>
    {!d.canRecordCustomerAuthority && <p className="decision-boundary" role="status">{d.transitions.record_customer_authority.refusal}</p>}
    <div className="dialog-actions decision-actions"><button type="button" className="button ghost" onClick={onCancel}>Cancel</button><button type="button" className="button primary" disabled={!d.canRecordCustomerAuthority} onClick={() => onPerform('record_customer_authority', {}, 'Authority recorded from the signed letter')}>Record the contact</button></div>
  </div>;
}

export function LetterDetail({state, derived: d}) {
  const receipts = letterReceipts(state);
  if (!receipts.length) return <div className="dialog-body"><h3>No signed letter is recorded.</h3><p>The customer’s signed decision has not been received for version {state.document?.version || 'not recorded'}.</p></div>;
  const rider = renewalFixture.agreement.applicableRider, authority = state.evidence?.customerAuthority;
  return <div className="dialog-body">
    <p>The letters are walkthrough data. Every receipt is kept here with each of its inspections, rejected ones included. The general counsel is named as a contact only by the authority record.</p>
    {receipts.slice().reverse().map(receipt => <LetterHandoff key={receipt.id} state={state} receipt={receipt}/>)}
    <dl className="detail-list"><div><dt>Version basis</dt><dd>{`${renewalFixture.agreement.label} names ${rider.documentId} ${rider.version} (agreement record, not a letter)`}</dd></div>
      <div><dt>Authority record</dt><dd>{authority ? `${authority.id} · revision ${authority.revision} · ${authority.status} · covers ${authority.documentId || 'no recorded document'} ${authority.version || ''}` : 'Not recorded'}</dd></div>
      <div><dt>Customer decision authority</dt><dd>{d.authorityContact ? `${d.authorityContact.displayName} · authority record` : 'Unidentified'}</dd></div></dl>
  </div>;
}
