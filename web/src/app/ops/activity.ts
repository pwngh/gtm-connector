import type {Api} from '../../api/client.ts';
import {sealedText} from '../../api/sealed.ts';
import type {CardDetail, Json, ReceiptView} from '../../api/types.ts';
import {serverPaths} from '../routes.ts';

// Activity's own logic, kept apart from the page so it can be tested without
// a browser. The receipts come from the card the server sends: its
// seal_receipt_id and each unit's receipt_id. The server has no list of
// receipts, so nothing else is listed.

/** The address parameters: the card, and the receipt open in the drawer. */
export const cardParam = 'card';
export const receiptParam = 'receipt';

/** One receipt the card names, and whether it is the card's seal. */
export interface Named {
  id: string;
  seal: boolean;
}

const usable = (value: unknown): value is string => typeof value === 'string' && value !== '';

/** The card's seal receipt, then each distinct read receipt its units name, in the card's order. */
export function receiptsOf(card: CardDetail): Named[] {
  const seal = usable(card.seal_receipt_id) ? card.seal_receipt_id : null;
  const seen = new Set<string>(seal ? [seal] : []);
  const named: Named[] = seal ? [{id: seal, seal: true}] : [];
  for (const unit of Array.isArray(card.units) ? card.units : []) {
    const id = unit?.receipt_id;
    if (usable(id) && !seen.has(id)) {
      seen.add(id);
      named.push({id, seal: false});
    }
  }
  return named;
}

const timeOf = (value: unknown) => (usable(value) ? Date.parse(value) : Number.NaN);

/**
 * The seal first, then the reads in ordinal order. A read with no ordinal
 * comes after those with one, by when it started; a read not read yet, or
 * refused, keeps its place in the card's order after them.
 */
export function inOrder<T extends Named>(rows: T[], receiptOf: (row: T) => ReceiptView | null): T[] {
  const key = (row: T, at: number) => {
    const receipt = receiptOf(row);
    const ordinal = receipt && Number.isInteger(receipt.ordinal) ? (receipt.ordinal as number) : Number.POSITIVE_INFINITY;
    const started = receipt ? timeOf(receipt.started_at) : Number.NaN;
    return {seal: row.seal ? 0 : 1, ordinal, started: Number.isFinite(started) ? started : Number.POSITIVE_INFINITY, at};
  };
  return rows
    .map((row, at) => ({row, key: key(row, at)}))
    .sort((a, b) => a.key.seal - b.key.seal || a.key.ordinal - b.key.ordinal || a.key.started - b.key.started || a.key.at - b.key.at)
    .map(item => item.row);
}

/** The address of Activity for a card, with one of its receipts open. */
export function activityHref(card: string, receipt: string | null = null): string {
  const params = new URLSearchParams({[cardParam]: card});
  if (receipt) {
    params.set(receiptParam, receipt);
  }
  return `${serverPaths.activity}?${params}`;
}

/**
 * A value as inert text: every character outside printable ASCII, and the
 * backslash, written as an escape, so no control, format or look-alike
 * character from a caller reaches the screen as itself.
 */
export function inert(value: string): string {
  let out = '';
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    if (char === '\\') {
      out += '\\\\';
    } else if (code >= 0x20 && code <= 0x7e) {
      out += char;
    } else {
      for (let i = 0; i < char.length; i++) {
        out += `\\u${char.charCodeAt(i).toString(16).padStart(4, '0')}`;
      }
    }
  }
  return out;
}

/** JSON as the server sent it, written out as ASCII JSON text with escapes, two spaces to a level. */
export function inertJson(value: Json | undefined): string {
  const text = JSON.stringify(value ?? null, null, 2) ?? 'null';
  return text.replace(/[^\n\x20-\x7e]/gu, char =>
    Array.from({length: char.length}, (_, i) => `\\u${char.charCodeAt(i).toString(16).padStart(4, '0')}`).join(''),
  );
}

/** The digest the seal names: its one row, briefs/<the card's brief ID>/<64 hex digits>, or null when it names none. */
export function namedDigest(receipt: Pick<ReceiptView, 'rows'>, card: Pick<CardDetail, 'brief_id'>): string | null {
  const rows = Array.isArray(receipt.rows) ? receipt.rows : [];
  const prefix = `briefs/${card.brief_id}/`;
  const named = rows.filter(row => typeof row === 'string' && row.startsWith(prefix) && /^[0-9a-f]{64}$/.test(row.slice(prefix.length)));
  return rows.length === 1 && named.length === 1 ? (named[0] as string).slice(prefix.length) : null;
}

/**
 * Whether the card's stored bytes match the digest the seal names: sealedText
 * hashes the bytes in the browser against that digest and the digest the
 * server sends with them, and checks the text they hold. A seal that names no
 * digest matches nothing, and nothing is read for it. A refused read is thrown.
 */
export async function sealMatches(
  api: Pick<Api, 'cardBytes' | 'cardText'>,
  card: Pick<CardDetail, 'id' | 'brief_id'>,
  seal: Pick<ReceiptView, 'rows'>,
): Promise<boolean> {
  const digest = namedDigest(seal, card);
  return digest !== null && (await sealedText(api, {id: card.id, sha256: digest})).outcome === 'verified';
}

/** The status as the receipt holds it, with its HTTP status when there is one. */
export function statusOf(receipt: Pick<ReceiptView, 'status' | 'http_status'>): string {
  return Number.isInteger(receipt.http_status) ? `${inert(String(receipt.status))} ${receipt.http_status}` : inert(String(receipt.status));
}

/** How long the work took, to two places as audit prints it, or that it is not recorded. */
export function elapsedOf(receipt: Pick<ReceiptView, 'elapsed_ms'>): string {
  return typeof receipt.elapsed_ms === 'number' && Number.isFinite(receipt.elapsed_ms) ? `${receipt.elapsed_ms.toFixed(2)} ms` : 'Not recorded';
}

/** The receipt the server answered is the one asked for. */
export const answers = (receipt: ReceiptView | null | undefined, id: string): receipt is ReceiptView =>
  typeof receipt === 'object' && receipt !== null && receipt.id === id;
