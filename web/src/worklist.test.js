import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState, transition} from './workflow.js';
import {fixturePeople, renewalFixture, worklistFixture, worklistPeople, worklistResponseExamples} from './renewalFixtures.js';
import {
  buildWorklist, daysUntil, formatWorklistTime, readWorklistLocation, resolveWorklistEntry,
  resolveWorklistSelection, safeWorklistReturn, selectWorklist, updateWorklistSearch, worklistActionHref, worklistHref,
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
  fixture.renewals[3].lastChangedAt = literal;
  const result = buildWorklist(initialState, fixture);
  assert.equal(result.renewals[3].lastChangedValue, null);
  assert.equal(result.renewals[3].lastChangedTime, formatWorklistTime(literal));
});

test('renewal sorting supports both directions and always places unknown values last', () => {
  const expected = {
    days: [[second.id, third.id, first.id, unknown.id], [first.id, third.id, second.id, unknown.id]],
    blockers: [[third.id, first.id, second.id, unknown.id], [second.id, first.id, third.id, unknown.id]],
    changed: [[second.id, first.id, third.id, unknown.id], [third.id, first.id, second.id, unknown.id]],
  };
  for (const [sort, orders] of Object.entries(expected)) {
    for (const [index, direction] of ['asc', 'desc'].entries()) assert.deepEqual(ids(selectWorklist(data(), 'renewals', `sort=${sort}&direction=${direction}`)), orders[index]);
  }
});

test('equal sort values have stable ID ordering and never mutate the source collection', () => {
  const result = data();
  const tied = {...result, renewals: result.renewals.map(row => ({...row, daysLeft: 8})).reverse()};
  const before = structuredClone(tied);
  const expected = tied.renewals.map(row => row.id).sort();
  assert.deepEqual(ids(selectWorklist(tied, 'renewals', 'sort=days&direction=asc')), expected);
  assert.deepEqual(ids(selectWorklist(tied, 'renewals', 'sort=days&direction=desc')), expected);
  assert.deepEqual(tied, before);
});

test('queue sorting uses renewal days, renewal blockers, work due times and individual change times', () => {
  for (const sort of ['days', 'blockers', 'changed', 'due']) {
    const field = {days: 'daysLeft', blockers: 'openBlockerCount', changed: 'lastChangedValue', due: 'dueValue'}[sort];
    for (const direction of ['asc', 'desc']) {
      const result = selectWorklist(data(), 'queue', `sort=${sort}&direction=${direction}`);
      const values = result.rows.map(row => row[field]);
      const known = values.filter(value => value !== null);
      assert.deepEqual(values.slice(0, known.length), known);
      assert.deepEqual(known, [...known].sort((a, b) => direction === 'asc' ? a - b : b - a));
    }
  }
});

test('search, owner and stage intersect while unknown and unassigned are explicit filters', () => {
  const result = data();
  const filtered = selectWorklist(result, 'renewals', new URLSearchParams({q: second.accountName.toUpperCase(), owner: second.ownerId, stage: second.crm.stage}));
  assert.deepEqual(ids(filtered), [second.id]);
  assert.equal(selectWorklist(result, 'renewals', `owner=${third.ownerId}&stage=negotiation`).total, 0);
  assert.deepEqual(ids(selectWorklist(result, 'renewals', 'owner=unassigned&stage=unknown')), [unknown.id]);
  assert.equal(selectWorklist(result, 'renewals', 'q=%20%20').total, result.renewals.length);
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
  const result = selectWorklist(data(), 'renewals', 'attention=blockers&attention=due-soon');
  assert.equal(result.unit, 'renewals');
  assert.equal(result.facets.find(facet => facet.id === 'blockers').count, 2);
  assert.equal(result.facets.find(facet => facet.id === 'due-soon').count, 3);
  assert.equal(result.total, 3);
  assert.equal(result.facetScopeCount, 4);
  const scoped = selectWorklist(data(), 'renewals', `stage=${second.crm.stage}&attention=blockers&attention=due-soon`);
  assert.equal(scoped.facetScopeCount, 2);
  assert.equal(scoped.total, 2);
  assert.ok(scoped.facets.every(facet => facet.count <= 2 && facet.unit === 'renewals'));
});

test('queue facets count work items and include overlapping due, waiting and blocked items only once in results', () => {
  const result = selectWorklist(data(), 'queue', 'attention=blocked&attention=due-soon');
  const blocked = result.facets.find(facet => facet.id === 'blocked').count;
  const due = result.facets.find(facet => facet.id === 'due-soon').count;
  assert.equal(result.unit, 'work items');
  assert.equal(blocked, 5);
  assert.equal(due, 2);
  assert.equal(result.total, 6);
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
  assert.ok(review.blockedReasons.some(reason => reason.code === 'customer_decision'));
});

test('resolving a requirement changes neither work-item completions nor the CRM or commercial outcome', () => {
  const before = buildWorklist(reviewed()), after = buildWorklist(act(reviewed(), 'resolve_requirement'));
  assert.equal(primary(before).openRequirementCount, 1);
  assert.equal(primary(after).openRequirementCount, 0);
  assert.deepEqual(primary(after).crm, primary(before).crm);
  assert.deepEqual(primary(after).commercial, primary(before).commercial);
  assert.deepEqual(after.workItems.map(row => [row.id, row.status, row.revision]), before.workItems.map(row => [row.id, row.status, row.revision]));
  assert.equal(after.openWorkItemCount, before.openWorkItemCount);
  assert.equal(selectWorklist(after, 'queue', 'status=all').total, worklistFixture.workItems.length);
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
  const search = updateWorklistSearch('context=kept', {q: 'terms & review', owner: worklistPeople[0].id, stage: 'negotiation', attention: ['blocked', 'unassigned'], sort: 'changed', direction: 'asc', status: 'all', response: 'truncated', limit: 'all', selection: selected});
  const parsed = readWorklistLocation(search, 'queue');
  assert.equal(parsed.q, 'terms & review');
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
  const search = new URLSearchParams({owner: second.ownerId, selection: selected});
  const filtered = selectWorklist(data(), 'renewals', search);
  assert.equal(filtered.total, 1);
  assert.equal(filtered.selection.resource.id, first.id);
  assert.equal(filtered.selection.visible, false);
  assert.equal(selectWorklist(data(), 'renewals', updateWorklistSearch(search, {owner: null})).selection.visible, true);
  const truncated = selectWorklist(data(), 'renewals', new URLSearchParams({response: 'truncated', selection: selected}));
  assert.equal(truncated.selection.visible, false);
});

test('truncation follows filtering and sorting, retains full-scope counts and has a URL-restored expansion', () => {
  const limited = selectWorklist(data(), 'renewals', 'response=truncated&sort=days');
  assert.equal(limited.total, 4);
  assert.deepEqual(ids(limited), [second.id, third.id]);
  assert.equal(limited.remaining, 2);
  assert.equal(limited.truncated, true);
  assert.equal(limited.facets.find(facet => facet.id === 'due-soon').count, 3);
  const all = selectWorklist(data(), 'renewals', 'response=truncated&limit=all');
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
    assert.equal(resolveWorklistEntry(result, url.pathname, url.search).step, 4);
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
