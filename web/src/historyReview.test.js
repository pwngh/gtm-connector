import test from 'node:test';
import assert from 'node:assert/strict';
import {approvalLetters, fixturePeople, historyFixture, historyReviewTestRecords as records, historyTestRecords, initialState, readinessEvidenceFixture, readinessFixture, walkthroughDocumentExamples} from './renewalFixtures.js';
import {customerDecisionSteps, walkthroughFields} from './customerDecision.js';
import {transition, transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal} from './readiness.js';
import {buildWorklist, resolveWorklistEntry} from './worklist.js';
import {buildHistoryExample, createHistorySelector, historyActionHref, historyExport, selectHistory} from './history.js';
import {compactHistoryState, historyContextKey, historyEvidenceSnapshot, hydrateHistoryRecord} from './historyRecording.js';
import {saveWorkflowState, workflowStorageKey} from './workflowStorage.js';

const act = (state, type, extra = {}) => transitionWalkthrough(state, {type, ...extra});
const approved = () => act(act(initialState, 'request_review'), 'approve_plan');
const received = () => act(approved(), 'obtain_document');
const verified = () => act(received(), 'verify_document');
const requested = () => act(verified(), 'request_customer_review');
// The letter, its inspection and the authority record; the decision itself carries no actor unless a test adds one.
const authorized = () => customerDecisionSteps.slice(0, -1).reduce((state, type) => act(state, type, walkthroughFields(state, type)), requested());
const decisionInput = () => {const {actor, ...fields} = walkthroughFields(authorized(), 'record_customer_approval'); return fields;};
const decided = () => act(authorized(), 'record_customer_approval', decisionInput());
const resolved = () => act(decided(), 'resolve_requirement');
const entry = (view, id) => view.entries.find(item => item.id === id);
const last = state => entry(selectHistory(state), state.history.at(-1).id);
const values = row => Object.fromEntries(row.values.map(field => [field.field, field]));
const example = historyFixture.examples.find(item => item.mode === 'example');
const exampleId = type => example.steps.find(step => step.action?.type === type || step.observation?.type === type || step.correction?.type === type).id;
const exampleView = params => selectHistory(initialState, new URLSearchParams({example: example.id, ...params}));
const withContext = (state, context) => {
  state.history.at(-1).historyRecord.contextAfter = structuredClone(context);
  return state;
};

test('time cutoffs retain an eligible prefix in both views, never a later snapshot of an excluded save', () => {
  let state = transition(initialState, {type: 'save_plan', title: historyTestRecords.revisedTitle, at: records.laterRecording});
  state = transition(state, {type: 'request_review', at: records.earlierRecording});
  for (const view of ['recorded', 'effective']) {
    const result = selectHistory(state, new URLSearchParams({view, as_of: records.cutoff, entry: state.history.at(-1).id}));
    assert.deepEqual(result.entries.map(item => item.id), [historyFixture.baseline.id]);
    assert.equal(result.recordedCount, 0);
    assert.equal(result.withheldRecordings, 2);
    assert.equal(result.selection.status, 'not_available');
    assert.equal(result.dataQuality.find(item => item.code === 'recording_times_out_of_order').count, 1);
    assert.ok(!JSON.stringify(historyExport(result)).includes(historyTestRecords.revisedTitle));
  }
  const bySequence = selectHistory(state, new URLSearchParams({as_of: state.history.at(-1).id}));
  assert.equal(bySequence.recordedCount, 2);
  assert.equal(values(entry(bySequence, state.history.at(-1).id))['plan.title'].value, historyTestRecords.revisedTitle);
});

test('an excluded or undated recording also withholds later dependent outputs after an eligible prefix', () => {
  for (const unknown of [false, true]) {
    let state = transition(initialState, {type: 'request_review', at: records.earlierRecording});
    state = transition(state, {type: 'approve_plan', at: records.laterRecording});
    state = transition(state, {type: 'obtain_document', at: records.earlierRecording});
    if (unknown) {state.history[1].at = null; state.history[1].recordedAt = null;}
    const result = selectHistory(state, new URLSearchParams({as_of: records.cutoff}));
    assert.equal(result.recordedCount, 1);
    assert.equal(result.derivedCount, 0);
    assert.ok(!result.entries.some(item => item.type === 'obtain_document'));
    assert.equal(result.omittedUnknownTimes, Number(unknown));
  }
});

