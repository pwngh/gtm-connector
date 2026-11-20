import test from 'node:test';
import assert from 'node:assert/strict';
import {advanceReadinessJournal, selectReadiness} from './readiness.js';
import {derive, initialState, transition, transitionWalkthrough} from './workflow.js';
import {fixturePeople, renewalFixture, renewalRecords, worklistFixture, worklistPeople, worklistResponseExamples} from './renewalFixtures.js';
import {
  buildWorklist, clearWorklistSearch, daysUntil, formatWorklistTime, readWorklistLocation, resolveWorklistEntry,
  resolveWorklistSelection, safeWorklistReturn, selectWorklist, updateWorklistSearch, worklistAccountHref, worklistActionHref, worklistHref,
} from './worklist.js';

const act = (state, type, fields = {}) => transition(state, {type, ...fields});
const authorize = () => act(act(initialState, 'request_review'), 'approve_plan');
const received = () => act(authorize(), 'obtain_document');
const verified = () => act(received(), 'verify_document');
const reviewed = () => act(act(verified(), 'request_customer_review'), 'record_customer_approval', {reviewedBy: fixturePeople.customerCounsel, evidenceText: 'Local walkthrough decision.'});
const primary = data => data.renewals.find(row => row.id === renewalFixture.id);
const work = (data, kind) => data.workItems.find(row => row.renewalId === renewalFixture.id && row.kind === kind);
const ids = result => result.rows.map(row => row.id);
const data = () => buildWorklist(initialState);
const [first, second, third, unknown] = worklistFixture.renewals;

test('the worklist adapter emits string identities and revisions with separate actions and reasons, without mutations', () => {
  const stateBefore = structuredClone(initialState), fixtureBefore = structuredClone(worklistFixture);
  const result = data();
  for (const row of [...result.renewals, ...result.requirements, ...result.workItems]) {
    assert.equal(typeof row.id, 'string');
    assert.equal(typeof row.revision, 'string');
    assert.ok(Array.isArray(row.allowedActions));
    assert.ok(Array.isArray(row.blockedReasons));
    assert.ok(row.selectionKey.endsWith(row.id));
    assert.ok(row.allowedActions.every(action => !('reason' in action)));
    assert.ok(row.blockedReasons.every(reason => reason.code && reason.action && reason.reason));
  }
  assert.deepEqual(initialState, stateBefore);
  assert.deepEqual(worklistFixture, fixtureBefore);
  assert.equal(result.revision, worklistFixture.revision);
});

test('known dates use UTC calendar days and missing owners, readiness, stages and dates remain missing', () => {
  const result = data(), missing = result.renewals.find(row => row.id === unknown.id);
  assert.equal(primary(result).daysLeft, renewalFixture.account.days);
  assert.equal(daysUntil(first.renewalDate, worklistFixture.asOf), 24);
  assert.equal(daysUntil(null, worklistFixture.asOf), null);
  assert.equal(daysUntil(first.renewalDate, worklistFixture.asOf.slice(0, -1)), null);
  assert.equal(missing.owner, null);
  assert.equal(missing.renewalDate, null);
  assert.equal(missing.daysLeft, null);
  assert.equal(missing.openBlockerCount, null);
  assert.equal(missing.requirements, null);
  assert.equal(missing.crm.stage, null);
  assert.equal(missing.crm.readAt, null);
  assert.equal(missing.lastChangedValue, null);
  assert.equal(result.renewals.find(row => row.id === third.id).openBlockerCount, 0);
});

test('offsetless times are preserved literally instead of being reinterpreted in the browser time zone', () => {
  const literal = worklistFixture.asOf.slice(0, -1);
  assert.ok(formatWorklistTime(literal).startsWith(literal));
  const fixture = structuredClone(worklistFixture);
  fixture.renewals[0].lastChangedAt = literal;
  const result = buildWorklist(initialState, fixture);
  assert.equal(result.renewals[0].lastChangedValue, null);
  assert.equal(result.renewals[0].lastChangedTime, formatWorklistTime(literal));
});

test('renewal sorting supports both directions and always places unknown values last', () => {
  const expected = {
    days: [[second.id, third.id, first.id, unknown.id], [first.id, third.id, second.id, unknown.id]],
    blockers: [[third.id, first.id, second.id, unknown.id], [second.id, first.id, third.id, unknown.id]],
    changed: [[first.id, third.id, second.id, unknown.id], [first.id, third.id, second.id, unknown.id]],
  };
  for (const [sort, orders] of Object.entries(expected)) {
    for (const [index, direction] of ['asc', 'desc'].entries()) assert.deepEqual(ids(selectWorklist(data(), 'renewals', `scope=all&sort=${sort}&direction=${direction}`)), orders[index]);
  }
});

