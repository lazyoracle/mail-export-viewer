import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installFakeDOM } from './fakedom.js';

installFakeDOM();
const { renderRow } = await import('../static/js/list.js');
const { renderMessage, renderConversation, effectiveView, TEXT_DISPLAY_MAX } = await import('../static/js/reader.js');

const now = new Date('2026-11-20T15:30:00Z');
const evil = '<img src=x onerror=alert(1)>';
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));

function item(extra = {}) {
  return {
    id: 7, threadId: 7, threadCount: 1, sourceId: 1, sourceKind: 'mbox', folderId: 3, folder: 'Inbox',
    subject: 'Hello', from: { name: 'Ann', email: 'ann@example.com' }, toText: 'Bob <bob@example.com>',
    date: '2026-11-20T09:00:00Z', flags: [], hasAttachment: false, labels: [], categories: [], tags: [],
    snippet: 'snip', ...extra,
  };
}

test('renderRow puts every string in text nodes', () => {
  const li = renderRow(item({ subject: evil, snippet: evil, from: { name: evil, email: '' }, folder: evil }), now, [], { showFolder: true });
  assert.equal(li.getAttribute('role'), 'option');
  assert.equal(li.id, 'msg-7');
  assert.ok(!li.descendants().some((e) => e.tagName === 'IMG'), 'no element parsed from text');
  assert.equal(li.querySelector('.row-subject').textContent, evil);
  assert.equal(li.querySelector('.row-snippet').textContent, evil);
  assert.equal(li.querySelector('.row-from').textContent, evil);
  assert.equal(li.querySelector('.row-folder').textContent, evil);
});

test('renderRow: sender falls back to email, empty subject is labelled, folder only on request', () => {
  const li = renderRow(item({ subject: '', from: { name: '', email: 'bob@example.com' } }), now);
  assert.equal(li.querySelector('.row-from').textContent, 'bob@example.com');
  assert.equal(li.querySelector('.row-subject').textContent, '(no subject)');
  assert.ok(li.querySelector('.row-subject').classList.contains('is-empty'));
  assert.equal(li.querySelector('.row-folder'), null);
});

test('renderRow: unread style, flag icons, thread badge', () => {
  const plain = renderRow(item(), now);
  assert.ok(!plain.classList.contains('is-unread'));
  assert.equal(plain.querySelector('.row-flags'), null);
  assert.equal(plain.querySelector('.row-thread'), null);
  const li = renderRow(item({ flags: ['unread', 'starred', 'attachment'], hasAttachment: true, threadCount: 4 }), now);
  assert.ok(li.classList.contains('is-unread'));
  assert.ok(li.querySelector('.unread-dot'));
  assert.deepEqual(li.querySelectorAll('.flag').map((f) => f.dataset.flag), ['starred', 'attachment']);
  assert.equal(li.querySelector('.row-thread').textContent, '4');
  assert.equal(li.querySelector('.row-thread').getAttribute('aria-label'), '4 messages in conversation');
  assert.ok(renderRow(item({ flags: ['deleted'] }), now).classList.contains('is-deleted'));
});

test('renderRow: highlightHtml becomes <mark> elements and literal text only', () => {
  const li = renderRow(item({ highlightHtml: `a <mark>b</mark> ${evil.replace(/</g, '&lt;')}<script>x</script>` }), now);
  const snip = li.querySelector('.row-snippet');
  assert.deepEqual(snip.children.map((c) => c.tagName), ['MARK']);
  assert.equal(snip.querySelector('mark').textContent, 'b');
  assert.equal(snip.textContent, `a b ${evil}<script>x</script>`);
});

test('renderRow: subject marks come from the query terms; pills for terms', () => {
  const li = renderRow(item({ subject: 'Report <b>due</b>', labels: ['Inbox', 'Work'], tags: [{ name: 'Urgent', color: '#FF0000' }] }), now, ['report', '<b>']);
  const marks = li.querySelector('.row-subject').querySelectorAll('mark');
  assert.deepEqual(marks.map((m) => m.textContent), ['Report', '<b>']);
  assert.ok(!li.descendants().some((e) => e.tagName === 'B'));
  assert.deepEqual(li.querySelectorAll('.pill').map((p) => p.textContent), ['Work', 'Urgent']);
  const t = li.querySelector('time');
  assert.equal(t.getAttribute('datetime'), '2026-11-20T09:00:00Z');
  assert.ok(t.textContent.length > 0);
});

