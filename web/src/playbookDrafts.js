import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';
import {renewalFixture} from './renewalFixtures.js';

export const draftFields = Object.freeze({
  title: {label: 'Draft title', maxLength: 160},
  specifications: {label: 'Specifications', maxLength: 1200},
  instructions: {label: 'Instructions', maxLength: 1200},
});
export const draftChangeLabels = {...Object.fromEntries(Object.entries(draftFields).map(([key, value]) => [key, value.label])), removedItemIds: 'Auto-added items removed'};
const clean = value => typeof value === 'string' ? value.trim() : '';
const clone = value => structuredClone(value);
const pick = (value, keys) => Object.fromEntries(keys.map(key => [key, value?.[key] ?? null]));
const eligible = (name, options) => options.includes(clean(name)) ? clean(name) : null;
const reviewEvents = ['request_review', 'request_changes', 'approve_plan'];

// Read-only adapter. Role choices never grant authority.
export function buildPlaybookContext(workflow, evidence = [], roles = {}) {
  const fixture = renewalFixture;
  return {
    schemaVersion: 2, synthetic: true, id: fixture.id, accountName: fixture.account.name,
    refs: {accountId: fixture.account.id, renewalId: fixture.renewal.id, agreementId: fixture.agreement.id, requirementId: workflow.requirement.id},
    agreement: clone(fixture.agreement), paths: clone(fixture.paths),
    bindings: {...fixture.bindings, current_document: workflow.document?.id || null, current_version_basis: workflow.documentVersionBasis?.id || null},
    requirement: pick(workflow.requirement, ['id', 'title', 'status', 'resolvedAt']),
    existingPlan: pick(workflow.plan, ['id', 'title', 'revision', 'status', 'approvedRevision', 'owner', 'specifications', 'instructions', 'reviewNote', 'requestedAt', 'reviewedAt', 'approvedAt', 'approver']),
    planReviews: (workflow.history || []).filter(event => reviewEvents.includes(event.type)).map(event => pick(event, ['id', 'type', 'at', 'title', 'detail', 'actor', 'revision'])),
    document: workflow.document ? pick(workflow.document, ['id', 'title', 'name', 'version', 'source', 'status', 'receivedAt', 'verifiedAt', 'verifiedBy']) : null,
    documentVersionBasis: clone(workflow.documentVersionBasis ?? fixture.documentVersionBasis),
    customerReview: workflow.customerReview ? pick(workflow.customerReview, ['status', 'planRevision', 'documentId', 'documentVersion', 'reviewedBy', 'recordedBy', 'evidenceText', 'requestedAt', 'requestedBy', 'approvedAt']) : null,
    commercialStatus: workflow.commercial.status,
    evidence: evidence.filter(record => Object.values(fixture.bindings).includes(record.id)).map(record => pick(record,
      ['id', 'binding', 'type', 'speaker', 'role', 'time', 'quote', 'context', 'meaning', 'detail', 'agreement'])),
    roleOptions: clone(fixture.roles),
    roles: {
      actionOwner: eligible(Object.hasOwn(roles, 'actionOwner') ? roles.actionOwner : workflow.plan.owner, fixture.roles.actionOwners),
      internalReviewer: eligible(roles.internalReviewer, fixture.roles.internalReviewers),
      customerAuthority: eligible(roles.customerAuthority, fixture.roles.customerAuthorityOptions),
      authorityBasis: null,
    },
  };
}

export function hasIndependentVersionBasis(document, basis) {
  return Boolean(document?.id && basis?.id && basis.id !== document.id && basis.documentId === document.id
    && clean(basis.version) && ['unit', 'receipt'].includes(basis.source?.kind)
    && clean(basis.source?.ref) && basis.source.ref !== document.id && basis.source.documentId !== document.id);
}

