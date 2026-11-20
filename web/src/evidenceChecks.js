import {readinessFixture} from './renewalFixtures.js';

// Evidence checks shared by the reducer, Readiness and History. They read typed
// records only: receipts, contacts, the authority record, the version basis and
// the recorded decision. No typed name or decision text can pass them.
export const evidenceCheckIds = ['current_version_basis', 'customer_authority', 'decision_citation'];
export const emptyEvidence = {artifactReceipts: [], contacts: [], customerAuthority: null, documentVersionBasis: null};

const byId = (preferred = [], others = []) => [...new Map([...others, ...preferred].map(item => [item.id, item])).values()];

// The recorded decision as an evidence record, bound to what it cites.
export function decisionRecord(review) {
  if (review?.status !== 'approved' || !review.evidenceRef) return null;
  return {id: review.id || `decision-${review.documentId}-${review.documentVersion}`, revision: '1', recordedAt: review.approvedAt || null,
    recordedBy: review.recordedBy || null, status: 'accepted', outcome: 'approved', agreementId: review.agreementId || null,
    documentId: review.documentId || null, documentVersion: review.documentVersion || null, contactId: review.contactId || null,
    evidenceRef: review.evidenceRef};
}

// The evidence a check reads: the fixture's card and agreement, with the walkthrough's own
// records. A fixture or retained history context wins over the state it was recorded from.
export function workflowEvidence(state, fixture = readinessFixture) {
  const own = state?.evidence || emptyEvidence;
  return {...fixture,
    contacts: byId(fixture.contacts, own.contacts), artifactReceipts: byId(fixture.artifactReceipts, own.artifactReceipts),
    documentVersionBasis: fixture.documentVersionBasis || own.documentVersionBasis || null,
    customerAuthority: fixture.customerAuthority || own.customerAuthority || null,
    decisionReceipt: fixture.decisionReceipt || decisionRecord(state?.customerReview) || null,
  };
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

export function authorityEvidence(fixture) {
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

// What a cited receipt itself records. A document never approves itself, and a
// receipt that names an approved version must name this one.
export function citationIssues(reference, document, agreementId) {
  const record = reference?.record;
  if (!record || reference.source) return [];
  if (!document?.id) return ['No received document is recorded for the cited receipt to cover.'];
  if (record.kind === 'document' || record.artifactId === document.id) return ['A document receipt cannot record the customer’s approval of that document.'];
  const approves = record.approves;
  if (approves && (approves.agreementId !== agreementId || approves.documentId !== document.id || approves.version !== document.version))
    return [`The cited receipt approves ${approves.documentId || 'an unrecorded document'} ${approves.version || ''} for ${approves.agreementId || 'an unrecorded agreement'}, not this document version.`.replace(/\s+/g, ' ')];
  return [];
}

export function evaluateEvidenceCheck(id, state, fixture) {
  const definition = fixture.checks?.find(check => check.id === id) || {id, title: id};
  if (!evidenceCheckIds.includes(id)) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation: 'No evaluation rule is recorded for this evidence check.'};
  if (!fixture.card) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation: 'No source card is recorded. This evidence check has not been evaluated.'};
  let issues = [], contact = null;
  if (id === 'current_version_basis') {
    const basis = fixture.documentVersionBasis;
    if (!basis) issues.push('No independent current-version basis is recorded. The document cannot vouch for its own current version.');
    else {
      const reference = resolveEvidenceReference(basis.source, fixture);
      if (reference.issue) issues.push(reference.issue);
      else if (!isIndependentSource(reference, state?.document)) issues.push('The version source is not established as independent of the document.');
      if (!state?.document?.id || !state.document.version || basis.documentId !== state.document.id || basis.version !== state.document.version || basis.agreementId !== fixture.agreement.id || basis.status === 'withdrawn') issues.push('The separate version basis does not identify this document version for this agreement.');
    }
  } else if (id === 'customer_authority') {
    ({issues, contact} = authorityEvidence(fixture));
  } else {
    const basis = fixture.decisionReceipt;
    if (!basis) issues.push('No cited customer decision is recorded. Typed text is not a cited unit or inspected approval receipt.');
    else {
      const reference = resolveEvidenceReference(basis.evidenceRef, fixture);
      if (reference.issue) issues.push(reference.issue);
      else issues.push(...citationIssues(reference, state?.document, fixture.agreement.id));
      if (!['unit', 'receipt'].includes(basis.evidenceRef?.kind)) issues.push('A customer decision must cite a unit or an inspected artifact receipt.');
      if (!state?.document?.id || !state.document.version || basis.status !== 'accepted' || basis.outcome !== 'approved' || basis.agreementId !== fixture.agreement.id || basis.documentId !== state.document.id || basis.documentVersion !== state.document.version) issues.push('The accepted decision is not bound to this agreement and document version.');
      const authority = authorityEvidence(fixture);
      if (!authority.contact || basis.contactId !== authority.contact.id) issues.push('The decision does not identify the contact supported by the accepted authority basis.');
    }
  }
  const explanation = issues.length ? issues.join(' ') : 'A resolved evidence reference supports this check within the synthetic records.';
  return {
    result: issues.length ? 'failed' : 'passed', allowed: null, allowedActions: [], supportedContact: contact,
    blockedReasons: issues.length ? [{code: id, action: 'establish_evidence', name: definition.title, reason: explanation, checkId: id, evidenceGap: true}] : [],
    explanation,
  };
}

// The contact supported by accepted authority evidence, or null. A role or typed name never is.
export function supportedAuthority(fixture) {
  return authorityEvidence(fixture).contact;
}