test('retained evidence is bounded by its own receipt and inspection times, not the containing snapshot', () => {
  const context = historyEvidenceSnapshot(readinessEvidenceFixture);
  context.artifactReceipts.push(structuredClone(records.lateReceipt));
  context.decisionReceipt.evidenceRef = {kind: 'receipt', id: records.lateReceipt.id};
  const state = withContext(decided(), context);
  for (const view of ['recorded', 'effective']) {
    const result = selectHistory(state, new URLSearchParams({view, as_of: records.cutoff}));
    const row = entry(result, state.history.at(-1).id);
    assert.equal(values(row)['decision.citedReceipt'].value, null);
    assert.equal(row.references.find(item => item.kind === 'basis' && item.id === context.decisionReceipt.id).receipt, null);
    assert.equal(row.impacts.evidenceChecks.find(item => item.id === 'decision_citation').result, 'failed');
    assert.equal(row.evidenceWithheld, true);
    assert.ok(!JSON.stringify(historyExport(result)).includes(records.lateReceipt.artifactId));
  }
  context.artifactReceipts.at(-1).receivedAt = records.earlierRecording;
  const inspectedLater = last(withContext(decided(), context));
  const receipt = inspectedLater.references.find(item => item.id === context.decisionReceipt.id).receipt;
  assert.equal(receipt.id, records.lateReceipt.id);
  assert.equal(receipt.inspection, null);
});

test('future contacts and source reads are withheld even when the log snapshot was recorded earlier', () => {
  const context = historyEvidenceSnapshot(readinessEvidenceFixture);
  context.contacts[0].recordedAt = records.laterRecording;
  context.card.units[0].readAt = records.laterRecording;
  const row = last(withContext(decided(), context));
  assert.equal(row.impacts.authorityLabel, 'unidentified');
  assert.equal(row.references.find(item => item.kind === 'unit' && item.n === context.card.units[0].n).unit, null);
  assert.equal(row.impacts.evidenceChecks.find(item => item.id === 'customer_authority').result, 'failed');
});

test('only explicit action actors become event actors; default people remain separately typed record fields', () => {
  // The decision's explicit actor is its recorder, the signed-in demo principal; other actors leave their fields alone.
  for (const [state, type, extra, field, expected, explicitValue = expected] of [
    [act(initialState, 'request_review'), 'approve_plan', {}, 'plan.approver', fixturePeople.internalReviewer],
    [received(), 'verify_document', {}, 'document.verifiedBy', initialState.plan.owner],
    [verified(), 'request_customer_review', {}, 'decision.requestedBy', initialState.plan.owner],
    [authorized(), 'record_customer_approval', decisionInput(), 'decision.recordedBy', fixturePeople.internalReviewer, fixturePeople.otherAccountOwner],
  ]) {
    const next = act(state, type, extra), row = last(next);
    assert.equal(next.history.at(-1).actor, null);
    assert.equal(row.actor, 'not recorded');
    assert.equal(values(row)[field].value, expected);
    assert.equal(values(row)[field].origin, 'walkthrough_default');
    const journal = advanceReadinessJournal(advanceReadinessJournal(null, state), next);
    assert.equal(journal.changes.cause.actor, null);
    const explicit = act(state, type, {...extra, actor: fixturePeople.otherAccountOwner});
    assert.equal(last(explicit).actor, fixturePeople.otherAccountOwner);
    assert.equal(values(last(explicit))[field].value, explicitValue);
  }
});

test('explicit typed approver, inspector and recorder fields do not imply an event actor or a default', () => {
  for (const [state, type, extra, field] of [
    [act(initialState, 'request_review'), 'approve_plan', {approver: fixturePeople.internalReviewer}, 'plan.approver'],
    [received(), 'verify_document', {verifiedBy: fixturePeople.accountOwner}, 'document.verifiedBy'],
    [authorized(), 'record_customer_approval', {...decisionInput(), recordedBy: fixturePeople.internalReviewer}, 'decision.recordedBy'],
  ]) {
    const row = last(act(state, type, extra));
    assert.equal(row.actor, 'not recorded');
    assert.equal(values(row)[field].origin, 'recorded_field');
  }
  const row = last(act(authorized(), 'record_customer_approval', {...decisionInput(), actor: '  '}));
  assert.equal(row.actor, 'not recorded');
  assert.equal(values(row)['decision.reviewer'].attribution.actor, null);
});