export function describeDocument(document, basis = null) {
  const supported = hasIndependentVersionBasis(document, basis);
  const currentVersion = supported ? basis.version : null;
  const versionSource = supported ? basis.source.ref : null;
  const describe = (state, label, detail) => ({state, label, detail, currentVersion, versionSource});
  if (!document) return describe('missing', 'Not received', 'The applicable file and its version are unknown.');
  if (!document.id || !document.version) return describe('unknown', 'Version or identity unknown', 'Establish file identity and a separately sourced current version before inspection.');
  if (!supported) return describe('unknown', 'Current version: basis not recorded', `Received ${document.version}. No separate source establishes which version is current. A document's own version claim or legacy verification status is not that basis.`);
  if (document.version !== basis.version) return describe('stale', 'Version mismatch', `Received ${document.version}; separate source ${basis.source.ref} identifies ${basis.version} as current. Locate the applicable file on our side and inspect it.`);
  if (document.status === 'verified') return describe('verified', 'Inspected with a separate version basis', `Document ${document.version} has a recorded walkthrough inspection and version source ${basis.source.ref}. Neither is customer approval.`);
  return describe('received', 'Received, not inspected', `Document ${document.version} is present. Receipt does not establish applicability or approval.`);
}

export function getContextSource(context, binding) {
  return context.evidence.find(source => source.id === context.bindings[binding]);
}

const boundRef = (ref, context) => context.bindings[ref] || ref;
const bindInput = (input, context) => ({...input, ref: boundRef(input.ref, context)});
function bindProof(proof, context) {
  return {...proof, ref: boundRef(proof.ref, context),
    ...(proof.versionBasisRef ? {versionBasisRef: boundRef(proof.versionBasisRef, context)} : {}),
    ...(proof.alsoRefs ? {alsoRefs: proof.alsoRefs.map(ref => boundRef(ref, context))} : {}),
    ...(proof.cites ? {cites: proof.cites.map(ref => boundRef(ref, context))} : {}),
  };
}

export function buildTemplateWork(templateId, context, removedItemIds = []) {
  const template = getPlaybookTemplate(templateId);
  if (!template) throw new Error('Unknown playbook template.');
  const document = describeDocument(context.document, context.documentVersionBasis);
  const ownerMissing = !context.roles.actionOwner;
  const additions = template.automaticItems.filter(item => item.when !== 'owner_missing' || ownerMissing);
  const includedAdditions = additions.filter(item => !removedItemIds.includes(item.id));
  const omittedIds = template.automaticItems.filter(item => !includedAdditions.some(included => included.id === item.id)).map(item => item.id);
  const items = clone([...includedAdditions, ...template.items]).map(item => ({
    ...item, status: 'proposed', inputs: item.inputs.map(input => bindInput(input, context)),
    completionProof: bindProof(item.completionProof, context),
    dependencies: {
      all: item.dependencies.all.filter(id => !omittedIds.includes(id)),
      oneOf: item.dependencies.oneOf.map(group => group.filter(id => !omittedIds.includes(id))),
      excludes: item.dependencies.excludes.filter(id => !omittedIds.includes(id)),
    },
  }));
  if (templateId === 'obtain_current_rider' && context.document) {
    items.find(item => item.id === 'locate_document').title = document.state === 'stale'
      ? 'Locate a replacement on our side' : 'Locate and retain the applicable file from our records';
  }
  const authorityItem = items.find(item => item.id === 'confirm_authority');
  authorityItem.inputs.push({ref: 'customer_authority', mode: 'allowed', label: context.roles.customerAuthority
    ? `${context.roles.customerAuthority}: deliberately selected walkthrough fixture; authority basis not recorded`
    : 'Unidentified decision owner. Record who decides, with evidence.'});

  const sourceNeed = (id, label) => {
    const source = getContextSource(context, id);
    const available = Boolean(clean(source?.quote) && clean(source?.context));
    return {id, label, ref: context.bindings[id], state: available ? 'available' : 'missing', status: available ? 'Source available' : 'Source missing',
      detail: available ? `${source.speaker} · ${source.type}. Preserve the statement in context.` : 'Recover the original statement and its source context. Do not invent its contents.'};
  };
  const allNeeds = {
    customer_statement: sourceNeed('customer_statement', 'Customer condition statement'),
    comparison_statement: sourceNeed('comparison_statement', 'Comparison statement'),
    current_document: {id: 'current_document', label: 'Applicable AI terms document', ref: context.document?.id || null,
      state: document.state, status: document.label, detail: document.detail},
    current_version_basis: {id: 'current_version_basis', label: 'Separate current-version basis', ref: context.documentVersionBasis?.id || null,
      state: document.currentVersion ? 'available' : 'missing', status: document.currentVersion ? `Sourced version: ${document.currentVersion}` : 'Basis not recorded',
      detail: document.versionSource ? `Version source: ${document.versionSource}. Inspect its applicability to the agreement.` : 'Locate a separate source stating which version is current. The file cannot establish this by itself.'},
    customer_authority: {id: 'customer_authority', label: 'Customer decision authority', ref: null, state: 'unknown',
      status: context.roles.customerAuthority ? 'Walkthrough fixture selected; basis not recorded' : 'Unidentified',
      detail: 'Record who decides, with evidence. A typed name, contact role, or internal review does not establish customer authority.'},
  };
  const recovery = id => items.some(item => item.id === id) ? id : null;
  return {
    inputs: template.inputs.map(input => bindInput(input, context)),
    requiredEvidence: template.requiredEvidence.map(ref => boundRef(ref, context)),
    items,
    evidenceNeeds: template.evidence.map(id => ({...allNeeds[id], mode: template.requiredEvidence.includes(id) ? 'required' : 'allowed',
      output: template.output.evidence.includes(id), recoveryTaskId: template.recoveryTasks[id]})),
    prerequisites: [
      {id: 'owner', label: 'Action responsibility', status: ownerMissing ? 'Needs assignment' : 'Proposed owner named',
        detail: ownerMissing ? 'Assign an action owner before releasing work. Removing its proposed item does not assign an owner.' : `${context.roles.actionOwner} is the proposed owner, not the customer decision authority.`,
        recoveryTaskId: recovery(ownerMissing ? 'assign_owner' : 'authorize_scope')},
      {id: 'reviewer', label: 'Independent internal reviewer', status: context.roles.internalReviewer ? 'Walkthrough reviewer selected' : 'Unassigned',
        detail: 'A future review needs an independent reviewer. This workbench records no review or authorization.', recoveryTaskId: recovery('authorize_scope')},
      {id: 'internal_authorization', label: 'Internal work authorization', status: recovery('authorize_scope') ? 'Not requested for this draft' : 'Scope-review item removed; no authorization',
        detail: 'Missing evidence can be acquired under a separately reviewed scope. Keeping or removing a proposed review item never authorizes work. Preserving source statements does not wait for that review.', recoveryTaskId: recovery('authorize_scope')},
    ],
  };
}

