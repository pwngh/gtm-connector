import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState,transition} from './workflow.js';
import {buildOperations} from './operations.js';
import {customerDecisionSteps} from './customerDecision.js';
import {walkthroughInputs} from './testing/walkthroughInputs.js';
import {approvalLetters,walkthroughDocumentExamples} from './renewalFixtures.js';

const act=(state,type,fields={})=>transition(state,{type,...fields});
const step=(state,type,fields={})=>act(state,type,{...walkthroughInputs(state,type),...fields});
const authorize=state=>act(act(state,'request_review'),'approve_plan');
const verified=()=>step(step(authorize(initialState),'obtain_document'),'verify_document');
const approved=()=>customerDecisionSteps.reduce(step,act(verified(),'request_customer_review'));
const completed=()=>act(approved(),'resolve_requirement');
const row=(report,id)=>report.rows.find(item=>item.id===id);

function assertPartition(report) {
  assert.deepEqual(report.rows.map(item=>item.id),['plan','document','condition']);
  assert.equal(Object.values(report.counts).reduce((sum,count)=>sum+count,0),3);
  assert.equal(report.openCount+report.completeCount,3);
  for(const bucket of ['ready','blocked','waiting','done']) {
    assert.equal(report.counts[bucket],report.rows.filter(item=>item.bucket===bucket).length);
  }
  for(const item of report.rows.filter(item=>item.bucket==='blocked')) {
    assert.ok(item.dependency,'A blocked reason names its resolving prerequisite.');
    assert.equal(item.step,item.dependency.step);
  }
}

test('the report counts dependent reasons without presenting them as independent next actions',()=>{
  const initial=buildOperations(initialState);
  assertPartition(initial);
  assert.deepEqual(initial.counts,{ready:1,blocked:2,waiting:0,done:0});
  assert.equal(initial.next.id,'plan');
  assert.equal(initial.next.owner,'Morgan Reed');
  assert.equal(initial.next.step,1);
  assert.deepEqual(initial.rows.slice(1).map(item=>item.dependency.id),['plan','plan']);
  const submitted=buildOperations(act(initialState,'request_review'));
  assert.equal(submitted.next.owner,'Alex Chen');
  assert.ok(submitted.rows.every(item=>item.owner==='Alex Chen'));
  const returned=buildOperations(act(act(initialState,'request_review'),'request_changes',{note:'Clarify the document source.'}));
  assert.equal(returned.next.owner,'Morgan Reed');
  assert.equal(returned.next.action,'Revise work plan');
});

test('the full workflow moves the next action to its responsible person and keeps counsel waiting distinct',()=>{
  let state=authorize(initialState);
  let report=buildOperations(state);
  assert.deepEqual(report.counts,{ready:1,blocked:1,waiting:0,done:1});
  assert.equal(report.next.id,'document');
  assert.equal(report.next.owner,'Morgan Reed');
  assert.equal(row(report,'condition').dependency.id,'document');

  state=act(state,'obtain_document');
  report=buildOperations(state);
  assert.equal(report.next.id,'document');
  assert.equal(report.next.status,'Inspection needed');
  assert.equal(report.next.step,2);

  state=step(state,'verify_document');
  report=buildOperations(state);
  assert.deepEqual(report.counts,{ready:1,blocked:0,waiting:0,done:2});
  assert.equal(report.next.id,'condition');
  assert.equal(report.next.owner,'Morgan Reed');
  assert.equal(report.next.step,3);

  state=act(state,'request_customer_review');
  report=buildOperations(state);
  assert.deepEqual(report.counts,{ready:0,blocked:0,waiting:1,done:2});
  assert.equal(report.next.owner,'Alex Chen');
  assert.equal(report.next.waitingOn,'Customer counsel · unidentified');
  assert.equal(report.next.step,3);

  // The letter, its inspection and the authority record are the reviewer's work, not a wait on the customer.
  for(const type of customerDecisionSteps.slice(0,-1)) {
    state=step(state,type);
    report=buildOperations(state);
    assert.deepEqual([report.next.id,report.next.bucket,report.next.owner,report.next.waitingOn],['condition','ready','Alex Chen',null]);
  }
  state=step(state,'record_customer_approval');
  report=buildOperations(state);
  assert.equal(report.next.bucket,'ready');
  assert.equal(report.next.step,4);
  assert.equal(report.next.waitingOn,null);
  state=act(state,'resolve_requirement');
  report=buildOperations(state);
  assertPartition(report);
  assert.deepEqual(report.counts,{ready:0,blocked:0,waiting:0,done:3});
  assert.equal(report.next,null);
  assert.equal(row(report,'condition').action,'View resolution');
  assert.match(row(report,'condition').reason,/accepted signed letter, an authority record for that version and the agreement record’s version basis/);
  assert.equal(row(report,'condition').step,4);
  assert.equal(state.commercial.status,'not_confirmed');
});

