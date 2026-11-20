import React, {useEffect, useId, useRef, useState} from 'react';
import {CheckCircle2, Clock, FileSignature, ShieldCheck, XCircle} from 'lucide-react';
import {approvalLetters, renewalFixture, walkthroughActors} from './renewalFixtures.js';
import {approvalLetterFor} from './workflow.js';
import {formatWorklistTime} from './worklist.js';
import {awaitedCounsel, awaitedCounselNote} from './renewalEvidence.js';

// The signed letter, its inspection and the authority record. A name appears only
// once a reviewer has recorded the signatory as a contact from this letter's receipt.

const inspectionLabel = inspection => !inspection ? 'Needs inspection' : inspection.status === 'accepted' ? 'Accepted at inspection' : 'Rejected at inspection';
const StateIcon = ({inspection}) => !inspection ? <Clock size={14}/> : inspection.status === 'accepted' ? <CheckCircle2 size={14}/> : <XCircle size={14}/>;

// Receipt facts in one place: version, uploader, received time, checksum, and the inspection.
export function ReceiptFacts({receipt, version}) {
  if (!receipt) return null;
  const inspection = receipt.inspection;
  return <div className="receipt-meta">
    <span>Receipt</span><p>{receipt.id}</p>
    {version && <><span>Version</span><p>{version}</p></>}
    <span>Uploaded by</span><p>{receipt.uploadedBy || 'Not recorded'}</p>
    <span>Received</span><p>{formatWorklistTime(receipt.receivedAt)}</p>
    <span>Checksum</span><p>{receipt.checksum || 'Not recorded'} (synthetic)</p>
    <span>Inspection</span><p>{inspection ? `${inspection.status === 'accepted' ? 'Accepted' : 'Rejected'} by ${inspection.recordedBy || 'an unrecorded reviewer'}, ${formatWorklistTime(inspection.at)}${inspection.reason ? `. ${inspection.reason}` : ''}` : 'Not yet inspected'}</p>
  </div>;
}

// The contact the authority record names, attributed to the letter. Null until it is recorded.
export function recordedSignatory(state, receipt) {
  const authority = state.evidence?.customerAuthority;
  if (!receipt || authority?.status !== 'accepted' || authority.evidenceRef?.id !== receipt.id) return null;
  return state.evidence.contacts?.find(contact => contact.id === authority.contactId) || null;
}

export function LetterHandoff({state, receipt, onInspect, inspectLabel = 'Inspect the letter'}) {
  if (!receipt) return <article className="handoff-card" data-status="missing"><div className="handoff-header"><FileSignature size={18}/><strong>Signed letter not received</strong></div><p>The customer’s signed decision on version {state.document?.version || 'not recorded'} has not been received. Nobody is recorded as the decision owner yet.</p></article>;
  const contact = recordedSignatory(state, receipt);
  return <article className="handoff-card" data-status={receipt.inspection?.status === 'accepted' ? 'verified' : receipt.inspection?.status === 'rejected' ? 'rejected' : 'received'}>
    <div className="handoff-header"><span className="mini-label">SIGNED APPROVAL LETTER · WALKTHROUGH DATA</span><span className="handoff-state"><StateIcon inspection={receipt.inspection}/> {inspectionLabel(receipt.inspection)}</span></div>
    <div className="handoff-file"><FileSignature size={24}/><div><strong>{receipt.title}</strong><span>Approves version {receipt.approves?.version} · {renewalFixture.agreement.label}</span></div></div>
    <ReceiptFacts receipt={receipt}/>
    <p className="letter-signatory">{contact ? <>Signed by <strong>{contact.displayName}</strong>, {contact.role.toLowerCase()}. Recorded as a contact from this letter’s receipt.</> : 'The signatory is not shown until a reviewer records who decides from this receipt.'}</p>
    {onInspect && <div className="handoff-actions"><button type="button" className="button" onClick={onInspect}>{inspectLabel}</button></div>}
  </article>;
}

// The receipts the walkthrough keeps for this version, latest first, rejected ones included.
function letterReceipts(state) {
  const doc = state.document;
  return (state.evidence?.artifactReceipts || []).filter(receipt => receipt.kind === 'approval_letter' && receipt.approves?.documentId === doc?.id && receipt.approves?.version === doc?.version);
}

export function LetterUpload({state, derived: d, onCancel, onPerform}) {
  const letter = approvalLetterFor(state.document);
  return <div className="dialog-body review-decision decision-letter">
    <div className="decision-context"><span className="mini-label">CUSTOMER REVIEW · WALKTHROUGH DATA</span><span>Version {state.document?.version || 'not recorded'}</span></div>
    <h3 className="decision-title">Add the customer’s signed letter.</h3>
    {letter ? <div className="decision-binding"><FileSignature size={18}/><div><strong>{letter.name}</strong><p>{letter.source} · {renewalFixture.agreement.label} · Synthetic checksum {letter.checksum}</p></div></div>
      : <p className="decision-boundary" role="status">{d.transitions.upload_approval_letter.refusal}</p>}
    <p className="decision-boundary">The upload records a receipt with its received time, uploader and checksum. Earlier receipts are kept. Inspection comes next, and nobody is named until a reviewer records who decides.</p>
    <div className="decision-people"><div><span>Uploaded by</span><strong>{state.plan.owner}</strong><small>Customer review request owner</small></div><div><span>Awaited</span><strong>{d.authorityContact ? d.authorityContact.displayName : awaitedCounsel.label}</strong><small>{d.authorityContact ? 'Recorded contact (authority record)' : awaitedCounselNote}</small></div></div>
    <div className="dialog-actions decision-actions"><button type="button" className="button ghost" onClick={onCancel}>Cancel</button><button type="button" className="button primary" disabled={!d.canUploadApprovalLetter} onClick={() => onPerform('upload_approval_letter', {}, 'Signed letter received. Inspection is next.')}>Record the letter’s receipt</button></div>
  </div>;
}

