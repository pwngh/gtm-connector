import {historyEventActor} from './historyRecording.js';
import {readinessFixture} from './renewalFixtures.js';
import {derive, transitionEvidenceChecks} from './workflow.js';
import {buildWorklist, formatWorklistTime, safeWorklistReturn, worklistActionHref} from './worklist.js';

export const readinessStates = [
  {id: 'not_evaluated', label: 'Not evaluated'},
  {id: 'not_applicable', label: 'Already recorded / not applicable'},
  {id: 'failed', label: 'Failed'},
  {id: 'passed', label: 'Passed'},
];
export const readinessStorageKey = 'gtm-readiness-evaluations-v2';
const stateLabel = value => readinessStates.find(item => item.id === value)?.label || 'Unknown';
const countLabel = (count, singular) => count === null ? 'Unknown' : `${count} ${singular}${count === 1 ? '' : 's'}`;
const person = (fixture, id) => fixture.worklist.people.find(item => item.id === id) || null;
const resultOrder = {failed: 0, not_evaluated: 1, passed: 2, not_applicable: 3};
const requirementLabel = status => ({open: 'Open', resolved: 'Resolved in walkthrough'})[status] || 'Unknown';
const copy = value => JSON.parse(JSON.stringify(value));
const uniqueIds = (before, after) => [...new Set([...before, ...after].map(item => item.id))];

// Small change keys, not security tokens or stored fixture copies.
function inputHash(value) {
  const text = JSON.stringify(value);
  let first = 2166136261, second = 5381;
  for (let i = 0; i < text.length; i++) {
    first = Math.imul(first ^ text.charCodeAt(i), 16777619);
    second = Math.imul(second, 33) ^ text.charCodeAt(i);
  }
  return `${(first >>> 0).toString(16).padStart(8, '0')}${(second >>> 0).toString(16).padStart(8, '0')}`;
}

function latestInputTime(values) {
  if (!values.length || values.some(value => !value)) return null;
  const literals = values.filter(value => !/(?:Z|[+-]\d\d:\d\d)$/.test(value));
  if (literals.length) return new Set(literals).size === 1 ? literals[0] : null;
  if (values.some(value => !Number.isFinite(Date.parse(value)))) return null;
  return values.reduce((latest, value) => Date.parse(value) > Date.parse(latest) ? value : latest);
}

function inputMarkers(state, fixture, previous) {
  const {card, documentVersionBasis, customerAuthority, decisionReceipt, contacts, artifactReceipts, ...configuration} = fixture;
  const lastEvent = state.history.at(-1);
  const registryTimes = [...(contacts || []).map(record => record.recordedAt), ...(artifactReceipts || []).map(record => record.inspection?.at)];
  const inputs = [
    {kind: 'workflow', value: {plan: state.plan, document: state.document, decision: state.customerReview, requirement: state.requirement, task: state.task, commercial: state.commercial, history: state.history},
      at: lastEvent ? lastEvent.at : fixture.initialEvaluationAt, recording: lastEvent?.id || 'initial'},
    {kind: 'card', value: card, at: card?.readAt, recording: card?.id},
    ...[['version_basis', documentVersionBasis], ['authority', customerAuthority], ['decision_receipt', decisionReceipt]].map(([kind, value]) => ({
      kind, value, at: value ? value.recordedAt : fixture.initialEvaluationAt, recording: value?.id || 'missing',
    })),
    {kind: 'evidence_registry', value: {contacts, artifactReceipts}, at: registryTimes.length ? latestInputTime(registryTimes) : fixture.initialEvaluationAt},
    {kind: 'configuration', value: configuration, at: fixture.initialEvaluationAt},
  ];
  return inputs.map(({kind, value, at, recording}) => {
    const key = inputHash(value ?? null), recordedAt = at || null;
    const recordingKey = `${recording || kind}:${recordedAt || 'unknown'}`;
    const prior = previous?.inputMarkers?.find(input => input.kind === kind);
    const asOf = !prior ? recordedAt : prior.key === key ? prior.asOf : prior.recordingKey !== recordingKey ? recordedAt : null;
    return {kind, key, recordedAt, recordingKey, asOf};
  });
}

