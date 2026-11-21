import assert from 'node:assert/strict';
import test from 'node:test';
import type {Load, Outcome, Reads} from '../../api/reads.ts';
import {startPool} from './pooledReads.ts';

// A read layer whose reads each wait until the test settles them.
function heldReads() {
  const open = new Map<string, (outcome: Outcome<string>) => void>();
  const released: string[] = [];
  let most = 0;
  const reads = {
    request<T>(key: string, _load: Load<T>, done: (outcome: Outcome<T>) => void) {
      open.set(key, done as (outcome: Outcome<string>) => void);
      most = Math.max(most, open.size);
      return () => {
        open.delete(key);
        released.push(key);
      };
    },
    last: () => undefined,
    forget: () => undefined,
  } satisfies Reads;
  const settle = (key: string) => {
    const done = open.get(key);
    assert.ok(done, `${key} is open`);
    open.delete(key);
    done({status: 'ready', data: key});
  };
  return {reads, open, released, settle, most: () => most};
}

test('a pool reads at most its limit at once, in order, and starts the next as each settles', () => {
  const held = heldReads();
  const keys = ['a', 'b', 'c', 'd', 'e', 'f', 'g'];
  const answered: string[] = [];
  startPool<string>(
    held.reads,
    keys,
    () => async () => '',
    4,
    key => answered.push(key),
  );
  assert.deepEqual([...held.open.keys()], ['a', 'b', 'c', 'd']);
  held.settle('b');
  assert.deepEqual([...held.open.keys()], ['a', 'c', 'd', 'e']);
  for (const key of ['a', 'c', 'd', 'e', 'f', 'g']) {
    held.settle(key);
  }
  assert.deepEqual(answered, ['b', 'a', 'c', 'd', 'e', 'f', 'g']);
  assert.equal(held.most(), 4);
  assert.equal(held.open.size, 0);
});

test('stopping a pool releases the reads still open and starts no more', () => {
  const held = heldReads();
  const stop = startPool<string>(
    held.reads,
    ['a', 'b', 'c', 'd', 'e', 'f'],
    () => async () => '',
    4,
    () => undefined,
  );
  held.settle('a');
  stop();
  assert.deepEqual(held.released, ['b', 'c', 'd', 'e']);
  assert.equal(held.open.size, 0);
});
