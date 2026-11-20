import {historyFixture} from './renewalFixtures.js';
import {derive, transition, workflowTransitionChecks} from './workflow.js';
import {compareReadinessChecks, evaluateReadiness, readinessHref, resolveEvidenceReference} from './readiness.js';
import {buildWorklist, formatWorklistTime, safeWorklistReturn} from './worklist.js';
import {compactHistoryState, historyEvidenceSnapshot, historyEventActor, historyWorkflowSnapshot, hydrateHistoryRecord} from './historyRecording.js';
import {partialHistoryContext, partialHistorySnapshot, validHistoryChanges, validHistoryContext, validHistorySnapshot} from './historyValidation.js';

const copy = value => value == null ? value : JSON.parse(JSON.stringify(value));
const canonical = value => Array.isArray(value) ? value.map(canonical) : value && typeof value === 'object' ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const same = (a, b) => JSON.stringify(canonical(a ?? null)) === JSON.stringify(canonical(b ?? null));
const text = value => typeof value === 'string' && value.trim() ? value : null;
const id = value => value == null ? null : String(value);
const timestamp = value => typeof value === 'string' && /(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null;
export const formatHistoryTime = value => value ? formatWorklistTime(value) : 'not recorded';

export const historyCategories = [
  {id: 'all', label: 'All entries'}, {id: 'plan', label: 'Plan and internal review'},
  {id: 'document', label: 'Document receipt and inspection'}, {id: 'customer', label: 'Customer review'},
  {id: 'requirement', label: 'Requirement transitions'}, {id: 'output', label: 'Work-item outputs'},
  {id: 'correction', label: 'Corrections'}, {id: 'outcome', label: 'Commercial outcomes'}, {id: 'context', label: 'Opening context'},
];
const kinds = {
  opening_snapshot: ['context', 'Opening snapshot', 'plan_review'],
  save_plan: ['plan', 'Plan revision', 'plan_review'], request_review: ['plan', 'Internal review request', 'plan_review'],
  approve_plan: ['plan', 'Internal review decision', 'plan_review'], request_changes: ['plan', 'Internal review decision', 'plan_review'],
  obtain_document: ['document', 'Document receipt', 'collection'], verify_document: ['document', 'Document inspection', 'inspection'],
  request_customer_review: ['customer', 'Customer review request', 'customer_review'], record_customer_approval: ['customer', 'Customer decision', 'customer_review'],
  resolve_requirement: ['requirement', 'Requirement resolution', null], reopen_requirement: ['requirement', 'Requirement reopening', null],
  requirement_transition: ['requirement', 'Requirement transition', null], work_item_output: ['output', 'Work-item output', null],
  authority_observation: ['customer', 'Authority observation', 'customer_review'],
  commercial_outcome: ['outcome', 'Commercial outcome', null], correction: ['correction', 'Correction', null],
};
const planLabels = {title: 'Title', owner: 'Task owner', specifications: 'Specifications', instructions: 'Instructions'};
const stateFor = snapshot => ({...snapshot, history: [], error: null});
const completeSnapshot = validHistorySnapshot;
const validContext = validHistoryContext;
const completeRecord = record => ['1', '2'].includes(record?.schema) && completeSnapshot(record.before) && completeSnapshot(record.after)
  && validContext(record.contextBefore) && validContext(record.contextAfter);
const unknownImpacts = () => ({known: false, effects: [], checks: [], evidenceChecks: [], outputs: []});
const contextFor = (context, fixture) => ({...fixture.readiness, ...context, contacts: context?.contacts || [], artifactReceipts: context?.artifactReceipts || []});
const requirementsIn = snapshot => Array.isArray(snapshot?.requirements) ? snapshot.requirements : snapshot?.requirement ? [snapshot.requirement] : [];
const basisKinds = {
  obtain_document: ['version'], verify_document: ['version'], authority_observation: ['authority', 'decision'],
  record_customer_approval: ['authority', 'decision', 'version'], resolve_requirement: ['authority', 'decision', 'version'],
  reopen_requirement: ['authority', 'decision', 'version'], requirement_transition: ['authority', 'decision', 'version'],
};

export function historyScopeLabel(scope) {
  return Object.entries(scope || {}).filter(([, value]) => value != null).map(([key, value]) => `${({agreementId: 'Agreement', planId: 'Plan', planRevision: 'Plan revision', documentId: 'Document', documentVersion: 'Version', requirementId: 'Requirement', workItemId: 'Work item', cardId: 'Card', unitNumber: 'Unit'})[key] || key}: ${value}`).join(' · ') || 'Scope not recorded';
}

function displayValue(type, value) {
  if (value == null || value === '') return type === 'authority' ? 'unidentified' : 'not recorded';
  if (typeof value !== 'object') return String(value);
  if (type === 'citation') return value.kind === 'unit' ? `Card ${value.cardId} · Unit ${value.n}` : `${value.kind || 'Record'} ${value.id || 'not recorded'}`;
  if (type === 'source_fields') return value.map(field => `${field.label}: ${field.value ?? 'not recorded'}`).join('\n');
  return [value.id || null, value.revision ? `revision ${value.revision}` : null, value.version || null, value.status || null].filter(Boolean).join(' · ') || 'not recorded';
}

function referenceDetails(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [];
  const labels = {agreementId: 'Agreement in the record', documentId: 'Document in the record', documentVersion: 'Version in the record',
    contactId: 'Contact in the record', artifactId: 'Artifact', recordedAt: 'Recorded time', receivedAt: 'Receipt time', readAt: 'Read time',
    evidenceRef: 'Supporting evidence reference', source: 'Independent source reference'};
  const details = Object.entries(labels).filter(([key]) => value[key] != null).map(([key, label]) => ({key, label,
    type: key.endsWith('At') ? 'time' : ['evidenceRef', 'source'].includes(key) ? 'citation' : 'text',
    value: copy(value[key]), display: displayValue(['evidenceRef', 'source'].includes(key) ? 'citation' : 'text', value[key])}));
  if (value.inspection) details.push({key: 'inspection.status', label: 'Receipt inspection', type: 'text', value: value.inspection.status ?? null, display: value.inspection.status || 'not recorded'},
    {key: 'inspection.at', label: 'Inspection recorded time', type: 'time', value: value.inspection.at ?? null});
  return details;
}

function typed(field, label, type, value, scope, attribution = {kind: 'workflow_record', actor: null}) {
  const normalized = value == null || value === '' ? null : type === 'revision' || type === 'id' ? String(value) : copy(value);
  const attributionKind = {walkthrough_default: 'Walkthrough default', reviewer_input: 'Reviewer input', evidence_record: 'Evidence record', source_unit: 'Source unit'}[attribution.kind] || 'Workflow record';
  return {field, label, type, value: normalized, display: displayValue(type, normalized), details: referenceDetails(normalized), scope: copy(scope || {}),
    origin: attribution.kind === 'walkthrough_default' ? 'walkthrough_default' : 'recorded_field',
    attribution: copy(attribution), attributionLabel: attribution.kind === 'walkthrough_default' ? attributionKind : `${attributionKind} · ${attribution.actor || (attribution.kind === 'source_unit' ? 'speaker not recorded' : 'author not recorded')}${attribution.attributedTo ? ` · attributed customer reviewer: ${attribution.attributedTo}` : ''}`};
}

export function compareHistoryValues(before = [], after = []) {
  const fields = [...new Set([...after, ...before].map(value => value.field))];
  return fields.flatMap(field => {
    const previous = before.find(value => value.field === field), next = after.find(value => value.field === field);
    const a = previous || typed(field, next.label, next.type, null, {}), b = next || typed(field, previous.label, previous.type, null, {});
    const valueChanged = !same(a.value, b.value) || a.type !== b.type;
    const attributionChanged = !same(a.attribution, b.attribution), scopeChanged = !same(a.scope, b.scope);
    return valueChanged || attributionChanged || scopeChanged ? [{field, label: b.label, type: b.type, before: a, after: b, valueChanged, attributionChanged, scopeChanged}] : [];
  });
}

function valuesFor(event, snapshot, context, actor = null) {
  const plan = snapshot?.plan, doc = snapshot?.document, review = snapshot?.customerReview, requirement = snapshot?.requirement;
  const agreementId = context?.agreement?.id ?? null;
  const planScope = {agreementId, planId: id(plan?.id), planRevision: id(plan?.revision)};
  const documentScope = {agreementId, documentId: id(doc?.id), documentVersion: id(doc?.version)};
  const decisionScope = {agreementId, requirementId: id(requirement?.id), documentId: id(review?.documentId), documentVersion: id(review?.documentVersion), planRevision: id(review?.planRevision)};
  const defaults = event.record?.[snapshot === event.record.after ? 'defaultsAfter' : 'defaultsBefore'] || [];
  const origin = field => ({kind: defaults.includes(field) ? 'walkthrough_default' : 'workflow_record', actor: null});
  const input = {kind: 'reviewer_input', actor: defaults.includes('customerReview.recordedBy') ? null : text(review?.recordedBy), attributedTo: text(review?.reviewedBy)};
  const category = kinds[event.type]?.[0];
  const values = [];
  if (category === 'plan' || category === 'context') {
    values.push(typed('plan.revision', 'Plan revision', 'revision', plan?.revision, planScope));
    for (const [field, label] of Object.entries(planLabels)) values.push(typed(`plan.${field}`, label, field === 'owner' ? 'person' : 'text', plan?.[field], planScope, {kind: 'reviewer_input', actor: text(plan?.authoredBy)}));
    values.push(typed('plan.status', 'Plan state', 'enum', plan?.status, planScope));
    if (['approve_plan', 'request_changes'].includes(event.type)) {
      values.push(typed('plan.approver', 'Approver record', 'person', plan?.approver, planScope, origin('plan.approver')));
      values.push(typed('plan.reviewReason', 'Review reason', 'text', event.reason, planScope, {kind: 'reviewer_input', actor}));
      if (event.type === 'request_changes') values.push(typed('plan.reviewNote', 'Review note record', 'text', plan?.reviewNote, planScope, origin('plan.reviewNote')));
    }
  }
  if (category === 'document') {
    values.push(typed('document.id', 'Document record', 'id', doc?.id, documentScope));
    values.push(typed('document.version', 'Received version', 'text', doc?.version, documentScope));
    values.push(typed('document.source', 'Recorded supplier', 'text', doc?.source, documentScope));
    values.push(typed('document.status', 'Walkthrough inspection state', 'enum', doc?.status, documentScope));
    values.push(typed('document.verifiedBy', 'Verified by record', 'person', doc?.verifiedBy, documentScope, origin('document.verifiedBy')));
    if (event.type === 'verify_document') values.push(typed('plan.specifications', 'Specifications inspected against', 'text', plan?.specifications, {...planScope, ...documentScope}, {kind: 'reviewer_input', actor: text(plan?.authoredBy)}));
    values.push(typed('document.versionBasis', 'Independent version basis', 'reference', context?.documentVersionBasis, documentScope, {kind: 'evidence_record', actor: text(context?.documentVersionBasis?.recordedBy)}));
  }
  if (category === 'customer' || category === 'requirement') {
    if (category === 'requirement') values.push(typed('requirement.status', 'Requirement state', 'enum', requirement?.status, {agreementId, requirementId: id(requirement?.id)}));
    values.push(typed('decision.status', 'Customer review state', 'enum', review?.status, decisionScope));
    values.push(typed('decision.document', 'Document record', 'id', review?.documentId, decisionScope));
    values.push(typed('decision.version', 'Reviewed version', 'text', review?.documentVersion, decisionScope));
    values.push(typed('decision.planRevision', 'Walkthrough plan binding', 'revision', review?.planRevision, decisionScope));
    values.push(typed('decision.reviewer', 'Customer reviewer (reviewer input)', 'person', review?.reviewedBy, decisionScope, input));
    values.push(typed('decision.requestedBy', 'Requested by record', 'person', review?.requestedBy, decisionScope, origin('customerReview.requestedBy')));
    values.push(typed('decision.recordedBy', 'Recorded by record', 'person', review?.recordedBy, decisionScope, origin('customerReview.recordedBy')));
    values.push(typed('decision.text', 'Decision text (reviewer input)', 'text', review?.evidenceText, decisionScope, input));
    if ((basisKinds[event.type] || []).includes('decision')) {
      values.push(typed('decision.authority', 'Authority basis', 'authority', context?.customerAuthority, decisionScope, {kind: 'evidence_record', actor: text(context?.customerAuthority?.recordedBy)}));
      values.push(typed('decision.citation', 'Cited decision evidence', 'citation', context?.decisionReceipt?.evidenceRef, decisionScope, {kind: 'evidence_record', actor: text(context?.decisionReceipt?.recordedBy)}));
      const ref = context?.decisionReceipt?.evidenceRef;
      const resolved = validContext(context) && ref ? resolveEvidenceReference(ref, context) : null;
      if (ref?.kind === 'receipt') values.push(typed('decision.citedReceipt', 'Cited artifact receipt', 'reference', resolved?.record, decisionScope, {kind: 'evidence_record', actor: text(resolved?.record?.recordedBy)}));
      if (ref?.kind === 'unit') {
        const unit = resolved?.record;
        const scope = {...decisionScope, cardId: ref.cardId, unitNumber: ref.n};
        const attribution = {kind: 'source_unit', actor: text(unit?.speaker?.name), role: text(unit?.speaker?.role), basis: text(unit?.speaker?.basis),
          partial: unit?.partial ?? null, partialReason: text(unit?.partialReason)};
        values.push(typed('decision.citedUnit', 'Cited unit record', 'reference', unit ? {id: unit.id, revision: id(context.card.revision), readAt: unit.readAt} : null, scope, attribution));
        values.push(typed('decision.citedText', unit?.kind === 'record' ? 'Cited source fields' : 'Cited source text', unit?.kind === 'record' ? 'source_fields' : 'source_text', unit ? unit.kind === 'record' ? unit.fields : unit.text : null, scope, attribution));
      }
    }
  }

  if (event.type === 'commercial_outcome') values.push(typed('commercial.status', 'Commercial outcome', 'enum', snapshot?.commercial?.status, {agreementId}));
  return values;
}

export function historyOutputIdentity(snapshot, kind, receiptId = null) {
  const doc = snapshot?.document, review = snapshot?.customerReview;
  const receipt = {documentId: doc?.id ?? null, version: doc?.version ?? null, receiptRecording: receiptId || doc?.receivedAt || null};
  if (kind === 'collection') return receipt;
  if (kind === 'inspection') return {...receipt, verifiedAt: doc?.verifiedAt ?? null, verifiedBy: doc?.verifiedBy ?? null};
  if (kind === 'plan_review') return {planId: snapshot?.plan?.id ?? null, revision: id(snapshot?.plan?.revision), approvedAt: snapshot?.plan?.approvedAt ?? null};
  return copy(review);
}

export function historyImpacts(before, after, contextBefore, contextAfter, fixture = historyFixture, receipts = {}) {
  if (!completeSnapshot(before) || !completeSnapshot(after) || !validContext(contextBefore) || !validContext(contextAfter)) return unknownImpacts();
  if (contextBefore.policyVersion !== fixture.readiness.policyVersion || contextAfter.policyVersion !== fixture.readiness.policyVersion) return unknownImpacts();
  const oldState = stateFor(before), newState = stateFor(after);
  const previous = derive(oldState), next = derive(newState);
  const definitions = [
    ['isPlanApproved', 'approve_plan', 'Plan approval', 'Plan needs internal re-review', 'Current plan approved in the walkthrough'],
    ['isDocumentVerified', 'verify_document', 'Document inspection', 'Document needs re-inspection', 'Inspection recorded for the received version'],
    ['customerReviewIsCurrent', 'request_customer_review', 'Customer review request', 'Customer review request is no longer current', 'Customer review request matches the walkthrough inputs'],
    ['hasCustomerApproval', 'record_customer_decision', 'Customer approval', 'Customer approval needs re-review', 'Customer approval recorded in the walkthrough'],
  ];
  const effects = definitions.flatMap(([key, checkId, label, lost, gained]) => previous[key] || next[key] ? [{
    id: key, checkId, label, status: previous[key] && !next[key] ? 'invalidated' : !previous[key] && next[key] ? 'recorded' : 'kept',
    description: previous[key] && !next[key] ? lost : !previous[key] && next[key] ? gained : `${label} kept by the walkthrough rules`,
  }] : []);
  const oldEvaluation = evaluateReadiness(oldState, contextFor(contextBefore, fixture));
  const evaluation = evaluateReadiness(newState, contextFor(contextAfter, fixture));
  const checks = compareReadinessChecks(oldEvaluation, evaluation).filter(check => check.changed);
  const oldWork = buildWorklist(oldState, fixture.readiness.worklist), newWork = buildWorklist(newState, fixture.readiness.worklist);
  const outputs = newWork.workItems.filter(item => item.renewalId === fixture.renewalId && item.status === 'complete' && item.output).filter(item => {
    const prior = oldWork.workItems.find(row => row.id === item.id);
    return prior?.status !== 'complete' || !same(historyOutputIdentity(before, item.kind, receipts.receiptBeforeId), historyOutputIdentity(after, item.kind, receipts.receiptAfterId));
  }).map(({id, title, kind, entryTargetId, completionRule}) => {
    const doc = newState.document, decision = newState.customerReview;
    const output = kind === 'plan_review' ? `Plan ${newState.plan.id || 'not recorded'} · revision ${newState.plan.revision ?? 'not recorded'} · internal approval recorded.`
      : kind === 'collection' ? `Document ${doc?.id || 'not recorded'} · ${doc?.version || 'not recorded'} · receipt recorded.`
      : kind === 'inspection' ? `Document ${doc?.id || 'not recorded'} · ${doc?.version || 'not recorded'} · walkthrough inspection recorded.`
      : `Customer review · ${decision?.documentId || 'not recorded'} · ${decision?.documentVersion || 'not recorded'} · decision recorded.`;
    return {id, title, kind, output, entryTargetId, completionRule, identity: historyOutputIdentity(after, kind, receipts.receiptAfterId)};
  });
  return {known: true, effects, checks, outputs,
    authorityLabel: evaluation.checks.find(check => check.id === 'customer_authority')?.supportedContact?.displayName || 'unidentified',
    evidenceChecks: evaluation.checks.filter(check => check.kind === 'evidence').map(({id, title, result, resultLabel, explanation}) => ({id, title, result, resultLabel, explanation})),
    requirementEvidence: evaluation.requirements?.map(({id, status, recordedStatus, evidenceStatus}) => ({id, status, recordedStatus, evidenceStatus})) || [],
  };
}

export function buildHistoryExample(example, fixture = historyFixture) {
  let state = copy(fixture.baseline.state);
  let context = historyEvidenceSnapshot(example.evidence || fixture.readiness);
  for (const step of example.steps || []) {
    if (step.correction) state = {...state, history: [...state.history, {...copy(step.correction), id: step.id}]};
    else if (step.observation) {
      const after = {...context, ...copy(step.observation.contextAfter)};
      const snapshot = historyWorkflowSnapshot(state), prior = state.history.at(-1)?.historyRecord;
      state = {...state, history: [...state.history, {...copy(step.observation), id: step.id,
        historyRecord: {schema: '2', before: snapshot, after: copy(snapshot), contextBefore: context, contextAfter: after,
          defaultsBefore: prior?.defaultsAfter || [], defaultsAfter: prior?.defaultsAfter || [], receiptBeforeId: prior?.receiptAfterId, receiptAfterId: prior?.receiptAfterId}}]};
      context = after;
    } else {
      const next = transition(state, step.action);
      if (next.error || next.history.length !== state.history.length + 1) throw new Error('The local history example has an invalid transition.');
      const recorded = next.history.at(-1);
      state = {...next, history: [...state.history, {...recorded, id: step.id,
        historyRecord: {...recorded.historyRecord, receiptAfterId: recorded.historyRecord.receiptAfterId === recorded.id ? step.id : recorded.historyRecord.receiptAfterId,
          contextBefore: copy(context), contextAfter: copy(context)}}]};
    }
  }
  return compactHistoryState(state);
}

function legacyRecord(event) {
  const after = {};
  if (event.planSnapshot) after.plan = copy(event.planSnapshot);
  if (event.documentSnapshot) after.document = copy(event.documentSnapshot);
  if (event.requirementSnapshot) after.requirement = copy(event.requirementSnapshot);
  if (event.evidenceSnapshot) {
    after.customerReview = {...copy(event.evidenceSnapshot), status: event.type === 'record_customer_approval' ? 'approved' : null};
    after.document = {id: event.evidenceSnapshot.documentId ?? null, version: event.evidenceSnapshot.documentVersion ?? null};
  }
  return {schema: 'legacy', before: null, after: partialHistorySnapshot(after), contextBefore: null, contextAfter: null,
    actor: historyEventActor(event), eventAt: text(event.eventAt), reason: text(event.reason)};
}

function normalizeEvent(event, index, contexts) {
  const saved = hydrateHistoryRecord(event.historyRecord, contexts);
  const supported = ['1', '2'].includes(saved?.schema);
  const complete = supported && completeRecord(saved);
  const snapshotsKnown = supported && completeSnapshot(saved.before) && completeSnapshot(saved.after);
  const record = supported ? {...copy(saved), before: partialHistorySnapshot(saved.before), after: partialHistorySnapshot(saved.after),
    contextBefore: partialHistoryContext(saved.contextBefore), contextAfter: partialHistoryContext(saved.contextAfter),
    defaultsBefore: Array.isArray(saved.defaultsBefore) ? saved.defaultsBefore.filter(value => typeof value === 'string') : [],
    defaultsAfter: Array.isArray(saved.defaultsAfter) ? saved.defaultsAfter.filter(value => typeof value === 'string') : [],
    receiptBeforeId: text(saved.receiptBeforeId), receiptAfterId: text(saved.receiptAfterId),
  } : legacyRecord(event);
  return {...copy(event), id: id(event.id) || `unidentified-record-${index + 1}`, identityRecorded: event.id != null,
    type: text(event.type), title: text(event.title), detail: text(event.detail), supersedes: text(event.supersedes),
    sequence: String(index + 1), recordedAt: text(event.recordedAt ?? event.at),
    eventAt: text(event.eventAt) || text(record.eventAt), actor: historyEventActor(event) || 'not recorded',
    reason: text(event.reason), record, complete, snapshotsKnown, malformed: Boolean(event.historyRecord && !complete),
    correctionIds: [], dependencyCorrectionIds: [],
  };
}

function mergeSnapshot(before, patch) {
  if (!patch || !before) return copy(before);
  const result = copy(before);
  for (const key of ['plan', 'document', 'customerReview', 'requirement', 'requirements', 'task', 'commercial']) {
    if (!(key in patch)) continue;
    result[key] = patch[key] === null || Array.isArray(patch[key]) || typeof patch[key] !== 'object' ? copy(patch[key]) : {...before[key], ...copy(patch[key])};
  }
  return result;
}

function evidenceAt(context, ceiling) {
  if (!context || ceiling === null) return context;
  const known = (record, key) => record && timestamp(record[key]) !== null && timestamp(record[key]) <= ceiling
    && ['recordedAt', 'receivedAt', 'readAt'].every(field => record[field] == null || timestamp(record[field]) !== null && timestamp(record[field]) <= ceiling);
  const inspectionAt = record => record && Object.hasOwn(record, 'inspection') ? {...record, inspection: known(record.inspection, 'at') ? record.inspection : null} : record;
  const result = copy(context);
  for (const key of ['documentVersionBasis', 'customerAuthority', 'decisionReceipt']) result[key] = known(result[key], 'recordedAt') ? inspectionAt(result[key]) : null;
  result.contacts = (result.contacts || []).filter(record => known(record, 'recordedAt'));
  result.artifactReceipts = (result.artifactReceipts || []).filter(record => known(record, 'receivedAt')).map(inspectionAt);
  if (!known(result.card, 'readAt')) result.card = null;
  else {
    result.card.units = result.card.units.filter(unit => known(unit, 'readAt') && (!unit.receipt || known(unit.receipt, unit.receipt.readAt ? 'readAt' : 'receivedAt')))
      .map(unit => ({...unit, ...(unit.receipt ? {receipt: inspectionAt(unit.receipt)} : {})}));
    result.card.sources = result.card.sources.filter(source => known(source, 'readAt')).map(inspectionAt);
  }
  return result;
}

function evidenceCeiling(recordedAt, bound) {
  const times = [timestamp(recordedAt), bound.knownBy.kind === 'time' ? timestamp(bound.knownBy.recordedAt) : null].filter(value => value !== null);
  return times.length ? Math.min(...times) : null;
}

function retainEvidence(event, bound) {
  const ceiling = evidenceCeiling(event.recordedAt, bound);
  const contextBefore = evidenceAt(event.record.contextBefore, ceiling), contextAfter = evidenceAt(event.record.contextAfter, ceiling);
  return {...event, evidenceWithheld: !same(contextBefore, event.record.contextBefore) || !same(contextAfter, event.record.contextAfter),
    record: {...event.record, contextBefore, contextAfter}};
}

// Corrections are projections. Original entries and recording times never change.
function correctedEntry(target, correction, bound) {
  const patch = correction.changes;
  if (!validHistoryChanges(patch)) return null;
  const record = {...copy(target.record), after: mergeSnapshot(target.record.after, patch.after),
    contextAfter: patch.contextAfter ? {...copy(target.record.contextAfter), ...copy(patch.contextAfter)} : copy(target.record.contextAfter)};
  if (!completeRecord(record)) return null;
  record.defaultsAfter = (record.defaultsAfter || []).filter(path => {const [resource, field] = path.split('.'); return !Object.hasOwn(patch.after?.[resource] || {}, field);});
  record.contextAfter = evidenceAt(record.contextAfter, evidenceCeiling(correction.recordedAt, bound));
  return {...target, record, eventAt: Object.hasOwn(patch, 'eventAt') ? patch.eventAt : target.eventAt,
    correctionIds: [...target.correctionIds, correction.id]};
}

function correctDependent(entry, before, after, correction) {
  if (entry.id === before.id || entry.type === 'correction' || Number(entry.sequence) < Number(before.sequence)) return entry;
  const primaryResource = ({plan: 'plan', document: 'document', customer: 'customerReview', requirement: 'requirement'})[kinds[before.type]?.[0]];
  const resourceKeys = [...new Set([primaryResource, ...Object.keys(correction.changes.after || {})].filter(Boolean))];
  const contextKeys = Object.keys(correction.changes.contextAfter || {});
  let depends = false;
  const record = {...entry.record};
  for (const side of ['before', 'after']) {
    let snapshot = record[side], context = record[side === 'before' ? 'contextBefore' : 'contextAfter'];
    for (const key of resourceKeys) if (before.record.after?.[key] != null && same(snapshot?.[key], before.record.after[key])) {
      depends = true;
      snapshot = {...snapshot, [key]: copy(after.record.after[key])};
      const defaultsKey = side === 'before' ? 'defaultsBefore' : 'defaultsAfter';
      record[defaultsKey] = [...(record[defaultsKey] || []).filter(path => !path.startsWith(`${key}.`)), ...(after.record.defaultsAfter || []).filter(path => path.startsWith(`${key}.`))];
    }
    for (const key of contextKeys) if (before.record.contextAfter?.[key] != null && same(context?.[key], before.record.contextAfter[key])) {
      depends = true;
      context = {...context, [key]: copy(after.record.contextAfter[key])};
    }
    record[side] = snapshot;
    record[side === 'before' ? 'contextBefore' : 'contextAfter'] = context;
  }
  return depends ? {...entry, record, dependencyCorrectionIds: [...entry.dependencyCorrectionIds, correction.id]} : entry;
}

export function readHistoryLocation(search = '', fixture = historyFixture) {
  const params = new URLSearchParams(search);
  const choices = {view: ['recorded', 'effective'], category: historyCategories.map(item => item.id), order: ['oldest', 'newest'], example: fixture.examples.map(item => item.id)};
  const defaults = {view: 'recorded', category: 'all', order: 'oldest', example: 'walkthrough'};
  const invalidFields = Object.entries(choices).filter(([key, values]) => params.has(key) && (params.getAll(key).length !== 1 || !values.includes(params.get(key)))).map(([key]) => key);
  const selected = Object.fromEntries(Object.entries(choices).map(([key, values]) => [key, !invalidFields.includes(key) && values.includes(params.get(key)) ? params.get(key) : defaults[key]]));
  return {...selected, entry: params.has('entry') ? params.get('entry') : null,
    ambiguousEntry: params.getAll('entry').length > 1,
    asOf: params.get('as_of') || 'latest', ambiguousPoint: params.getAll('as_of').length > 1 || params.has('as_of') && !params.get('as_of'),
    invalidFields, invalid: invalidFields.length > 0,
    returnTo: params.has('returnTo') ? safeWorklistReturn(params.get('returnTo')) : null,
  };
}

export function historySearch(search, changes) {
  const params = new URLSearchParams(search);
  for (const [key, value] of Object.entries(changes)) {
    params.delete(key);
    if (value != null && value !== '') params.set(key, String(value));
  }
  return params;
}

export function historyHref(search = '', changes = {}, paths = historyFixture.paths) {
  const params = historySearch(search, changes);
  return `${paths.history}${params.size ? `?${params}` : ''}`;
}

export function historyActionHref(href, search, paths = historyFixture.paths) {
  const [address, hash] = href.split('#'), [path, query = ''] = address.split('?');
  const params = new URLSearchParams(query), location = readHistoryLocation(search);
  if (location.returnTo) params.set('returnTo', location.returnTo);
  params.set('historyReturn', historyHref(search, {}, paths));
  return `${path}?${params}${hash ? `#${hash}` : ''}`;
}

export function safeHistoryReturn(search, paths = historyFixture.paths) {
  const value = new URLSearchParams(search).get('historyReturn');
  return value && !value.includes('#') && value.split('?')[0] === paths.history ? value : null;
}

function boundHistory(events, location, fixture) {
  const baseline = fixture.baseline;
  if (location.ambiguousPoint) return {error: true};
  if (location.asOf === 'latest') return {events, baseline: true, knownBy: {kind: 'latest', id: 'latest', recordedAt: events.length ? events.at(-1).recordedAt : baseline.recordedAt, label: 'Latest recorded point'}};
  if (location.asOf === baseline.id) return {events: [], baseline: true, knownBy: {kind: 'sequence', id: baseline.id, recordedAt: baseline.recordedAt, label: 'Opening snapshot only'}};
  const index = events.findIndex(event => event.id === location.asOf);
  if (index >= 0) return {events: events.slice(0, index + 1), baseline: true,
    knownBy: {kind: 'sequence', id: events[index].id, recordedAt: events[index].recordedAt, label: `Through recording ${events[index].sequence}`}};
  const at = timestamp(location.asOf);
  if (at === null) return {error: true};
  const baselineKnown = timestamp(baseline.recordedAt) !== null && timestamp(baseline.recordedAt) <= at;
  const excluded = events.findIndex(event => timestamp(event.recordedAt) === null || timestamp(event.recordedAt) > at);
  const prefix = baselineKnown ? events.slice(0, excluded < 0 ? events.length : excluded) : [];
  return {events: prefix, baseline: baselineKnown, withheldRecordings: events.length - prefix.length,
    omittedUnknownTimes: events.filter(event => timestamp(event.recordedAt) === null).length,
    knownBy: {kind: 'time', id: location.asOf, recordedAt: location.asOf, label: 'Through the eligible recording prefix at this time'}};
}

function baselineEntry(fixture, location) {
  const base = fixture.baseline, context = historyEvidenceSnapshot(fixture.examples.find(item => item.id === location.example)?.evidence || fixture.readiness);
  return {id: base.id, identityRecorded: true, sequence: '0', type: 'opening_snapshot', title: base.title, detail: base.note,
    recordedAt: base.recordedAt, eventAt: base.eventAt, actor: base.actor || 'not recorded', reason: null, complete: true, snapshotsKnown: true, correctionIds: [], dependencyCorrectionIds: [],
    record: {schema: '1', before: historyWorkflowSnapshot(base.state), after: historyWorkflowSnapshot(base.state), contextBefore: context, contextAfter: context}};
}

function groupFor(event) {
  const kind = ['approve_plan', 'request_changes'].includes(event.type) ? 'plan_decision' : event.type;
  const scope = kinds[event.type]?.[0] === 'plan' ? event.record.after?.plan?.id
    : event.record.after?.requirement?.id || event.record.before?.requirement?.id || event.requirementId;
  return `${kind}:${scope || `unrecorded:${event.id}`}`;
}

function inspectLinks(event, fixture, workKind = kinds[event.type]?.[2]) {
  const requirementId = id(event.record?.after?.requirement?.id || event.record?.before?.requirement?.id || fixture.baseline.state.requirement.id);
  const work = fixture.readiness.worklist.workItems.find(item => item.renewalId === fixture.renewalId && item.kind === workKind);
  const check = workflowTransitionChecks[event.type] || (event.type === 'save_plan' || event.type === 'request_changes' ? 'submit_plan' : event.type === 'opening_snapshot' || event.type === 'reopen_requirement' ? 'resolve_requirement' : null);
  const links = [];
  if (work) links.push({id: 'workplan', label: 'Inspect current work item in Work plan', href: `${fixture.paths.workplan}?${new URLSearchParams({workItem: work.id})}#work-item-${work.id}`});
  else links.push({id: 'workplan', label: 'Inspect current resolution step in Work plan', href: `${fixture.paths.workplan}?${new URLSearchParams({requirement: requirementId})}#step-4`});
  links.push({id: 'readiness', label: check ? 'Inspect this check in Readiness' : 'Inspect this requirement in Readiness', href: readinessHref('', check ? {check} : {requirement: requirementId}, fixture.paths)});
  return links;
}

function requirementTransitions(event, fixture) {
  if (event.type === 'correction' || event.type === 'opening_snapshot') return [];
  if (!event.snapshotsKnown) {
    if (event.malformed || !['resolve_requirement', 'reopen_requirement'].includes(event.type)) return [];
    return [{requirementId: id(event.requirementId || fixture.baseline.state.requirement.id), before: event.type === 'resolve_requirement' ? 'open' : 'resolved', after: event.type === 'resolve_requirement' ? 'resolved' : 'open', explicit: true}];
  }
  return requirementsIn(event.record.after).flatMap(after => {
    const before = requirementsIn(event.record.before).find(item => item.id === after.id);
    if (before?.status === after.status) return [];
    return [{requirementId: id(after.id), title: after.title, before: before?.status ?? null, after: after.status,
      explicit: ['resolve_requirement', 'reopen_requirement'].includes(event.type)}];
  });
}

function recordReferences(event, entries, fixture) {
  const snapshot = event.record?.after, before = event.record?.before;
  const context = event.record?.contextAfter;
  const references = [], eligible = entries.filter(row => Number(row.sequence) <= Number(event.sequence));
  const doc = snapshot?.document || before?.document, review = snapshot?.customerReview || before?.customerReview;
  if (doc?.id && doc?.version) {
    const receipt = eligible.filter(row => row.type === 'obtain_document' && row.record.after?.document?.id === doc.id && row.record.after?.document?.version === doc.version
      && (!doc.receivedAt || row.record.after.document.receivedAt === doc.receivedAt)
      && (!event.record.receiptAfterId || row.id === event.record.receiptAfterId)).at(-1);
    references.push({kind: 'document', id: id(doc.id), version: id(doc.version), label: `Document ${doc.id} · ${doc.version}`, entryId: receipt?.id || null,
      note: 'Received version only. Its independent current-version basis is checked separately.'});
  }
  if (review?.status === 'approved') {
    const decision = eligible.filter(row => row.type === 'record_customer_approval' && same(row.record.after?.customerReview, review)).at(-1);
    if (decision && decision.id !== event.id) references.push({kind: 'review', id: decision.id, label: 'Customer review record used here', entryId: decision.id});
  }
  const plan = snapshot?.plan || before?.plan;
  if (plan?.id) {
    const planRecord = eligible.filter(row => ['approve_plan', 'save_plan', 'opening_snapshot'].includes(row.type) && row.record.after?.plan?.id === plan.id && row.record.after?.plan?.revision === plan.revision).at(-1);
    references.push({kind: 'plan', id: id(plan.id), revision: id(plan.revision), label: `Plan ${plan.id} · revision ${plan.revision ?? 'not recorded'}`, entryId: planRecord?.id || null});
  }
  const requirement = fixture.readiness.requirements.find(item => item.id === (snapshot?.requirement?.id || before?.requirement?.id || fixture.baseline.state.requirement.id));
  for (const citation of requirement?.citations || []) {
    const unit = (Array.isArray(context?.card?.units) ? context.card.units.find(unit => unit?.cardId === citation.cardId && unit.n === citation.n) : null) || null;
    references.push({kind: 'unit', id: `${citation.cardId}:${citation.n}`, cardId: citation.cardId, n: citation.n, unit: copy(unit),
      label: `Unit ${citation.n} · ${unit?.sourceLabel || 'source not recorded'}`, note: 'Workflow context: cited by the linked requirement, not proof of approval.',
      href: readinessHref('', {card: citation.cardId, unit: citation.n}, fixture.paths)});
  }
  for (const [kind, record, ref] of [['authority', context?.customerAuthority, context?.customerAuthority?.evidenceRef], ['decision', context?.decisionReceipt, context?.decisionReceipt?.evidenceRef], ['version', context?.documentVersionBasis, context?.documentVersionBasis?.source]]) {
    if (!record || !(basisKinds[event.type] || []).includes(kind)) continue;
    const receipt = ref?.kind === 'receipt' ? (Array.isArray(context?.artifactReceipts) ? context.artifactReceipts.find(item => item.id === ref.id) : null) : null;
    const check = {authority: 'customer_authority', decision: 'decision_citation', version: 'current_version_basis'}[kind];
    references.push({kind: 'basis', id: id(record.id) || `unrecorded:${kind}`, label: `${kind === 'authority' ? 'Authority' : kind === 'version' ? 'Version' : 'Decision'} basis ${record.id || 'not recorded'}`, record: copy(record), receipt: copy(receipt) || null,
      href: ref?.kind === 'unit' ? readinessHref('', {card: ref.cardId, unit: ref.n}, fixture.paths) : readinessHref('', {check}, fixture.paths),
      note: 'The retained reference is shown here. The link opens the current evidence check, which may have different inputs.'});
  }
  return references;
}

function laterOutcomes(event, entries, fixture) {
  const later = entries.filter(row => Number(row.sequence) > Number(event.sequence) && !row.derived && row.type !== 'correction');
  const snapshot = event.record.after;
  const samePlan = row => snapshot?.plan?.id != null && snapshot.plan.id === row.record.after?.plan?.id;
  const sameRequirement = row => snapshot?.requirement?.id != null && snapshot.requirement.id === row.record.after?.requirement?.id;
  const matches = (row, field, keys) => keys.every(key => snapshot?.[field]?.[key] != null && snapshot[field][key] === row.record.after?.[field]?.[key]);
  let result = null;
  if (event.type === 'request_review') {
    const next = later.find(row => samePlan(row) && ['request_review', 'approve_plan', 'request_changes', 'save_plan'].includes(row.type));
    if (next && ['approve_plan', 'request_changes'].includes(next.type) && matches(next, 'plan', ['id', 'revision'])) result = next;
  } else if (event.type === 'obtain_document') {
    const next = later.find(row => sameRequirement(row) && samePlan(row) && ['verify_document', 'obtain_document', 'save_plan'].includes(row.type));
    if (next?.type === 'verify_document' && matches(next, 'document', ['id', 'version'])) result = next;
  } else if (event.type === 'request_customer_review') {
    const next = later.find(row => sameRequirement(row) && samePlan(row) && ['record_customer_approval', 'request_customer_review', 'save_plan', 'obtain_document', 'reopen_requirement'].includes(row.type));
    if (next?.type === 'record_customer_approval' && matches(next, 'customerReview', ['documentId', 'documentVersion', 'planRevision'])) result = next;
  } else if (event.type === 'record_customer_approval') {
    const next = later.find(row => sameRequirement(row) && ['resolve_requirement', 'record_customer_approval', 'save_plan', 'obtain_document', 'reopen_requirement'].includes(row.type));
    if (next?.type === 'resolve_requirement' && same(snapshot?.customerReview, next.record.before?.customerReview) && snapshot?.customerReview) result = next;
  } else if (event.type === 'resolve_requirement') {
    const requirementId = snapshot?.requirement?.id || event.requirementId || fixture.baseline.state.requirement.id;
    const next = later.find(row => requirementTransitions(row, fixture).some(change => change.requirementId === requirementId && change.before === 'resolved' && change.after === 'open'));
    if (next) result = {...next, title: 'Requirement reopened by this later recording'};
  }
  return result ? [{id: result.id, title: result.title, recordedAt: result.recordedAt, kind: result.type}] : [];
}

function projectEntries(events, baseline, location, fixture, bound) {
  const original = [...(baseline ? [baselineEntry(fixture, location)] : []), ...events].map(event => retainEvidence(event, bound));
  const originals = new Map(original.map(event => [event.id, event]));
  const effective = new Map(originals), corrections = new Map();
  for (const event of original.filter(row => row.type === 'correction')) {
    let target = effective.get(event.supersedes);
    if (target?.type === 'correction') target = effective.get(corrections.get(target.id)?.targetId);
    if (!target || Number(target.sequence) >= Number(event.sequence) || !target.complete) {
      corrections.set(event.id, {error: 'The earlier target or its typed snapshot is not available. No substitute entry was corrected.'});
      continue;
    }
    const updated = correctedEntry(target, event, bound);
    if (!updated) {
      corrections.set(event.id, {error: 'The correction has a partial or malformed typed patch. Its projection is withheld.'});
      continue;
    }
    corrections.set(event.id, {targetId: target.id, before: target, after: updated});
    for (const [key, entry] of effective) effective.set(key, correctDependent(entry, target, updated, event));
    effective.set(target.id, updated);
  }
  const summary = recordId => {
    const row = originals.get(recordId);
    return {id: recordId, recordedAt: row.recordedAt, actor: row.actor, reason: row.reason};
  };
  const projected = original.map(event => location.view === 'effective' && event.type !== 'correction' ? effective.get(event.id) : event);
  const previousGroups = new Map(), rows = [];
  for (const event of projected) {
    const originalEvent = originals.get(event.id), correction = corrections.get(event.id);
    const resource = correction?.after || event;
    const values = valuesFor(resource, resource.record.after, resource.record.contextAfter, resource.actor);
    const group = groupFor(event), previous = previousGroups.get(group);
    const usePrevious = previous && ['approve_plan', 'request_changes', 'record_customer_approval', 'obtain_document', 'verify_document', 'request_customer_review'].includes(event.type);
    const before = correction?.before ? valuesFor(correction.before, correction.before.record.after, correction.before.record.contextAfter, correction.before.actor)
      : usePrevious ? previous.values : valuesFor(event, event.record.before, event.record.contextBefore);
    const comparisonEntryId = correction?.targetId || (usePrevious ? previous.id : null);
    const appliedIds = [...new Set([...event.correctionIds, ...event.dependencyCorrectionIds])];
    const recordedImpacts = originalEvent.complete ? historyImpacts(originalEvent.record.before, originalEvent.record.after,
      originalEvent.record.contextBefore, originalEvent.record.contextAfter, fixture, originalEvent.record) : unknownImpacts();
    const impacts = event.type === 'correction' && correction?.before
      ? historyImpacts(correction.before.record.after, correction.after.record.after, correction.before.record.contextAfter, correction.after.record.contextAfter, fixture)
      : appliedIds.length && event.complete ? historyImpacts(event.record.before, event.record.after, event.record.contextBefore, event.record.contextAfter, fixture, event.record) : recordedImpacts;
    const transitions = requirementTransitions(originalEvent, fixture);
    const [category, kindLabel] = kinds[event.type] || ['context', 'Unclassified record'];
    const changedFields = compareHistoryValues(before, values);
    if (correction?.before && correction.before.eventAt !== correction.after.eventAt) changedFields.push(...compareHistoryValues(
      [typed('source.eventAt', 'Source event time', 'time', correction.before.eventAt, {})],
      [typed('source.eventAt', 'Source event time', 'time', correction.after.eventAt, {})]));
    const inputChanges = changedFields.filter(item => item.valueChanged && Object.keys(planLabels).includes(item.field.replace('plan.', '')));
    const trigger = inputChanges.length ? `${inputChanges.map(item => item.label).join(', ')} changed.` : null;
    const supersededBy = original.filter(item => corrections.get(item.id)?.targetId === event.id).map(item => summary(item.id));
    const upstreamCorrections = effective.get(event.id).dependencyCorrectionIds.map(summary);
    const provenance = correction?.targetId ? originals.get(correction.targetId) : originalEvent;
    const row = {...event, category, kindLabel, values, authorityRelevant: values.some(value => value.field === 'decision.authority'), differences: event.type === 'opening_snapshot' ? [] : changedFields,
      comparisonEntryId, comparisonLabel: correction?.targetId ? 'Compared with the superseded record' : usePrevious ? 'Compared with the previous record for this subject and decision kind' : event.complete ? 'Compared with inputs immediately before this event' : 'Earlier input snapshot not recorded',
      impacts, recordedImpacts, trigger, transitions, projectedTransitions: requirementTransitions(event, fixture),
      recordedRecord: originalEvent.record, recordedSnapshotsKnown: originalEvent.snapshotsKnown,
      title: event.title || kindLabel, reasonRequired: ['request_changes', 'reopen_requirement', 'correction'].includes(event.type) || transitions.some(item => item.before === 'resolved' && item.after === 'open'),
      reasonLabel: 'Recorded reason', correctionError: correction?.error || null, supersedesRoot: correction?.targetId || null,
      appliedCorrections: appliedIds.map(summary), correctedProjection: location.view === 'effective' && appliedIds.length > 0,
      supersededBy, upstreamCorrections,
      recordedValues: valuesFor(originalEvent, originalEvent.record.after, originalEvent.record.contextAfter, originalEvent.actor),
      recordedEventAt: originalEvent.eventAt,
      references: recordReferences(provenance, original, fixture), inspectionLinks: inspectLinks(resource, fixture),
      outcomes: [], derived: false,
    };
    if (event.type !== 'correction') previousGroups.set(group, row);
    rows.push(row);
    for (const change of transitions.filter(item => !item.explicit)) {
      rows.push({...row, id: `${event.id}:requirement:${change.requirementId}`, type: 'requirement_transition', category: 'requirement', kindLabel: 'Requirement transition', derived: true, parentId: event.id,
        title: change.after === 'open' ? 'Requirement reopened by this change' : 'Requirement state changed',
        detail: 'A separate requirement row, derived from the original committed before and after snapshots.',
        values: [typed('requirement.status', 'Recorded requirement state', 'enum', change.after, {requirementId: change.requirementId})],
        differences: compareHistoryValues([typed('requirement.status', 'Recorded requirement state', 'enum', change.before, {requirementId: change.requirementId})], [typed('requirement.status', 'Recorded requirement state', 'enum', change.after, {requirementId: change.requirementId})]),
        transitions: [change], comparisonEntryId: event.id, comparisonLabel: 'Caused by this recorded change',
        appliedCorrections: [], correctedProjection: false, upstreamCorrections: [...supersededBy, ...upstreamCorrections],
        impacts: {...recordedImpacts, outputs: []}, inspectionLinks: inspectLinks({...originalEvent, type: 'reopen_requirement'}, fixture),
      });
    }
    if (event.type !== 'opening_snapshot' && event.type !== 'correction') for (const output of recordedImpacts.outputs) {
      const scope = {workItemId: output.id};
      rows.push({...row, id: `${event.id}:output:${output.id}`, type: 'work_item_output', category: 'output', kindLabel: 'Work-item output', derived: true, parentId: event.id,
        title: `Output recorded: ${output.title}`, detail: output.output, output, reasonRequired: false, reason: null,
        appliedCorrections: [], correctionIds: [], correctedProjection: false, supersededBy: [], upstreamCorrections: [...supersededBy, ...upstreamCorrections],
        values: [typed('output.record', 'Work item', 'id', output.id, scope), typed('output.proof', 'Recorded output', 'text', output.output, scope)], differences: [],
        comparisonEntryId: event.id, comparisonLabel: 'Derived from this recorded event, not a completion command',
        transitions: [], impacts: {known: true, effects: [], checks: [], evidenceChecks: [], outputs: []},
        references: recordReferences(originalEvent, original, fixture), inspectionLinks: inspectLinks(originalEvent, fixture, output.kind),
      });
    }
  }
  for (const row of rows) if (!row.derived) row.outcomes = laterOutcomes(originals.get(row.id), original, fixture);
  return rows;
}

export function formatHistoryDuration(milliseconds) {
  if (milliseconds == null || milliseconds < 0) return 'not recorded';
  const minutes = Math.floor(milliseconds / 60000), days = Math.floor(minutes / 1440), hours = Math.floor(minutes % 1440 / 60), remaining = minutes % 60;
  return [days ? `${days}d` : '', hours ? `${hours}h` : '', remaining || !days && !hours ? `${remaining}m` : ''].filter(Boolean).join(' ');
}

function buildIntervals(entries, bound, fixture, projected = false) {
  if (!fixture.readiness.requirementsRecorded || !bound.baseline && !entries.length) return null;
  const initial = (bound.baseline ? fixture.readiness.requirements : []).filter(item => item.renewalId === fixture.renewalId);
  const recordings = entries.filter(entry => !entry.derived && !['correction', 'opening_snapshot'].includes(entry.type));
  const recordFor = entry => projected ? entry.record : entry.recordedRecord;
  const changesFor = entry => projected ? entry.projectedTransitions : entry.transitions;
  const observed = recordings.flatMap(entry => [...requirementsIn(recordFor(entry)?.before), ...requirementsIn(recordFor(entry)?.after)]);
  const ids = [...new Set([...initial.map(item => item.id), ...observed.map(item => item.id), ...recordings.flatMap(entry => changesFor(entry).map(change => change.requirementId))])].filter(Boolean);
  return ids.map(requirementId => {
    const definition = initial.find(item => item.id === requirementId), seed = fixture.baseline.state.requirement;
    let status = bound.baseline ? (definition?.binding === 'workflow_requirement' && seed.id === requirementId ? seed.status : definition?.status) ?? null : null;
    const intervals = [], changes = [];
    let gap = false, incomplete = false;
    let active = status === 'open' ? {id: `${requirementId}:interval:1`, startEntryId: fixture.baseline.id, openedAt: definition?.openedAt || null, observedAt: fixture.baseline.recordedAt,
      openingActor: 'not recorded', reason: null, initial: true} : null;
    function observe(next, entry) {
      if (status !== next) {
        changes.push({requirementId, before: status, after: next, entryId: entry.id, at: null, observedAt: entry.recordedAt, actor: 'not recorded', reason: null, observation: true});
        if (next === 'open') active = {id: `${requirementId}:interval:${intervals.length + 1}`, startEntryId: entry.id, openedAt: null,
          observedAt: entry.recordedAt, openingActor: 'not recorded', reason: null, initial: false, observationOnly: true};
        else if (active) {
          intervals.push({...active, endEntryId: entry.id, resolvedAt: null, observedResolvedAt: entry.recordedAt, resolvingActor: 'not recorded', status: 'closed'});
          active = null;
        }
        incomplete = true;
      } else if (gap && active) active = {...active, durationUnknown: true, observedAt: entry.recordedAt};
      status = next;
      gap = false;
    }
    for (const entry of recordings) {
      const reliable = projected ? entry.snapshotsKnown : entry.recordedSnapshotsKnown;
      const record = recordFor(entry), before = requirementsIn(record?.before).find(item => item.id === requirementId);
      const after = requirementsIn(record?.after).find(item => item.id === requirementId);
      if (reliable && before && ['open', 'resolved'].includes(before.status)) {
        if (gap || status !== before.status) observe(before.status, entry);
      } else if (!reliable) {gap = true; incomplete = true;}
      const transitions = changesFor(entry).filter(change => change.requirementId === requirementId);
      for (const transition of transitions) {
        const change = {...transition, entryId: transition.explicit ? entry.id : `${entry.id}:requirement:${requirementId}`, at: entry.recordedAt,
          actor: entry.actor, reason: entry.reason, cause: entry.trigger || entry.recordedImpacts.effects.filter(effect => effect.status === 'invalidated').map(effect => effect.description).join('. ') || null};
        changes.push(change);
        if (change.after === 'open' && (status !== 'open' || gap)) active = {id: `${requirementId}:interval:${intervals.length + 1}`, startEntryId: change.entryId,
          openedAt: change.at, observedAt: change.at, openingActor: change.actor, reason: change.reason, cause: change.cause, initial: false};
        if (change.after === 'resolved') {
          intervals.push({...active, id: active?.id || `${requirementId}:interval:${intervals.length + 1}`, startEntryId: active?.startEntryId || null,
            openedAt: active?.openedAt || null, observedAt: active?.observedAt || null, openingActor: active?.openingActor || 'not recorded', reason: active?.reason || null,
            durationUnknown: gap || active?.durationUnknown, endEntryId: change.entryId, resolvedAt: change.at, resolvingActor: change.actor, status: 'closed'});
          active = null;
        }
        status = change.after;
        gap = false;
      }
      if (reliable && after && ['open', 'resolved'].includes(after.status) && (gap || status !== after.status)) observe(after.status, entry);
    }
    if (active) intervals.push({...active, endEntryId: null, resolvedAt: null, status: gap ? 'unknown' : 'open'});
    return {id: id(requirementId), title: definition?.title || observed.find(item => item.id === requirementId)?.title || 'Requirement title not recorded', status: gap ? null : status,
      incomplete, transitions: changes,
      intervals: intervals.map(interval => {
        const start = interval.durationUnknown ? null : timestamp(interval.openedAt), observed = timestamp(interval.observedAt);
        const finish = timestamp(interval.status === 'closed' ? interval.resolvedAt : interval.status === 'open' ? bound.knownBy.recordedAt : null);
        const durationMs = start !== null && finish !== null && finish >= start ? finish - start : null;
        const observedMs = observed !== null && finish !== null && finish >= observed ? finish - observed : null;
        return {...interval, durationMs, observedMs, measuredThrough: interval.status === 'closed' ? interval.resolvedAt : interval.status === 'open' ? bound.knownBy.recordedAt : null,
          durationLabel: durationMs !== null ? `${formatHistoryDuration(durationMs)}${interval.status === 'open' ? ' open through this point' : ' open'}`
            : observedMs !== null ? `${formatHistoryDuration(observedMs)} observed open; ${interval.observationOnly ? 'reopening' : 'opening'} time not recorded` : 'Duration not recorded'};
      })};
  });
}

function requirementState(intervals) {
  if (intervals === null) return 'Requirement state not recorded';
  if (!intervals.length) return 'No requirement records';
  if (intervals.length === 1) {
    const requirement = intervals[0];
    if (requirement.status === 'resolved') return 'Resolved in walkthrough';
    if (requirement.status !== 'open') return 'Requirement state not recorded';
    return requirement.transitions.some(change => change.before === 'resolved' && change.after === 'open') ? 'Reopened in walkthrough' : 'Open in walkthrough';
  }
  const open = intervals.filter(item => item.status === 'open').length, resolved = intervals.filter(item => item.status === 'resolved').length;
  return `${open} open, ${resolved} resolved, ${intervals.length - open - resolved} unknown`;
}

function separateOutcomes(entries, intervals, fixture, location, projectedIntervals) {
  const resolutions = entries.filter(entry => !entry.derived && entry.transitions.some(change => change.after === 'resolved'));
  const outputs = entries.filter(entry => entry.type === 'work_item_output');
  const correctedCount = outputs.filter(entry => entry.upstreamCorrections.length > 0).length;
  const latest = entries.findLast(entry => !entry.derived && entry.type !== 'correction');
  const snapshot = latest?.recordedRecord.after;
  let currentCount = null;
  if (latest?.recordedSnapshotsKnown && completeSnapshot(snapshot)) {
    const complete = buildWorklist(stateFor(snapshot), fixture.readiness.worklist).workItems.filter(item => item.renewalId === fixture.renewalId && item.status === 'complete');
    currentCount = complete.filter(item => outputs.some(entry => entry.output.id === item.id && !entry.upstreamCorrections.length
      && same(entry.output.identity, historyOutputIdentity(snapshot, item.kind, latest.recordedRecord.receiptAfterId)))).length;
  }
  const commercial = entries.filter(entry => entry.type === 'commercial_outcome' && entry.recordedRecord.after?.commercial).at(-1);
  return [
    {id: 'requirement', label: 'Requirement resolution', value: `${requirementState(intervals)} · ${resolutions.length} resolution recording${resolutions.length === 1 ? '' : 's'}`,
      recordedCount: resolutions.length, recordedStatuses: intervals?.map(({id, status}) => ({id, status})) ?? null,
      projectionValue: location.view === 'effective' ? `Projected state: ${requirementState(projectedIntervals)}` : null,
      projectedStatuses: location.view === 'effective' ? projectedIntervals?.map(({id, status}) => ({id, status})) ?? null : null,
      note: 'State observed at this point, with original recorded transitions counted. Evidence readiness is a separate check.', entryIds: resolutions.map(entry => entry.id)},
    {id: 'work', label: 'Work-item outputs', value: `${currentCount === null ? 'Current output state unknown' : `${currentCount} current uncorrected outputs`} · ${outputs.length} output recording${outputs.length === 1 ? '' : 's'}`,
      recordedCount: outputs.length, currentCount, correctedCount,
      note: `${correctedCount} output recording${correctedCount === 1 ? ' has' : 's have'} a corrected underlying decision and ${correctedCount === 1 ? 'is' : 'are'} not counted as current. Outputs do not resolve the requirement.`, entryIds: outputs.map(entry => entry.id)},
    {id: 'commercial', label: 'Commercial outcome', value: commercial ? commercial.recordedRecord.after.commercial.label || 'not recorded' : fixture.baseline.state.commercial.label,
      note: commercial ? 'Separately recorded commercial result.' : 'No separate commercial result is recorded. Never inferred from resolution or task completion.',
      recordedAt: commercial?.recordedAt || null, entryIds: commercial ? [commercial.id] : []},
  ];
}

function recordingQuality(events, fixture) {
  let latest = timestamp(fixture.baseline.recordedAt), outOfOrder = 0;
  for (const event of events) {
    const at = timestamp(event.recordedAt);
    if (at === null) continue;
    if (latest !== null && at < latest) outOfOrder++;
    latest = latest === null ? at : Math.max(latest, at);
  }
  return outOfOrder ? [{code: 'recording_times_out_of_order', count: outOfOrder,
    description: 'Recording times are out of order. The trail stays in append order; a time cutoff stops before the first excluded recording.'}] : [];
}

function projectHistory(state, location, fixture) {
  const example = fixture.examples.find(item => item.id === location.example);
  const source = example.mode === 'example' ? buildHistoryExample(example, fixture) : state;
  const base = {id: fixture.id, revision: `${source.history?.length ?? 0}:${Array.isArray(source.history) ? source.history.at(-1)?.id || 'opening' : 'unknown'}`, renewalId: fixture.renewalId, paths: fixture.paths, example,
    examples: fixture.examples.map(({id, label, mode}) => ({id, label, mode})), filename: fixture.filename,
    scope: fixture.scope, policyBoundary: fixture.policyBoundary, allowedActions: [],
    startLinks: inspectLinks({type: 'request_review'}, fixture),
    viewLabel: location.view === 'recorded' ? 'As recorded' : 'As effective',
    viewDescription: location.view === 'recorded' ? 'Original entries, as they were recorded. Later corrections are separate entries, not rewrites.' : 'Corrections known by this point are applied in a labelled projection. Original recordings remain inspectable.',
  };
  const unavailable = status => ({...base, status, entries: [], visibleEntries: [], points: [], selection: null});
  if (!Array.isArray(source.history) || source.history.some(event => !event || typeof event !== 'object' || Array.isArray(event))) return unavailable('unavailable');
  const events = source.history.map((event, index) => normalizeEvent(event, index, source.historyContexts));
  if (new Set(events.map(event => event.id)).size !== events.length || events.some(event => event.id === fixture.baseline.id)) return unavailable('unavailable');
  const points = [{id: fixture.baseline.id, recordedAt: fixture.baseline.recordedAt, label: 'Opening snapshot'}, ...events.map(event => ({id: event.id, recordedAt: event.recordedAt, label: `Recording ${event.sequence}`}))];
  const latestEvent = events.findLast(event => event.type !== 'correction');
  const retained = latestEvent?.snapshotsKnown ? latestEvent.record.after : !events.length ? historyWorkflowSnapshot(fixture.baseline.state) : null;
  const unrecordedStateChange = example.mode === 'live' && Boolean(retained && !same(historyWorkflowSnapshot(source), retained));
  const bound = boundHistory(events, location, fixture);
  if (bound.error) return {...unavailable('invalid_point'), points};
  const entries = projectEntries(bound.events, bound.baseline, location, fixture, bound);
  if (new Set(entries.map(entry => entry.id)).size !== entries.length) return unavailable('unavailable');
  const intervals = buildIntervals(entries, bound, fixture);
  const projectedIntervals = location.view === 'effective' ? buildIntervals(entries, bound, fixture, true) : intervals;
  const counts = Object.fromEntries(historyCategories.map(category => [category.id, category.id === 'all' ? entries.length : entries.filter(entry => entry.category === category.id).length]));
  return {...base, status: 'ready', unrecordedStateChange, knownBy: bound.knownBy, omittedUnknownTimes: bound.omittedUnknownTimes || 0,
    withheldRecordings: bound.withheldRecordings || 0, dataQuality: recordingQuality(events, fixture), points,
    entries, counts, intervals: intervals?.map(interval => ({...interval, projectedStatus: location.view === 'effective' ? projectedIntervals?.find(item => item.id === interval.id)?.status ?? null : undefined})) ?? null,
    outcomes: separateOutcomes(entries, intervals, fixture, location, projectedIntervals),
    recordedCount: bound.events.length, derivedCount: entries.filter(entry => entry.derived).length,
    correctionCount: entries.filter(entry => entry.type === 'correction').length,
    legacyCount: entries.filter(entry => !entry.complete && !entry.derived && entry.type !== 'correction').length,
    malformedCount: entries.filter(entry => entry.malformed && !entry.derived || entry.correctionError).length,
    emptyWalkthrough: !events.length,
  };
}

function selectProjection(projection, location) {
  if (projection.status !== 'ready') return {...projection, location};
  const {entries} = projection;
  const visibleEntries = entries.filter(entry => location.category === 'all' || entry.category === location.category);
  if (location.order === 'newest') visibleEntries.sort((a, b) => Number(b.sequence) - Number(a.sequence));
  let selection = null;
  if (location.entry !== null) {
    const entry = !location.ambiguousEntry && entries.find(entry => entry.id === location.entry);
    selection = entry ? {id: entry.id, status: 'ready', resource: entry, outside: !visibleEntries.some(row => row.id === entry.id)}
      : {id: location.entry, status: 'not_available', resource: null};
  }
  return {...projection, location, visibleEntries, selection, selectedOutside: selection?.outside ? selection.resource : null};
}

export function selectHistory(state, search = '', fixture = historyFixture) {
  const location = readHistoryLocation(search, fixture);
  return selectProjection(projectHistory(state, location, fixture), location);
}

export function createHistorySelector() {
  let cached = null;
  return (state, search = '', fixture = historyFixture) => {
    const location = readHistoryLocation(search, fixture);
    const key = JSON.stringify([location.view, location.asOf, location.ambiguousPoint, location.example]);
    if (!cached || cached.state !== state || cached.fixture !== fixture || cached.key !== key) cached = {state, fixture, key, projection: projectHistory(state, location, fixture)};
    return selectProjection(cached.projection, location);
  };
}

function exportEntry(entry) {
  const {record, recordedRecord, historyRecord, planSnapshot, evidenceSnapshot, documentSnapshot, requirementSnapshot,
    changes, contextBefore, contextAfter, ...retained} = entry;
  return retained;
}

export function historyExport(view) {
  if (view.status !== 'ready') return null;
  return {scope: view.scope, renewalId: view.renewalId, view: view.location.view, knownBy: view.knownBy, example: view.location.example,
    filter: view.location.category, order: view.location.order, selectedEntryId: view.location.entry,
    entries: view.visibleEntries.map(exportEntry),
    selectedOutside: view.selectedOutside ? {outsideFilter: true, entry: exportEntry(view.selectedOutside)} : null,
    intervals: view.intervals, outcomes: view.outcomes, dataQuality: view.dataQuality,
    note: 'Only this bounded view is exported. An outside-filter selection is included separately. Current inspection links do not replace retained historical values.'};
}
