import {ApiError, sessionExpired} from './api.js';

// Which data the app shows, and the signed-in session when the Go server
// serves it. The mode is decided once per page load by asking the server; a
// server that answers and refuses stays the source, never the walkthrough.
//
// Each change of signed-in session starts a new generation. Reads are keyed
// by it, so a read made under an earlier session is dropped, and an answer
// that arrives after the session changed is ignored.

export const walkthroughStore = {
  subscribe: () => () => {},
  getSnapshot: () => walkthroughState,
  start: () => Promise.resolve(walkthroughState),
};
const walkthroughState = Object.freeze({mode: 'walkthrough', session: null, error: null, ended: false, generation: 0, elsewhere: null});

const signedOutView = {signed_in: false, principal: null, csrf_token: null, expires_at: null};
// A signed-in session is known by its CSRF token, which the server derives from the session ID.
const identity = session => session?.signed_in ? `${session.principal?.id}:${session.csrf_token}` : null;

export function createServerStore(api, {now = () => Date.now(), setTimer = (...args) => setTimeout(...args), clearTimer = id => clearTimeout(id), events = globalThis.document} = {}) {
  let state = {mode: 'probing', session: null, error: null, ended: false, generation: 0, elsewhere: null};
  let probe = null, timer = null;
  const listeners = new Set();

  // The only timer: the end of a signed-in session. At that moment the view
  // signs out at once, then asks the server for a fresh sign-in token.
  function schedule() {
    if (timer !== null) {clearTimer(timer); timer = null;}
    if (!state.session?.signed_in) return;
    const expires = Date.parse(state.session.expires_at || '');
    if (!Number.isFinite(expires)) return;
    timer = setTimer(() => {timer = null; return endSession();}, Math.min(Math.max(0, expires - now()), 2 ** 31 - 1));
  }
  function set(next) {
    const generation = identity(next.session) === identity(state.session) ? state.generation : state.generation + 1;
    state = {elsewhere: null, ...next, generation};
    schedule();
    for (const listener of [...listeners]) listener();
  }
  // A session that was signed in and is not now, without a sign-out here, ended.
  function settle(session, {signedOut = false} = {}) {
    const ended = !signedOut && !session.signed_in && (state.session?.signed_in || state.ended);
    set({mode: 'api', session, error: null, ended: Boolean(ended)});
  }
  // Ask the server for the session. An answer that arrives after the session
  // changed here belongs to an earlier generation and is ignored.
  async function refresh() {
    const generation = state.generation;
    try {
      const session = await api.session();
      if (state.generation === generation) settle(session);
    } catch (error) {
      if (state.generation === generation) set({...state, mode: 'api', error});
    }
    return state;
  }
  // The session is over here: expired, or refused by the server on a read.
  function endSession() {
    if (state.session?.signed_in) set({...state, mode: 'api', session: signedOutView, ended: true});
    return refresh();
  }
  // A timer can be late, after sleep or in a background tab: check on return.
  function check() {
    if (sessionExpired(state.session, now())) return endSession();
    return Promise.resolve(state);
  }

  return {
    api,
    subscribe(listener) {listeners.add(listener); return () => listeners.delete(listener);},
    getSnapshot: () => state,
    start() {
      if (!probe) {
        events?.addEventListener?.('visibilitychange', () => {if (events.visibilityState !== 'hidden') check();});
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
      let session;
      try {
        session = await api.signIn(state.session?.csrf_token, login, password);
      } catch (error) {
        if (error.kind !== 'csrf') throw error;
        // The sign-in token expired or was used: get a fresh one and try once more.
        const fresh = await api.session();
        if (fresh.signed_in) {
          // Another tab signed this browser in. The typed login was not checked,
          // so this tab is not signed in as it until the person chooses to be.
          set({mode: 'api', session: {...signedOutView, csrf_token: fresh.csrf_token}, error: null, ended: false, elsewhere: fresh});
          throw new ApiError({kind: 'already_signed_in'});
        }
        session = await api.signIn(fresh.csrf_token, login, password);
      }
      set({mode: 'api', session, error: null, ended: false});
      return state;
    },
    // Continue as the session another tab signed in.
    adoptElsewhere() {
      if (state.elsewhere) set({mode: 'api', session: state.elsewhere, error: null, ended: false});
      return state;
    },
    async signOut() {
      const signOutWith = token => api.signOut(token).then(() => true, error => {
        if (error.kind === 'signed_out') return true;
        throw error;
      });
      try {
        await signOutWith(state.session?.csrf_token);
      } catch (error) {
        if (error.kind !== 'csrf') {await refresh(); throw error;}
        // The session rotated under this tab: retry once with its fresh token.
        const fresh = await api.session();
        if (fresh.signed_in) await signOutWith(fresh.csrf_token);
      }
      // Signed out: clear the view now, then fetch a sign-in token.
      set({mode: 'api', session: signedOutView, error: null, ended: false});
      const generation = state.generation;
      try {
        const session = await api.session();
        if (state.generation === generation) settle(session, {signedOut: true});
      } catch (error) {
        if (state.generation === generation) set({...state, error});
      }
      return state;
    },
  };
}
