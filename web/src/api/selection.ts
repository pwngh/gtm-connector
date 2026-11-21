import {ApiError, demoAccount} from './client.ts';
import type {Read} from './reads.ts';
import type {AccountView, CardDetail, CardView} from './types.ts';

// Which account and card a server page reads. A link's text is never trusted:
// an account is shown by name only when it is one the server granted, a card
// is opened only when it is one of that account's published cards, and a card
// the server returns must be that card of that account.

// What an account ID can look like; anything else in a link is not read or shown.
export const accountShape = /^[A-Za-z0-9._:-]{1,64}$/;

export interface ResolvedAccount {
  account: string | null;
  verified: boolean;
  invalid: boolean;
  name: string | null;
}

export function resolveAccount(asked: string | null | undefined, granted: AccountView[] | null | undefined): ResolvedAccount {
  if (asked !== null && asked !== undefined && !accountShape.test(asked)) {
    return {account: null, verified: false, invalid: true, name: null};
  }
  if (asked) {
    const entry = granted?.find(item => item.account === asked);
    return {account: asked, verified: Boolean(entry), invalid: false, name: entry?.name || null};
  }
  const entry = granted?.find(item => item.account === demoAccount) ?? granted?.[0];
  if (entry) {
    return {account: entry.account, verified: true, invalid: false, name: entry.name || null};
  }
  return {account: null, verified: false, invalid: false, name: null};
}

export interface ResolvedCard {
  cardId: string | null;
  mismatch: boolean;
}

// The card to open: the one asked for, if it is in the account's list; else
// the latest published. Until the list is read, none.
export function resolveCard(asked: string | null | undefined, listed: Pick<CardView, 'id'>[] | null | undefined): ResolvedCard {
  if (!listed) {
    return {cardId: null, mismatch: false};
  }
  if (asked) {
    return listed.some(card => card.id === asked) ? {cardId: asked, mismatch: false} : {cardId: null, mismatch: true};
  }
  return {cardId: listed[0]?.id || null, mismatch: false};
}

// A card read that came back as another card, or another account's, is refused here.
export function checkCard(read: Read<CardDetail>, account: string | null, cardId: string | null): Read<CardDetail> {
  if (read.status !== 'ready') {
    return read;
  }
  if (read.data?.id === cardId && read.data?.account === account) {
    return read;
  }
  return {status: 'failed', error: new ApiError({kind: 'mismatch'}), retry: read.retry};
}
