import {renewalFixture} from './renewalFixtures.js';
import React from 'react';
import {CheckCheck,FileCheck2} from 'lucide-react';

export default function ResolutionReceipt({state,onDecision,onHistory}) {
  const decision=state.customerReview,doc=state.document;
  if(state.requirement.status!=='resolved'||!decision)return null;
  return <section className="resolution-receipt" aria-label="Recorded resolution">
    <div className="receipt-status"><span><CheckCheck size={20}/></span><div><span className="mini-label">REVIEWED & RECORDED</span><h3>Customer condition resolved.</h3></div></div>
    <dl><div><dt>Agreement</dt><dd>{renewalFixture.agreement.id} · {renewalFixture.account.name}</dd></div><div><dt>Verified document</dt><dd><FileCheck2 size={15}/>{doc?.title||doc?.name} · {decision.documentVersion}</dd></div><div><dt>Customer counsel</dt><dd>{decision.reviewedBy}</dd></div><div><dt>Recorded by</dt><dd>{decision.recordedBy}</dd></div><div><dt>Resolved</dt><dd>{new Date(state.requirement.resolvedAt).toLocaleString([],{month:'short',day:'numeric',year:'numeric',hour:'2-digit',minute:'2-digit'})}</dd></div></dl>
    <p className="receipt-boundary">The commercial renewal remains unconfirmed.</p>
    <div className="receipt-actions"><button className="button" onClick={onDecision}>Inspect approval</button><button className="text-button" onClick={onHistory}>Decision history</button></div>
  </section>;
}
