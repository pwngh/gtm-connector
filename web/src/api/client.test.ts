import assert from 'node:assert/strict';
import test from 'node:test';
import {ApiError, createApiClient, formatUtc, refusal, unitFacts} from './client.ts';
import {fakeFetch, refused, signedIn, signedOut} from './fakeFetch.ts';

const kindOf = (kind: string) => (error: unknown) => error instanceof ApiError && error.kind === kind;

test('the probe picks the API only when the server answers in JSON, even with a refusal', async () => {
  let {fetch, calls} = fakeFetch({json: signedOut});
  assert.deepEqual(await createApiClient(fetch).probe(), {mode: 'api', session: signedOut, error: null});
  assert.equal(calls[0]?.path, '/api/v1/session');
  assert.equal(calls[0]?.credentials, 'same-origin');
  assert.equal(calls[0]?.headers.Accept, 'application/json');

  for (const answer of [
    {status: 200, text: '<!doctype html>'},
    {status: 404, text: 'Not found'},
  ]) {
    ({fetch} = fakeFetch(answer));
    assert.equal((await createApiClient(fetch).probe()).mode, 'static');
  }
  // A server that answers and refuses is still the server, never a static host.
  ({fetch} = fakeFetch(refused(503, 'internal')));
  const failed = await createApiClient(fetch).probe();
  assert.equal(failed.mode, 'api');
  assert.equal(failed.error?.kind, 'server');
  ({fetch} = fakeFetch(refused(421, 'untrusted_host')));
  assert.equal((await createApiClient(fetch).probe()).error?.kind, 'host');
  // No answer at all is neither mode.
  ({fetch} = fakeFetch(new TypeError('Failed to fetch')));
  assert.equal((await createApiClient(fetch).probe()).mode, 'unreachable');
});

test('sign-in and sign-out send the CSRF header, JSON and same-origin credentials', async () => {
  const {fetch, calls} = fakeFetch({json: signedIn()}, {json: {signed_in: false, principal: null}});
  const api = createApiClient(fetch);
  const session = await api.signIn('pre-token', 'demo-reviewer', 'secret');
  assert.equal(session.principal?.demo, true);
  assert.equal(calls[0]?.method, 'POST');
  assert.equal(calls[0]?.headers['X-CSRF-Token'], 'pre-token');
  assert.equal(calls[0]?.headers['Content-Type'], 'application/json');
  assert.equal(calls[0]?.credentials, 'same-origin');
  assert.deepEqual(JSON.parse(String(calls[0]?.body)), {login: 'demo-reviewer', password: 'secret'});
  await api.signOut('session-token');
  assert.equal(calls[1]?.method, 'DELETE');
  assert.equal(calls[1]?.headers['X-CSRF-Token'], 'session-token');
  assert.equal(calls[1]?.body, undefined);
});

test('reads carry no CSRF header and escape their path segments', async () => {
  const {fetch, calls} = fakeFetch({json: {accounts: []}}, {json: {account: 'a/b', cards: []}}, {json: {}}, {json: {}});
  const api = createApiClient(fetch);
  await api.accounts();
  await api.accountCards('a/b');
  await api.card('card 1');
  await api.unit('c-1', 5);
  assert.deepEqual(
    calls.map(call => call.path),
    ['/api/v1/accounts', '/api/v1/accounts/a%2Fb/cards', '/api/v1/cards/card%201', '/api/v1/cards/c-1/units/5'],
  );
  for (const call of calls) {
    assert.equal(call.method, 'GET');
    assert.equal(call.credentials, 'same-origin');
    assert.equal(call.headers['X-CSRF-Token'], undefined);
  }
  await assert.rejects(api.unit('c-1', 0), kindOf('not_found'));
});