test('equal sort values have stable ID ordering and never mutate the source collection', () => {
  const result = data();
  const tied = {...result, renewals: result.renewals.map(row => ({...row, daysLeft: 8})).reverse()};
  const before = structuredClone(tied);
  const expected = tied.renewals.map(row => row.id).sort();
  assert.deepEqual(ids(selectWorklist(tied, 'renewals', 'scope=all&sort=days&direction=asc')), expected);
  assert.deepEqual(ids(selectWorklist(tied, 'renewals', 'scope=all&sort=days&direction=desc')), expected);
  assert.deepEqual(tied, before);
});

test('queue sorting uses renewal days, renewal blockers, work due times and individual change times', () => {
  for (const sort of ['days', 'blockers', 'changed', 'due']) {
    const field = {days: 'daysLeft', blockers: 'openBlockerCount', changed: 'lastChangedValue', due: 'dueValue'}[sort];
    for (const direction of ['asc', 'desc']) {
      const result = selectWorklist(data(), 'queue', `scope=all&sort=${sort}&direction=${direction}`);
      const values = result.rows.map(row => row[field]);
      const known = values.filter(value => value !== null);
      assert.deepEqual(values.slice(0, known.length), known);
      assert.deepEqual(known, [...known].sort((a, b) => direction === 'asc' ? a - b : b - a));
    }
  }
});

test('search, owner and stage intersect while unknown and unassigned are explicit filters', () => {
  const result = data();
  const filtered = selectWorklist(result, 'renewals', new URLSearchParams({scope: 'all', q: second.accountName.toUpperCase(), owner: second.ownerId, stage: second.crm.stage}));
  assert.deepEqual(ids(filtered), [second.id]);
  assert.equal(selectWorklist(result, 'renewals', `owner=${third.ownerId}&stage=negotiation`).total, 0);
  assert.deepEqual(ids(selectWorklist(result, 'renewals', 'scope=all&owner=unassigned&stage=unknown')), [unknown.id]);
  assert.equal(selectWorklist(result, 'renewals', 'scope=all&q=%20%20').total, result.renewals.length);
  assert.equal(selectWorklist(result, 'renewals', 'owner=not-a-record').total, 0);
  assert.equal(selectWorklist(result, 'renewals', 'stage=not-a-stage').total, 0);
});

test('queue ownership follows the action, not the renewal owner or the awaited party', () => {
  const result = buildWorklist(act(initialState, 'request_review'));
  const plan = work(result, 'plan_review');
  assert.equal(plan.owner.displayName, fixturePeople.internalReviewer);
  assert.equal(primary(result).owner.displayName, fixturePeople.accountOwner);
  const queue = selectWorklist(result, 'queue', `owner=${plan.owner.id}`);
  assert.deepEqual(ids(queue), [plan.id]);
  assert.equal(selectWorklist(result, 'renewals', `owner=${plan.owner.id}`).total, 0);
  assert.equal(plan.waitingOn, null);
});

test('attention facets count renewals before attention filtering and use union matching, not a sum', () => {
  const result = selectWorklist(data(), 'renewals', 'scope=all&attention=blockers&attention=due-soon');
  assert.equal(result.unit, 'renewals');
  assert.equal(result.facets.find(facet => facet.id === 'blockers').count, 2);
  assert.equal(result.facets.find(facet => facet.id === 'due-soon').count, 1);
  assert.equal(result.total, 2);
  assert.equal(result.facetScopeCount, 4);
  const scoped = selectWorklist(data(), 'renewals', `scope=all&stage=${second.crm.stage}&attention=blockers&attention=due-soon`);
  assert.equal(scoped.facetScopeCount, 2);
  assert.equal(scoped.total, 2);
  assert.ok(scoped.facets.every(facet => facet.count <= 2 && facet.unit === 'renewals'));
});

