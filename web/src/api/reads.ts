import {createContext, useCallback, useContext, useEffect, useRef, useState, useSyncExternalStore} from 'react';
import {type Api, ApiError} from './client.ts';
import type {ServerState, ServerStore} from './session.ts';

// Reads from the server, for the pages. A read is keyed by the session
// generation and the page's own key, so:
// - an answer for a key no longer asked for is dropped;
// - a read refused for sign-in ends the session;
// - reads of one key at the same time share one request;
// - a read nobody waits for any more is aborted;
// - a page that comes back within the session shows its last answer while
//   it reads again;
// - a change of session forgets every answer and aborts every read.
// Answers are kept in memory only, never in browser storage.

export type Load<T> = (api: Api, signal: AbortSignal) => Promise<T>;

export type Outcome<T> = {status: 'ready'; data: T} | {status: 'failed'; error: ApiError};

/** A read as a page sees it. `again` marks a last answer shown while it is read again. */
export type Read<T> = ({status: 'idle'} | {status: 'loading'} | {status: 'ready'; data: T; again?: boolean} | {status: 'failed'; error: ApiError}) & {
  retry?: (() => void) | undefined;
};

export interface Reads {
  /** Asks for a key. `done` gets the answer unless the returned release is called first. */
  request<T>(key: string, load: Load<T>, done: (outcome: Outcome<T>) => void): () => void;
  /** The last answer for a key in this session, if there is one. */
  last<T>(key: string): {data: T} | undefined;
  /** Forgets the last answer for a key. */
  forget(key: string): void;
}

type StoreLike = Pick<ServerStore, 'api' | 'subscribe' | 'getSnapshot' | 'expire'>;

interface Flight {
  controller: AbortController;
  waiters: number;
  promise: Promise<Outcome<unknown>>;
}

const asError = (error: unknown) => (error instanceof ApiError ? error : new ApiError());

export function createReads(store: StoreLike): Reads {
  let generation = store.getSnapshot().generation;
  const flights = new Map<string, Flight>();
  const answers = new Map<string, {data: unknown}>();
  store.subscribe(() => {
    const next = store.getSnapshot().generation;
    if (next === generation) {
      return;
    }
    generation = next;
    for (const flight of flights.values()) {
      flight.controller.abort();
    }
    flights.clear();
    answers.clear();
  });

  function start(key: string, load: Load<unknown>): Flight {
    const controller = new AbortController();
    const born = generation;
    const current = () => born === generation && !controller.signal.aborted;
    const settle = (outcome: Outcome<unknown>) => {
      if (flights.get(key) === flight) {
        flights.delete(key);
      }
      return outcome;
    };
    const flight: Flight = {
      controller,
      waiters: 0,
      promise: load(store.api, controller.signal).then(
        data => {
          if (current()) {
            answers.set(key, {data});
          }
          return settle({status: 'ready', data});
        },
        failure => {
          const error = asError(failure);
          const live = current();
          const outcome = settle({status: 'failed', error});
          if (live) {
            answers.delete(key);
            if (error.kind === 'signed_out') {
              store.expire();
            }
          }
          return outcome;
        },
      ),
    };
    flights.set(key, flight);
    return flight;
  }

  return {
    request<T>(key: string, load: Load<T>, done: (outcome: Outcome<T>) => void) {
      const flight = flights.get(key) ?? start(key, load);
      flight.waiters += 1;
      let waiting = true;
      flight.promise.then(outcome => {
        // A read aborted by a change of session answers nobody.
        if (waiting && !flight.controller.signal.aborted) {
          waiting = false;
          done(outcome as Outcome<T>);
        }
      });
      return () => {
        if (!waiting) {
          return;
        }
        waiting = false;
        flight.waiters -= 1;
        if (flight.waiters === 0 && flights.get(key) === flight) {
          flights.delete(key);
          flight.controller.abort();
        }
      };
    },
    last<T>(key: string) {
      return answers.get(key) as {data: T} | undefined;
    },
    forget(key: string) {
      answers.delete(key);
    },
  };
}

// The server's store: the signed-in session and how the server answered.
// Without a provider, as when one view renders alone, nothing is signed in
// and nothing is read.
const detachedState: ServerState = Object.freeze({mode: 'detached', session: null, error: null, ended: false, generation: 0, elsewhere: null});
const detached = () => Promise.resolve(detachedState);
const nothing = () => Promise.reject(new ApiError({kind: 'unreachable'}));
const detachedStore: ServerStore = {
  api: {
    probe: () => Promise.resolve({mode: 'unreachable', session: null, error: new ApiError({kind: 'unreachable'})}),
    session: nothing,
    signIn: nothing,
    signOut: nothing,
    accounts: nothing,
    accountCards: nothing,
    card: nothing,
    cardBytes: nothing,
    cardText: nothing,
    unit: nothing,
    receipt: nothing,
    health: nothing,
  },
  subscribe: () => () => undefined,
  getSnapshot: () => detachedState,
  start: detached,
  refresh: detached,
  check: detached,
  expire: detached,
  signIn: detached,
  signOut: detached,
  adoptElsewhere: () => detachedState,
};

export const ServerContext = createContext<ServerStore>(detachedStore);

export function useServer(): {store: ServerStore; state: ServerState} {
  const store = useContext(ServerContext);
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
  return {store, state};
}

const readers = new WeakMap<StoreLike, Reads>();

/** The one read layer of a store. */
export function readsFor(store: StoreLike): Reads {
  let reads = readers.get(store);
  if (!reads) {
    reads = createReads(store);
    readers.set(store, reads);
  }
  return reads;
}

// Reads once per key, and again when the key or the session generation
// changes. A null key reads nothing.
export function useRead<T>(key: string | null, load: Load<T>): Read<T> {
  const {store, state} = useServer();
  const full = key === null ? null : `${state.generation}|${key}`;
  const [answer, setAnswer] = useState<{key: string; outcome: Outcome<T>} | null>(null);
  const [attempt, setAttempt] = useState(0);
  const loader = useRef(load);
  loader.current = load;
  // biome-ignore lint/correctness/useExhaustiveDependencies: a retry changes attempt, which reads again.
  useEffect(() => {
    if (full === null) {
      return undefined;
    }
    return readsFor(store).request<T>(
      full,
      (api, signal) => loader.current(api, signal),
      outcome => setAnswer({key: full, outcome}),
    );
  }, [full, attempt, store]);
  const retry = useCallback(() => {
    if (full !== null) {
      readsFor(store).forget(full);
    }
    setAnswer(null);
    setAttempt(value => value + 1);
  }, [full, store]);
  if (full === null) {
    return {status: 'idle', retry};
  }
  if (answer?.key === full) {
    return {...answer.outcome, retry};
  }
  const last = readsFor(store).last<T>(full);
  return last ? {status: 'ready', data: last.data, again: true, retry} : {status: 'loading', retry};
}
