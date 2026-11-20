import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import React, {useEffect, useId, useRef, useState} from 'react';
import {FileText, CheckCircle2, Clock, ShieldCheck, XCircle} from 'lucide-react';
import {awaitedCounsel, awaitedCounselNote} from './renewalEvidence.js';
import {ReceiptFacts} from './CustomerEvidence.jsx';
import {decisionContact} from './evidenceChecks.js';

// File inspection and the per-document prescription remain distinct
// from the human decision that follows them.
// The upload's receipt, with the inspection that accepted or rejected it.
// The upload's own receipt with every inspection. A document saved before receipts existed shows what it had.
export function documentReceipt(document, state = null) {
  if (!document) return null;
  const recorded = state?.evidence?.artifactReceipts?.find(receipt => receipt.id === document.receiptId);
  if (recorded) return recorded;
  const inspection = document.status === 'verified' ? {status: 'accepted', at: document.verifiedAt, recordedBy: document.verifiedBy}
    : document.status === 'rejected' ? {status: 'rejected', at: document.rejectedAt, recordedBy: document.rejectedBy, reason: document.rejectionReason} : null;
  return {id: document.receiptId || 'Not recorded', uploadedBy: document.uploadedBy, receivedAt: document.receivedAt, checksum: document.checksum, inspections: inspection ? [inspection] : []};
}

export function DocumentHandoff({document, state = null, verified = false, onInspect}) {
  if (!document) return <article className="handoff-card" data-status="missing"><div className="handoff-header"><FileText size={18}/><strong>Document not received</strong></div><p>The task needs the applicable document before it can be inspected.</p></article>;
  const rejected = document.status === 'rejected', rider = renewalFixture.agreement.applicableRider;
  const applies = document.id === rider.documentId && document.version === rider.version;
  return <article className="handoff-card" data-status={verified ? 'verified' : rejected ? 'rejected' : 'received'}>
    <div className="handoff-header"><span className="mini-label">RECEIVED DOCUMENT</span><span className="handoff-state">{verified ? <CheckCircle2 size={14}/> : rejected ? <XCircle size={14}/> : <Clock size={14}/>} {verified ? 'Accepted at inspection' : rejected ? 'Rejected at inspection' : 'Needs inspection'}</span></div>
    <div className="handoff-file"><FileText size={24}/><div><strong>{document.title || document.name}</strong><span>Version {document.version} · {renewalFixture.agreement.label}</span></div></div>
    <div className="handoff-meta"><span>Source</span><p>{document.source}</p></div>
    {!applies && <p className="decision-boundary">{renewalFixture.agreement.label} names version {rider.version} (agreement record). Version {document.version} does not apply to it.</p>}
    <ReceiptFacts receipt={documentReceipt(document, state)} version={document.version}/>
    {onInspect && <div className="handoff-actions"><button type="button" className="button" onClick={onInspect}>Inspect document</button></div>}
  </article>;
}

function PlanScope({plan}) {
  return <div className="decision-scope">
    <div className="decision-specifications"><span className="mini-label">DOCUMENT SPECIFICATIONS</span><p>{plan.specifications}</p></div>
    <details className="decision-background"><summary>Task instructions</summary><p>{plan.instructions}</p></details>
  </div>;
}