function message(extra = {}) {
  return {
    id: 42, threadId: 42, source: { id: 1, name: 'Takeout.mbox', kind: 'mbox' }, folderId: 3, folder: 'Inbox',
    messageId: 'm@example.com', inReplyTo: '', references: [], subject: 'Notes',
    from: { name: 'Ada', email: 'ada@example.com' }, to: [{ name: '', email: 'c@example.com' }], cc: [], bcc: [],
    date: '2023-01-02T10:00:00Z', flags: ['starred'], labels: ['Inbox', 'Work'], categories: ['Finance'],
    tags: [{ name: 'To do', color: '#FF9900' }], props: [{ key: 'X-Custom', value: evil }],
    headers: [['Subject', 'Notes'], ['X-Evil', evil]],
    text: 'see mailto:ada@example.com now', textTruncated: false, bodyHtml: '<p>Hi</p>', htmlTruncated: false,
    remoteContentBlocked: false, attachments: [], rawUrl: '/api/messages/42/raw', parseError: false, ...extra,
  };
}

function render(msg, view = 'html', more = {}) {
  const article = document.createElement('article');
  const calls = [];
  renderMessage(article, msg, { view, onView: (v) => calls.push(v), onBack() {}, now, ...more });
  return { article, calls };
}

test('HTML body goes into a sandboxed srcdoc iframe without scripts or same-origin', () => {
  const { article } = render(message());
  const frames = article.querySelectorAll('iframe');
  assert.equal(frames.length, 1);
  const f = frames[0];
  assert.equal(f.getAttribute('sandbox'), 'allow-popups allow-popups-to-escape-sandbox');
  assert.equal(f.getAttribute('referrerpolicy'), 'no-referrer');
  assert.match(f.srcdoc, /<body><p>Hi<\/p><\/body>/);
  assert.equal(f.src, undefined, 'never loads a URL');
});

test('notices: remote content, truncation, parse error', () => {
  assert.equal(render(message()).article.querySelector('.notice'), null);
  const n = render(message({ remoteContentBlocked: true })).article.querySelector('.notice-remote');
  assert.equal(n.textContent, 'Remote images and content were blocked. This viewer never loads anything from the internet.');
  assert.equal(render(message({ remoteContentBlocked: true }), 'text').article.querySelector('.notice-remote'), null, 'only beside the HTML');
  assert.ok(render(message({ htmlTruncated: true })).article.querySelector('.notice-truncated'));
  assert.ok(render(message({ textTruncated: true }), 'text').article.querySelector('.notice-truncated'));
  assert.ok(render(message({ parseError: true })).article.querySelector('.notice-parse'));
});

test('header block: subject, sender, recipients, flags, folder and terms', () => {
  const { article } = render(message({ subject: evil, cc: [{ name: 'Mary', email: 'm@example.com' }] }));
  assert.equal(article.querySelector('h2').textContent, evil);
  assert.ok(!article.descendants().some((e) => e.tagName === 'IMG'));
  assert.equal(article.querySelector('.from-name').textContent, 'Ada');
  assert.equal(article.querySelector('.from-email').textContent, 'ada@example.com');
  const rows = article.querySelectorAll('.meta-row').map((r) => r.textContent);
  assert.deepEqual(rows, ['Toc@example.com', 'CcMary <m@example.com>']);
  assert.equal(article.querySelector('.msg-where').textContent, 'mboxTakeout.mbox › Inbox');
  const where = (src, folder) => render(message({ source: { id: 1, name: src, kind: 'olm' }, folder })).article.querySelector('.where-text').textContent;
  assert.equal(where('backup.olm', 'backup/Inbox'), 'backup.olm › Inbox', 'the file name is not repeated');
  assert.equal(where('Takeout/All mail.mbox', 'Takeout/All mail'), 'Takeout/All mail.mbox', 'an mbox file is its folder');
  assert.equal(where('Mail', 'Archive/2019'), 'Mail › Archive/2019');
  assert.deepEqual(article.querySelector('.msg-flags').querySelectorAll('.flag').map((f) => f.dataset.flag), ['starred']);
  assert.deepEqual(article.querySelectorAll('.pill').map((l) => l.textContent), ['Work', 'Finance', 'To do', 'Inbox']);
  const raw = article.querySelector('a.tool-link');
  assert.equal(raw.href, '/api/messages/42/raw');
});

