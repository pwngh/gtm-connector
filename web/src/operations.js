import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import {derive} from './workflow.js';

// One account, three follow-up reasons. Reasons can share a prerequisite;
// they are not three independent tasks or a portfolio-wide blocker count.
export function buildOperations(state, d=derive(state)) {
  const resolved=state.requirement.status==='resolved';
  const reviewPending=state.plan.status==='in_review';
  const changesRequested=state.plan.status==='changes_requested';
  const planOwner=d.isPlanApproved||reviewPending?fixturePeople.internalReviewer:state.plan.owner;
  const prerequisite=d.isPlanApproved
    ? {id:'document',label:'Document preparation',step:2}
    : {id:'plan',label:'Internal work approval',step:1};
  const prerequisiteOwner=d.isPlanApproved?state.plan.owner:planOwner;
  const counselPending=d.canRecordCustomerApproval;

  const rows=[
    {
      id:'plan',title:'Internal work approval',owner:planOwner,waitingOn:null,
      reason:d.isPlanApproved?'The current plan version is approved.':reviewPending?'The submitted work is waiting for internal review.':changesRequested?'The reviewer requested changes to the work.':'The document work has not been submitted for review.',
      status:d.isPlanApproved?'Approved':reviewPending?'Awaiting review':changesRequested?'Changes requested':'Not submitted',
      bucket:d.isPlanApproved?'done':'ready',
      action:d.isPlanApproved?'View approved work':reviewPending?'Review submitted plan':changesRequested?'Revise work plan':'Prepare work plan',
      step:1,dependency:null,
      completionRule:`An internal reviewer approves plan version ${state.plan.revision} and its document specifications.`,
    },
    {
      id:'document',title:'Document preparation',
      owner:d.isDocumentVerified||d.isPlanApproved?state.plan.owner:planOwner,waitingOn:null,
      reason:d.isDocumentVerified?'The document’s agreement and current version are verified.':state.document?'The document is present; its agreement and version still need verification.':'The applicable AI terms document has not been received.',
      status:d.isDocumentVerified?'Verified':state.document?'Verification needed':'Missing document',
      bucket:d.isDocumentVerified?'done':d.isPlanApproved?'ready':'blocked',
      action:d.isDocumentVerified?'View document task':d.isPlanApproved?'Open document task':'Review prerequisite',
      step:d.isDocumentVerified||d.isPlanApproved?2:1,
      dependency:d.isDocumentVerified||d.isPlanApproved?null:{id:'plan',label:'Internal work approval',step:1},
      completionRule:`Verify that the received document is current, applies to ${renewalFixture.agreement.label}, and meets the plan’s specifications.`,
    },
    {
      id:'condition',title:'Customer condition',
      owner:resolved||d.hasCustomerApproval||counselPending?fixturePeople.internalReviewer:d.canRequestCustomerReview?state.plan.owner:prerequisiteOwner,
      waitingOn:counselPending?`${fixturePeople.customerCounsel} · Customer counsel`:null,
      reason:resolved?'The condition was resolved with recorded approval evidence.':d.hasCustomerApproval?'Customer approval is recorded; the resolution still needs to be confirmed.':counselPending?'Customer counsel’s decision has not been recorded.':d.canRequestCustomerReview?'The current document is ready for customer counsel review.':!d.isPlanApproved?'The current work plan needs internal approval before customer review can proceed.':'Obtain and verify the applicable document before requesting customer counsel review.',
      status:resolved?'Resolved':d.hasCustomerApproval?'Ready to resolve':counselPending?'Awaiting counsel':d.canRequestCustomerReview?'Ready to request':'Prerequisite open',
      bucket:resolved?'done':d.hasCustomerApproval||d.canRequestCustomerReview?'ready':counselPending?'waiting':'blocked',
      action:resolved?'View resolution':d.hasCustomerApproval?'Review resolution':counselPending?'Record counsel’s decision':d.canRequestCustomerReview?'Prepare counsel review':'View prerequisite',
      step:resolved||d.hasCustomerApproval?4:d.canRequestCustomerReview||counselPending?3:prerequisite.step,
      dependency:resolved||d.hasCustomerApproval||counselPending||d.canRequestCustomerReview?null:prerequisite,
      completionRule:'Record customer counsel’s approval for the current document and plan version, then explicitly record the condition’s resolution.',
    },
  ];
  const counts={ready:0,blocked:0,waiting:0,done:0};
  for(const row of rows)counts[row.bucket]+=1;
  return {
    rows,counts,
    next:rows.find(row=>row.bucket==='ready')||rows.find(row=>row.bucket==='waiting')||rows.find(row=>row.bucket!=='done')||null,
    openCount:rows.length-counts.done,
    completeCount:counts.done,
  };
}
