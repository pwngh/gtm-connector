import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';

export const draftFields = Object.freeze({
  title: {label: 'Draft title', maxLength: 160},
  specifications: {label: 'Specifications', maxLength: 1200},
  instructions: {label: 'Instructions', maxLength: 1200},
});
const clean = value => typeof value === 'string' ? value.trim() : '';
const clone = value => structuredClone(value);
const pick = (value, keys) => Object.fromEntries(keys.map(key => [key, value?.[key] ?? null]));

// Read-only adapter. It deliberately excludes UI icons, credentials, and live integrations.
// These are the existing Thornfield identities, not new or independently inspected records.
export function buildPlaybookContext(workflow, evidence = [], roles = {}) {
  return {
    schemaVersion: 1,
    synthetic: true,
    id: 'thornfield-a17',
    accountName: 'Thornfield Wren LLP',
    refs: {accountId: 'thornfield', renewalId: '003', agreementId: 'A-17', requirementId: workflow.requirement.id},
    requirement: pick(workflow.requirement, ['id', 'title', 'status']),
    existingPlan: pick(workflow.plan, ['id', 'revision', 'status', 'approvedRevision', 'owner', 'specifications', 'instructions']),
    document: workflow.document ? pick(workflow.document, ['id', 'title', 'name', 'version', 'currentVersion', 'source', 'status', 'receivedAt', 'verifiedAt', 'verifiedBy']) : null,
    customerReview: workflow.customerReview ? pick(workflow.customerReview, ['status', 'planRevision', 'documentId', 'documentVersion', 'reviewedBy', 'recordedBy', 'evidenceText']) : null,
    commercialStatus: workflow.commercial.status,
    evidence: evidence.filter(record => ['E-017', 'E-042'].includes(record.id)).map(record => pick(record,
      ['id', 'type', 'speaker', 'role', 'time', 'quote', 'context', 'meaning', 'detail', 'agreement'])),
    roles: {
      actionOwner: clean(Object.hasOwn(roles, 'actionOwner') ? roles.actionOwner : workflow.plan.owner) || null,
      internalReviewer: 'Alex Chen',
      customerAuthority: clean(Object.hasOwn(roles, 'customerAuthority') ? roles.customerAuthority : 'Nina Patel') || null,
      authorityBasis: 'Synthetic counsel role only; not authenticated decision authority.',
    },
  };
}

export function describeDocument(document) {
  if (!document) return {state: 'missing', label: 'Not received', detail: 'The applicable file and its version are unknown.'};
  if (!document.id || !document.version || !document.currentVersion) return {
    state: 'unknown', label: 'Version or identity unknown', detail: 'Establish the file identity and applicable current version before verification.',
  };
  if (document.version !== document.currentVersion) return {
    state: 'stale', label: 'Version mismatch', detail: `Received ${document.version}; the walkthrough identifies ${document.currentVersion} as current. Obtain the applicable file and verify it.`,
  };
  if (document.status === 'verified') return {
    state: 'verified', label: 'Verified in the walkthrough', detail: `Document ${document.version} has a recorded version check. That is not customer approval.`,
  };
  return {state: 'received', label: 'Received, not verified', detail: `Document ${document.version} is present. Receipt does not establish applicability or approval.`};
}

