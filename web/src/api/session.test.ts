import assert from 'node:assert/strict';
import test from 'node:test';
import {ApiError, createApiClient, sessionExpired} from './client.ts';
import {deferred, fakeFetch, principal, refused, signedIn, signedOut} from './fakeFetch.ts';
import {createServerStore} from './session.ts';

interface Timer {
  run: () => unknown;
  ms: number;
}
const quiet = {setTimer: () => 0, clearTimer() {}};
const snapshot = (store: ReturnType<typeof createServerStore>) => store.getSnapshot();

test('expiry: a session past its time, or a read refused for sign-in, shows as signed out', async () => {
  assert.equal(sessionExpired(signedIn('t', '2027-01-15T10:00:00Z'), Date.parse('2027-01-15T10:00:00Z')), true);
  assert.equal(sessionExpired(signedIn('t', '2027-01-15T10:00:00Z'), Date.parse('2027-01-15T09:59:59Z')), false);
  assert.equal(sessionExpired(signedOut, Date.parse('2030-01-01T00:00:00Z')), false);

  const timers: Timer[] = [];
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T10:00:00Z')}, {json: signedOut}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), {
    now: () => Date.parse('2027-01-15T09:00:00Z'),
    setTimer: (run, ms) => {
      timers.push({run, ms});
      return timers.length;
    },
    clearTimer() {},
  });
  await store.start();
  assert.equal(snapshot(store).session?.signed_in, true);
  // At the session's stated end the store signs out, then asks the server for a sign-in token.
  assert.equal(timers.at(-1)?.ms, 3600 * 1000);
  await timers.at(-1)?.run();
  assert.equal(snapshot(store).session?.signed_in, false);
  assert.equal(snapshot(store).ended, true);
  assert.equal(snapshot(store).session?.csrf_token, 'pre-token');
  // A revoked session found by a refused read is the same.
  const revoked = createServerStore(createApiClient(fakeFetch({json: signedIn()}, {json: signedOut}).fetch), quiet);
  await revoked.start();
  await revoked.expire();
  assert.deepEqual([snapshot(revoked).session?.signed_in, snapshot(revoked).ended], [false, true]);
});

test('the store signs in with the pre-session token, retries once with a fresh one, and signs out', async () => {
  const {fetch, calls} = fakeFetch(
    {json: signedOut},
    refused(403, 'csrf_invalid'),
    {json: {...signedOut, csrf_token: 'fresh'}},
    {json: signedIn('s2')},
    {json: {signed_in: false, principal: null}},
    {json: {...signedOut, csrf_token: 'after'}},
  );
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  await store.signIn('demo-reviewer', 'secret');
  assert.deepEqual(
    calls.filter(call => call.method === 'POST').map(call => call.headers['X-CSRF-Token']),
    ['pre-token', 'fresh'],
  );
  assert.equal(snapshot(store).session?.principal?.display_name, 'Demo reviewer');
  await store.signOut();
  assert.equal(calls.at(-2)?.headers['X-CSRF-Token'], 's2');
  assert.deepEqual([snapshot(store).session?.signed_in, snapshot(store).ended, snapshot(store).session?.csrf_token], [false, false, 'after']);

  const wrong = createServerStore(createApiClient(fakeFetch({json: signedOut}, refused(401, 'sign_in_refused')).fetch), quiet);
  await wrong.start();
  await assert.rejects(wrong.signIn('demo-reviewer', 'nope'), error => error instanceof ApiError && error.kind === 'sign_in_refused');
  assert.equal(snapshot(wrong).session?.signed_in, false);
});

test('a sign-out keeps the identity provider’s sign-out page it offers, an https one only, until the next sign-in', async () => {
  const offered = 'https://login.invented.example/logout?client_id=connector-invented-client';
  const {fetch} = fakeFetch(
    {json: signedIn('s1')},
    {json: {signed_in: false, principal: null, end_session_url: offered}},
    {json: signedOut},
    {json: signedIn('s2')},
  );
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  assert.equal(snapshot(store).endSessionUrl, null);
  await store.signOut();
  assert.deepEqual([snapshot(store).session?.signed_in, snapshot(store).endSessionUrl], [false, offered]);
  await store.signIn('demo-reviewer', 'secret');
  assert.deepEqual([snapshot(store).session?.signed_in, snapshot(store).endSessionUrl], [true, null]);

  for (const page of ['javascript:alert(1)', 'http://login.invented.example/logout', 42]) {
    const other = createServerStore(
      createApiClient(fakeFetch({json: signedIn('s1')}, {json: {signed_in: false, principal: null, end_session_url: page}}, {json: signedOut}).fetch),
      quiet,
    );
    await other.start();
    await other.signOut();
    assert.equal(snapshot(other).endSessionUrl, null, `${page} is not offered`);
  }
});

