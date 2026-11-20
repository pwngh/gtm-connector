import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import React, {Fragment, useId, useLayoutEffect, useRef} from 'react';
import {useSearchParams} from 'react-router';
import {CheckCircle2, CircleDashed, ClipboardCheck, FileText, GitBranch, History, Info, Link2, Phone, ShieldCheck, UserRound, X} from 'lucide-react';
import PlanesSegmentedControl from './vendor/PlanesSegmentedControl';
import {MotionConfig} from 'motion/react';
import {buildOperations} from './operations';
import './operations.css';

const groups=[{id:'ready',label:'Ready to act'},{id:'blocked',label:'Prerequisites open'},{id:'waiting',label:'Awaiting counsel'},{id:'done',label:'Complete'}];
const icons={plan:ClipboardCheck,document:FileText,condition:ShieldCheck};
const dateText=value=>value?new Date(value).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}):null;

// Follow-up reasons stay connected to their exact resolving work and evidence.
export default function FollowUpReport({state, derived:d, evidence, onTask, onHistory, onSource, onDocument, onDecision, onScope}) {
  const report=buildOperations(state,d);
  const [params,setParams]=useSearchParams();
  const id=useId();
  const viewTools=useRef(null), restoreViewFocus=useRef(false);
  useLayoutEffect(()=>{
    if(!restoreViewFocus.current)return;
    viewTools.current?.querySelector('[role="radio"][aria-checked="true"]')?.focus();
    restoreViewFocus.current=false;
  },[params]);
  const bucket=groups.some(g=>g.id===params.get('status'))?params.get('status'):'all';
  const view=bucket==='done'||params.get('view')==='all'?'all':'open';
  const shown=report.rows.filter(r=>(view==='all'||r.bucket!=='done')&&(bucket==='all'||r.bucket===bucket));
  const active=shown.find(r=>r.id===params.get('reason'))||shown.find(r=>r.id===report.next?.id)||shown[0];
  const latest=state.history.at(-1);
  const next=report.next;
  const dependantCount=next?report.rows.filter(r=>r.dependency?.id===next.id).length:0;
  function update(values){const query=new URLSearchParams(params);Object.entries(values).forEach(([key,value])=>value==null?query.delete(key):query.set(key,value));setParams(query,{replace:true,preventScrollReset:true});}
  function act(row){if(row.id==='condition'&&row.bucket==='done')onHistory();else onTask(row.step);}
  function recoverView(values){restoreViewFocus.current=true;update(values);}
  const showAll=()=>recoverView({view:'all',status:null});

  return <MotionConfig reducedMotion="user"><div className="ops-page">
    <div className="ops-scope"><span><strong>{renewalFixture.account.name}</strong><span className="ops-scope-divider">/</span>{renewalFixture.agreement.label}</span><button className="text-button" onClick={onScope}><Info size={15}/>Report scope</button></div>

    <section className={`ops-brief ${next?'':'is-complete'}`} aria-label="Next action">
      <div className="ops-brief-mark" aria-hidden="true">{next?<GitBranch size={27}/>:<CheckCircle2 size={27}/>}</div>
      <div className="ops-brief-copy"><span className="ops-kicker">{!next?'WORK RECORDED':next.bucket==='waiting'?'AWAITING A CUSTOMER DECISION':'NEXT ACTION'}</span><h2>{next?next.action:'The operational work is complete.'}</h2><p>{!next?'The condition is resolved with evidence. The renewal remains unconfirmed.':next.bucket==='waiting'?'Record counsel’s decision when its supporting evidence is available.':next.id==='plan'&&dependantCount?`${dependantCount} other follow-up ${dependantCount===1?'reason depends':'reasons depend'} on this approval.`:next.id==='document'?'A verified document enables customer review. Receiving the file is a separate step.':d.hasCustomerApproval?'The approval is recorded. Review its evidence and record the resolution.':'The verified document is ready for a customer counsel review request.'}</p></div>
      <div className="ops-brief-action">{next&&<span><UserRound size={14}/>{next.owner}</span>}<button className="button primary" onClick={()=>next?act(next):onHistory()}>{next?'Open next action':'Inspect resolution history'}</button></div>
    </section>

    <section className="ops-state-summary" aria-label="Follow-up reason states">
      <div className="ops-summary-heading"><span className="ops-kicker">WHERE THE WORK STANDS</span><span><strong>{report.openCount}</strong> open · {report.completeCount} complete · {report.rows.length} reasons</span></div>
      <div className="ops-state-bars">{groups.map(group=><button key={group.id} className={`ops-state ${group.id}`} aria-pressed={bucket===group.id} onClick={()=>update({status:bucket===group.id?null:group.id,view:group.id==='done'?'all':'open'})}><span className="ops-state-top"><span>{group.label}</span><strong>{report.counts[group.id]}</strong></span><span className="ops-bar-track" aria-hidden="true"><span style={{width:`${report.counts[group.id]/report.rows.length*100}%`}}/></span></button>)}</div>
    </section>



    <div className="ops-workbench">
      <div className="ops-list-head"><div><span className="ops-kicker">WORKLIST</span><h2>Follow-up reasons</h2></div><span className="ops-list-note">Workflow order</span></div>
    <div className="ops-view-tools" ref={viewTools}><PlanesSegmentedControl label="Follow-up view" options={[{value:'open',label:'Open reasons'},{value:'all',label:'All reasons'}]} value={view} onChange={value=>update({view:value,status:null})}/>{bucket!=='all'&&<button className="ops-filter-chip" onClick={()=>recoverView({status:null})}> {groups.find(g=>g.id===bucket).label}<X size={14}/><span className="sr-only">Clear state filter</span></button>}<span className="ops-result-count" role="status">{shown.length} of {report.rows.length} reasons shown</span></div>
      {shown.map((row,index)=>{const Icon=icons[row.id];return <Fragment key={row.id}>
        <button className={`ops-reason ${row.bucket} ${active?.id===row.id?'is-selected':''}`} style={{'--reason-row':index+3}} id={`${id}-${row.id}`} aria-pressed={active?.id===row.id} aria-controls={`${id}-details`} onClick={()=>update({reason:row.id})}>
          <span className="ops-reason-icon"><Icon size={20}/></span><span className="ops-reason-copy"><span className="ops-reason-title">{row.title}</span><span className="ops-reason-status">{row.status}</span><span className="ops-reason-owner"><UserRound size={13}/>{row.owner}<span>Action owner</span></span>{row.waitingOn&&<span className="ops-reason-wait">Waiting on {row.waitingOn}</span>}</span><span className="ops-reason-indicator" aria-hidden="true">{row.bucket==='done'?<CheckCircle2 size={18}/>:active?.id===row.id?<span/>:<CircleDashed size={18}/>}</span>
        </button>
        {active?.id===row.id&&<section className="ops-folio" id={`${id}-details`} aria-label={`${row.title} details`}>
          <div className="ops-folio-header"><span className="ops-kicker">REASON DETAILS</span><span className={`ops-status ${row.bucket}`}>{row.status}</span></div>
          <h2>{row.title}</h2><p className="ops-folio-description">{row.reason}</p>
          <dl className="ops-responsibility"><div><dt>Action owner</dt><dd><span className="ops-person-avatar" aria-hidden="true">{row.owner.split(' ').map(n=>n[0]).join('')}</span>{row.owner}</dd></div><div><dt>{row.waitingOn?'Waiting on':row.dependency?'First needed':'Current scope'}</dt><dd>{row.waitingOn||row.dependency?.label||`Plan version ${state.plan.revision}`}</dd></div></dl>
          {row.dependency&&<div className="ops-dependency"><GitBranch size={17}/><p>This reason is held by <strong>{row.dependency.label.toLowerCase()}</strong>. Its action opens the prerequisite first.</p></div>}
          <div className="ops-completion-rule"><span className="ops-kicker">WHAT CLEARS THIS REASON</span><p>{row.completionRule}</p></div>
          <div className="ops-proof">
            <span className="ops-kicker">{row.id==='condition'?'SUPPORTING EVIDENCE':'RECORD TO INSPECT'}</span>
            {row.id==='plan'&&<><button className="ops-record" onClick={()=>onTask(1)}><ClipboardCheck size={21}/><span><strong>Work plan · version {state.plan.revision}</strong><small>{d.isPlanApproved?`Approved by ${state.plan.approver||'${fixturePeople.internalReviewer}'}`:state.plan.status==='in_review'?`Submitted to ${fixturePeople.internalReviewer}`:'Document specifications and staff instructions'}</small></span><Link2 size={15}/></button>{state.plan.reviewNote&&<p className="ops-review-note"><strong>Reviewer’s note</strong>{state.plan.reviewNote}</p>}</>}
            {row.id==='document'&&(state.document?<button className="ops-record" onClick={onDocument}><FileText size={21}/><span><strong>{state.document.title} · {state.document.version}</strong><small>{state.document.source}</small><small>{d.isDocumentVerified?`Verified ${dateText(state.document.verifiedAt)}`:`Received ${dateText(state.document.receivedAt)} · Needs verification`}</small></span><Link2 size={15}/></button>:<div className="ops-missing"><FileText size={22}/><div><strong>Current AI terms document</strong><span>Not received · {renewalFixture.agreement.label}</span></div></div>)}
            {row.id==='document'&&<div className="ops-artifact-note"><strong>Document specifications</strong><p>{state.plan.specifications}</p></div>}
            {row.id==='condition'&&<><blockquote>“{evidence[0].quote}”</blockquote><button className="ops-citation" onClick={()=>onSource(0)}><Phone size={15}/><span>{evidence[0].id} · {evidence[0].speaker}<small>{evidence[0].type} · {evidence[0].time}</small></span><Link2 size={14}/></button>{d.hasCustomerApproval&&<button className="ops-record approval" onClick={onDecision}><ShieldCheck size={20}/><span><strong>Customer approval recorded</strong><small>Customer reviewer (reviewer input): {state.customerReview.reviewedBy} · Document {state.customerReview.documentVersion}</small></span><Link2 size={15}/></button>}</>}
          </div>
          <div className="ops-folio-action"><span>{row.bucket==='done'?'Inspect the completed work':'Continue with the required work'}</span><button className="button primary" onClick={()=>act(row)}>{row.action}</button></div>
        </section>}
      </Fragment>})}
      {!shown.length&&<div className="ops-empty"><CheckCircle2 size={28}/><h2>{report.openCount===0&&view==='open'?'Every operational reason is complete.':'No reasons in this view.'}</h2><p>{report.openCount===0?'The supporting decisions are preserved in history. The commercial renewal remains unconfirmed.':'Try another work state, or inspect all three reasons.'}</p><button className="button" onClick={showAll}>Show all reasons</button></div>}
    </div>

    <div className="ops-ledger-row"><span className="ops-ledger-icon"><History size={20}/></span><div><span className="ops-kicker">LATEST WORKSPACE CHANGE</span><strong>{latest?.title||'Prepared draft · no activity recorded yet'}</strong><p>{latest?dateText(latest.at):'Actions taken in this walkthrough will appear in decision history.'}</p></div><button className="text-button" onClick={onHistory}>View history</button></div>
    <div className="ops-commercial"><span><CircleDashed size={17}/><strong>Commercial renewal</strong></span><span className="ops-commercial-state">Not confirmed</span><p>Completion here records the operational work. A signed renewal is a separate outcome.</p></div>
  </div></MotionConfig>;
}
