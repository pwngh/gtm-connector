import {fixturePeople, renewalFixture, worklistFixture, worklistPeople, worklistResponseExamples} from './renewalFixtures.js';
import {derive} from './workflow.js';

const day = 86400000;
const stages = {negotiation: 'Negotiation', proposal: 'Proposal'};
const requirementKinds = {customer_approval: 'Customer approval', commercial_exception: 'Commercial exception', decision_owner: 'Decision owner'};
const statuses = {ready: 'Ready to act', blocked: 'Prerequisite open', planned: 'Not released', waiting: 'Waiting on a party', complete: 'Output recorded'};
const modeLabels = {walkthrough: 'Interactive walkthrough', illustrative: 'Illustrative · inspect only'};
const illustrativeBlock = {action: 'execute', code: 'illustrative_only', reason: 'This is an illustrative snapshot. No assignment, review, or completion workflow exists for this account.'};
const timestamp = value => typeof value === 'string' && /(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null;
const person = id => worklistPeople.find(item => item.id === id) || null;
const personNamed = name => worklistPeople.find(item => item.displayName === name) || null;
const ownerFields = owner => ({owner, ownerId: owner?.id || null, ownerLabel: owner?.displayName || 'Unassigned'});
const keyFor = (kind, id) => `${kind}:${id}`;
const lastEvent = (state, types) => state.history.filter(event => !types || types.includes(event.type)).at(-1);
const revisionFor = (state, types, initial = '1') => String(Number(initial) + state.history.filter(event => types.includes(event.type)).length);

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

function changeFields(at, asOf) {
  return {lastChangedAt: at, lastChangedValue: timestamp(at), lastChangedLabel: ageLabel(at, asOf), lastChangedTime: formatWorklistTime(at)};
}

function stepAction(item, label, intent = 'continue') {
  const params = new URLSearchParams({workItem: item.id});
  return {id: 'open_step', label, intent, href: `${renewalFixture.paths.workplan}?${params}`};
}

function walkthroughWork(item, state, d) {
  const planOwner = personNamed(state.plan.owner);
  const reviewer = personNamed(fixturePeople.internalReviewer);
  const independent = planOwner?.id !== reviewer?.id;
  let owner = planOwner, status = 'blocked', waitingOn = null, reason = null, label = 'Inspect walkthrough step';
  let events = [], output = null;
  if (item.kind === 'plan_review') {
    events = ['save_plan', 'request_review', 'approve_plan', 'request_changes'];
    owner = state.plan.status === 'in_review' || d.isPlanApproved ? reviewer : planOwner;
    status = d.isPlanApproved ? 'complete' : 'ready';
    label = d.isPlanApproved ? 'Inspect approved plan' : d.canApprovePlan ? 'Review submitted plan' : 'Request plan review';
    output = d.isPlanApproved ? `Plan revision ${state.plan.revision} approved in the local walkthrough.` : null;
    if (!independent && status !== 'complete') {
      status = 'blocked';
      reason = {action: 'review_plan', code: 'independent_reviewer', reason: 'An independent reviewer is needed. The plan owner cannot review their own work.'};
    }
  } else if (item.kind === 'collection') {
    events = ['approve_plan', 'obtain_document'];
    status = state.document ? 'complete' : d.canObtainDocument ? 'ready' : state.task ? 'blocked' : 'planned';
    label = state.document ? 'Inspect received document' : 'Add current document';
    output = state.document ? `Received document ${state.document.version}. Inspection is separate.` : null;
    if (!state.document && !d.isPlanApproved) reason = {action: 'obtain_document', code: 'plan_approval', reason: 'The current plan revision needs internal approval before collection can start.'};
  } else if (item.kind === 'inspection') {
    events = ['obtain_document', 'verify_document', 'save_plan'];
    status = d.isDocumentVerified ? 'complete' : d.canVerifyDocument ? 'ready' : 'blocked';
    label = d.isDocumentVerified ? 'Inspect verification record' : 'Verify document version';
    output = d.isDocumentVerified ? `Inspection recorded for ${state.document.version} in the existing walkthrough. A separate current-version basis is not recorded.` : null;
    if (status === 'blocked') reason = {action: 'inspect_document', code: state.document ? 'plan_approval' : 'document_receipt', reason: state.document ? 'The current plan revision needs internal approval.' : 'Receive the document before inspecting its version.'};
  } else if (item.kind === 'customer_review') {
    events = ['request_customer_review', 'record_customer_approval', 'obtain_document', 'save_plan', 'reopen_requirement'];
    const decision = state.customerReview?.status === 'approved' && state.customerReview.documentId === state.document?.id && state.customerReview.documentVersion === state.document?.version;
    status = decision ? 'complete' : d.canRecordCustomerApproval ? 'waiting' : d.canRequestCustomerReview ? 'ready' : 'blocked';
    owner = decision || status === 'waiting' ? reviewer : planOwner;
    waitingOn = status === 'waiting' ? item.awaitedParty : null;
    label = decision ? 'Inspect recorded decision' : status === 'waiting' ? 'Record supported customer decision' : 'Request counsel review';
    output = decision ? `Customer decision recorded for ${state.customerReview.documentVersion} in the local walkthrough. This does not resolve the requirement.` : null;
    if (status === 'blocked') reason = {action: 'request_customer_review', code: 'inspection', reason: 'Approve the work plan and inspect the received version before requesting customer review.'};
    if (status === 'waiting') reason = {action: 'complete_review', code: 'customer_decision', reason: 'The customer decision is awaited. Recording it requires supporting evidence; the action owner is not the customer approver.'};
  }
  if (!owner && status !== 'complete') {
    status = 'blocked';
    reason = {action: 'execute', code: 'owner_missing', reason: 'An action owner has not been recorded. Inspect the work before assigning responsibility.'};
  }
  const allowedActions = ['independent_reviewer', 'owner_missing'].includes(reason?.code) ? [] : [stepAction(item, ['blocked', 'planned'].includes(status) ? 'Inspect walkthrough step' : label, ['ready', 'waiting'].includes(status) ? 'continue' : 'inspect')];
  return {
    ...item, title: item.kind === 'collection' ? state.task?.title || state.plan.title || item.title : item.title, ...ownerFields(owner), status, waitingOn, output,
    revision: revisionFor(state, events, item.revision),
    lastChangedAt: lastEvent(state, events)?.at || item.lastChangedAt,
    allowedActions, blockedReasons: reason ? [reason] : [],
    nextActionLabel: ['ready', 'waiting', 'complete'].includes(status) && allowedActions.length ? label : 'Inspect work item',
  };
}

// Local adapter only. These response shapes are not an authorization boundary.
export function buildWorklist(state, fixture = worklistFixture) {
  const d = derive(state);
  const knownTimes = [timestamp(fixture.asOf), ...state.history.map(event => timestamp(event.at))].filter(value => value !== null);
  const asOf = new Date(Math.max(...knownTimes)).toISOString();
  const requirements = fixture.requirements.map(item => {
    const interactive = item.renewalId === renewalFixture.id;
    const status = interactive ? state.requirement.status : item.status;
    const allowedActions = interactive ? [{id: 'open_requirement', label: status === 'resolved' ? 'Inspect resolution' : d.canResolveRequirement ? 'Review requirement resolution' : 'Inspect resolution criteria', intent: 'inspect', href: `${renewalFixture.paths.workplan}?${new URLSearchParams({requirement: item.id})}`} ] : [];
    return {
      ...item, status, kindLabel: requirementKinds[item.kind], selectionKey: keyFor('requirement', item.id),
      ...ownerFields(person(item.ownerId)),
      revision: interactive ? revisionFor(state, ['resolve_requirement', 'reopen_requirement', 'save_plan', 'obtain_document'], item.revision) : item.revision,
      allowedActions, blockedReasons: interactive ? [] : [illustrativeBlock],
    };
  });
  const workItems = fixture.workItems.map(item => {
    const renewal = fixture.renewals.find(record => record.id === item.renewalId);
    const current = renewal.mode === 'walkthrough' ? walkthroughWork(item, state, d) : {
      ...item, ...ownerFields(person(item.ownerId)), allowedActions: [], output: null,
      blockedReasons: [{action: 'complete', code: 'snapshot_prerequisite', reason: item.blockedReason}, illustrativeBlock], nextActionLabel: 'Inspect snapshot',
    };
    const dueValue = timestamp(current.dueAt);
    return {
      ...current, ...changeFields(current.lastChangedAt, asOf),
      selectionKey: keyFor('work-item', item.id), mode: renewal.mode, modeLabel: modeLabels[renewal.mode],
      statusLabel: statuses[current.status], canAct: current.allowedActions.some(action => action.intent === 'continue'),
      dueValue, dueLabel: formatWorklistTime(current.dueAt), overdue: current.status !== 'complete' && dueValue !== null && dueValue < timestamp(asOf),
      dueSoon: current.status !== 'complete' && dueValue !== null && dueValue <= timestamp(asOf) + 7 * day,
      waitingLabel: current.waitingOn?.label || 'No party currently awaited',
      blockedLabel: current.blockedReasons.find(reason => reason.code !== 'illustrative_only')?.reason || 'No prerequisite open',
    };
  });
  const renewals = fixture.renewals.map(item => {
    const interactive = item.mode === 'walkthrough';
    const linkedRequirements = item.requirementsRecorded ? requirements.filter(record => record.renewalId === item.id) : null;
    const open = linkedRequirements?.filter(record => record.status !== 'resolved').sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id));
    const work = workItems.filter(record => record.renewalId === item.id);
    const next = work.find(record => record.status !== 'complete');
    const nextStep = next ? {selectionKey: next.selectionKey, label: next.nextActionLabel, title: next.title}
      : open?.length ? {selectionKey: open[0].selectionKey, label: 'Review requirement resolution', title: open[0].title}
      : {selectionKey: keyFor('renewal', item.id), label: 'Inspect readiness', title: 'Renewal readiness'};
    const daysLeft = daysUntil(item.renewalDate, asOf);
    return {
      ...item, ...ownerFields(person(item.ownerId)), ...changeFields(interactive ? lastEvent(state)?.at || item.lastChangedAt : item.lastChangedAt, asOf),
      revision: interactive ? String(Number(item.revision) + state.history.length) : item.revision,
      selectionKey: keyFor('renewal', item.id), title: item.accountName, modeLabel: modeLabels[item.mode],
      renewalDateLabel: formatWorklistTime(item.renewalDate, true), daysLeft, urgentDate: daysLeft !== null && daysLeft <= 14,
      daysLabel: daysLeft === null ? 'Days left unknown' : daysLeft < 0 ? `${Math.abs(daysLeft)} days overdue` : daysLeft === 0 ? 'Due today' : `${daysLeft} days left`,
      crm: {...item.crm, stageLabel: stages[item.crm.stage] || 'Unknown', readLabel: formatWorklistTime(item.crm.readAt)},
      commercial: interactive ? {...state.commercial, label: state.commercial.status === 'not_confirmed' ? 'Not confirmed' : state.commercial.label} : item.commercial,
      requirements: linkedRequirements, openRequirementCount: open?.length ?? null,
      openBlockerCount: open ? open.filter(record => record.blocking).length : null,
      urgentRequirement: open?.[0] || null, workItems: work, nextStep,
      blockerLabel: !open ? 'Blockers unknown' : `${open.filter(record => record.blocking).length} blocking`,
      readinessLabel: !open ? 'Readiness not recorded' : open.length ? `${open.length} open ${open.length === 1 ? 'requirement' : 'requirements'}` : 'No open requirements',
      allowedActions: interactive ? [{id: 'open_readiness', label: 'Open walkthrough readiness', intent: 'inspect', href: `${renewalFixture.paths.root}/readiness`}] : [],
      blockedReasons: interactive ? [] : [illustrativeBlock],
    };
  });
  const rows = workItems.map(item => {
    const renewal = renewals.find(record => record.id === item.renewalId);
    return {...item, accountName: renewal.accountName, crm: renewal.crm, daysLeft: renewal.daysLeft, daysLabel: renewal.daysLabel, openBlockerCount: renewal.openBlockerCount, renewalSelectionKey: renewal.selectionKey};
  });
  return {revision: String(Number(fixture.revision) + state.history.length), asOf, asOfLabel: formatWorklistTime(asOf), renewals, requirements, workItems: rows, openWorkItemCount: rows.filter(row => row.status !== 'complete').length};
}

