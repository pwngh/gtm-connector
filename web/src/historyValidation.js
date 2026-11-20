const object = value => Boolean(value && typeof value === 'object' && !Array.isArray(value));
const string = value => typeof value === 'string';
const scalar = value => ['string', 'boolean'].includes(typeof value) || typeof value === 'number' && Number.isFinite(value);
const revision = value => string(value) || typeof value === 'number' && Number.isFinite(value);
const optional = (value, check) => value == null || check(value);
const list = (value, check) => Array.isArray(value) && value.every(check);
const fields = (value, names, check = string) => names.every(key => optional(value[key], check));
const identity = value => object(value) && string(value.id) && Boolean(value.id);
const strings = (value, names) => object(value) && fields(value, names.split(' '));
const ref = value => object(value) && string(value.kind) && fields(value, ['id', 'cardId']) && optional(value.n, revision);
const inspection = value => strings(value, 'status at recordedAt recordedBy');
const receipt = value => identity(value) && strings(value, 'status artifactId documentId documentVersion receivedAt readAt recordedAt recordedBy')
  && optional(value.inspection, inspection);
const speaker = value => strings(value, 'name role basis authority');
const source = value => identity(value) && strings(value, 'label kind status readAt recordedAt scope documentId artifactId')
  && optional(value.unitNumbers, values => list(values, revision));
const unit = value => identity(value) && strings(value, 'cardId text sourceId sourceLabel eventAt readAt notShownReason partialReason')
  && optional(value.n, revision) && ['sentence', 'record'].includes(value.kind)
  && fields(value, ['shown', 'partial'], flag => typeof flag === 'boolean')
  && optional(value.speaker, speaker) && optional(value.receipt, receipt)
  && optional(value.neighbours, values => list(values, item => object(item) && optional(item.n, revision) && fields(item, ['cardId', 'id'])))
  && (value.kind !== 'record' || list(value.fields, field => strings(field, 'label') && string(field.label) && optional(field.value, scalar)));
const card = value => identity(value) && strings(value, 'label readAt recordedAt provenance') && optional(value.revision, revision)
  && list(value.units, unit) && list(value.sources, source)
  && list(value.gaps, gap => strings(gap, 'code title text') && string(gap.code) && optional(gap.count, count => typeof count === 'number' && Number.isFinite(count)));
const contact = value => identity(value) && strings(value, 'displayName status recordedAt');
const basis = value => identity(value) && strings(value, 'status outcome agreementId documentId documentVersion version contactId artifactId recordedAt receivedAt readAt recordedBy')
  && optional(value.revision, revision) && optional(value.evidenceRef, ref) && optional(value.source, ref) && optional(value.inspection, inspection);
const agreement = value => identity(value) && strings(value, 'label title displayTitle provenance');

const validators = {
  plan: value => strings(value, 'id title owner specifications instructions status requestedAt approvedAt approver reviewNote authoredBy')
    && fields(value, ['revision', 'approvedRevision'], revision),
  document: value => strings(value, 'id title name version currentVersion source status receivedAt verifiedAt verifiedBy'),
  customerReview: value => strings(value, 'status requestedAt requestedBy documentId documentVersion reviewedBy recordedBy evidenceText approvedAt')
    && optional(value.planRevision, revision),
  requirement: value => strings(value, 'id title status resolvedAt openedAt'),
  task: value => strings(value, 'id title owner status createdAt') && optional(value.planRevision, revision),
  commercial: value => strings(value, 'status label note'),
};
validators.requirements = value => list(value, item => validators.requirement(item) && identity(item) && string(item.status));
const contextValidators = {
  agreement, card, documentVersionBasis: basis, customerAuthority: basis, decisionReceipt: basis,
  contacts: value => list(value, contact), artifactReceipts: value => list(value, receipt), policyVersion: string,
};

export function validHistorySnapshot(snapshot) {
  return object(snapshot) && ['plan', 'requirement', 'commercial'].every(key => validators[key](snapshot[key]) && string(snapshot[key].status))
    && identity(snapshot.plan) && revision(snapshot.plan.revision) && identity(snapshot.requirement)
    && optional(snapshot.document, doc => identity(doc) && string(doc.version) && Boolean(doc.version) && string(doc.status))
    && optional(snapshot.customerReview, review => string(review.status) && string(review.documentId) && Boolean(review.documentId)
      && string(review.documentVersion) && Boolean(review.documentVersion) && revision(review.planRevision))
    && Object.entries(validators).every(([key, check]) => optional(snapshot[key], check));
}

export function validHistoryContext(context) {
  return object(context) && agreement(context.agreement) && list(context.contacts, contact) && list(context.artifactReceipts, receipt)
    && Object.entries(contextValidators).every(([key, check]) => optional(context[key], check));
}

export function partialHistorySnapshot(snapshot) {
  if (!object(snapshot)) return null;
  return Object.fromEntries(Object.entries(validators).map(([key, check]) => [key, snapshot[key] != null && check(snapshot[key]) ? snapshot[key] : null]));
}

export function partialHistoryContext(context) {
  if (!object(context)) return null;
  return Object.fromEntries(Object.entries(contextValidators).map(([key, check]) => [key,
    context[key] != null && check(context[key]) ? context[key] : ['contacts', 'artifactReceipts'].includes(key) ? [] : null]));
}

export function validHistoryChanges(changes) {
  return object(changes) && Object.keys(changes).length > 0 && Object.keys(changes).every(key => ['after', 'contextAfter', 'eventAt'].includes(key))
    && optional(changes.eventAt, string)
    && (!Object.hasOwn(changes, 'after') || object(changes.after) && Object.keys(changes.after).every(key => Object.hasOwn(validators, key)))
    && (!Object.hasOwn(changes, 'contextAfter') || object(changes.contextAfter) && Object.keys(changes.contextAfter).every(key => Object.hasOwn(contextValidators, key)));
}
