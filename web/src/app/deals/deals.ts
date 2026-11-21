import {ApiError} from '../../api/client.ts';
import type {Outcome} from '../../api/reads.ts';
import type {AccountCards, AccountView, CardView} from '../../api/types.ts';

// The deal list's own logic, kept apart from the page so it can be tested
// without a browser. A deal is an account the server grants; nothing here
// ranks, groups or dates a deal beyond what the server sent.

export const dealsPath = '/deals';

/** A deal's tabs: the card at the deal's own address, and its plain text at /text below it. */
export type DealTab = 'card' | 'text';

/** The address of one deal on one of its tabs, with a pinned card and the list's filter kept. */
export function dealHref(account: string, query = '', {tab = 'card', card = null}: {tab?: DealTab; card?: string | null} = {}): string {
  const params = new URLSearchParams();
  if (card) {
    params.set('card', card);
  }
  if (query.trim()) {
    params.set('q', query);
  }
  const search = params.size ? `?${params}` : '';
  return `${dealsPath}/${encodeURIComponent(account)}${tab === 'text' ? '/text' : ''}${search}`;
}

const dealPath = /^\/deals\/([^/]+)(\/text)?$/;

/** The tab a deal's address opens. */
export function tabAt(pathname: string): DealTab {
  return dealPath.exec(pathname)?.[2] ? 'text' : 'card';
}

/** The deal an address opens: the one segment after /deals/, on the card or at /text, or null. */
export function dealAt(pathname: string): string | null {
  const match = dealPath.exec(pathname);
  if (!match?.[1]) {
    return null;
  }
  try {
    return decodeURIComponent(match[1]);
  } catch {
    // A segment that does not decode is passed on as written, and is then not an account ID.
    return match[1];
  }
}

const collator = new Intl.Collator('en', {sensitivity: 'base', numeric: true});

/** The granted accounts by name, then by ID for equal names. */
export function byName(accounts: readonly AccountView[]): AccountView[] {
  return [...accounts].sort((a, b) => collator.compare(a.name, b.name) || collator.compare(a.account, b.account));
}

/** The accounts whose name holds the filter, ignoring case. An empty filter keeps all. */
export function matching(accounts: readonly AccountView[], query: string): AccountView[] {
  const wanted = query.trim().toLocaleLowerCase('en');
  return wanted ? accounts.filter(item => item.name.toLocaleLowerCase('en').includes(wanted)) : [...accounts];
}

/** What a row shows at its right: reading, the newest card, no card, or a refusal. */
export type RowRead = {status: 'reading'} | {status: 'ready'; newest: CardView | null} | {status: 'failed'; error: ApiError};

// An account's card list as a row shows it. The server lists cards newest
// first, so the newest is the first; a list that is not a list is refused.
export function rowFrom(outcome: Outcome<AccountCards>): RowRead {
  if (outcome.status === 'failed') {
    return {status: 'failed', error: outcome.error};
  }
  const cards = outcome.data?.cards;
  if (!Array.isArray(cards)) {
    return {status: 'failed', error: new ApiError({kind: 'unexpected'})};
  }
  return {status: 'ready', newest: cards[0] ?? null};
}

/** Starts one read; calls done with its answer unless the returned release is called first. */
export type StartRead = (name: string, done: () => void) => () => void;

// Reads every name in order with at most `limit` reads in flight: each
// answer starts the next. The returned stop releases the reads still in
// flight and starts no more.
export function readInTurn(names: readonly string[], limit: number, start: StartRead): () => void {
  const inFlight = new Map<number, () => void>();
  let next = 0;
  let stopped = false;
  const fill = () => {
    while (!stopped && inFlight.size < limit && next < names.length) {
      const at = next;
      next += 1;
      let answered = false;
      const release = start(names[at] as string, () => {
        answered = true;
        inFlight.delete(at);
        fill();
      });
      if (!answered) {
        inFlight.set(at, release);
      }
    }
  };
  fill();
  return () => {
    stopped = true;
    for (const release of inFlight.values()) {
      release();
    }
    inFlight.clear();
  };
}

/** How many card lists the deal list reads at once. */
export const cardReadLimit = 4;

/** The display choice that hides the first-run hint. */
export const hintKey = 'gtm-connector-deals-hint';