export function buildTemplateWork(templateId, context) {
  const template = getPlaybookTemplate(templateId);
  if (!template) throw new Error('Unknown playbook template.');
  const tasks = clone(template.tasks);
  const document = describeDocument(context.document);
  const ownerMissing = !context.roles.actionOwner;
  const before = [];
  if (ownerMissing) before.push({
    id: 'assign_owner', title: 'Identify an accountable action owner', role: 'internalReviewer', dependsOn: [],
    inputs: ['Agreement A-17', 'The proposed work scope'], output: 'A named action owner and explicit responsibility for this work.',
    completionRule: 'Name an accountable person before releasing operational work. Do not assign the customer’s authority to the internal owner.',
  });
  before.push({
    id: 'authorize_scope', title: 'Review and authorize this separate work scope', role: 'internalReviewer',
    dependsOn: ownerMissing ? ['assign_owner'] : [],
    inputs: ['Confirmed synthetic context', 'This draft revision, specifications, and instructions'],
    output: 'A separately reviewed internal work scope; no approval is recorded here.',
    completionRule: 'An authorized internal reviewer would approve this exact revision before work is released. The existing plan’s approval cannot be inherited.',
  });
  if (templateId === 'obtain_current_rider' && context.document) {
    tasks.find(task => task.id === 'locate_document').title = document.state === 'stale'
      ? 'Obtain a replacement for the stale document' : 'Confirm the received document and its source';
    if (document.state === 'verified') tasks.find(task => task.id === 'verify_document').title = 'Recheck the recorded version verification';
  }
  const authority = tasks.find(task => task.id === 'confirm_authority');
  if (!context.roles.customerAuthority) authority.title = 'Identify the customer’s decision authority';
  authority.inputs.push(context.roles.customerAuthority
    ? `${context.roles.customerAuthority}: fixture counsel, authority still needs a basis`
    : 'Customer authority is not established; ask the customer who can decide.');

  const sourceNeed = (id, sourceId, label, recoveryTaskId) => {
    const record = context.evidence.find(item => item.id === sourceId && clean(item.quote) && clean(item.context));
    return {id, label, ref: sourceId, state: record ? 'available' : 'missing', status: record ? 'Source available' : 'Source missing',
      detail: record ? `${record.speaker} · ${record.type}. Preserve the statement in context.` : 'Obtain the original statement and its context; do not invent its contents.', recoveryTaskId};
  };
  const allNeeds = {
    customer_statement: sourceNeed('customer_statement', 'E-017', 'Exact customer condition', template.recoveryTasks.customer_statement),
    comparison_statement: sourceNeed('comparison_statement', 'E-042', 'Statement to compare', template.recoveryTasks.comparison_statement),
    current_document: {id: 'current_document', label: 'Applicable AI terms document', ref: context.document?.id || null,
      state: document.state, status: document.label, detail: document.detail, recoveryTaskId: template.recoveryTasks.current_document},
    customer_authority: {id: 'customer_authority', label: 'Customer decision authority', ref: null,
      state: 'unknown', status: context.roles.customerAuthority ? 'Fixture role; authority unconfirmed' : 'Not established',
      detail: context.roles.customerAuthority ? `${context.roles.customerAuthority} is named as counsel in the demo. Confirm their remit before relying on a decision.` : 'Identify who can decide for this customer. A missing name does not prevent planning that inquiry.',
      recoveryTaskId: 'confirm_authority'},
  };
  return {
    tasks: [...before, ...tasks].map(task => ({...task, status: 'proposed'})),
    evidenceNeeds: template.requiredEvidence.map(id => allNeeds[id]),
    prerequisites: [
      {id: 'owner', label: 'Action responsibility', status: ownerMissing ? 'Needs assignment' : 'Proposed owner named',
        detail: ownerMissing ? 'Assign an action owner before releasing work.' : `${context.roles.actionOwner} is the proposed action owner, not the customer decision authority.`,
        recoveryTaskId: ownerMissing ? 'assign_owner' : 'authorize_scope'},
      {id: 'internal_authorization', label: 'Internal work authorization', status: 'Not requested for this draft',
        detail: 'A new draft needs its own scope review. Missing evidence may be acquired under that bounded scope; it is not required just to prepare the draft.', recoveryTaskId: 'authorize_scope'},
    ],
  };
}

export function contextRecordKey(context) {
  // Role choices are draft-local. Freshness compares the actual source records instead.
  const {roles, ...records} = context;
  return JSON.stringify(records);
}

export function isDraftContextCurrent(draft, context) {
  return Boolean(draft && contextRecordKey(draft.contextSnapshot) === contextRecordKey(context));
}

export function preparePlaybookDraft({templateId, context, confirmed, id, at}) {
  const template = getPlaybookTemplate(templateId);
  if (!template) return {ok: false, error: 'Choose one of the three supported templates.'};
  if (confirmed !== true) return {ok: false, error: 'Confirm the synthetic context before preparing a draft.'};
  if (context?.synthetic !== true || context.id !== 'thornfield-a17'
    || context.refs?.accountId !== 'thornfield' || context.refs?.renewalId !== '003'
    || context.refs?.agreementId !== 'A-17' || context.refs?.requirementId !== 'req-ai-counsel'
    || !context.roles || !Array.isArray(context.evidence)) {
    return {ok: false, error: 'This local workbench supports only the invented Thornfield / A-17 context.'};
  }
  if (!clean(id) || !clean(at)) return {ok: false, error: 'A local draft identity and preparation time are required.'};
  const snapshot = clone(context);
  return {ok: true, draft: {
    schemaVersion: 1, id, simulation: true, status: 'draft', revision: 1, createdAt: at,
    templateRef: {id: template.id, version: template.version},
    contextSnapshot: snapshot, contextConfirmedAt: at,
    title: template.draftTitle, specifications: template.specifications, instructions: template.instructions,
    ...buildTemplateWork(templateId, snapshot),
    output: clone(template.output), completionRules: clone(template.completionRules), reviewBoundary: template.reviewBoundary,
    humanEdits: [],
  }};
}

export function draftEditorValues(draft) {
  return Object.fromEntries(Object.keys(draftFields).map(field => [field, draft[field]]));
}