function inputVersions(state, fixture, worklist) {
  const work = kind => worklist.workItems.find(item => item.renewalId === fixture.renewalId && item.kind === kind);
  const resource = (kind, label, value, extra = {}) => ({kind, label, id: value?.id ?? null, recorded: Boolean(value), revision: value?.revision == null ? null : String(value.revision), ...extra});
  return [
    resource('plan', 'Work plan', state.plan, {workItemRevision: work('plan_review')?.revision ?? null}),
    resource('document', 'Received document', state.document, {version: state.document?.version ?? null}),
    resource('inspection_work_item', 'Inspection work item', work('inspection')),
    resource('customer_decision', 'Customer review record', state.customerReview, {version: state.customerReview?.documentVersion ?? null}),
    resource('customer_review_work_item', 'Customer review work item', work('customer_review')),
    resource('requirement', 'Workflow requirement', worklist.requirements.find(item => item.id === state.requirement.id)),
    resource('version_basis', 'Separate version source', fixture.documentVersionBasis),
    resource('authority', 'Customer authority basis', fixture.customerAuthority),
    resource('decision_receipt', 'Cited decision receipt', fixture.decisionReceipt),
    resource('card', 'Source card', fixture.card),
    resource('policy', 'Local workflow rules', {id: fixture.policyVersion, revision: fixture.revision}),
  ];
}

export function resolveEvidenceReference(ref, fixture = readinessFixture) {
  if (!ref || typeof ref !== 'object') return {issue: 'No supported evidence reference is recorded.'};
  if (ref.kind === 'receipt') {
    const record = fixture.artifactReceipts?.find(item => item.id === ref.id);
    if (!record) return {issue: 'The cited artifact receipt does not exist.'};
    if (record.status !== 'received' || record.inspection?.status !== 'accepted') return {issue: 'The cited artifact receipt needs an accepted inspection.'};
    return {record, source: null};
  }
  if (!fixture.card) return {issue: 'The source card is not available.'};
  if (ref.kind === 'source') {
    const source = fixture.card.sources.find(item => item.id === ref.id);
    if (!source) return {issue: 'The referenced source does not exist.'};
    if (source.status !== 'read') return {issue: 'The referenced source has not been read.'};
    return {record: source, source};
  }
  if (ref.kind === 'unit') {
    const record = ref.cardId === fixture.card.id && fixture.card.units.find(item => item.cardId === ref.cardId && item.n === ref.n);
    if (!record) return {issue: 'The cited card and unit do not exist.'};
    if (!record.shown || (record.kind === 'sentence' ? !record.text?.trim() : !record.fields?.length)) return {issue: 'The cited unit is not available for inspection.'};
    const source = fixture.card.sources.find(item => item.id === record.sourceId);
    if (!source || source.status !== 'read') return {issue: 'The cited unit has no available read source.'};
    return {record, source};
  }
  return {issue: 'The evidence reference has an unsupported kind.'};
}

function isIndependentSource(reference, document) {
  if (!reference.record || !document?.id) return false;
  const records = [reference.record, reference.source].filter(Boolean);
  if (records.some(record => [record.id, record.documentId, record.artifactId].includes(document.id))) return false;
  return reference.source ? ['call', 'message', 'record'].includes(reference.source.kind)
    : Boolean(reference.record.artifactId || reference.record.documentId);
}

function authorityEvidence(fixture) {
  const basis = fixture.customerAuthority;
  if (!basis) return {issues: ['No authority record is recorded for this agreement.'], contact: null};
  const issues = [];
  const contact = fixture.contacts?.find(item => item.id === basis.contactId && item.status === 'active');
  if (!contact) issues.push('The authority record does not identify an available customer contact.');
  if (basis.status !== 'accepted' || basis.agreementId !== fixture.agreement.id) issues.push('The authority record is not accepted for this agreement.');
  const reference = resolveEvidenceReference(basis.evidenceRef, fixture);
  if (reference.issue) issues.push(reference.issue);
  return {issues, contact: issues.length ? null : contact};
}

function unknownCheck(explanation) {
  return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation};
}

