import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installFakeDOM } from './fakedom.js';

installFakeDOM();
const { renderStats, heatLevel, nextMonth, senderQuery, yearSpan } = await import('../static/js/stats.js');

const evil = '<img src=x onerror=alert(1)>';
const stats = {
  total: 9, undated: 1, outOfRange: 2, unread: 3, withAttachments: 2,
  byYear: [{ year: 2022, count: 1 }, { year: 2023, count: 8 }],
  byMonth: [{ month: '2022-12', count: 1 }, { month: '2023-01', count: 3 }, { month: '2023-02', count: 5 }],
  topSenders: [{ name: evil, email: 'a@example.com', count: 4 }, { name: 'No Mail', email: '', count: 1 }],
  byFolder: [{ path: 'Archive', count: 5 }, { path: evil, count: 3 }],
  bySource: [{ id: 1, name: 'Archive', kind: 'mbox', count: 5 }, { id: 2, name: 'b.pst', kind: 'pst', count: 4 }],
  partial: true, computedAt: '2026-01-01T12:00:00Z',
};

const href = (r) => `#${JSON.stringify(r)}`;

test('helpers: heat levels, month stepping, sender queries, year span', () => {
  assert.equal(heatLevel(0, 10), 0);
  assert.equal(heatLevel(10, 10), 5);
  assert.ok(heatLevel(1, 10) >= 1);
  assert.equal(nextMonth(2023, 12), '2024-01');
  assert.equal(nextMonth(2023, 2), '2023-03');
  assert.equal(senderQuery({ email: 'a@example.com', name: 'A' }), 'from:a@example.com');
  assert.equal(senderQuery({ email: '', name: 'Ann "x" B' }), 'from:"Ann \\"x\\" B"');
  assert.equal(yearSpan([]), '');
  assert.equal(yearSpan([{ year: 2020 }]), 'All from 2020');
  assert.equal(yearSpan([{ year: 2020 }, { year: 2024 }]), 'From 2020 to 2024');
});

test('renderStats: totals, notes and every section, all text-only', () => {
  const el = document.createElement('div');
  renderStats(el, stats, { href });
  assert.ok(!el.descendants().some((e) => e.tagName === 'IMG'));
  assert.equal(el.querySelector('.stats-title').textContent, '9 messages');
  const tiles = el.querySelectorAll('.stat-tile').map((t) => t.textContent);
  assert.deepEqual(tiles, ['3unread', '2with attachments', '1without a date']);
  assert.match(el.querySelector('.stats-sub').textContent, /2 with an implausible date/);
  assert.ok(el.querySelector('.stats-partial'), 'partial while indexing');
  const years = el.querySelectorAll('.bar');
  assert.equal(years.length, 2);
  assert.equal(years[1].href, href({ q: 'after:2023 before:2024' }));
  assert.equal(years[1].getAttribute('aria-label'), '2023: 8 messages');
  assert.equal(years[1].querySelector('.bar-fill').style.props.get('--size'), '100%');
  const cells = el.querySelectorAll('a.heat-c');
  assert.equal(cells.length, 3);
  assert.ok(cells.some((c) => c.href === href({ q: 'after:2022-12 before:2023-01' })));
  const senders = el.querySelectorAll('.sender');
  assert.equal(senders[0].querySelector('.sender-name').textContent, evil);
  assert.equal(senders[0].href, href({ q: 'from:a@example.com' }));
  const folders = el.querySelectorAll('.stat-folder');
  assert.equal(folders[1].querySelector('.share-name').textContent, evil);
  assert.equal(folders[0].href, href({ folder: 'Archive' }));
  const sources = el.querySelectorAll('.stat-source');
  assert.equal(sources[1].href, href({ source: 2 }));
});

test('renderStats: an empty archive says so', () => {
  const el = document.createElement('div');
  renderStats(el, { ...stats, total: 0, byYear: [], byMonth: [], topSenders: [], byFolder: [], bySource: [], partial: false }, { href });
  assert.equal(el.querySelector('.stats-title').textContent, '0 messages');
  assert.equal(el.querySelector('.bar'), null);
  assert.ok(el.querySelector('.stats-empty'));
});