export function draftResponsibilityError(context) {
  const owner = clean(context?.roles?.actionOwner).toLowerCase();
  const reviewer = clean(context?.roles?.internalReviewer).toLowerCase();
  return owner && owner === reviewer ? 'The action owner cannot review their own draft. Choose a different internal reviewer or leave the reviewer unassigned. Walkthrough role choices grant no authority.' : null;
}

export function contextRecordKey(context) {
  // Choices are draft-local. Freshness follows source records and review feedback.
  const {roles, ...records} = context;
  return JSON.stringify(records);
}

export function isDraftContextCurrent(draft, context) {
  return Boolean(draft?.contextSnapshot && context && contextRecordKey(draft.contextSnapshot) === contextRecordKey(context));
}

function supportedContext(context) {
  const fixture = renewalFixture;
  return context?.synthetic === true && context.id === fixture.id && context.refs?.accountId === fixture.account.id
    && context.refs?.renewalId === fixture.renewal.id && context.refs?.agreementId === fixture.agreement.id
    && context.refs?.requirementId === fixture.requirementId && context.roles && context.bindings && Array.isArray(context.evidence);
}

export function preparePlaybookDraft({templateId, context, confirmed, id, at}) {
  const template = getPlaybookTemplate(templateId);
  if (!template) return {ok: false, error: 'Choose one of the supported templates.'};
  if (confirmed !== true) return {ok: false, error: 'Confirm the synthetic context before preparing a draft.'};
  if (!supportedContext(context)) return {ok: false, error: 'This local workbench supports only the supplied invented renewal and agreement.'};
  const roleError = draftResponsibilityError(context);
  if (roleError) return {ok: false, error: roleError};
  if (!clean(id) || !clean(at)) return {ok: false, error: 'A local draft identity and preparation time are required.'};
  const snapshot = clone(context);
  return {ok: true, draft: {
    schemaVersion: 2, id, simulation: true, status: 'draft', revision: 1, createdAt: at,
    templateRef: {id: template.id, version: template.version},
    contextSnapshot: snapshot, contextConfirmedAt: at,
    title: template.draftTitle, specifications: template.specifications, instructions: template.instructions,
    ...buildTemplateWork(templateId, snapshot), removedItemIds: [],
    output: clone(template.output), completionRules: clone(template.completionRules), reviewBoundary: template.reviewBoundary,
    humanEdits: [],
  }};
}