function evaluateEvidence(definition, state, fixture) {
  if (!['current_version_basis', 'customer_authority', 'decision_citation'].includes(definition.id)) return unknownCheck('No evaluation rule is recorded for this evidence check.');
  if (!fixture.card) return unknownCheck('No source card is recorded. This evidence check has not been evaluated.');
  let issues = [], contact = null;
  if (definition.id === 'current_version_basis') {
    const basis = fixture.documentVersionBasis;
    if (!basis) issues.push('No independent current-version basis is recorded. The document cannot vouch for its own current version.');
    else {
      const reference = resolveEvidenceReference(basis.source, fixture);
      if (reference.issue) issues.push(reference.issue);
      else if (!isIndependentSource(reference, state.document)) issues.push('The version source is not established as independent of the document.');
      if (!state.document?.id || !state.document.version || basis.documentId !== state.document.id || basis.version !== state.document.version || basis.agreementId !== fixture.agreement.id || basis.status === 'withdrawn') issues.push('The separate version basis does not identify this document version for this agreement.');
    }
  } else if (definition.id === 'customer_authority') {
    ({issues, contact} = authorityEvidence(fixture));
  } else {
    const basis = fixture.decisionReceipt;
    if (!basis) issues.push('No cited customer decision is recorded. Typed walkthrough input is not a cited unit or inspected approval receipt.');
    else {
      const reference = resolveEvidenceReference(basis.evidenceRef, fixture);
      if (reference.issue) issues.push(reference.issue);
      if (!['unit', 'receipt'].includes(basis.evidenceRef?.kind)) issues.push('A customer decision must cite a unit or an inspected artifact receipt.');
      if (!state.document?.id || !state.document.version || basis.status !== 'accepted' || basis.outcome !== 'approved' || basis.agreementId !== fixture.agreement.id || basis.documentId !== state.document.id || basis.documentVersion !== state.document.version) issues.push('The accepted decision is not bound to this agreement and document version.');
      const authority = authorityEvidence(fixture);
      if (!authority.contact || basis.contactId !== authority.contact.id) issues.push('The decision does not identify the contact supported by the accepted authority basis.');
    }
  }
  const explanation = issues.length ? issues.join(' ') : 'A resolved evidence reference supports this check within the synthetic records.';
  return {
    result: issues.length ? 'failed' : 'passed', allowed: null, allowedActions: [], supportedContact: contact,
    blockedReasons: issues.length ? [{code: definition.id, action: 'establish_evidence', name: definition.title, reason: explanation, checkId: definition.id, evidenceGap: true}] : [],
    explanation,
  };
}

function correctiveAction(checks, fixture) {
  const check = ['customer_authority', 'current_version_basis', 'decision_citation'].map(id => checks.find(item => item.id === id && item.result !== 'passed' && item.correctiveLabel)).find(Boolean);
  return check ? {
    id: `inspect_${check.id}`, label: check.correctiveLabel, intent: 'inspect_evidence', checkId: check.id,
    owner: person(fixture, fixture.worklist.walkthrough.reviewerId),
    href: `${fixture.paths.readiness}?${new URLSearchParams({check: check.id})}`,
  } : null;
}

function commandAction(definition, state, fixture, worklist, replacement) {
  const item = worklist.workItems.find(row => row.renewalId === fixture.renewalId && row.kind === definition.workKind);
  const requirement = worklist.requirements.find(row => row.id === state.requirement.id);
  const params = item ? new URLSearchParams({workItem: item.id}) : new URLSearchParams({requirement: state.requirement.id});
  return {
    id: definition.command, command: definition.command, label: replacement ? definition.replacementActionLabel : definition.actionLabel, intent: 'open_local_step', replacement,
    checkId: definition.id, owner: item?.owner || requirement?.owner || null,
    href: `${fixture.paths.workplan}?${params}${item ? `#${item.entryTargetId}` : ''}`,
  };
}

function evaluateTransition(definition, state, fixture, worklist, d, evidenceChecks) {
  const rule = d.transitions[definition.command];
  if (!rule) return unknownCheck('No evaluation rule is recorded for this transition.');
  const demonstration = {allowed: rule.allowed, fact: null};
  const title = rule.replacement ? definition.replacementTitle : definition.title;
  if (rule.recorded) return {title, result: 'not_applicable', recorded: true, allowed: false, blockedReasons: [], allowedActions: [], demonstration, explanation: rule.recordedReason};
  const requiredEvidence = (transitionEvidenceChecks[definition.command] || []).map(id => evidenceChecks.find(check => check.id === id)
    || {id, result: 'not_evaluated', blockedReasons: [], title: id, explanation: 'No evaluation rule is recorded for this evidence check.'});
  const evidenceBlockers = requiredEvidence.filter(check => check.result !== 'passed').flatMap(check => check.blockedReasons.length ? check.blockedReasons : [{
    code: check.id, action: definition.command, name: check.title, reason: check.explanation, checkId: check.id, evidenceGap: true,
  }]);
  const blockedReasons = [...rule.blockedReasons, ...evidenceBlockers.map(reason => ({...reason, action: definition.command}))];
  const allowed = rule.allowed && !evidenceBlockers.length;
  if (rule.allowed && evidenceBlockers.length) demonstration.fact = 'The demonstration walkthrough would still allow this step; real use would refuse it.';
  return {
    title, result: allowed ? 'passed' : 'failed', allowed, blockedReasons, demonstration, replacement: rule.replacement,
    allowedActions: allowed ? [commandAction(definition, state, fixture, worklist, rule.replacement)] : [],
    correctiveAction: evidenceBlockers.length ? correctiveAction(requiredEvidence, fixture) : null,
    explanation: allowed ? 'The local transition prerequisites pass with these inputs.' : 'Every unmet workflow or required evidence check is listed below.',
  };
}

