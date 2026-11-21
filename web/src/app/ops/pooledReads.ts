import {useEffect, useRef, useState} from 'react';
import {type Load, type Outcome, type Reads, readsFor, useServer} from '../../api/reads.ts';

/** A key's read as a page sees it: its answer, or still reading. */
export type Pooled<T> = Outcome<T> | {status: 'reading'};

/**
 * Starts the reads of many keys through the read layer, at most `limit` at a
 * time, in the order given. Each next key starts when one settles. A change
 * of keys or of session releases every read still open, which aborts it when
 * nothing else waits for it.
 */
export function startPool<T>(
  reads: Reads,
  keys: string[],
  load: (key: string) => Load<T>,
  limit: number,
  done: (key: string, outcome: Outcome<T>) => void,
): () => void {
  const open = new Map<string, () => void>();
  let next = 0;
  let stopped = false;
  const fill = () => {
    while (!stopped && open.size < limit && next < keys.length) {
      const key = keys[next] as string;
      next += 1;
      open.set(
        key,
        reads.request<T>(key, load(key), outcome => {
          open.delete(key);
          done(key, outcome);
          fill();
        }),
      );
    }
  };
  fill();
  return () => {
    stopped = true;
    for (const release of open.values()) {
      release();
    }
    open.clear();
  };
}

/**
 * Reads each key, at most `limit` at a time, keyed by the session as useRead
 * keys its reads. A key read earlier in the session shows its last answer
 * while it is read again.
 */
export function usePooledReads<T>(keys: string[], load: (key: string) => Load<T>, limit: number): Map<string, Pooled<T>> {
  const {store, state} = useServer();
  const generation = state.generation;
  const joined = keys.join('\n');
  const pool = `${generation}\n${joined}`;
  const [answers, setAnswers] = useState<{pool: string; outcomes: Map<string, Outcome<T>>}>({pool, outcomes: new Map()});
  const loader = useRef(load);
  loader.current = load;

  useEffect(() => {
    const own = joined ? joined.split('\n') : [];
    const full = (key: string) => `${generation}|${key}`;
    setAnswers({pool, outcomes: new Map()});
    return startPool<T>(
      readsFor(store),
      own.map(full),
      key => loader.current(key.slice(key.indexOf('|') + 1)),
      limit,
      (key, outcome) =>
        setAnswers(previous =>
          previous.pool === pool ? {pool, outcomes: new Map(previous.outcomes).set(key.slice(key.indexOf('|') + 1), outcome)} : previous,
        ),
    );
  }, [store, generation, joined, pool, limit]);

  const reads = readsFor(store);
  const shown = new Map<string, Pooled<T>>();
  for (const key of keys) {
    const outcome = answers.pool === pool ? answers.outcomes.get(key) : undefined;
    const last = reads.last<T>(`${generation}|${key}`);
    shown.set(key, outcome ?? (last ? {status: 'ready', data: last.data} : {status: 'reading'}));
  }
  return shown;
}
