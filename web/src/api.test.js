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
    // A request that never answers: it ends only when its signal aborts.
    if (answer.hang) return new Promise((resolve, reject) => init.signal?.addEventListener('abort', () => reject(init.signal.reason ?? new DOMException('Aborted', 'AbortError'))));
    if (answer.redirect) return {status: 0, ok: false, type: 'opaqueredirect', redirected: false, headers: new Headers(), json: async () => {throw new SyntaxError('opaque');}};
    if (answer.gate) await answer.gate;
    const {status = 200, json, text, broken = false, type = json === undefined && !broken ? 'text/html' : 'application/json'} = answer;
    return {status, ok: status >= 200 && status < 300, type: 'basic', headers: new Headers(type ? {'Content-Type': type} : {}),
      json: async () => {if (broken) throw new SyntaxError('Unexpected end of JSON input'); return json;}, text: async () => text ?? JSON.stringify(json)};
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
  const {fetch, calls} = fakeFetch({json: {accounts: []}}, {json: {account: 'a/b', cards: []}}, {json: {}}, {json: {}});
  const api = createApiClient(fetch);
  await api.accounts();
  await api.accountCards('a/b');
  await api.card('card 1');
  await api.unit('c-1', 5);
  assert.deepEqual(calls.map(call => call.path), ['/api/v1/accounts', '/api/v1/accounts/a%2Fb/cards', '/api/v1/cards/card%201', '/api/v1/cards/c-1/units/5']);
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
  // At the session's stated end the store signs out, then asks the server for a sign-in token.
  assert.equal(timers.at(-1).ms, 3600 * 1000);
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

// Review round: the probe, deadlines and redirects.
test('the probe picks walkthrough only for a static host: a 404, or a 2xx text/html page', async () => {
  for (const answer of [{status: 404, text: 'Not found', type: null}, {status: 404, text: 'Not found', type: 'text/plain'}, {status: 200, text: '<!doctype html>', type: 'text/html; charset=utf-8'}]) {
    assert.equal((await createApiClient(fakeFetch(answer).fetch).probe()).mode, 'walkthrough', JSON.stringify(answer));
  }
  const cases = [
    [{status: 431, text: 'Request Header Fields Too Large', type: 'text/plain; charset=utf-8'}, 'unexpected'],
    [{status: 431, text: '<html>too large</html>', type: 'text/html'}, 'unexpected'],
    [{status: 502, text: '<html>Bad gateway</html>', type: 'text/html'}, 'server'],
    [{status: 200, text: 'plain words', type: 'text/plain'}, 'unexpected'],
    [{status: 200, broken: true}, 'unreachable'],
    [{status: 200, json: null}, 'unexpected'],
    [{redirect: true}, 'redirect'],
  ];
  for (const [answer, kind] of cases) {
    const result = await createApiClient(fakeFetch(answer).fetch).probe();
    assert.equal(result.mode, 'api', `${JSON.stringify(answer)} must not be walkthrough`);
    assert.equal(result.session, null);
    assert.equal(result.error?.kind, kind, JSON.stringify(answer));
  }
});

test('every API request has a deadline and refuses a redirect instead of following it', async () => {
  const {fetch, calls} = fakeFetch({hang: true}, {hang: true}, {redirect: true});
  const api = createApiClient(fetch, {timeoutMs: 20});
  const probe = await api.probe();
  assert.deepEqual([probe.mode, probe.error?.kind], ['unreachable', 'unreachable']);
  await assert.rejects(api.accounts(), error => error.kind === 'unreachable');
  await assert.rejects(api.signOut('t'), error => error.kind === 'redirect');
  for (const call of calls) {
    assert.equal(call.redirect, 'manual');
    assert.ok(call.signal, 'each request carries an abort signal');
  }
});

test('a declared-JSON body that fails to read is unreachable, never an empty result', async () => {
  const {fetch} = fakeFetch({status: 200, broken: true}, {status: 403, broken: true}, {status: 200, json: null});
  const api = createApiClient(fetch);
  await assert.rejects(api.accounts(), error => error.kind === 'unreachable');
  await assert.rejects(api.card('c'), error => error.kind === 'unreachable');
  await assert.rejects(api.accounts(), error => error.kind === 'unexpected');
});

test('accounts, card and unit reject a 403 as not permitted, as account cards do', async () => {
  const {fetch} = fakeFetch(refused(403, 'not_permitted'), refused(403, 'not_permitted'), refused(403, 'not_permitted'), refused(403, 'not_permitted'));
  const api = createApiClient(fetch);
  for (const read of [() => api.accounts(), () => api.card('c-1'), () => api.unit('c-1', 2), () => api.receipt('r-1')]) {
    await assert.rejects(read(), error => error.kind === 'not_permitted' && error.code === 'not_permitted');
  }
});

