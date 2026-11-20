import {readinessFixture} from './renewalFixtures.js';
import {derive} from './workflow.js';
import {buildWorklist, formatWorklistTime, safeWorklistReturn, worklistActionHref} from './worklist.js';

export const readinessStates = [
  {id: 'not_evaluated', label: 'Not evaluated'},
  {id: 'not_applicable', label: 'Not applicable'},
  {id: 'failed', label: 'Failed'},
  {id: 'passed', label: 'Passed'},
];
export const readinessStorageKey = 'gtm-readiness-evaluations-v1';
const stateLabel = value => readinessStates.find(item => item.id === value)?.label || 'Unknown';
const countLabel = (count, singular) => count === null ? 'Unknown' : `${count} ${singular}${count === 1 ? '' : 's'}`;
const person = (fixture, id) => fixture.worklist.people.find(item => item.id === id) || null;
const resultOrder = {failed: 0, not_evaluated: 1, passed: 2, not_applicable: 3};
const requirementLabel = status => ({open: 'Open', resolved: 'Resolved in walkthrough'})[status] || 'Unknown';
const copy = value => JSON.parse(JSON.stringify(value));

function inputVersions(state, fixture, worklist) {
  const work = kind => worklist.workItems.find(item => item.renewalId === fixture.renewalId && item.kind === kind);
  const resource = (kind, label, value, extra = {}) => ({kind, label, id: value?.id ?? null, revision: value?.revision == null ? null : String(value.revision), ...extra});
  return [
    resource('plan', 'Work plan', state.plan, {stateRevision: work('plan_review')?.revision ?? null}),
    resource('document', 'Received document', state.document && {...state.document, revision: work('inspection')?.revision}, {version: state.document?.version ?? null}),
    resource('customer_decision', 'Customer review record', state.customerReview && {id: work('customer_review')?.id, revision: work('customer_review')?.revision}, {version: state.customerReview?.documentVersion ?? null}),
    resource('requirement', 'Workflow requirement', worklist.requirements.find(item => item.id === state.requirement.id)),
    resource('version_basis', 'Separate version source', fixture.documentVersionBasis),
    resource('authority', 'Customer authority basis', fixture.customerAuthority),
    resource('decision_receipt', 'Cited decision receipt', fixture.decisionReceipt),
    resource('card', 'Source card', fixture.card),
    resource('policy', 'Local workflow rules', {id: fixture.policyVersion, revision: fixture.revision}),
  ];
}

function reasonsFor(state, d) {
  return {
    independent_reviewer: {met: d.isReviewerEligible, name: 'Independent internal reviewer', reason: d.planReviewBlockedReason, checkId: 'submit_plan'},
    plan_submission: {met: state.plan.status === 'in_review', name: 'Current plan submitted for review', reason: 'Submit the current plan revision before it can be approved.', checkId: 'submit_plan'},
    plan_approval: {met: d.isPlanApproved, name: 'Current plan approval', reason: 'The current plan revision needs internal approval.', checkId: 'approve_plan'},
    document_receipt: {met: Boolean(state.document), name: 'Document receipt', reason: 'The applicable document has not been received.', checkId: 'receive_document'},
    document_inspection: {met: d.isDocumentVerified, name: 'Document inspection', reason: 'The received version needs a current walkthrough inspection.', checkId: 'verify_document'},
    customer_request: {met: d.customerReviewIsCurrent, name: 'Current customer review request', reason: 'A review request must match the current walkthrough inputs.', checkId: 'request_customer_review'},
    customer_approval: {met: d.hasCustomerApproval, name: 'Recorded customer approval', reason: 'A current customer approval record is needed for this document.', checkId: 'record_customer_decision'},
    open_requirement: {met: state.requirement.status === 'open', name: 'Open requirement', reason: 'The recorded requirement must be open for a resolution transition.', checkId: 'resolve_requirement'},
  };
}

