import {useSyncExternalStore} from 'react';
import {ApiError} from '../../api/client.ts';
import type {Outcome} from '../../api/reads.ts';
import type {AccountCards, AccountView, CardView} from '../../api/types.ts';

// The deal list's own logic, kept apart from the page so it can be tested
// without a browser. A deal is an account the server grants; nothing here
// ranks, groups or dates a deal beyond what the server sent.

export const dealsPath = '/deals';

/** At this width and wider the list is a rail beside the open deal. */
export const wideQuery = '(min-width: 1100px)';

/** Whether a media query matches, following it as it changes. False without a window. */
export function useMedia(query: string): boolean {
  return useSyncExternalStore(
    notify => {
      const media = window.matchMedia(query);
      media.addEventListener('change', notify);
      return () => media.removeEventListener('change', notify);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

/** A deal's tabs: the card at the deal's own address, what was read at /communications and its plain text at /text below it. */
export type DealTab = 'card' | 'communications' | 'text';

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
  return `${dealsPath}/${encodeURIComponent(account)}${tab === 'card' ? '' : `/${tab}`}${search}`;
}

const dealPath = /^\/deals\/([^/]+)(?:\/(communications|text))?$/;

/** The tab a deal's address opens. */
export function tabAt(pathname: string): DealTab {
  return (dealPath.exec(pathname)?.[2] as DealTab | undefined) ?? 'card';
}

/** Where the calls and threads that wait for a person are listed. */
export const matchesPath = '/deals/matches';

/** The deal an address opens: the one segment after /deals/, on the card, at /communications or at /text, or null. The matches page is no deal. */
export function dealAt(pathname: string): string | null {
  const match = dealPath.exec(pathname);
  if (!match?.[1] || pathname === matchesPath) {
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

/** The one line above the deal list when calls or threads wait for a person. */
export function waitingLine(count: number): string {
  return count === 1 ? '1 call or thread waits for your OK' : `${count} calls and threads wait for your OK`;
}

/** What a waiting match is, in a word or two. */
export function matchKind(kind: string): string {
  return kind === 'call' ? 'Call' : kind === 'thread' ? 'Slack thread' : kind;
}

/** Why the connector did not decide a match itself, from the basis the server gave. */
export function matchReason(basis: string): string {
  switch (basis) {
    case 'time_unknown':
      return 'It fits more than one deal of this account, so the connector did not choose.';
    case 'deal_closed':
      return 'The deal it was on is closed, and the account has a new deal open.';
    default:
      return 'It waits for a person to decide.';
  }
}
