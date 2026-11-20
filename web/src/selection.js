import {ApiError, demoAccount} from './api.js';

// Which account and card a server page reads. A link's text is never trusted:
// an account is shown by name only when it is one the server granted, a card
// is opened only when it is one of that account's published cards, and a card
// the server returns must be that card of that account.

// What an account ID can look like; anything else in a link is not read or shown.
export const accountShape = /^[A-Za-z0-9._:-]{1,64}$/;

export function resolveAccount(asked, granted) {
  if (asked !== null && asked !== undefined && !accountShape.test(asked)) return {account: null, verified: false, invalid: true, name: null};
  if (asked) {
    const entry = granted?.find(item => item.account === asked);
    return {account: asked, verified: Boolean(entry), invalid: false, name: entry?.name || null};
  }
  if (granted?.length) {
    const entry = granted.find(item => item.account === demoAccount) || granted[0];
    return {account: entry.account, verified: true, invalid: false, name: entry.name || null};
  }
  return {account: null, verified: false, invalid: false, name: null};
}

// The card to open: the one asked for, if it is in the account's list; else
// the latest published. Until the list is read, none.
export function resolveCard(asked, listed) {
  if (!listed) return {cardId: null, mismatch: false};
  if (asked) return listed.some(card => card.id === asked) ? {cardId: asked, mismatch: false} : {cardId: null, mismatch: true};
  return {cardId: listed[0]?.id || null, mismatch: false};
}

// A card read that came back as another card, or another account's, is refused here.
export function checkCard(read, account, cardId) {
  if (read.status !== 'ready') return read;
  if (read.data?.id === cardId && read.data?.account === account) return read;
  return {...read, status: 'failed', data: undefined, error: new ApiError({kind: 'mismatch'})};
}