test('queue facets count overlapping work items without using illustrative due dates for urgency', () => {
  const fixture = structuredClone(worklistFixture);
  fixture.workItems[2].dueAt = fixture.workItems[4].dueAt;
  const result = selectWorklist(buildWorklist(initialState, fixture), 'queue', 'scope=all&attention=blocked&attention=due-soon');
  const blocked = result.facets.find(facet => facet.id === 'blocked').count;
  const due = result.facets.find(facet => facet.id === 'due-soon').count;
  assert.equal(result.unit, 'work items');
  assert.equal(blocked, 5);
  assert.equal(due, 1);
  assert.equal(result.total, 5);
  assert.equal(new Set(ids(result)).size, result.total);
  assert.equal(result.available, worklistFixture.workItems.length);
  assert.ok(new Set(result.rows.map(row => row.renewalId)).size > 1);
});

test('receiving a document completes collection only, without resolving or completing inspection', () => {
  const before = buildWorklist(authorize()), after = buildWorklist(received());
  assert.equal(work(before, 'collection').status, 'ready');
  assert.equal(work(after, 'collection').status, 'complete');
  assert.equal(work(after, 'inspection').status, 'ready');
  assert.equal(work(after, 'customer_review').status, 'blocked');
  assert.equal(primary(after).openRequirementCount, 1);
  assert.equal(primary(after).commercial.status, primary(before).commercial.status);
  const open = selectWorklist(after, 'queue');
  assert.ok(!ids(open).includes(work(after, 'collection').id));
  assert.ok(ids(selectWorklist(after, 'queue', 'status=complete')).includes(work(after, 'collection').id));
  assert.equal(after.openWorkItemCount, before.openWorkItemCount - 1);
});

test('awaited counsel stays unidentified without contact evidence and stays separate from the action owner', () => {
  const result = buildWorklist(act(verified(), 'request_customer_review'));
  const review = work(result, 'customer_review');
  assert.equal(review.status, 'waiting');
  assert.equal(review.owner.displayName, fixturePeople.internalReviewer);
  assert.equal(review.waitingOn.id, null);
  assert.ok(review.waitingOn.basis);
  assert.notEqual(review.waitingLabel, review.ownerLabel);
  assert.ok(review.allowedActions.some(action => action.intent === 'continue'));
  assert.equal(review.blockedReasons.length, 0);
  assert.ok(review.waitingReason);
});

test('resolving a requirement changes neither work-item completions nor the CRM or commercial outcome', () => {
  const before = buildWorklist(reviewed()), after = buildWorklist(act(reviewed(), 'resolve_requirement'));
  assert.equal(primary(before).openRequirementCount, 1);
  assert.equal(primary(after).openRequirementCount, 0);
  assert.deepEqual(primary(after).crm, primary(before).crm);
  assert.deepEqual(primary(after).commercial, primary(before).commercial);
  assert.deepEqual(after.workItems.map(row => [row.id, row.status, row.revision]), before.workItems.map(row => [row.id, row.status, row.revision]));
  assert.equal(after.openWorkItemCount, before.openWorkItemCount);
  assert.equal(selectWorklist(after, 'queue', 'scope=all&status=all').total, worklistFixture.workItems.length);
  assert.equal(selectWorklist(after, 'queue', 'status=complete').total, 4);
});

test('replacements reopen inspection and customer review but not the already-received collection output', () => {
  const prior = reviewed();
  const next = act(prior, 'obtain_document', {document: {...prior.document, version: `${prior.document.version}-replacement`, currentVersion: `${prior.document.version}-replacement`}});
  const result = buildWorklist(next);
  assert.equal(work(result, 'collection').status, 'complete');
  assert.equal(work(result, 'inspection').status, 'ready');
  assert.equal(work(result, 'customer_review').status, 'blocked');
  assert.equal(primary(result).openRequirementCount, 1);
});

test('the next renewal step moves to its exact work item, then to requirement resolution', () => {
  for (const [state, kind] of [[initialState, 'plan_review'], [authorize(), 'collection'], [received(), 'inspection'], [verified(), 'customer_review']]) {
    const result = buildWorklist(state);
    assert.equal(primary(result).nextStep.selectionKey, work(result, kind).selectionKey);
  }
  const result = buildWorklist(reviewed());
  assert.equal(primary(result).nextStep.selectionKey, result.requirements.find(item => item.renewalId === first.id).selectionKey);
});

test('unknown owners and self-review expose blocked reasons instead of offering a permitted execution action', () => {
  for (const owner of ['', fixturePeople.internalReviewer]) {
    const result = buildWorklist({...initialState, plan: {...initialState.plan, owner}});
    const plan = work(result, 'plan_review');
    assert.equal(plan.status, 'blocked');
    assert.equal(plan.canAct, false);
    assert.equal(plan.allowedActions.length, 0);
    assert.ok(plan.blockedReasons.length);
  }
});