test('older schema actors without explicit-action provenance are not promoted from reducer defaults', () => {
  const state = approved(), event = state.history.at(-1);
  event.historyRecord.schema = '1';
  event.historyRecord.actor = fixturePeople.internalReviewer;
  delete event.historyRecord.actorSource;
  event.actor = fixturePeople.internalReviewer;
  assert.equal(last(state).actor, 'not recorded');
  assert.equal(advanceReadinessJournal(advanceReadinessJournal(null, initialState), state).changes.cause.actor, null);
});

test('effective dependents retain original decision provenance and flag corrected snapshots on either side', () => {
  const original = exampleView({}), effective = exampleView({view: 'effective'});
  const decisionId = exampleId('record_customer_approval'), correctionId = exampleId('correction');
  for (const type of ['resolve_requirement', 'authority_observation', 'reopen_requirement']) {
    const recorded = entry(original, exampleId(type)), projected = entry(effective, recorded.id);
    assert.ok(recorded.upstreamCorrections.some(item => item.id === correctionId));
    assert.ok(projected.upstreamCorrections.some(item => item.id === correctionId));
    assert.equal(projected.correctedProjection, true);
    assert.deepEqual(projected.references, recorded.references);
    assert.equal(projected.references.find(item => item.kind === 'review').entryId, decisionId);
    assert.deepEqual(projected.recordedValues, recorded.values);
  }
  const resolution = entry(effective, exampleId('resolve_requirement'));
  assert.equal(values(resolution)['decision.reviewer'].value, fixturePeople.customerContact);
  assert.equal(resolution.recordedValues.find(item => item.field === 'decision.reviewer').value, approvalLetters[0].signatory.name);
  assert.deepEqual(original.entries.map(item => item.id), effective.entries.map(item => item.id));
});

test('a correction to resolution status changes only projected status, not recorded totals or intervals', () => {
  const state = resolved(), resolution = state.history.at(-1);
  state.history.push({...structuredClone(records.resolutionCorrection), supersedes: resolution.id});
  const recorded = selectHistory(state), effective = selectHistory(state, 'view=effective');
  for (const result of [recorded, effective]) {
    assert.equal(result.outcomes[0].recordedCount, 1);
    assert.deepEqual(result.outcomes[0].entryIds, [resolution.id]);
    assert.equal(result.outcomes[0].recordedStatuses[0].status, 'resolved');
    assert.equal(result.intervals[0].status, 'resolved');
    assert.equal(result.intervals[0].intervals[0].endEntryId, resolution.id);
  }
  assert.equal(effective.outcomes[0].projectedStatuses[0].status, 'open');
  assert.equal(effective.intervals[0].projectedStatus, 'open');
  assert.equal(values(entry(effective, resolution.id))['requirement.status'].value, 'open');
  assert.equal(values(entry(recorded, resolution.id))['requirement.status'].value, 'resolved');
});

const malformedInputs = [
  ['requirements object', record => {record.after.requirements = {};}],
  ['null requirement', record => {record.before.requirements = [null];}],
  ['review text object', record => {record.after.customerReview.evidenceText = {};}],
  ['null contact', record => {record.contextAfter.contacts = [null];}],
  ['null receipt', record => {record.contextAfter.artifactReceipts = [null];}],
  ['invalid inspection', record => {record.contextAfter.artifactReceipts[0].inspection = {status: {}};}],
  ['null source unit', record => {record.contextAfter.card.units = [null];}],
  ['invalid source fields', record => {record.contextAfter.card.units[2].fields = {};}],
  ['null source field', record => {record.contextAfter.card.units[2].fields = [null];}],
  ['source text object', record => {record.contextAfter.card.units[0].text = {};}],
  ['null source', record => {record.contextAfter.card.sources = [null];}],
  ['null gap', record => {record.contextAfter.card.gaps = [null];}],
  ['invalid evidence reference', record => {record.contextAfter.decisionReceipt.evidenceRef = [];}],
];
for (const [label, mutate] of malformedInputs) test(`malformed ${label} is a partial record, not a crash or a projected success`, () => {
  const state = decided(), event = state.history.at(-1);
  const record = structuredClone(hydrateHistoryRecord(event.historyRecord, state.historyContexts));
  record.contextAfter = historyEvidenceSnapshot(readinessEvidenceFixture);
  mutate(record);
  event.historyRecord = record;
  const result = selectHistory(state, new URLSearchParams({entry: event.id}));
  assert.equal(result.status, 'ready');
  assert.equal(result.selection.resource.complete, false);
  assert.equal(result.selection.resource.impacts.known, false);
  assert.equal(result.malformedCount, 1);
  assert.equal(result.legacyCount, 1);
  assert.ok(historyExport(result));
});

