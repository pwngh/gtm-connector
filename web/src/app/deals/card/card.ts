import {gapDispositions, notShownReasons, type UnitFacts, unitFacts} from '../../../api/client.ts';
import type {CardDetail, CardView, GapView} from '../../../api/types.ts';

// The Deal card's own logic, kept apart from its sections so it can be tested
// without a browser. Everything here is read from the card's units and gaps;
// nothing reads the card's text, and nothing reads a sealed card's evidence.

/** A sentence unit as the card shows it. */
export type Sentence = UnitFacts & {n: number};

/** A record unit: its number and its reference. */
export interface RecordUnit {
  n: number;
  recordRef: string;
}

const units = (card: CardDetail) => (Array.isArray(card.units) ? card.units : []);

/** The card's sentence units, in the card's order. */
export function sentencesOf(card: CardDetail): Sentence[] {
  return units(card)
    .filter(unit => unit?.kind === 'sentence' && Number.isInteger(unit.n))
    .map(unit => unitFacts(unit) as Sentence);
}

/** The card's record units, in the card's order. */
export function recordsOf(card: CardDetail): RecordUnit[] {
  return units(card)
    .filter(unit => unit?.kind === 'record' && Number.isInteger(unit.n))
    .map(unit => ({n: unit.n, recordRef: typeof unit.record_ref === 'string' ? unit.record_ref : ''}));
}

/** A whole sentence the card shows. A cut unit is never one, whatever else it says. */
export const onCard = (sentence: Sentence): boolean => sentence.shown === 'yes' && !sentence.partial;

export type GroupId = 'customer' | 'unknown' | 'seller' | 'other_party';

/**
 * The groups of what people said, in the order a rep reads them: the buyers, those of no established
 * role, your side, then other organisations. The last is shown only when a sentence is in it.
 */
export const groups: {id: GroupId; title: string; always: boolean}[] = [
  {id: 'customer', title: 'Buyer quotes', always: true},
  {id: 'unknown', title: 'No established role', always: true},
  {id: 'seller', title: 'Your side', always: true},
  {id: 'other_party', title: 'Other party', always: false},
];

/** The group a sentence belongs to. A customer-side speaker who is not a contact is with the buyers; any
 * other role that is not customer, seller or another party is not on either side. */
export function groupOf(sentence: Sentence): GroupId {
  switch (sentence.role) {
    case 'customer':
    case 'customer_side_not_in_crm':
      return 'customer';
    case 'seller':
    case 'other_party':
      return sentence.role;
  }
  return 'unknown';
}

/** The role in a word. */
export const roleWords: Record<GroupId, string> = {customer: 'Customer', seller: 'Seller', unknown: 'Unknown', other_party: 'Other party'};

/** The organisation of a speaker of another party: the domain its basis names, as sealed. */
export function partyDomain(sentence: Sentence): string | null {
  const named = /^other party by domain (\S+)$/.exec(sentence.basis ?? '');
  return named?.[1] ?? null;
}

/**
 * Who a sentence is from, in words: its role, the domain of another party, and how the speaker was
 * matched, as sealed. A unit sealed before the speaker rules shows its basis in the words it was sealed
 * with.
 */
export function roleOf(sentence: Sentence): string {
  const group = groupOf(sentence);
  const domain = group === 'other_party' ? partyDomain(sentence) : null;
  return `${roleWords[group]}${domain ? `, ${domain}` : ''} (${sentence.basis ?? 'basis not recorded'})`;
}

/** Where a sentence was said: a call, Slack, or the source as recorded. */
export function sourceWord(source: string | null): string {
  if (source === 'calls') {
    return 'call';
  }
  if (source === 'slack') {
    return 'Slack';
  }
  return source ?? 'Source not recorded';
}

/** A time that can be ordered: one with an offset that parses. Any other time is a literal. */
export function usableTime(at: string | null): number | null {
  if (!at || !/(?:Z|[+-]\d\d:?\d\d)$/i.test(at)) {
    return null;
  }
  const time = Date.parse(at);
  return Number.isFinite(time) ? time : null;
}