test('illustrative resources never offer workflow commands or manufacture state on inspection', () => {
  const result = data(), before = structuredClone(result);
  for (const renewal of result.renewals.filter(row => row.mode === 'illustrative')) {
    for (const record of [renewal, ...(renewal.requirements || []), ...renewal.workItems]) {
      assert.equal(record.allowedActions.length, 0);
      assert.ok(record.blockedReasons.some(reason => reason.code === 'illustrative_only'));
      assert.equal(resolveWorklistSelection(result, record.selectionKey).resource.id, record.id);
    }
  }
  assert.deepEqual(result, before);
});

test('query state round-trips all filters, sort, direction, response and exact selection without losing unrelated parameters', () => {
  const selected = work(data(), 'inspection').selectionKey;
  const search = updateWorklistSearch('context=kept', {q: 'terms & review', scope: 'all', owner: worklistPeople[0].id, stage: 'negotiation', attention: ['blocked', 'unassigned'], sort: 'changed', direction: 'asc', status: 'all', response: 'truncated', limit: 'all', selection: selected});
  const parsed = readWorklistLocation(search, 'queue');
  assert.equal(parsed.q, 'terms & review');
  assert.equal(parsed.scope, 'all');
  assert.deepEqual(parsed.attention, ['blocked', 'unassigned']);
  assert.equal(parsed.selection, selected);
  assert.equal(parsed.sort, 'changed');
  assert.equal(parsed.direction, 'asc');
  assert.equal(parsed.status, 'all');
  assert.equal(parsed.expanded, true);
  const href = worklistHref('queue', search);
  assert.deepEqual(readWorklistLocation(new URL(href, 'http://local.test').search, 'queue'), parsed);
  const cleared = updateWorklistSearch(search, {q: null, owner: null, stage: null, attention: []});
  assert.equal(cleared.get('context'), 'kept');
  assert.equal(cleared.get('selection'), selected);
  assert.equal(cleared.get('sort'), 'changed');
  assert.equal(cleared.has('attention'), false);
});

test('invalid sorts and facets fall back safely while invalid selections never open a substitute record', () => {
  const parsed = readWorklistLocation('sort=due&direction=bad&attention=missing&response=bad', 'renewals');
  assert.equal(parsed.sort, 'days');
  assert.equal(parsed.direction, 'asc');
  assert.deepEqual(parsed.attention, []);
  assert.equal(parsed.response, 'normal');
  assert.equal(parsed.invalid, true);
  const result = selectWorklist(data(), 'renewals', 'selection=requirement%3Amissing');
  assert.equal(result.selection.status, 'not_found');
  assert.ok(!result.selection.resource);
});

test('selection survives filtering and truncation with an explicit outside-results state', () => {
  const selected = primary(data()).selectionKey;
  const search = new URLSearchParams({scope: 'all', owner: second.ownerId, selection: selected});
  const filtered = selectWorklist(data(), 'renewals', search);
  assert.equal(filtered.total, 1);
  assert.equal(filtered.selection.resource.id, first.id);
  assert.equal(filtered.selection.visible, false);
  assert.equal(selectWorklist(data(), 'renewals', updateWorklistSearch(search, {owner: null})).selection.visible, true);
  const truncated = selectWorklist(data(), 'renewals', new URLSearchParams({scope: 'all', response: 'truncated', selection: selected}));
  assert.equal(truncated.selection.visible, false);
});

test('truncation follows filtering and sorting, retains full-scope counts and has a URL-restored expansion', () => {
  const limited = selectWorklist(data(), 'renewals', 'scope=all&response=truncated&sort=days');
  assert.equal(limited.total, 4);
  assert.deepEqual(ids(limited), [second.id, third.id]);
  assert.equal(limited.remaining, 2);
  assert.equal(limited.truncated, true);
  assert.equal(limited.facets.find(facet => facet.id === 'due-soon').count, 1);
  const all = selectWorklist(data(), 'renewals', 'scope=all&response=truncated&limit=all');
  assert.equal(all.rows.length, 4);
  assert.equal(all.truncated, false);
  assert.deepEqual(all.facets, limited.facets);
  const none = selectWorklist(data(), 'renewals', 'response=truncated&q=no-record');
  assert.equal(none.total, 0);
  assert.equal(none.truncated, false);
});

