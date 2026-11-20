import {readinessFixture} from './renewalFixtures.js';

// Evidence checks shared by the reducer, Readiness and History. They read typed
// records only: receipts, contacts, the authority record, the version basis and
// the recorded decision. No typed name or decision text can pass them.
export const evidenceCheckIds = ['current_version_basis', 'customer_authority', 'decision_citation'];
export const emptyEvidence = {artifactReceipts: [], contacts: [], customerAuthority: null};

// The walkthrough's own records win over a fixture or a retained context with the same ID.
const byId = (own = [], others = []) => [...new Map([...others, ...own].map(item => [item.id, item])).values()];

// A receipt keeps every inspection; the latest one is its current state.
export function receiptInspections(receipt) {
  if (Array.isArray(receipt?.inspections)) return receipt.inspections;
  return receipt?.inspection ? [receipt.inspection] : [];
}
export const latestInspection = receipt => receiptInspections(receipt).at(-1) || null;

// The recorded decision as an evidence record, bound to what it cites and the authority it relied on.
export function decisionRecord(review) {
  if (review?.status !== 'approved' || !review.evidenceRef) return null;
  return {id: review.id || `decision-${review.documentId}-${review.documentVersion}`, revision: '1', recordedAt: review.approvedAt || null,
    recordedBy: review.recordedBy || null, status: 'accepted', outcome: 'approved', agreementId: review.agreementId || null,
    documentId: review.documentId || null, documentVersion: review.documentVersion || null, contactId: review.contactId || null,
    authorityRef: review.authorityRef || null, evidenceRef: review.evidenceRef};
}

// The evidence a check reads: the fixture's card, agreement and version basis, with the walkthrough's
// own records. The walkthrough's own authority, decision, contacts and receipts always win, so a
// fixture or retained context can never override what the walkthrough recorded.
export function workflowEvidence(state, fixture = readinessFixture) {
  const own = state?.evidence || emptyEvidence;
  // A decision the walkthrough recorded, current or marked for re-review, always wins over a fixture's or a retained context's.
  const ownHasDecision = ['approved', 'needs_re_review'].includes(state?.customerReview?.status);
  const ownDecision = decisionRecord(state?.customerReview);
  return {...fixture,
    contacts: byId(own.contacts, fixture.contacts), artifactReceipts: byId(own.artifactReceipts, fixture.artifactReceipts),
    documentVersionBasis: fixture.documentVersionBasis || null,
    customerAuthority: own.customerAuthority || fixture.customerAuthority || null,
    decisionReceipt: ownHasDecision ? ownDecision : fixture.decisionReceipt || null,
  };
}

