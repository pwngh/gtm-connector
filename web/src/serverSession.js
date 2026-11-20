// Which data the app shows, and the signed-in session when the Go server
// serves it. The mode is decided once per page load by asking the server; a
// server that answers and refuses stays the source, never the walkthrough.

export const walkthroughStore = {
  subscribe: () => () => {},
  getSnapshot: () => walkthroughState,
  start: () => Promise.resolve(walkthroughState),
};
const walkthroughState = Object.freeze({mode: 'walkthrough', session: null, error: null, ended: false});

export function createServerStore(api, {now = () => Date.now(), setTimer = (...args) => setTimeout(...args), clearTimer = id => clearTimeout(id)} = {}) {
  let state = {mode: 'probing', session: null, error: null, ended: false};
  let probe = null, timer = null;
  const listeners = new Set();

  function schedule() {
    if (timer !== null) {clearTimer(timer); timer = null;}
    const expires = Date.parse(state.session?.signed_in ? state.session.expires_at || '' : '');
    // Ask the server again when the session should end; it decides.
    if (Number.isFinite(expires)) timer = setTimer(() => {timer = null; return refresh();}, Math.min(Math.max(0, expires - now()) + 1000, 2 ** 31 - 1));
  }
  function set(next) {
    state = next;
    schedule();
    for (const listener of [...listeners]) listener();
  }
  // A session that was signed in and is not now, without a sign-out here, ended.
  function settle(session, {signedOut = false} = {}) {
    const ended = !signedOut && !session.signed_in && (state.session?.signed_in || state.ended);
    set({mode: 'api', session, error: null, ended: Boolean(ended)});
  }
  async function refresh() {
    try {
      settle(await api.session());
    } catch (error) {
      set({...state, mode: 'api', error});
    }
    return state;
  }

  return {
    api,
    subscribe(listener) {listeners.add(listener); return () => listeners.delete(listener);},
    getSnapshot: () => state,
    start() {
      probe ??= api.probe().then(result => {
        set({mode: result.mode, session: result.session, error: result.error, ended: false});
        return state;
      });
      return probe;
    },
    refresh,
    // A read was refused because the session is gone (expired or revoked).
    expire() {
      if (state.session?.signed_in) set({...state, session: {...state.session, signed_in: false, principal: null}, ended: true});
      return refresh();
    },
    async signIn(login, password) {
      let session;
      try {
        session = await api.signIn(state.session?.csrf_token, login, password);
      } catch (error) {
        if (error.kind !== 'csrf') throw error;
        // The sign-in token expired or was used: get a fresh one and try once more.
        const fresh = await api.session();
        if (fresh.signed_in) {settle(fresh); return state;}
        session = await api.signIn(fresh.csrf_token, login, password);
      }
      set({mode: 'api', session, error: null, ended: false});
      return state;
    },
    async signOut() {
      try {
        await api.signOut(state.session?.csrf_token);
      } catch (error) {
        if (error.kind !== 'signed_out') {await refresh(); throw error;}
      }
      try {
        settle(await api.session(), {signedOut: true});
      } catch (error) {
        set({mode: 'api', session: null, error, ended: false});
      }
      return state;
    },
  };
}
