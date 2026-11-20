import test from 'node:test';
import assert from 'node:assert/strict';
import {ApiError, createApiClient, formatUtc, refusal, sessionExpired, unitFacts} from './api.js';
import {createServerStore} from './serverSession.js';

// A fake fetch: answers from a queue of responses and records every request.
function fakeFetch(...answers) {
  const calls = [];
  const fetch = async (path, init = {}) => {
    calls.push({path, ...init});
    const answer = answers.shift();
    if (!answer) throw new Error('unexpected request ' + path);
    if (answer instanceof Error) throw answer;
    const {status = 200, json, text, type = json === undefined ? 'text/html' : 'application/json'} = answer;
    return {status, ok: status >= 200 && status < 300, headers: new Headers({'Content-Type': type}),
      json: async () => json, text: async () => text ?? JSON.stringify(json)};
  };
  return {fetch, calls};
}
const signedOut = {signed_in: false, principal: null, csrf_token: 'pre-token', expires_at: '2027-01-15T16:00:00Z'};
const principal = {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
const signedIn = (token = 'session-token', expires = '2027-01-15T23:00:00Z') => ({signed_in: true, principal, csrf_token: token, expires_at: expires});
const refused = (status, code) => ({status, json: {error: {code, message: 'Refused.'}}});

test('the probe picks the API only when the server answers in JSON, even with a refusal', async () => {
  let {fetch, calls} = fakeFetch({json: signedOut});
  assert.deepEqual(await createApiClient(fetch).probe(), {mode: 'api', session: signedOut, error: null});
  assert.equal(calls[0].path, '/api/v1/session');
  assert.equal(calls[0].credentials, 'same-origin');
  assert.equal(calls[0].headers.Accept, 'application/json');

  for (const answer of [{status: 200, text: '<!doctype html>'}, {status: 404, text: 'Not found'}]) {
    ({fetch} = fakeFetch(answer));
    assert.equal((await createApiClient(fetch).probe()).mode, 'walkthrough');
  }
  // A server that answers and refuses stays the source: never walkthrough data.
  ({fetch} = fakeFetch(refused(503, 'internal')));
  const failed = await createApiClient(fetch).probe();
  assert.equal(failed.mode, 'api');
  assert.equal(failed.error.kind, 'server');
  ({fetch} = fakeFetch(refused(421, 'untrusted_host')));
  assert.equal((await createApiClient(fetch).probe()).error.kind, 'host');
  // No answer at all is neither mode.
  ({fetch} = fakeFetch(new TypeError('Failed to fetch')));
  assert.equal((await createApiClient(fetch).probe()).mode, 'unreachable');
});

test('sign-in and sign-out send the CSRF header, JSON and same-origin credentials', async () => {
  const {fetch, calls} = fakeFetch({json: signedIn()}, {json: {signed_in: false, principal: null}});
  const api = createApiClient(fetch);
  const session = await api.signIn('pre-token', 'demo-reviewer', 'secret');
  assert.equal(session.principal.demo, true);
  assert.equal(calls[0].method, 'POST');
  assert.equal(calls[0].headers['X-CSRF-Token'], 'pre-token');
  assert.equal(calls[0].headers['Content-Type'], 'application/json');
  assert.equal(calls[0].credentials, 'same-origin');
  assert.deepEqual(JSON.parse(calls[0].body), {login: 'demo-reviewer', password: 'secret'});
  await api.signOut('session-token');
  assert.equal(calls[1].method, 'DELETE');
  assert.equal(calls[1].headers['X-CSRF-Token'], 'session-token');
  assert.equal(calls[1].body, undefined);
});

test('reads carry no CSRF header and escape their path segments', async () => {
  const {fetch, calls} = fakeFetch({json: {account: 'a/b', cards: []}}, {json: {}}, {json: {}});
  const api = createApiClient(fetch);
  await api.accountCards('a/b');
  await api.card('card 1');
  await api.unit('c-1', 5);
  assert.deepEqual(calls.map(call => call.path), ['/api/v1/accounts/a%2Fb/cards', '/api/v1/cards/card%201', '/api/v1/cards/c-1/units/5']);
  for (const call of calls) {
    assert.equal(call.method, 'GET');
    assert.equal(call.credentials, 'same-origin');
    assert.equal(call.headers['X-CSRF-Token'], undefined);
  }
  await assert.rejects(api.unit('c-1', 0), error => error.kind === 'not_found');
});

test('refusals map by the server code, never to an empty result', async () => {
  const cases = [[403, 'not_permitted', 'not_permitted'], [401, 'sign_in_required', 'signed_out'], [401, 'sign_in_refused', 'sign_in_refused'],
    [403, 'csrf_invalid', 'csrf'], [403, 'origin_refused', 'origin'], [404, 'not_found', 'not_found'], [500, 'internal', 'server']];
  for (const [status, code, kind] of cases) {
    const {fetch} = fakeFetch(refused(status, code));
    await assert.rejects(createApiClient(fetch).accountCards('acc-001'), error => error instanceof ApiError && error.kind === kind && error.code === code && error.status === status);
  }
  const {fetch} = fakeFetch({status: 200, text: '<html>'}, new TypeError('offline'));
  const api = createApiClient(fetch);
  await assert.rejects(api.card('c'), error => error.kind === 'unexpected');
  await assert.rejects(api.card('c'), error => error.kind === 'unreachable');
  assert.equal(refusal(502, null).kind, 'server');
  assert.equal(refusal(400, {error: {code: 'invalid_json'}}).kind, 'unexpected');
});

test('expiry: a session past its time, or a read refused for sign-in, shows as signed out', async () => {
  assert.equal(sessionExpired(signedIn('t', '2027-01-15T10:00:00Z'), Date.parse('2027-01-15T10:00:00Z')), true);
  assert.equal(sessionExpired(signedIn('t', '2027-01-15T10:00:00Z'), Date.parse('2027-01-15T09:59:59Z')), false);
  assert.equal(sessionExpired(signedOut, Date.parse('2030-01-01T00:00:00Z')), false);

  const timers = [];
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T10:00:00Z')}, {json: signedOut}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), {now: () => Date.parse('2027-01-15T09:00:00Z'), setTimer: (fn, ms) => {timers.push({fn, ms}); return timers.length;}, clearTimer() {}});
  await store.start();
  assert.equal(store.getSnapshot().session.signed_in, true);
  // The store asks the server again just after the session's stated end.
  assert.equal(timers.at(-1).ms, 3600 * 1000 + 1000);
  await timers.at(-1).fn();
  assert.equal(store.getSnapshot().session.signed_in, false);
  assert.equal(store.getSnapshot().ended, true);
  assert.equal(store.getSnapshot().session.csrf_token, 'pre-token');
  // A revoked session found by a refused read is the same.
  const revoked = createServerStore(createApiClient(fakeFetch({json: signedIn()}, {json: signedOut}).fetch), {setTimer: () => 0, clearTimer() {}});
  await revoked.start();
  await revoked.expire();
  assert.deepEqual([revoked.getSnapshot().session.signed_in, revoked.getSnapshot().ended], [false, true]);
});

test('the store signs in with the pre-session token, retries once with a fresh one, and signs out', async () => {
  const {fetch, calls} = fakeFetch({json: signedOut}, refused(403, 'csrf_invalid'), {json: {...signedOut, csrf_token: 'fresh'}}, {json: signedIn('s2')},
    {json: {signed_in: false, principal: null}}, {json: {...signedOut, csrf_token: 'after'}});
  const store = createServerStore(createApiClient(fetch), {setTimer: () => 0, clearTimer() {}});
  await store.start();
  await store.signIn('demo-reviewer', 'secret');
  assert.deepEqual(calls.filter(call => call.method === 'POST').map(call => call.headers['X-CSRF-Token']), ['pre-token', 'fresh']);
  assert.equal(store.getSnapshot().session.principal.display_name, 'Demo reviewer');
  await store.signOut();
  assert.equal(calls.at(-2).headers['X-CSRF-Token'], 's2');
  assert.deepEqual([store.getSnapshot().session.signed_in, store.getSnapshot().ended, store.getSnapshot().session.csrf_token], [false, false, 'after']);

  const wrong = createServerStore(createApiClient(fakeFetch({json: signedOut}, refused(401, 'sign_in_refused')).fetch), {setTimer: () => 0, clearTimer() {}});
  await wrong.start();
  await assert.rejects(wrong.signIn('demo-reviewer', 'nope'), error => error.kind === 'sign_in_refused');
  assert.equal(wrong.getSnapshot().session.signed_in, false);
});

test('times show in UTC to the second, and a time without an offset is a literal', () => {
  assert.equal(formatUtc('2027-01-15T15:00:04.123456Z'), '15 Jan 2027, 15:00:04 UTC');
  assert.equal(formatUtc('2027-01-15T09:00:04-06:00'), '15 Jan 2027, 15:00:04 UTC');
  assert.equal(formatUtc('2027-01-12 15:00'), '2027-01-12 15:00 · no time zone recorded');
  assert.equal(formatUtc(null), 'Not recorded');
});

test('unit facts keep cut and not-shown units labelled and read the body defensively', () => {
  const cut = unitFacts({n: 4, kind: 'sentence', shown: 'cut', body: {text: 'We will', partial: true, role: 'seller'}});
  assert.equal(cut.partial, true);
  assert.match(cut.notShown, /^Cut at a pause/);
  assert.equal(unitFacts({n: 1, kind: 'sentence', shown: 'yes', body: {text: 'Yes.'}}).notShown, null);
  assert.equal(unitFacts({n: 2, kind: 'sentence', shown: 'odd', body: {}}).notShown, 'Reason odd');
  assert.equal(unitFacts({n: 3, kind: 'record', body: null}).text, null);
});