test('not-permitted, error and loading responses withhold rows, facets, counts and direct selection', () => {
  for (const example of worklistResponseExamples.filter(item => item.status !== 'ready')) {
    for (const view of ['renewals', 'queue']) {
      const result = selectWorklist(data(), view, new URLSearchParams({response: example.id, selection: primary(data()).selectionKey}));
      assert.equal(result.status, example.status);
      assert.equal(result.rows, null);
      assert.equal(result.facets, null);
      assert.equal(result.total, null);
      assert.equal(result.selection, null);
      assert.equal(result.ownerOptions, undefined);
      assert.equal(result.asOf, undefined);
    }
  }
  assert.equal(selectWorklist(data(), 'renewals', 'response=denied').response.code, 403);
});

test('each walkthrough deep link opens its own item rather than whichever action is next', () => {
  for (const state of [initialState, received(), reviewed()]) {
    const result = buildWorklist(state);
    for (const item of result.workItems.filter(row => row.mode === 'walkthrough')) {
      const href = item.allowedActions[0].href;
      const url = new URL(href, 'http://local.test');
      const entry = resolveWorklistEntry(result, url.pathname, url.search);
      assert.equal(entry.id, item.id);
      assert.equal(entry.step, worklistFixture.workItems.find(record => record.id === item.id).step);
    }
    const requirement = result.requirements.find(row => row.renewalId === first.id);
    const url = new URL(requirement.allowedActions[0].href, 'http://local.test');
    assert.equal(url.pathname, `${renewalFixture.paths.root}/readiness`);
    const selection = selectReadiness(advanceReadinessJournal(null, state), url.search).selection;
    assert.equal(selection.kind, 'requirement');
    assert.equal(selection.resource.id, requirement.id);
    assert.equal(resolveWorklistEntry(result, renewalFixture.paths.workplan, url.search).step, 4);
  }
  assert.equal(resolveWorklistEntry(data(), renewalFixture.paths.workplan, '?workItem=missing').status, 'not_found');
  assert.equal(resolveWorklistEntry(data(), renewalFixture.paths.workplan, `?workItem=${worklistFixture.workItems.at(-1).id}`).status, 'not_found');
});

test('workflow entry keeps the entire originating worklist URL and restricts return paths to the two lists', () => {
  const item = work(data(), 'inspection');
  const returnTo = worklistHref('queue', new URLSearchParams({owner: item.ownerId, sort: 'changed', selection: item.selectionKey}));
  const href = worklistActionHref(item.allowedActions[0], returnTo);
  const url = new URL(href, 'http://local.test');
  const entry = resolveWorklistEntry(data(), url.pathname, url.search);
  assert.equal(entry.returnTo, returnTo);
  for (const invalid of ['//outside.test', 'https://outside.test', '/queue/other', '/renewals/other', '/queue#fragment']) assert.equal(safeWorklistReturn(invalid), '/queue');
  assert.equal(safeWorklistReturn(returnTo), returnTo);
});

test('last-change ages and resource revisions advance on actual events, not duplicate commands or reads', () => {
  const start = data();
  const approved = authorize();
  const after = buildWorklist(approved);
  assert.ok(Number(primary(after).revision) > Number(primary(start).revision));
  assert.equal(primary(after).lastChangedAt, approved.history.at(-1).at);
  assert.ok(Date.parse(after.asOf) >= Date.parse(primary(after).lastChangedAt));
  const duplicate = act(approved, 'approve_plan');
  assert.deepEqual(buildWorklist(duplicate), after);
  assert.deepEqual(buildWorklist(approved), after);
});

test('walkthrough commands ignore a device timestamp and illustrative snapshots never age', () => {
  const before = data();
  const next = transitionWalkthrough(initialState, {type: 'request_review', at: '2027-03-01T12:00:00.000Z'});
  const after = buildWorklist(next);
  assert.equal(next.history[0].at, renewalFixture.workflowClockStart);
  assert.equal(after.asOf, renewalFixture.workflowClockStart);
  const approved = transitionWalkthrough(next, {type: 'approve_plan', at: '2027-03-01T12:00:00.000Z'});
  assert.equal(Date.parse(buildWorklist(approved).asOf) - Date.parse(after.asOf), 60000);
  const future = buildWorklist(act(initialState, 'request_review', {at: '2027-03-01T12:00:00.000Z'}));
  for (const result of [after, future]) {
    assert.deepEqual(result.renewals.filter(row => row.mode === 'illustrative'), before.renewals.filter(row => row.mode === 'illustrative'));
    assert.deepEqual(result.workItems.filter(row => row.mode === 'illustrative'), before.workItems.filter(row => row.mode === 'illustrative'));
    for (const row of result.workItems.filter(row => row.mode === 'illustrative')) {
      assert.equal(row.overdue, false);
      assert.equal(row.dueSoon, false);
      assert.equal(row.lastChangedValue, null);
      assert.equal(row.lastChangedAt, null);
    }
  }
  assert.equal(future.renewals.find(row => row.id === second.id).daysLeft, 8);
});