export function resolveEvidenceReference(ref, fixture = readinessFixture) {
  if (!ref || typeof ref !== 'object') return {issue: 'No supported evidence reference is recorded.'};
  if (ref.kind === 'receipt') {
    const record = fixture.artifactReceipts?.find(item => item.id === ref.id);
    if (!record) return {issue: 'The cited artifact receipt does not exist.'};
    if (record.status !== 'received' || latestInspection(record)?.status !== 'accepted') return {issue: 'The cited artifact receipt needs an accepted inspection.'};
    return {record, source: null};
  }
  if (ref.kind === 'agreement') {
    const agreement = fixture.agreement;
    if (!agreement || agreement.id !== ref.id) return {issue: 'The referenced agreement record does not exist.'};
    if (!agreement.applicableRider?.version) return {issue: 'The agreement record does not name an applicable version.'};
    return {record: agreement, source: null};
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

// A version basis is independent of the document, and a letter approving a version never establishes it.
function versionSourceIssue(reference, ref, document) {
  if (!reference.record || !document?.id) return 'The version source is not established as independent of the document.';
  if (ref?.kind === 'receipt') return 'A receipt cannot establish the applicable version. A letter names the version it approves, and a document cannot vouch for itself.';
  if (ref?.kind === 'agreement') {
    const rider = reference.record.applicableRider;
    return rider.documentId === document.id && rider.version === document.version ? null
      : `${reference.record.label || 'The agreement record'} names ${rider.documentId} ${rider.version}, not ${document.id} ${document.version}.`;
  }
  const records = [reference.record, reference.source].filter(Boolean);
  if (records.some(record => [record.id, record.documentId, record.artifactId].includes(document.id))) return 'The version source is not established as independent of the document.';
  return ['call', 'message', 'record'].includes(reference.source?.kind) ? null : 'The version source is not established as independent of the document.';
}

// Whether a receipt is an accepted signed letter for exactly this agreement, document and version.
export function letterScopeIssues(record, document, agreementId) {
  if (!record) return ['No receipt is recorded.'];
  if (record.kind !== 'approval_letter') return ['Only an accepted signed approval letter can record the customer’s approval. That receipt is not a signed approval letter.'];
  if (latestInspection(record)?.status !== 'accepted') return ['The signed letter needs an accepted inspection.'];
  if (!document?.id) return ['No received document is recorded for the letter to cover.'];
  const approves = record.approves || {};
  if (approves.agreementId !== agreementId || approves.documentId !== document.id || approves.version !== document.version)
    return [`The letter approves ${approves.documentId || 'an unrecorded document'} ${approves.version || ''} for ${approves.agreementId || 'an unrecorded agreement'}, not ${document.id} ${document.version} for ${agreementId}.`.replace(/\s+/g, ' ')];
  return [];
}

// The authority record and the contact it supports for the document under review. Authority
// is bound to the agreement, document, version and signatory of the accepted letter it cites.
export function authorityEvidence(fixture, document) {
  const basis = fixture.customerAuthority;
  if (!basis) return {issues: ['No authority record is recorded for this agreement.'], contact: null};
  const issues = [];
  if (basis.status !== 'accepted' || basis.agreementId !== fixture.agreement.id) issues.push(basis.status === 'withdrawn'
    ? `The authority record was withdrawn${basis.withdrawReason ? `: ${basis.withdrawReason}` : '.'}` : 'The authority record is not accepted for this agreement.');
  if (!document?.id) issues.push('No document version is under review for the authority record to cover.');
  else if (basis.documentId !== document.id || basis.version !== document.version)
    issues.push(basis.documentId ? `The authority record covers ${basis.documentId} ${basis.version}, not ${document.id} ${document.version}. Authority for this version is unidentified until a letter covering it is accepted and recorded.`
      : 'The authority record does not state which document version it covers.');
  const contact = fixture.contacts?.find(item => item.id === basis.contactId && item.status === 'active');
  if (!contact) issues.push('The authority record does not identify an available customer contact.');
  const reference = resolveEvidenceReference(basis.evidenceRef, fixture);
  if (reference.issue) issues.push(reference.issue);
  else {
    const record = reference.record;
    if (basis.evidenceRef?.kind !== 'receipt' || record.kind !== 'approval_letter') issues.push('The authority basis must be an accepted signed-letter receipt.');
    else {
      const approves = record.approves || {};
      if (approves.agreementId !== basis.agreementId || approves.documentId !== basis.documentId || approves.version !== basis.version) issues.push('The authority record’s scope differs from what its letter approves.');
      if (record.contactId !== basis.contactId) issues.push('The authority record names a contact who did not sign its letter.');
    }
  }
  return {issues, contact: issues.length ? null : contact};
}

// What a decision may cite: the accepted signed letter for the exact agreement, document and version,
// signed by the approving contact. Units, sources, documents and unrelated receipts are refused.
export function citationIssues(ref, fixture, document, contactId = null) {
  if (!ref || typeof ref !== 'object') return ['No supported evidence reference is recorded.'];
  if (ref.kind === 'unit') return ['A source unit cannot establish an approval until units carry a speaker-to-authority binding and a reviewer label. Cite the accepted signed letter.'];
  if (ref.kind !== 'receipt') return ['A customer decision must cite the accepted signed letter for this version.'];
  const reference = resolveEvidenceReference(ref, fixture);
  if (reference.issue) return [reference.issue];
  const record = reference.record;
  if (record.kind === 'document' || record.artifactId === document?.id) return ['A document receipt cannot record the customer’s approval of that document.'];
  const issues = letterScopeIssues(record, document, fixture.agreement.id);
  if (!issues.length && contactId && record.contactId !== contactId) issues.push('The cited letter is not signed by the approving contact.');
  return issues;
}

export function evaluateEvidenceCheck(id, state, fixture) {
  const definition = fixture.checks?.find(check => check.id === id) || {id, title: id};
  if (!evidenceCheckIds.includes(id)) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation: 'No evaluation rule is recorded for this evidence check.'};
  if (!fixture.card) return {result: 'not_evaluated', allowed: null, blockedReasons: [], allowedActions: [], explanation: 'No source card is recorded. This evidence check has not been evaluated.'};
  const document = state?.document;
  let issues = [], contact = null;
  if (id === 'current_version_basis') {
    const basis = fixture.documentVersionBasis;
    if (!basis) issues.push('No independent current-version basis is recorded. The document cannot vouch for its own current version.');
    else {
      const reference = resolveEvidenceReference(basis.source, fixture);
      if (reference.issue) issues.push(reference.issue);
      else {
        const issue = versionSourceIssue(reference, basis.source, document);
        if (issue) issues.push(issue);
      }
      if (!document?.id || !document.version || basis.documentId !== document.id || basis.version !== document.version || basis.agreementId !== fixture.agreement.id || basis.status === 'withdrawn') issues.push('The separate version basis does not identify this document version for this agreement.');
    }
  } else if (id === 'customer_authority') {
    ({issues, contact} = authorityEvidence(fixture, document));
  } else {
    const basis = fixture.decisionReceipt;
    if (!basis) issues.push('No cited customer decision is recorded. Typed text is not a cited inspected approval receipt.');
    else {
      issues.push(...citationIssues(basis.evidenceRef, fixture, document, basis.contactId));
      if (!document?.id || !document.version || basis.status !== 'accepted' || basis.outcome !== 'approved' || basis.agreementId !== fixture.agreement.id || basis.documentId !== document.id || basis.documentVersion !== document.version) issues.push('The accepted decision is not bound to this agreement and document version.');
      const authority = authorityEvidence(fixture, document);
      if (!authority.contact || basis.contactId !== authority.contact.id) issues.push('The decision does not identify the contact supported by the accepted authority basis.');
      else if (basis.authorityRef?.id !== fixture.customerAuthority.id || basis.authorityRef?.revision !== fixture.customerAuthority.revision) issues.push('The decision does not record the authority record it relied on.');
    }
  }
  const explanation = issues.length ? issues.join(' ') : 'A resolved evidence reference supports this check within the synthetic records.';
  return {
    result: issues.length ? 'failed' : 'passed', allowed: null, allowedActions: [], supportedContact: contact,
    blockedReasons: issues.length ? [{code: id, action: 'establish_evidence', name: definition.title, reason: explanation, checkId: id, evidenceGap: true}] : [],
    explanation,
  };
}

// The contact supported by accepted authority evidence for this document version, or null. A role or typed name never is.
export function supportedAuthority(fixture, document) {
  return authorityEvidence(fixture, document).contact;
}

// The approving contact from the decision's stable contact ID, never from a typed name.
export function decisionContact(state) {
  const id = state?.customerReview?.contactId;
  return id ? (state.evidence?.contacts || []).find(contact => contact.id === id) || null : null;
}
