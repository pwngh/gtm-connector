import assert from 'node:assert/strict';
import test from 'node:test';
import {type Api, ApiError} from './client.ts';
import {deferred} from './fakeFetch.ts';
import {createReads, type Outcome} from './reads.ts';
import type {ServerState} from './session.ts';

// A store with only what the read layer uses: its api, its generation, and expire.
function storeOf() {
  let state: ServerState = {mode: 'api', session: null, error: null, ended: false, generation: 1, elsewhere: null, endSessionUrl: null};
  const listeners = new Set<() => void>();
  let expired = 0;
  const store = {
    api: {} as Api,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getSnapshot: () => state,
    expire() {
      expired += 1;
      return Promise.resolve(state);
    },
  };
  return {
    store,
    expired: () => expired,
    nextSession() {
      state = {...state, generation: state.generation + 1};
      for (const listener of listeners) {
        listener();
      }
    },
  };
}

// A load whose answers the test gives, one per request, and which records aborts.
function loads() {
  const asked: {signal: AbortSignal; answer: ReturnType<typeof deferred<unknown>>}[] = [];
  const load = (_api: Api, signal: AbortSignal) => {
    const answer = deferred<unknown>();
    asked.push({signal, answer});
    return answer.promise;
  };
  return {asked, load};
}
const settled = () => new Promise(resolve => setImmediate(resolve));

test('reads of one key at the same time share one request, and each waiter gets the answer', async () => {
  const {store} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  const got: Outcome<unknown>[] = [];
  reads.request('1|accounts', load, outcome => got.push(outcome));
  reads.request('1|accounts', load, outcome => got.push(outcome));
  assert.equal(asked.length, 1);
  asked[0]?.answer.resolve({accounts: []});
  await settled();
  assert.deepEqual(got, [
    {status: 'ready', data: {accounts: []}},
    {status: 'ready', data: {accounts: []}},
  ]);
});

test('a read nobody waits for is aborted, and one still awaited is not', async () => {
  const {store} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  const first = reads.request('1|cards:acc-003', load, () => undefined);
  const second = reads.request('1|cards:acc-003', load, () => undefined);
  first();
  assert.equal(asked[0]?.signal.aborted, false, 'one waiter is left');
  second();
  assert.equal(asked[0]?.signal.aborted, true, 'nobody waits any more');
  // A new request for the key starts afresh.
  reads.request('1|cards:acc-003', load, () => undefined);
  assert.equal(asked.length, 2);
});

test('an answer for a key no longer asked for is dropped', async () => {
  const {store} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  const got: string[] = [];
  const release = reads.request('1|cards:acc-002', load, () => got.push('acc-002'));
  reads.request('1|cards:acc-003', load, () => got.push('acc-003'));
  release();
  asked[0]?.answer.resolve({cards: []});
  asked[1]?.answer.resolve({cards: []});
  await settled();
  assert.deepEqual(got, ['acc-003']);
  assert.equal(reads.last('1|cards:acc-002'), undefined, 'an aborted read keeps no answer');
});

test('a page that comes back within the session finds its last answer; a failed read again forgets it', async () => {
  const {store} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  reads.request('1|card:c-1', load, () => undefined);
  asked[0]?.answer.resolve({id: 'c-1'});
  await settled();
  assert.deepEqual(reads.last('1|card:c-1'), {data: {id: 'c-1'}});
  reads.request('1|card:c-1', load, () => undefined);
  assert.equal(asked.length, 2, 'it reads again');
  asked[1]?.answer.reject(new ApiError({kind: 'not_permitted', code: 'not_permitted'}));
  await settled();
  assert.equal(reads.last('1|card:c-1'), undefined, 'a refusal is never covered by an earlier answer');
});

test('a change of session forgets every answer and aborts every read', async () => {
  const {store, nextSession} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  const got: Outcome<unknown>[] = [];
  reads.request('1|accounts', load, () => undefined);
  asked[0]?.answer.resolve({accounts: [{account: 'acc-002', name: 'Second'}]});
  await settled();
  reads.request('1|cards:acc-002', load, outcome => got.push(outcome));
  nextSession();
  assert.equal(asked[1]?.signal.aborted, true);
  assert.equal(reads.last('1|accounts'), undefined);
  // An answer that lands after the change is not kept.
  asked[1]?.answer.resolve({cards: []});
  await settled();
  assert.equal(reads.last('1|cards:acc-002'), undefined);
  assert.deepEqual(got, [], 'nobody is answered from the earlier session');
});

test('a read refused for sign-in ends the session, once per request', async () => {
  const {store, expired} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  const got: Outcome<unknown>[] = [];
  reads.request('1|accounts', load, outcome => got.push(outcome));
  reads.request('1|accounts', load, outcome => got.push(outcome));
  asked[0]?.answer.reject(new ApiError({status: 401, kind: 'signed_out', code: 'sign_in_required'}));
  await settled();
  assert.equal(expired(), 1);
  assert.deepEqual(
    got.map(outcome => outcome.status === 'failed' && outcome.error.kind),
    ['signed_out', 'signed_out'],
  );
  // Any other refusal leaves the session alone, and a thrown non-API error reads as unexpected.
  reads.request('1|card:c-9', load, outcome => got.push(outcome));
  asked[1]?.answer.reject(new TypeError('boom'));
  await settled();
  assert.equal(expired(), 1);
  assert.equal(got.at(-1)?.status === 'failed' && got.at(-1)?.status, 'failed');
});

test('forget drops a last answer, so a retry shows the read, not the old answer', async () => {
  const {store} = storeOf();
  const reads = createReads(store);
  const {asked, load} = loads();
  reads.request('1|accounts', load, () => undefined);
  asked[0]?.answer.resolve({accounts: []});
  await settled();
  reads.forget('1|accounts');
  assert.equal(reads.last('1|accounts'), undefined);
});
