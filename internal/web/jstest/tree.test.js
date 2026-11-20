import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installFakeDOM } from './fakedom.js';

installFakeDOM();
const { sourceTrees, scopeOrder, renderSourceTrees, kindLabel } = await import('../static/js/tree.js');
const { FLAGS, flagQuery, flagMarks } = await import('../static/js/flags.js');

const ref = (folderId, sourceId, count = 1, unread = 0) => ({ folderId, sourceId, sourceKind: 'x', source: 's', count, unread });
const facets = {
  sources: [
    { id: 1, name: 'Mail', kind: 'mbox', count: 6, unread: 1 },
    { id: 2, name: 'Takeout.mbox', kind: 'mbox', count: 2, unread: 0 },
    { id: 3, name: 'backup.olm', kind: 'olm', count: 1, unread: 1 },
  ],
  folders: [
    { name: 'Archive', path: 'Archive', count: 5, unread: 0, sources: [ref(10, 1, 5)], children: [
      { name: '2019', path: 'Archive/2019', count: 1, unread: 1, sources: [ref(11, 1, 1, 1)], children: [] },
    ] },
    { name: 'Inbox', path: 'Inbox', count: 2, unread: 0, sources: [ref(20, 2, 2)], children: [] },
    { name: 'backup', path: 'backup', count: 0, unread: 0, sources: [ref(30, 3, 0)], children: [
      { name: 'Inbox', path: 'backup/Inbox', count: 1, unread: 1, sources: [ref(31, 3, 1, 1)], children: [] },
    ] },
    { name: 'shared', path: 'shared', count: 0, unread: 0, sources: [], children: [
      { name: 'x', path: 'shared/x', count: 2, unread: 0, sources: [ref(40, 1, 1), ref(41, 2, 1)], children: [] },
    ] },
  ],
};

test('sourceTrees groups the merged folder tree by source with per-source counts', () => {
  const t = sourceTrees(facets);
  assert.deepEqual(t.map((x) => x.source.id), [1, 2, 3]);
  const mail = t[0].nodes;
  assert.deepEqual(mail.map((n) => n.path), ['Archive', 'shared']);
  assert.equal(mail[0].folderId, 10);
  assert.equal(mail[0].count, 5);
  assert.deepEqual(mail[0].children.map((n) => [n.path, n.folderId, n.unread]), [['Archive/2019', 11, 1]]);
  assert.equal(mail[1].folderId, null, 'an intermediate node has no folder of its own');
  assert.deepEqual(mail[1].children.map((n) => [n.folderId, n.count]), [[40, 1]]);
  assert.deepEqual(t[1].nodes.flatMap((n) => [n.path, ...n.children.map((c) => c.path)]), ['Inbox', 'shared', 'shared/x']);
});

test('sourceTrees hoists the empty root named after a PST/OLM file', () => {
  const olm = sourceTrees(facets)[2];
  assert.deepEqual(olm.nodes.map((n) => [n.path, n.name, n.folderId]), [['backup/Inbox', 'Inbox', 31]]);
  assert.deepEqual(sourceTrees({}), []);
});

test('sourceTrees drops folder-less wrappers and a lone folder that is the source itself', () => {
  const t = sourceTrees({
    sources: [{ id: 5, name: 'Takeout/All mail.mbox', kind: 'mbox', count: 3, unread: 0 },
      { id: 6, name: 'Local/Archives.sbd/2021', kind: 'mbox', count: 2, unread: 0 }],
    folders: [
      { name: 'Takeout', path: 'Takeout', count: 0, unread: 0, sources: [], children: [
        { name: 'All mail', path: 'Takeout/All mail', count: 3, unread: 0, sources: [ref(50, 5, 3)], children: [] },
      ] },
      { name: 'Local', path: 'Local', count: 0, unread: 0, sources: [], children: [
        { name: 'Archives', path: 'Local/Archives', count: 0, unread: 0, sources: [], children: [
          { name: '2021', path: 'Local/Archives/2021', count: 2, unread: 0, sources: [ref(60, 6, 2)], children: [
            { name: 'Q1', path: 'Local/Archives/2021/Q1', count: 1, unread: 0, sources: [ref(61, 6, 1)], children: [] },
          ] },
        ] },
      ] },
    ],
  });
  assert.deepEqual(t[0].nodes, [], 'the source row stands for its only folder');
  assert.deepEqual(t[1].nodes.map((n) => [n.path, n.folderId, n.children.length]), [['Local/Archives/2021', 60, 1]]);
});

test('scopeOrder lists the sources and visible folders in sidebar order', () => {
  const t = sourceTrees(facets);
  assert.deepEqual(scopeOrder(t, new Set()).map((s) => `${s.kind}:${s.id}`),
    ['all:0', 'source:1', 'folder:10', 'folder:11', 'folder:40', 'source:2', 'folder:20', 'folder:41', 'source:3', 'folder:31']);
  assert.deepEqual(scopeOrder(t, new Set(['s:1', 'f:20'])).map((s) => `${s.kind}:${s.id}`),
    ['all:0', 'source:1', 'source:2', 'folder:20', 'folder:41', 'source:3', 'folder:31'], 'collapsed children are skipped');
});

test('renderSourceTrees: links, counts, kind badges, current item, disclosure', () => {
  const nav = document.createElement('div');
  const t = sourceTrees(facets);
  renderSourceTrees(nav, t, { current: { folderId: 11 }, collapsed: new Set(['f:p:1:shared']), href: (r) => `#/${JSON.stringify(r)}` });
  const sources = nav.querySelectorAll('.source');
  assert.equal(sources.length, 3);
  assert.equal(sources[0].querySelector('.source-name').textContent, 'Mail');
  assert.equal(sources[2].querySelector('.kind-badge').textContent, 'OLM');
  const cur = nav.querySelectorAll('a[aria-current="page"]');
  assert.equal(cur.length, 1);
  assert.equal(cur[0].querySelector('.node-name').textContent, '2019');
  assert.equal(cur[0].href, '#/{"folderId":11}');
  const shared = nav.querySelectorAll('.node').find((n) => n.dataset.key === 'f:p:1:shared');
  assert.equal(shared.querySelector('.twisty').getAttribute('aria-expanded'), 'false');
  assert.equal(shared.querySelector('ul').hidden, true);
  const inter = shared.querySelector('.node-label');
  assert.equal(inter.tagName, 'SPAN', 'a folder-less node is not a link');
  assert.equal(kindLabel('pst'), 'PST');
});

test('FLAGS and their filters', () => {
  assert.deepEqual(FLAGS.map((f) => f.name), ['unread', 'starred', 'answered', 'forwarded', 'draft', 'deleted', 'junk', 'attachment', 'important']);
  assert.deepEqual(flagQuery('attachment'), ['has', 'attachment']);
  assert.deepEqual(flagQuery('starred'), ['is', 'starred']);
});

test('flagMarks: an icon and a spoken name per flag; unread is shown by the row style', () => {
  const m = flagMarks(['unread', 'starred', 'attachment', 'bogus']);
  const names = m.querySelectorAll('.flag').map((f) => f.dataset.flag);
  assert.deepEqual(names, ['starred', 'attachment']);
  assert.equal(m.querySelector('.visually-hidden').textContent, 'Starred');
  assert.equal(flagMarks(['unread']), null);
  assert.equal(flagMarks([]), null);
});
