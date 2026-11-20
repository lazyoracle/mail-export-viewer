// Smoke test for the controller: app.js runs (it starts on import) against
// the real index.html skeleton in the fake DOM, a fake window/location and
// a routed fake fetch. The tests share one app instance and run in order.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { installFakeDOM } from './fakedom.js';

const document = installFakeDOM();

// ---- build the page from index.html (tags, ids, classes, hidden only) ----
const VOID = new Set(['meta', 'link', 'input', 'br', 'img', 'use', 'path', 'circle', 'rect']);
function buildPage(html) {
  const body = html.slice(html.indexOf('<body>') + 6, html.indexOf('</body>'));
  const stack = [document.body];
  for (const m of body.matchAll(/<(\/?)([a-zA-Z][\w-]*)([^>]*?)(\/?)>/g)) {
    const [, close, tag, attrs, selfClose] = m;
    if (close) { if (stack.length > 1) stack.pop(); continue; }
    const el = document.createElement(tag);
    for (const a of attrs.matchAll(/([\w-]+)(?:="([^"]*)")?/g)) {
      const [, k, v = ''] = a;
      if (k === 'hidden') el.hidden = true;
      else if (k.startsWith('data-')) el.dataset[k.slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = v;
      else el.setAttribute(k, v);
    }
    stack[stack.length - 1].append(el);
    if (!selfClose && !VOID.has(tag.toLowerCase())) stack.push(el);
  }
}
buildPage(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8'));

// ---- fake browser globals ----
const listeners = {};
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));
globalThis.location = { hash: '' };
globalThis.window = {
  addEventListener: (t, fn) => { (listeners[t] ||= []).push(fn); },
  matchMedia: () => ({ matches: false }),
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  history: { replaceState: (_, __, h) => { location.hash = h; }, back() {} },
};
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.IntersectionObserver = class { observe() {} };
globalThis.CSS = { escape: (s) => s };
const fire = (type, ev = {}) => { for (const fn of listeners[type] || []) fn({ preventDefault() {}, defaultPrevented: false, ...ev }); };

const evil = '<img src=x onerror=alert(1)>';
const item = (id, extra = {}) => ({
  id, threadId: id, threadCount: 1, sourceId: 1, sourceKind: 'mbox', folderId: 10, folder: 'Inbox', subject: `Subject ${id}`,
  from: { name: 'Ann', email: 'ann@example.com' }, toText: '', date: '2024-01-02T10:00:00Z', flags: [], hasAttachment: false,
  labels: ['Work'], categories: [], tags: [], snippet: 'snip', ...extra,
});
const DATA = {
  '/api/status': { version: 't', includeDeleted: false, indexing: { running: false, finished: true, messages: 3, warnings: 0, error: '' },
    sources: [{ id: 1, name: 'Inbox.mbox', kind: 'mbox', state: 'done', unit: 'bytes', done: 1, total: 1, messages: 3, count: 3, unread: 1, complete: true, warningCount: 0, warnings: [], error: '' }] },
  '/api/facets': {
    sources: [{ id: 1, name: 'Mail', kind: 'mbox', count: 3, unread: 1 }],
    folders: [{ name: 'Inbox', path: 'Inbox', count: 3, unread: 1, sources: [{ folderId: 10, sourceId: 1, sourceKind: 'mbox', source: 'Mail', count: 3, unread: 1 }], children: [
      { name: 'Sub', path: 'Inbox/Sub', count: 0, unread: 0, sources: [{ folderId: 11, sourceId: 1, sourceKind: 'mbox', source: 'Mail', count: 0, unread: 0 }], children: [] }] }],
    labels: [{ name: 'Work', color: '', count: 3, unread: 1 }], categories: [], tags: [{ name: evil, color: '#FF0000', count: 1, unread: 0 }],
    flags: [{ name: 'unread', count: 1 }, { name: 'starred', count: 2 }],
  },
};
const fetched = [];
globalThis.fetch = async (url) => {
  fetched.push(url);
  const path = url.split('?')[0];
  let body = DATA[path];
  if (path === '/api/messages') body = { items: [item(1, { subject: evil }), item(2), item(3)], total: 3, nextCursor: '' };
  const m = /^\/api\/messages\/(\d+)$/.exec(path);
  if (m) {
    body = Number(m[1]) === 404 ? null : { ...item(Number(m[1])), source: { id: 1, name: 'Mail', kind: 'mbox' }, messageId: 'x@example.com',
      inReplyTo: '', references: [], to: [], cc: [], bcc: [], props: [], headers: [], text: 'hello', textTruncated: false,
      bodyHtml: '', htmlTruncated: false, remoteContentBlocked: false, attachments: [], rawUrl: '', parseError: false };
    if (!body) return new Response(JSON.stringify({ error: 'gone', code: 'not_found' }), { status: 404 });
  }
  return new Response(JSON.stringify(body || {}), { status: body ? 200 : 404 });
};

