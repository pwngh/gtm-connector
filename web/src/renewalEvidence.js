import {readinessFixture, worklistFixture} from './renewalFixtures.js';
import {customerAuthorityLabel} from './readiness.js';

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

// Who the counsel review waits on. A role alone is not an identified decision owner.
export const awaitedCounsel = worklistFixture.workItems.find(item => item.kind === 'customer_review').awaitedParty;
export const awaitedCounselNote = 'Unidentified until a reviewer records who decides, with evidence';

// The same projection Readiness shows, so a typed name never becomes authority.
export function decisionAuthorityLabel(state = null) {
  return customerAuthorityLabel(readinessFixture, state);
}

// A role and how it was established. A role never establishes authority.
export function speakerRole(record, card = sourceCard) {
  const speaker = evidenceUnit(record, card)?.speaker;
  return {
    name: speaker?.name || record?.speaker || 'Not recorded',
    role: speaker?.role || record?.role || 'Not recorded',
    basis: speaker?.basis || 'Not recorded. The role is a label in the excerpt.',
    // The kind of match, for compact previews: the basis up to its first colon.
    basisLabel: speaker?.basis ? speaker.basis.split(':')[0] : 'not recorded',
    authority: authorityLabels[speaker?.authority] || authorityLabels.unidentified,
  };
}

// A compact line for previews: the role, its basis and the speaker's authority.
export function speakerLine(record, card = sourceCard) {
  const speaker = speakerRole(record, card);
  return `Role: ${speaker.role} (basis: ${speaker.basisLabel.toLowerCase()}). Customer decision authority: ${speaker.authority.toLowerCase()}.`;
}

// A cut flag on the record wins over the card, since the record is what is shown.
export function evidenceCut(record, card = sourceCard) {
  const unit = evidenceUnit(record, card);
  return cutLabel(record && 'partial' in record ? record : unit);
}

// The cited unit inside its preserved excerpt: the text before and after it.
export function excerptAround(record) {
  const context = record?.context || '';
  const index = record?.quote ? context.indexOf(record.quote) : -1;
  if (index < 0) return null;
  return {before: context.slice(0, index).trim(), after: context.slice(index + record.quote.length).trim()};
}
