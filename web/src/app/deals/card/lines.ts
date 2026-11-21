import type {CardDetail, Json} from '../../../api/types.ts';

// The card's lines as the server sends them in GET /api/v1/cards/{card}: each
// line under ATTENTION (a signal) or RECORD VALUES (a value), with the facts
// its code carries. Nothing here reads the card's text: every word shown is a
// line's own text, label or typed fact.

/** A countdown: the day a renewal counts to and the server's count of days from the card's date. */
export interface Countdown {
  date: string;
  /** From the card's as_of day to date; negative once date has passed. */
  days: number;
  /** renewal, an account's renewal date, or contract_end, the end of the contract that ends next. */
  countsTo: string;
}

/** One line of the card. */
export interface CardLine {
  section: 'signal' | 'value';
  code: string;
  /** A value's label as the card prints it; null for a signal. */
  label: string | null;
  /** The value or the sentence, in the connector's words. */
  text: string;
  /** The numbers of the units it cites, in order. */
  cites: number[];
  /** How many more a signal counts than it cites. */
  more: number;
  countdown: Countdown | null;
  amount: {amount: string; currency: string | null} | null;
  contact: {name: string | null; title: string | null} | null;
}

type JsonObject = {[key: string]: Json};

const object = (value: Json | undefined): JsonObject | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as JsonObject) : null;
const textOrNull = (value: Json | undefined): string | null | undefined => (value === null ? null : typeof value === 'string' ? value : undefined);
const count = (value: Json | undefined): number | null => (typeof value === 'number' && Number.isInteger(value) ? value : null);

/** One line as the server sent it, or null when it is not a line's shape. */
export function readLine(value: Json): CardLine | null {
  const line = object(value);
  if (!line) {
    return null;
  }
  const {section, code, label, text, cites, more} = line;
  const numbers = Array.isArray(cites) ? cites.map(count) : null;
  if (
    (section !== 'signal' && section !== 'value') ||
    typeof code !== 'string' ||
    typeof text !== 'string' ||
    textOrNull(label) === undefined ||
    !numbers?.length ||
    numbers.some(n => n === null || n < 1) ||
    count(more) === null
  ) {
    return null;
  }
  let countdown: Countdown | null = null;
  if (line.countdown !== null) {
    const c = object(line.countdown);
    const days = count(c?.days);
    if (!c || typeof c.date !== 'string' || days === null || typeof c.counts_to !== 'string') {
      return null;
    }
    countdown = {date: c.date, days, countsTo: c.counts_to};
  }
  let amount: CardLine['amount'] = null;
  if (line.amount !== null) {
    const a = object(line.amount);
    const currency = textOrNull(a?.currency);
    if (!a || typeof a.amount !== 'string' || currency === undefined) {
      return null;
    }
    amount = {amount: a.amount, currency};
  }
  let contact: CardLine['contact'] = null;
  if (line.contact !== null) {
    const p = object(line.contact);
    const name = textOrNull(p?.name);
    const title = textOrNull(p?.title);
    if (!p || name === undefined || title === undefined) {
      return null;
    }
    contact = {name, title};
  }
  return {section, code, label: label as string | null, text, cites: numbers as number[], more: more as number, countdown, amount, contact};
}

/**
 * The card's lines in the card's order, or null when the card has none: a
 * card published before lines were kept, or lines that are not a line's
 * shape, which are then not read at all.
 */
export function linesOf(card: CardDetail): CardLine[] | null {
  if (!Array.isArray(card.lines)) {
    return null;
  }
  const lines = card.lines.map(readLine);
  return lines.every(line => line !== null) ? (lines as CardLine[]) : null;
}

/** The value lines, in the card's order. */
export const valuesOf = (lines: readonly CardLine[]): CardLine[] => lines.filter(line => line.section === 'value');

/** The signal lines, in the card's order. */
export const signalsOf = (lines: readonly CardLine[]): CardLine[] => lines.filter(line => line.section === 'signal');

/**
 * The line a countdown is read from: the renewal date, or the contract end on
 * a card that counts to a contract. Null when the card has neither.
 */
export const renewalOf = (lines: readonly CardLine[]): CardLine | null =>
  lines.find(line => line.section === 'value' && line.code === 'renewal_date') ??
  lines.find(line => line.section === 'value' && line.code === 'contract_end') ??
  null;

/** The annual recurring revenue line, or null when the card has none. */
export const arrOf = (lines: readonly CardLine[]): CardLine | null =>
  lines.find(line => line.section === 'value' && line.code === 'arr' && line.amount) ?? null;

/** What a countdown counts to, in words. */
export const countsToWords: Record<string, string> = {renewal: 'the renewal', contract_end: 'the contract end'};

/** The unit word beside a number of days. */
export const dayWord = (days: number): string => (days === 1 || days === -1 ? 'day' : 'days');

/**
 * The title of each contact the card shows, by the number of the contact's
 * unit: the title from that contact's line, when the record has one.
 */
export function titlesOf(lines: readonly CardLine[] | null): Map<number, string> {
  const titles = new Map<number, string>();
  for (const line of lines ?? []) {
    const n = line.cites[0];
    if (line.section === 'value' && line.code === 'contact' && line.contact?.title && n !== undefined && !titles.has(n)) {
      titles.set(n, line.contact.title);
    }
  }
  return titles;
}
