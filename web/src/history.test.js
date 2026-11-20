import test from 'node:test';
import assert from 'node:assert/strict';
import {fixturePeople, historyFixture, historyTestRecords, initialState, readinessFixture, readinessEvidenceFixture, walkthroughDecisions, walkthroughDocumentExamples} from './renewalFixtures.js';
import {derive, transition, transitionWalkthrough} from './workflow.js';
import {compareReadinessChecks, evaluateReadiness, selectReadiness, advanceReadinessJournal} from './readiness.js';
import {buildWorklist, resolveWorklistEntry} from './worklist.js';
import {historyEvidenceSnapshot, historyWorkflowSnapshot} from './historyRecording.js';
import {buildHistoryExample, compareHistoryValues, formatHistoryDuration, formatHistoryTime, historyActionHref, historyCategories, historyExport, historyHref, historyImpacts, historySearch, readHistoryLocation, safeHistoryReturn, selectHistory} from './history.js';

const act = (state, type, extra = {}) => transitionWalkthrough(state, {type, ...extra});
const chain = (...steps) => steps.reduce((state, step) => act(state, typeof step === 'string' ? step : step.type, typeof step === 'string' ? {} : step), initialState);
const approved = () => chain('request_review', 'approve_plan');
const verified = () => act(act(approved(), 'obtain_document'), 'verify_document');
const requested = () => act(verified(), 'request_customer_review');
const decided = () => act(requested(), 'record_customer_approval', {reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
const resolved = () => act(decided(), 'resolve_requirement');
const byId = (view, id) => view.entries.find(entry => entry.id === id);
const primary = view => view.entries.filter(entry => !entry.derived && entry.type !== 'opening_snapshot');
const values = entry => Object.fromEntries(entry.values.map(value => [value.field, value]));
const last = (state, search = '') => byId(selectHistory(state, search), state.history.at(-1).id);
const sample = historyFixture.examples.find(example => example.mode === 'example');
const exampleView = search => selectHistory(initialState, `example=${sample.id}&${search || ''}`);
const exampleId = type => sample.steps.find(step => step.action?.type === type || step.correction?.type === type).id;
const localURL = href => new URL(href, 'http://local.test');
const ids = items => items.map(item => item.id);

test('history is a pure typed projection, with string identities, lists, and no executable actions', () => {
  const state = resolved(), before = structuredClone(state), fixture = structuredClone(historyFixture);
  const view = selectHistory(state);
  assert.deepEqual(state, before);
  assert.deepEqual(historyFixture, fixture);
  assert.equal(typeof view.id, 'string');
  assert.equal(typeof view.revision, 'string');
  assert.equal(view.allowedActions.length, 0);
  assert.ok(Array.isArray(view.intervals));
  for (const entry of view.entries) {
    assert.equal(typeof entry.id, 'string');
    assert.equal(typeof entry.sequence, 'string');
    assert.equal(typeof entry.actor, 'string');
    for (const field of entry.values) {
      assert.equal(typeof field.type, 'string');
      assert.equal(typeof field.display, 'string');
      assert.ok(field.attribution && field.scope);
      if (['id', 'revision'].includes(field.type) && field.value !== null) assert.equal(typeof field.value, 'string');
    }
  }
});

test('recording metadata captures immutable before and after inputs without changing committed workflow rules', () => {
  const state = decided(), before = structuredClone(state);
  const next = act(state, 'resolve_requirement');
  const event = next.history.at(-1);
  assert.deepEqual(event.historyRecord.before, historyWorkflowSnapshot(state));
  assert.deepEqual(event.historyRecord.after, historyWorkflowSnapshot(next));
  assert.equal(next.task, state.task);
  assert.equal(next.commercial, state.commercial);
  assert.equal(next.customerReview, state.customerReview);
  assert.deepEqual(next.history.slice(0, -1), state.history);
  next.plan.title = historyTestRecords.revisedTitle;
  assert.equal(event.historyRecord.after.plan.title, before.plan.title);
  assert.equal(event.historyRecord.before.plan.title, before.plan.title);
  assert.deepEqual(event.historyRecord.contextBefore, historyEvidenceSnapshot());
  assert.ok(!('history' in event.historyRecord.before));
  assert.ok(!('error' in event.historyRecord.after));
});

test('rejected and no-op commands append neither decisions nor snapshot records', () => {
  for (const [state, action] of [[initialState, {type: 'approve_plan'}], [approved(), {type: 'approve_plan'}],
    [initialState, {type: 'save_plan', title: initialState.plan.title}], [resolved(), {type: 'reopen_requirement', reason: ''}]]) {
    const result = transitionWalkthrough(state, action);
    assert.equal(result.history, state.history);
    assert.deepEqual(ids(selectHistory(result).entries), ids(selectHistory(state).entries));
  }
});

test('a fresh walkthrough shows an observed opening snapshot, not invented creation events or actors', () => {
  const view = selectHistory(initialState), baseline = view.entries[0], interval = view.intervals[0].intervals[0];
  assert.equal(view.recordedCount, 0);
  assert.equal(view.derivedCount, 0);
  assert.equal(view.emptyWalkthrough, true);
  assert.equal(baseline.type, 'opening_snapshot');
  assert.equal(baseline.actor, 'not recorded');
  assert.equal(baseline.eventAt, null);
  assert.equal(interval.openedAt, null);
  assert.equal(interval.observedAt, historyFixture.baseline.recordedAt);
  assert.equal(interval.durationMs, null);
  assert.equal(interval.observedMs, 0);
});

test('the full walkthrough keeps every event in recording order and separates four output rows from resolution', () => {
  const state = resolved(), view = selectHistory(state);
  assert.deepEqual(primary(view).map(entry => entry.type), state.history.map(event => event.type));
  assert.equal(view.recordedCount, state.history.length);
  const outputs = view.entries.filter(entry => entry.type === 'work_item_output');
  assert.equal(outputs.length, 4);
  assert.deepEqual(outputs.map(entry => entry.output.kind), ['plan_review', 'collection', 'inspection', 'customer_review']);
  assert.ok(outputs.every(entry => entry.derived && entry.parentId !== state.history.at(-1).id));
  assert.equal(view.outcomes.find(item => item.id === 'requirement').entryIds.length, 1);
  assert.equal(view.outcomes.find(item => item.id === 'commercial').value, initialState.commercial.label);
  assert.equal(view.outcomes.find(item => item.id === 'commercial').recordedAt, null);
  assert.equal(last(state).impacts.outputs.length, 0);
});

test('actors come from the recorded event or decision, never from today’s owner or an assumed principal', () => {
  const state = resolved(), view = selectHistory(state);
  assert.equal(primary(view).find(entry => entry.type === 'request_review').actor, 'not recorded');
  assert.equal(primary(view).find(entry => entry.type === 'approve_plan').actor, fixturePeople.internalReviewer);
  assert.equal(primary(view).find(entry => entry.type === 'verify_document').actor, state.document.verifiedBy);
  assert.equal(primary(view).find(entry => entry.type === 'record_customer_approval').actor, state.customerReview.recordedBy);
  assert.equal(last(state).actor, 'not recorded');
  const edited = act(state, 'save_plan', {owner: fixturePeople.otherAccountOwner});
  assert.equal(last(edited).actor, 'not recorded');
  assert.equal(byId(selectHistory(edited), state.history[1].id).actor, fixturePeople.internalReviewer);
});

test('specification changes report exactly the lost derive facts and the same changed Readiness checks', () => {
  const before = resolved(), after = act(before, 'save_plan', {specifications: historyTestRecords.revisedSpecifications});
  const entry = last(after), old = derive(before), next = derive(after);
  assert.deepEqual(entry.differences.filter(change => change.valueChanged && change.field.startsWith('plan.')).map(change => change.field), ['plan.revision', 'plan.specifications', 'plan.status']);
  assert.ok(entry.trigger);
  for (const effect of entry.impacts.effects) {
    assert.equal(effect.status === 'invalidated', Boolean(old[effect.id] && !next[effect.id]));
  }
  assert.deepEqual(entry.impacts.checks, compareReadinessChecks(evaluateReadiness({...before, history: []}), evaluateReadiness({...after, history: []})).filter(check => check.changed));
  assert.ok(entry.impacts.effects.find(effect => effect.id === 'isDocumentVerified' && effect.status === 'invalidated'));
  assert.ok(entry.impacts.effects.find(effect => effect.id === 'hasCustomerApproval' && effect.status === 'invalidated'));
  assert.equal(after.requirement.status, 'open');
});

test('title and owner edits report the actual legacy invalidation while retaining document inspection', () => {
  for (const change of [{title: historyTestRecords.revisedTitle}, {owner: fixturePeople.otherAccountOwner}]) {
    const before = resolved(), after = act(before, 'save_plan', change), entry = last(after);
    assert.equal(entry.impacts.effects.find(effect => effect.id === 'isDocumentVerified').status, 'kept');
    assert.equal(entry.impacts.effects.find(effect => effect.id === 'hasCustomerApproval').status, 'invalidated');
    assert.equal(after.requirement.status, 'open');
    assert.equal(entry.transitions[0].after, after.requirement.status);
    assert.equal(entry.transitions[0].before, before.requirement.status);
    assert.deepEqual(entry.differences.filter(difference => difference.valueChanged && Object.keys(change).some(key => `plan.${key}` === difference.field)).map(difference => difference.field), Object.keys(change).map(key => `plan.${key}`));
  }
});

test('an automatic reopening is its own linked row with unknown actor and reason, not fabricated reviewer input', () => {
  const state = act(resolved(), 'save_plan', {specifications: historyTestRecords.revisedSpecifications});
  const view = selectHistory(state), event = last(state), reopened = view.entries.find(entry => entry.parentId === event.id && entry.type === 'requirement_transition');
  assert.ok(reopened);
  assert.equal(reopened.reasonRequired, true);
  assert.equal(reopened.reason, null);
  assert.equal(reopened.actor, 'not recorded');
  assert.equal(reopened.transitions[0].requirementId, state.requirement.id);
  const interval = view.intervals[0].intervals.at(-1);
  assert.equal(interval.startEntryId, reopened.id);
  assert.equal(interval.reason, null);
  assert.equal(interval.cause, event.trigger);
  assert.equal(interval.openedAt, event.recordedAt);
});

test('review requests acquire an outcome only when the actual matching decision is known', () => {
  let state = act(initialState, 'request_review');
  const request = state.history.at(-1);
  state = act(state, 'request_changes', {note: historyTestRecords.editReason, actor: fixturePeople.internalReviewer});
  const decision = state.history.at(-1), view = selectHistory(state);
  assert.equal(byId(view, decision.id).reason, historyTestRecords.editReason);
  assert.equal(byId(view, decision.id).reasonRequired, true);
  assert.equal(byId(view, request.id).outcomes[0].id, decision.id);
  assert.equal(byId(selectHistory(state, `as_of=${request.id}`), request.id).outcomes.length, 0);
  assert.equal(byId(view, request.id).eventAt, null);
  assert.notEqual(byId(view, request.id).recordedAt, byId(view, request.id).outcomes[0].recordedAt);
});

test('replacement comparisons keep the old receipt, typed versions and approval snapshots inspectable', () => {
  const original = resolved(), before = structuredClone(original.history), state = act(original, 'obtain_document', {document: walkthroughDocumentExamples.replacement});
  const view = selectHistory(state), entry = last(state), difference = entry.differences.find(change => change.field === 'document.version');
  assert.equal(difference.before.value, original.document.version);
  assert.equal(difference.after.value, state.document.version);
  assert.equal(difference.scopeChanged, true);
  assert.ok(view.entries.some(row => row.type === 'obtain_document' && values(row)['document.version'].value === original.document.version));
  assert.ok(view.entries.some(row => row.type === 'record_customer_approval' && values(row)['decision.version'].value === original.document.version));
  assert.deepEqual(original.history, before);
  assert.equal(entry.impacts.effects.find(effect => effect.id === 'isPlanApproved').status, 'kept');
});

test('identical decision text is a typed change when its attribution or scope changes', () => {
  const first = decided();
  let state = act(act(first, 'resolve_requirement'), 'reopen_requirement', {reason: historyTestRecords.editReason});
  state = act(state, 'request_customer_review');
  state = act(state, 'record_customer_approval', {reviewedBy: fixturePeople.customerContact, recordedBy: fixturePeople.accountOwner, evidenceText: first.customerReview.evidenceText});
  const entry = last(state), difference = entry.differences.find(change => change.field === 'decision.text');
  assert.equal(difference.valueChanged, false);
  assert.equal(difference.attributionChanged, true);
  assert.equal(difference.before.value, difference.after.value);
  assert.equal(difference.after.attribution.actor, fixturePeople.accountOwner);
  assert.equal(difference.after.attribution.attributedTo, fixturePeople.customerContact);
  assert.equal(entry.comparisonEntryId, first.history.at(-1).id);
  const corrected = byId(exampleView('view=effective'), exampleId('record_customer_approval'));
  assert.equal(values(corrected)['decision.text'].value, first.customerReview.evidenceText);
  assert.equal(values(corrected)['decision.text'].scope.documentVersion, walkthroughDocumentExamples.replacement.version);
});

test('authority, citation and version effects are evaluated by Readiness, not inferred from typed approval', () => {
  const view = selectHistory(decided()), decision = primary(view).find(row => row.type === 'record_customer_approval');
  assert.equal(values(decision)['decision.authority'].value, null);
  assert.equal(values(decision)['decision.authority'].display, 'unidentified');
  assert.equal(values(decision)['decision.citation'].value, null);
  assert.ok(decision.impacts.evidenceChecks.every(check => check.result === 'failed'));
  const document = primary(view).find(row => row.type === 'verify_document');
  assert.equal(values(document)['document.versionBasis'].value, null);
  assert.equal(Object.keys(values(document)).includes('document.currentVersion'), false);
  const correction = byId(exampleView(), exampleId('correction'));
  assert.ok(correction.differences.find(change => change.field === 'decision.authority' && change.before.value && change.after.value === null));
  assert.ok(correction.differences.find(change => change.field === 'decision.citation' && change.before.value && change.after.value === null));
  const checks = correction.impacts.checks;
  assert.ok(checks.find(check => check.id === 'customer_authority' && check.before === 'passed' && check.after === 'failed'));
  assert.ok(checks.find(check => check.id === 'decision_citation' && check.before === 'passed' && check.after === 'failed'));
});

test('source, recording and later outcome clocks remain distinct, including late-arriving decisions', () => {
  const view = exampleView(), requestId = exampleId('request_customer_review'), decisionId = exampleId('record_customer_approval');
  const request = byId(view, requestId), decision = byId(view, decisionId);
  assert.ok(Date.parse(decision.eventAt) < Date.parse(request.recordedAt));
  assert.ok(Date.parse(decision.recordedAt) > Date.parse(request.recordedAt));
  const earlier = exampleView(`as_of=${requestId}`);
  assert.equal(byId(earlier, decisionId), undefined);
  assert.equal(byId(earlier, requestId).outcomes.length, 0);
  assert.equal(request.outcomes[0].recordedAt, decision.recordedAt);
  assert.equal(decision.outcomes[0].recordedAt, byId(view, exampleId('resolve_requirement')).recordedAt);
});

test('as-recorded preserves the original, and as-effective applies only corrections known by the point', () => {
  const recorded = exampleView(), effective = exampleView('view=effective'), decisionId = exampleId('record_customer_approval'), correctionId = exampleId('correction');
  const original = byId(recorded, decisionId), corrected = byId(effective, decisionId), correction = byId(recorded, correctionId);
  assert.equal(values(original)['decision.reviewer'].value, walkthroughDecisions.reviewedBy);
  assert.equal(values(corrected)['decision.reviewer'].value, fixturePeople.customerContact);
  assert.equal(original.correctionIds.length, 0);
  assert.equal(original.supersededBy[0].id, correctionId);
  assert.equal(corrected.appliedCorrections[0].id, correctionId);
  assert.deepEqual(corrected.recordedValues, original.values);
  assert.equal(corrected.recordedAt, original.recordedAt);
  assert.equal(corrected.actor, original.actor);
  assert.equal(correction.supersedesRoot, original.id);
  assert.equal(correction.reasonRequired, true);
  const beforeCorrection = byId(exampleView(`view=effective&as_of=${exampleId('resolve_requirement')}`), decisionId);
  assert.deepEqual(beforeCorrection.values, original.values);
  assert.equal(beforeCorrection.appliedCorrections.length, 0);
  assert.equal(beforeCorrection.supersededBy.length, 0);
});

test('effective corrections do not erase original work outputs or the later results actually recorded', () => {
  const recorded = exampleView(), effective = exampleView('view=effective');
  assert.deepEqual(ids(effective.entries), ids(recorded.entries));
  assert.equal(effective.derivedCount, recorded.derivedCount);
  for (const row of primary(recorded)) assert.deepEqual(byId(effective, row.id).outcomes, row.outcomes);
  assert.deepEqual(effective.outcomes, recorded.outcomes);
});

test('correction chains retain each supersedes pointer and layer typed changes without mutating originals', () => {
  const state = buildHistoryExample(sample), previous = structuredClone(state);
  state.history.push(structuredClone(historyTestRecords.followupCorrection));
  const view = selectHistory(state, 'view=effective'), decision = byId(view, exampleId('record_customer_approval'));
  assert.deepEqual(decision.correctionIds, [exampleId('correction'), historyTestRecords.followupCorrection.id]);
  assert.equal(values(decision)['decision.version'].value, previous.document.version);
  assert.equal(values(decision)['decision.reviewer'].value, fixturePeople.customerContact);
  const second = byId(view, historyTestRecords.followupCorrection.id);
  assert.equal(second.supersedes, exampleId('correction'));
  assert.equal(second.supersedesRoot, decision.id);
  assert.deepEqual(state.history.slice(0, -1), previous.history);
});

test('a missing or forward correction target is an unresolved link, never a correction to another entry', () => {
  for (const target of ['missing', historyTestRecords.followupCorrection.id]) {
    const state = buildHistoryExample(sample);
    const correction = {...historyTestRecords.followupCorrection, supersedes: target};
    state.history.push(correction);
    const view = selectHistory(state, 'view=effective'), entry = byId(view, correction.id);
    assert.ok(entry.correctionError);
    assert.equal(entry.supersedesRoot, null);
    assert.ok(!byId(view, exampleId('record_customer_approval')).correctionIds.includes(correction.id));
  }
});

test('recording points disambiguate tied times while an ISO cutoff includes every record at that time', () => {
  const at = historyFixture.baseline.recordedAt;
  let state = transition(initialState, {type: 'request_review', at});
  state = transition(state, {type: 'approve_plan', at});
  assert.equal(selectHistory(state, `as_of=${state.history[0].id}`).recordedCount, 1);
  assert.equal(selectHistory(state, new URLSearchParams({as_of: at})).recordedCount, 2);
  assert.equal(selectHistory(state, `as_of=${historyFixture.baseline.id}`).recordedCount, 0);
  assert.equal(selectHistory(state, new URLSearchParams({as_of: historyTestRecords.beforeClock})).entries.length, 0);
  assert.equal(selectHistory(state, new URLSearchParams({as_of: historyTestRecords.beforeClock})).intervals, null);
});

test('unknown recording times stay unknown, are excluded from time cutoffs, and remain inspectable by sequence', () => {
  const state = approved();
  const event = state.history[0];
  event.at = null; event.recordedAt = null;
  const all = selectHistory(state);
  assert.equal(byId(all, event.id).recordedAt, null);
  assert.equal(byId(selectHistory(state, `as_of=${event.id}`), event.id).recordedAt, null);
  const timed = selectHistory(state, new URLSearchParams({as_of: historyTestRecords.afterClock}));
  assert.equal(timed.omittedUnknownTimes, 1);
  assert.equal(byId(timed, event.id), undefined);
});

test('literal times are not converted to the device zone and open duration is unknown without an endpoint', () => {
  const state = resolved();
  const opened = transition(state, {type: 'reopen_requirement', reason: historyTestRecords.editReason, at: historyTestRecords.literalTime});
  const view = selectHistory(opened), interval = view.intervals[0].intervals.at(-1);
  assert.equal(last(opened).recordedAt, historyTestRecords.literalTime);
  assert.ok(formatHistoryTime(last(opened).recordedAt).startsWith(historyTestRecords.literalTime));
  assert.equal(interval.durationMs, null);
  assert.equal(formatHistoryTime(null), 'not recorded');
  assert.equal(formatHistoryDuration(-1), 'not recorded');
});

test('requirement intervals track resolution, explicit reopening and a later resolution on the recording clock', () => {
  const first = resolved();
  let state = act(first, 'reopen_requirement', {reason: historyTestRecords.editReason, actor: fixturePeople.internalReviewer});
  const reopening = state.history.at(-1);
  state = act(state, 'request_customer_review');
  state = act(state, 'record_customer_approval', {reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
  state = act(state, 'resolve_requirement');
  const view = selectHistory(state), intervals = view.intervals[0].intervals;
  assert.equal(intervals.length, 2);
  assert.equal(intervals[0].openedAt, null);
  assert.equal(intervals[0].durationMs, null);
  assert.equal(intervals[0].observedMs, Date.parse(first.requirement.resolvedAt) - Date.parse(historyFixture.baseline.recordedAt));
  assert.equal(intervals[1].openedAt, reopening.recordedAt);
  assert.equal(intervals[1].resolvedAt, state.requirement.resolvedAt);
  assert.equal(intervals[1].durationMs, 3 * 60000);
  assert.equal(intervals[1].reason, historyTestRecords.editReason);
  assert.equal(intervals[1].openingActor, fixturePeople.internalReviewer);
  assert.equal(intervals[1].status, 'closed');
  const atReopen = selectHistory(state, `as_of=${reopening.id}`).intervals[0].intervals[1];
  assert.equal(atReopen.resolvedAt, null);
  assert.equal(atReopen.durationMs, 0);
  assert.equal(selectHistory(state, `as_of=${first.history.at(-1).id}`).intervals[0].intervals.length, 1);
});

test('multiple requirements keep independent intervals and unknown collections do not become zero', () => {
  const fixture = {...historyFixture, readiness: {...readinessFixture, requirements: [...readinessFixture.requirements, historyTestRecords.secondRequirement]}};
  const view = selectHistory(resolved(), '', fixture);
  assert.equal(view.intervals.length, 2);
  assert.equal(view.intervals[0].status, 'resolved');
  assert.equal(view.intervals[1].status, 'open');
  assert.equal(view.intervals[1].intervals[0].status, 'open');
  assert.equal(view.intervals[1].transitions.length, 0);
  assert.equal(selectHistory(initialState, '', {...historyFixture, readiness: {...readinessFixture, requirementsRecorded: false}}).intervals, null);
  assert.deepEqual(selectHistory(initialState, '', {...historyFixture, readiness: {...readinessFixture, requirements: []}}).intervals, []);
});

test('partial legacy records show only saved fields, with unknown metadata rather than later defaults', () => {
  const state = resolved();
  state.history = state.history.map(({historyRecord, recordedAt, actor, eventAt, reason, ...event}) => event);
  const approval = state.history.find(event => event.type === 'approve_plan');
  delete approval.planSnapshot.specifications;
  delete approval.planSnapshot.instructions;
  const view = selectHistory(state), entry = byId(view, approval.id);
  assert.equal(view.legacyCount, state.history.length);
  assert.equal(values(entry)['plan.specifications'].value, null);
  assert.equal(values(entry)['plan.instructions'].value, null);
  assert.equal(entry.actor, 'not recorded');
  assert.equal(entry.impacts.known, false);
  assert.equal(view.intervals[0].incomplete, true);
  assert.equal(view.intervals[0].intervals[0].resolvedAt, state.requirement.resolvedAt);
  const knownDecision = primary(view).find(row => row.type === 'record_customer_approval');
  assert.equal(values(knownDecision)['decision.text'].value, state.customerReview.evidenceText);
  assert.equal(knownDecision.actor, state.customerReview.recordedBy);
});

test('snapshot observation does not fill fields that were absent from old saved input', () => {
  const state = approved();
  delete state.plan.specifications; delete state.plan.instructions;
  const next = act(state, 'save_plan', {title: historyTestRecords.revisedTitle});
  assert.equal(next.history.at(-1).historyRecord.before.plan.specifications, undefined);
  const difference = last(next).differences.find(item => item.field === 'plan.specifications');
  assert.equal(difference.before.value, null);
  assert.equal(difference.after.value, next.plan.specifications);
});

test('missing actor, source time, recorded time and reason remain unknown in an incomplete review event', () => {
  const state = {...initialState, history: [historyTestRecords.missingEvent]};
  const entry = last(state);
  assert.equal(entry.actor, 'not recorded');
  assert.equal(entry.recordedAt, null);
  assert.equal(entry.eventAt, null);
  assert.equal(entry.reason, null);
  assert.equal(entry.reasonRequired, true);
  assert.equal(entry.outcomes.length, 0);
});

test('category counts partition known entries, filter real rows, and do not count hidden selections twice', () => {
  const state = resolved(), all = selectHistory(state);
  assert.equal(historyCategories.filter(category => category.id !== 'all').reduce((sum, category) => sum + all.counts[category.id], 0), all.counts.all);
  for (const category of historyCategories) {
    const view = selectHistory(state, new URLSearchParams({category: category.id, entry: state.history.at(-1).id}));
    assert.equal(view.visibleEntries.length, all.counts[category.id]);
    assert.deepEqual(view.counts, all.counts);
    assert.equal(view.selection.outside, !view.visibleEntries.some(entry => entry.id === view.selection.id));
    if (view.selection.outside) assert.equal(view.selectedOutside.id, state.history.at(-1).id);
  }
  assert.equal(selectHistory(state, 'category=correction').visibleEntries.length, 0);
});

test('oldest and newest sorting are stable, with derived rows remaining after their recording', () => {
  const state = resolved(), oldest = selectHistory(state), newest = selectHistory(state, 'order=newest');
  assert.deepEqual(primary(newest).map(row => row.id), primary(oldest).map(row => row.id));
  const rawNewest = newest.visibleEntries.filter(row => !row.derived);
  assert.deepEqual(ids(rawNewest), ids(oldest.visibleEntries.filter(row => !row.derived)).reverse());
  for (const row of newest.visibleEntries.filter(row => row.derived)) assert.ok(newest.visibleEntries.indexOf(row) > newest.visibleEntries.findIndex(parent => parent.id === row.parentId));
  assert.deepEqual(ids(selectHistory(state, 'order=newest').visibleEntries), ids(newest.visibleEntries));
});

test('unknown, ambiguous and future selections never open a substitute or reveal later content', () => {
  const state = resolved();
  for (const entry of ['', 'missing', `${state.history[0].id}&entry=${state.history[1].id}`]) {
    const view = selectHistory(state, `entry=${entry}`);
    assert.equal(view.selection.status, 'not_available');
    assert.equal(view.selection.resource, null);
  }
  const early = selectHistory(state, new URLSearchParams({entry: state.history.at(-1).id, as_of: state.history[0].id}));
  assert.equal(early.selection.resource, null);
  assert.equal(early.recordedCount, 1);
  assert.equal(JSON.stringify(historyExport(early)).includes(state.history.at(-1).title), false);
});

test('invalid or ambiguous known-by points withhold history instead of silently showing the latest state', () => {
  for (const query of ['as_of=missing', 'as_of=', 'as_of=latest&as_of=missing', `as_of=${historyTestRecords.literalTime}`]) {
    const view = selectHistory(resolved(), query);
    assert.equal(view.status, 'invalid_point');
    assert.deepEqual(view.entries, []);
    assert.equal(view.intervals, undefined);
    assert.equal(view.outcomes, undefined);
    assert.equal(historyExport(view), null);
  }
});

test('view, known-by, selection, filters, order and origin round-trip without losing unrelated query state', () => {
  const state = resolved(), entry = state.history.at(-1).id;
  const params = historySearch('context=kept', {entry, as_of: entry, view: 'effective', category: 'requirement', order: 'newest', returnTo: '/queue?sort=changed&attention=blocked'});
  const location = readHistoryLocation(params);
  assert.equal(location.entry, entry);
  assert.equal(location.asOf, entry);
  assert.equal(location.view, 'effective');
  assert.equal(location.order, 'newest');
  assert.deepEqual(readHistoryLocation(localURL(historyHref(params)).search), location);
  const closed = historySearch(params, {entry: null});
  assert.equal(closed.has('entry'), false);
  assert.equal(closed.get('context'), 'kept');
  assert.equal(closed.get('as_of'), entry);
  assert.equal(closed.get('returnTo'), params.get('returnTo'));
  const invalid = readHistoryLocation('view=effective&view=recorded&category=bad&order=bad');
  assert.equal(invalid.invalid, true);
  assert.equal(invalid.view, 'recorded');
  assert.equal(invalid.category, 'all');
});

test('trace links resolve to exact work items, checks and numbered units, with historical versions retained locally', () => {
  const state = resolved(), view = selectHistory(state), worklist = buildWorklist(state), readiness = advanceReadinessJournal(null, state);
  for (const entry of view.entries) {
    for (const link of entry.inspectionLinks) {
      const url = localURL(link.href);
      if (url.pathname === view.paths.workplan) {
        const selected = resolveWorklistEntry(worklist, url.pathname, url.search);
        assert.equal(selected.status, 'ready');
        assert.equal(url.hash, selected.targetId ? `#${selected.targetId}` : `#step-${selected.step}`);
      } else assert.equal(selectReadiness(readiness, url.search).selection.status, 'ready');
    }
    for (const ref of entry.references) {
      if (ref.entryId) assert.ok(byId(view, ref.entryId));
      if (ref.kind === 'unit') {
        const url = localURL(ref.href), unit = selectReadiness(readiness, url.search).unitSelection.resource;
        assert.equal(unit.cardId, ref.cardId);
        assert.equal(unit.n, ref.n);
        assert.equal(unit.text, ref.unit.text);
      }
    }
  }
});

test('history inspection links and safe returns preserve the full place and cannot navigate to another origin', () => {
  const view = selectHistory(resolved()), entry = view.entries.at(-1), params = new URLSearchParams({entry: entry.id, view: 'effective', as_of: entry.id, category: 'requirement', returnTo: '/queue?attention=blocked'});
  const destination = localURL(historyActionHref(entry.inspectionLinks[0].href, params));
  assert.equal(safeHistoryReturn(destination.search), historyHref(params));
  assert.equal(destination.searchParams.get('returnTo'), params.get('returnTo'));
  assert.equal(readHistoryLocation(localURL(safeHistoryReturn(destination.search)).search).entry, entry.id);
  for (const value of ['https://outside.test', '//outside.test', `${view.paths.history}/other`, `${view.paths.history}#fragment`, view.paths.workplan]) assert.equal(safeHistoryReturn(new URLSearchParams({historyReturn: value})), null);
});

test('export is bounded by known-by and the category view, with typed values rather than raw current-version flags', () => {
  const state = resolved(), point = state.history.find(row => row.type === 'approve_plan');
  const view = selectHistory(state, new URLSearchParams({as_of: point.id, category: 'plan', order: 'newest'})), exported = historyExport(view);
  assert.deepEqual(ids(exported.entries), ids(view.visibleEntries));
  assert.equal(exported.knownBy.id, point.id);
  assert.equal(exported.view, 'recorded');
  assert.ok(exported.entries.every(row => !('historyRecord' in row) && !('record' in row)));
  assert.equal(JSON.stringify(exported).includes(state.history.at(-1).title), false);
  assert.equal(exported.outcomes.find(item => item.id === 'commercial').value, initialState.commercial.label);
});

test('only a separately recorded commercial event can change the commercial outcome row', () => {
  const state = resolved(), record = historyTestRecords.commercialOutcome;
  const before = historyWorkflowSnapshot(state), after = {...before, commercial: record.commercial};
  const event = {...record, historyRecord: {schema: '1', before, after, contextBefore: historyEvidenceSnapshot(), contextAfter: historyEvidenceSnapshot(), actor: record.actor, reason: null, eventAt: null}};
  const source = {...state, commercial: record.commercial, history: [...state.history, event]};
  assert.equal(selectHistory(source, `as_of=${state.history.at(-1).id}`).outcomes.find(item => item.id === 'commercial').value, initialState.commercial.label);
  const view = selectHistory(source);
  assert.equal(view.outcomes.find(item => item.id === 'commercial').value, record.commercial.label);
  assert.equal(view.outcomes.find(item => item.id === 'commercial').recordedAt, record.at);
  assert.equal(view.counts.outcome, 1);
});

test('invalid history collections and duplicate identities produce a real unavailable state, not an empty success', () => {
  for (const history of [null, {}, [null], [historyTestRecords.missingEvent, historyTestRecords.missingEvent]]) {
    const view = selectHistory({...initialState, history});
    assert.equal(view.status, 'unavailable');
    assert.deepEqual(view.entries, []);
    assert.equal(view.counts, undefined);
    assert.equal(historyExport(view), null);
  }
});

test('unknown policy versions withhold historical checks and equivalent typed object key order creates no false diff', () => {
  const state = resolved(), entry = state.history.at(-1), unknownContext = {...historyEvidenceSnapshot(), policyVersion: 'unavailable'};
  assert.equal(historyImpacts(entry.historyRecord.before, entry.historyRecord.after, unknownContext, unknownContext).known, false);
  const source = values(last(state))['decision.citation'];
  const a = {...source, value: {kind: 'unit', cardId: readinessFixture.card.id, n: '1'}};
  const b = {...source, value: {n: '1', cardId: readinessFixture.card.id, kind: 'unit'}};
  assert.deepEqual(compareHistoryValues([a], [b]), []);
});

test('comparisons stay within the plan subject, rather than borrowing a decision from a different plan', () => {
  const first = approved();
  let state = {...initialState, plan: historyTestRecords.secondPlan, history: first.history};
  state = act(act(state, 'request_review'), 'approve_plan');
  const entry = last(state);
  assert.equal(entry.comparisonEntryId, null);
  assert.equal(values(entry)['plan.title'].value, historyTestRecords.secondPlan.title);
  assert.ok(entry.differences.every(change => !change.before.scope.planId || change.before.scope.planId === historyTestRecords.secondPlan.id));
});

test('a later plan edit compares its actual pre-event inputs, not an older draft or approval record', () => {
  const first = act(initialState, 'save_plan', {title: historyTestRecords.revisedTitle});
  const approvedState = act(act(first, 'request_review'), 'approve_plan');
  const second = act(approvedState, 'save_plan', {specifications: historyTestRecords.revisedSpecifications});
  const entry = last(second);
  assert.equal(entry.comparisonEntryId, null);
  assert.equal(entry.differences.find(change => change.field === 'plan.status').before.value, approvedState.plan.status);
  assert.equal(values(entry)['plan.revision'].value, String(second.plan.revision));
});

test('effective projections retain the original policy effects and never claim a corrected approval was established then', () => {
  const view = exampleView('view=effective'), entry = byId(view, exampleId('record_customer_approval'));
  assert.equal(entry.recordedImpacts.effects.find(effect => effect.id === 'hasCustomerApproval').status, 'recorded');
  assert.equal(entry.impacts.effects.some(effect => effect.id === 'hasCustomerApproval' && effect.status === 'recorded'), false);
  assert.equal(entry.recordedImpacts.evidenceChecks.find(check => check.id === 'customer_authority').result, 'passed');
  assert.equal(entry.impacts.evidenceChecks.find(check => check.id === 'customer_authority').result, 'failed');
  assert.equal(entry.impacts.authorityLabel, 'unidentified');
  assert.notEqual(entry.recordedImpacts.authorityLabel, 'unidentified');
  const output = view.entries.find(row => row.parentId === entry.id && row.output);
  assert.ok(output.upstreamCorrections.length);
  assert.equal(output.appliedCorrections.length, 0);
  assert.equal(output.values.find(value => value.field === 'output.proof').display.includes(walkthroughDocumentExamples.replacement.version), false);
});

test('cited source text compares speaker attribution even when the card, number and words are identical', () => {
  const first = decided();
  let state = act(act(first, 'resolve_requirement'), 'reopen_requirement', {reason: historyTestRecords.editReason});
  state = act(state, 'request_customer_review');
  state = act(state, 'record_customer_approval', {reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
  const decisions = state.history.filter(event => event.type === 'record_customer_approval');
  for (const event of decisions) event.historyRecord.contextAfter.decisionReceipt = structuredClone(historyTestRecords.pendingUnitCitation);
  const card = decisions.at(-1).historyRecord.contextAfter.card;
  card.units[0].speaker = structuredClone(card.units[1].speaker);
  const entry = last(state), difference = entry.differences.find(change => change.field === 'decision.citedText');
  assert.equal(difference.valueChanged, false);
  assert.equal(difference.attributionChanged, true);
  assert.equal(difference.scopeChanged, false);
  assert.equal(difference.before.type, 'source_text');
  assert.equal(difference.before.value, readinessFixture.card.units[0].text);
  assert.equal(difference.before.attribution.actor, readinessFixture.card.units[0].speaker.name);
  assert.equal(difference.after.attribution.actor, readinessFixture.card.units[1].speaker.name);
  assert.equal(entry.impacts.evidenceChecks.find(check => check.id === 'decision_citation').result, 'failed');
});

test('authority and receipt comparisons expose structured scope, contact, reference and inspection fields', () => {
  const decision = byId(exampleView(), exampleId('record_customer_approval'));
  const authority = values(decision)['decision.authority'], receipt = values(decision)['decision.citedReceipt'];
  assert.equal(authority.details.find(detail => detail.key === 'contactId').value, readinessEvidenceFixture.customerAuthority.contactId);
  assert.deepEqual(authority.details.find(detail => detail.key === 'evidenceRef').value, readinessEvidenceFixture.customerAuthority.evidenceRef);
  assert.equal(receipt.details.find(detail => detail.key === 'inspection.status').value, readinessEvidenceFixture.artifactReceipts[0].inspection.status);
  assert.equal(receipt.details.find(detail => detail.key === 'inspection.at').value, readinessEvidenceFixture.artifactReceipts[0].inspection.at);
});

test('malformed retained evidence does not invent a check result or crash the historical inspector', () => {
  const state = decided(), event = state.history.at(-1);
  event.historyRecord.contextAfter = {...event.historyRecord.contextAfter, card: {}, decisionReceipt: historyTestRecords.pendingUnitCitation};
  const entry = last(state);
  assert.equal(entry.impacts.known, false);
  assert.equal(values(entry)['decision.citedText'].value, null);
  assert.ok(entry.references.filter(reference => reference.kind === 'unit').every(reference => reference.unit === null));
});

test('resolution follow-up times appear only once an explicit or automatic reopening is recorded', () => {
  const before = resolved();
  for (const state of [act(before, 'reopen_requirement', {reason: historyTestRecords.editReason}), act(before, 'save_plan', {specifications: historyTestRecords.revisedSpecifications})]) {
    const resolution = byId(selectHistory(state), before.history.at(-1).id);
    assert.equal(resolution.outcomes[0].id, state.history.at(-1).id);
    assert.equal(resolution.outcomes[0].recordedAt, state.history.at(-1).recordedAt);
    assert.equal(byId(selectHistory(state, `as_of=${before.history.at(-1).id}`), resolution.id).outcomes.length, 0);
  }
});

test('unlogged loaded values are flagged but never copied back into the opening or earlier snapshots', () => {
  const state = {...initialState, plan: {...initialState.plan, title: historyTestRecords.revisedTitle}};
  const view = selectHistory(state);
  assert.equal(view.unrecordedStateChange, true);
  assert.equal(values(view.entries[0])['plan.title'].value, initialState.plan.title);
  assert.equal(view.recordedCount, 0);
  assert.equal(selectHistory(initialState).unrecordedStateChange, false);
  assert.equal(selectHistory(resolved()).unrecordedStateChange, false);
});

test('a separately recorded commercial result with a missing label stays unknown instead of reverting to an assumed result', () => {
  const state = resolved(), record = historyTestRecords.commercialOutcome, before = historyWorkflowSnapshot(state);
  const event = {...record, historyRecord: {schema: '1', before, after: {...before, commercial: {status: record.commercial.status}},
    contextBefore: historyEvidenceSnapshot(), contextAfter: historyEvidenceSnapshot()}};
  const view = selectHistory({...state, history: [...state.history, event]});
  assert.equal(view.outcomes.find(item => item.id === 'commercial').value, 'not recorded');
  assert.deepEqual(view.outcomes.find(item => item.id === 'commercial').entryIds, [event.id]);
});
