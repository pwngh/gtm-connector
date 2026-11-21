import {type Api, ApiError, isSessionView, sessionExpired} from './client.ts';
import type {SessionView} from './types.ts';

// How the server answered, and the signed-in session when the Go server
// serves the app. The mode is decided once per page load by asking the
// server; a server that answers and refuses is still the server.
//
// Each change of signed-in session starts a new generation. Reads are keyed
// by it, so a read made under an earlier session is dropped, and an answer
// that arrives after the session changed is ignored.

export type Mode = 'probing' | 'api' | 'static' | 'unreachable' | 'detached';

export interface ServerState {
  mode: Mode;
  session: SessionView | null;
  error: ApiError | null;
  ended: boolean;
  generation: number;
  /** A session another tab signed this browser in to, offered and not taken. */
  elsewhere: SessionView | null;
  /** The identity provider's own sign-out page, offered by the sign-out that just happened here. */
  endSessionUrl: string | null;
}

type Next = Omit<ServerState, 'generation' | 'elsewhere' | 'endSessionUrl'> & {elsewhere?: SessionView | null; endSessionUrl?: string | null};

export interface ServerStore {
  api: Api;
  subscribe(listener: () => void): () => void;
  getSnapshot(): ServerState;
  start(): Promise<ServerState>;
  refresh(): Promise<ServerState>;
  check(): Promise<ServerState>;
  expire(): Promise<ServerState>;
  signIn(login: string, password: string): Promise<ServerState>;
  adoptElsewhere(): ServerState;
  signOut(): Promise<ServerState>;
}

type TimerId = ReturnType<typeof setTimeout> | number;

interface StoreOptions {
  now?: () => number;
  setTimer?: (run: () => unknown, ms: number) => TimerId;
  clearTimer?: (id: TimerId) => void;
  events?: Pick<Document, 'addEventListener' | 'visibilityState'> | undefined;
}

const signedOutView: SessionView = {signed_in: false, principal: null};
// A signed-in session is known by its CSRF token, which the server derives from the session ID.
const identity = (session: SessionView | null) => (session?.signed_in ? `${session.principal?.id}:${session.csrf_token}` : null);
const asError = (error: unknown) => (error instanceof ApiError ? error : new ApiError());

export function createServerStore(
  api: Api,
  {now = () => Date.now(), setTimer = (run, ms) => setTimeout(run, ms), clearTimer = id => clearTimeout(id), events = globalThis.document}: StoreOptions = {},
): ServerStore {
  let state: ServerState = {mode: 'probing', session: null, error: null, ended: false, generation: 0, elsewhere: null, endSessionUrl: null};
  let probe: Promise<ServerState> | null = null;
  let timer: TimerId | null = null;
  const listeners = new Set<() => void>();

  // The only timer: the end of a signed-in session. At that moment the view
  // signs out at once, then asks the server for a fresh sign-in token.
  function schedule() {
    if (timer !== null) {
      clearTimer(timer);
      timer = null;
    }
    if (!state.session?.signed_in) {
      return;
    }
    const expires = Date.parse(state.session.expires_at || '');
    if (!Number.isFinite(expires)) {
      return;
    }
    timer = setTimer(
      () => {
        timer = null;
        return endSession();
      },
      Math.min(Math.max(0, expires - now()), 2 ** 31 - 1),
    );
  }
  function set(next: Next) {
    const generation = identity(next.session) === identity(state.session) ? state.generation : state.generation + 1;
    state = {elsewhere: null, endSessionUrl: null, ...next, generation};
    schedule();
    for (const listener of [...listeners]) {
      listener();
    }
  }
  // A session that was signed in and is not now, without a sign-out here, ended.
  function settle(session: SessionView, {signedOut = false, endSessionUrl = null as string | null} = {}) {
    const ended = !signedOut && !session.signed_in && (Boolean(state.session?.signed_in) || state.ended);
    set({mode: 'api', session, error: null, ended, endSessionUrl});
  }
  // Ask the server for the session. An answer that arrives after the session
  // changed here belongs to an earlier generation and is ignored.
  async function refresh() {
    const generation = state.generation;
    try {
      const session = await api.session();
      if (state.generation === generation) {
        settle(session);
      }
    } catch (error) {
      if (state.generation === generation) {
        set({...state, mode: 'api', error: asError(error)});
      }
    }
    return state;
  }
  // The session is over here: expired, or refused by the server on a read.
  function endSession() {
    if (state.session?.signed_in) {
      set({...state, mode: 'api', session: signedOutView, ended: true});
    }
    return refresh();
  }
  // A timer can be late, after sleep or in a background tab: check on return.
  function check() {
    if (sessionExpired(state.session, now())) {
      return endSession();
    }
    return Promise.resolve(state);
  }

  return {
    api,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getSnapshot: () => state,
    start() {
      if (!probe) {
        events?.addEventListener?.('visibilitychange', () => {
          if (events.visibilityState !== 'hidden') {
            check();
          }
        });
        probe = api.probe().then(result => {
          set({mode: result.mode, session: result.session, error: result.error, ended: false});
          return check().then(() => state);
        });
      }
      return probe;
    },
    refresh,
    check,
    expire: endSession,
    async signIn(login, password) {
      let session: SessionView;
      try {
        session = await api.signIn(state.session?.csrf_token, login, password);
      } catch (error) {
        if (asError(error).kind !== 'csrf') {
          throw error;
        }
        // The sign-in token expired or was used: get a fresh one and try once more.
        const fresh = await api.session();
        if (fresh.signed_in) {
          // Another tab signed this browser in. The typed login was not checked,
          // so this tab is not signed in as it until the person chooses to be.
          const token = fresh.csrf_token === undefined ? {} : {csrf_token: fresh.csrf_token};
          set({mode: 'api', session: {...signedOutView, ...token}, error: null, ended: false, elsewhere: fresh});
          throw new ApiError({kind: 'already_signed_in'});
        }
        session = await api.signIn(fresh.csrf_token, login, password);
      }
      set({mode: 'api', session, error: null, ended: false});
      return state;
    },
    // Continue as the session another tab signed in.
    adoptElsewhere() {
      if (state.elsewhere) {
        set({mode: 'api', session: state.elsewhere, error: null, ended: false});
      }
      return state;
    },
    async signOut() {
      // The answer of a sign-out, or null when the session had ended already.
      const signOutWith = (token: string | undefined) =>
        api.signOut(token).then(
          answer => answer,
          error => {
            if (asError(error).kind === 'signed_out') {
              return null;
            }
            throw error;
          },
        );
      let answer: unknown = null;
      try {
        answer = await signOutWith(state.session?.csrf_token);
      } catch (error) {
        if (asError(error).kind !== 'csrf') {
          await refresh();
          throw error;
        }
        // The session rotated under this tab: retry once with its fresh token.
        const fresh = await api.session();
        if (fresh.signed_in) {
          answer = await signOutWith(fresh.csrf_token);
        }
      }
      // A person who signed in through the identity provider is offered its own sign-out page.
      const offered =
        isSessionView(answer) && typeof answer.end_session_url === 'string' && /^https:\/\//.test(answer.end_session_url) ? answer.end_session_url : null;
      // Signed out: clear the view now, then fetch a sign-in token.
      set({mode: 'api', session: signedOutView, error: null, ended: false, endSessionUrl: offered});
      const generation = state.generation;
      try {
        const session = await api.session();
        if (state.generation === generation) {
          settle(session, {signedOut: true, endSessionUrl: offered});
        }
      } catch (error) {
        if (state.generation === generation) {
          set({...state, error: asError(error)});
        }
      }
      return state;
    },
  };
}