const transitionInputs = {
  request_review: ['independent_reviewer'],
  approve_plan: ['independent_reviewer', 'plan_submission'],
  obtain_document: ['plan_approval'],
  verify_document: ['plan_approval', 'document_receipt'],
  request_customer_review: ['plan_approval', 'document_receipt', 'document_inspection'],
  record_customer_approval: ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request'],
  resolve_requirement: ['plan_approval', 'document_receipt', 'document_inspection', 'customer_request', 'customer_approval', 'open_requirement'],
};

function notApplicable(command, state, d) {
  if (command === 'request_review' && (d.isPlanApproved || state.plan.status === 'in_review')) return 'The current revision has already been submitted. A new submission is not needed now.';
  if (command === 'approve_plan' && d.isPlanApproved) return 'The current plan revision is already approved.';
  if (command === 'obtain_document' && state.document) return 'A document has already been received. Inspection is a separate step.';
  if (command === 'verify_document' && d.isDocumentVerified) return 'Inspection is already recorded for this walkthrough version. Its independent version basis is checked separately.';
  if (command === 'request_customer_review' && d.customerReviewIsCurrent) return 'A current review record already exists. Do not request it again.';
  if (command === 'record_customer_approval' && d.hasCustomerApproval) return 'The walkthrough already has a current customer decision record.';
  if (command === 'resolve_requirement' && state.requirement.status === 'resolved') return 'The recorded requirement is already resolved. Reopening needs an explicit reason.';
  return null;
}

function commandAction(definition, state, fixture, worklist) {
  const item = worklist.workItems.find(row => row.renewalId === fixture.renewalId && row.kind === definition.workKind);
  const requirement = worklist.requirements.find(row => row.id === state.requirement.id);
  const params = item ? new URLSearchParams({workItem: item.id}) : new URLSearchParams({requirement: state.requirement.id});
  return {
    id: definition.command, command: definition.command, label: definition.actionLabel, intent: 'open_local_step',
    checkId: definition.id, owner: item?.owner || requirement?.owner || null,
    href: `${fixture.paths.workplan}?${params}${item ? `#${item.entryTargetId}` : ''}`,
  };
}

function evaluateTransition(definition, state, fixture, worklist, d, gates) {
  if (!(definition.allowedFlag in d)) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation: 'No evaluation rule is recorded for this transition.'};
  const allowed = Boolean(d[definition.allowedFlag]);
  if (allowed) return {result: 'passed', allowed: true, blockedReasons: [], allowedActions: [commandAction(definition, state, fixture, worklist)], explanation: 'The existing walkthrough permits this step with these inputs.'};
  const inapplicable = notApplicable(definition.command, state, d);
  if (inapplicable) return {result: 'not_applicable', allowed: false, blockedReasons: [], allowedActions: [], explanation: inapplicable};
  const blockedReasons = (transitionInputs[definition.command] || []).filter(code => !gates[code].met).map(code => {
    const {met, ...reason} = gates[code];
    return {code, action: definition.command, ...reason};
  });
  if (!blockedReasons.length) blockedReasons.push({code: 'workflow_state', action: definition.command, name: 'Supported workflow state', reason: 'The existing workflow does not allow this transition from the recorded state.', checkId: null});
  return {result: 'failed', allowed: false, blockedReasons, allowedActions: [], explanation: 'Every unmet local prerequisite for this transition is listed below.'};
}