function decorateCheck(check, evaluatedAt, versions) {
  const evaluated = check.result !== 'not_evaluated';
  const blockerCount = ['passed', 'failed'].includes(check.result) ? check.blockedReasons.length : null;
  return {
    ...check, evaluatedAt: evaluated ? evaluatedAt : null,
    inputVersions: versions.filter(input => check.inputKinds.includes(input.kind)),
    resultLabel: check.recorded ? 'Already recorded' : stateLabel(check.result), blockerCount,
    resultDetail: check.result === 'passed' ? '0 blockers in this check'
      : check.result === 'failed' ? countLabel(blockerCount, 'blocker')
      : check.recorded ? 'Recorded in the walkthrough' : check.result === 'not_applicable' ? 'Not needed in this state' : 'Blocker count unknown',
  };
}

export function evaluateReadiness(state, fixture = readinessFixture, previous = null) {
  const reviewer = person(fixture, fixture.worklist.walkthrough.reviewerId);
  const d = derive(state, reviewer?.displayName ?? null);
  const worklist = buildWorklist(state, fixture.worklist);
  const renewal = worklist.renewals.find(row => row.id === fixture.renewalId);
  const markers = inputMarkers(state, fixture, previous);
  const evaluatedAt = latestInputTime(markers.map(input => input.asOf));
  const versions = inputVersions(state, fixture, worklist);
  const evidence = fixture.checks.filter(definition => definition.kind === 'evidence').map(definition => ({...definition, ...evaluateEvidence(definition, state, fixture)}));
  const checks = fixture.checks.map((definition, index) => decorateCheck({
    ...definition, order: index, revision: String(Number(fixture.revision) + state.history.length),
    ...(definition.kind === 'transition' ? evaluateTransition(definition, state, fixture, worklist, d, evidence) : evidence.find(check => check.id === definition.id) || unknownCheck('No evaluation rule is recorded for this check.')),
  }, evaluatedAt, versions));
  const requirements = fixture.requirementsRecorded ? fixture.requirements.filter(record => record.renewalId === fixture.renewalId).map(record => {
    const bound = record.binding === 'workflow_requirement' && record.id === state.requirement.id;
    const recordedStatus = bound ? state.requirement.status : record.status;
    const related = checks.filter(check => record.checkIds.includes(check.id));
    const evidenceChecks = checks.filter(check => record.evidenceCheckIds.includes(check.id));
    const evidenceOpen = record.evidenceCheckIds.some(id => checks.find(check => check.id === id)?.result !== 'passed');
    const status = recordedStatus === 'resolved' && evidenceOpen ? 'open' : recordedStatus;
    const authority = evidenceChecks.find(check => check.id === 'customer_authority');
    const resolution = related.find(check => check.command === 'resolve_requirement');
    const nextCheck = related.find(check => check.command === d.nextCommand);
    const nextAction = bound && status === 'open' ? recordedStatus === 'resolved' ? correctiveAction(evidenceChecks, fixture)
      : nextCheck?.allowedActions[0] || (nextCheck?.demonstration?.allowed ? nextCheck.correctiveAction : null) || null : null;
    const owner = person(fixture, record.ownerId);
    const planWork = worklist.workItems.find(item => item.renewalId === fixture.renewalId && item.kind === 'plan_review');
    const nextStepOwner = nextAction ? nextAction.owner : (bound && status === 'open' && !d.isPlanApproved ? state.plan.owner?.trim() ? planWork?.owner || null : null : owner);
    const workflowMissing = bound && recordedStatus === 'open' ? resolution?.blockedReasons || [] : [];
    const missing = [...new Map([...workflowMissing, ...evidenceChecks.filter(check => check.result !== 'passed').map(check => ({
      code: check.id, name: check.title, reason: check.explanation, checkId: check.id, evidenceGap: true,
    }))].map(reason => [reason.code, reason])).values()];
    if (bound && status === 'open' && resolution?.allowed) missing.unshift({code: 'explicit_resolution', name: 'Explicit requirement resolution', reason: 'The supported decision is recorded. A reviewer still needs to record the resolution.', checkId: resolution.id});
    const waitingOn = bound && state.customerReview && (d.canRecordCustomerApproval || authority?.result !== 'passed')
      ? authority?.supportedContact ? {id: authority.supportedContact.id, label: authority.supportedContact.displayName, basis: 'Contact supported by the accepted authority evidence.'} : record.awaitedParty : null;
    return {
      ...record, agreement: fixture.agreement, title: bound ? state.requirement.title : record.title, status, statusLabel: requirementLabel(status), recordedStatus, recordedStatusLabel: requirementLabel(recordedStatus),
      evidenceStatus: evidenceOpen ? 'open' : 'established',
      revision: worklist.requirements.find(row => row.id === record.id)?.revision || String(record.revision),
      resolvedAt: bound ? state.requirement.resolvedAt : null,
      owner, ownerLabel: owner?.displayName || 'Unassigned', nextStepOwner, nextStepOwnerLabel: nextStepOwner?.displayName || 'Unassigned',
      waitingOn, waitingLabel: waitingOn?.label || 'No external party currently awaited',
      customerAuthorityLabel: authority?.supportedContact?.displayName || 'Unidentified',
      missing, evidenceChecks, nextAction, inputVersions: versions.filter(input => input.kind !== 'policy'),
      allowedActions: nextAction ? [nextAction] : bound && d.canReopenRequirement ? [{id: 'reopen_requirement', command: 'reopen_requirement', label: 'Reopen with a reason', intent: 'local_form', owner}] : [],
      reopenAction: bound && d.canReopenRequirement && nextAction ? {id: 'reopen_requirement', command: 'reopen_requirement', label: 'Reopen the walkthrough record with a reason', intent: 'local_form', owner} : null,
      blockedReasons: status === 'open' ? missing : [],
      actionUnavailableReason: !bound ? 'No executable workflow is recorded for this requirement.' : status === 'open' && !nextAction ? d.planReviewBlockedReason || 'No corrective transition is currently permitted by the local workflow.' : null,
      inspectAction: bound && recordedStatus === 'open' && !nextAction && planWork ? {id: 'inspect_prerequisite', label: 'Inspect review prerequisites', intent: 'inspect', href: `${fixture.paths.workplan}?${new URLSearchParams({workItem: planWork.id})}#${planWork.entryTargetId}`} : null,
      citations: record.citations.map(citation => ({...citation, unit: fixture.card?.units.find(unit => unit.cardId === citation.cardId && unit.n === citation.n) || null})),
      recordedDecision: bound && state.customerReview?.status === 'approved' ? {
        label: 'Reviewer input in the existing walkthrough', text: state.customerReview.evidenceText,
        recordedBy: state.customerReview.recordedBy || null, recordedAt: state.customerReview.approvedAt || null,
        documentVersion: state.customerReview.documentVersion || null,
      } : null,
    };
  }) : null;
  const openCount = requirements && requirements.every(item => ['open', 'resolved'].includes(item.status)) ? requirements.filter(item => item.status === 'open').length : null;
  return {
    id: fixture.id, renewalId: fixture.renewalId, revision: String(Number(fixture.revision) + state.history.length),
    evaluatedAt, inputVersions: versions, inputMarkers: markers, inputKey: inputHash(markers.map(({kind, key}) => ({kind, key}))), lastEventId: state.history.at(-1)?.id || null,
    paths: fixture.paths, agreement: fixture.agreement, scope: fixture.scope, policyBoundary: fixture.policyBoundary,
    crm: renewal.crm, commercial: renewal.commercial, requirements, checks,
    openRequirementCount: openCount, requirementCount: requirements?.length ?? null,
    requirementCountLabel: openCount === null ? 'Requirements unknown' : countLabel(openCount, 'open requirement'),
    evidenceGapCount: checks.filter(check => check.kind === 'evidence' && check.result !== 'passed').length,
    nextAction: requirements?.find(item => item.nextAction)?.nextAction || null,
    allowedActions: checks.flatMap(check => check.allowedActions), blockedReasons: checks.flatMap(check => check.blockedReasons),
    coverage: fixture.card ? copy(fixture.card) : null,
    document: state.document ? {id: state.document.id, version: state.document.version || null, receivedAt: state.document.receivedAt || null, verifiedAt: state.document.verifiedAt || null, verificationRecorded: d.isDocumentVerified} : null,
  };
}

