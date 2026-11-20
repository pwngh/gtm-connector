import {worklistFixture, worklistResponseExamples} from './renewalFixtures.js';
import {derive, workflowTransitionChecks} from './workflow.js';

const day = 86400000;
const stages = {negotiation: 'Negotiation', proposal: 'Proposal'};
const requirementKinds = {customer_approval: 'Customer approval', commercial_exception: 'Commercial exception', decision_owner: 'Decision owner'};
const statuses = {ready: 'Ready to act', blocked: 'Prerequisite open', planned: 'Not released', waiting: 'Waiting on a party', complete: 'Output recorded'};
const statusOrder = {ready: 0, waiting: 1, blocked: 2, planned: 3, complete: 4};
const modeLabels = {walkthrough: 'Interactive walkthrough', illustrative: 'Illustrative · inspect only'};
const illustrativeBlock = {action: 'execute', code: 'illustrative_only', reason: 'This is an illustrative snapshot. No assignment, review, or completion workflow exists for this account.'};
const timestamp = value => typeof value === 'string' && /(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null;
const person = (people, id) => people.find(item => item.id === id) || null;
const personNamed = (people, name) => people.find(item => item.displayName === name) || null;
const ownerFields = owner => ({owner, ownerId: owner?.id || null, ownerLabel: owner?.displayName || 'Unassigned'});
const keyFor = (kind, id) => `${kind}:${id}`;
const lastEvent = (state, types) => state.history.filter(event => !types || types.includes(event.type)).at(-1);
const revisionFor = (state, types, initial = '1') => String(Number(initial) + state.history.filter(event => types.includes(event.type)).length);

export const worklistNoun = (count, singular) => count === 1 ? singular : `${singular}s`;
export const worklistCount = (count, singular) => `${count} ${worklistNoun(count, singular)}`;

export function formatWorklistTime(value, dateOnly = false) {
  if (!value) return 'Not recorded';
  if (dateOnly && /^\d{4}-\d{2}-\d{2}$/.test(value)) {
    return new Date(`${value}T00:00:00Z`).toLocaleDateString('en-GB', {day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC'});
  }
  if (timestamp(value) === null) return `${value} · no time zone recorded`;
  return `${new Date(value).toLocaleString('en-GB', {day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'UTC'})} UTC`;
}

export function daysUntil(date, asOf) {
  if (!date || !/^\d{4}-\d{2}-\d{2}$/.test(date) || timestamp(asOf) === null) return null;
  const value = Date.parse(`${date}T00:00:00Z`);
  return Number.isFinite(value) ? Math.round((value - Math.floor(timestamp(asOf) / day) * day) / day) : null;
}

function ageLabel(value, asOf) {
  const time = timestamp(value), now = timestamp(asOf);
  if (time === null || now === null) return 'Unknown';
  const elapsed = now - time;
  if (elapsed < 0) return 'After snapshot';
  if (elapsed < 60000) return 'Just changed';
  if (elapsed < 3600000) return `${Math.floor(elapsed / 60000)}m ago`;
  if (elapsed < day) return `${Math.floor(elapsed / 3600000)}h ago`;
  return `${Math.floor(elapsed / day)}d ago`;
}

function changeFields(at, asOf, snapshotAt) {
  if (snapshotAt) return {lastChangedAt: null, lastChangedValue: null, lastChangedLabel: 'Snapshot only', lastChangedTime: `Captured ${formatWorklistTime(snapshotAt)}. No change history recorded.`};
  return {lastChangedAt: at, lastChangedValue: timestamp(at), lastChangedLabel: ageLabel(at, asOf), lastChangedTime: formatWorklistTime(at)};
}

function stepAction(item, paths, label, intent = 'continue') {
  const params = new URLSearchParams({workItem: item.id});
  return {id: 'open_step', label, intent, href: `${paths.workplan}?${params}#work-item-${item.id}`};
}

function walkthroughWork(item, state, d, fixture) {
  const planOwner = personNamed(fixture.people, state.plan.owner);
  const reviewer = person(fixture.people, fixture.walkthrough.reviewerId);
  let owner = planOwner, status = 'blocked', waitingOn = null, waitingReason = null, reason = null, label = 'Inspect walkthrough step';
  let events = [], output = null, outputAt = null, command = null;
  if (item.kind === 'plan_review') {
    events = ['save_plan', 'request_review', 'approve_plan', 'request_changes'];
    owner = state.plan.status === 'in_review' || d.isPlanApproved ? reviewer : planOwner;
    status = d.isPlanApproved ? 'complete' : 'ready';
    label = d.isPlanApproved ? 'Inspect approved plan' : d.canApprovePlan ? 'Review submitted plan' : 'Request plan review';
    command = d.isPlanApproved ? 'inspect_plan' : d.canApprovePlan ? 'approve_plan' : d.canRequestReview ? 'request_review' : null;
    output = d.isPlanApproved ? `Plan revision ${state.plan.revision} approved in the local walkthrough.` : null;
    outputAt = d.isPlanApproved ? state.plan.approvedAt : null;
    if (!d.isReviewerEligible && status !== 'complete') {
      status = 'blocked';
      reason = {action: 'review_plan', code: 'independent_reviewer', reason: d.planReviewBlockedReason};
    }
  } else if (item.kind === 'collection') {
    events = ['save_plan', 'approve_plan', 'obtain_document', 'reject_document'];
    // A rejected version reopens acquisition; its receipt stays recorded.
    const received = state.document && !d.documentRejected;
    status = received ? 'complete' : d.canObtainDocument || d.documentRejected && d.canReplaceDocument ? 'ready' : state.task ? 'blocked' : 'planned';
    label = received ? 'Inspect received document' : d.documentRejected ? 'Attach a replacement document' : 'Add current document';
    command = received ? 'inspect_document' : d.canObtainDocument || d.documentRejected && d.canReplaceDocument ? 'obtain_document' : null;
    output = received ? `Received document ${state.document.version} as receipt ${state.document.receiptId || 'not recorded'}. Inspection is separate.` : null;
    outputAt = received ? state.document.receivedAt : null;
    if (!state.document && !d.isPlanApproved) reason = {action: 'obtain_document', code: 'plan_approval', reason: 'The current plan revision needs internal approval before collection can start.'};
  } else if (item.kind === 'inspection') {
    events = ['obtain_document', 'verify_document', 'reject_document', 'save_plan'];
    status = d.isDocumentVerified ? 'complete' : d.canVerifyDocument ? 'ready' : 'blocked';
    label = d.isDocumentVerified ? 'Inspect verification record' : 'Verify document version';
    command = d.isDocumentVerified ? 'inspect_document' : d.canVerifyDocument ? 'verify_document' : null;
    output = d.isDocumentVerified ? `Inspection recorded for ${state.document.version} by ${state.document.verifiedBy || 'an unrecorded reviewer'}. A separate current-version basis is not recorded.` : null;
    outputAt = d.isDocumentVerified ? state.document.verifiedAt : null;
    if (status === 'blocked') reason = d.transitions.verify_document.blockedReasons.find(item => item.code === (!state.document ? 'document_receipt' : d.documentRejected ? 'document_not_rejected' : d.needsReplacement ? 'document_current_version' : 'plan_approval')) || d.transitions.verify_document.blockedReasons[0] || null;
  } else if (item.kind === 'customer_review') {
    events = ['request_customer_review', 'upload_approval_letter', 'inspect_approval_letter', 'record_customer_authority', 'record_customer_approval', 'obtain_document', 'save_plan', 'reopen_requirement'];
    const decision = d.hasCustomerApproval;
    // After the request, the customer's signed letter is awaited; its receipt, inspection and the authority record follow.
    const steps = [
      ['upload_approval_letter', d.canUploadApprovalLetter, 'Add the signed approval letter'],
      ['inspect_approval_letter', d.canInspectApprovalLetter, 'Inspect the approval letter'],
      ['record_customer_authority', d.canRecordCustomerAuthority, 'Record who decides, with evidence'],
      ['record_customer_approval', d.canRecordCustomerApproval, 'Record the cited customer decision'],
    ];
    const step = steps.find(([, allowed]) => allowed);
    const awaitingLetter = !decision && d.customerReviewIsCurrent && d.nextCommand === 'upload_approval_letter';
    status = decision ? 'complete' : awaitingLetter ? 'waiting' : step ? 'ready' : d.canRequestCustomerReview ? 'ready' : 'blocked';
    owner = decision || d.customerReviewIsCurrent ? reviewer : planOwner;
    waitingOn = awaitingLetter ? d.authorityContact ? {id: d.authorityContact.id, label: `${d.authorityContact.displayName} · ${d.authorityContact.role || 'recorded contact'}`, basis: 'Contact recorded from the inspected signed letter.'} : item.awaitedParty : null;
    label = decision ? 'Inspect recorded decision' : step ? step[2] : awaitingLetter ? 'Inspect the review request' : 'Request counsel review';
    command = decision ? 'inspect_decision' : step ? step[0] : d.canRequestCustomerReview ? 'request_customer_review' : null;
    output = decision ? `Customer decision recorded for ${state.customerReview.documentVersion}, citing ${state.customerReview.evidenceRef?.kind === 'receipt' ? `receipt ${state.customerReview.evidenceRef.id}` : 'a source unit'}. This does not resolve the requirement.` : null;
    outputAt = decision ? state.customerReview.approvedAt : null;
    if (status === 'blocked') reason = {action: 'request_customer_review', code: 'inspection', reason: 'Approve the work plan and inspect the received version before requesting customer review.'};
    if (status === 'waiting') {
      waitingReason = d.canUploadApprovalLetter ? 'The customer’s signed letter is awaited. When it arrives, its receipt and inspection come before anyone is recorded as the decision owner.'
        : d.transitions.upload_approval_letter.blockedReasons.find(reason => reason.code === 'letter_available')?.reason || 'The customer’s signed letter is awaited.';
    }
  }
  if (!owner && status !== 'complete') {
    status = 'blocked';
    reason = {action: 'execute', code: 'owner_missing', reason: 'An action owner has not been recorded. Inspect the work before assigning responsibility.'};
  }
  const unavailable = ['independent_reviewer', 'owner_missing'].includes(reason?.code);
  const allowedActions = unavailable ? [] : [stepAction(item, fixture.walkthrough.paths, ['blocked', 'planned'].includes(status) ? 'Inspect walkthrough step' : label, ['ready', 'waiting'].includes(status) ? 'continue' : 'inspect')];
  const readinessCommand = command || (item.kind === 'plan_review' ? d.nextCommand : null);
  const readinessCheckId = workflowTransitionChecks[readinessCommand] || (status === 'complete' ? null : item.readinessCheckId);
  return {
    ...item, title: item.kind === 'collection' ? state.task?.title || state.plan.title || item.title : item.title,
    ...ownerFields(owner), status, waitingOn, waitingReason, output, outputTimeLabel: output ? formatWorklistTime(outputAt) : null,
    planRevision: String(state.plan.revision), entryTargetId: `work-item-${item.id}`,
    workflowAction: command && !unavailable ? {command, label} : null,
    readinessCheckId,
    readinessAction: readinessCheckId ? {id: 'inspect_readiness', label: 'Inspect this transition’s readiness', href: `${fixture.walkthrough.paths.root}/readiness?${new URLSearchParams({check: readinessCheckId})}`}
      : status === 'complete' ? stepAction(item, fixture.walkthrough.paths, 'Inspect the recorded output', 'inspect') : null,
    replacementAction: item.kind === 'collection' && d.canReplaceDocument ? {command: 'obtain_document', label: 'Attach a replacement document'} : null,
    revision: revisionFor(state, events, item.revision), lastChangedAt: lastEvent(state, events)?.at || item.lastChangedAt,
    allowedActions, blockedReasons: reason ? [reason] : [],
    nextActionLabel: ['ready', 'waiting', 'complete'].includes(status) && allowedActions.length ? label : 'Inspect work item',
  };
}

// Local adapter only. These shapes are not an authorization boundary.
export function buildWorklist(state, fixture = worklistFixture) {
  const {people, walkthrough} = fixture;
  const d = derive(state, person(people, walkthrough.reviewerId)?.displayName ?? null);
  const knownTimes = [timestamp(fixture.asOf), ...state.history.map(event => timestamp(event.at))].filter(value => value !== null);
  const asOf = new Date(Math.max(...knownTimes)).toISOString();
  const requirements = fixture.requirements.map(item => {
    const interactive = item.renewalId === walkthrough.renewalId;
    const status = interactive ? state.requirement.status : item.status;
    const allowedActions = interactive ? [{id: 'open_requirement', label: 'Inspect requirement readiness', intent: 'inspect', href: `${walkthrough.paths.root}/readiness?${new URLSearchParams({requirement: item.id})}`} ] : [];
    return {
      ...item, status, kindLabel: requirementKinds[item.kind] || item.kind, selectionKey: keyFor('requirement', item.id),
      ...ownerFields(person(people, item.ownerId)),
      revision: interactive ? revisionFor(state, ['resolve_requirement', 'reopen_requirement', 'save_plan', 'obtain_document'], item.revision) : item.revision,
      allowedActions, blockedReasons: interactive ? [] : [illustrativeBlock],
    };
  });
  const workItems = fixture.workItems.map(item => {
    const renewal = fixture.renewals.find(record => record.id === item.renewalId);
    const interactive = renewal.mode === 'walkthrough';
    const snapshotAt = interactive ? null : renewal.snapshotAt || fixture.asOf;
    const current = interactive ? walkthroughWork(item, state, d, fixture) : {
      ...item, ...ownerFields(person(people, item.ownerId)), allowedActions: [], output: null,
      blockedReasons: [...(item.blockedReason ? [{action: 'complete', code: 'snapshot_prerequisite', reason: item.blockedReason}] : []), illustrativeBlock],
      waitingReason: item.waitingReason || null, nextActionLabel: 'Inspect snapshot',
    };
    const dueValue = timestamp(current.dueAt);
    return {
      ...current, ...changeFields(current.lastChangedAt, asOf, snapshotAt), snapshotAt, snapshotLabel: formatWorklistTime(snapshotAt),
      selectionKey: keyFor('work-item', item.id), mode: renewal.mode, modeLabel: modeLabels[renewal.mode],
      statusLabel: statuses[current.status], canAct: current.allowedActions.some(action => action.intent === 'continue'),
      dueValue, dueLabel: formatWorklistTime(current.dueAt),
      overdue: interactive && current.status !== 'complete' && dueValue !== null && dueValue < timestamp(asOf),
      dueSoon: interactive && current.status !== 'complete' && dueValue !== null && dueValue <= timestamp(asOf) + 7 * day,
      waitingLabel: current.waitingOn?.label || 'No party currently awaited',
      blockedLabel: current.blockedReasons.find(reason => reason.code !== 'illustrative_only')?.reason || 'No prerequisite open',
    };
  });
  const renewals = fixture.renewals.map(item => {
    const interactive = item.mode === 'walkthrough';
    const snapshotAt = interactive ? null : item.snapshotAt || fixture.asOf;
    const linkedRequirements = item.requirementsRecorded ? requirements.filter(record => record.renewalId === item.id) : null;
    const open = linkedRequirements?.filter(record => record.status !== 'resolved').sort((a, b) => Number(Boolean(b.blocking)) - Number(Boolean(a.blocking)) || a.kindLabel.localeCompare(b.kindLabel) || a.id.localeCompare(b.id));
    const work = workItems.filter(record => record.renewalId === item.id);
    const next = work.find(record => record.status !== 'complete');
    const nextStep = next ? {selectionKey: next.selectionKey, label: next.nextActionLabel, title: next.title, ...ownerFields(next.owner), waitingOn: next.waitingOn, waitingLabel: next.waitingLabel}
      : open?.length ? {selectionKey: open[0].selectionKey, label: 'Review requirement resolution', title: open[0].title, ...ownerFields(open[0].owner), waitingOn: null}
      : {selectionKey: keyFor('renewal', item.id), label: 'Inspect readiness', title: 'Renewal readiness', ...ownerFields(person(people, item.ownerId)), waitingOn: null};
    const daysLeft = daysUntil(item.renewalDate, snapshotAt || asOf);
    const daysLabel = daysLeft === null ? 'Days left unknown'
      : !interactive ? daysLeft < 0 ? 'Date precedes the snapshot' : `${worklistCount(daysLeft, 'day')} from snapshot`
      : daysLeft < 0 ? `${worklistCount(Math.abs(daysLeft), 'day')} overdue` : daysLeft === 0 ? 'Due today' : `${worklistCount(daysLeft, 'day')} left`;
    return {
      ...item, ...ownerFields(person(people, item.ownerId)), ...changeFields(interactive ? lastEvent(state)?.at || item.lastChangedAt : null, asOf, snapshotAt),
      snapshotAt, snapshotLabel: formatWorklistTime(snapshotAt),
      revision: interactive ? String(Number(item.revision) + state.history.length) : item.revision,
      selectionKey: keyFor('renewal', item.id), title: item.accountName, modeLabel: modeLabels[item.mode],
      renewalDateLabel: formatWorklistTime(item.renewalDate, true), daysLeft, daysLabel, urgentDate: interactive && daysLeft !== null && daysLeft <= 14,
      crm: {...item.crm, stageLabel: stages[item.crm.stage] || 'Unknown', readLabel: formatWorklistTime(item.crm.readAt)},
      commercial: interactive ? {...state.commercial, label: state.commercial.status === 'not_confirmed' ? 'Not confirmed' : state.commercial.label} : item.commercial,
      requirements: linkedRequirements, openRequirementCount: open?.length ?? null,
      openBlockerCount: open ? open.filter(record => record.blocking).length : null,
      urgentRequirement: open?.[0] || null, workItems: work, nextStep,
      blockerLabel: !open ? 'Blockers unknown' : `${open.filter(record => record.blocking).length} blocking`,
      readinessLabel: !open ? 'Readiness not recorded' : open.length ? `${worklistCount(open.length, 'open requirement')}` : 'No open requirements',
      allowedActions: interactive ? [{id: 'open_readiness', label: 'Open walkthrough readiness', intent: 'inspect', href: `${walkthrough.paths.root}/readiness`}] : [],
      blockedReasons: interactive ? [] : [illustrativeBlock],
    };
  });
  const rows = workItems.map(item => {
    const renewal = renewals.find(record => record.id === item.renewalId);
    return {...item, accountName: renewal.accountName, crm: renewal.crm, daysLeft: renewal.daysLeft, daysLabel: renewal.daysLabel, openBlockerCount: renewal.openBlockerCount, renewalSelectionKey: renewal.selectionKey};
  });
  return {
    revision: String(Number(fixture.revision) + state.history.length), asOf, asOfLabel: formatWorklistTime(asOf),
    snapshotAsOfLabel: formatWorklistTime(fixture.asOf), paths: walkthrough.paths, walkthroughRenewalId: walkthrough.renewalId,
    renewals, requirements, workItems: rows,
    openWorkItemCount: rows.filter(row => row.mode === 'walkthrough' && row.status !== 'complete').length,
  };
}

export const worklistSorts = [
  {id: 'days', label: 'Renewal days left', initialDirection: 'asc'},
  {id: 'blockers', label: 'Open blockers', initialDirection: 'desc'},
  {id: 'changed', label: 'Last change', initialDirection: 'desc'},
  {id: 'due', label: 'Work due date', initialDirection: 'asc', view: 'queue'},
];
export const worklistScopes = [
  {id: 'walkthrough', label: 'Walkthrough'},
  {id: 'illustrative', label: 'Illustrative snapshots'},
  {id: 'all', label: 'All records'},
];
export const worklistFacets = {
  renewals: [
    {id: 'blockers', label: 'Open blockers', matches: row => row.openBlockerCount > 0},
    {id: 'due-soon', label: 'Due within 30 days', matches: row => row.mode === 'walkthrough' && row.daysLeft !== null && row.daysLeft <= 30},
    {id: 'unassigned', label: 'Unassigned', matches: row => !row.ownerId},
    {id: 'unknown', label: 'Unknown readiness', matches: row => row.openRequirementCount === null},
  ],
  queue: [
    {id: 'actionable', label: 'Can act in walkthrough', matches: row => row.canAct},
    {id: 'blocked', label: 'Prerequisite open', matches: row => ['blocked', 'planned'].includes(row.status)},
    {id: 'waiting', label: 'Waiting on a party', matches: row => row.status === 'waiting'},
    {id: 'due-soon', label: 'Due within 7 days', matches: row => row.dueSoon},
    {id: 'unassigned', label: 'Unassigned', matches: row => !row.ownerId},
  ],
};

export function readWorklistLocation(search, view = 'renewals') {
  const params = new URLSearchParams(search);
  const allowedSorts = worklistSorts.filter(sort => !sort.view || sort.view === view);
  const sort = allowedSorts.find(item => item.id === params.get('sort')) || allowedSorts.find(item => item.id === (view === 'queue' ? 'due' : 'days'));
  const response = worklistResponseExamples.find(item => item.id === params.get('response')) || worklistResponseExamples[0];
  const options = {
    sort: allowedSorts.map(item => item.id), direction: ['asc', 'desc'], status: view === 'queue' ? ['open', 'all', 'complete'] : [],
    scope: worklistScopes.map(item => item.id), response: worklistResponseExamples.map(item => item.id), limit: ['all'],
    attention: worklistFacets[view].map(item => item.id),
  };
  const invalidFields = Object.entries(options).filter(([key, values]) => params.getAll(key).some(value => !values.includes(value))).map(([key]) => key);
  return {
    q: params.get('q') || '', owner: params.get('owner') || '', stage: params.get('stage') || '',
    attention: [...new Set(params.getAll('attention'))].filter(id => options.attention.includes(id)),
    scope: options.scope.includes(params.get('scope')) ? params.get('scope') : 'walkthrough',
    sort: sort.id, direction: options.direction.includes(params.get('direction')) ? params.get('direction') : sort.initialDirection,
    status: view === 'queue' && options.status.includes(params.get('status')) ? params.get('status') : 'open',
    selection: params.get('selection') || '', response: response.id, expanded: params.get('limit') === 'all',
    invalid: invalidFields.length > 0, invalidFields,
  };
}

export function updateWorklistSearch(search, changes) {
  const params = new URLSearchParams(search);
  for (const [key, value] of Object.entries(changes)) {
    params.delete(key);
    if (Array.isArray(value)) for (const item of value) params.append(key, item);
    else if (value != null && value !== '') params.set(key, String(value));
  }
  return params;
}

export function clearWorklistSearch(search, view) {
  const invalid = Object.fromEntries(readWorklistLocation(search, view).invalidFields.map(key => [key, null]));
  return updateWorklistSearch(search, {...invalid, q: null, owner: null, stage: null, attention: [], status: null, scope: null});
}

export function worklistHref(view, search, changes = {}) {
  const params = updateWorklistSearch(search, changes);
  return `/${view}${params.size ? `?${params}` : ''}`;
}

function compareNullable(a, b, direction) {
  if (a === null || a === undefined) return b === null || b === undefined ? 0 : 1;
  if (b === null || b === undefined) return -1;
  return (a - b) * (direction === 'desc' ? -1 : 1);
}

function relatesToSelection(row, selection, view) {
  if (view === 'renewals') return row.id === selection.renewal.id;
  if (selection.kind === 'renewal') return row.renewalId === selection.renewal.id;
  if (selection.kind === 'requirement') return row.requirementId === selection.resource.id;
  return row.selectionKey === selection.resource.selectionKey;
}

export function selectWorklist(data, view = 'renewals', search = '') {
  const location = readWorklistLocation(search, view);
  const response = worklistResponseExamples.find(example => example.id === location.response);
  const singularUnit = view === 'renewals' ? 'renewal' : 'work item', unit = `${singularUnit}s`;
  const base = {status: response.status, response, location, unit, singularUnit, revision: data.revision, examples: worklistResponseExamples, sorts: worklistSorts.filter(sort => !sort.view || sort.view === view)};
  if (response.status !== 'ready') return {...base, rows: null, facets: null, total: null, selection: null};
  const all = view === 'renewals' ? data.renewals : data.workItems;
  const ownerOptions = [...new Map(all.filter(row => row.owner).map(row => [row.owner.id, row.owner])).values()];
  const stageOptions = [...new Set(all.map(row => row.crm.stage))].map(stage => ({id: stage || 'unknown', label: stages[stage] || 'Unknown'}));
  const statusFiltered = all.filter(row => view !== 'queue' || location.status === 'all' || (location.status === 'complete' ? row.status === 'complete' : row.status !== 'complete'));
  const searched = statusFiltered.filter(row => {
    const matchesQuery = `${row.accountName} ${row.title || ''} ${row.id}`.toLowerCase().includes(location.q.trim().toLowerCase());
    return matchesQuery && (!location.owner || (location.owner === 'unassigned' ? !row.ownerId : row.ownerId === location.owner))
      && (!location.stage || (location.stage === 'unknown' ? !row.crm.stage : row.crm.stage === location.stage));
  });
  const scopes = worklistScopes.map(scope => ({...scope, count: searched.filter(row => scope.id === 'all' || row.mode === scope.id).length}));
  const scoped = searched.filter(row => location.scope === 'all' || row.mode === location.scope);
  const facets = worklistFacets[view].map(facet => ({id: facet.id, label: facet.label, count: scoped.filter(facet.matches).length, unit}));
  const matched = scoped.filter(row => !location.attention.length || worklistFacets[view].some(facet => location.attention.includes(facet.id) && facet.matches(row)));
  const sortField = {days: 'daysLeft', blockers: 'openBlockerCount', changed: 'lastChangedValue', due: 'dueValue'}[location.sort];
  const sorted = [...matched].sort((a, b) => compareNullable(a[sortField], b[sortField], location.direction)
    || (view === 'queue' ? statusOrder[a.status] - statusOrder[b.status] || compareNullable(a.step, b.step, 'asc') : 0)
    || a.id.localeCompare(b.id));
  const rows = response.limit && !location.expanded ? sorted.slice(0, response.limit) : sorted;
  const selection = resolveWorklistSelection(data, location.selection);
  if (selection?.status === 'ready') {
    const relatedRow = rows.find(row => relatesToSelection(row, selection, view));
    selection.visible = Boolean(relatedRow);
    selection.rowKey = relatedRow?.selectionKey || null;
  }
  return {
    ...base, asOf: data.asOf, asOfLabel: data.asOfLabel, snapshotAsOfLabel: data.snapshotAsOfLabel,
    rows, total: sorted.length, available: all.length, scopeCount: scoped.length, scopes,
    facetScopeCount: scoped.length, facets, ownerOptions, stageOptions, selection,
    truncated: rows.length < sorted.length, remaining: sorted.length - rows.length,
    invalid: location.invalid || (Boolean(location.owner) && location.owner !== 'unassigned' && !ownerOptions.some(item => item.id === location.owner)) || (Boolean(location.stage) && !stageOptions.some(item => item.id === location.stage)),
  };
}

export function resolveWorklistSelection(data, selection) {
  if (!selection) return null;
  const resource = [...data.renewals, ...data.requirements, ...data.workItems].find(item => item.selectionKey === selection);
  if (!resource) return {status: 'not_found', key: selection};
  const kind = selection.slice(0, selection.indexOf(':'));
  const renewal = kind === 'renewal' ? resource : data.renewals.find(item => item.id === resource.renewalId);
  return {status: 'ready', kind, resource, renewal};
}

export function worklistActionHref(action, returnTo) {
  if (!returnTo) return action.href;
  const [address, hash] = action.href.split('#');
  const [path, query = ''] = address.split('?');
  const params = new URLSearchParams(query);
  params.set('returnTo', returnTo);
  return `${path}?${params}${hash ? `#${hash}` : ''}`;
}

export function resolveWorklistEntry(data, pathname, search) {
  const params = new URLSearchParams(search);
  const id = params.get('workItem'), requirementId = params.get('requirement');
  if (pathname !== data.paths.workplan || (!id && !requirementId)) return null;
  const resource = id ? data.workItems.find(item => item.id === id && item.mode === 'walkthrough') : data.requirements.find(item => item.id === requirementId && item.renewalId === data.walkthroughRenewalId);
  const returnTo = params.has('returnTo') ? safeWorklistReturn(params.get('returnTo')) : null;
  return resource ? {status: 'ready', id: resource.id, title: resource.title, kind: id ? 'work-item' : 'requirement', resource, targetId: id ? resource.entryTargetId : null, step: id ? resource.step : 4, returnTo} : {status: 'not_found', returnTo};
}

export function safeWorklistReturn(value) {
  return typeof value === 'string' && /^\/(renewals|queue)(\?[^#]*)?$/.test(value) ? value : '/queue';
}

export function worklistAccountHref(pathname, search, selection) {
  const params = new URLSearchParams(search);
  const origin = ['/renewals', '/queue'].includes(pathname) ? `${pathname}${params.size ? `?${params}` : ''}`
    : params.has('returnTo') ? safeWorklistReturn(params.get('returnTo')) : '/renewals';
  const [path, query = ''] = origin.split('?');
  return worklistHref(path.slice(1), query, {selection});
}
