import type {Api} from '../../../api/client.ts';
import {type Sealed, sealedText} from '../../../api/sealed.ts';
import type {CardView} from '../../../api/types.ts';

// The cards that failed their seal check in this session, by ID, kept in
// memory for each server store. A card that failed stays failed until the
// session ends, whatever a later read answers; a new session starts empty.
const failures = new WeakMap<object, {generation: number; cards: Set<string>}>();

/** The open card's seal check, answered failed at once for a card that failed earlier in the session. */
export async function checkSeal(
  store: object,
  generation: number,
  api: Pick<Api, 'cardBytes' | 'cardText'>,
  card: Pick<CardView, 'id' | 'sha256'>,
  signal?: AbortSignal,
): Promise<Sealed> {
  let failed = failures.get(store);
  if (failed?.generation !== generation) {
    failed = {generation, cards: new Set()};
    failures.set(store, failed);
  }
  if (failed.cards.has(card.id)) {
    return {outcome: 'failed'};
  }
  const sealed = await sealedText(api, card, signal ? {signal} : {});
  if (sealed.outcome === 'failed') {
    failed.cards.add(card.id);
  }
  return sealed;
}