const validEvaluation = value => value && typeof value.inputKey === 'string' && Array.isArray(value.inputMarkers) && Array.isArray(value.checks)
  && value.checks.every(check => readinessStates.some(status => status.id === check.result) && Array.isArray(check.blockedReasons) && Array.isArray(check.allowedActions) && Array.isArray(check.inputVersions))
  && (value.requirements === null || Array.isArray(value.requirements) && value.requirements.every(item => Array.isArray(item.missing) && Array.isArray(item.citations) && Array.isArray(item.allowedActions)))
  && Array.isArray(value.inputVersions) && typeof value.evaluationId === 'string' && value.paths && value.crm && value.commercial
  && (value.coverage === null || value.coverage && Array.isArray(value.coverage.units) && Array.isArray(value.coverage.sources) && Array.isArray(value.coverage.gaps));

export function readReadinessJournal(value) {
  try {
    const parsed = typeof value === 'string' ? JSON.parse(value) : value;
    return parsed?.schema === '2' && /^\d+$/.test(parsed.sequence) && validEvaluation(parsed.current) && (!parsed.previous || validEvaluation(parsed.previous))
      && parsed.changes?.cause && Array.isArray(parsed.changes.requirements) && Array.isArray(parsed.changes.checks) ? parsed : null;
  } catch {return null;}
}

