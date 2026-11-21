// The browser's client for the Go server's /api/v1. Same origin only: every
// request sends the session cookie, and every write carries the CSRF token the
// server handed out. Refusals become ApiError with a kind the pages show, so a
// refusal is never read as an empty result.

import type {AccountCards, AccountList, CardDetail, Json, ReceiptView, SessionView, UnitBody, UnitView} from './types.ts';

export const sessionPath = '/api/v1/session';
export const csrfHeader = 'X-CSRF-Token';

// The connector demo's account for this renewal: the default choice when the
// signed-in principal holds a grant on it.
export const demoAccount = 'acc-003';

/** What went wrong with a request, as the pages tell it. */
export type ErrorKind =
  | 'not_permitted'
  | 'signed_out'
  | 'sign_in_refused'
  | 'csrf'
  | 'origin'
  | 'host'
  | 'not_found'
  | 'server'
  | 'unexpected'
  | 'unreachable'
  | 'redirect'
  | 'mismatch'
  | 'already_signed_in'
  | 'aborted';

const kinds: Record<string, ErrorKind> = {
  not_permitted: 'not_permitted',
  sign_in_required: 'signed_out',
  sign_in_refused: 'sign_in_refused',
  csrf_invalid: 'csrf',
  origin_required: 'origin',
  origin_refused: 'origin',
  untrusted_host: 'host',
  not_found: 'not_found',
};

interface ApiErrorInit {
  status?: number;
  kind?: ErrorKind;
  code?: string | null;
  message?: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly kind: ErrorKind;
  readonly code: string | null;

  constructor({status = 0, kind = 'unexpected', code = null, message = ''}: ApiErrorInit = {}) {
    super(message || kind);
    this.name = 'ApiError';
    this.status = status;
    this.kind = kind;
    this.code = code;
  }
}

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null;

// What a refused or failed response means. The server's code decides; the
// status only covers responses without one.
export function refusal(status: number, body: unknown): ApiError {
  const error = isObject(body) && isObject(body.error) ? body.error : null;
  const code = typeof error?.code === 'string' ? error.code : null;
  const message = typeof error?.message === 'string' ? error.message : '';
  const kind = (code !== null ? kinds[code] : undefined) ?? (status >= 500 ? 'server' : 'unexpected');
  return new ApiError({status, kind, code, message});
}

type Body = {kind: 'json'; value: unknown} | {kind: 'other'; type: string} | {kind: 'broken'};

// What a response's body is: JSON that was read, a body that is not JSON, or
// a body declared JSON that could not be read (cut off, or malformed).
async function readBody(response: Response): Promise<Body> {
  const type = response.headers?.get?.('Content-Type') || '';
  if (!/^application\/json\b/i.test(type)) {
    return {kind: 'other', type};
  }
  try {
    return {kind: 'json', value: await response.json()};
  } catch {
    return {kind: 'broken'};
  }
}

export function isSessionView(value: unknown): value is SessionView {
  return isObject(value) && typeof value.signed_in === 'boolean' && (value.principal === null || isObject(value.principal));
}

const segment = (value: unknown): string => {
  if (typeof value !== 'string' || !value) {
    throw new ApiError({kind: 'not_found'});
  }
  return encodeURIComponent(value);
};

// How long any API request may take before it counts as unanswered.
export const requestDeadline = 10_000;

export type Fetch = (path: string, init: RequestInit) => Promise<Response>;

/** Lets a caller stop a read nobody waits for any more. */
export interface ReadOptions {
  signal?: AbortSignal;
}

export interface Probe {
  mode: 'api' | 'static' | 'unreachable';
  session: SessionView | null;
  error: ApiError | null;
}

export interface Api {
  probe(): Promise<Probe>;
  session(): Promise<SessionView>;
  signIn(csrf: string | null | undefined, login: string, password: string): Promise<SessionView>;
  signOut(csrf: string | null | undefined): Promise<unknown>;
  accounts(options?: ReadOptions): Promise<AccountList>;
  accountCards(account: string, options?: ReadOptions): Promise<AccountCards>;
  card(id: string, options?: ReadOptions): Promise<CardDetail>;
  unit(card: string, n: number, options?: ReadOptions): Promise<UnitView>;
  receipt(id: string, options?: ReadOptions): Promise<ReceiptView>;
}

