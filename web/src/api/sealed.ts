import type {Api, ReadOptions} from './client.ts';
import type {CardView} from './types.ts';

/** A card's seal check: the plain-text card when its bytes match its seal, else failed and nothing else. */
export type Sealed = {outcome: 'verified'; text: string} | {outcome: 'failed'};

const hex = (digest: ArrayBuffer) => Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('');

// Checks a card's stored bytes against its seal in the browser, and answers
// its plain-text card. The bytes are hashed with SHA-256; the digest must
// equal the card's sha256 and the X-Content-SHA256 the server sent with the
// bytes. The bytes are then read as JSON, without the field named evidence,
// and the string in text must equal the text the server serves for the card.
// Any difference answers failed. Only the outcome and the text leave; the
// parsed file is not kept. A refused read is thrown as the server's refusal.
export async function sealedText(api: Pick<Api, 'cardBytes' | 'cardText'>, card: Pick<CardView, 'id' | 'sha256'>, {signal}: ReadOptions = {}): Promise<Sealed> {
  const [{bytes, sha256}, served] = await Promise.all([api.cardBytes(card.id, signal ? {signal} : {}), api.cardText(card.id, signal ? {signal} : {})]);
  const digest = hex(await crypto.subtle.digest('SHA-256', bytes));
  if (digest !== card.sha256 || digest !== sha256) {
    return {outcome: 'failed'};
  }
  let text: unknown;
  try {
    const file = new TextDecoder('utf-8', {fatal: true, ignoreBOM: true}).decode(bytes);
    text = (JSON.parse(file, (key, value) => (key === 'evidence' ? undefined : value)) as {text?: unknown} | null)?.text;
  } catch {
    return {outcome: 'failed'};
  }
  return typeof text === 'string' && text === served ? {outcome: 'verified', text} : {outcome: 'failed'};
}