test('malformed correction patches are withheld without changing the target or its dependents', () => {
  for (const changes of [
    {after: {requirements: {}}}, {after: {requirements: [null]}}, {after: {plan: {owner: {}}}},
    {contextAfter: {contacts: [null]}}, {contextAfter: {artifactReceipts: [null]}},
    {contextAfter: {card: {...readinessFixture.card, units: [null]}}},
  ]) {
    const state = resolved(), target = state.history.at(-1);
    const correction = {...records.resolutionCorrection, supersedes: target.id, changes};
    state.history.push(correction);
    const result = selectHistory(state, 'view=effective');
    assert.ok(entry(result, correction.id).correctionError);
    assert.equal(entry(result, target.id).appliedCorrections.length, 0);
    assert.equal(result.intervals[0].status, 'resolved');
    assert.equal(result.intervals[0].projectedStatus, 'resolved');
    assert.equal(result.outcomes[0].recordedCount, 1);
  }
});

test('the first reliable snapshot after a legacy gap re-establishes open state without inventing a reopening time', () => {
  const completed = resolved();
  let state = act(completed, 'save_plan', {title: historyTestRecords.revisedTitle});
  const edit = state.history.at(-1);
  delete edit.historyRecord;
  state = act(state, 'request_review');
  const request = state.history.at(-1), result = selectHistory(state), interval = result.intervals[0].intervals.at(-1);
  assert.equal(request.historyRecord.before.requirement.status, 'open');
  assert.equal(request.historyRecord.after.requirement.status, 'open');
  assert.equal(result.intervals[0].status, 'open');
  assert.equal(result.intervals[0].incomplete, true);
  assert.equal(result.intervals[0].intervals.length, 2);
  assert.equal(interval.observationOnly, true);
  assert.equal(interval.startEntryId, request.id);
  assert.equal(interval.openedAt, null);
  assert.equal(interval.durationMs, null);
  assert.equal(interval.observedAt, request.recordedAt);
  assert.equal(result.outcomes[0].recordedCount, 1);
  assert.equal(selectHistory(state, `as_of=${edit.id}`).intervals[0].status, null);
  assert.equal(selectHistory(state, `as_of=${completed.history.at(-1).id}`).intervals[0].status, 'resolved');
});

test('a different document at the same version produces a distinct structured collection output', () => {
  const first = received(), state = act(first, 'obtain_document', {document: records.sameVersionReplacement});
  const result = selectHistory(state), receipts = result.entries.filter(item => item.output?.kind === 'collection');
  assert.equal(receipts.length, 2);
  assert.equal(receipts[0].output.identity.version, receipts[1].output.identity.version);
  assert.notEqual(receipts[0].output.identity.documentId, receipts[1].output.identity.documentId);
  assert.equal(receipts[1].output.identity.receiptRecording, state.history.at(-1).id);
  assert.equal(last(state).references.find(item => item.kind === 'document').entryId, state.history.at(-1).id);
  assert.equal(result.outcomes[1].currentCount, 2);
});

test('tied receipt times still identify separate committed recordings, while a receipt no-op adds no output', () => {
  const first = received(), recording = first.history.at(-1);
  const second = transition(first, {type: 'obtain_document', at: recording.recordedAt,
    document: {...first.document, currentVersion: walkthroughDocumentExamples.replacement.version}});
  const collections = selectHistory(second).entries.filter(item => item.output?.kind === 'collection');
  assert.equal(collections.length, 2);
  assert.notEqual(collections[0].output.identity.receiptRecording, collections[1].output.identity.receiptRecording);
  assert.equal(second.document.receivedAt, first.document.receivedAt);
  const noOp = act(second, 'obtain_document', {document: second.document});
  assert.equal(noOp.history, second.history);
  assert.equal(selectHistory(noOp).entries.filter(item => item.output?.kind === 'collection').length, 2);
});