// Review round: the session store.
test('at expiry the view is signed out at once; a failed refresh stays signed out and nothing polls', async () => {
  const timers: Timer[] = [];
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T10:00:00Z')}, refused(503, 'internal'));
  const store = createServerStore(createApiClient(fetch), {
    now: () => Date.parse('2027-01-15T09:00:00Z'),
    setTimer: (run, ms) => {
      timers.push({run, ms});
      return timers.length;
    },
    clearTimer() {},
  });
  await store.start();
  const before = snapshot(store).generation;
  assert.equal(timers.length, 1);
  assert.equal(timers[0]?.ms, 3600 * 1000);
  await timers[0]?.run();
  const state = snapshot(store);
  assert.equal(state.session?.signed_in, false, 'an expired session never shows as signed in');
  assert.equal(state.ended, true);
  assert.equal(state.error?.kind, 'server');
  assert.ok(state.generation > before, 'the expiry starts a new generation, so earlier reads are dropped');
  assert.equal(timers.length, 1, 'no timer is armed again after a failed refresh');
});

test('a session already past its expiry when it arrives is signed out at once, and no timer stays armed', async () => {
  const armed = new Set<number>();
  let next = 0;
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T08:00:00Z')}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), {
    now: () => Date.parse('2027-01-15T09:00:00Z'),
    setTimer: () => {
      next += 1;
      armed.add(next);
      return next;
    },
    clearTimer: id => armed.delete(id as number),
  });
  await store.start();
  assert.deepEqual([snapshot(store).session?.signed_in, snapshot(store).ended], [false, true]);
  assert.equal(snapshot(store).session?.csrf_token, 'pre-token');
  assert.equal(armed.size, 0);
});

test('a refresh that started before a sign-out cannot restore the signed-in view', async () => {
  const late = deferred();
  const {fetch} = fakeFetch({json: signedIn('s1')}, {gate: late.promise, json: signedIn('s1')}, {json: {signed_in: false, principal: null}}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const refreshing = store.refresh();
  await store.signOut();
  assert.equal(snapshot(store).session?.signed_in, false);
  late.resolve();
  await refreshing;
  assert.equal(snapshot(store).session?.signed_in, false, 'the earlier refresh result is ignored');
});

test('a successful sign-out clears the signed-in view even when the following session read fails', async () => {
  const {fetch} = fakeFetch({json: signedIn('s1')}, {json: {signed_in: false, principal: null}}, refused(503, 'internal'));
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const before = snapshot(store).generation;
  await store.signOut();
  const state = snapshot(store);
  assert.equal(state.session?.signed_in, false);
  assert.equal(state.ended, false);
  assert.ok(state.generation > before);
});

test('sign-out retries once with the fresh token after csrf_invalid', async () => {
  const {fetch, calls} = fakeFetch(
    {json: signedIn('old')},
    refused(403, 'csrf_invalid'),
    {json: signedIn('new')},
    {json: {signed_in: false, principal: null}},
    {json: signedOut},
  );
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  await store.signOut();
  const deletes = calls.filter(call => call.method === 'DELETE');
  assert.deepEqual(
    deletes.map(call => call.headers['X-CSRF-Token']),
    ['old', 'new'],
  );
  assert.equal(snapshot(store).session?.signed_in, false);
  assert.equal(snapshot(store).ended, false);
});

test('the sign-in CSRF retry does not take over a session signed in elsewhere without saying so', async () => {
  const other = {...signedIn('elsewhere'), principal: {...principal, display_name: 'Someone else'}};
  const {fetch, calls} = fakeFetch({json: signedOut}, refused(403, 'csrf_invalid'), {json: other});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  await assert.rejects(store.signIn('demo-reviewer', 'typed'), error => error instanceof ApiError && error.kind === 'already_signed_in');
  const state = snapshot(store);
  assert.equal(state.session?.signed_in, false, 'the typed login was not checked, so this tab is not signed in as it');
  assert.equal(state.elsewhere?.principal?.display_name, 'Someone else');
  assert.equal(calls.filter(call => call.method === 'POST').length, 1);
  store.adoptElsewhere();
  assert.equal(snapshot(store).session?.principal?.display_name, 'Someone else');
});

test('each change of signed-in session starts a new generation', async () => {
  const {fetch} = fakeFetch({json: signedOut}, {json: signedIn('a')}, {json: signedIn('b')});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const g0 = snapshot(store).generation;
  await store.signIn('demo-reviewer', 'p');
  const g1 = snapshot(store).generation;
  await store.refresh();
  const g2 = snapshot(store).generation;
  assert.ok(g1 > g0 && g2 > g1, `${g0} ${g1} ${g2}`);
});