export function inspectPlaybookDraft(draft) {
  if (!draft || draft.status !== 'draft') return {ok: false, error: 'This separate draft is not available for inspection.'};
  const error = draftResponsibilityError(draft.contextSnapshot);
  return error ? {ok: false, error} : {ok: true, draft};
}

export function draftEditorValues(draft) {
  return {...Object.fromEntries(Object.keys(draftFields).map(field => [field, draft[field]])), removedItemIds: [...draft.removedItemIds]};
}

export function draftAutomaticItems(draft) {
  return buildTemplateWork(draft.templateRef.id, draft.contextSnapshot).items.filter(item => item.addedBecauseOf);
}

export function validateDraftEdits(values, draft) {
  const errors = {};
  for (const [field, {label, maxLength}] of Object.entries(draftFields)) {
    if (typeof values?.[field] !== 'string' || !values[field].trim()) errors[field] = `${label} is required.`;
    else if (values[field].trim().length > maxLength) errors[field] = `${label} must be ${maxLength} characters or fewer.`;
  }
  const removableIds = draft ? draftAutomaticItems(draft).map(item => item.id) : playbookTemplates.flatMap(template => template.automaticItems.map(item => item.id));
  if (!Array.isArray(values?.removedItemIds) || values.removedItemIds.some(id => !removableIds.includes(id))
    || new Set(values.removedItemIds).size !== values.removedItemIds.length) errors.removedItemIds = 'Only auto-added items can be removed, one level deep.';
  if (values && Object.keys(values).some(field => !Object.hasOwn(draftChangeLabels, field))) errors.scope = 'Only draft text and the inclusion of auto-added items are editable. Completion proof and authority requirements stay intact.';
  return errors;
}

export function changedDraftFields(draft, values) {
  const fields = Object.keys(draftFields).filter(field => clean(values?.[field]) !== draft[field]);
  if (JSON.stringify([...(values?.removedItemIds || [])].sort()) !== JSON.stringify([...draft.removedItemIds].sort())) fields.push('removedItemIds');
  return fields;
}

export function hasUnsavedPlaybookEdits(session) {
  return Boolean(session.draft && session.editor && changedDraftFields(session.draft, session.editor).length);
}

export function savePlaybookDraft(draft, values, {expectedRevision, at} = {}) {
  const inspection = inspectPlaybookDraft(draft);
  if (!inspection.ok) return inspection;
  if (!Number.isSafeInteger(expectedRevision) || expectedRevision !== draft.revision) return {
    ok: false, code: 'stale_revision', currentRevision: draft.revision,
    error: `This draft has changed. Expected revision ${expectedRevision ?? 'not supplied'}; current saved revision is ${draft.revision}. Your unsaved edits are retained. Copy any text you need, then Cancel to restore the latest saved draft.`,
  };
  const errors = validateDraftEdits(values, draft);
  if (Object.keys(errors).length) return {ok: false, error: 'Complete the required draft fields.', fieldErrors: errors};
  const fields = changedDraftFields(draft, values);
  if (!fields.length) return {ok: true, draft};
  if (!clean(at)) return {ok: false, error: 'A local edit time is required.'};
  const normalized = {...Object.fromEntries(Object.keys(draftFields).map(field => [field, values[field].trim()])), removedItemIds: [...values.removedItemIds].sort()};
  const revision = draft.revision + 1;
  return {ok: true, draft: {
    ...draft, ...normalized, revision,
    ...buildTemplateWork(draft.templateRef.id, draft.contextSnapshot, normalized.removedItemIds),
    humanEdits: [...draft.humanEdits, {
      revision, at, actor: 'Local user; not authenticated',
      changes: fields.map(field => ({field, before: clone(draft[field]), after: clone(normalized[field])})),
    }],
  }};
}

let nonceSequence = 0;
export function createPlaybooksSessionId(cryptoApi = globalThis.crypto) {
  try {if (typeof cryptoApi?.randomUUID === 'function') return cryptoApi.randomUUID();} catch { /* Use a local URL nonce. */ }
  nonceSequence += 1;
  return `local-${Date.now().toString(36)}-${nonceSequence}-${Math.random().toString(36).slice(2)}`;
}