test('plan-only changes retain verified document work while specification changes require verification again',()=>{
  const resolved=completed();
  const before=structuredClone(resolved);
  const ownerEdit=act(resolved,'save_plan',{owner:'Maya Chen'});
  const ownerReport=buildOperations(ownerEdit);
  assertPartition(ownerReport);
  // An owner edit keeps the customer decision and the resolution; only the plan needs review again.
  assert.deepEqual(ownerReport.counts,{ready:1,blocked:0,waiting:0,done:2});
  assert.equal(ownerReport.next.id,'plan');
  assert.equal(ownerReport.next.owner,'Maya Chen');
  assert.equal(row(ownerReport,'document').bucket,'done');
  assert.equal(row(ownerReport,'condition').status,'Resolved');

  const specEdit=step(resolved,'save_plan',{specifications:'Confirm Agreement A-17 and retain the source timestamp.'});
  const specReport=buildOperations(specEdit);
  assertPartition(specReport);
  assert.deepEqual(specReport.counts,{ready:1,blocked:2,waiting:0,done:0});
  assert.equal(row(specReport,'document').bucket,'blocked');
  assert.equal(row(specReport,'document').step,1);
  const reapproved=buildOperations(authorize(specEdit));
  assert.equal(reapproved.next.id,'document');
  assert.equal(reapproved.next.step,2);
  assert.equal(row(reapproved,'condition').dependency.id,'document');
  assert.deepEqual(resolved,before,'Selecting report data cannot mutate the source state or historical approval.');
});

test('replacement and reopening expose the actual recovery action instead of inheriting a prior resolution',()=>{
  const state=completed();
  const replaced=buildOperations(step(state,'obtain_document',{document:{id:'terms-v4',version:'v4',currentVersion:'v4'}}));
  assertPartition(replaced);
  assert.deepEqual(replaced.counts,{ready:1,blocked:1,waiting:0,done:1});
  assert.equal(replaced.next.id,'document');
  assert.equal(replaced.next.step,2);
  assert.equal(row(replaced,'condition').dependency.id,'document');
  const reopened=buildOperations(step(state,'reopen_requirement',{reason:'Customer requested another review.'}));
  assertPartition(reopened);
  assert.deepEqual(reopened.counts,{ready:1,blocked:0,waiting:0,done:2});
  assert.equal(reopened.next.id,'condition');
  // The decision is kept for re-review, so the reviewer records it again; no new request is needed.
  assert.equal(reopened.next.owner,'Alex Chen');
  assert.equal(reopened.next.action,'Record the cited decision');
  assert.equal(reopened.next.step,3);
  assert.equal(reopened.next.waitingOn,null);
});

test('the awaited counsel stays a role; a withdrawn authority or another version never names the recorded contact',()=>{
  const pending=act(verified(),'request_customer_review');
  assert.equal(buildOperations(pending).next.waitingOn,'Customer counsel · unidentified');
  const withdrawn=buildOperations(step(completed(),'withdraw_customer_authority',{reason:'The authority basis was contradicted.'}));
  assert.equal(withdrawn.next.bucket,'waiting');
  assert.equal(withdrawn.next.waitingOn,'Customer counsel · unidentified');
  const replaced=buildOperations(step(completed(),'obtain_document',{document:walkthroughDocumentExamples.replacement}));
  assert.equal(row(replaced,'condition').waitingOn,null);
  assert.ok(!JSON.stringify(replaced.rows).includes(approvalLetters[0].signatory.name));
});
