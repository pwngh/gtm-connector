import {readinessFixture} from './renewalFixtures.js';

// Shared export for existing evidence consumers.
export {renewalEvidence} from './renewalFixtures.js';

// One wording for sources, gaps, cut units and speaker roles, so Work plan,
// Evidence and Readiness say the same thing about the same records.
export const sourceCard = readinessFixture.card;

export function sourceReadLabel(status) {
  return status === 'read' ? 'Bounded local read' : 'Not read';
}

export function cutLabel(unit) {
  return unit?.partial ? `Partial excerpt · ${unit.partialReason || 'cut boundary not recorded'}` : null;
}

// The numbered unit behind an evidence record, if the card has it.
export function evidenceUnit(record, card = sourceCard) {
  return card?.units.find(unit => unit.legacyId === record?.id) || null;
}

const authorityLabels = {
  unidentified: 'Unidentified',
  not_customer_authority: 'None (internal speaker)',
};

// The walkthrough records no authority basis, so a typed name never becomes authority.
export const customerAuthorityLabel = readinessFixture.customerAuthority ? 'Recorded separately' : authorityLabels.unidentified;

// Context written for the walkthrough, kept apart from what the speaker said.
export function workflowNoteText(record, card = sourceCard) {
  return `${record.meaning}. Customer decision authority: ${speakerRole(record, card).authority.toLowerCase()}.`;
}

// A role and how it was established. A role never establishes authority.
export function speakerRole(record, card = sourceCard) {
  const speaker = evidenceUnit(record, card)?.speaker;
  return {
    name: speaker?.name || record?.speaker || 'Not recorded',
    role: speaker?.role || record?.role || 'Not recorded',
    basis: speaker?.basis || 'Not recorded. The role is a label in the excerpt.',
    authority: authorityLabels[speaker?.authority] || authorityLabels.unidentified,
  };
}

// A cut flag on the record wins over the card, since the record is what is shown.
export function evidenceCut(record, card = sourceCard) {
  const unit = evidenceUnit(record, card);
  return cutLabel(record && 'partial' in record ? record : unit);
}
