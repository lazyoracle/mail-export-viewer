import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  emptyList, startLoading, applyPage, failPage,
  clampIndex, progressInfo, shouldRefresh, sourceWarnings, inputWarnings, visibleRows,
} from '../static/js/state.js';
import { ApiError, readJSON } from '../static/js/api.js';
import { buildSrcdoc } from '../static/js/srcdoc.js';

test('list paging keeps the first page total', () => {
  let l = startLoading(emptyList());
  assert.equal(l.loading, true);
  l = applyPage(l, { items: [{ id: 1 }, { id: 2 }], total: 3, nextCursor: 'c1' });
  assert.deepEqual(l.items.map((i) => i.id), [1, 2]);
  assert.equal(l.total, 3);
  assert.equal(l.cursor, 'c1');
  assert.equal(l.done, false);
  assert.equal(l.loading, false);
  l = applyPage(startLoading(l), { items: [{ id: 2 }, { id: 3 }], total: -1, nextCursor: '' });
  assert.deepEqual(l.items.map((i) => i.id), [1, 2, 3], 'duplicates dropped');
  assert.equal(l.total, 3, 'later page -1 ignored');
  assert.equal(l.done, true);
});

test('list total falls back to item count if the first page lacks one', () => {
  const l = applyPage(startLoading(emptyList()), { items: [{ id: 1 }], total: -1, nextCursor: '' });
  assert.equal(l.total, 1);
});

test('failPage records the error and stops loading', () => {
  const l = failPage(startLoading(emptyList()), new Error('boom'));
  assert.equal(l.loading, false);
  assert.equal(l.error, 'boom');
  assert.equal(applyPage(l, { items: [], total: 0, nextCursor: '' }).error, null);
});

test('clampIndex', () => {
  assert.equal(clampIndex(-1, 1, 5), 0);
  assert.equal(clampIndex(0, -1, 5), 0);
  assert.equal(clampIndex(4, 1, 5), 4);
  assert.equal(clampIndex(2, 1, 5), 3);
  assert.equal(clampIndex(0, 1, 0), -1);
});

const src = (o) => ({ id: 1, name: 'a', kind: 'mbox', state: 'done', unit: 'bytes', done: 0, total: 0, warningCount: 0, warnings: [], error: '', ...o });

test('progressInfo: idle, running with byte progress, indeterminate', () => {
  assert.equal(progressInfo(null).active, false);
  const idle = progressInfo({ indexing: { running: false, finished: true, messages: 5, warnings: 2, error: '' }, sources: [src({ state: 'cached' })] });
  assert.deepEqual(idle, { active: false, fraction: 1, messages: 5, warnings: 2, current: '', error: '', sources: 1, finished: 1 });
  const run = progressInfo({
    indexing: { running: true, finished: false, messages: 7, warnings: 1, error: '' },
    sources: [
      src({ name: 'Inbox', state: 'done', done: 10, total: 10 }),
      src({ name: 'big.pst', kind: 'pst', state: 'indexing', unit: 'messages', done: 5, total: 10 }),
      src({ name: 'later', state: 'queued', done: 0, total: 100 }),
      src({ name: 'old', state: 'incomplete' }),
    ],
  });
  assert.equal(run.active, true);
  assert.equal(run.current, 'big.pst');
  assert.equal(run.fraction, 0.5, '(1 + 0.5 + 0) / 3 run sources');
  assert.equal(run.sources, 3);
  assert.equal(run.finished, 1);
  assert.equal(run.messages, 7);
  const unknown = progressInfo({ indexing: { running: true, messages: 0 }, sources: [src({ state: 'indexing', unit: 'messages', done: 3, total: 0 })] });
  assert.equal(unknown.fraction, null, 'no total: indeterminate');
  assert.equal(progressInfo({ indexing: { running: true }, sources: [src({ state: 'indexing', done: 200, total: 100 })] }).fraction, 1);
  assert.equal(progressInfo({ indexing: { running: false, error: 'disk full' }, sources: [] }).error, 'disk full');
  const lag = progressInfo({ indexing: { running: true, messages: 0 }, sources: [src({ state: 'indexing', messages: 1800, done: 3, total: 10 }), src({ state: 'done', messages: 200 })] });
  assert.equal(lag.messages, 2000, 'per-source counts when the run total lags');
});

test('shouldRefresh: when message count grows or a run ends', () => {
  const st = (running, messages) => ({ indexing: { running, messages } });
  assert.equal(shouldRefresh(null, st(true, 10)), true);
  assert.equal(shouldRefresh(st(true, 10), st(true, 10)), false);
  assert.equal(shouldRefresh(st(true, 10), st(true, 11)), true);
  assert.equal(shouldRefresh(st(true, 10), st(false, 10)), true);
  assert.equal(shouldRefresh(st(false, 10), st(false, 10)), false);
});

