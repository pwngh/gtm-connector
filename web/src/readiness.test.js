import test from 'node:test';
import assert from 'node:assert/strict';
import {fixturePeople, initialState, readinessFixture, renewalEvidence, worklistFixture, walkthroughDecisions} from './renewalFixtures.js';
import {derive, transitionWalkthrough} from './workflow.js';
import {buildWorklist, resolveWorklistEntry, worklistActionHref} from './worklist.js';
import {
  advanceReadinessJournal, evaluateReadiness, formatReadinessTime, readReadinessJournal,
  readReadinessLocation, readinessActionHref, readinessHref, readinessSearch, readinessSelectionKey,
  safeReadinessReturn, selectReadiness,
} from './readiness.js';

const act = (state, type, fields = {}) => transitionWalkthrough(state, {type, ...fields});
const chain = (...actions) => actions.reduce((state, action) => act(state, typeof action === 'string' ? action : action.type, typeof action === 'string' ? {} : action), initialState);
const approved = () => chain('request_review', 'approve_plan');
const received = () => act(approved(), 'obtain_document');
const verified = () => act(received(), 'verify_document');
const requested = () => act(verified(), 'request_customer_review');
const decided = () => act(requested(), 'record_customer_approval', {reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
const resolved = () => act(decided(), 'resolve_requirement');
const journal = (state = initialState) => advanceReadinessJournal(null, state);
const byId = (model, id) => model.checks.find(check => check.id === id);
const codes = check => check.blockedReasons.map(reason => reason.code);
const ids = rows => rows.map(row => row.id);
const localURL = href => new URL(href, 'http://local.test');

test('readiness is a pure API-shaped projection with string identities and independent actions and blockers', () => {
  const stateBefore = structuredClone(initialState), fixtureBefore = structuredClone(readinessFixture);
  const result = evaluateReadiness(initialState);
  assert.deepEqual(initialState, stateBefore);
  assert.deepEqual(readinessFixture, fixtureBefore);
  assert.equal(typeof result.id, 'string');
  assert.equal(typeof result.revision, 'string');
  for (const requirement of result.requirements) {
    assert.equal(typeof requirement.id, 'string');
    assert.equal(typeof requirement.revision, 'string');
    assert.ok(Array.isArray(requirement.allowedActions));
    assert.ok(Array.isArray(requirement.blockedReasons));
  }
  for (const check of result.checks) {
    assert.equal(typeof check.id, 'string');
    assert.ok(check.inputVersions.length);
    assert.ok(Array.isArray(check.allowedActions));
    assert.ok(check.allowedActions.every(action => !('reason' in action)));
    assert.ok(check.blockedReasons.every(reason => reason.name && reason.reason && reason.code && reason.action));
    assert.ok(check.inputVersions.every(input => input.id === null || typeof input.id === 'string'));
    assert.ok(check.inputVersions.every(input => input.revision === null || typeof input.revision === 'string'));
  }
});

test('every transition permission follows derive throughout the walkthrough, including reopening and plan edits', () => {
  const completed = resolved();
  const states = [initialState, act(initialState, 'request_review'), approved(), received(), verified(), requested(), decided(), completed,
    act(completed, 'reopen_requirement', {reason: initialState.plan.specifications}),
    act(completed, 'save_plan', {title: initialState.plan.instructions}),
    act(completed, 'save_plan', {specifications: initialState.plan.instructions}),
  ];
  for (const state of states) {
    const d = derive(state), model = evaluateReadiness(state);
    for (const definition of readinessFixture.checks.filter(item => item.kind === 'transition')) {
      const check = byId(model, definition.id);
      assert.equal(check.allowed, Boolean(d[definition.allowedFlag]), definition.id);
      assert.equal(check.allowedActions.length, check.allowed ? 1 : 0);
      if (check.allowed) {
        assert.equal(check.result, 'passed');
        assert.equal(check.blockerCount, 0);
      }
    }
    for (const requirement of model.requirements) assert.ok(requirement.allowedActions.length <= 1);
  }
});

test('missing documents do not block submission but later transitions list every unmet gate by name', () => {
  const model = evaluateReadiness(initialState);
  assert.equal(byId(model, 'submit_plan').result, 'passed');
  assert.deepEqual(codes(byId(model, 'approve_plan')), ['plan_submission']);
  assert.deepEqual(codes(byId(model, 'verify_document')), ['plan_approval', 'document_receipt']);
  assert.deepEqual(codes(byId(model, 'request_customer_review')), ['plan_approval', 'document_receipt', 'document_inspection']);
  assert.deepEqual(codes(byId(model, 'record_customer_decision')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request']);
  assert.deepEqual(codes(byId(model, 'resolve_requirement')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'customer_approval']);
  for (const check of model.checks.filter(item => item.result === 'failed')) {
    assert.equal(check.blockerCount, check.blockedReasons.length);
    assert.equal(new Set(codes(check)).size, check.blockerCount);
  }
});

test('four check results keep unevaluated and inapplicable counts distinct from a passed zero', () => {
  const model = evaluateReadiness(approved());
  assert.deepEqual(new Set(model.checks.map(check => check.result)), new Set(['not_applicable', 'passed', 'failed', 'not_evaluated']));
  for (const check of model.checks) {
    assert.equal(check.blockerCount === null, ['not_evaluated', 'not_applicable'].includes(check.result));
    assert.equal(check.evaluatedAt === null, check.result === 'not_evaluated');
    if (check.result === 'passed') assert.equal(check.blockerCount, 0);
    if (check.result === 'failed') assert.ok(check.blockerCount > 0);
  }
});

test('independent-review refusal is shared for draft and submitted plans, including missing owners', () => {
  for (const base of [initialState, act(initialState, 'request_review')]) {
    for (const owner of [fixturePeople.internalReviewer, '']) {
      const state = {...base, plan: {...base.plan, owner}}, d = derive(state), model = evaluateReadiness(state);
      const check = byId(model, base.plan.status === 'in_review' ? 'approve_plan' : 'submit_plan');
      assert.equal(check.allowed, false);
      assert.equal(check.allowedActions.length, 0);
      assert.ok(check.blockedReasons.some(reason => reason.code === 'independent_reviewer' && reason.reason === d.planReviewBlockedReason));
      assert.equal(model.requirements[0].nextAction, null);
      if (!owner) assert.equal(model.requirements[0].nextStepOwner, null);
    }
  }
});

test('legacy verification and typed approval never manufacture independent version, authority, or citation evidence', () => {
  for (const state of [initialState, received(), verified(), decided(), resolved()]) {
    const model = evaluateReadiness(state);
    for (const check of model.checks.filter(item => item.kind === 'evidence')) {
      assert.equal(check.result, 'not_evaluated');
      assert.equal(check.blockerCount, null);
      assert.equal(check.allowed, null);
      assert.equal(check.evaluatedAt, null);
    }
    assert.equal(model.requirements[0].customerAuthorityLabel.toLowerCase(), 'unidentified');
    assert.equal(model.evidenceGapCount, 3);
    assert.ok(model.requirements[0].missing.some(item => item.code === 'current_version_basis'));
    assert.ok(model.requirements[0].missing.some(item => item.code === 'customer_authority'));
  }
  const state = decided(), model = evaluateReadiness(state);
  assert.equal(model.requirements[0].recordedDecision.text, state.customerReview.evidenceText);
  assert.equal(model.requirements[0].recordedDecision.recordedBy, state.customerReview.recordedBy);
  assert.equal(model.requirements[0].recordedDecision.documentVersion, state.document.version);
});

test('requirements separate the action owner, requirement owner, customer role, and awaited party', () => {
  const start = evaluateReadiness(initialState).requirements[0];
  assert.equal(start.nextStepOwner.displayName, fixturePeople.accountOwner);
  assert.equal(start.owner.displayName, fixturePeople.internalReviewer);
  assert.equal(start.waitingOn, null);
  const pending = evaluateReadiness(requested()).requirements[0];
  assert.equal(pending.nextStepOwner.displayName, fixturePeople.internalReviewer);
  assert.equal(pending.waitingOn.id, null);
  assert.equal(pending.waitingOn.basis, worklistFixture.workItems[3].awaitedParty.basis);
  assert.notEqual(pending.waitingLabel, pending.nextStepOwnerLabel);
  assert.equal(evaluateReadiness(decided()).requirements[0].waitingOn, null);
});

test('all permitted next steps open the precise work item or requirement without executing a command', () => {
  for (const state of [initialState, act(initialState, 'request_review'), approved(), received(), verified(), requested(), decided()]) {
    const before = structuredClone(state), model = evaluateReadiness(state), action = model.requirements[0].allowedActions[0];
    assert.ok(action.href);
    const url = localURL(action.href), entry = resolveWorklistEntry(buildWorklist(state), url.pathname, url.search);
    assert.equal(entry.status, 'ready');
    if (action.command === 'resolve_requirement') {
      assert.equal(entry.kind, 'requirement');
      assert.equal(entry.id, state.requirement.id);
      assert.equal(entry.step, 4);
    } else {
      assert.equal(entry.resource.workflowAction.command, action.command);
      assert.equal(url.hash, `#${entry.targetId}`);
    }
    assert.deepEqual(state, before);
  }
});

test('resolving the recorded requirement does not change CRM, commercial outcomes, or projected work-item completions', () => {
  const beforeState = decided(), afterState = act(beforeState, 'resolve_requirement');
  const before = evaluateReadiness(beforeState), after = evaluateReadiness(afterState);
  assert.equal(before.openRequirementCount, 1);
  assert.equal(after.openRequirementCount, 0);
  assert.deepEqual(after.crm, before.crm);
  assert.deepEqual(after.commercial, before.commercial);
  assert.deepEqual(buildWorklist(afterState).workItems.map(item => [item.id, item.status, item.revision]), buildWorklist(beforeState).workItems.map(item => [item.id, item.status, item.revision]));
  assert.equal(byId(after, 'resolve_requirement').result, 'not_applicable');
  assert.equal(after.requirements[0].allowedActions[0].command, 'reopen_requirement');
  assert.ok(after.requirements[0].missing.length > 0);
});

test('multiple requirement records keep their own states and do not borrow the executable requirement status', () => {
  const fixture = structuredClone(readinessFixture);
  fixture.requirements.push({...worklistFixture.requirements[1], renewalId: fixture.renewalId, binding: null, resolutionCriteria: [], citations: [], checkIds: [], evidenceCheckIds: []});
  const result = evaluateReadiness(resolved(), fixture);
  assert.equal(result.requirementCount, 2);
  assert.equal(result.openRequirementCount, 1);
  assert.equal(result.requirements[0].status, 'resolved');
  assert.equal(result.requirements[1].status, 'open');
  assert.equal(result.requirements[1].allowedActions.length, 0);
  assert.equal(result.requirements[1].nextAction, null);
});

test('a missing requirement collection is unknown, distinct from a recorded empty collection', () => {
  const unknown = evaluateReadiness(initialState, {...readinessFixture, requirementsRecorded: false});
  assert.equal(unknown.requirements, null);
  assert.equal(unknown.openRequirementCount, null);
  assert.equal(unknown.requirementCount, null);
  const empty = evaluateReadiness(initialState, {...readinessFixture, requirements: []});
  assert.deepEqual(empty.requirements, []);
  assert.equal(empty.openRequirementCount, 0);
  assert.equal(empty.requirementCount, 0);
});

test('unit citations preserve shared evidence exactly, with role bases and bounded receipt metadata', () => {
  const model = evaluateReadiness(initialState), requirement = model.requirements[0];
  assert.equal(requirement.citations.length, renewalEvidence.length);
  for (const [index, citation] of requirement.citations.entries()) {
    assert.equal(citation.unit.text, renewalEvidence[index].quote);
    assert.equal(citation.unit.speaker.name, renewalEvidence[index].speaker);
    assert.equal(citation.unit.speaker.role, renewalEvidence[index].role);
    assert.ok(citation.unit.speaker.basis);
    assert.equal(citation.unit.cardId, model.coverage.id);
    assert.equal(typeof citation.unit.n, 'string');
    assert.equal(citation.unit.partial, true);
    assert.ok(citation.unit.receipt.readAt);
  }
  const record = model.coverage.units.find(unit => unit.kind === 'record');
  assert.ok(record.fields.length);
  assert.equal(record.text, undefined);
  assert.equal(record.eventAt, null);
});

test('receipt and verification never close the source-card gaps or change its read times', () => {
  const original = evaluateReadiness(initialState).coverage;
  assert.deepEqual(original.gaps.map(gap => gap.code), ['calls_outside_records', 'slack_channel', 'neutralized']);
  for (const state of [received(), verified(), decided(), resolved()]) {
    const model = evaluateReadiness(state);
    assert.deepEqual(model.coverage, original);
    assert.equal(model.document.receivedAt, state.document.receivedAt);
    assert.equal(model.document.version, state.document.version);
  }
  const literal = renewalEvidence[0].time;
  assert.ok(formatReadinessTime(literal).startsWith(literal));
  assert.equal(evaluateReadiness(initialState).coverage.units[0].eventAt, literal);
});

test('evaluation times come from recorded walkthrough inputs and persist independently for each check', () => {
  const model = evaluateReadiness(received());
  assert.equal(model.evaluatedAt, received().history.at(-1).at);
  assert.ok(model.checks.filter(check => check.result !== 'not_evaluated').every(check => check.evaluatedAt === model.evaluatedAt));
  assert.equal(evaluateReadiness(initialState).evaluatedAt, readinessFixture.initialEvaluationAt);
  assert.equal(byId(model, 'verify_document').inputVersions.find(input => input.kind === 'document').version, received().document.version);
  const literal = readinessFixture.initialEvaluationAt.slice(0, -1);
  const custom = evaluateReadiness(initialState, {...readinessFixture, initialEvaluationAt: literal});
  assert.equal(custom.evaluatedAt, literal);
  assert.ok(formatReadinessTime(custom.evaluatedAt).startsWith(literal));
});

test('new walkthrough inputs retain the previous evaluation and identify changed requirements and cleared gates', () => {
  let state = approved(), prior = journal(state);
  const saved = structuredClone(prior.current);
  state = act(state, 'obtain_document');
  const next = advanceReadinessJournal(prior, state);
  assert.deepEqual(next.previous, saved);
  assert.notEqual(next.current.evaluationId, saved.evaluationId);
  assert.equal(next.changes.cause.id, state.history.at(-1).id);
  assert.equal(next.changes.cause.detail, state.history.at(-1).detail);
  assert.equal(next.changes.requirements[0].id, state.requirement.id);
  assert.ok(next.changes.requirements[0].cleared.some(reason => reason.code === 'document_receipt'));
  assert.ok(next.changes.checks.find(item => item.id === 'verify_document').cleared.some(reason => reason.code === 'document_receipt'));
  assert.deepEqual(prior.current, saved);
});

test('changes in document inputs are recorded even when an open requirement and its next action stay the same', () => {
  const first = received(), prior = journal(first);
  const replacement = act(first, 'obtain_document', {document: {...first.document, version: `${first.document.version}-replacement`, currentVersion: `${first.document.version}-replacement`}});
  const next = advanceReadinessJournal(prior, replacement);
  assert.equal(next.current.requirements[0].status, prior.current.requirements[0].status);
  assert.equal(next.current.requirements[0].nextAction.id, prior.current.requirements[0].nextAction.id);
  assert.ok(next.changes.requirements[0].differences.some(difference => difference.field === 'input:document'));
  assert.equal(next.previous.document.version, first.document.version);
});

test('reopening retains the old resolved result and reports the recorded reason without inventing an actor', () => {
  const state = resolved(), prior = journal(state);
  const opened = act(state, 'reopen_requirement', {reason: initialState.plan.specifications});
  const next = advanceReadinessJournal(prior, opened);
  assert.equal(next.previous.requirements[0].status, 'resolved');
  assert.equal(next.current.requirements[0].status, 'open');
  assert.equal(next.changes.cause.detail, opened.history.at(-1).detail);
  assert.equal(next.changes.cause.actor, null);
  assert.ok(next.changes.requirements[0].differences.some(difference => difference.field === 'status'));
  assert.ok(next.changes.requirements[0].added.some(reason => reason.code === 'customer_approval'));
});

test('duplicate commands, rejected commands, and unchanged reads do not manufacture evaluation revisions', () => {
  const state = approved(), prior = journal(state);
  assert.equal(advanceReadinessJournal(prior, state), prior);
  assert.equal(advanceReadinessJournal(prior, act(state, 'approve_plan')), prior);
  assert.equal(advanceReadinessJournal(prior, act(state, 'resolve_requirement')), prior);
  assert.equal(advanceReadinessJournal(prior, act(state, 'save_plan', {title: state.plan.title})), prior);
});

test('the evaluation journal survives reload, rejects broken storage, and never fabricates an older result', () => {
  const next = advanceReadinessJournal(journal(initialState), approved());
  const reloaded = readReadinessJournal(JSON.stringify(next));
  assert.deepEqual(reloaded, next);
  assert.equal(advanceReadinessJournal(reloaded, approved()), reloaded);
  for (const value of [null, '', '{', '{}', JSON.stringify({schema: '1', current: {}})]) assert.equal(readReadinessJournal(value), null);
  assert.equal(journal(resolved()).previous, null);
  assert.equal(journal(resolved()).current.requirements[0].status, 'resolved');
});

test('retained evaluations are snapshots, not live references to the source card or workflow', () => {
  const fixture = structuredClone(readinessFixture), state = structuredClone(initialState);
  const prior = advanceReadinessJournal(null, state, fixture), original = structuredClone(prior);
  fixture.card.units[0].text = renewalEvidence[0].context;
  fixture.card.revision = '2';
  fixture.requirements[0].resolutionCriteria.reverse();
  state.plan.owner = fixturePeople.otherAccountOwner;
  const next = advanceReadinessJournal(prior, state, fixture);
  assert.deepEqual(prior, original);
  assert.equal(next.previous.requirements[0].citations[0].unit.text, renewalEvidence[0].quote);
  assert.equal(next.current.coverage.units[0].text, renewalEvidence[0].context);
  assert.equal(next.changes.cause.id, null);
});

test('filtering counts transition checks once and does not sum requirements or evidence checks into them', () => {
  const source = journal(approved());
  const all = selectReadiness(source);
  assert.equal(all.transitionCount, readinessFixture.checks.filter(item => item.kind === 'transition').length);
  assert.equal(Object.values(all.counts).reduce((sum, count) => sum + count, 0), all.transitionCount);
  assert.equal(all.requirementCount, 1);
  assert.equal(all.evidenceChecks.length, 3);
  for (const result of Object.keys(all.counts)) {
    const view = selectReadiness(source, new URLSearchParams({checkState: result}));
    assert.equal(view.visibleChecks.length, all.counts[result]);
    assert.ok(view.visibleChecks.every(check => check.result === result));
    assert.deepEqual(view.counts, all.counts);
    assert.equal(view.evidenceChecks.length, all.evidenceChecks.length);
  }
});

test('blocker ordering is stable within result groups, while workflow ordering follows the input definitions', () => {
  const source = journal(initialState), before = structuredClone(source);
  const normal = selectReadiness(source), sorted = selectReadiness(source, 'order=blockers');
  assert.deepEqual(ids(normal.visibleChecks), readinessFixture.checks.filter(item => item.kind === 'transition').map(item => item.id));
  assert.equal(sorted.visibleChecks.at(-1).id, 'submit_plan');
  assert.deepEqual(ids(sorted.visibleChecks).slice(0, -1), ids(normal.visibleChecks).slice(1));
  assert.deepEqual(selectReadiness(source, 'order=blockers').counts, normal.counts);
  assert.deepEqual(source, before);
});

test('requirement filtering produces real empty views while keeping an explicitly selected resolved item inspectable', () => {
  const source = journal(resolved());
  const filtered = selectReadiness(source, new URLSearchParams({requirement: initialState.requirement.id}));
  assert.deepEqual(filtered.visibleRequirements, []);
  assert.equal(filtered.selection.outside, true);
  assert.equal(filtered.selectedRequirementOutside.id, initialState.requirement.id);
  const all = selectReadiness(source, 'requirements=all');
  assert.equal(all.visibleRequirements.length, 1);
  assert.equal(all.openRequirementCount, 0);
  assert.equal(selectReadiness(journal(), 'requirements=resolved').visibleRequirements.length, 0);
});

test('filter-hidden checks retain exact selection and evidence checks never borrow transition-filter membership', () => {
  const source = journal();
  const hidden = selectReadiness(source, 'check=submit_plan&checkState=failed');
  assert.equal(hidden.visibleChecks.length, 6);
  assert.equal(hidden.selectedCheckOutside.id, 'submit_plan');
  const evidence = selectReadiness(source, 'check=customer_authority&checkState=passed');
  assert.equal(evidence.selection.outside, false);
  assert.equal(evidence.selection.resource.id, 'customer_authority');
});

test('selection, filter, sort and origin round-trip through URL updates without discarding unrelated state', () => {
  const returnTo = '/queue?sort=changed&attention=blocked&selection=work-item%3Awork-inspect-ai-terms';
  const params = readinessSearch('context=kept', {requirement: initialState.requirement.id, requirements: 'all', checkState: 'failed', order: 'blockers', returnTo});
  const read = readReadinessLocation(params);
  assert.equal(read.requirement, initialState.requirement.id);
  assert.equal(read.order, 'blockers');
  assert.equal(read.checkState, 'failed');
  assert.equal(read.returnTo, returnTo);
  assert.deepEqual(readReadinessLocation(localURL(readinessHref(params)).search), read);
  const changed = readinessSearch(params, {check: 'verify_document'});
  assert.equal(changed.has('requirement'), false);
  assert.equal(changed.get('returnTo'), returnTo);
  assert.equal(changed.get('context'), 'kept');
  const citation = readinessSearch(changed, {card: readinessFixture.card.id, unit: '1'});
  assert.equal(citation.get('check'), 'verify_document');
  assert.equal(readinessSearch(citation, {check: 'submit_plan'}).has('unit'), false);
});

test('unknown, ambiguous and empty selections never open a substitute requirement, check, or unit', () => {
  const source = journal();
  for (const query of ['check=missing', 'check=', 'requirement=missing', 'requirement=', 'check=submit_plan&requirement=missing', 'check=submit_plan&check=verify_document']) {
    const selected = selectReadiness(source, query).selection;
    assert.equal(selected.status, 'not_found');
    assert.equal(selected.resource, undefined);
  }
  for (const query of ['card=&unit=', 'unit=1', `card=${readinessFixture.card.id}&unit=missing`, `card=missing&unit=1`, `card=${readinessFixture.card.id}&unit=1&unit=2`]) {
    assert.equal(selectReadiness(source, query).unitSelection.status, 'not_found');
  }
  const invalid = readReadinessLocation('order=bad&requirements=bad&checkState=bad&evaluation=bad');
  assert.equal(invalid.invalid, true);
  assert.equal(invalid.order, 'workflow');
  assert.equal(invalid.evaluation, 'current');
});

test('source inspection resolves by both card and number and preserves the parent selection', () => {
  const query = new URLSearchParams({requirement: initialState.requirement.id, card: readinessFixture.card.id, unit: '2'});
  const view = selectReadiness(journal(), query);
  assert.equal(view.selection.resource.id, initialState.requirement.id);
  assert.equal(view.unitSelection.resource.text, renewalEvidence[1].quote);
  assert.equal(view.focusSelection, view.unitSelection);
  assert.equal(readinessSelectionKey(view.unitSelection), `unit:${readinessFixture.card.id}:2`);
  const closed = selectReadiness(journal(), readinessSearch(query, {card: null, unit: null}));
  assert.equal(closed.unitSelection, null);
  assert.equal(closed.focusSelection, closed.selection);
});

test('not-evaluated examples withhold results and actions, never substituting zero blockers', () => {
  const source = journal(decided()), before = structuredClone(source), view = selectReadiness(source, 'evaluation=not-evaluated');
  assert.equal(view.evaluatedAt, null);
  assert.equal(view.nextAction, null);
  assert.equal(view.allowedActions.length, 0);
  assert.equal(view.blockedReasons.length, 0);
  for (const check of view.checks) {
    assert.equal(check.result, 'not_evaluated');
    assert.equal(check.blockerCount, null);
    assert.equal(check.allowed, null);
    assert.equal(check.evaluatedAt, null);
    assert.equal(check.allowedActions.length, 0);
  }
  assert.equal(view.requirements[0].missing, null);
  assert.equal(view.requirements[0].allowedActions.length, 0);
  assert.deepEqual(source, before);
});

test('a failed-refresh preview keeps the complete previous result and sealed card while withholding current actions', () => {
  const fixture = structuredClone(readinessFixture), first = advanceReadinessJournal(null, initialState, fixture);
  fixture.card.revision = '2';
  fixture.card.units[0].text = renewalEvidence[0].context;
  const next = advanceReadinessJournal(first, approved(), fixture);
  const stale = selectReadiness(next, 'evaluation=stale', fixture);
  assert.equal(stale.status, 'stale');
  assert.equal(stale.evaluationId, first.current.evaluationId);
  assert.equal(stale.evaluatedAt, first.current.evaluatedAt);
  assert.deepEqual(stale.coverage, first.current.coverage);
  assert.equal(stale.nextAction, null);
  assert.ok(stale.checks.every(check => check.allowed === null && !check.allowedActions.length));
  assert.ok(stale.requirements.every(item => !item.allowedActions.length));
  assert.equal(byId(stale, 'submit_plan').result, 'passed');
  assert.equal(selectReadiness(next, '', fixture).coverage.revision, '2');
});

test('worklist readiness links restore exact requirements and checks, keeping the complete originating list URL', () => {
  const data = buildWorklist(initialState), source = journal();
  const origin = '/queue?sort=changed&attention=blocked&selection=requirement%3Areq-ai-counsel';
  const requirement = data.requirements.find(item => item.id === initialState.requirement.id);
  const requirementURL = localURL(worklistActionHref(requirement.allowedActions[0], origin));
  assert.equal(requirementURL.pathname, readinessFixture.paths.readiness);
  assert.equal(selectReadiness(source, requirementURL.search).selection.resource.id, requirement.id);
  assert.equal(requirementURL.searchParams.get('returnTo'), origin);
  for (const item of data.workItems.filter(item => item.mode === 'walkthrough')) {
    const url = localURL(worklistActionHref(item.readinessAction, origin));
    assert.equal(url.pathname, readinessFixture.paths.readiness);
    assert.equal(selectReadiness(source, url.search).selection.resource.id, item.readinessCheckId);
    assert.equal(url.searchParams.get('returnTo'), origin);
    assert.equal(localURL(item.allowedActions[0].href).pathname, readinessFixture.paths.workplan);
  }
});

test('corrective links keep readiness filters, exact selection and worklist origin with safe return paths', () => {
  const model = journal(), params = new URLSearchParams({check: 'submit_plan', order: 'blockers', checkState: 'passed', returnTo: '/renewals?sort=changed'});
  const url = localURL(readinessActionHref(model.current.allowedActions[0], params));
  assert.equal(url.pathname, readinessFixture.paths.workplan);
  assert.equal(url.searchParams.get('returnTo'), params.get('returnTo'));
  assert.equal(safeReadinessReturn(url.search), readinessHref(params));
  assert.equal(resolveWorklistEntry(buildWorklist(initialState), url.pathname, url.search).resource.kind, 'plan_review');
  for (const value of ['https://outside.test', '//outside.test', `${readinessFixture.paths.readiness}/other`, `${readinessFixture.paths.readiness}#fragment`, readinessFixture.paths.history]) {
    assert.equal(safeReadinessReturn(new URLSearchParams({readinessReturn: value})), null);
  }
  assert.equal(readReadinessLocation('returnTo=https://outside.test').returnTo, '/queue');
});

test('stored actions are never policy: unchanged input versions still recompute a corrupted cached permission', () => {
  const state = approved(), stored = structuredClone(journal(state));
  const check = stored.current.checks.find(item => item.id === 'submit_plan');
  check.allowed = true;
  check.result = 'passed';
  check.blockerCount = 0;
  check.allowedActions = evaluateReadiness(initialState).checks[0].allowedActions;
  const restored = advanceReadinessJournal(stored, state);
  assert.equal(byId(restored.current, 'submit_plan').allowed, derive(state).canRequestReview);
  assert.equal(byId(restored.current, 'submit_plan').allowedActions.length, 0);
  assert.equal(restored.sequence, stored.sequence);
  assert.equal(restored.current.evaluationId, stored.current.evaluationId);
  assert.equal(restored.previous, null);
});

test('a missing source card and an unsupported evaluation rule remain unknown instead of passing', () => {
  const fixture = structuredClone(readinessFixture);
  fixture.card = null;
  fixture.checks[0].allowedFlag = 'not_recorded';
  const model = evaluateReadiness(initialState, fixture);
  assert.equal(model.coverage, null);
  assert.equal(model.requirements[0].citations[0].unit, null);
  assert.equal(model.checks[0].result, 'not_evaluated');
  assert.equal(model.checks[0].allowed, null);
  assert.equal(model.checks[0].blockerCount, null);
});

test('missing event times stay unknown without replacing them with the initial fixture time', () => {
  const state = act(initialState, 'request_review');
  delete state.history[0].at;
  const model = evaluateReadiness(state);
  assert.equal(model.evaluatedAt, null);
  assert.equal(byId(model, 'approve_plan').result, 'passed');
  assert.equal(byId(model, 'approve_plan').evaluatedAt, null);
  assert.equal(byId(model, 'approve_plan').blockerCount, 0);
});

test('an unavailable reviewer links to the actual review prerequisite, not the blocked resolution step', () => {
  const state = {...initialState, plan: {...initialState.plan, owner: fixturePeople.internalReviewer}};
  const item = evaluateReadiness(state).requirements[0];
  assert.equal(item.allowedActions.length, 0);
  assert.equal(item.inspectAction.intent, 'inspect');
  const url = localURL(item.inspectAction.href);
  const entry = resolveWorklistEntry(buildWorklist(state), url.pathname, url.search);
  assert.equal(entry.step, 1);
  assert.equal(entry.resource.kind, 'plan_review');
  assert.equal(entry.resource.workflowAction, null);
});
