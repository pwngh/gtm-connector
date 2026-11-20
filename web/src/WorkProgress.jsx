import React from 'react';
import {motion,useReducedMotion} from 'motion/react';
import {Check,FileText,ShieldCheck,CheckCheck,ClipboardCheck} from 'lucide-react';

export default function WorkProgress({state,derived:d,selected,onSelect}){
  const reduce=useReducedMotion();
  const resolved=d.requirementStatus==='resolved';
  const stages=[
    {id:1,label:'Plan',sub:d.isPlanApproved?'Approved':state.plan.status==='in_review'?'In review':state.plan.status==='changes_requested'?'Changes needed':'Draft to review',done:d.isPlanApproved,icon:ClipboardCheck},
    {id:2,label:'Document',sub:d.isDocumentVerified?'Accepted':d.documentRejected?'Rejected':state.document?'Inspection required':'Not received',done:d.isDocumentVerified,icon:FileText},
    {id:3,label:'Counsel',sub:d.hasCustomerApproval?'Decision recorded':d.customerReviewIsCurrent?({upload_approval_letter:d.canUploadApprovalLetter?'Add the letter':'Awaiting letter',inspect_approval_letter:'Inspect letter',record_customer_authority:'Record authority'})[d.nextCommand]||(d.decisionNeedsReview?'Re-review decision':'Record decision'):d.canRequestCustomerReview?'Ready to request':d.decisionNeedsReview?'Decision needs re-review':!d.isDocumentVerified?'Needs inspected document':'Needs plan approval',done:d.hasCustomerApproval,icon:ShieldCheck},
    {id:4,label:'Resolution',sub:resolved?'Recorded':d.resolutionNeedsReview?'Needs review':d.canResolveRequirement?'Ready to confirm':'Evidence required',done:resolved,icon:CheckCheck},
  ];
  return <section className="workflow-progress" aria-label="Workflow progress">
    <div className="progress-summary"><span>Evidence to resolution</span><span className="progress-count">{stages.filter(s=>s.done).length} of 4 stages complete</span></div>
    <nav className="progress-track" aria-label="Workflow stages">{stages.map(s=><button key={s.id} className={`progress-node ${s.done?'complete':''} ${selected===s.id?'selected':''}`} aria-current={selected===s.id?'step':undefined} onClick={()=>onSelect(s.id)}>{selected===s.id&&<motion.span className="progress-active-fill" layoutId="workflow-active" transition={reduce?{duration:0}:{type:'spring',stiffness:380,damping:34}}/>}<span className="progress-index">{s.done?<Check size={18}/>:<s.icon size={19}/>}</span><span className="progress-copy"><strong className="progress-label">{s.label}</strong><span className="progress-sub">{s.sub}</span></span></button>)}</nav>
  </section>;
}