export function LetterInspection({state, derived: d, onCancel, onPerform}) {
  const receipt = d.approvalLetter, [rejecting, setRejecting] = useState(false), [reason, setReason] = useState('');
  const reasonId = useId(), rejectButton = useRef(null), wasRejecting = useRef(false);
  // Leaving the reason form returns focus to the control that opened it.
  useEffect(() => {if (!rejecting && wasRejecting.current) rejectButton.current?.focus(); wasRejecting.current = rejecting;}, [rejecting]);
  return <div className="dialog-body review-decision decision-letter-inspection">
    <div className="decision-context"><span className="mini-label">LETTER INSPECTION</span><span>{renewalFixture.agreement.label}</span></div>
    <h3 className="decision-title">Check the letter before anyone relies on it.</h3>
    <LetterHandoff state={state} receipt={receipt}/>
    <p className="decision-boundary">Accept only a signed, legible letter that names this agreement and version {receipt?.approves?.version || 'not recorded'}. Acceptance records the version it names as the version basis, separate from the document file. Inspected by <strong>{walkthroughActors.reviewer}</strong>.</p>
    {rejecting ? <form className="review-changes" onSubmit={event => {event.preventDefault(); if (reason.trim()) onPerform('inspect_approval_letter', {outcome: 'rejected', reason: reason.trim()}, 'Letter rejected. Its receipt is kept.');}}>
      <label className="field" htmlFor={reasonId}>Reason for rejecting <span className="field-required">(required)</span><span className="field-hint">The receipt is kept with this reason. A new letter would be needed.</span></label>
      <textarea id={reasonId} className="review-reason" autoFocus required maxLength={1200} rows={3} value={reason} onChange={event => setReason(event.target.value)}/>
      <div className="dialog-actions"><button type="button" className="button ghost" onClick={() => setRejecting(false)}>Keep inspecting</button><button type="submit" className="button primary" disabled={!reason.trim() || !d.canInspectApprovalLetter}>Reject the letter</button></div>
    </form> : <div className="dialog-actions decision-actions">
      <button ref={rejectButton} type="button" className="button ghost" disabled={!d.canInspectApprovalLetter} onClick={() => setRejecting(true)}>Reject with a reason</button>
      <button type="button" className="button primary" disabled={!d.canInspectApprovalLetter} onClick={() => onPerform('inspect_approval_letter', {outcome: 'accepted'}, 'Letter accepted at inspection')}>Accept the letter</button>
    </div>}
  </div>;
}

export function AuthorityRecord({state, derived: d, onCancel, onPerform}) {
  const receipt = d.approvalLetter, letter = approvalLetters.find(item => item.artifactId === receipt?.artifactId);
  return <div className="dialog-body review-decision decision-authority">
    <div className="decision-context"><span className="mini-label"><ShieldCheck size={14}/> AUTHORITY RECORD</span><span>{renewalFixture.agreement.label}</span></div>
    <h3 className="decision-title">Record who decides, from the letter.</h3>
    <LetterHandoff state={state} receipt={receipt}/>
    <div className="decision-people"><div><span>Contact to record</span><strong>The letter’s signatory</strong><small>{letter ? `${letter.signatory.role}, as signed` : 'Not recorded'}</small></div><div><span>Authority basis</span><strong>Receipt {receipt?.id || 'not recorded'}</strong><small>Inspected signed letter</small></div></div>
    <p className="decision-boundary">The contact is recorded with this receipt as the basis of their authority for {renewalFixture.agreement.label}, and recorded by <strong>{walkthroughActors.reviewer}</strong>. The contact’s name comes from the letter. A role or a typed name is never an authority basis.</p>
    <div className="dialog-actions decision-actions"><button type="button" className="button ghost" onClick={onCancel}>Cancel</button><button type="button" className="button primary" disabled={!d.canRecordCustomerAuthority} onClick={() => onPerform('record_customer_authority', {}, 'Authority recorded from the signed letter')}>Record the contact</button></div>
  </div>;
}

export function LetterDetail({state, derived: d}) {
  const receipts = letterReceipts(state);
  if (!receipts.length) return <div className="dialog-body"><h3>No signed letter is recorded.</h3><p>The customer’s signed decision has not been received for version {state.document?.version || 'not recorded'}.</p></div>;
  return <div className="dialog-body">
    <p>The letter is walkthrough data. Its receipt and inspection are recorded here; the general counsel is named only by the authority record.</p>
    {receipts.slice().reverse().map(receipt => <LetterHandoff key={receipt.id} state={state} receipt={receipt}/>)}
    <dl className="detail-list"><div><dt>Version basis</dt><dd>{state.evidence?.documentVersionBasis ? `Version ${state.evidence.documentVersionBasis.version} for ${renewalFixture.agreement.label}, from receipt ${state.evidence.documentVersionBasis.source?.id}` : 'Not recorded'}</dd></div>
      <div><dt>Customer decision authority</dt><dd>{d.authorityContact ? `${d.authorityContact.displayName} · authority record` : 'Unidentified'}</dd></div></dl>
  </div>;
}
