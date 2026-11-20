import {renewalFixture,fixturePeople,renewalEvidence} from './renewalFixtures.js';
import React, {useId, useState} from 'react';
import {motion, useReducedMotion} from 'motion/react';
import {Phone, FileText, MessageSquare, ShieldCheck, CheckCheck, Link2, Check, CircleDashed} from 'lucide-react';
import './evidence-flow.css';

// Original relationship map. It describes the evidence in this walkthrough;
// the lines are relationships, never a claim that an integration is running.
export default function EvidenceFlow({state, derived: d, onSource, onDocument, onStep}) {
  const [active, setActive] = useState(null);
  const reduce = useReducedMotion();
  const titleId = useId();
  const doc = state.document;
  const resolved = state.requirement.status === 'resolved';
  const reviewStatus = d.hasCustomerApproval ? 'Approval recorded' : d.canRecordCustomerApproval ? 'Awaiting counsel' : d.canRequestCustomerReview ? 'Ready to request' : 'Prerequisites open';
  const reviewStep = d.hasCustomerApproval || d.canRecordCustomerApproval || d.canRequestCustomerReview ? 3 : d.isPlanApproved ? 2 : 1;
  const related = id => active === id || active === 'review';
  const spring = reduce ? {duration: 0} : {type: 'spring', stiffness: 380, damping: 32};

  function node({id, icon: Icon, eyebrow, title, detail, status, tone, onClick}) {
    return <motion.button type="button" className={`evidence-flow-node flow-${id} ${tone || ''}`} onClick={onClick}
      onMouseEnter={() => setActive(id)} onMouseLeave={() => setActive(null)} onFocus={() => setActive(id)} onBlur={() => setActive(null)}
      whileHover={reduce ? undefined : {y: -2}} whileTap={reduce ? undefined : {scale: .985}} transition={spring}>
      <span className="flow-node-top"><span className="flow-node-icon"><Icon size={18}/></span><span className="flow-eyebrow">{eyebrow}</span><span className="flow-open-icon"><Link2 size={13}/></span></span>
      <strong>{title}</strong><span className="flow-node-detail">{detail}</span>
      {status && <span className="flow-node-status">{tone === 'complete' ? <Check size={12}/> : <CircleDashed size={12}/>} {status}</span>}
    </motion.button>;
  }

  return <section className="evidence-flow" aria-labelledby={titleId}>
    <div className="evidence-flow-heading"><div><span className="flow-eyebrow">RECORD RELATIONSHIPS</span><h2 id={titleId}>How the records connect</h2></div><span className="flow-agreement"><Link2 size={14}/>{renewalFixture.agreement.label}</span></div>
    <div className="evidence-flow-map">
      <svg className="evidence-flow-lines" viewBox="0 0 1000 250" preserveAspectRatio="none" aria-hidden="true">
        <path d="M 270 62 C 355 62 340 125 422 125" className="flow-wire"/>
        <path d="M 270 190 C 355 190 340 125 422 125" className={`flow-wire ${!d.isDocumentVerified ? 'unverified' : ''}`}/>
        <path d="M 668 125 L 758 125" className="flow-wire"/>
        <motion.path d="M 270 62 C 355 62 340 125 422 125" className="flow-wire-lit" animate={{opacity: related('customer') ? 1 : 0}} transition={spring}/>
        <motion.path d="M 270 190 C 355 190 340 125 422 125" className="flow-wire-lit" animate={{opacity: related('document') ? 1 : 0}} transition={spring}/>
        <motion.path d="M 668 125 L 758 125" className="flow-wire-lit" animate={{opacity: active === 'review' || active === 'resolution' ? 1 : 0}} transition={spring}/>
        <circle cx="400" cy="125" r="3.5" className="flow-junction"/>
      </svg>
      <span className="flow-relation relation-condition">Establishes<br/>the condition</span>
      <span className="flow-relation relation-document">Enables<br/>the review</span>
      <span className="flow-relation relation-decision">{d.hasCustomerApproval?<>Recorded<br/>approval</>:<>Approval<br/>required</>}</span>
      {node({id:'customer', icon:Phone, eyebrow:`CUSTOMER · ${renewalFixture.bindings.customer_statement}`, title:'Counsel approval required', detail:`${renewalEvidence[0].speaker} · ${renewalEvidence[0].type}`, onClick:() => onSource(0), tone:'source'})}
      {node({id:'document', icon:FileText, eyebrow:'DOCUMENT', title:doc ? doc.title || doc.name : 'Current AI terms needed', detail:doc ? `Version ${doc.version} · ${renewalFixture.agreement.label}` : 'The applicable version is missing', status:d.isDocumentVerified ? 'Version verified' : doc ? 'Needs verification' : 'Not received', tone:d.isDocumentVerified ? 'complete' : doc ? '' : 'missing', onClick:() => doc ? onDocument() : onStep(d.isPlanApproved ? 2 : 1)})}
      {node({id:'review', icon:ShieldCheck, eyebrow:'HUMAN DECISION', title:'Customer counsel review', detail:`${fixturePeople.customerCounsel} · Customer counsel`, status:reviewStatus, tone:d.hasCustomerApproval ? 'complete' : 'decision', onClick:() => onStep(reviewStep)})}
      {node({id:'resolution', icon:CheckCheck, eyebrow:resolved?'VERIFIED OUTCOME':'RESOLUTION', title:'Customer condition', detail:resolved ? 'Resolution preserved in history' : 'Resolve with reviewed evidence', status:resolved ? 'Resolved' : 'Still open', tone:resolved ? 'complete' : '', onClick:() => onStep(4)})}
    </div>
    <button type="button" className="flow-context" onClick={() => onSource(1)}>
      <span className="flow-context-icon"><MessageSquare size={18}/></span><span className="flow-context-copy"><span className="flow-eyebrow">SELLER EXPECTATION · {renewalFixture.bindings.comparison_statement}</span><strong>“{renewalEvidence[1].quote}”</strong></span><span className="flow-context-boundary">Commercial context<span>Does not establish approval</span></span><Link2 className="flow-context-open" size={15}/>
    </button>
    <p className="flow-caption">Open any record to inspect its source or the work it requires. A resolved condition does not confirm a signed renewal.</p>
  </section>;
}
