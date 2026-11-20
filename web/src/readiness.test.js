import test from 'node:test';
import assert from 'node:assert/strict';
import {approvalLetters, fixturePeople, initialState, readinessFixture, readinessEvidenceFixture, readinessEvidenceExample, renewalEvidence, worklistFixture, walkthroughDocumentExamples} from './renewalFixtures.js';
import {customerDecisionSteps} from './customerDecision.js';
import {typedInputs, walkthroughInputs} from './testing/walkthroughInputs.js';
import {derive, transitionEvidenceChecks, transitionWalkthrough, workflowTransitionChecks} from './workflow.js';
import {buildWorklist, resolveWorklistEntry, worklistActionHref} from './worklist.js';
import {
  advanceReadinessJournal, compareReadinessChecks, evaluateReadiness, resolveEvidenceReference, formatReadinessTime, readReadinessJournal,
  readReadinessLocation, readinessActionHref, readinessHref, readinessSearch, readinessSelectionKey,
  safeReadinessReturn, selectReadiness,
} from './readiness.js';

// Document inspection needs the reason the operator types; tests supply it here.
const act = (state, type, fields = {}) => transitionWalkthrough(state, {type, ...(type === 'verify_document' ? typedInputs.verify_document : {}), ...fields});
const chain = (...actions) => actions.reduce((state, action) => act(state, typeof action === 'string' ? action : action.type, typeof action === 'string' ? {} : action), initialState);
const approved = () => chain('request_review', 'approve_plan');
const received = () => act(approved(), 'obtain_document');
const verified = () => act(received(), 'verify_document');
const requested = () => act(verified(), 'request_customer_review');
const step = (state, type) => act(state, type, walkthroughInputs(state, type));
const lettered = () => step(requested(), 'upload_approval_letter');
const inspected = () => step(lettered(), 'inspect_approval_letter');
const authorized = () => step(inspected(), 'record_customer_authority');
const decided = () => step(authorized(), 'record_customer_approval');
const resolved = () => act(decided(), 'resolve_requirement');
const journal = (state = initialState, fixture = readinessFixture) => advanceReadinessJournal(null, state, fixture);
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