test('the default scope and queue badge count only walkthrough records, with separate scope counts', () => {
  const result = data();
  assert.equal(result.openWorkItemCount, 4);
  assert.deepEqual(ids(selectWorklist(result)), ['thornfield-a17']);
  const queue = selectWorklist(result, 'queue');
  assert.deepEqual(queue.scopes.map(scope => [scope.id, scope.count]), [['walkthrough', 4], ['illustrative', 3], ['all', 7]]);
  assert.deepEqual(queue.facets.map(facet => [facet.id, facet.count]), [['actionable', 1], ['blocked', 3], ['waiting', 0], ['due-soon', 0], ['unassigned', 0]]);
  assert.deepEqual(ids(selectWorklist(result, 'renewals', 'scope=illustrative')), ['calder-renewal', 'ashbourne-renewal', 'mercer-renewal']);
  const onlyUnassigned = selectWorklist(result, 'queue', 'scope=illustrative&owner=unassigned');
  assert.deepEqual(ids(onlyUnassigned), ['work-mercer-owner']);
  assert.deepEqual(onlyUnassigned.scopes.map(scope => scope.count), [0, 1, 1]);
  const outputs = selectWorklist(buildWorklist(received()), 'queue', 'status=complete');
  assert.deepEqual(outputs.scopes.map(scope => scope.count), [2, 0, 2]);
});

test('the queue defaults to due date and breaks every sort tie by readiness, step, then ID', () => {
  const initial = selectWorklist(data(), 'queue');
  assert.equal(initial.location.sort, 'due');
  assert.deepEqual(ids(initial), ['work-plan-review-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms', 'task-obtain-ai-terms']);
  for (const direction of ['asc', 'desc']) {
    const tied = {...data(), workItems: data().workItems.map(row => ({...row, dueValue: null, daysLeft: null, openBlockerCount: null, lastChangedValue: null})).reverse()};
    for (const sort of ['due', 'days', 'blockers', 'changed']) {
      assert.deepEqual(ids(selectWorklist(tied, 'queue', `scope=all&sort=${sort}&direction=${direction}`)), [
        'work-plan-review-ai-terms', 'work-ashbourne-package', 'work-inspect-ai-terms', 'work-review-ai-terms', 'work-calder-exception', 'work-mercer-owner', 'task-obtain-ai-terms',
      ]);
    }
  }
});

test('renewal next steps expose their action owner and awaited party independently of the renewal owner', () => {
  const submitted = primary(buildWorklist(act(initialState, 'request_review')));
  assert.equal(submitted.ownerLabel, fixturePeople.accountOwner);
  assert.equal(submitted.nextStep.ownerLabel, fixturePeople.internalReviewer);
  assert.equal(submitted.nextStep.waitingOn, null);
  const waiting = primary(buildWorklist(act(verified(), 'request_customer_review')));
  assert.equal(waiting.nextStep.ownerLabel, fixturePeople.internalReviewer);
  assert.equal(waiting.nextStep.waitingOn.id, null);
  assert.equal(waiting.nextStep.waitingOn, worklistFixture.workItems[3].awaitedParty);
  const resolution = primary(buildWorklist(reviewed())).nextStep;
  assert.equal(resolution.ownerLabel, fixturePeople.internalReviewer);
});

test('submitted self-review is rejected by shared workflow eligibility and the queue projection', () => {
  const submitted = act(initialState, 'request_review');
  const state = {...submitted, plan: {...submitted.plan, owner: fixturePeople.internalReviewer}};
  const d = derive(state), item = work(buildWorklist(state), 'plan_review');
  assert.equal(d.isReviewerEligible, false);
  assert.equal(d.canApprovePlan, false);
  assert.equal(item.canAct, false);
  assert.equal(item.workflowAction, null);
  assert.equal(item.blockedReasons[0].reason, d.planReviewBlockedReason);
  const refused = act(state, 'approve_plan');
  assert.equal(refused.plan, state.plan);
  assert.equal(refused.history, state.history);
  assert.equal(refused.error, d.planReviewBlockedReason);
  assert.equal(derive(submitted).canApprovePlan, true);
  assert.equal(work(buildWorklist(submitted), 'plan_review').workflowAction.command, 'approve_plan');
});