function compareBlockers(before, after) {
  return {
    added: after.filter(item => !before.some(prior => prior.code === item.code)),
    cleared: before.filter(item => !after.some(next => next.code === item.code)),
    updated: after.flatMap(item => {
      const prior = before.find(value => value.code === item.code);
      return prior && JSON.stringify(prior) !== JSON.stringify(item) ? [{code: item.code, before: prior, after: item}] : [];
    }),
  };
}

function compareRequirements(before, after) {
  const oldItems = before.requirements || [], newItems = after.requirements || [];
  return uniqueIds(oldItems, newItems).flatMap(id => {
    const old = oldItems.find(item => item.id === id), next = newItems.find(item => item.id === id);
    const blockers = compareBlockers(old?.missing || [], next?.missing || []);
    const differences = [];
    if (old?.status !== next?.status) differences.push({field: 'status', label: 'Recorded state', before: old?.statusLabel || 'Not recorded', after: next?.statusLabel || 'Not recorded'});
    if (old?.recordedStatus !== next?.recordedStatus) differences.push({field: 'recordedStatus', label: 'Walkthrough record', before: old?.recordedStatusLabel || 'Not recorded', after: next?.recordedStatusLabel || 'Not recorded'});
    if (old?.nextAction?.id !== next?.nextAction?.id) differences.push({field: 'action', label: 'Next permitted step', before: old?.nextAction?.label || 'No permitted step', after: next?.nextAction?.label || 'No permitted step'});
    if (old?.nextStepOwnerLabel !== next?.nextStepOwnerLabel) differences.push({field: 'owner', label: 'Action owner', before: old?.nextStepOwnerLabel || 'Unassigned', after: next?.nextStepOwnerLabel || 'Unassigned'});
    if (old?.waitingLabel !== next?.waitingLabel) differences.push({field: 'waiting', label: 'Waiting on', before: old?.waitingLabel || 'Not recorded', after: next?.waitingLabel || 'Not recorded'});
    const kinds = [...new Set([...(old?.inputVersions || []), ...(next?.inputVersions || [])].map(input => input.kind))];
    for (const kind of kinds) {
      const prior = old?.inputVersions?.find(item => item.kind === kind), input = next?.inputVersions?.find(item => item.kind === kind);
      const describe = value => !value?.recorded ? 'Not recorded' : `${value.id || 'Record ID not recorded'} · Revision ${value.revision ?? 'not recorded'}${value.workItemRevision ? ` · Work-item revision ${value.workItemRevision}` : ''}${value.version ? ` · Version ${value.version}` : ''}`;
      if (describe(prior) !== describe(input)) differences.push({field: `input:${kind}`, label: `${input?.label || prior.label} input`, before: describe(prior), after: describe(input)});
    }
    if (old?.title !== next?.title) differences.push({field: 'title', label: 'Requirement title', before: old?.title || 'Not recorded', after: next?.title || 'Not recorded'});
    if (!blockers.added.length && !blockers.cleared.length && !blockers.updated.length && !differences.length) return [];
    return [{id, title: next?.title || old.title, ...blockers, differences}];
  });
}

export function compareReadinessChecks(before, after) {
  return uniqueIds(before.checks, after.checks).map(id => {
    const prior = before.checks.find(check => check.id === id), current = after.checks.find(check => check.id === id);
    const blockers = compareBlockers(prior?.blockedReasons || [], current?.blockedReasons || []);
    return {id, title: current?.title || prior.title, before: prior?.result ?? null, after: current?.result ?? null,
      beforeLabel: prior?.resultLabel || 'Not recorded', afterLabel: current?.resultLabel || 'Not recorded',
      beforeBlockerCount: prior?.blockerCount ?? null, afterBlockerCount: current?.blockerCount ?? null,
      changed: prior?.result !== current?.result || prior?.explanation !== current?.explanation || Object.values(blockers).some(items => items.length),
      beforeExplanation: prior?.explanation || null, afterExplanation: current?.explanation || null, ...blockers};
  });
}