test('500 edits persist one context by content key and stay well below the former storage footprint', () => {
  let state = initialState;
  for (let i = 0; i < 500; i++) state = act(state, 'save_plan', {title: i % 2 ? initialState.plan.title : historyTestRecords.revisedTitle});
  const keys = Object.keys(state.historyContexts), context = historyEvidenceSnapshot();
  assert.deepEqual(keys, [historyContextKey(context)]);
  assert.deepEqual(state.historyContexts[keys[0]], context);
  assert.equal(state.history.length, 500);
  assert.ok(Buffer.byteLength(JSON.stringify(state)) < 2_000_000);
  assert.ok(state.history.every(event => event.historyRecord.contextBeforeId === keys[0]
    && !Object.hasOwn(event.historyRecord, 'contextBefore') && !Object.hasOwn(event.historyRecord, 'contextAfter') && !Object.hasOwn(event.historyRecord, 'contextAfterId')));
  const restored = JSON.parse(JSON.stringify(state));
  assert.deepEqual(hydrateHistoryRecord(restored.history.at(-1).historyRecord, restored.historyContexts).contextAfter, context);
});

test('compaction migrates inline contexts without mutating old records and stores an after-context only when different', () => {
  const state = approved(), originalContext = historyEvidenceSnapshot(), changedContext = historyEvidenceSnapshot(records.contextChange);
  for (const event of state.history) event.historyRecord = {...event.historyRecord, contextBefore: originalContext, contextAfter: originalContext};
  state.history.at(-1).historyRecord.contextAfter = changedContext;
  const before = structuredClone(state), compacted = compactHistoryState(state);
  assert.deepEqual(state, before);
  assert.equal(Object.keys(compacted.historyContexts).length, 2);
  assert.ok(!Object.hasOwn(compacted.history[0].historyRecord, 'contextAfterId'));
  assert.notEqual(compacted.history.at(-1).historyRecord.contextBeforeId, compacted.history.at(-1).historyRecord.contextAfterId);
  assert.deepEqual(hydrateHistoryRecord(compacted.history.at(-1).historyRecord, compacted.historyContexts).contextAfter, changedContext);
  assert.deepEqual(compacted.history.map(item => item.id), state.history.map(item => item.id));
  assert.equal(compactHistoryState(compacted), compacted);
});

test('a failed save reports failure and preserves both the in-tab state and the previous saved data for retry', () => {
  const persisted = new Map(), storage = {setItem: (key, value) => persisted.set(key, value)};
  const state = approved();
  assert.equal(saveWorkflowState(state, storage).status, 'saved');
  const prior = persisted.get(workflowStorageKey), next = act(state, 'obtain_document');
  const failure = saveWorkflowState(next, {setItem() {throw new Error('Storage full');}});
  assert.equal(failure.status, 'failed');
  assert.equal(failure.state, next);
  assert.equal(persisted.get(workflowStorageKey), prior);
  assert.equal(saveWorkflowState(next, storage).status, 'saved');
  assert.deepEqual(JSON.parse(persisted.get(workflowStorageKey)).history.map(item => item.id), next.history.map(item => item.id));
});

test('selection, filters and sorting reuse one projection while temporal, example and state changes recompute it', () => {
  const state = resolved(), select = createHistorySelector(), first = select(state), lastId = state.history.at(-1).id;
  for (const query of [`entry=${lastId}`, `entry=${lastId}&category=document`, 'order=newest', 'returnTo=%2Fqueue', 'category=invalid']) {
    const result = select(state, query);
    assert.equal(result.entries, first.entries);
    assert.equal(result.outcomes, first.outcomes);
    assert.equal(result.counts, first.counts);
  }
  const early = select(state, `as_of=${state.history[0].id}&entry=${lastId}`);
  assert.notEqual(early.entries, first.entries);
  assert.equal(early.selection.status, 'not_available');
  const effective = select(state, `as_of=${state.history[0].id}&view=effective`);
  assert.notEqual(effective.entries, early.entries);
  const sample = select(state, 'example=correction');
  assert.notEqual(sample.entries, effective.entries);
  assert.notEqual(select(act(state, 'reopen_requirement', {reason: historyTestRecords.editReason})).entries, sample.entries);
});