test('saving a changed collection plan advances its revision and change sort without completing it', () => {
  const approved = authorize(), before = buildWorklist(approved);
  const edited = act(approved, 'save_plan', {title: `${approved.plan.title} with source context`, owner: fixturePeople.otherAccountOwner});
  const after = buildWorklist(edited), collection = work(after, 'collection');
  assert.equal(collection.status, 'blocked');
  assert.equal(collection.title, edited.plan.title);
  assert.equal(collection.ownerLabel, fixturePeople.otherAccountOwner);
  assert.equal(Number(collection.revision), Number(work(before, 'collection').revision) + 1);
  assert.equal(collection.lastChangedAt, edited.history.at(-1).at);
  assert.ok(collection.lastChangedValue > work(before, 'collection').lastChangedValue);
  assert.equal(selectWorklist(after, 'queue', 'sort=changed').rows[0].id, 'work-plan-review-ai-terms');
  assert.deepEqual(ids(selectWorklist(after, 'queue', 'sort=changed')), ['work-plan-review-ai-terms', 'task-obtain-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms']);
  assert.deepEqual(buildWorklist(act(edited, 'save_plan', {title: edited.plan.title})), after);
});

test('collection and inspection have different exact destinations with their own prerequisites and outputs', () => {
  const start = data(), collection = work(start, 'collection'), inspection = work(start, 'inspection');
  assert.notEqual(collection.entryTargetId, inspection.entryTargetId);
  for (const [state, expectedCollection, expectedInspection] of [
    [initialState, null, null], [authorize(), 'obtain_document', null], [received(), 'inspect_document', 'verify_document'], [verified(), 'inspect_document', 'inspect_document'],
  ]) {
    const result = buildWorklist(state);
    for (const [kind, command] of [['collection', expectedCollection], ['inspection', expectedInspection]]) {
      const item = work(result, kind), url = new URL(item.allowedActions[0].href, 'http://local.test');
      const entry = resolveWorklistEntry(result, url.pathname, url.search);
      assert.equal(url.hash, `#${entry.targetId}`);
      assert.equal(entry.resource.workflowAction?.command ?? null, command);
      assert.equal(entry.resource.id, item.id);
      if (!command) assert.ok(entry.resource.blockedReasons.length > 0);
      if (item.status === 'complete') assert.ok(entry.resource.output);
    }
  }
});

test('waiting reasons are separate from blocking prerequisites for both scopes', () => {
  const result = buildWorklist(act(verified(), 'request_customer_review'));
  for (const row of result.workItems.filter(row => row.status === 'waiting')) {
    assert.ok(row.waitingReason);
    assert.ok(row.waitingOn);
    assert.equal(row.blockedReasons.filter(reason => reason.code !== 'illustrative_only').length, 0);
    assert.ok(!row.blockedLabel.includes(row.waitingReason));
  }
});

test('most urgent prioritizes blocking requirements before kind and ID, ignoring any old priority', () => {
  const fixture = structuredClone(worklistFixture);
  const commercial = fixture.requirements[1], decision = fixture.requirements[2];
  commercial.blocking = false; commercial.priority = -1; decision.priority = 100;
  assert.equal(buildWorklist(initialState, fixture).renewals[1].urgentRequirement.id, decision.id);
  commercial.blocking = true;
  assert.equal(buildWorklist(initialState, fixture).renewals[1].urgentRequirement.id, commercial.id);
  commercial.status = 'resolved';
  assert.equal(buildWorklist(initialState, fixture).renewals[1].urgentRequirement.id, decision.id);
});

