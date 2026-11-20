import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState,transition} from './workflow.js';
import {buildOperations} from './operations.js';

const act=(state,type,fields={})=>transition(state,{type,...fields});
const authorize=state=>act(act(state,'request_review'),'approve_plan');
const verified=()=>act(act(authorize(initialState),'obtain_document'),'verify_document');
const approved=()=>act(act(verified(),'request_customer_review'),'record_customer_approval',{
  reviewedBy:'Nina Patel, customer counsel',recordedBy:'Alex Chen',evidenceText:'Written approval of the current terms for Agreement A-17.',
});
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
  assert.equal(report.next.status,'Verification needed');
  assert.equal(report.next.step,2);

  state=act(state,'verify_document');
  report=buildOperations(state);
  assert.deepEqual(report.counts,{ready:1,blocked:0,waiting:0,done:2});
  assert.equal(report.next.id,'condition');
  assert.equal(report.next.owner,'Morgan Reed');
  assert.equal(report.next.step,3);

  state=act(state,'request_customer_review');
  report=buildOperations(state);
  assert.deepEqual(report.counts,{ready:0,blocked:0,waiting:1,done:2});
  assert.equal(report.next.owner,'Alex Chen');
  assert.equal(report.next.waitingOn,'Nina Patel · Customer counsel');
  assert.equal(report.next.step,3);

  state=act(state,'record_customer_approval',{reviewedBy:'Nina Patel',evidenceText:'Approved for Agreement A-17.'});
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
  assert.equal(row(report,'condition').step,4);
  assert.equal(state.commercial.status,'not_confirmed');
});

test('plan-only changes retain verified document work while specification changes require verification again',()=>{
  const resolved=completed();
  const before=structuredClone(resolved);
  const ownerEdit=act(resolved,'save_plan',{owner:'Maya Chen'});
  const ownerReport=buildOperations(ownerEdit);
  assertPartition(ownerReport);
  assert.deepEqual(ownerReport.counts,{ready:1,blocked:1,waiting:0,done:1});
  assert.equal(ownerReport.next.id,'plan');
  assert.equal(ownerReport.next.owner,'Maya Chen');
  assert.equal(row(ownerReport,'document').bucket,'done');
  assert.equal(row(ownerReport,'condition').dependency.id,'plan');

  const specEdit=act(resolved,'save_plan',{specifications:'Confirm Agreement A-17 and retain the source timestamp.'});
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
  const replaced=buildOperations(act(state,'obtain_document',{document:{id:'terms-v4',version:'v4',currentVersion:'v4'}}));
  assertPartition(replaced);
  assert.deepEqual(replaced.counts,{ready:1,blocked:1,waiting:0,done:1});
  assert.equal(replaced.next.id,'document');
  assert.equal(replaced.next.step,2);
  assert.equal(row(replaced,'condition').dependency.id,'document');
  const reopened=buildOperations(act(state,'reopen_requirement',{reason:'Customer requested another review.'}));
  assertPartition(reopened);
  assert.deepEqual(reopened.counts,{ready:1,blocked:0,waiting:0,done:2});
  assert.equal(reopened.next.id,'condition');
  assert.equal(reopened.next.owner,'Morgan Reed');
  assert.equal(reopened.next.step,3);
  assert.equal(reopened.next.waitingOn,null);
});