export function advanceReadinessJournal(previousJournal, state, fixture = readinessFixture) {
  const stored = readReadinessJournal(previousJournal);
  const journal = stored?.current.renewalId === fixture.renewalId ? stored : null;
  const evaluation = evaluateReadiness(state, fixture, journal?.current);
  if (journal?.current.inputKey === evaluation.inputKey) {
    const {evaluationId, ...saved} = journal.current;
    if (JSON.stringify(saved) === JSON.stringify(evaluation)) return journal;
    return {...journal, current: copy({...evaluation, evaluationId})};
  }
  const sequence = String(Number(journal?.sequence || '0') + 1);
  const current = copy({...evaluation, evaluationId: `${fixture.id}:${sequence}`});
  const previous = journal?.current || null;
  const event = state.history.at(-1);
  const cause = previous && event && event.id !== previous.lastEventId ? {
    id: event.id, title: event.title, detail: event.detail || 'No reason recorded.', at: event.at || null, actor: historyEventActor(event),
    label: 'Walkthrough event text (reviewer input)',
  } : {id: null, title: previous ? 'Evaluation inputs changed' : 'Initial local evaluation', detail: previous ? 'The recorded inputs changed without a new workflow event. No actor or reason was supplied.' : 'Evaluated the available local records. This is not a live source read.', at: current.evaluatedAt, actor: null, label: 'Local evaluation context'};
  return {schema: '2', sequence, current, previous, changes: {cause,
    requirements: previous ? compareRequirements(previous, current) : [],
    checks: previous ? compareReadinessChecks(previous, current).filter(check => check.changed) : [],
  }};
}

export function readReadinessLocation(search, fixture = readinessFixture) {
  const params = new URLSearchParams(search);
  const requirement = params.get('requirement'), check = params.get('check');
  const card = params.get('card'), unit = params.get('unit');
  const options = {requirements: ['open', 'all', 'resolved'], checkState: ['all', ...readinessStates.map(item => item.id)], order: ['workflow', 'blockers'], evaluation: fixture.responses.map(item => item.id)};
  const invalidFields = Object.entries(options).filter(([key, allowed]) => params.getAll(key).some(value => !allowed.includes(value))).map(([key]) => key);
  return {
    requirement, check, card, unit,
    selection: params.has('requirement') && params.has('check') || params.getAll('requirement').length > 1 || params.getAll('check').length > 1 ? {kind: 'invalid', id: `${requirement}:${check}`} : params.has('requirement') ? {kind: 'requirement', id: requirement} : params.has('check') ? {kind: 'check', id: check} : null,
    unitSelection: params.has('card') || params.has('unit') ? {cardId: card, n: unit, ambiguous: params.getAll('card').length > 1 || params.getAll('unit').length > 1} : null,
    requirements: options.requirements.includes(params.get('requirements')) ? params.get('requirements') : 'open',
    checkState: options.checkState.includes(params.get('checkState')) ? params.get('checkState') : 'all',
    order: options.order.includes(params.get('order')) ? params.get('order') : 'workflow',
    evaluation: options.evaluation.includes(params.get('evaluation')) ? params.get('evaluation') : 'current',
    invalidFields, invalid: invalidFields.length > 0,
    returnTo: params.has('returnTo') ? safeWorklistReturn(params.get('returnTo')) : null,
  };
}

export function readinessSearch(search, changes) {
  const params = new URLSearchParams(search);
  if ('requirement' in changes && changes.requirement) {params.delete('check'); params.delete('card'); params.delete('unit');}
  if ('check' in changes && changes.check) {params.delete('requirement'); params.delete('card'); params.delete('unit');}
  for (const [key, value] of Object.entries(changes)) {
    params.delete(key);
    if (value != null && value !== '') params.set(key, String(value));
  }
  return params;
}

export function readinessHref(search, changes = {}, paths = readinessFixture.paths) {
  const params = readinessSearch(search, changes);
  return `${paths.readiness}${params.size ? `?${params}` : ''}`;
}

export function readinessSelectionKey(selection) {
  return selection ? selection.kind === 'unit' ? `unit:${selection.cardId}:${selection.n}` : `${selection.kind}:${selection.id}` : '';
}