test('refusals map by the server code, never to an empty result', async () => {
  const cases: [number, string, string][] = [
    [403, 'not_permitted', 'not_permitted'],
    [401, 'sign_in_required', 'signed_out'],
    [401, 'sign_in_refused', 'sign_in_refused'],
    [403, 'csrf_invalid', 'csrf'],
    [403, 'origin_refused', 'origin'],
    [404, 'not_found', 'not_found'],
    [500, 'internal', 'server'],
  ];
  for (const [status, code, kind] of cases) {
    const {fetch} = fakeFetch(refused(status, code));
    await assert.rejects(
      createApiClient(fetch).accountCards('acc-001'),
      error => error instanceof ApiError && error.kind === kind && error.code === code && error.status === status,
    );
  }
  const {fetch} = fakeFetch({status: 200, text: '<html>'}, new TypeError('offline'));
  const api = createApiClient(fetch);
  await assert.rejects(api.card('c'), kindOf('unexpected'));
  await assert.rejects(api.card('c'), kindOf('unreachable'));
  assert.equal(refusal(502, null).kind, 'server');
  assert.equal(refusal(400, {error: {code: 'invalid_json'}}).kind, 'unexpected');
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
  assert.match(cut.notShown ?? '', /^Cut at a pause/);
  assert.equal(unitFacts({n: 1, kind: 'sentence', shown: 'yes', body: {text: 'Yes.'}}).notShown, null);
  assert.equal(unitFacts({n: 2, kind: 'sentence', shown: 'odd', body: {}}).notShown, 'Reason odd');
  assert.equal(unitFacts({n: 3, kind: 'record', body: null}).text, null);
});

// Review round: the probe, deadlines and redirects.
test('the probe takes only a 404, or a 2xx text/html page, for a static host', async () => {
  for (const answer of [
    {status: 404, text: 'Not found', type: null},
    {status: 404, text: 'Not found', type: 'text/plain'},
    {status: 200, text: '<!doctype html>', type: 'text/html; charset=utf-8'},
  ]) {
    assert.equal((await createApiClient(fakeFetch(answer).fetch).probe()).mode, 'static', JSON.stringify(answer));
  }
  const cases: [Parameters<typeof fakeFetch>[0], string][] = [
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
    assert.equal(result.mode, 'api', `${JSON.stringify(answer)} must not be a static host`);
    assert.equal(result.session, null);
    assert.equal(result.error?.kind, kind, JSON.stringify(answer));
  }
});

test('every API request has a deadline and refuses a redirect instead of following it', async () => {
  const {fetch, calls} = fakeFetch({hang: true}, {hang: true}, {redirect: true});
  const api = createApiClient(fetch, {timeoutMs: 20});
  const probe = await api.probe();
  assert.deepEqual([probe.mode, probe.error?.kind], ['unreachable', 'unreachable']);
  await assert.rejects(api.accounts(), kindOf('unreachable'));
  await assert.rejects(api.signOut('t'), kindOf('redirect'));
  for (const call of calls) {
    assert.equal(call.redirect, 'manual');
    assert.ok(call.signal, 'each request carries an abort signal');
  }
});

test('a declared-JSON body that fails to read is unreachable, never an empty result', async () => {
  const {fetch} = fakeFetch({status: 200, broken: true}, {status: 403, broken: true}, {status: 200, json: null});
  const api = createApiClient(fetch);
  await assert.rejects(api.accounts(), kindOf('unreachable'));
  await assert.rejects(api.card('c'), kindOf('unreachable'));
  await assert.rejects(api.accounts(), kindOf('unexpected'));
});

test('accounts, card and unit reject a 403 as not permitted, as account cards do', async () => {
  const {fetch} = fakeFetch(refused(403, 'not_permitted'), refused(403, 'not_permitted'), refused(403, 'not_permitted'), refused(403, 'not_permitted'));
  const api = createApiClient(fetch);
  for (const read of [() => api.accounts(), () => api.card('c-1'), () => api.unit('c-1', 2), () => api.receipt('r-1')]) {
    await assert.rejects(read(), error => error instanceof ApiError && error.kind === 'not_permitted' && error.code === 'not_permitted');
  }
});

test('a read whose caller stops waiting is aborted, as its own kind, and an aborted signal sends nothing', async () => {
  const {fetch, calls} = fakeFetch({hang: true});
  const api = createApiClient(fetch);
  const stop = new AbortController();
  const read = api.accounts({signal: stop.signal});
  stop.abort();
  await assert.rejects(read, kindOf('aborted'));
  await assert.rejects(api.card('c-1', {signal: stop.signal}), kindOf('aborted'));
  assert.equal(calls.length, 1);
});
