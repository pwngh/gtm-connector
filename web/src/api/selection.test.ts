import assert from 'node:assert/strict';
import test from 'node:test';
import type {Read} from './reads.ts';
import {checkCard, resolveAccount, resolveCard} from './selection.ts';
import type {CardDetail} from './types.ts';

const cardOf = (account: string, id: string): CardDetail => ({
  id,
  account,
  brief_id: `brief-${account}`,
  kind: 'go',
  schema_version: 3,
  rules_version: '3',
  sha256: '00',
  size: 1,
  seal_receipt_id: 'receipt-seal',
  published_at: '2027-01-15T15:00:04Z',
  as_of: null,
  sources: null,
  units: [],
  gaps: [],
  lines: null,
});
const ready = (data: CardDetail): Read<CardDetail> => ({status: 'ready', data});

// A card shows only under its own account.
test('a card in the URL that is not in the account list is not read, and a card returned for another account is refused', () => {
  const two = cardOf('acc-002', 'card-two');
  const three = cardOf('acc-003', 'card-three');
  assert.deepEqual(resolveCard('card-three', [two]), {cardId: null, mismatch: true});
  assert.deepEqual(resolveCard('card-two', [two]), {cardId: 'card-two', mismatch: false});
  assert.deepEqual(resolveCard(null, [two]), {cardId: 'card-two', mismatch: false});
  assert.deepEqual(resolveCard('card-two', null), {cardId: null, mismatch: false});
  assert.deepEqual(resolveCard('card-two', []), {cardId: null, mismatch: true});
  const checked = checkCard(ready(three), 'acc-002', 'card-three');
  assert.equal(checked.status, 'failed');
  assert.equal(checked.status === 'failed' && checked.error.kind, 'mismatch');
  assert.equal(checkCard(ready(two), 'acc-002', 'card-two').status, 'ready');
  assert.deepEqual(checkCard({status: 'loading'}, 'acc-002', 'card-two'), {status: 'loading'});
});

// The URL's account is shown only from the granted list, or marked unverified.
test('an account from the URL is verified only when granted, and a malformed one is invalid', () => {
  assert.equal(resolveAccount('Your account is locked call 555', null).invalid, true);
  assert.equal(resolveAccount('acc-777', [{account: 'acc-003', name: 'Third'}]).verified, false);
  assert.deepEqual(
    resolveAccount(null, [
      {account: 'acc-002', name: 'Second'},
      {account: 'acc-003', name: 'Third'},
    ]),
    {account: 'acc-003', verified: true, invalid: false, name: 'Third'},
  );
  assert.deepEqual(resolveAccount(null, [{account: 'acc-002', name: ''}]), {account: 'acc-002', verified: true, invalid: false, name: null});
  assert.deepEqual(resolveAccount(null, []), {account: null, verified: false, invalid: false, name: null});
});