/** Newest first; a sentence with no usable time last; equal times in the card's order. */
export function newestFirst(sentences: readonly Sentence[]): Sentence[] {
  return sentences
    .map((sentence, at) => ({sentence, at, time: usableTime(sentence.at)}))
    .sort((a, b) => {
      if (a.time === null || b.time === null) {
        return a.time === b.time ? a.at - b.at : a.time === null ? 1 : -1;
      }
      return b.time - a.time || a.at - b.at;
    })
    .map(item => item.sentence);
}

/** Each basis among the sentences, as recorded, with how many sentences have it, in the order first met. */
export function basisCounts(sentences: readonly Sentence[]): {basis: string; count: number}[] {
  const counts = new Map<string, number>();
  for (const sentence of sentences) {
    const basis = sentence.basis ?? 'Basis not recorded';
    counts.set(basis, (counts.get(basis) ?? 0) + 1);
  }
  return [...counts].map(([basis, count]) => ({basis, count}));
}

/** The reasons a sentence is not on the card, in the connector's order. */
const reasonOrder = ['cut', 'later', 'long', 'more'];

export interface Reason {
  shown: string;
  label: string;
  sentences: Sentence[];
}

/** The sentences the card does not show, grouped by reason, each group in the card's order. */
export function notShown(sentences: readonly Sentence[]): Reason[] {
  const byReason = new Map<string, Sentence[]>();
  for (const sentence of sentences) {
    if (onCard(sentence)) {
      continue;
    }
    // A cut unit is listed as cut, whatever its shown says.
    const shown = sentence.partial ? 'cut' : (sentence.shown ?? '');
    byReason.set(shown, [...(byReason.get(shown) ?? []), sentence]);
  }
  const rank = (shown: string) => (reasonOrder.includes(shown) ? reasonOrder.indexOf(shown) : reasonOrder.length);
  return [...byReason]
    .sort(([a], [b]) => rank(a) - rank(b))
    .map(([shown, list]) => ({
      shown,
      label: notShownReasons[shown] ?? (shown ? `Not shown: ${shown}` : 'Whether it is shown is not recorded'),
      sentences: list,
    }));
}

/** The gap codes that say a whole source was not read. */
export const wholeSources = new Set([
  'calls_denied',
  'calls_not_connected',
  'calls_not_matched',
  'slack_denied',
  'slack_not_connected',
  'slack_not_requested',
  'slack_not_selected',
  'slack_not_matched',
  'crm_unmapped_fields',
]);

function gapRank(gap: GapView): number {
  if (gap.disposition === 'not_read') {
    return wholeSources.has(gap.code) ? 0 : 1;
  }
  if (gap.disposition === 'read_not_shown') {
    return 2;
  }
  return gap.disposition === 'note' ? 3 : 4;
}

/** The gaps in the order Not read lists them, each group in the card's order. */
export function orderedGaps(card: CardDetail): GapView[] {
  const gaps = Array.isArray(card.gaps) ? card.gaps : [];
  return gaps
    .map((gap, at) => ({gap, at}))
    .sort((a, b) => gapRank(a.gap) - gapRank(b.gap) || a.at - b.at)
    .map(item => item.gap);
}

/** A gap's disposition in words. */
export const dispositionWords = (disposition: string): string => gapDispositions[disposition] ?? disposition;

/** The card the deal shows and whether it is the newest, from the account's card list. */
export interface Pinned {
  /** The card the address pins, when it is not the newest. */
  older: CardView | null;
  newest: CardView | null;
}

export function pinnedOf(listed: readonly CardView[] | null, cardId: string | null): Pinned {
  const newest = listed?.[0] ?? null;
  const open = listed?.find(card => card.id === cardId) ?? null;
  return {newest, older: open && newest && open.id !== newest.id ? open : null};
}

export const plural = (count: number, one: string, many: string): string => `${count} ${count === 1 ? one : many}`;