// Memory only. This reducer has no workflow or storage commands.
export function createPlaybooksSession(sessionId) {
  if (!clean(sessionId)) throw new Error('A per-mount session identity is required.');
  return {schemaVersion: 2, sessionId, nextDraftNumber: 1, draft: null, editor: null, editorRevision: null, error: null, fieldErrors: {}, validationAttempt: 0, notice: ''};
}

export function playbooksSessionReducer(session, action) {
  switch (action.type) {
    case 'prepare': {
      if (session.draft) return {...session, error: 'Resume or delete the current draft before preparing another.'};
      const result = preparePlaybookDraft({...action, id: `pb-${session.sessionId}-${session.nextDraftNumber}`});
      if (!result.ok) return {...session, error: result.error};
      return {...session, draft: result.draft, editor: draftEditorValues(result.draft), editorRevision: result.draft.revision, nextDraftNumber: session.nextDraftNumber + 1,
        error: null, fieldErrors: {}, notice: 'Separate draft prepared as revision 1 in memory. Existing work is unchanged.'};
    }
    case 'edit_field': {
      if (!session.draft || !Object.hasOwn(draftFields, action.field) || typeof action.value !== 'string') return session;
      return {...session, editor: {...session.editor, [action.field]: action.value}, error: null,
        fieldErrors: {...session.fieldErrors, [action.field]: undefined}, notice: ''};
    }
    case 'toggle_auto_item': {
      if (!session.draft || !draftAutomaticItems(session.draft).some(item => item.id === action.id)) return session;
      const removed = session.editor.removedItemIds;
      return {...session, editor: {...session.editor, removedItemIds: removed.includes(action.id) ? removed.filter(id => id !== action.id) : [...removed, action.id]}, error: null, fieldErrors: {}, notice: ''};
    }
    case 'save_revision': {
      const result = savePlaybookDraft(session.draft, session.editor, {expectedRevision: action.expectedRevision, at: action.at});
      if (!result.ok) return {...session, error: result.error, fieldErrors: result.fieldErrors || {}, validationAttempt: session.validationAttempt + 1, notice: ''};
      return {...session, draft: result.draft, editor: draftEditorValues(result.draft), editorRevision: result.draft.revision, error: null, fieldErrors: {},
        notice: `Saved as revision ${result.draft.revision}. In this tab only. Existing work is unchanged.`};
    }
    case 'inspect': {
      const result = inspectPlaybookDraft(session.draft);
      return result.ok ? {...session, error: null, fieldErrors: {}} : {...session, error: result.error};
    }
    case 'cancel_edits':
      return session.draft ? {...session, editor: draftEditorValues(session.draft), editorRevision: session.draft.revision, error: null, fieldErrors: {}, notice: `Unsaved edits cancelled. Saved revision ${session.draft.revision} restored.`} : session;
    case 'delete_confirmed':
      return {...session, draft: null, editor: null, editorRevision: null, error: null, fieldErrors: {}, notice: 'Draft deleted. Reusable templates and existing work are unchanged.'};
    case 'reset_session':
      return {...createPlaybooksSession(session.sessionId), nextDraftNumber: session.nextDraftNumber};
    case 'clear_error':
      return {...session, error: null, fieldErrors: {}};
    default:
      return session;
  }
}

export function playbookSearch(templateId, view = 'template', draftId = null) {
  const params = new URLSearchParams({template: templateId});
  if (view !== 'template') params.set('view', view);
  if (['edit', 'inspect'].includes(view) && draftId) params.set('draft', draftId);
  return params;
}

export function preparedDraftLocation(previous, next) {
  if (previous.draft || next.error || !next.draft || next.nextDraftNumber !== previous.nextDraftNumber + 1) return null;
  return playbookSearch(next.draft.templateRef.id, 'edit', next.draft.id);
}

export function readPlaybookLocation(search, session) {
  const params = new URLSearchParams(search);
  const requested = params.get('template');
  const templateId = getPlaybookTemplate(requested)?.id || playbookTemplates[0].id;
  const requestedView = params.get('view') || 'template';
  const view = ['template', 'context', 'edit', 'inspect'].includes(requestedView) ? requestedView : 'template';
  const needsDraft = ['edit', 'inspect'].includes(view);
  const draftMatches = Boolean(getPlaybookTemplate(requested)) && session.draft?.templateRef.id === requested && session.draft?.id === params.get('draft');
  return {templateId, view: needsDraft && !draftMatches ? 'unavailable' : view,
    invalidSelection: Boolean(requested && requested !== templateId) || view !== requestedView};
}