export default function ReviewDecision({type, state, derived, onCancel, onPerform, onInspectDocument}) {
  const [requestingChanges, setRequestingChanges] = useState(false);
  const [reason, setReason] = useState('');
  const changesButton = useRef(null), wasRequestingChanges=useRef(false);
  useEffect(()=>{if(!requestingChanges&&wasRequestingChanges.current)changesButton.current?.focus();wasRequestingChanges.current=requestingChanges},[requestingChanges]);
  const reasonId = useId();
  const plan = state.plan;
  const doc = state.document;
  const decision = state.customerReview;
  const authority = state.evidence?.customerAuthority, rider = renewalFixture.agreement.applicableRider, contact = decisionContact(state);
  const configuration = {
    request: {action: 'request_review', enabled: derived.canRequestReview, label: 'Request review', message: `Review requested from ${fixturePeople.internalReviewer}`},
    review: {action: 'approve_plan', enabled: derived.canApprovePlan, label: state.task ? 'Approve and resume task' : 'Approve and create task', message: state.task ? 'Plan approved. Existing task resumed.' : 'Plan approved. Document task created.'},
    verify: {action: 'verify_document', enabled: derived.canVerifyDocument && Boolean(reason.trim()), label: 'Accept this version', message: 'Document version accepted at inspection'},
    resolve: {action: 'resolve_requirement', enabled: derived.canResolveRequirement, label: 'Record resolution', message: 'Condition resolved from the cited decision'},
    counselRequest: {action: 'request_customer_review', enabled: derived.canRequestCustomerReview, label: 'Record review request', message: 'Counsel review request recorded'},
  }[type];
  if (!configuration) return null;
  const inspect = onInspectDocument ? () => onInspectDocument(type) : undefined;
  const perform = () => onPerform(configuration.action, type === 'review' ? {approver: fixturePeople.internalReviewer} : type === 'verify' ? {verifiedBy: plan.owner, reason: reason.trim()} : {}, configuration.message);
  return <div className={`dialog-body review-decision decision-${type}`}>
    {['request', 'review'].includes(type) && <>
      <div className="decision-context"><span className="mini-label">PLAN VERSION {plan.revision}</span><span>{renewalFixture.agreement.label}</span></div>
      <h3 className="decision-title">{plan.title}</h3>
      <div className="decision-people"><div><span>Task owner</span><strong>{plan.owner}</strong></div><div><span>Internal reviewer</span><strong>{fixturePeople.internalReviewer}</strong></div></div>
      <PlanScope plan={plan}/>
      {!derived.isReviewerEligible && <p className="decision-boundary" role="status">{derived.planReviewBlockedReason}</p>}
      <p className="decision-boundary">{type === 'request' ? 'Submit this version for internal review. The document task remains on hold until approval.' : state.task ? 'Approval resumes the existing document task under this plan version. Customer counsel’s approval must be recorded separately.' : 'Approval creates the task to obtain and verify this document. Customer counsel’s approval must be recorded separately.'}</p>
    </>}
    {type === 'verify' && <>
      <div className="decision-context"><span className="mini-label">DOCUMENT CHECK</span><span>Plan version {plan.revision}</span></div>
      <h3 className="decision-title">Check the file against the requirements.</h3>
      <DocumentHandoff document={doc} state={state} verified={derived.isDocumentVerified} onInspect={inspect}/>
      <PlanScope plan={plan}/>
      <p className="decision-boundary">Recorded as checked by <strong>{plan.owner}</strong>. Accept the version only if it is the version {renewalFixture.agreement.label} names and it meets these specifications, or reject it. Either way, type the reason; it is kept with the receipt. A rejected version is kept. Customer approval is a separate decision.</p>
      {!derived.canVerifyDocument && derived.canRejectDocument && <p className="decision-boundary" role="status">{derived.transitions.verify_document.refusal}</p>}
      <label className="field" htmlFor={reasonId}>Inspection reason <span className="field-required">(required)</span><span className="field-hint">Kept with the receipt for either outcome. A rejected version and its receipt are kept, and acquisition reopens for a distinct replacement.</span></label>
      <textarea id={reasonId} className="review-reason" required maxLength={1200} rows={3} value={reason} onChange={event => setReason(event.target.value)}/>
    </>}
    {type === 'counselRequest' && <>
      <div className="decision-context"><span className="mini-label">CUSTOMER REVIEW HANDOFF</span><span>Plan version {plan.revision}</span></div>
      <h3 className="decision-title">Ready for customer counsel.</h3>
      <DocumentHandoff document={doc} state={state} verified={derived.isDocumentVerified} onInspect={inspect}/>
      <div className="decision-people"><div><span>For</span><strong>{derived.authorityContact ? derived.authorityContact.displayName : awaitedCounsel.label}</strong><small>{derived.authorityContact ? 'Recorded contact (authority record)' : awaitedCounselNote}</small></div><div><span>Recorded by</span><strong>{plan.owner}</strong><small>Document task owner</small></div></div>
      <p className="decision-boundary">This records an internal review request. No message is sent. The customer’s signed letter, its inspection and the authority record come next.</p>
    </>}
    {type === 'resolve' && <>
      <div className="decision-context"><span className="mini-label"><ShieldCheck size={14}/> CITED DECISION</span><span>{renewalFixture.agreement.label}</span></div>
      <h3 className="decision-title">Resolve the customer condition.</h3>
      <div className="decision-people"><div><span>Approving contact</span><strong>{contact?.displayName || 'Unidentified'}</strong><small>Authority record {decision?.authorityRef?.id || 'not recorded'}, revision {decision?.authorityRef?.revision || 'not recorded'}, from receipt {authority?.evidenceRef?.id || 'not recorded'}</small></div><div><span>Recorded by</span><strong>{decision?.recordedBy}</strong><small>Signed-in demo principal</small></div></div>
      <div className="decision-binding"><FileText size={18}/><div><strong>{doc?.title || doc?.name}</strong><p>Document version {decision?.documentVersion} · Cites receipt {decision?.evidenceRef?.id || 'not recorded'}</p><p>Version basis: {renewalFixture.agreement.label} names {rider.version} (agreement record)</p></div></div>
      <p className="decision-boundary">Resolution records that the cited decision, the authority record and the agreement record’s version basis support this condition. Separate work-item outputs and the commercial renewal are unchanged.</p>
    </>}
    {type === 'review' && requestingChanges ? <form className="review-changes" onSubmit={event => {event.preventDefault(); if (reason.trim()) onPerform('request_changes', {note: reason.trim()}, 'Changes requested');}}>
      <label className="field" htmlFor={reasonId}>Changes needed <span className="field-required">(required)</span><span className="field-hint">Tell the task owner what needs to change before this plan can be approved.</span></label>
      <textarea id={reasonId} className="review-reason" autoFocus required maxLength={1200} rows={4} value={reason} onChange={event => setReason(event.target.value)}/>
      <div className="dialog-actions"><button type="button" className="button ghost" onClick={() => setRequestingChanges(false)}>Keep reviewing</button><button type="submit" className="button primary" disabled={!reason.trim() || !derived.canRequestChanges}>Send back for changes</button></div>
    </form> : <div className="dialog-actions decision-actions">
      {type === 'review' ? <button ref={changesButton} type="button" className="button ghost" disabled={!derived.canRequestChanges} onClick={() => setRequestingChanges(true)}>Request changes</button>
        : <button type="button" className="button ghost" onClick={onCancel}>Cancel</button>}
      {type === 'verify' && <button type="button" className="button" disabled={!derived.canRejectDocument || !reason.trim()} onClick={() => onPerform('reject_document', {reason: reason.trim()}, 'Version rejected. Its receipt is kept.')}>Reject this version</button>}
      <button type="button" className="button primary" disabled={!configuration.enabled} onClick={perform}>{configuration.label}</button>
    </div>}
  </div>;
}
