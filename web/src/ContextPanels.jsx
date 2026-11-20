import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import React from 'react';
import {FileText, Link2, ShieldCheck, Info} from 'lucide-react';
import {DecisionAuthority, ReviewerInput} from './EvidenceNotes.jsx';
import {awaitedCounselNote} from './renewalEvidence.js';

export function RecordContext({state, onTask}) {
  return <div className="dialog-body context-body">
    <span className="context-overline"><Link2 size={16}/> THIS RENEWAL</span>
    <h3>One customer. One agreement.<br/>A specific decision.</h3>
    <div className="record-chain">
      <div><span>Customer organization</span><strong>{renewalFixture.account.name}</strong><p>The account this work belongs to.</p></div>
      <div><span>Renewal deal · Opportunity {renewalFixture.renewal.id}</span><strong>Annual renewal</strong><p>The commercial decision being tracked. Its outcome is still unconfirmed.</p></div>
      <div><span>Contract · {renewalFixture.agreement.label}</span><strong>{renewalFixture.agreement.displayTitle}</strong><p>The AI terms document must apply to this agreement and its current version.</p></div>
      <div><span>Customer condition</span><strong>Counsel approves the current AI terms</strong><p>The customer counsel is {awaitedCounselNote.toLowerCase()}. {fixturePeople.internalReviewer} reviews the internal work and the recorded evidence.</p></div>
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
  const decision=state.customerReview;
  if(decision?.status!=='approved')return <div className="dialog-body"><h3>No customer approval recorded.</h3><p>The current document still needs a supported decision from customer counsel.</p></div>;
  return <div className="dialog-body decision-evidence">
    <span className="context-overline"><ShieldCheck size={16}/> RECORDED REVIEWER INPUT</span>
    <h3>Walkthrough decision input.</h3>
    <ReviewerInput label="Decision text">{decision.evidenceText}</ReviewerInput>
    <dl className="detail-list"><div><dt>Customer reviewer (reviewer input)</dt><dd>{decision.reviewedBy}</dd></div><DecisionAuthority/><div><dt>Recorded by</dt><dd>{decision.recordedBy}</dd></div><div><dt>Document</dt><dd>{state.document.title} · {decision.documentVersion}</dd></div><div><dt>Applies to</dt><dd>{renewalFixture.agreement.label} · Plan version {decision.planRevision}</dd></div></dl>
    <div className="interpretation"><strong>What this establishes</strong><p>Typed reviewer input for this document version, not a cited receipt or an authority finding. The commercial renewal outcome remains unconfirmed.</p></div>
    <button className="button" onClick={onHistory}>View decision history</button>
  </div>;
}

export function ReportScope() {
  return <div className="dialog-body"><span className="context-overline"><Info size={16}/> COUNTING RULES</span><h3>Three reasons. One renewal.</h3><p>This report follows internal work approval, document preparation, and the customer condition for {renewalFixture.account.name}. They can share a prerequisite, so three open reasons do not mean three independent tasks.</p><dl className="report-definitions"><div><dt>Ready to act</dt><dd>The reason has a permitted next action with no earlier work blocking it.</dd></div><div><dt>Prerequisites open</dt><dd>An earlier plan or document step must be completed first. The reason links to that work.</dd></div><div><dt>Awaiting counsel</dt><dd>A current review request exists and customer counsel’s decision still needs to be recorded. The action owner and the person being awaited are shown separately.</dd></div><div><dt>Complete</dt><dd>The reason’s specific completion rule is met in the current state. A change can reopen it.</dd></div></dl><p className="small-note">Every reason belongs to exactly one state. Counts sum to three; filters select those same reasons. All records are invented and saved in this browser. No counts represent portfolio revenue, model accuracy, or a signed renewal.</p></div>;
}