function evaluateEvidence(definition, state, fixture) {
  let basis, explanation, valid = false, reason;
  if (definition.id === 'current_version_basis') {
    basis = fixture.documentVersionBasis;
    explanation = 'No separate source identifies the current version. The document’s own currentVersion field is not evidence.';
    valid = Boolean(state.document?.id && state.document.version && basis?.source && basis.documentId === state.document?.id && basis.version === state.document?.version && basis.agreementId === fixture.agreement.id);
    reason = 'The separately sourced version does not establish the received file as applicable to this agreement.';
  } else if (definition.id === 'customer_authority') {
    basis = fixture.customerAuthority;
    explanation = 'Customer authority is unidentified. Neither the customer role nor the walkthrough’s typed counsel name is an authority basis.';
    valid = Boolean(basis?.contactId && basis.evidenceRef && basis.agreementId === fixture.agreement.id && basis.status === 'accepted');
    reason = 'The authority record is incomplete, withdrawn, or has not been accepted for this agreement.';
  } else {
    basis = fixture.decisionReceipt;
    explanation = 'No cited unit or inspected approval receipt is recorded. Any typed walkthrough decision stays labelled as reviewer input.';
    valid = Boolean(state.document?.id && state.document.version && basis?.evidenceRef && basis.status === 'accepted' && basis.agreementId === fixture.agreement.id && basis.documentId === state.document?.id && basis.documentVersion === state.document?.version);
    reason = 'The decision citation has not been accepted for this document version.';
  }
  if (!basis) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation};
  return {
    result: valid ? 'passed' : 'failed', allowed: null, allowedActions: [],
    blockedReasons: valid ? [] : [{code: definition.id, action: 'establish_evidence', name: definition.title, reason, checkId: definition.id}],
    explanation: valid ? 'A separate, accepted evidence record supports this check.' : reason,
  };
}

function decorateCheck(check, evaluatedAt, versions) {
  const evaluated = check.result !== 'not_evaluated';
  const blockerCount = ['passed', 'failed'].includes(check.result) ? check.blockedReasons.length : null;
  return {
    ...check, evaluatedAt: evaluated ? evaluatedAt : null,
    inputVersions: versions.filter(input => check.inputKinds.includes(input.kind)),
    resultLabel: stateLabel(check.result), blockerCount,
    resultDetail: check.result === 'passed' ? '0 blockers in this check'
      : check.result === 'failed' ? countLabel(blockerCount, 'blocker')
      : check.result === 'not_applicable' ? 'Not needed in this state' : 'Blocker count unknown',
  };
}