interface SendOptions {
  csrf?: string | null | undefined;
  body?: Json | undefined;
  signal?: AbortSignal | undefined;
}

interface Sent {
  response: Response;
  body: Body | null;
  redirect: boolean;
}

const defaultFetch: Fetch = (path, init) => globalThis.fetch(path, init);

export function createApiClient(fetchImpl: Fetch = defaultFetch, {timeoutMs = requestDeadline} = {}): Api {
  // Every request has a deadline, and a redirect is refused rather than
  // followed, so the cookie and the CSRF token never go anywhere else. A
  // caller's signal stops it too, as "aborted", which no page shows.
  async function send(method: string, path: string, {csrf, body, signal}: SendOptions = {}): Promise<Sent> {
    const headers: Record<string, string> = {Accept: 'application/json'};
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (csrf) {
      headers[csrfHeader] = csrf;
    }
    const deadline = new AbortController();
    const timer = setTimeout(() => deadline.abort(), timeoutMs);
    const init: RequestInit = {
      method,
      headers,
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'manual',
      signal: signal ? AbortSignal.any([deadline.signal, signal]) : deadline.signal,
    };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
    }
    try {
      if (signal?.aborted) {
        throw new ApiError({kind: 'aborted'});
      }
      const response = await fetchImpl(path, init);
      if (response.type === 'opaqueredirect' || (response.status >= 300 && response.status < 400)) {
        return {response, body: null, redirect: true};
      }
      return {response, body: await readBody(response), redirect: false};
    } catch {
      throw new ApiError({kind: signal?.aborted ? 'aborted' : 'unreachable'});
    } finally {
      clearTimeout(timer);
    }
  }

  async function request<T>(method: string, path: string, options?: SendOptions): Promise<T> {
    const {response, body, redirect} = await send(method, path, options);
    if (redirect || body === null) {
      throw new ApiError({status: response.status, kind: 'redirect'});
    }
    if (body.kind === 'broken') {
      throw new ApiError({status: response.status, kind: 'unreachable'});
    }
    const json = body.kind === 'json' ? body.value : null;
    if (!response.ok) {
      throw refusal(response.status, json);
    }
    if (!isObject(json)) {
      throw new ApiError({status: response.status, kind: 'unexpected'});
    }
    return json as T;
  }

  async function session(): Promise<SessionView> {
    const view = await request<unknown>('GET', sessionPath);
    if (!isSessionView(view)) {
      throw new ApiError({status: 200, kind: 'unexpected'});
    }
    return view;
  }

  return {
    // Whether a server is behind the page. Only what a static host answers
    // means none: a 404, or a 2xx HTML page (its fallback for any path). The
    // Go server answers in JSON, so anything else, a refusal, an error page
    // in front of it or a body cut off, is the server's answer, shown as such.
    async probe() {
      let sent: Sent;
      try {
        sent = await send('GET', sessionPath);
      } catch (error) {
        return {mode: 'unreachable', session: null, error: error as ApiError};
      }
      const {response, body, redirect} = sent;
      if (redirect || body === null) {
        return {mode: 'api', session: null, error: new ApiError({status: response.status, kind: 'redirect'})};
      }
      if (body.kind === 'broken') {
        return {mode: 'api', session: null, error: new ApiError({status: response.status, kind: 'unreachable'})};
      }
      if (body.kind === 'other') {
        if (response.status === 404 || (response.ok && /^text\/html\b/i.test(body.type))) {
          return {mode: 'static', session: null, error: null};
        }
        return {mode: 'api', session: null, error: response.ok ? new ApiError({status: response.status, kind: 'unexpected'}) : refusal(response.status, null)};
      }
      if (response.ok && isSessionView(body.value)) {
        return {mode: 'api', session: body.value, error: null};
      }
      return {
        mode: 'api',
        session: null,
        error: response.ok ? new ApiError({status: response.status, kind: 'unexpected'}) : refusal(response.status, body.value),
      };
    },
    session,
    async signIn(csrf, login, password) {
      const view = await request<unknown>('POST', sessionPath, {csrf, body: {login, password}});
      if (!isSessionView(view) || !view.signed_in) {
        throw new ApiError({status: 200, kind: 'unexpected'});
      }
      return view;
    },
    signOut(csrf) {
      return request('DELETE', sessionPath, {csrf});
    },
    accounts({signal} = {}) {
      return request('GET', '/api/v1/accounts', {signal});
    },
    async accountCards(account, {signal} = {}) {
      return request('GET', `/api/v1/accounts/${segment(account)}/cards`, {signal});
    },
    async card(id, {signal} = {}) {
      return request('GET', `/api/v1/cards/${segment(id)}`, {signal});
    },
    async unit(card, n, {signal} = {}) {
      if (!Number.isInteger(n) || n < 1) {
        throw new ApiError({kind: 'not_found'});
      }
      return request('GET', `/api/v1/cards/${segment(card)}/units/${n}`, {signal});
    },
    async receipt(id, {signal} = {}) {
      return request('GET', `/api/v1/receipts/${segment(id)}`, {signal});
    },
  };
}