function withholdActions(evaluation, notEvaluated) {
  return {
    ...evaluation, nextAction: null, allowedActions: [], blockedReasons: notEvaluated ? [] : evaluation.blockedReasons,
    evidenceGapCount: notEvaluated ? null : evaluation.evidenceGapCount,
    evaluatedAt: notEvaluated ? null : evaluation.evaluatedAt,
    requirements: evaluation.requirements?.map(item => ({...item, allowedActions: [], nextAction: null, inspectAction: null, reopenAction: null, missing: notEvaluated ? null : item.missing, actionUnavailableReason: notEvaluated ? 'Actions are withheld because this example has not been evaluated.' : 'This is a retained result. Return to the current evaluation before continuing.'})) ?? null,
    checks: evaluation.checks.map(check => notEvaluated ? {
      ...check, correctiveAction: null, demonstration: null, result: 'not_evaluated', resultLabel: stateLabel('not_evaluated'), resultDetail: 'Blocker count unknown',
      evaluatedAt: null, blockerCount: null, allowed: null, allowedActions: [], blockedReasons: [], explanation: 'No evaluation has run in this local example.',
    } : {...check, allowed: null, allowedActions: [], correctiveAction: null}),
  };
}

export function selectReadiness(journal, search = '', fixture = readinessFixture) {
  const location = readReadinessLocation(search, fixture);
  const response = fixture.responses.find(item => item.id === location.evaluation);
  const snapshot = response.status === 'stale' ? journal.previous || journal.current : journal.current;
  const evaluation = response.status === 'ready' ? snapshot : withholdActions(snapshot, response.status === 'not_evaluated');
  const transitions = evaluation.checks.filter(check => check.kind === 'transition');
  const counts = Object.fromEntries(readinessStates.map(status => [status.id, transitions.filter(check => check.result === status.id).length]));
  const checks = transitions.filter(check => location.checkState === 'all' || check.result === location.checkState).sort((a, b) => (location.order === 'blockers' ? resultOrder[a.result] - resultOrder[b.result] : 0) || a.order - b.order || a.id.localeCompare(b.id));
  const requirements = evaluation.requirements?.filter(item => location.requirements === 'all' || (location.requirements === 'resolved' ? item.recordedStatus === 'resolved' : item.status === 'open')) ?? null;
  let selection = null, unitSelection = null;
  if (location.selection) {
    const {kind, id} = location.selection;
    const resource = kind === 'requirement' ? evaluation.requirements?.find(item => item.id === id) : kind === 'check' ? evaluation.checks.find(item => item.id === id) : null;
    selection = resource ? {kind, id, status: 'ready', resource, outside: kind === 'requirement' ? !requirements.some(item => item.id === id) : resource.kind === 'transition' && !checks.some(item => item.id === id)} : {kind, id, status: 'not_found'};
  }
  if (location.unitSelection) {
    const {cardId, n} = location.unitSelection;
    const resource = !location.unitSelection.ambiguous && evaluation.coverage?.units.find(item => item.cardId === cardId && item.n === n);
    unitSelection = {kind: 'unit', cardId, n, status: resource ? 'ready' : 'not_found', resource: resource || null};
  }
  return {
    ...evaluation, status: response.status, response, examples: fixture.responses, location,
    visibleRequirements: requirements, visibleChecks: checks, transitionCount: transitions.length, counts,
    evidenceChecks: evaluation.checks.filter(check => check.kind === 'evidence'), selection, unitSelection,
    focusSelection: unitSelection || selection,
    nextAction: evaluation.nextAction,
    selectedRequirementOutside: selection?.status === 'ready' && selection.kind === 'requirement' && selection.outside ? selection.resource : null,
    selectedCheckOutside: selection?.status === 'ready' && selection.kind === 'check' && selection.outside ? selection.resource : null,
  };
}

export function readinessActionHref(action, search, paths = readinessFixture.paths) {
  const current = readReadinessLocation(search);
  const href = worklistActionHref(action, current.returnTo);
  const [address, hash] = href.split('#'), [path, query = ''] = address.split('?');
  const params = new URLSearchParams(query);
  params.set('readinessReturn', readinessHref(search, {}, paths));
  return `${path}?${params}${hash ? `#${hash}` : ''}`;
}

export function safeReadinessReturn(search, paths = readinessFixture.paths) {
  const value = new URLSearchParams(search).get('readinessReturn');
  if (!value || !value.startsWith(paths.readiness) || value.includes('#')) return null;
  const [path] = value.split('?');
  return path === paths.readiness ? value : null;
}

export {formatWorklistTime as formatReadinessTime};