export function evaluateReadiness(state, fixture = readinessFixture) {
  const reviewer = person(fixture, fixture.worklist.walkthrough.reviewerId);
  const d = derive(state, reviewer?.displayName ?? null);
  const worklist = buildWorklist(state, fixture.worklist);
  const renewal = worklist.renewals.find(row => row.id === fixture.renewalId);
  const evaluatedAt = state.history.length ? state.history.at(-1).at ?? null : fixture.initialEvaluationAt;
  const versions = inputVersions(state, fixture, worklist), gates = reasonsFor(state, d);
  const checks = fixture.checks.map((definition, index) => decorateCheck({
    ...definition, order: index, revision: String(Number(fixture.revision) + state.history.length),
    ...(definition.kind === 'transition' ? evaluateTransition(definition, state, fixture, worklist, d, gates) : evaluateEvidence(definition, state, fixture)),
  }, evaluatedAt, versions));
  const requirements = fixture.requirementsRecorded ? fixture.requirements.filter(record => record.renewalId === fixture.renewalId).map(record => {
    const bound = record.binding === 'workflow_requirement' && record.id === state.requirement.id;
    const status = bound ? state.requirement.status : record.status;
    const related = checks.filter(check => record.checkIds.includes(check.id));
    const evidenceChecks = checks.filter(check => record.evidenceCheckIds.includes(check.id));
    const resolution = related.find(check => check.command === 'resolve_requirement');
    const nextAction = bound && status === 'open' ? related.flatMap(check => check.allowedActions)[0] || null : null;
    const owner = person(fixture, record.ownerId);
    const planWork = worklist.workItems.find(item => item.renewalId === fixture.renewalId && item.kind === 'plan_review');
    const nextStepOwner = nextAction ? nextAction.owner : (bound && status === 'open' && !d.isPlanApproved ? state.plan.owner?.trim() ? planWork?.owner || null : null : owner);
    const workflowMissing = bound && status === 'open' ? resolution?.blockedReasons || [] : [];
    const missing = [...workflowMissing, ...evidenceChecks.filter(check => check.result !== 'passed').map(check => ({
      code: check.id, name: check.title, reason: check.explanation, checkId: check.id, evidenceGap: true,
    }))];
    if (bound && status === 'open' && resolution?.allowed) missing.unshift({code: 'explicit_resolution', name: 'Explicit requirement resolution', reason: 'The walkthrough decision is recorded. A reviewer still needs to record the resolution.', checkId: resolution.id});
    const waitingOn = bound && d.canRecordCustomerApproval ? record.awaitedParty : null;
    return {
      ...record, agreement: fixture.agreement, title: bound ? state.requirement.title : record.title, status, statusLabel: requirementLabel(status),
      revision: worklist.requirements.find(row => row.id === record.id)?.revision || String(record.revision),
      resolvedAt: bound ? state.requirement.resolvedAt : null,
      owner, ownerLabel: owner?.displayName || 'Unassigned', nextStepOwner, nextStepOwnerLabel: nextStepOwner?.displayName || 'Unassigned',
      waitingOn, waitingLabel: waitingOn?.label || 'No external party currently awaited',
      customerAuthorityLabel: evidenceChecks.some(check => check.id === 'customer_authority' && check.result === 'passed') ? fixture.customerAuthority.contactLabel || 'Contact label not recorded' : 'Unidentified',
      missing, evidenceChecks, nextAction, inputVersions: versions.filter(input => input.kind !== 'policy'),
      allowedActions: nextAction ? [nextAction] : bound && d.canReopenRequirement ? [{id: 'reopen_requirement', command: 'reopen_requirement', label: 'Reopen with a reason', intent: 'local_form', owner}] : [],
      blockedReasons: status === 'open' ? workflowMissing : [],
      actionUnavailableReason: !bound ? 'No executable workflow is recorded for this requirement.' : status === 'open' && !nextAction ? d.planReviewBlockedReason || 'No corrective transition is currently permitted by the local workflow.' : null,
      inspectAction: bound && status === 'open' && !nextAction && planWork ? {id: 'inspect_prerequisite', label: 'Inspect review prerequisites', intent: 'inspect', href: `${fixture.paths.workplan}?${new URLSearchParams({workItem: planWork.id})}#${planWork.entryTargetId}`} : null,
      citations: record.citations.map(citation => ({...citation, unit: fixture.card?.units.find(unit => unit.cardId === citation.cardId && unit.n === citation.n) || null})),
      recordedDecision: bound && state.customerReview?.status === 'approved' ? {
        label: 'Reviewer input in the existing walkthrough', text: state.customerReview.evidenceText,
        recordedBy: state.customerReview.recordedBy || null, recordedAt: state.customerReview.approvedAt || null,
        documentVersion: state.customerReview.documentVersion || null,
      } : null,
    };
  }) : null;
  const openCount = requirements && requirements.every(item => ['open', 'resolved'].includes(item.status)) ? requirements.filter(item => item.status === 'open').length : null;
  const inputKey = JSON.stringify({
    fixture, plan: state.plan, document: state.document, decision: state.customerReview, requirement: state.requirement,
    commercial: state.commercial, history: state.history,
  });
  return {
    id: fixture.id, renewalId: fixture.renewalId, revision: String(Number(fixture.revision) + state.history.length),
    evaluatedAt, inputVersions: versions, inputKey, lastEventId: state.history.at(-1)?.id || null,
    paths: fixture.paths, agreement: fixture.agreement, scope: fixture.scope, policyBoundary: fixture.policyBoundary,
    crm: renewal.crm, commercial: renewal.commercial, requirements, checks,
    openRequirementCount: openCount, requirementCount: requirements?.length ?? null,
    requirementCountLabel: openCount === null ? 'Requirements unknown' : countLabel(openCount, 'open requirement'),
    evidenceGapCount: checks.filter(check => check.kind === 'evidence' && check.result !== 'passed').length,
    allowedActions: checks.flatMap(check => check.allowedActions), blockedReasons: checks.flatMap(check => check.blockedReasons),
    coverage: fixture.card ? copy(fixture.card) : null,
    document: state.document ? {id: state.document.id, version: state.document.version || null, receivedAt: state.document.receivedAt || null, verifiedAt: state.document.verifiedAt || null, verificationRecorded: d.isDocumentVerified} : null,
  };
}

