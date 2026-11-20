import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  deletedMode, serverDeleted, openError, scopeTitle, threadSize, flagPressed, scopeKey, stepScope, loadCollapsed,
} from '../static/js/logic.js';
import { sourceTrees, scopeOrder } from '../static/js/tree.js';
import { defaultRoute } from '../static/js/router.js';

const r = (o) => ({ ...defaultRoute(), ...o });

const ref = (folderId, sourceId) => ({ folderId, sourceId, sourceKind: 'mbox', source: 's', count: 1, unread: 0 });
const facets = {
  sources: [{ id: 1, name: 'Mail', kind: 'mbox', count: 3, unread: 0 }, { id: 2, name: 'b.olm', kind: 'olm', count: 1, unread: 0 }],
  folders: [
    { name: 'Archive', path: 'Archive', count: 1, unread: 0, sources: [ref(10, 1)], children: [
      { name: '2019', path: 'Archive/2019', count: 1, unread: 0, sources: [ref(11, 1)], children: [] },
    ] },
    { name: 'Inbox', path: 'Inbox', count: 1, unread: 0, sources: [ref(12, 1), ref(20, 2)], children: [] },
  ],
  labels: [], categories: [], tags: [], flags: [],
};
const trees = sourceTrees(facets);

test('deletedMode: the route wins, else the server default, else unknown', () => {
  assert.equal(deletedMode(r({ deleted: 'only' }), { includeDeleted: false }), 'only');
  assert.equal(deletedMode(r(), { includeDeleted: true }), 'include');
  assert.equal(deletedMode(r(), { includeDeleted: false }), 'hide');
  assert.equal(deletedMode(r(), null), '');
  assert.equal(serverDeleted({ includeDeleted: true }), 'include');
  assert.equal(serverDeleted(null), '');
});

test('openError explains each error code', () => {
  assert.match(openError({ code: 'not_found' })[0], /no longer in the index/);
  assert.match(openError({ code: 'stale' })[0], /changed/);
  assert.match(openError({ code: 'gone' })[0], /missing/);
  assert.match(openError({ code: 'too_large' })[0], /too large/);
  assert.deepEqual([...openError({ message: 'boom' })], ['This message could not be opened: boom', '']);
  assert.equal(openError({ code: 'not_found' }).retry, false);
  assert.equal(openError({ code: 'stale' }).retry, true);
});

test('scopeTitle names the list', () => {
  assert.equal(scopeTitle(r(), facets, trees), 'All mail');
  assert.equal(scopeTitle(r({ source: 2 }), facets, trees), 'b.olm');
  assert.equal(scopeTitle(r({ folderId: 11 }), facets, trees), 'Archive/2019');
  assert.equal(scopeTitle(r({ folder: 'Inbox' }), facets, trees), 'Inbox');
  assert.equal(scopeTitle(r({ folderId: 99 }), facets, trees), 'Folder');
  assert.equal(scopeTitle(r({ source: 9 }), null, []), 'Source');
  assert.equal(scopeTitle(r({ deleted: 'only' }), facets, trees), 'All mail: deleted');
});

test('threadSize: the full size even when the server capped the thread', () => {
  assert.equal(threadSize(null, null), 0);
  assert.equal(threadSize(null, { threadCount: 4 }), 4);
  assert.equal(threadSize([{ threadCount: 6000 }, { threadCount: 6000 }], null), 6000);
  assert.equal(threadSize([{}, {}, {}], { threadCount: 1 }), 3);
});

test('flagPressed: is:/has: in the query, deleted from the route', () => {
  assert.equal(flagPressed('starred', 'x is:flagged', r()), true);
  assert.equal(flagPressed('attachment', 'has:att', r()), true);
  assert.equal(flagPressed('unread', '-is:unread', r()), false);
  assert.equal(flagPressed('deleted', 'is:deleted', r()), false);
  assert.equal(flagPressed('deleted', '', r({ deleted: 'only' })), true);
});

test('scopeKey and stepScope: [ and ] step from the current scope, path scopes included', () => {
  assert.equal(scopeKey(r(), trees), 'all:0');
  assert.equal(scopeKey(r({ source: 2 }), trees), 'source:2');
  assert.equal(scopeKey(r({ folderId: 11 }), trees), 'folder:11');
  assert.equal(scopeKey(r({ folder: 'Archive/2019' }), trees), 'folder:11', 'a path scope maps to its folder');
  const order = scopeOrder(trees, new Set());
  assert.deepEqual(stepScope(order, r({ folder: 'Archive/2019' }), trees, 1), { folderId: 12 });
  assert.deepEqual(stepScope(order, r({ folderId: 10 }), trees, -1), { source: 1 });
  assert.deepEqual(stepScope(order, r({ source: 1 }), trees, -1), {});
  assert.deepEqual(stepScope(order, r(), trees, -1), {}, 'clamped at All mail');
  assert.deepEqual(stepScope(order, r({ source: 2 }), trees, 5), { folderId: 20 }, 'clamped at the end');
});

test('loadCollapsed keeps only string keys of the stored array, pruned to the trees', () => {
  assert.deepEqual([...loadCollapsed(['s:1', 'f:11', 'f:999', 7, null], trees)], ['s:1', 'f:11']);
  assert.deepEqual([...loadCollapsed({ evil: true }, trees)], []);
  assert.deepEqual([...loadCollapsed('s:1', trees)], []);
  assert.deepEqual([...loadCollapsed(['s:1', 'f:999'], null)], ['s:1', 'f:999'], 'no trees yet: keep');
});