test('reasons come only from explicit event reasons, never fallback review notes or legacy detail text', () => {
  const review = act(initialState, 'request_review'), noReason = act(review, 'request_changes');
  const row = last(noReason);
  assert.equal(row.reasonRequired, true);
  assert.equal(row.reason, null);
  assert.equal(values(row)['plan.reviewReason'].value, null);
  assert.equal(values(row)['plan.reviewNote'].origin, 'walkthrough_default');
  const legacy = {...noReason, history: noReason.history.map(({historyRecord, reason, ...event}) => event)};
  assert.equal(last(legacy).reason, null);
  const explicit = act(review, 'request_changes', {note: historyTestRecords.editReason});
  assert.equal(last(explicit).reason, explicit.history.at(-1).reason);
  assert.equal(last(explicit).reason, historyTestRecords.editReason);
});

test('unchanged plan text has a scope difference when carried into another revision', () => {
  const state = act(approved(), 'save_plan', {title: historyTestRecords.revisedTitle}), row = last(state);
  const change = row.differences.find(item => item.field === 'plan.instructions');
  assert.equal(change.valueChanged, false);
  assert.equal(change.scopeChanged, true);
  assert.equal(change.before.scope.planId, change.after.scope.planId);
  assert.notEqual(change.before.scope.planRevision, change.after.scope.planRevision);
  assert.equal(change.after.scope.planRevision, String(state.plan.revision));
});

test('export includes a displayed outside-filter selection separately without changing filter counts or bounds', () => {
  const state = resolved(), selected = state.history.at(-1).id;
  const result = selectHistory(state, new URLSearchParams({category: 'document', entry: selected})), exported = historyExport(result);
  assert.ok(exported.entries.every(item => item.category === 'document'));
  assert.equal(exported.entries.length, result.counts.document);
  assert.equal(exported.selectedOutside.outsideFilter, true);
  assert.equal(exported.selectedOutside.entry.id, selected);
  assert.ok(!Object.hasOwn(exported.selectedOutside.entry, 'recordedRecord'));
  const bounded = selectHistory(state, new URLSearchParams({category: 'document', entry: selected, as_of: state.history[0].id}));
  assert.equal(historyExport(bounded).selectedOutside, null);
});

test('the correction example changes attribution only and keeps authority withdrawal as a separate recording', () => {
  const result = exampleView({view: 'effective'}), original = exampleView({});
  const decision = entry(result, exampleId('record_customer_approval')), recorded = entry(original, decision.id);
  const correction = entry(result, exampleId('correction')), withdrawal = entry(result, exampleId('authority_observation'));
  assert.deepEqual(decision.record.after.document, recorded.record.after.document);
  assert.equal(values(decision)['decision.version'].value, recorded.record.after.document.version);
  assert.equal(values(decision)['decision.document'].value, recorded.record.after.document.id);
  assert.deepEqual(values(decision)['decision.citation'].value, values(recorded)['decision.citation'].value);
  assert.ok(correction.differences.every(item => item.field === 'decision.reviewer'));
  assert.deepEqual(decision.record.contextAfter, recorded.record.contextAfter);
  assert.equal(withdrawal.record.contextAfter.customerAuthority.status, 'withdrawn');
  assert.deepEqual(withdrawal.record.contextAfter.decisionReceipt, recorded.record.contextAfter.decisionReceipt);
  assert.deepEqual(withdrawal.record.contextAfter.artifactReceipts, recorded.record.contextAfter.artifactReceipts);
  assert.equal(withdrawal.impacts.evidenceChecks.find(item => item.id === 'customer_authority').result, 'failed');
});

test('authority, decision and version bases appear only on entries that use those bases', () => {
  const result = exampleView({});
  for (const type of ['request_review', 'approve_plan', 'request_customer_review', 'save_plan']) {
    const row = entry(result, exampleId(type));
    assert.equal(row.references.some(item => item.kind === 'basis'), false);
    assert.equal(row.values.some(item => ['decision.authority', 'decision.citation'].includes(item.field)), false);
  }
  // The version basis comes from the inspected letter, so the earlier document inspection has none.
  const inspection = entry(result, exampleId('verify_document'));
  assert.deepEqual(inspection.references.filter(item => item.kind === 'basis').map(item => item.id), []);
  const letter = entry(result, exampleId('inspect_approval_letter'));
  assert.deepEqual(letter.references.filter(item => item.kind === 'basis').map(item => item.id), ['version-basis-receipt-approval-letter-v4-1']);
  assert.equal(entry(result, exampleId('record_customer_approval')).references.filter(item => item.kind === 'basis').length, 3);
});