test('every displayed transition is accepted by the reducer, including replacements, reopening and plan edits', () => {
  const completed = resolved();
  const states = [initialState, act(initialState, 'request_review'), approved(), received(), verified(), requested(), lettered(), inspected(), authorized(), decided(), completed,
    act(received(), 'reject_document', {reason: initialState.plan.specifications}),
    act(completed, 'reopen_requirement', {reason: initialState.plan.specifications}),
    act(completed, 'save_plan', {title: initialState.plan.instructions}),
    act(completed, 'save_plan', {specifications: initialState.plan.instructions}),
    act(approved(), 'obtain_document', {document: walkthroughDocumentExamples.outdated}),
    act(completed, 'obtain_document', {document: walkthroughDocumentExamples.replacement}),
    act(act(initialState, 'request_review'), 'request_changes', {note: initialState.plan.specifications}),
    {...initialState, plan: {...initialState.plan, owner: fixturePeople.internalReviewer}},
  ];
  // Readiness shows the reducer's own decision. The isolated evidence example is only compared, never acted on.
  for (const fixture of [readinessFixture, readinessEvidenceFixture]) for (const state of states) {
    const d = derive(state, fixturePeople.internalReviewer, fixture), model = evaluateReadiness(state, fixture);
    for (const definition of fixture.checks.filter(item => item.kind === 'transition')) {
      const check = byId(model, definition.id), rule = d.transitions[definition.command];
      assert.equal(check.allowed, rule.recorded ? false : rule.allowed, definition.id);
      const missingEvidence = (transitionEvidenceChecks[definition.command] || []).some(id => byId(model, id).result !== 'passed');
      if (missingEvidence) assert.equal(check.allowed, false);
      assert.equal('demonstration' in check, false);
      assert.equal(check.allowedActions.length, check.allowed ? 1 : 0);
      if (check.allowed) {
        assert.equal(check.result, 'passed');
        assert.equal(check.blockerCount, 0);
      }
    }
    const actions = [...model.allowedActions, ...model.requirements.flatMap(item => [...item.allowedActions, ...(item.reopenAction ? [item.reopenAction] : [])])];
    for (const action of actions) {
      if (!action.command) {
        assert.equal(action.intent, 'inspect_evidence');
        assert.equal(action.checkId, localURL(action.href).searchParams.get('check'));
        continue;
      }
      if (fixture !== readinessFixture) continue;
      const next = act(state, action.command, {...walkthroughInputs(state, action.command),
        reason: initialState.plan.specifications, ...(action.replacement ? {document: walkthroughDocumentExamples.replacement} : {})});
      assert.equal(next.error, null, `${state.plan.status}: ${action.command}`);
      assert.ok(next.history.length >= state.history.length);
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
  assert.deepEqual(codes(byId(model, 'record_customer_decision')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'letter_receipt', 'letter_accepted', 'customer_authority', 'current_version_basis']);
  assert.deepEqual(codes(byId(model, 'receive_approval_letter')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'letter_available']);
  // Letter inspection and the authority record wait on the same plan, document and request gates as the upload.
  assert.deepEqual(codes(byId(model, 'inspect_approval_letter')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'letter_receipt', 'letter_inspection_needed']);
  assert.deepEqual(codes(byId(model, 'record_customer_authority')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'letter_receipt', 'letter_accepted']);
  assert.deepEqual(codes(byId(model, 'resolve_requirement')), ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'customer_approval', 'customer_authority', 'decision_citation', 'current_version_basis']);
  for (const check of model.checks.filter(item => item.result === 'failed')) {
    assert.equal(check.blockerCount, check.blockedReasons.length);
    assert.equal(new Set(codes(check)).size, check.blockerCount);
  }
});

test('four check results keep unevaluated and inapplicable counts distinct from a passed zero', () => {
  const fixture = structuredClone(readinessFixture);
  fixture.checks.at(-1).id = 'unsupported_rule';
  const model = evaluateReadiness(approved(), fixture);
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

test('verification and typed approval never manufacture evidence; the inspected letter and authority record do', () => {
  const typed = act(requested(), 'record_customer_approval', {reviewedBy: approvalLetters[0].signatory.name, evidenceText: 'Approved.'});
  assert.ok(typed.error);
  // The version basis is the agreement record, so it passes once the version it names is received; authority and decision stay failed.
  for (const state of [initialState, received(), verified(), requested(), typed]) {
    const model = evaluateReadiness(state), basisPasses = state !== initialState;
    for (const check of model.checks.filter(item => item.kind === 'evidence' && (item.id !== 'current_version_basis' || !basisPasses))) {
      assert.equal(check.result, 'failed');
      assert.equal(check.blockerCount, 1);
      assert.equal(check.blockedReasons[0].code, check.id);
      assert.equal(check.allowed, null);
      assert.equal(check.evaluatedAt, model.evaluatedAt);
    }
    assert.equal(byId(model, 'current_version_basis').result, basisPasses ? 'passed' : 'failed');
    assert.equal(model.requirements[0].customerAuthorityLabel.toLowerCase(), 'unidentified');
    assert.equal(model.evidenceGapCount, basisPasses ? 2 : 3);
    assert.equal(model.requirements[0].missing.some(item => item.code === 'current_version_basis'), !basisPasses);
    assert.ok(model.requirements[0].missing.some(item => item.code === 'customer_authority'));
  }
  const state = decided(), model = evaluateReadiness(state);
  assert.ok(model.checks.filter(item => item.kind === 'evidence').every(check => check.result === 'passed'));
  assert.equal(model.evidenceGapCount, 0);
  assert.equal(model.requirements[0].customerAuthorityLabel, approvalLetters[0].signatory.name);
  assert.equal(evaluateReadiness(inspected()).requirements[0].customerAuthorityLabel, 'Unidentified');
  assert.equal(byId(evaluateReadiness(inspected()), 'current_version_basis').result, 'passed');
  assert.equal(model.requirements[0].recordedDecision.contact, approvalLetters[0].signatory.name);
  assert.equal(model.requirements[0].recordedDecision.citation, `Receipt ${state.customerReview.evidenceRef.id}`);
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
  // Once the signed letter is in hand nobody is awaited. A later version the agreement record does not name is not
  // requested, and the v4 authority record never names anyone for it.
  for (const state of [lettered(), authorized(), decided()]) assert.equal(evaluateReadiness(state).requirements[0].waitingOn, null);
  const later = act(resolved(), 'obtain_document', {...walkthroughInputs(resolved(), 'obtain_document'), document: walkthroughDocumentExamples.replacement});
  assert.match(act(later, 'verify_document').error, /names version v4/);
  const replaced = evaluateReadiness(later).requirements[0];
  assert.equal(replaced.waitingOn, null);
  assert.equal(replaced.customerAuthorityLabel, 'Unidentified');
});

test('all permitted next steps open the precise work item or requirement without executing a command', () => {
  for (const state of [initialState, act(initialState, 'request_review'), approved(), received(), verified(), requested(), decided()]) {
    const before = structuredClone(state), model = evaluateReadiness(state), action = model.requirements[0].allowedActions[0];
    assert.ok(action.href);
    const url = localURL(action.href), entry = resolveWorklistEntry(buildWorklist(state), url.pathname, url.search);
    if (action.intent === 'inspect_evidence') {
      assert.equal(url.pathname, readinessFixture.paths.readiness);
      assert.equal(selectReadiness(journal(state), url.search).selection.id, 'customer_authority');
      assert.deepEqual(state, before);
      continue;
    }
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
  assert.equal(after.requirements[0].recordedStatus, 'resolved');
  assert.deepEqual(after.crm, before.crm);
  assert.deepEqual(afterState.task, beforeState.task);
  assert.equal(afterState.task.status, beforeState.task.status);
  assert.equal(after.requirements[0].evidenceStatus, 'established');
  assert.deepEqual(afterState.requirement.resolvedWith, {decisionId: beforeState.customerReview.id, evidenceRef: beforeState.customerReview.evidenceRef,
    authorityId: beforeState.evidence.customerAuthority.id, authorityRevision: beforeState.evidence.customerAuthority.revision, versionBasisId: readinessFixture.documentVersionBasis.id});
  assert.deepEqual(after.commercial, before.commercial);
  assert.deepEqual(buildWorklist(afterState).workItems.map(item => [item.id, item.status, item.revision]), buildWorklist(beforeState).workItems.map(item => [item.id, item.status, item.revision]));
  assert.equal(byId(after, 'resolve_requirement').result, 'not_applicable');
  assert.equal(after.requirements[0].allowedActions[0].command, 'reopen_requirement');
  assert.equal(after.requirements[0].nextAction, null);
  assert.deepEqual(after.requirements[0].missing, []);
});

test('multiple requirement records keep their own states and do not borrow the executable requirement status', () => {
  const fixture = structuredClone(readinessEvidenceFixture);
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
    assert.equal(citation.unit.partial, renewalEvidence[index].partial);
    assert.equal(citation.unit.partial, false);
    assert.ok(citation.unit.receipt.readAt);
  }
  const record = model.coverage.units.find(unit => unit.kind === 'record');
  assert.ok(record.fields.length);
  assert.equal(record.text, undefined);
  assert.equal(record.eventAt, null);
});

test('receipt and verification never close the source-card gaps or change its read times', () => {
  const original = evaluateReadiness(initialState).coverage;
  assert.deepEqual(original.gaps.map(gap => gap.code), ['calls_outside_records']);
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
  // A distinct file of the same version: the next action stays the same, and the new receipt is recorded as a changed input.
  const replacement = act(first, 'obtain_document', {document: walkthroughDocumentExamples.rescan});
  const next = advanceReadinessJournal(prior, replacement);
  assert.equal(next.current.requirements[0].status, prior.current.requirements[0].status);
  assert.equal(next.current.requirements[0].nextAction.id, prior.current.requirements[0].nextAction.id);
  assert.ok(next.changes.requirements[0].differences.some(difference => difference.field === 'input:document'));
  assert.equal(next.previous.document.version, first.document.version);
});

test('reopening retains the old resolved result and reports the recorded reason with its explicit actor', () => {
  const state = resolved(), prior = journal(state);
  assert.match(act(state, 'reopen_requirement', {reason: initialState.plan.specifications}).error, /Name the principal reopening/);
  const opened = act(state, 'reopen_requirement', {reason: initialState.plan.specifications, actorId: 'principal-reviewer'});
  const next = advanceReadinessJournal(prior, opened);
  assert.equal(next.previous.requirements[0].recordedStatus, 'resolved');
  assert.equal(next.current.requirements[0].recordedStatus, 'open');
  assert.equal(next.current.requirements[0].status, 'open');
  assert.equal(next.changes.cause.detail, opened.history.at(-1).detail);
  assert.equal(next.changes.cause.actor, fixturePeople.internalReviewer);
  assert.ok(next.changes.requirements[0].differences.some(difference => difference.field === 'recordedStatus'));
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
  const state = approved(), next = advanceReadinessJournal(journal(initialState), state);
  const reloaded = readReadinessJournal(JSON.stringify(next));
  assert.deepEqual(reloaded, next);
  assert.equal(advanceReadinessJournal(reloaded, structuredClone(state)), reloaded);
  for (const value of [null, '', '{', '{}', JSON.stringify({schema: '1', current: {}})]) assert.equal(readReadinessJournal(value), null);
  assert.equal(journal(resolved()).previous, null);
  assert.equal(journal(resolved()).current.requirements[0].recordedStatus, 'resolved');
  assert.equal(journal(resolved()).current.requirements[0].status, 'resolved');
  assert.equal(journal(resolved(), {...readinessFixture, card: null}).current.requirements[0].status, 'open');
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
  const source = journal(resolved(), readinessEvidenceFixture);
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
  assert.equal(hidden.visibleChecks.length, 9);
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
  fixture.checks[0].command = 'not_recorded';
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

test('an outdated receipt blocks verification and exposes a replacement accepted by the same reducer', () => {
  const state = act(approved(), 'obtain_document', {document: walkthroughDocumentExamples.outdated});
  const model = evaluateReadiness(state), verification = byId(model, 'verify_document'), receipt = byId(model, 'receive_document');
  assert.equal(verification.allowed, false);
  assert.deepEqual(codes(verification), ['document_applies']);
  assert.deepEqual(verification.blockedReasons, derive(state).transitions.verify_document.blockedReasons);
  assert.equal(verification.allowedActions.length, 0);
  assert.equal(receipt.allowed, true);
  assert.equal(receipt.replacement, true);
  assert.equal(model.requirements[0].nextAction.command, 'obtain_document');
  const url = localURL(receipt.allowedActions[0].href), item = resolveWorklistEntry(buildWorklist(state), url.pathname, url.search).resource;
  assert.equal(item.kind, 'collection');
  assert.equal(item.replacementAction.command, receipt.allowedActions[0].command);
  const replaced = act(state, item.replacementAction.command);
  assert.equal(replaced.error, null);
  assert.equal(byId(evaluateReadiness(replaced), 'verify_document').allowed, true);
  assert.equal(act(replaced, 'verify_document').error, null);
});

test('the reducer refuses decision recording and resolution until their evidence checks pass, and Readiness shows the same blockers', () => {
  const pending = requested(), recording = byId(evaluateReadiness(pending), 'record_customer_decision');
  assert.equal(recording.allowed, false);
  assert.deepEqual(codes(recording), ['letter_receipt', 'letter_accepted', 'customer_authority']);
  assert.deepEqual(recording.blockedReasons, derive(pending).transitions.record_customer_approval.blockedReasons);
  assert.equal(evaluateReadiness(pending).nextAction.command, 'upload_approval_letter');
  assert.ok(act(pending, 'record_customer_approval', walkthroughInputs(pending, 'record_customer_approval')).error);
  // Without the agreement record's version basis, resolution is blocked on that evidence alone, in Readiness and the reducer.
  const state = decided(), withoutBasis = {...readinessFixture, documentVersionBasis: null};
  const blocked = evaluateReadiness(state, withoutBasis), check = byId(blocked, 'resolve_requirement');
  assert.deepEqual(codes(check), ['current_version_basis']);
  assert.equal(check.allowedActions.length, 0);
  assert.equal(blocked.nextAction.intent, 'inspect_evidence');
  assert.equal(blocked.nextAction.checkId, 'current_version_basis');
  assert.match(derive(state, undefined, withoutBasis).transitions.resolve_requirement.refusal, /approval evidence/);
  const supported = evaluateReadiness(state);
  assert.equal(byId(supported, 'resolve_requirement').allowed, true);
  assert.equal(byId(supported, 'resolve_requirement').blockerCount, 0);
  assert.equal(supported.nextAction.command, 'resolve_requirement');
  assert.equal(act(state, 'resolve_requirement').error, null);
});

test('known-missing evidence fails by name while a missing card or rule stays unevaluated', () => {
  const missing = evaluateReadiness(requested()), noDocument = evaluateReadiness(approved());
  for (const [model, id] of [[missing, 'customer_authority'], [missing, 'decision_citation'], [noDocument, 'current_version_basis']]) {
    assert.equal(byId(model, id).result, 'failed');
    assert.deepEqual(codes(byId(model, id)), [id]);
    assert.equal(byId(model, id).blockerCount, 1);
  }
  const fixture = structuredClone(readinessFixture);
  fixture.card = null;
  fixture.checks.find(check => check.id === 'submit_plan').command = 'unavailable_rule';
  const unavailable = evaluateReadiness(decided(), fixture);
  for (const id of ['submit_plan', 'customer_authority', 'decision_citation', 'current_version_basis']) {
    assert.equal(byId(unavailable, id).result, 'not_evaluated');
    assert.equal(byId(unavailable, id).blockerCount, null);
  }
  assert.equal(byId(unavailable, 'resolve_requirement').allowed, false);
  assert.equal(byId(unavailable, 'resolve_requirement').blockerCount, 3);
});

test('each reference resolves by its real source, card and unit, or inspected receipt identity', () => {
  const fixture = structuredClone(readinessEvidenceFixture), example = readinessEvidenceExample;
  const references = [example.documentVersionBasis.source, {kind: 'unit', cardId: example.unit.cardId, n: example.unit.n}, example.decisionReceipt.evidenceRef];
  for (const reference of references) {
    const resolved = resolveEvidenceReference(reference, fixture);
    assert.ok(resolved.record);
    assert.equal(resolved.issue, undefined);
    fixture.documentVersionBasis.source = reference;
    // A receipt resolves, but a letter receipt never establishes which version applies.
    assert.equal(byId(evaluateReadiness(received(), fixture), 'current_version_basis').result, reference.kind === 'receipt' ? 'failed' : 'passed');
  }
  for (const reference of [null, example.source.label, {kind: 'source', id: 'missing'}, {kind: 'unit', cardId: 'missing', n: example.unit.n},
    {kind: 'unit', cardId: example.unit.cardId, n: 'missing'}, {kind: 'receipt', id: 'missing'}, {kind: 'unknown', id: example.receipt.id}]) {
    assert.ok(resolveEvidenceReference(reference, fixture).issue);
    fixture.documentVersionBasis.source = reference;
    assert.equal(byId(evaluateReadiness(received(), fixture), 'current_version_basis').result, 'failed');
  }
});

test('unread sources, hidden units, and uninspected receipts cannot support evidence checks', () => {
  const example = readinessEvidenceExample;
  for (const [ref, invalidate] of [
    [example.documentVersionBasis.source, fixture => {fixture.card.sources.at(-1).status = 'not_read';}],
    [{kind: 'unit', cardId: example.unit.cardId, n: example.unit.n}, fixture => {fixture.card.units.at(-1).shown = false;}],
    [{kind: 'unit', cardId: example.unit.cardId, n: example.unit.n}, fixture => {fixture.card.sources.at(-1).status = 'not_read';}],
    [example.decisionReceipt.evidenceRef, fixture => {fixture.artifactReceipts[0].inspections[0].status = 'pending';}],
    [example.decisionReceipt.evidenceRef, fixture => {fixture.artifactReceipts[0].status = 'withdrawn';}],
  ]) {
    const fixture = structuredClone(readinessEvidenceFixture);
    fixture.documentVersionBasis.source = ref;
    fixture.customerAuthority.evidenceRef = ref;
    fixture.decisionReceipt.evidenceRef = ref;
    invalidate(fixture);
    // The walkthrough's own records win over a fixture, so the fixture's records are read against a pending request.
    const model = evaluateReadiness(requested(), fixture);
    assert.ok(model.checks.filter(check => check.kind === 'evidence').every(check => check.result === 'failed'));
    assert.equal(byId(model, 'resolve_requirement').allowed, false);
  }
});

test('neither a document source, its unit, nor its own receipt can vouch for its current version', () => {
  const state = received(), example = readinessEvidenceExample;
  for (const [ref, identifyAsDocument] of [
    [example.documentVersionBasis.source, fixture => {fixture.card.sources.at(-1).documentId = state.document.id;}],
    [{kind: 'unit', cardId: example.unit.cardId, n: example.unit.n}, fixture => {fixture.card.units.at(-1).documentId = state.document.id;}],
    [example.decisionReceipt.evidenceRef, fixture => {fixture.artifactReceipts[0].artifactId = state.document.id;}],
    [example.documentVersionBasis.source, fixture => {fixture.card.sources.at(-1).kind = 'unidentified';}],
  ]) {
    const fixture = structuredClone(readinessEvidenceFixture);
    fixture.documentVersionBasis.source = ref;
    identifyAsDocument(fixture);
    const check = byId(evaluateReadiness(state, fixture), 'current_version_basis');
    assert.equal(check.result, 'failed');
    assert.deepEqual(codes(check), ['current_version_basis']);
  }
});

test('accepted authority requires a real contact, agreement binding, and a resolvable evidence reference', () => {
  for (const invalidate of [
    fixture => {fixture.customerAuthority.contactId = 'missing';},
    fixture => {fixture.contacts[0].status = 'withdrawn';},
    fixture => {fixture.customerAuthority.agreementId = 'missing';},
    fixture => {fixture.customerAuthority.status = 'withdrawn';},
    fixture => {fixture.customerAuthority.evidenceRef.id = 'missing';},
  ]) {
    const fixture = structuredClone(readinessEvidenceFixture);
    invalidate(fixture);
    const model = evaluateReadiness(requested(), fixture);
    assert.equal(byId(model, 'customer_authority').result, 'failed');
    assert.equal(byId(model, 'decision_citation').result, 'failed');
    assert.equal(model.requirements[0].customerAuthorityLabel, 'Unidentified');
    assert.equal(evaluateReadiness(requested(), fixture).requirements[0].waitingOn.id, null);
  }
});

test('a cited decision must bind the agreement, document, version, and evidence-backed approving contact', () => {
  for (const [field, value] of [['agreementId', 'missing'], ['documentId', 'missing'], ['documentVersion', walkthroughDocumentExamples.replacement.version],
    ['contactId', 'missing'], ['status', 'pending'], ['outcome', 'rejected']]) {
    const fixture = structuredClone(readinessEvidenceFixture);
    fixture.decisionReceipt[field] = value;
    const model = evaluateReadiness(requested(), fixture);
    assert.equal(byId(model, 'customer_authority').result, 'passed');
    assert.equal(byId(model, 'decision_citation').result, 'failed', field);
    assert.equal(byId(model, 'resolve_requirement').allowed, false);
  }
  const fixture = structuredClone(readinessEvidenceFixture);
  assert.equal(byId(evaluateReadiness(requested(), fixture), 'decision_citation').result, 'passed');
  fixture.decisionReceipt.evidenceRef = fixture.documentVersionBasis.source;
  assert.equal(byId(evaluateReadiness(requested(), fixture), 'decision_citation').result, 'failed');
  fixture.decisionReceipt.evidenceRef = {kind: 'unit', cardId: fixture.card.id, n: 'missing'};
  assert.equal(byId(evaluateReadiness(requested(), fixture), 'decision_citation').result, 'failed');
  // A shown unit from a read source still cannot stand as the decision.
  fixture.decisionReceipt.evidenceRef = {kind: 'unit', cardId: fixture.card.id, n: '1'};
  assert.match(byId(evaluateReadiness(requested(), fixture), 'decision_citation').explanation, /source unit cannot establish an approval/);
});

test('work queue readiness follows each current command and completed items fall back to their recorded output', () => {
  const states = [initialState, act(initialState, 'request_review'), approved(), received(), verified(), requested(), lettered(), inspected(), authorized(), decided(), resolved(),
    act(approved(), 'obtain_document', {document: walkthroughDocumentExamples.outdated}), act(received(), 'reject_document', {reason: initialState.plan.specifications})];
  for (const state of states) {
    const data = buildWorklist(state), source = journal(state);
    for (const item of data.workItems.filter(row => row.mode === 'walkthrough')) {
      const url = localURL(item.readinessAction.href), command = item.workflowAction?.command;
      if (item.status === 'complete') {
        assert.equal(url.pathname, readinessFixture.paths.workplan);
        assert.equal(item.readinessAction.intent, 'inspect');
        assert.equal(resolveWorklistEntry(data, url.pathname, url.search).id, item.id);
        assert.ok(command.startsWith('inspect_'));
      } else {
        assert.equal(url.pathname, readinessFixture.paths.readiness);
        const check = selectReadiness(source, url.search).selection.resource;
        if (command) assert.equal(check.id, workflowTransitionChecks[command]);
        // Rejection shares the inspection check with acceptance.
        if (command) assert.equal(check.command, command === 'reject_document' ? 'verify_document' : command);
        assert.notEqual(check.result, 'not_applicable');
      }
      for (const action of [item.workflowAction, item.replacementAction].filter(action => action && !action.command.startsWith('inspect_'))) {
        assert.equal(act(state, action.command, {...walkthroughInputs(state, action.command),
          ...(item.replacementAction === action ? {document: walkthroughDocumentExamples.replacement} : {})}).error, null);
      }
    }
  }
  const plan = buildWorklist(states[1]).workItems.find(item => item.kind === 'plan_review');
  assert.equal(localURL(plan.readinessAction.href).searchParams.get('check'), 'approve_plan');
  const review = buildWorklist(requested()).workItems.find(item => item.kind === 'customer_review');
  assert.equal(localURL(review.readinessAction.href).searchParams.get('check'), 'receive_approval_letter');
  const authority = buildWorklist(inspected()).workItems.find(item => item.kind === 'customer_review');
  assert.equal(localURL(authority.readinessAction.href).searchParams.get('check'), 'record_customer_authority');
});

test('check differences include added and removed IDs and changes to blocker details without changing their codes', () => {
  const fixture = structuredClone(readinessFixture), state = received();
  let before = advanceReadinessJournal(null, state, fixture);
  fixture.checks = fixture.checks.filter(check => check.id !== 'verify_document');
  const removed = advanceReadinessJournal(before, state, fixture);
  const row = removed.changes.checks.find(check => check.id === 'verify_document');
  assert.equal(row.before, 'passed');
  assert.equal(row.after, null);
  assert.ok(compareReadinessChecks(removed.previous, removed.current).some(check => check.id === row.id));
  const added = advanceReadinessJournal(removed, state);
  assert.equal(added.changes.checks.find(check => check.id === row.id).before, null);
  assert.equal(added.changes.checks.find(check => check.id === row.id).after, 'passed');
  const old = act(approved(), 'obtain_document', {document: walkthroughDocumentExamples.outdated});
  before = journal(old);
  // The file's own currentVersion changes nothing; a different received version changes the agreement-record blocker.
  const changed = advanceReadinessJournal(before, {...old, document: {...old.document, version: 'v2'}});
  const diff = changed.changes.checks.find(check => check.id === 'verify_document');
  assert.equal(diff.before, diff.after);
  assert.equal(diff.added.length, 0);
  assert.equal(diff.cleared.length, 0);
  assert.equal(diff.updated[0].code, 'document_applies');
  assert.notEqual(diff.updated[0].before.reason, diff.updated[0].after.reason);
  assert.equal(changed.current.evaluatedAt, null);
});

test('requirement differences preserve changed blocker explanations with the same code', () => {
  const fixture = structuredClone(readinessEvidenceFixture);
  fixture.customerAuthority.evidenceRef.id = 'missing';
  const before = advanceReadinessJournal(null, requested(), fixture);
  fixture.customerAuthority.evidenceRef = readinessEvidenceExample.customerAuthority.evidenceRef;
  fixture.artifactReceipts[0].inspections[0].status = 'pending';
  const after = advanceReadinessJournal(before, requested(), fixture);
  const change = after.changes.requirements.find(item => item.id === initialState.requirement.id);
  assert.ok(change.updated.some(item => item.code === 'customer_authority' && item.before.reason !== item.after.reason));
});

test('source-only changes keep input time unknown until a recorded read covers them, not an old event time', () => {
  const state = requested(), fixture = structuredClone(readinessFixture);
  const before = advanceReadinessJournal(null, state, fixture);
  fixture.card.units[0].text = renewalEvidence[0].context;
  fixture.card.revision = String(Number(fixture.card.revision) + 1);
  const changed = advanceReadinessJournal(before, state, fixture);
  assert.equal(changed.current.evaluatedAt, null);
  assert.equal(changed.changes.cause.at, null);
  assert.equal(changed.changes.cause.id, null);
  assert.equal(changed.previous.evaluatedAt, state.history.at(-1).at);
  assert.equal(advanceReadinessJournal(changed, state, fixture), changed);
  const decidedState = decided(), eventOnly = advanceReadinessJournal(changed, decidedState, fixture);
  assert.equal(eventOnly.current.evaluatedAt, null);
  fixture.card.readAt = decidedState.history.at(-1).at;
  const read = advanceReadinessJournal(eventOnly, decidedState, fixture);
  assert.equal(read.current.evaluatedAt, fixture.card.readAt);
});

test('the first local evaluation always has an initial cause, and later raw event text is labelled reviewer input', () => {
  const first = journal(decided());
  assert.equal(first.changes.cause.id, null);
  assert.equal(first.changes.cause.title, journal().changes.cause.title);
  assert.notEqual(first.changes.cause.detail, decided().history.at(-1).detail);
  const after = advanceReadinessJournal(journal(requested()), decided());
  assert.equal(after.changes.cause.detail, decided().history.at(-1).detail);
  assert.ok(after.changes.cause.label);
  assert.notEqual(after.changes.cause.label, first.changes.cause.label);
  assert.equal(after.changes.cause.actor, fixturePeople.internalReviewer);
});

test('input identities distinguish document and review records from their work-item revisions', () => {
  const state = decided(), inputs = evaluateReadiness(state).inputVersions;
  const document = inputs.find(input => input.kind === 'document');
  const review = inputs.find(input => input.kind === 'customer_decision');
  const work = buildWorklist(state);
  assert.equal(document.id, state.document.id);
  assert.equal(document.revision, null);
  assert.equal(document.version, state.document.version);
  assert.equal(review.id, state.customerReview.id);
  assert.equal(review.revision, null);
  assert.equal(review.version, state.customerReview.documentVersion);
  assert.equal(review.recorded, true);
  for (const [kind, workKind] of [['inspection_work_item', 'inspection'], ['customer_review_work_item', 'customer_review']]) {
    const input = inputs.find(input => input.kind === kind), item = work.workItems.find(item => item.kind === workKind);
    assert.equal(input.id, item.id);
    assert.equal(input.revision, item.revision);
  }
});

test('compact input keys change on relevant records but not error-only updates or fresh object copies', () => {
  const state = received(), before = journal(state), key = before.current.inputKey;
  assert.match(key, /^[0-9a-f]{16}$/);
  assert.equal(advanceReadinessJournal(before, structuredClone(state)).current.inputKey, key);
  assert.equal(advanceReadinessJournal(before, {...state, error: 'Local refusal'}), before);
  const fixture = structuredClone(readinessFixture);
  fixture.card.sources[0].status = 'not_read';
  const changed = advanceReadinessJournal(before, state, fixture);
  assert.notEqual(changed.current.inputKey, key);
  assert.notEqual(changed.current.evaluationId, before.current.evaluationId);
  assert.equal(changed.previous.inputKey, key);
  assert.ok(!changed.current.inputKey.includes(JSON.stringify(fixture)));
});

test('authority that becomes unknown keeps the requirement open in readiness even after a walkthrough resolution', () => {
  // A fixture cannot override the walkthrough's own authority record; a restored record whose own authority is withdrawn can.
  const supportedState = resolved(), withdrawn = readinessFixture;
  const state = {...supportedState, evidence: {...supportedState.evidence, customerAuthority: {...supportedState.evidence.customerAuthority, status: 'withdrawn'}}};
  const source = journal(state, withdrawn), model = source.current;
  assert.equal(state.requirement.status, 'resolved');
  assert.equal(model.requirements[0].recordedStatus, state.requirement.status);
  assert.equal(model.requirements[0].status, 'open');
  assert.equal(model.openRequirementCount, 1);
  assert.equal(model.requirements[0].nextAction.checkId, 'customer_authority');
  assert.equal(selectReadiness(source, 'requirements=open', withdrawn).visibleRequirements.length, 1);
  assert.equal(selectReadiness(source, 'requirements=resolved', withdrawn).visibleRequirements.length, 1);
  const supported = evaluateReadiness(supportedState);
  assert.equal(supported.requirements[0].status, 'resolved');
  assert.equal(supported.openRequirementCount, 0);
  assert.equal(supported.requirements[0].allowedActions[0].command, 'reopen_requirement');
});

test('a blocked submitted review still links to approval prerequisites, not an already recorded submission', () => {
  const submitted = act(initialState, 'request_review');
  const state = {...submitted, plan: {...submitted.plan, owner: fixturePeople.internalReviewer}};
  const item = buildWorklist(state).workItems.find(item => item.kind === 'plan_review');
  assert.equal(item.workflowAction, null);
  const url = localURL(item.readinessAction.href);
  const check = selectReadiness(journal(state), url.search).selection.resource;
  assert.equal(check.id, 'approve_plan');
  assert.equal(check.result, 'failed');
  assert.deepEqual(codes(check), ['independent_reviewer']);
});

test('a substituted fixture reviewer cannot offer a plan action the demonstration reducer refuses', () => {
  const fixture = structuredClone(readinessFixture);
  fixture.worklist.walkthrough.reviewerId = fixture.worklist.people[0].id;
  for (const base of [initialState, act(initialState, 'request_review')]) {
    const state = {...base, plan: {...base.plan, owner: fixturePeople.internalReviewer}};
    const model = evaluateReadiness(state, fixture);
    const command = derive(state).nextCommand;
    assert.equal(byId(model, workflowTransitionChecks[command]).allowed, false);
    assert.equal(byId(model, workflowTransitionChecks[command]).allowedActions.length, 0);
    assert.ok(act(state, command).error);
  }
});