export const worklistSorts = [
  {id: 'days', label: 'Renewal days left', initialDirection: 'asc'},
  {id: 'blockers', label: 'Open blockers', initialDirection: 'desc'},
  {id: 'changed', label: 'Last change', initialDirection: 'desc'},
  {id: 'due', label: 'Work due date', initialDirection: 'asc', view: 'queue'},
];
export const worklistFacets = {
  renewals: [
    {id: 'blockers', label: 'Open blockers', matches: row => row.openBlockerCount > 0},
    {id: 'due-soon', label: 'Due within 30 days', matches: row => row.daysLeft !== null && row.daysLeft <= 30},
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
  const sort = allowedSorts.find(item => item.id === params.get('sort')) || allowedSorts[0];
  const response = worklistResponseExamples.find(item => item.id === params.get('response')) || worklistResponseExamples[0];
  return {
    q: params.get('q') || '', owner: params.get('owner') || '', stage: params.get('stage') || '',
    attention: [...new Set(params.getAll('attention'))].filter(id => worklistFacets[view].some(facet => facet.id === id)),
    sort: sort.id, direction: ['asc', 'desc'].includes(params.get('direction')) ? params.get('direction') : sort.initialDirection,
    status: view === 'queue' && ['all', 'complete'].includes(params.get('status')) ? params.get('status') : 'open',
    selection: params.get('selection') || '', response: response.id, expanded: params.get('limit') === 'all',
    invalid: Boolean((params.has('sort') && !allowedSorts.some(item => item.id === params.get('sort'))) || (params.has('response') && response.id !== params.get('response')) || params.getAll('attention').some(id => !worklistFacets[view].some(facet => facet.id === id))),
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

export function worklistHref(view, search, changes = {}) {
  const params = updateWorklistSearch(search, changes);
  return `/${view}${params.size ? `?${params}` : ''}`;
}

function compareNullable(a, b, direction) {
  if (a === null || a === undefined) return b === null || b === undefined ? 0 : 1;
  if (b === null || b === undefined) return -1;
  return (a - b) * (direction === 'desc' ? -1 : 1);
}

export function selectWorklist(data, view = 'renewals', search = '') {
  const location = readWorklistLocation(search, view);
  const response = worklistResponseExamples.find(example => example.id === location.response);
  const unit = view === 'renewals' ? 'renewals' : 'work items';
  const base = {status: response.status, response, location, unit, revision: data.revision, examples: worklistResponseExamples, sorts: worklistSorts.filter(sort => !sort.view || sort.view === view)};
  if (response.status !== 'ready') return {...base, rows: null, facets: null, total: null, selection: null};
  const all = view === 'renewals' ? data.renewals : data.workItems;
  const ownerOptions = worklistPeople.filter(owner => all.some(row => row.ownerId === owner.id));
  const stageOptions = [...new Set(all.map(row => row.crm.stage))].map(stage => ({id: stage || 'unknown', label: stages[stage] || 'Unknown'}));
  const scope = all.filter(row => view !== 'queue' || location.status === 'all' || (location.status === 'complete' ? row.status === 'complete' : row.status !== 'complete'));
  const searched = scope.filter(row => {
    const matchesQuery = `${row.accountName} ${row.title || ''} ${row.id}`.toLowerCase().includes(location.q.trim().toLowerCase());
    return matchesQuery && (!location.owner || (location.owner === 'unassigned' ? !row.ownerId : row.ownerId === location.owner))
      && (!location.stage || (location.stage === 'unknown' ? !row.crm.stage : row.crm.stage === location.stage));
  });
  const facets = worklistFacets[view].map(facet => ({id: facet.id, label: facet.label, count: searched.filter(facet.matches).length, unit}));
  const matched = searched.filter(row => !location.attention.length || worklistFacets[view].some(facet => location.attention.includes(facet.id) && facet.matches(row)));
  const sortField = {days: 'daysLeft', blockers: 'openBlockerCount', changed: 'lastChangedValue', due: 'dueValue'}[location.sort];
  const sorted = [...matched].sort((a, b) => compareNullable(a[sortField], b[sortField], location.direction) || a.id.localeCompare(b.id));
  const rows = response.limit && !location.expanded ? sorted.slice(0, response.limit) : sorted;
  const selection = resolveWorklistSelection(data, location.selection);
  if (selection?.status === 'ready') {
    selection.visible = rows.some(row => row.selectionKey === selection.resource.selectionKey || (view === 'renewals' && row.id === selection.renewal.id));
  }
  return {
    ...base, asOf: data.asOf, asOfLabel: data.asOfLabel, rows, total: sorted.length, available: all.length, scopeCount: scope.length,
    facetScopeCount: searched.length, facets, ownerOptions, stageOptions, selection,
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
  const [path, query = ''] = action.href.split('?');
  const params = new URLSearchParams(query);
  params.set('returnTo', returnTo);
  return `${path}?${params}`;
}

export function resolveWorklistEntry(data, pathname, search) {
  const params = new URLSearchParams(search);
  const id = params.get('workItem'), requirementId = params.get('requirement');
  if (pathname !== renewalFixture.paths.workplan || (!id && !requirementId)) return null;
  const resource = id ? data.workItems.find(item => item.id === id && item.mode === 'walkthrough') : data.requirements.find(item => item.id === requirementId && item.renewalId === renewalFixture.id);
  const returnTo = safeWorklistReturn(params.get('returnTo'));
  return resource ? {status: 'ready', id: resource.id, title: resource.title, step: id ? resource.step : 4, returnTo} : {status: 'not_found', returnTo};
}

export function safeWorklistReturn(value) {
  return typeof value === 'string' && /^\/(renewals|queue)(\?[^#]*)?$/.test(value) ? value : '/queue';
}