test('outcome summaries keep current recorded state and distinguish corrected output recordings from current outputs', () => {
  const reopened = act(resolved(), 'reopen_requirement', {reason: historyTestRecords.editReason}), result = selectHistory(reopened);
  assert.equal(result.outcomes[0].recordedStatuses[0].status, 'open');
  assert.equal(result.outcomes[0].recordedCount, 1);
  assert.equal(result.outcomes[1].recordedCount, 4);
  assert.equal(result.outcomes[1].currentCount, 3);
  const corrected = exampleView({as_of: exampleId('correction'), view: 'effective'});
  assert.equal(corrected.outcomes[1].recordedCount, 4);
  assert.equal(corrected.outcomes[1].correctedCount, 1);
  assert.equal(corrected.outcomes[1].currentCount, 3);
});

test('History Work plan links do not fabricate a worklist return, but preserve an explicit origin', () => {
  const state = resolved(), row = last(state), data = buildWorklist(state);
  for (const returnTo of [null, '/queue?order=newest']) {
    const search = new URLSearchParams({entry: row.id, ...(returnTo ? {returnTo} : {})});
    const url = new URL(historyActionHref(row.inspectionLinks[0].href, search), 'http://local.test');
    assert.equal(resolveWorklistEntry(data, url.pathname, url.search).returnTo, returnTo);
    assert.ok(url.searchParams.has('historyReturn'));
  }
});

test('new event identities cannot retarget bookmarks or exports after a reset or reload', () => {
  const first = act(initialState, 'request_review'), id = first.history.at(-1).id;
  const exported = historyExport(selectHistory(first, `entry=${id}&as_of=${id}`));
  const reset = act(first, 'reset'), next = act(reset, 'request_review');
  assert.notEqual(next.history.at(-1).id, id);
  assert.equal(selectHistory(next, `entry=${id}`).selection.status, 'not_available');
  assert.equal(selectHistory(next, `as_of=${id}`).status, 'invalid_point');
  assert.ok(!selectHistory(next).entries.some(item => item.id === exported.selectedEntryId));
  const reloaded = act(JSON.parse(JSON.stringify(next)), 'approve_plan');
  assert.equal(new Set([...first.history, ...reloaded.history].map(item => item.id)).size, 3);
  assert.equal(reloaded.history[0].id, next.history[0].id);
});


test('receipt metadata cannot leak a later recording or inspection through an earlier read timestamp', () => {
  const context = historyEvidenceSnapshot(readinessEvidenceFixture);
  context.artifactReceipts[0].recordedAt = records.laterRecording;
  context.card.units[0].receipt.inspection = {status: 'accepted', at: records.laterRecording};
  const row = last(withContext(decided(), context));
  assert.equal(values(row)['decision.citedReceipt'].value, null);
  const unit = row.references.find(reference => reference.kind === 'unit' && reference.n === context.card.units[0].n).unit;
  assert.equal(unit.receipt.inspection, null);
  assert.equal(row.impacts.evidenceChecks.find(item => item.id === 'decision_citation').result, 'failed');
});


test('a missing inline legacy after-context stays unknown through compaction and projection', () => {
  const state = approved(), event = state.history.at(-1);
  event.historyRecord = {...event.historyRecord, schema: '1', contextBefore: historyEvidenceSnapshot()};
  delete event.historyRecord.contextBeforeId;
  delete event.historyRecord.contextAfter;
  const compacted = compactHistoryState(state), retained = hydrateHistoryRecord(compacted.history.at(-1).historyRecord, compacted.historyContexts);
  assert.equal(retained.contextAfter, null);
  assert.equal(last(compacted).complete, false);
  assert.equal(last(compacted).impacts.known, false);
});

test('missing snapshot identities and revision bindings cannot turn equal unknown values into an approval', () => {
  for (const mutate of [snapshot => {delete snapshot.plan.revision; delete snapshot.plan.approvedRevision;},
    snapshot => {delete snapshot.document.id; delete snapshot.customerReview.documentId;},
    snapshot => {delete snapshot.document.version; delete snapshot.document.currentVersion; delete snapshot.customerReview.documentVersion;}]) {
    const state = decided(), event = state.history.at(-1);
    mutate(event.historyRecord.after);
    const row = last(state);
    assert.equal(row.complete, false);
    assert.equal(row.impacts.known, false);
    assert.equal(row.impacts.outputs.length, 0);
  }
});
