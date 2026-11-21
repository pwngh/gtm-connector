// A fake fetch for the API tests: it answers from a queue of answers and
// records every request. Only tests import it.

import type {Fetch} from './client.ts';
import type {PrincipalView, SessionView} from './types.ts';

export interface Answer {
  status?: number;
  json?: unknown;
  text?: string;
  broken?: boolean;
  type?: string | null;
  /** Never answers: it ends only when its signal aborts. */
  hang?: boolean;
  /** An opaque redirect, as fetch reports one with redirect: 'manual'. */
  redirect?: boolean;
  /** Answers once this settles. */
  gate?: Promise<unknown>;
}

export interface Call extends RequestInit {
  path: string;
  headers: Record<string, string>;
}

export function fakeFetch(...answers: (Answer | Error)[]): {fetch: Fetch; calls: Call[]} {
  const calls: Call[] = [];
  const fetch: Fetch = async (path, init) => {
    calls.push({...init, path, headers: (init.headers ?? {}) as Record<string, string>});
    const answer = answers.shift();
    if (!answer) {
      throw new Error(`unexpected request ${path}`);
    }
    if (answer instanceof Error) {
      throw answer;
    }
    if (answer.hang) {
      return new Promise<Response>((_, reject) => {
        init.signal?.addEventListener('abort', () => reject(init.signal?.reason ?? new DOMException('Aborted', 'AbortError')));
      });
    }
    if (answer.redirect) {
      return {
        status: 0,
        ok: false,
        type: 'opaqueredirect',
        redirected: false,
        headers: new Headers(),
        json: async () => {
          throw new SyntaxError('opaque');
        },
      } as unknown as Response;
    }
    if (answer.gate) {
      await answer.gate;
    }
    const {status = 200, json, text, broken = false} = answer;
    const type = answer.type === undefined ? (json === undefined && !broken ? 'text/html' : 'application/json') : answer.type;
    return {
      status,
      ok: status >= 200 && status < 300,
      type: 'basic',
      headers: new Headers(type ? {'Content-Type': type} : {}),
      json: async () => {
        if (broken) {
          throw new SyntaxError('Unexpected end of JSON input');
        }
        return json;
      },
      text: async () => text ?? JSON.stringify(json),
    } as unknown as Response;
  };
  return {fetch, calls};
}

export const signedOut: SessionView = {signed_in: false, principal: null, csrf_token: 'pre-token', expires_at: '2027-01-15T16:00:00Z'};
export const principal: PrincipalView = {id: 'p-1', display_name: 'Demo reviewer', kind: 'person', workspace_id: 'w-1', workspace_kind: 'demo', demo: true};
export const signedIn = (token = 'session-token', expires = '2027-01-15T23:00:00Z'): SessionView => ({
  signed_in: true,
  principal,
  csrf_token: token,
  expires_at: expires,
});
export const refused = (status: number, code: string): Answer => ({status, json: {error: {code, message: 'Refused.'}}});

export function deferred<T = void>() {
  let resolve: (value: T) => void = () => undefined;
  let reject: (error: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return {promise, resolve, reject};
}
