import { test, mock } from 'node:test';
import assert from 'node:assert/strict';
import { debounce, latest } from '../static/js/debounce.js';

test('debounce runs once, with the last arguments, after the quiet period', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    const seen = [];
    const f = debounce((x) => seen.push(x), 250);
    f('a'); mock.timers.tick(100);
    f('ab'); mock.timers.tick(249);
    assert.deepEqual(seen, []);
    assert.equal(f.pending(), true);
    mock.timers.tick(1);
    assert.deepEqual(seen, ['ab']);
    assert.equal(f.pending(), false);
  } finally { mock.timers.reset(); }
});

test('debounce cancel and flush', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    const seen = [];
    const f = debounce((x) => seen.push(x), 250);
    f('x'); f.cancel(); mock.timers.tick(500);
    assert.deepEqual(seen, []);
    f('y'); f.flush();
    assert.deepEqual(seen, ['y']);
    mock.timers.tick(500);
    assert.deepEqual(seen, ['y'], 'flushed call does not run again');
    f.flush();
    assert.deepEqual(seen, ['y'], 'flush with nothing pending is a no-op');
  } finally { mock.timers.reset(); }
});

test('latest aborts the previous request and marks it stale', () => {
  const l = latest();
  const a = l.next();
  assert.equal(a.current(), true);
  const b = l.next();
  assert.equal(a.signal.aborted, true);
  assert.equal(a.current(), false);
  assert.equal(b.current(), true);
  l.cancel();
  assert.equal(b.signal.aborted, true);
  assert.equal(b.current(), false);
});

test('latest.run never delivers a stale result', async () => {
  const l = latest();
  const order = [];
  let releaseA;
  const pA = l.run((signal) => new Promise((r) => { releaseA = () => r('A'); signal.addEventListener('abort', () => order.push('aborted A')); }));
  const pB = l.run(async () => 'B');
  assert.deepEqual(await pB, { stale: false, value: 'B' });
  releaseA();
  assert.deepEqual(await pA, { stale: true });
  assert.deepEqual(order, ['aborted A']);
});

test('latest.run reports stale for errors of superseded requests, rethrows current ones', async () => {
  const l = latest();
  const p1 = l.run(() => new Promise((_, rej) => setTimeout(() => rej(new Error('late')), 5)));
  l.next();
  assert.deepEqual(await p1, { stale: true });
  await assert.rejects(l.run(async () => { throw new Error('now'); }), /now/);
});