const validEvaluation = value => value && typeof value.inputKey === 'string' && Array.isArray(value.checks)
  && value.checks.every(check => readinessStates.some(status => status.id === check.result) && Array.isArray(check.blockedReasons) && Array.isArray(check.allowedActions) && Array.isArray(check.inputVersions))
  && (value.requirements === null || Array.isArray(value.requirements) && value.requirements.every(item => Array.isArray(item.missing) && Array.isArray(item.citations) && Array.isArray(item.allowedActions)))
  && Array.isArray(value.inputVersions) && typeof value.evaluationId === 'string' && value.paths && value.crm && value.commercial
  && (value.coverage === null || value.coverage && Array.isArray(value.coverage.units) && Array.isArray(value.coverage.sources) && Array.isArray(value.coverage.gaps));

export function readReadinessJournal(value) {
  try {
    const parsed = typeof value === 'string' ? JSON.parse(value) : value;
    return parsed?.schema === '1' && /^\d+$/.test(parsed.sequence) && validEvaluation(parsed.current) && (!parsed.previous || validEvaluation(parsed.previous))
      && parsed.changes?.cause && Array.isArray(parsed.changes.requirements) && Array.isArray(parsed.changes.checks) ? parsed : null;
  } catch {return null;}
}

function compareRequirements(before, after) {
  const oldItems = before.requirements || [], newItems = after.requirements || [];
  return [...new Set([...oldItems, ...newItems].map(item => item.id))].flatMap(id => {
    const old = oldItems.find(item => item.id === id), next = newItems.find(item => item.id === id);
    const previousMissing = old?.missing || [], nextMissing = next?.missing || [];
    const added = nextMissing.filter(item => !previousMissing.some(prior => prior.code === item.code));
    const cleared = previousMissing.filter(item => !nextMissing.some(current => current.code === item.code));
    const differences = [];
    if (old?.status !== next?.status) differences.push({field: 'status', label: 'Recorded state', before: old?.statusLabel || 'Not recorded', after: next?.statusLabel || 'Not recorded'});
    if (old?.nextAction?.id !== next?.nextAction?.id) differences.push({field: 'action', label: 'Next permitted step', before: old?.nextAction?.label || 'No permitted step', after: next?.nextAction?.label || 'No permitted step'});
    if (old?.nextStepOwnerLabel !== next?.nextStepOwnerLabel) differences.push({field: 'owner', label: 'Action owner', before: old?.nextStepOwnerLabel || 'Unassigned', after: next?.nextStepOwnerLabel || 'Unassigned'});
    if (old?.waitingLabel !== next?.waitingLabel) differences.push({field: 'waiting', label: 'Waiting on', before: old?.waitingLabel || 'Not recorded', after: next?.waitingLabel || 'Not recorded'});
    for (const input of next?.inputVersions || []) {
      const prior = old?.inputVersions?.find(item => item.kind === input.kind);
      const describe = value => !value?.id ? 'Not recorded' : `${value.id} · Revision ${value.revision ?? 'not recorded'}${value.stateRevision ? ` · State ${value.stateRevision}` : ''}${value.version ? ` · Version ${value.version}` : ''}`;
      if (describe(prior) !== describe(input)) differences.push({field: `input:${input.kind}`, label: `${input.label} input`, before: describe(prior), after: describe(input)});
    }
    if (old?.title !== next?.title) differences.push({field: 'title', label: 'Requirement title', before: old?.title || 'Not recorded', after: next?.title || 'Not recorded'});
    if (!added.length && !cleared.length && !differences.length) return [];
    return [{id, title: next?.title || old.title, added, cleared, differences}];
  });
}