// A time as the server sent it, in UTC to the second. A time without an
// offset is a literal and is shown as written.
export function formatUtc(value: unknown): string {
  if (typeof value !== 'string' || !value) {
    return 'Not recorded';
  }
  if (!/(?:Z|[+-]\d\d:?\d\d)$/i.test(value) || !Number.isFinite(Date.parse(value))) {
    return `${value} · no time zone recorded`;
  }
  const date = new Date(value);
  const day = date.toLocaleDateString('en-GB', {day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC'});
  return `${day}, ${date.toISOString().slice(11, 19)} UTC`;
}

// A session past its expiry is over, whatever the cookie still says.
export function sessionExpired(session: SessionView | null | undefined, now = Date.now()): boolean {
  if (!session?.signed_in) {
    return false;
  }
  const expires = Date.parse(session.expires_at || '');
  return Number.isFinite(expires) && expires <= now;
}

// Why a sentence unit is not on the card, in the connector's terms.
export const notShownReasons: Record<string, string> = {
  cut: 'Cut at a pause, a change of speaker or the edge of an excerpt',
  later: 'Dated after the card’s day',
  long: 'Longer than a card sentence',
  more: 'More sentences than the card has room for',
};

export const gapDispositions: Record<string, string> = {not_read: 'Not read', read_not_shown: 'Read, not shown', note: 'Note'};

export interface UnitFacts {
  n: number | undefined;
  kind: string | undefined;
  recordRef: string | undefined;
  tool: string | undefined;
  source: string | null;
  at: string | null;
  text: string | null;
  partial: boolean;
  shown: string | null;
  notShown: string | null;
  speaker: string | null;
  role: string | null;
  basis: string | null;
  /** The number of the contact's record unit a customer's label equals, or null. */
  contact: number | null;
  receiptId: string | null;
}

const text = (value: unknown): string | null => (typeof value === 'string' ? value : null);

// The units' own fields, read defensively: a body is the sealed unit as stored.
export function unitFacts(unit: Partial<UnitView> | null | undefined): UnitFacts {
  const body: UnitBody = isObject(unit?.body) ? (unit.body as UnitBody) : {};
  const shown = typeof unit?.shown === 'string' && unit.shown ? unit.shown : text(body.shown);
  return {
    n: unit?.n,
    kind: unit?.kind,
    recordRef: unit?.record_ref,
    tool: unit?.tool,
    source: text(body.source),
    at: text(body.at),
    text: text(body.text),
    partial: body.partial === true,
    shown,
    notShown: shown && shown !== 'yes' ? (notShownReasons[shown] ?? `Reason ${shown}`) : null,
    speaker: text(body.speaker),
    role: text(body.role),
    basis: text(body.basis),
    contact: Number.isInteger(body.contact) && (body.contact as number) > 0 ? (body.contact as number) : null,
    receiptId: unit?.receipt_id || null,
  };
}
