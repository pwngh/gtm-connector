import {renewalFixture,fixturePeople} from './renewalFixtures.js';
import {derive} from './workflow.js';
import {awaitedCounsel} from './renewalEvidence.js';

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
  // Waiting only while the customer's signed letter is outstanding. Until an authority
  // record exists, the awaited party is a role, not a person.
  const reviewOpen=d.customerReviewIsCurrent&&!d.hasCustomerApproval;
  const counselPending=reviewOpen&&d.nextCommand==='upload_approval_letter';
  const evidenceStep=reviewOpen&&!counselPending;
  const awaited=d.authorityContact?`${d.authorityContact.displayName} · ${d.authorityContact.role||'Recorded contact'} (authority record)`:awaitedCounsel.label;
  const evidenceAction={inspect_approval_letter:'Inspect the approval letter',record_customer_authority:'Record who decides',record_customer_approval:'Record the cited decision'}[d.nextCommand];

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
      reason:d.isDocumentVerified?'The document’s agreement and current version are verified.':d.documentRejected?'The received version was rejected at inspection. Its receipt is kept; a replacement is needed.':state.document?'The document is present; its agreement and version still need verification.':'The applicable AI terms document has not been received.',
      status:d.isDocumentVerified?'Verified':d.documentRejected?'Replacement needed':state.document?'Verification needed':'Missing document',
      bucket:d.isDocumentVerified?'done':d.isPlanApproved?'ready':'blocked',
      action:d.isDocumentVerified?'View document task':d.isPlanApproved?'Open document task':'Review prerequisite',
      step:d.isDocumentVerified||d.isPlanApproved?2:1,
      dependency:d.isDocumentVerified||d.isPlanApproved?null:{id:'plan',label:'Internal work approval',step:1},
      completionRule:`Verify that the received document is current, applies to ${renewalFixture.agreement.label}, and meets the plan’s specifications.`,
    },
    {
      id:'condition',title:'Customer condition',
      owner:resolved||d.hasCustomerApproval||reviewOpen?fixturePeople.internalReviewer:d.canRequestCustomerReview?state.plan.owner:prerequisiteOwner,
      waitingOn:counselPending?awaited:null,
      reason:resolved?'The condition was resolved from a decision citing an inspected signed letter, an authority record and an independent version basis.':d.hasCustomerApproval?'The cited customer decision is recorded; the resolution still needs to be confirmed.':counselPending?'The customer’s signed decision has not been received.':evidenceStep?'The customer’s signed letter is received. Its inspection, the authority record and the cited decision are recorded in turn.':d.canRequestCustomerReview?'The current document is ready for customer counsel review.':!d.isPlanApproved?'The current work plan needs internal approval before customer review can proceed.':'Obtain and verify the applicable document before requesting customer counsel review.',
      status:resolved?'Resolved':d.hasCustomerApproval?'Ready to resolve':counselPending?'Awaiting counsel':evidenceStep?'Evidence to record':d.canRequestCustomerReview?'Ready to request':'Prerequisite open',
      bucket:resolved?'done':d.hasCustomerApproval||d.canRequestCustomerReview||evidenceStep?'ready':counselPending?'waiting':'blocked',
      action:resolved?'View resolution':d.hasCustomerApproval?'Review resolution':counselPending?'Add the signed letter when it arrives':evidenceStep?evidenceAction||'Open the review task':d.canRequestCustomerReview?'Prepare counsel review':'View prerequisite',
      step:resolved||d.hasCustomerApproval?4:d.canRequestCustomerReview||reviewOpen?3:prerequisite.step,
      dependency:resolved||d.hasCustomerApproval||reviewOpen||d.canRequestCustomerReview?null:prerequisite,
      completionRule:'Record a customer decision that cites an inspected signed letter and names the contact from the authority record, then explicitly record the condition’s resolution.',
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