await import('../static/js/app.js');
await tick(20);
const $ = (id) => document.getElementById(id);

test('app starts: list, sidebar and title render from the API', () => {
  assert.ok(fetched.includes('/api/status'));
  assert.ok(fetched.some((u) => u.startsWith('/api/facets')));
  const rows = $('message-list').querySelectorAll('.row');
  assert.equal(rows.length, 3);
  assert.equal(rows[0].querySelector('.row-subject').textContent, evil);
  assert.ok(!document.body.descendants().some((e) => e.tagName === 'IMG' && e.getAttribute('src') === 'x'), 'no element parsed from text');
  assert.equal($('list-title').textContent, 'All mail');
  assert.equal($('list-count').textContent, '3 messages');
  assert.equal($('source-tree').querySelectorAll('.source').length, 1);
  assert.equal($('terms-tag').hidden, false);
  assert.equal($('terms-category').hidden, true, 'empty sections stay hidden');
  assert.equal($('deleted-mode').value, 'hide', 'server default');
});

test('typing in the search box marks the sidebar without rebuilding it', async () => {
  const before = $('flag-list').querySelectorAll('.flag-btn');
  const termsBefore = $('terms-label').querySelectorAll('.side-term');
  $('search').value = 'is:starred label:work';
  $('search').dispatch('input');
  const after = $('flag-list').querySelectorAll('.flag-btn');
  assert.ok(before.length === after.length && before.every((b, i) => b === after[i]), 'same flag buttons');
  assert.ok(termsBefore.every((b, i) => b === $('terms-label').querySelectorAll('.side-term')[i]), 'same term buttons');
  assert.equal(after.find((b) => b.dataset.flag === 'starred').getAttribute('aria-pressed'), 'true');
  assert.equal($('terms-label').querySelector('.side-term').getAttribute('aria-pressed'), 'true');
  assert.deepEqual($('query-chips').querySelectorAll('.query-chip-text').map((c) => c.textContent), ['Starred', 'Label: work']);
  await tick(300); // the debounced search runs
  assert.ok(fetched.some((u) => u.includes('q=is%3Astarred+label%3Awork')), fetched.at(-1));
});

test('a deep link opens the message; a stale one is explained', async () => {
  location.hash = '#/m/2';
  fire('hashchange');
  await tick(20);
  assert.equal($('message').hidden, false);
  assert.equal($('message').querySelector('.msg-subject').textContent, 'Subject 2');
  location.hash = '#/m/404';
  fire('hashchange');
  await tick(20);
  assert.match($('message').querySelector('.reader-state-title').textContent, /no longer in the index/);
  assert.equal($('message').querySelector('.btn'), null, 'no retry for a stale id');
});

test('keyboard: ] steps to the next folder', async () => {
  location.hash = '#/';
  fire('hashchange');
  await tick(20);
  fire('keydown', { key: ']', target: document.body });
  await tick(20);
  assert.match(location.hash, /src=1/);
  fire('keydown', { key: ']', target: document.body });
  await tick(20);
  assert.match(location.hash, /fid=10/);
});
