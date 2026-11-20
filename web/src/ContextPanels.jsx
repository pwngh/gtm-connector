import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import React from 'react';
import {FileText, Link2, ShieldCheck, Info} from 'lucide-react';
import {DecisionAuthority} from './EvidenceNotes.jsx';
import {awaitedCounselNote} from './renewalEvidence.js';
import {derive} from './workflow.js';
import {decisionContact} from './evidenceChecks.js';

export function RecordContext({state, onTask}) {
  const contact = derive(state).authorityContact;
  return <div className="dialog-body context-body">
    <span className="context-overline"><Link2 size={16}/> THIS RENEWAL</span>
    <h3>One customer. One agreement.<br/>A specific decision.</h3>
    <div className="record-chain">
      <div><span>Customer organization</span><strong>{renewalFixture.account.name}</strong><p>The account this work belongs to.</p></div>
      <div><span>Renewal deal · Opportunity {renewalFixture.renewal.id}</span><strong>Annual renewal</strong><p>The commercial decision being tracked. Its outcome is still unconfirmed.</p></div>
      <div><span>Contract · {renewalFixture.agreement.label}</span><strong>{renewalFixture.agreement.displayTitle}</strong><p>The AI terms document must apply to this agreement and its current version.</p></div>
      <div><span>Customer condition</span><strong>Counsel approves the current AI terms</strong><p>{contact ? `${contact.displayName}, ${(contact.role || 'customer contact').toLowerCase()}, decides for ${renewalFixture.agreement.label}. The authority record cites the inspected signed letter.` : `The customer counsel is ${awaitedCounselNote.toLowerCase()}.`} {fixturePeople.internalReviewer} reviews the internal work and the recorded evidence.</p></div>
    </div>
    <button className="button primary" onClick={()=>onTask(3)}>View the review task</button>
    <p className="small-note">Invented relationships for this walkthrough. No CRM is connected.</p>
  </div>;
}

export function PlanVersionHelp({state}) {
  return <div className="dialog-body">
    <span className="context-overline"><FileText size={16}/> PLAN VERSION {state.plan.revision}</span>
    <h3>A saved version of this renewal’s work.</h3>
    <p>Changing the document task, owner, specifications, or instructions creates a new version. {fixturePeople.internalReviewer} must review that version before the work can proceed.</p>
    <div className="definition-pair"><div><strong>Work plan</strong><p>The work tailored to this renewal. You can edit it here.</p></div><div><strong>Playbook</strong><p>A reusable starting point. Editing this work plan leaves the playbook unchanged.</p></div></div>
    <p className="small-note">Plan version {state.plan.revision} identifies the work instructions. {state.document?`Document version ${state.document.version} identifies the received file.`:'A document version has not been recorded yet.'} Internal plan approval is separate from customer counsel’s approval of the document.</p>
  </div>;
}

export function DecisionEvidence({state, onHistory}) {
  const decision=state.customerReview, authority=state.evidence?.customerAuthority, rider=renewalFixture.agreement.applicableRider, contact=decisionContact(state);
  const retained=(decision?.priorDecisions||[]).filter(item=>item.legacy);
  const hasDecision=decision&&(decision.status==='approved'||decision.status==='needs_re_review'&&decision.evidenceRef);
  if(!hasDecision)return <div className="dialog-body"><h3>No current customer decision.</h3><p>The current document still needs a supported decision from customer counsel.</p>{retained.map((item,index)=><div className="interpretation" key={index}><strong>Retained record from earlier rules · not a decision</strong><p>Saved under {item.policyVersion||'an earlier policy without a recorded version'} as typed text, with no cited letter or authority record. It is kept for History and does not count.</p></div>)}{decision?.reReview&&<p className="decision-boundary">{decision.reReview.reason}</p>}</div>;
  const citation=decision.evidenceRef?.kind==='receipt'?`Receipt ${decision.evidenceRef.id}, accepted signed letter`:decision.evidenceRef?.kind==='unit'?`Card ${decision.evidenceRef.cardId} · Unit ${decision.evidenceRef.n}`:'Not recorded';
  return <div className="dialog-body decision-evidence">
    <span className="context-overline"><ShieldCheck size={16}/> {decision.status==='approved'?'RECORDED CUSTOMER DECISION':'CUSTOMER DECISION · NEEDS RE-REVIEW'}</span>
    <h3>A decision bound to its evidence.</h3>
    {decision.reReview&&<p className="decision-boundary">Not current. {decision.reReview.reason} Marked {decision.reReview.actor?`by ${decision.reReview.actor}`:'by the walkthrough rules'}.</p>}
    <dl className="detail-list"><div><dt>Cited evidence</dt><dd>{citation}</dd></div><div><dt>Approving contact</dt><dd>{contact?`${contact.displayName} · ${contact.role||'role not recorded'}`:decision.contactId?`${decision.contactId} · not a recorded contact`:'Not recorded'}</dd></div><DecisionAuthority state={state}/><div><dt>Authority record relied on</dt><dd>{decision.authorityRef?`${decision.authorityRef.id}, revision ${decision.authorityRef.revision}${authority?.id===decision.authorityRef.id&&authority.revision===decision.authorityRef.revision?'':` (current: revision ${authority?.revision||'none'}, ${authority?.status||'not recorded'})`}`:'Not recorded'}</dd></div><div><dt>Authority basis</dt><dd>{authority?`Receipt ${authority.evidenceRef?.id}, covering ${authority.documentId||'no recorded document'} ${authority.version||''}`:'Not recorded'}</dd></div><div><dt>Version basis</dt><dd>{renewalFixture.agreement.label} names {rider.documentId} {rider.version} (agreement record)</dd></div><div><dt>Recorded by</dt><dd>{decision.recordedBy}</dd></div><div><dt>Document</dt><dd>{state.document?.title||'Not recorded'} · {decision.documentVersion}</dd></div><div><dt>Applies to</dt><dd>{renewalFixture.agreement.label}</dd></div></dl>
    <div className="interpretation"><strong>What this establishes</strong><p>The customer’s decision on this document version, cited to the accepted signed letter and the contact recorded from it. It is not tied to our plan’s revision. The commercial renewal outcome remains unconfirmed.</p></div>
    <button className="button" onClick={onHistory}>View decision history</button>
  </div>;
}

export function ReportScope() {
  return <div className="dialog-body"><span className="context-overline"><Info size={16}/> COUNTING RULES</span><h3>Three reasons. One renewal.</h3><p>This report follows internal work approval, document preparation, and the customer condition for {renewalFixture.account.name}. They can share a prerequisite, so three open reasons do not mean three independent tasks.</p><dl className="report-definitions"><div><dt>Ready to act</dt><dd>The reason has a permitted next action with no earlier work blocking it.</dd></div><div><dt>Prerequisites open</dt><dd>An earlier plan or document step must be completed first. The reason links to that work.</dd></div><div><dt>Awaiting counsel</dt><dd>A current review request exists and the customer’s signed letter has not been received. The action owner and the party being awaited are shown separately; the party is a role until an authority record names a contact.</dd></div><div><dt>Complete</dt><dd>The reason’s specific completion rule is met in the current state. A change can reopen it.</dd></div></dl><p className="small-note">Every reason belongs to exactly one state. Counts sum to three; filters select those same reasons. All records are invented and saved in this browser. No counts represent portfolio revenue, model accuracy, or a signed renewal.</p></div>;
}