test('term pills call onTerm', () => {
  const got = [];
  const { article } = render(message(), 'html', { onTerm: (t) => got.push(`${t.kind}:${t.name}`) });
  article.querySelectorAll('.pill')[2].dispatch('click', { stopPropagation() {} });
  assert.deepEqual(got, ['tag:To do']);
});

test('plain-text view linkifies safe links with text nodes', () => {
  const { article } = render(message({ text: `x ${evil} mailto:ada@example.com javascript:alert(1)` }), 'text');
  const pre = article.querySelector('pre');
  const links = pre.querySelectorAll('a');
  assert.equal(links.length, 1);
  assert.equal(links[0].href, 'mailto:ada@example.com');
  assert.equal(links[0].getAttribute('rel'), 'noopener noreferrer');
  assert.equal(pre.textContent, `x ${evil} mailto:ada@example.com javascript:alert(1)`);
  assert.equal(article.querySelector('iframe'), null);
});

test('without HTML the message tab is disabled and text is shown', () => {
  const msg = message({ bodyHtml: '' });
  assert.equal(effectiveView(msg, 'html'), 'text');
  const { article } = render(msg, 'html');
  assert.equal(article.querySelector('iframe'), null);
  assert.ok(article.querySelector('pre'));
  assert.equal(article.querySelector('.tab').disabled, true);
  assert.equal(article.querySelector('.tab[aria-selected="true"]').textContent, 'Plain text');
});

test('tabs call onView', () => {
  const { article, calls } = render(message());
  assert.deepEqual(article.querySelectorAll('.tab').map((t) => t.textContent), ['Message', 'Plain text', 'Headers', 'Properties', 'Raw']);
  article.querySelectorAll('.tab')[2].dispatch('click');
  assert.deepEqual(calls, ['headers']);
});

test('headers view lists every header as text', () => {
  const { article } = render(message(), 'headers');
  const cells = article.querySelectorAll('tr').map((tr) => tr.children.map((c) => c.textContent));
  assert.deepEqual(cells, [['Subject', 'Notes'], ['X-Evil', evil]]);
  assert.match(render(message({ headers: [] }), 'headers').article.querySelector('.muted').textContent, /no transport headers/);
});

test('properties view: where the message came from, ids, and format-specific props as text', () => {
  const { article } = render(message({ inReplyTo: 'p@example.com', references: ['a@example.com', 'p@example.com'] }), 'props');
  const rows = article.querySelectorAll('tr').map((tr) => tr.children.map((c) => c.textContent));
  assert.deepEqual(rows.slice(0, 6), [
    ['Source', 'Takeout.mbox (mbox)'], ['Folder', 'Inbox'], ['Message-ID', 'm@example.com'],
    ['In-Reply-To', 'p@example.com'], ['References', 'a@example.com p@example.com'], ['Index id', '42'],
  ]);
  assert.deepEqual(rows[rows.length - 1], ['X-Custom', evil]);
  assert.ok(!article.descendants().some((e) => e.tagName === 'IMG'));
  assert.match(render(message({ props: [] }), 'props').article.querySelector('.props-none').textContent, /No other properties/);
});

test('attachments: names as text, download links, previews only for raster images', () => {
  const { article } = render(message({
    attachments: [
      { index: 1, filename: evil, contentType: 'image/png', contentId: 'c@x', size: 2048, inline: true, url: '/api/messages/42/attachments/1' },
      { index: 2, filename: 'a.svg', contentType: 'image/svg+xml', contentId: '', size: 10, inline: false, url: '/api/messages/42/attachments/2' },
      { index: 3, filename: '', contentType: 'application/pdf', contentId: '', size: 5, inline: false, url: '/api/messages/42/attachments/3' },
    ],
  }));
  const links = article.querySelectorAll('a.att');
  assert.equal(links.length, 3);
  assert.equal(links[0].href, '/api/messages/42/attachments/1');
  assert.equal(links[0].querySelector('.att-name').textContent, evil);
  assert.equal(links[0].querySelector('img').src, '/api/messages/42/attachments/1?inline=1');
  assert.equal(links[1].querySelector('img'), null, 'no inline SVG');
  assert.equal(links[2].querySelector('.att-name').textContent, 'attachment-3');
  assert.equal(article.querySelector('.attachments-title').textContent, '3 attachments');
});

