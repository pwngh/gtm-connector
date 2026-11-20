// The browser's client for the Go server's /api/v1. Same origin only: every
// request sends the session cookie, and every write carries the CSRF token the
// server handed out. Refusals become ApiError with a kind the pages show, so a
// refusal is never read as an empty result.

export const sessionPath = '/api/v1/session';
export const csrfHeader = 'X-CSRF-Token';

// The connector demo's account for this renewal.
export const demoAccount = 'acc-003';

const kinds = {
  not_permitted: 'not_permitted',
  sign_in_required: 'signed_out',
  sign_in_refused: 'sign_in_refused',
  csrf_invalid: 'csrf',
  origin_required: 'origin',
  origin_refused: 'origin',
  untrusted_host: 'host',
  not_found: 'not_found',
};

export class ApiError extends Error {
  constructor({status = 0, kind = 'unexpected', code = null, message = ''} = {}) {
    super(message || kind);
    this.name = 'ApiError';
    this.status = status;
    this.kind = kind;
    this.code = code;
  }
}

// What a refused or failed response means. The server's code decides; the
// status only covers responses without one.
export function refusal(status, body) {
  const error = body && typeof body === 'object' && body.error && typeof body.error === 'object' ? body.error : null;
  const code = typeof error?.code === 'string' ? error.code : null;
  const message = typeof error?.message === 'string' ? error.message : '';
  const kind = kinds[code] || (status >= 500 ? 'server' : 'unexpected');
  return new ApiError({status, kind, code, message});
}

async function readJson(response) {
  const type = response.headers?.get?.('Content-Type') || '';
  if (!/^application\/json\b/i.test(type)) return null;
  try {return await response.json();} catch {return null;}
}

export function isSessionView(value) {
  return Boolean(value) && typeof value === 'object' && typeof value.signed_in === 'boolean'
    && (value.principal === null || typeof value.principal === 'object');
}

const segment = value => {
  if (typeof value !== 'string' || !value) throw new ApiError({kind: 'not_found'});
  return encodeURIComponent(value);
};

export function createApiClient(fetchImpl = (...args) => globalThis.fetch(...args)) {
  async function request(method, path, {csrf, body} = {}) {
    const headers = {Accept: 'application/json'};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (csrf) headers[csrfHeader] = csrf;
    let response;
    try {
      response = await fetchImpl(path, {method, headers, credentials: 'same-origin', cache: 'no-store',
        body: body === undefined ? undefined : JSON.stringify(body)});
    } catch {
      throw new ApiError({kind: 'unreachable'});
    }
    const json = await readJson(response);
    if (!response.ok) throw refusal(response.status, json);
    if (json === null) throw new ApiError({status: response.status, kind: 'unexpected'});
    return json;
  }
  async function session() {
    const view = await request('GET', sessionPath);
    if (!isSessionView(view)) throw new ApiError({status: 200, kind: 'unexpected'});
    return view;
  }
  return {
    // Which app this is: served by the Go server, which answers in JSON even
    // when it refuses, or a static build, which has no API and answers with
    // HTML or a plain 404. A request that gets no answer at all is neither.
    async probe() {
      let response;
      try {
        response = await fetchImpl(sessionPath, {method: 'GET', headers: {Accept: 'application/json'}, credentials: 'same-origin', cache: 'no-store'});
      } catch {
        return {mode: 'unreachable', session: null, error: new ApiError({kind: 'unreachable'})};
      }
      const json = await readJson(response);
      if (json === null) return {mode: 'walkthrough', session: null, error: null};
      if (response.ok && isSessionView(json)) return {mode: 'api', session: json, error: null};
      return {mode: 'api', session: null, error: response.ok ? new ApiError({status: response.status, kind: 'unexpected'}) : refusal(response.status, json)};
    },
    session,
    signIn(csrf, login, password) {
      return request('POST', sessionPath, {csrf, body: {login, password}}).then(view => {
        if (!isSessionView(view) || !view.signed_in) throw new ApiError({status: 200, kind: 'unexpected'});
        return view;
      });
    },
    signOut(csrf) {return request('DELETE', sessionPath, {csrf});},
    accountCards(account) {return request('GET', `/api/v1/accounts/${segment(account)}/cards`);},
    card(id) {return request('GET', `/api/v1/cards/${segment(id)}`);},
    unit(card, n) {
      if (!Number.isInteger(n) || n < 1) return Promise.reject(new ApiError({kind: 'not_found'}));
      return request('GET', `/api/v1/cards/${segment(card)}/units/${n}`);
    },
    receipt(id) {return request('GET', `/api/v1/receipts/${segment(id)}`);},
  };
}

// A time as the server sent it, in UTC to the second. A time without an
// offset is a literal and is shown as written.
export function formatUtc(value) {
  if (typeof value !== 'string' || !value) return 'Not recorded';
  if (!/(?:Z|[+-]\d\d:?\d\d)$/i.test(value) || !Number.isFinite(Date.parse(value))) return `${value} · no time zone recorded`;
  const date = new Date(value);
  const day = date.toLocaleDateString('en-GB', {day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC'});
  return `${day}, ${date.toISOString().slice(11, 19)} UTC`;
}

// A session past its expiry is over, whatever the cookie still says.
export function sessionExpired(session, now = Date.now()) {
  if (!session?.signed_in) return false;
  const expires = Date.parse(session.expires_at || '');
  return Number.isFinite(expires) && expires <= now;
}

// Why a sentence unit is not on the card, in the connector's terms.
export const notShownReasons = {
  cut: 'Cut at a pause, a change of speaker or the edge of an excerpt',
  later: 'Dated after the card’s day',
  long: 'Longer than a card sentence',
  more: 'More sentences than the card has room for',
};

export const gapDispositions = {not_read: 'Not read', read_not_shown: 'Read, not shown', note: 'Note'};

// The units' own fields, read defensively: a body is the sealed unit as stored.
export function unitFacts(unit) {
  const body = unit?.body && typeof unit.body === 'object' ? unit.body : {};
  const text = typeof body.text === 'string' ? body.text : null;
  const shown = typeof unit?.shown === 'string' && unit.shown ? unit.shown : typeof body.shown === 'string' ? body.shown : null;
  return {
    n: unit?.n, kind: unit?.kind, recordRef: unit?.record_ref, tool: unit?.tool,
    source: typeof body.source === 'string' ? body.source : null,
    at: typeof body.at === 'string' ? body.at : null,
    text, partial: body.partial === true, shown,
    notShown: shown && shown !== 'yes' ? (notShownReasons[shown] || `Reason ${shown}`) : null,
    speaker: typeof body.speaker === 'string' ? body.speaker : null,
    role: typeof body.role === 'string' ? body.role : null,
    basis: typeof body.basis === 'string' ? body.basis : null,
    receiptId: unit?.receipt_id || null,
  };
}
