import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseRoute, formatRoute, sameRoute, sameList, SORTS, apiSort } from '../static/js/router.js';

const r = (o) => ({ view: 'mail', message: null, q: '', sort: 'newest', source: null, folderId: null, folder: '', deleted: '', ...o });

test('parseRoute: views, message, query, sort and scope', () => {
  assert.deepEqual(parseRoute(''), r());
  assert.deepEqual(parseRoute('#'), r());
  assert.deepEqual(parseRoute('#/'), r());
  assert.deepEqual(parseRoute('#/stats'), r({ view: 'stats' }));
  assert.deepEqual(parseRoute('#/m/12'), r({ message: 12 }));
  assert.deepEqual(parseRoute('#/?q=from%3Aann+x'), r({ q: 'from:ann x' }));
  assert.deepEqual(parseRoute('#/m/9?q=a%2Fb&sort=oldest&fid=4'), r({ message: 9, q: 'a/b', sort: 'oldest', folderId: 4 }));
  assert.deepEqual(parseRoute('#/?src=3&deleted=only'), r({ source: 3, deleted: 'only' }));
  assert.deepEqual(parseRoute('#/?deleted=include'), r({ deleted: 'include' }));
  assert.deepEqual(parseRoute('#/?deleted=hide'), r({ deleted: 'hide' }));
  assert.deepEqual(parseRoute('#/?folder=Archive%2F2019'), r({ folder: 'Archive/2019' }));
});

test('parseRoute: malformed parts are dropped, the rest kept', () => {
  assert.deepEqual(parseRoute('#/m/abc'), r());
  assert.deepEqual(parseRoute('#/m/0'), r());
  assert.deepEqual(parseRoute('#/m/99999999999999999999'), r());
  assert.deepEqual(parseRoute('#/nowhere'), r());
  assert.deepEqual(parseRoute('#/?sort=bogus&q=x'), r({ q: 'x' }));
  assert.deepEqual(parseRoute('#/?fid=-1&src=x&deleted=yes'), r());
  assert.deepEqual(parseRoute('#/?q=%E0%A4%A'), r(), 'bad percent-encoding');
});

test('formatRoute is canonical and round-trips', () => {
  assert.equal(formatRoute(r()), '#/');
  assert.equal(formatRoute(r({ view: 'stats', q: 'ignored' })), '#/stats');
  assert.equal(formatRoute(r({ message: 9, folderId: 2 })), '#/m/9?fid=2');
  assert.equal(formatRoute(r({ q: 'a b/c#d', sort: 'relevance' })), '#/?q=a+b%2Fc%23d&sort=relevance');
  assert.equal(formatRoute({ message: 3 }), '#/m/3', 'missing fields default');
  for (const x of [r(), r({ message: 125, source: 1 }), r({ q: '"x y" -z', sort: 'sender', message: 1, deleted: 'only' }),
    r({ q: 'ünï 日本', folder: 'a/b c' }), r({ view: 'stats' })]) {
    assert.deepEqual(parseRoute(formatRoute(x)), x);
  }
});

test('sameRoute compares every field; sameList ignores the open message', () => {
  assert.equal(sameRoute(r(), r()), true);
  assert.equal(sameRoute(r(), r({ q: 'x' })), false);
  assert.equal(sameRoute(r({ sort: 'oldest' }), r()), false);
  assert.equal(sameRoute(r({ folderId: 1 }), r()), false);
  assert.equal(sameList(r({ message: 1 }), r({ message: 2 })), true);
  assert.equal(sameList(r({ deleted: 'include' }), r()), false);
  assert.equal(sameList(r({ source: 2 }), r({ source: 3 })), false);
});

test('sort names map to the API', () => {
  assert.deepEqual(SORTS.map((s) => s.id), ['newest', 'oldest', 'sender', 'subject', 'relevance']);
  assert.equal(apiSort('newest'), 'date_desc');
  assert.equal(apiSort('oldest'), 'date_asc');
  assert.equal(apiSort('sender'), 'from');
  assert.equal(apiSort('subject'), 'subject');
  assert.equal(apiSort('relevance'), 'relevance');
  assert.equal(apiSort('nope'), 'date_desc');
});