export function validateDraftEdits(values) {
  const errors = {};
  for (const [field, {label, maxLength}] of Object.entries(draftFields)) {
    if (typeof values?.[field] !== 'string' || !values[field].trim()) errors[field] = `${label} is required.`;
    else if (values[field].trim().length > maxLength) errors[field] = `${label} must be ${maxLength} characters or fewer.`;
  }
  if (values && Object.keys(values).some(field => !Object.hasOwn(draftFields, field))) errors.scope = 'Only the title, specifications, and instructions are editable. Completion rules and authority gates stay intact.';
  return errors;
}

export function changedDraftFields(draft, values) {
  return Object.keys(draftFields).filter(field => clean(values?.[field]) !== draft[field]);
}

export function savePlaybookDraft(draft, values, at) {
  if (!draft || draft.status !== 'draft') return {ok: false, error: 'Only a separate, unapproved Playbooks draft can be edited.'};
  const errors = validateDraftEdits(values);
  if (Object.keys(errors).length) return {ok: false, error: 'Complete the required draft fields.', fieldErrors: errors};
  const fields = changedDraftFields(draft, values);
  if (!fields.length) return {ok: true, draft};
  if (!clean(at)) return {ok: false, error: 'A local edit time is required.'};
  const normalized = Object.fromEntries(Object.keys(draftFields).map(field => [field, values[field].trim()]));
  const revision = draft.revision + 1;
  return {ok: true, draft: {
    ...draft, ...normalized, revision,
    humanEdits: [...draft.humanEdits, {
      revision, at, actor: 'Local user; not authenticated',
      changes: fields.map(field => ({field, before: draft[field], after: normalized[field]})),
    }],
  }};
}

// App owns this memory-only session, outside the existing workflow/localStorage reducer.
// IDs include a per-mount nonce supplied by the UI so old URLs cannot rebind after reload.
export function createPlaybooksSession(sessionId) {
  if (!clean(sessionId)) throw new Error('A per-mount session identity is required.');
  return {schemaVersion: 1, sessionId, nextDraftNumber: 1, draft: null, editor: null, error: null, fieldErrors: {}, notice: ''};
}

export function playbooksSessionReducer(session, action) {
  switch (action.type) {
    case 'prepare': {
      if (session.draft) return {...session, error: 'Resume or discard the current draft before preparing another.'};
      const result = preparePlaybookDraft({...action, id: `pb-${session.sessionId}-${session.nextDraftNumber}`});
      if (!result.ok) return {...session, error: result.error};
      return {...session, draft: result.draft, editor: draftEditorValues(result.draft), nextDraftNumber: session.nextDraftNumber + 1,
        error: null, fieldErrors: {}, notice: 'Separate draft prepared in memory. Existing work is unchanged.'};
    }
    case 'edit_field': {
      if (!session.draft || !Object.hasOwn(draftFields, action.field) || typeof action.value !== 'string') return session;
      return {...session, editor: {...session.editor, [action.field]: action.value}, error: null,
        fieldErrors: {...session.fieldErrors, [action.field]: undefined}, notice: ''};
    }
    case 'save_edits': {
      const result = savePlaybookDraft(session.draft, session.editor, action.at);
      if (!result.ok) return {...session, error: result.error, fieldErrors: result.fieldErrors || {}};
      return {...session, draft: result.draft, editor: draftEditorValues(result.draft), error: null, fieldErrors: {},
        notice: 'Read-only draft inspection. Nothing is submitted, approved, sent, or persisted.'};
    }
    case 'revert_edits':
      return session.draft ? {...session, editor: draftEditorValues(session.draft), error: null, fieldErrors: {}, notice: 'Unapplied edits discarded. The prepared draft is retained.'} : session;
    case 'discard_confirmed':
      return {...session, draft: null, editor: null, error: null, fieldErrors: {}, notice: 'Draft discarded. Reusable templates and existing work are unchanged.'};
    case 'clear_error':
      return {...session, error: null, fieldErrors: {}};
    default:
      return session; // No approve, execute, send, resolve, or workflow-replacement commands.
  }
}

export function playbookSearch(templateId, view = 'template', draftId = null) {
  const params = new URLSearchParams({template: templateId});
  if (view !== 'template') params.set('view', view);
  if (['edit', 'inspect'].includes(view) && draftId) params.set('draft', draftId);
  return params;
}

export function readPlaybookLocation(search, session) {
  const params = new URLSearchParams(search);
  const requested = params.get('template');
  const templateId = getPlaybookTemplate(requested)?.id || playbookTemplates[0].id;
  const requestedView = params.get('view') || 'template';
  const view = ['template', 'context', 'edit', 'inspect'].includes(requestedView) ? requestedView : 'template';
  const needsDraft = ['edit', 'inspect'].includes(view);
  const draftMatches = Boolean(getPlaybookTemplate(requested)) && session.draft?.templateRef.id === requested && session.draft?.id === params.get('draft');
  return {
    templateId,
    view: needsDraft && !draftMatches ? 'unavailable' : view,
    invalidSelection: Boolean(requested && requested !== templateId) || view !== requestedView,
  };
}