// Review round: the session store.
const quiet = {setTimer: () => 0, clearTimer() {}};
function deferred() {let resolve, reject; const promise = new Promise((a, b) => {resolve = a; reject = b;}); return {promise, resolve, reject};}

test('at expiry the view is signed out at once; a failed refresh stays signed out and nothing polls', async () => {
  const timers = [];
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T10:00:00Z')}, refused(503, 'internal'));
  const store = createServerStore(createApiClient(fetch), {now: () => Date.parse('2027-01-15T09:00:00Z'), setTimer: (fn, ms) => {timers.push({fn, ms}); return timers.length;}, clearTimer() {}});
  await store.start();
  const before = store.getSnapshot().generation;
  assert.equal(timers.length, 1);
  assert.equal(timers[0].ms, 3600 * 1000);
  await timers[0].fn();
  const state = store.getSnapshot();
  assert.equal(state.session?.signed_in, false, 'an expired session never shows as signed in');
  assert.equal(state.ended, true);
  assert.equal(state.error?.kind, 'server');
  assert.ok(state.generation > before, 'the expiry starts a new generation, so earlier reads are dropped');
  assert.equal(timers.length, 1, 'no timer is armed again after a failed refresh');
});

test('a session already past its expiry when it arrives is signed out at once, and no timer stays armed', async () => {
  const armed = new Set();
  let next = 0;
  const {fetch} = fakeFetch({json: signedIn('s1', '2027-01-15T08:00:00Z')}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), {now: () => Date.parse('2027-01-15T09:00:00Z'), setTimer: () => {armed.add(++next); return next;}, clearTimer: id => armed.delete(id)});
  await store.start();
  assert.deepEqual([store.getSnapshot().session.signed_in, store.getSnapshot().ended], [false, true]);
  assert.equal(store.getSnapshot().session.csrf_token, 'pre-token');
  assert.equal(armed.size, 0);
});

test('a refresh that started before a sign-out cannot restore the signed-in view', async () => {
  const late = deferred();
  const {fetch} = fakeFetch({json: signedIn('s1')}, {gate: late.promise, json: signedIn('s1')}, {json: {signed_in: false, principal: null}}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const refreshing = store.refresh();
  await store.signOut();
  assert.equal(store.getSnapshot().session.signed_in, false);
  late.resolve();
  await refreshing;
  assert.equal(store.getSnapshot().session.signed_in, false, 'the earlier refresh result is ignored');
});

test('a successful sign-out clears the signed-in view even when the following session read fails', async () => {
  const {fetch} = fakeFetch({json: signedIn('s1')}, {json: {signed_in: false, principal: null}}, refused(503, 'internal'));
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const before = store.getSnapshot().generation;
  await store.signOut();
  const state = store.getSnapshot();
  assert.equal(state.session?.signed_in, false);
  assert.equal(state.ended, false);
  assert.ok(state.generation > before);
});

test('sign-out retries once with the fresh token after csrf_invalid', async () => {
  const {fetch, calls} = fakeFetch({json: signedIn('old')}, refused(403, 'csrf_invalid'), {json: signedIn('new')}, {json: {signed_in: false, principal: null}}, {json: signedOut});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  await store.signOut();
  const deletes = calls.filter(call => call.method === 'DELETE');
  assert.deepEqual(deletes.map(call => call.headers['X-CSRF-Token']), ['old', 'new']);
  assert.equal(store.getSnapshot().session.signed_in, false);
  assert.equal(store.getSnapshot().ended, false);
});

test('the sign-in CSRF retry does not take over a session signed in elsewhere without saying so', async () => {
  const other = {...signedIn('elsewhere'), principal: {...principal, display_name: 'Someone else'}};
  const {fetch, calls} = fakeFetch({json: signedOut}, refused(403, 'csrf_invalid'), {json: other});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  await assert.rejects(store.signIn('demo-reviewer', 'typed'), error => error.kind === 'already_signed_in');
  const state = store.getSnapshot();
  assert.equal(state.session.signed_in, false, 'the typed login was not checked, so this tab is not signed in as it');
  assert.equal(state.elsewhere?.principal?.display_name, 'Someone else');
  assert.equal(calls.filter(call => call.method === 'POST').length, 1);
  store.adoptElsewhere();
  assert.equal(store.getSnapshot().session.principal.display_name, 'Someone else');
});

test('each change of signed-in session starts a new generation', async () => {
  const {fetch} = fakeFetch({json: signedOut}, {json: signedIn('a')}, {json: signedIn('b')});
  const store = createServerStore(createApiClient(fetch), quiet);
  await store.start();
  const g0 = store.getSnapshot().generation;
  await store.signIn('demo-reviewer', 'p');
  const g1 = store.getSnapshot().generation;
  await store.refresh();
  const g2 = store.getSnapshot().generation;
  assert.ok(g1 > g0 && g2 > g1, `${g0} ${g1} ${g2}`);
});