test('sourceWarnings lists sources with warnings or errors, worst first', () => {
  const w = sourceWarnings({ sources: [
    src({ name: 'ok' }),
    src({ name: 'w', warningCount: 30, warnings: ['bad header'] }),
    src({ name: 'f', state: 'failed', error: 'unreadable', warningCount: 1, warnings: ['x'] }),
  ] });
  assert.deepEqual(w.map((x) => x.name), ['f', 'w']);
  assert.equal(w[1].more, 29, 'count beyond the listed warnings');
  assert.deepEqual(sourceWarnings(null), []);
});

test('inputWarnings: skipped inputs, counted in the progress warnings', () => {
  const s = { indexing: { running: false, finished: true, messages: 1, warnings: 2 }, sources: [src({ state: 'done' })],
    inputWarnings: ['skipped files/fake.pst: pst: not a PST file'], inputWarningCount: 4 };
  assert.deepEqual(inputWarnings(s), { warnings: ['skipped files/fake.pst: pst: not a PST file'], more: 3 });
  assert.equal(progressInfo(s).warnings, 6, 'index warnings plus input warnings');
  assert.deepEqual(inputWarnings(null), { warnings: [], more: 0 });
  assert.deepEqual(inputWarnings({ sources: [] }), { warnings: [], more: 0 }, 'older servers omit the fields');
});

test('visibleRows keeps the first row of each conversation across pages', () => {
  const seen = new Set();
  const page1 = [{ id: 5, threadId: 1 }, { id: 4, threadId: 2 }, { id: 3, threadId: 1 }];
  const page2 = [{ id: 2, threadId: 0 }, { id: 1, threadId: 2 }, { id: 0, threadId: 0 }];
  assert.deepEqual(visibleRows(page1, seen, true).map((i) => i.id), [5, 4]);
  assert.deepEqual(visibleRows(page2, seen, true).map((i) => i.id), [2, 0], 'threadId 0 means not threaded yet');
  assert.deepEqual(visibleRows(page1, new Set(), false).map((i) => i.id), [5, 4, 3], 'off: every message');
});

test('readJSON surfaces the server error message', async () => {
  const ok = { ok: true, status: 200, json: async () => ({ a: 1 }) };
  assert.deepEqual(await readJSON(ok), { a: 1 });
  const bad = { ok: false, status: 410, json: async () => ({ error: 'the source file changed', code: 'stale' }) };
  await assert.rejects(readJSON(bad), (e) => e instanceof ApiError && e.status === 410 && e.code === 'stale' && e.message === 'the source file changed');
  const ugly = { ok: false, status: 500, json: async () => { throw new SyntaxError('x'); } };
  await assert.rejects(readJSON(ugly), (e) => e instanceof ApiError && e.message === 'Request failed (HTTP 500)');
});

test('buildSrcdoc wraps the sanitised fragment with a base style', () => {
  const doc = buildSrcdoc('<p>Hello</p>');
  assert.match(doc, /^<!doctype html>/i);
  assert.match(doc, /<meta charset="utf-8">/);
  assert.match(doc, /<style>[^<]+<\/style>/);
  assert.match(doc, /<body><p>Hello<\/p><\/body>/);
  assert.doesNotMatch(doc, /<script|<base|<link/i);
});

test('buildSrcdoc cannot be broken out of by a stray </style>', () => {
  const doc = buildSrcdoc('</style><p>x</p>');
  assert.equal(doc.indexOf('</style>'), doc.lastIndexOf('</style>', doc.indexOf('<body>')));
});

import { countText } from '../static/js/state.js';

test('countText: messages, results, many, searching', () => {
  const l = (o) => ({ ...emptyList(), ...o });
  assert.equal(countText(l({ total: null }), { query: '' }), '');
  assert.equal(countText(l({ total: 3 }), { query: '' }), '3 messages');
  assert.equal(countText(l({ total: 1 }), { query: 'x' }), '1 result');
  assert.equal(countText(l({ total: 1200 }), { query: 'x' }), '1,200 results');
  assert.equal(countText(l({ total: -1 }), { query: 'x' }), 'Many results');
  assert.equal(countText(l({ total: -1 }), { query: '' }), 'Many messages');
  assert.equal(countText(l({ total: 3 }), { query: 'x', searching: true }), 'Searching…');
  assert.equal(countText(l({ total: null, loading: true }), { query: 'x' }), 'Searching…');
  assert.equal(countText(l({ total: null, loading: true }), { query: '' }), '');
});

test('applyPage: an unknown first-page total stays unknown unless that was the last page', () => {
  const l = applyPage(startLoading(emptyList()), { items: [{ id: 1 }], total: -1, nextCursor: 'c' });
  assert.equal(l.total, -1);
});

test('applyPage stays linear over many pages', () => {
  let l = emptyList();
  const t = performance.now();
  for (let p = 0; p < 2000; p++) {
    const items = Array.from({ length: 50 }, (_, i) => ({ id: p * 50 + i + 1 }));
    l = applyPage(startLoading(l), { items, total: p ? -1 : 100000, nextCursor: p < 1999 ? `c${p}` : '' });
  }
  assert.equal(l.items.length, 100000);
  assert.equal(l.total, 100000);
  assert.ok(performance.now() - t < 1500, `took ${performance.now() - t} ms`);
});
