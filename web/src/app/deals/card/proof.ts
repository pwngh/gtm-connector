import {unitFacts} from '../../../api/client.ts';
import type {CardDetail, UnitView} from '../../../api/types.ts';
import {notShown, onCard, type Sentence} from './card.ts';

// The proof drawer's own logic, kept apart from the drawer so it can be
// tested without a browser. Everything here is read from the card's units and
// the unit the server answers; nothing reads the card's text.

/** The address parameter that opens the drawer on a unit. */
export const unitParam = 'unit';

/** The display key that says a drawer has been opened in this browser. */
export const proofSeenKey = 'gtm-connector-proof-seen';

/** What ?unit= asks for: the unit number, or null for a value that is not one, and the value as written. */
export interface Asked {
  n: number | null;
  written: string;
}

export function askedUnit(value: string | null): Asked | null {
  if (value === null) {
    return null;
  }
  return {n: /^[1-9]\d{0,8}$/.test(value) ? Number(value) : null, written: value};
}

/** The search of the address with ?unit= set to n, or removed for null; every other parameter is kept. */
export function unitSearch(search: string, n: number | null): string {
  const params = new URLSearchParams(search);
  if (n === null) {
    params.delete(unitParam);
  } else {
    params.set(unitParam, String(n));
  }
  const next = params.toString();
  return next ? `?${next}` : '';
}

/** The card's units that carry a number, in the card's order. */
export const unitsOf = (card: CardDetail): UnitView[] => (Array.isArray(card.units) ? card.units : []).filter(unit => Number.isInteger(unit?.n));

/** The card's unit n, or null when the card has none. */
export const unitOf = (card: CardDetail, n: number | null): UnitView | null => (n === null ? null : (unitsOf(card).find(unit => unit.n === n) ?? null));

/** The number of the unit before (-1) or after (1) unit n in the card's order, or null at either end. */
export function stepFrom(card: CardDetail, n: number, by: -1 | 1): number | null {
  const units = unitsOf(card);
  const at = units.findIndex(unit => unit.n === n);
  return at < 0 ? null : (units[at + by]?.n ?? null);
}

/** The units of the same record just before and after unit n, in the card's order. */
export function around(card: CardDetail, n: number): {before: UnitView | null; after: UnitView | null} {
  const unit = unitOf(card, n);
  if (!unit || typeof unit.record_ref !== 'string' || !unit.record_ref) {
    return {before: null, after: null};
  }
  const same = unitsOf(card).filter(item => item.record_ref === unit.record_ref);
  const at = same.findIndex(item => item.n === n);
  return {before: same[at - 1] ?? null, after: same[at + 1] ?? null};
}

/** A unit in one line: a whole sentence's text, a cut one's label, or a record's reference. */
export function firstLine(unit: UnitView): string {
  const facts = unitFacts(unit);
  if (unit.kind === 'record') {
    return unit.record_ref || 'Reference not recorded';
  }
  if (facts.partial) {
    return 'Cut, not a whole quotation';
  }
  return facts.text ?? 'Text not recorded';
}

/** Whether the card shows the unit, or why not, in the words the card page uses. */
export function placeOf(unit: UnitView): string {
  if (unit.kind !== 'sentence') {
    return 'On the card';
  }
  const sentence = unitFacts(unit) as Sentence;
  return onCard(sentence) ? 'On the card' : (notShown([sentence])[0]?.label ?? 'Whether it is shown is not recorded');
}

/** Where a read came from, in words. */
export function readFrom(source: string | null): string {
  if (source === 'calls') {
    return 'calls';
  }
  if (source === 'slack') {
    return 'Slack';
  }
  if (source === 'crm') {
    return 'the CRM';
  }
  return source ?? 'a source not recorded';
}

/** The unit the server answered is the one asked for: the same number of the same card. */
export const answers = (unit: UnitView | null | undefined, card: string, n: number): unit is UnitView =>
  typeof unit === 'object' && unit !== null && unit.card === card && unit.n === n;