test('raw view shows the original as text, with a note when truncated', async () => {
  const asked = [];
  const { article } = render(message(), 'raw', {
    loadRaw: async (id) => { asked.push(id); return { text: `From: x\n${evil}`, truncated: true }; },
  });
  assert.equal(article.querySelector('.raw-status').textContent, 'Loading the original message');
  await tick();
  assert.deepEqual(asked, [42]);
  assert.equal(article.querySelector('pre').textContent, `From: x\n${evil}`);
  assert.ok(!article.descendants().some((e) => e.tagName === 'IMG'));
  const note = article.querySelector('.raw-note');
  assert.match(note.textContent, /^Showing the first 2\.0 MB\. Download the original for the whole message\.$/);
  assert.equal(note.querySelector('a').href, '/api/messages/42/raw');
});

test('raw view: errors are shown, aborts are silent', async () => {
  const a1 = render(message(), 'raw', { loadRaw: async () => { throw new Error('gone'); } }).article;
  await tick();
  assert.equal(a1.querySelector('.raw-error').textContent, 'The original message could not be loaded: gone');
  const a2 = render(message(), 'raw', { loadRaw: async () => { throw new DOMException('aborted', 'AbortError'); } }).article;
  await tick();
  assert.equal(a2.querySelector('.raw-error'), null);
  assert.ok(a2.querySelector('.raw-status'));
});

test('toolbar: copy ID and conversation toggle appear only when useful', () => {
  assert.equal(render(message()).article.querySelector('.tool-copy'), null, 'no handler, no button');
  let copied = null;
  let toggled = 0;
  const a = render(message({ messageId: 'abc@x' }), 'text', {
    onCopyId: (id) => { copied = id; }, threadCount: 4, threadView: false, onThread: () => toggled++,
  }).article;
  a.querySelector('.tool-copy').dispatch('click');
  assert.equal(copied, 'abc@x');
  const t = a.querySelector('.tool-thread');
  assert.equal(t.getAttribute('aria-pressed'), 'false');
  assert.equal(t.textContent, 'Conversation (4)');
  t.dispatch('click');
  assert.equal(toggled, 1);
  assert.equal(render(message(), 'text', { threadCount: 1, onThread() {} }).article.querySelector('.tool-thread'), null);
});

test('renderConversation: hostile strings stay text; failed loads say so and can retry', async () => {
  const a = document.createElement('article');
  const thread = [item({ id: 1, from: { name: evil, email: '' }, snippet: evil }), item({ id: 42 })];
  let attempts = 0;
  renderConversation(a, message(), thread, {
    view: 'text', onView() {}, onBack() {}, now,
    loadMessage: async () => { attempts++; throw new Error('gone'); },
  });
  assert.ok(!a.descendants().some((e) => e.tagName === 'IMG'));
  const first = a.querySelectorAll('.convo-item')[0];
  assert.equal(first.querySelector('.convo-from').textContent, evil);
  assert.equal(first.querySelector('.convo-snippet').textContent, evil);
  const cur = a.querySelectorAll('.convo-item')[1];
  assert.equal(cur.getAttribute('aria-current'), 'true');
  assert.equal(cur.querySelector('.msg-body').id, 'msg-body');
  first.querySelector('.convo-toggle').dispatch('click');
  await tick(5);
  assert.equal(first.querySelector('.raw-error').textContent, 'This message could not be opened: gone');
  first.querySelector('.convo-toggle').dispatch('click');
  first.querySelector('.convo-toggle').dispatch('click');
  await tick(5);
  assert.equal(attempts, 2);
});

