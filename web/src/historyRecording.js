import {readinessFixture} from './renewalFixtures.js';

const clone = value => JSON.parse(JSON.stringify(value));
const resources = ['plan', 'document', 'customerReview', 'requirement', 'requirements', 'task', 'commercial'];
const text = value => typeof value === 'string' && value.trim() ? value.trim() : null;
const canonical = value => Array.isArray(value) ? value.map(canonical) : value && typeof value === 'object'
  ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const fieldValue = (snapshot, field) => {const [resource, key] = field.split('.'); return snapshot?.[resource]?.[key];};

export function historyWorkflowSnapshot(state) {
  return clone(Object.fromEntries(resources.map(key => [key, state[key] ?? null])));
}

export function historyEvidenceSnapshot(fixture = readinessFixture) {
  const keys = ['agreement', 'card', 'documentVersionBasis', 'customerAuthority', 'decisionReceipt', 'contacts', 'artifactReceipts', 'policyVersion'];
  return clone(Object.fromEntries(keys.map(key => [key, fixture[key] ?? null])));
}

// Content keys deduplicate local snapshots. They are not security checks.
export function historyContextKey(context) {
  const value = JSON.stringify(canonical(context));
  let first = 2166136261, second = 5381;
  for (let i = 0; i < value.length; i++) {
    first = Math.imul(first ^ value.charCodeAt(i), 16777619);
    second = Math.imul(second, 33) ^ value.charCodeAt(i);
  }
  return `context-${(first >>> 0).toString(16).padStart(8, '0')}${(second >>> 0).toString(16).padStart(8, '0')}`;
}

export function hydrateHistoryRecord(record, contexts = {}) {
  if (!record || typeof record !== 'object' || Array.isArray(record)) return null;
  const before = Object.hasOwn(record, 'contextBefore') ? record.contextBefore : contexts?.[record.contextBeforeId] ?? null;
  const after = Object.hasOwn(record, 'contextAfter') ? record.contextAfter
    : Object.hasOwn(record, 'contextAfterId') ? contexts?.[record.contextAfterId] ?? null
      : Object.hasOwn(record, 'contextBeforeId') ? before : null;
  return {...record, contextBefore: before, contextAfter: after};
}

export function compactHistoryState(state) {
  if (!Array.isArray(state.history)) return state;
  let contexts = state.historyContexts || {}, changed = false;
  function retain(context) {
    if (context == null) return null;
    const key = historyContextKey(context);
    if (!Object.hasOwn(contexts, key)) contexts = {...contexts, [key]: clone(context)};
    return key;
  }
  const history = state.history.map(event => {
    const record = event?.historyRecord;
    if (!record || !Object.hasOwn(record, 'contextBefore') && !Object.hasOwn(record, 'contextAfter')) return event;
    const hydrated = hydrateHistoryRecord(record, contexts);
    const beforeId = retain(hydrated.contextBefore), afterId = retain(hydrated.contextAfter);
    const {contextBefore, contextAfter, contextBeforeId, contextAfterId, ...fields} = record;
    changed = true;
    return {...event, historyRecord: {...fields, contextBeforeId: beforeId, ...(afterId !== beforeId ? {contextAfterId: afterId} : {})}};
  });
  return changed ? {...state, historyContexts: contexts, history} : state;
}

let fallbackSequence = 0;
const fallbackSession = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
export function createHistoryEntryId() {
  return `history-${globalThis.crypto?.randomUUID?.() || `${fallbackSession}-${++fallbackSequence}`}`;
}

// Observe committed inputs only. This does not decide a transition.
export function recordHistoryInputs(before, patch, action, recordingId) {
  const after = {...before, ...patch};
  const prior = before.history.at(-1)?.historyRecord;
  const defaultsBefore = (Array.isArray(prior?.defaultsAfter) ? prior.defaultsAfter : []).filter(field => typeof field === 'string' && fieldValue(before, field) != null
    && fieldValue(before, field) === fieldValue(prior.after, field));
  const defaultsAfter = new Set(defaultsBefore.filter(field => fieldValue(after, field) != null
    && fieldValue(after, field) === fieldValue(before, field)));
  const supplied = {
    approve_plan: ['plan.approver', action.approver], verify_document: ['document.verifiedBy', action.verifiedBy],
    request_customer_review: ['customerReview.requestedBy', null], record_customer_approval: ['customerReview.recordedBy', action.recordedBy],
    request_changes: ['plan.reviewNote', action.note],
  }[action.type];
  if (supplied) {
    defaultsAfter.delete(supplied[0]);
    if (!text(supplied[1])) defaultsAfter.add(supplied[0]);
  }
  const priorReceipt = [...before.history].reverse().find(event => event.type === 'obtain_document');
  const receiptBeforeId = before.document ? prior?.receiptAfterId || priorReceipt?.id || null : null;
  const receiptAfterId = action.type === 'obtain_document' ? recordingId : after.document ? receiptBeforeId : null;
  const context = historyEvidenceSnapshot(), contextBeforeId = historyContextKey(context);
  const historyContexts = Object.hasOwn(before.historyContexts || {}, contextBeforeId) ? before.historyContexts
    : {...before.historyContexts, [contextBeforeId]: context};
  return {
    historyContexts,
    historyRecord: {
      schema: '2', before: historyWorkflowSnapshot(before), after: historyWorkflowSnapshot(after), contextBeforeId,
      receiptBeforeId, receiptAfterId,
      defaultsBefore, defaultsAfter: [...defaultsAfter], actor: text(action.actor), actorSource: text(action.actor) ? 'action' : null,
      eventAt: text(action.eventAt), reason: text(action.reason) || (action.type === 'request_changes' ? text(action.note) : null),
    },
  };
}

export function historyEventActor(event) {
  if (event.historyRecord?.schema === '1' && event.historyRecord.actorSource !== 'action') return null;
  return text(event.actor);
}