export function advanceReadinessJournal(previousJournal, state, fixture = readinessFixture) {
  const stored = readReadinessJournal(previousJournal);
  const journal = stored?.current.renewalId === fixture.renewalId ? stored : null;
  const evaluation = evaluateReadiness(state, fixture);
  if (journal?.current.inputKey === evaluation.inputKey) {
    const {evaluationId, ...saved} = journal.current;
    if (JSON.stringify(saved) === JSON.stringify(evaluation)) return journal;
    return {...journal, current: copy({...evaluation, evaluationId})};
  }
  const sequence = String(Number(journal?.sequence || '0') + 1);
  const current = copy({...evaluation, evaluationId: `${fixture.id}:${sequence}`});
  const previous = journal?.current || null;
  const event = state.history.at(-1);
  const cause = event && event.id !== previous?.lastEventId ? {
    id: event.id, title: event.title, detail: event.detail || 'No reason recorded.', at: event.at || null, actor: event.actor || null,
  } : {id: null, title: previous ? 'Evaluation inputs changed' : 'Initial local evaluation', detail: previous ? 'The recorded inputs changed without a new workflow event. No actor or reason was supplied.' : 'Evaluated the available local records. This is not a live source read.', at: current.evaluatedAt, actor: null};
  const changedChecks = previous ? current.checks.flatMap(check => {
    const before = previous.checks.find(item => item.id === check.id);
    if (before?.result === check.result && JSON.stringify(before.blockedReasons.map(item => item.code)) === JSON.stringify(check.blockedReasons.map(item => item.code))) return [];
    return [{id: check.id, title: check.title, before: before?.result ?? null, after: check.result, beforeLabel: before?.resultLabel || 'Not recorded', afterLabel: check.resultLabel,
      added: check.blockedReasons.filter(item => !before?.blockedReasons.some(old => old.code === item.code)),
      cleared: before?.blockedReasons.filter(item => !check.blockedReasons.some(next => next.code === item.code)) || []}];
  }) : [];
  return {schema: '1', sequence, current, previous, changes: {cause, requirements: previous ? compareRequirements(previous, current) : [], checks: changedChecks}};
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
    ...evaluation, allowedActions: [], blockedReasons: notEvaluated ? [] : evaluation.blockedReasons,
    evidenceGapCount: notEvaluated ? null : evaluation.evidenceGapCount,
    evaluatedAt: notEvaluated ? null : evaluation.evaluatedAt,
    requirements: evaluation.requirements?.map(item => ({...item, allowedActions: [], nextAction: null, inspectAction: null, missing: notEvaluated ? null : item.missing, actionUnavailableReason: notEvaluated ? 'Actions are withheld because this example has not been evaluated.' : 'This is a retained result. Return to the current evaluation before continuing.'})) ?? null,
    checks: evaluation.checks.map(check => notEvaluated ? {
      ...check, result: 'not_evaluated', resultLabel: stateLabel('not_evaluated'), resultDetail: 'Blocker count unknown',
      evaluatedAt: null, blockerCount: null, allowed: null, allowedActions: [], blockedReasons: [], explanation: 'No evaluation has run in this local example.',
    } : {...check, allowed: null, allowedActions: []}),
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
  const requirements = evaluation.requirements?.filter(item => location.requirements === 'all' || item.status === (location.requirements === 'resolved' ? 'resolved' : 'open')) ?? null;
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
    nextAction: evaluation.allowedActions[0] || null,
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