test('renderConversation: expanded state and loaded bodies survive a redraw', async () => {
  const thread = [item({ id: 1, snippet: 'first' }), item({ id: 42 }), item({ id: 7, snippet: 'third' })];
  const expanded = new Map();
  const loaded = new Map();
  let fetches = 0;
  const opts = {
    view: 'text', onView() {}, onBack() {}, expanded, loaded, now,
    loadMessage: async (id) => { fetches++; return message({ id, text: `Body ${id}`, bodyHtml: '' }); },
  };
  const a = document.createElement('article');
  renderConversation(a, message(), thread, opts);
  a.querySelectorAll('.convo-item')[0].querySelector('.convo-toggle').dispatch('click');
  await tick(5);
  assert.equal(fetches, 1);
  renderConversation(a, message(), thread, { ...opts, view: 'headers' });
  const items = a.querySelectorAll('.convo-item');
  assert.deepEqual(items.map((e) => e.querySelector('.convo-toggle').getAttribute('aria-expanded')), ['true', 'true', 'false']);
  assert.equal(items[0].querySelector('pre').textContent, 'Body 1');
  assert.equal(fetches, 1, 'no refetch on redraw');
  items[1].querySelector('.convo-toggle').dispatch('click');
  renderConversation(a, message(), thread, opts);
  assert.equal(a.querySelectorAll('.convo-item')[1].querySelector('.convo-toggle').getAttribute('aria-expanded'), 'false');
  expanded.set(7, true);
  renderConversation(a, message(), thread, opts);
  await tick(5);
  assert.equal(fetches, 2);
  assert.equal(a.querySelectorAll('.convo-item')[2].querySelector('pre').textContent, 'Body 7');
});

test('renderConversation: a capped thread says so', () => {
  const a = document.createElement('article');
  const thread = [item({ id: 1, threadCount: 6000 }), item({ id: 42, threadCount: 6000 })];
  renderConversation(a, message(), thread, { view: 'text', onView() {}, onBack() {}, now, truncated: true, loadMessage: async () => message() });
  assert.match(a.querySelector('.notice-truncated').textContent, /6,000 messages/);
  assert.equal(a.querySelector('.convo-count').textContent, 'Showing 2 of 6,000 messages in this conversation');
  const b = document.createElement('article');
  renderConversation(b, message(), thread.slice(0, 2), { view: 'text', onView() {}, onBack() {}, now, loadMessage: async () => message() });
  assert.equal(b.querySelector('.notice-truncated'), null);
});

test('attachment links are built from the message id and index, never taken from the payload', () => {
  const { article } = render(message({
    attachments: [{ index: 2, filename: 'x.png', contentType: 'image/png', contentId: '', size: 1, inline: false, url: 'javascript:alert(1)//' },
      { index: 3, filename: 'y.pdf', contentType: 'application/pdf', contentId: '', size: 1, inline: false, url: 'https://evil.example/' }],
  }));
  const links = article.querySelectorAll('a.att');
  assert.deepEqual(links.map((a) => a.href), ['/api/messages/42/attachments/2', '/api/messages/42/attachments/3']);
  assert.equal(links[0].querySelector('img').src, '/api/messages/42/attachments/2?inline=1');
});

test('renderConversation mounts a long thread in chunks', () => {
  const thread = Array.from({ length: 450 }, (_, i) => item({ id: i + 1, threadCount: 450 }));
  const tasks = [];
  const a = document.createElement('article');
  renderConversation(a, message({ id: 3 }), thread, { view: 'text', onView() {}, onBack() {}, now, loadMessage: async () => message(), schedule: (fn) => tasks.push(fn) });
  const count = () => a.querySelectorAll('.convo-item').length;
  assert.equal(count(), 200, 'first chunk only');
  assert.equal(a.querySelector('.convo-item.is-current').dataset.id, '3');
  while (tasks.length) tasks.shift()();
  assert.equal(count(), 450);
  assert.deepEqual(a.querySelectorAll('.convo-item').slice(0, 3).map((e) => e.dataset.id), ['1', '2', '3'], 'order kept');
  // A redraw cancels the old render's pending chunks.
  const t2 = [];
  renderConversation(a, message({ id: 3 }), thread, { view: 'text', onView() {}, onBack() {}, now, loadMessage: async () => message(), schedule: (fn) => t2.push(fn) });
  renderConversation(a, message({ id: 3 }), thread.slice(0, 2), { view: 'text', onView() {}, onBack() {}, now, loadMessage: async () => message() });
  while (t2.length) t2.shift()();
  assert.equal(count(), 2);
});

test('a very long text body is shown in part, with a notice (issue #23)', () => {
  const long = 'q'.repeat(TEXT_DISPLAY_MAX + 5000);
  const { article } = render(message({ text: long }), 'text');
  const pre = article.querySelector('pre.plain');
  assert.equal(pre.textContent.length, TEXT_DISPLAY_MAX);
  const note = article.querySelector('.text-note');
  assert.ok(note, 'notice shown');
  assert.ok(note.textContent.includes('Download the original'));
  const { article: short } = render(message({ text: 'short' }), 'text');
  assert.equal(short.querySelector('.text-note'), null);
});
