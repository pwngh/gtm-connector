import {renewalFixture} from './renewalFixtures.js';
import React from 'react';
import {CheckCheck,FileCheck2} from 'lucide-react';
import {DecisionAuthority} from './EvidenceNotes.jsx';
import {decisionContact} from './evidenceChecks.js';
import {formatWorklistTime} from './worklist.js';
import {derive} from './workflow.js';

// Shown only while the resolution's evidence still supports it, so it never contradicts another view.
export default function ResolutionReceipt({state,onDecision,onHistory}) {
  const decision=state.customerReview,doc=state.document,authority=state.evidence?.customerAuthority,contact=decisionContact(state);
  if(derive(state).requirementStatus!=='resolved'||!decision)return null;
  const ref=decision.evidenceRef;
  const citation=ref?.kind==='receipt'?`Receipt ${ref.id}, accepted signed letter`:ref?.kind==='unit'?`Card ${ref.cardId} · Unit ${ref.n}`:ref?`${ref.kind} ${ref.id||'not recorded'}`:'Not recorded';
  return <section className="resolution-receipt" aria-label="Recorded resolution">
    <div className="receipt-status"><span><CheckCheck size={20}/></span><div><span className="mini-label">REVIEWED & RECORDED</span><h3>Customer condition resolved.</h3></div></div>
    <dl><div><dt>Agreement</dt><dd>{renewalFixture.agreement.id} · {renewalFixture.account.name}</dd></div><div><dt>Document accepted at inspection</dt><dd><FileCheck2 size={15}/>{doc?.title||doc?.name} · {decision.documentVersion}</dd></div><div><dt>Approving contact</dt><dd>{contact?`${contact.displayName} · ${contact.role||'role not recorded'}`:'Not recorded'}</dd></div><DecisionAuthority state={state}/><div><dt>Cited evidence</dt><dd>{citation}</dd></div><div><dt>Authority basis</dt><dd>{authority?`Authority record ${authority.id}, revision ${authority.revision}, from receipt ${authority.evidenceRef?.id}`:'Not recorded'}</dd></div><div><dt>Recorded by</dt><dd>{decision.recordedBy}</dd></div><div><dt>Resolved</dt><dd>{formatWorklistTime(state.requirement.resolvedAt)}</dd></div></dl>
    <p className="receipt-boundary">Resolved from a decision citing the accepted signed letter, an authority record for that version and the agreement record’s version basis. The commercial renewal remains unconfirmed.</p>
    <div className="receipt-actions"><button className="button" onClick={onDecision}>Inspect approval</button><button className="text-button" onClick={onHistory}>Decision history</button></div>
  </section>;
}