test('a supplied fixture owns all principal IDs, reviewer identity, paths and owner filter options', () => {
  const fixture = structuredClone(worklistFixture);
  fixture.people = fixture.people.map(owner => ({...owner, id: `${owner.id}-copy`}));
  fixture.walkthrough.reviewerId = `${fixture.walkthrough.reviewerId}-copy`;
  fixture.walkthrough.paths = Object.fromEntries(Object.entries(fixture.walkthrough.paths).map(([name, path]) => [name, path.replace('/thornfield', '/fixture-copy')]));
  for (const row of [...fixture.renewals, ...fixture.requirements, ...fixture.workItems]) if (row.ownerId) row.ownerId += '-copy';
  const result = buildWorklist(act(initialState, 'request_review'), fixture);
  const queue = selectWorklist(result, 'queue', 'scope=all');
  assert.ok(queue.ownerOptions.length > 0);
  assert.ok(queue.ownerOptions.every(owner => owner.id.endsWith('-copy')));
  const reviewer = fixture.people.find(owner => owner.id === fixture.walkthrough.reviewerId);
  assert.deepEqual(ids(selectWorklist(result, 'queue', `owner=${reviewer.id}`)), ['work-plan-review-ai-terms']);
  for (const resource of [primary(result), ...result.requirements.filter(row => row.renewalId === fixture.walkthrough.renewalId), ...result.workItems.filter(row => row.mode === 'walkthrough')]) {
    assert.ok(resource.allowedActions.every(action => action.href.startsWith(fixture.walkthrough.paths.root)));
  }
  const item = work(result, 'inspection'), url = new URL(item.allowedActions[0].href, 'http://local.test');
  assert.equal(resolveWorklistEntry(result, url.pathname, url.search).id, item.id);
  fixture.walkthrough.reviewerId = fixture.people[0].id;
  assert.equal(work(buildWorklist(act(initialState, 'request_review'), fixture), 'plan_review').canAct, false);
});

test('legacy renewal summary records agree with snapshot unknowns and known day counts', () => {
  const result = data();
  for (const record of renewalRecords) {
    const row = result.renewals.find(item => item.accountId === record.id);
    assert.equal(row.owner?.displayName ?? null, record.owner);
    assert.equal(row.daysLeft, record.days);
  }
});

test('invalid direction, status, scope and limit values are noticed and clear removes invalid settings only', () => {
  for (const [view, query, field] of [
    ['renewals', 'direction=sideways', 'direction'], ['queue', 'status=waiting', 'status'],
    ['queue', 'limit=2', 'limit'], ['renewals', 'sort=due', 'sort'], ['queue', 'scope=other', 'scope'],
  ]) {
    const search = `${query}&q=missing&selection=renewal%3Athornfield-a17&context=kept`;
    assert.equal(readWorklistLocation(search, view).invalid, true);
    const cleared = clearWorklistSearch(search, view);
    assert.equal(cleared.has(field), false);
    assert.equal(selectWorklist(data(), view, cleared).invalid, false);
    assert.equal(cleared.get('selection'), 'renewal:thornfield-a17');
    assert.equal(cleared.get('context'), 'kept');
  }
  const validSort = clearWorklistSearch('sort=changed&direction=desc&limit=all&owner=missing', 'queue');
  assert.equal(validSort.get('sort'), 'changed');
  assert.equal(validSort.get('direction'), 'desc');
  assert.equal(validSort.get('limit'), 'all');
  assert.equal(validSort.has('owner'), false);
});

test('queue readiness and requirement visibility use relationships among displayed rows, including truncation', () => {
  for (const selection of ['renewal:thornfield-a17', 'requirement:req-ai-counsel']) {
    const result = selectWorklist(data(), 'queue', new URLSearchParams({selection, response: 'truncated'}));
    assert.equal(result.selection.visible, true);
    assert.equal(result.selection.rowKey, 'work-item:work-plan-review-ai-terms');
    assert.equal(selectWorklist(data(), 'queue', new URLSearchParams({selection, scope: 'illustrative'})).selection.visible, false);
  }
  const unlinked = selectWorklist(data(), 'queue', 'scope=illustrative&selection=requirement%3Areq-calder-decision-owner');
  assert.equal(unlinked.selection.visible, false);
  const linked = selectWorklist(data(), 'queue', 'scope=illustrative&selection=requirement%3Areq-calder-exception');
  assert.equal(linked.selection.visible, true);
});

test('account palette links preserve the current worklist or its return path without dropping filters', () => {
  const query = 'scope=illustrative&owner=unassigned&sort=changed&direction=asc&attention=blocked&limit=all';
  for (const path of ['/renewals', '/queue']) {
    const expected = worklistHref(path.slice(1), query, {selection: 'renewal:mercer-renewal'});
    assert.equal(worklistAccountHref(path, query, 'renewal:mercer-renewal'), expected);
    assert.equal(worklistAccountHref(renewalFixture.paths.evidence, new URLSearchParams({returnTo: `${path}?${query}`}), 'renewal:mercer-renewal'), expected);
  }
});
