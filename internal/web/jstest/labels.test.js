import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installFakeDOM } from './fakedom.js';

installFakeDOM();
const {
  isSystemLabel, orderLabels, itemTerms, pill, rowPills, renderTermList, filterTerms, safeColor, FILTER_FROM, markActiveTerms,
} = await import('../static/js/labels.js');
const { avatarHue } = await import('../static/js/format.js');

const evil = '<img src=x onerror=alert(1)>';

test('system labels are recognised; user labels come first, order kept', () => {
  for (const l of ['Inbox', 'Sent', 'Important', 'Starred', 'Unread', 'Opened', 'Archived', 'Spam', 'Trash', 'Drafts', 'Chat', 'Category Updates']) {
    assert.ok(isSystemLabel(l), l);
  }
  for (const l of ['Work', 'inbox', 'Categoryless', 'Work/Inbox', 'Trash talk']) assert.ok(!isSystemLabel(l), l);
  assert.deepEqual(orderLabels(['Inbox', 'Work', 'Opened', 'Zed', 'Category Updates', 'Alpha']),
    ['Work', 'Zed', 'Alpha', 'Inbox', 'Opened', 'Category Updates']);
  assert.deepEqual(orderLabels(undefined), []);
});

test('safeColor accepts only #RRGGBB', () => {
  assert.equal(safeColor('#FF9900'), '#FF9900');
  assert.equal(safeColor('#ff990'), '');
  assert.equal(safeColor('red;background:url(x)'), '');
  assert.equal(safeColor(undefined), '');
});

test('itemTerms: user labels, categories, tags, then system labels', () => {
  const t = itemTerms({ labels: ['Inbox', 'Work'], categories: ['Finance'], tags: [{ name: 'To do', color: '#FF0000' }] });
  assert.deepEqual(t.map((x) => [x.kind, x.name, x.color]), [
    ['label', 'Work', ''], ['category', 'Finance', ''], ['tag', 'To do', '#FF0000'], ['label', 'Inbox', ''],
  ]);
  assert.deepEqual(itemTerms({}), []);
});

test('pill: a button naming its kind, text-only, tag colour or a stable hue', () => {
  const p = pill({ kind: 'label', name: 'Work, Projects' });
  assert.equal(p.tagName, 'BUTTON');
  assert.equal(p.type, 'button');
  assert.equal(p.textContent, 'Work, Projects');
  assert.equal(p.title, 'Label: Work, Projects');
  assert.equal(p.dataset.kind, 'label');
  assert.equal(p.dataset.name, 'Work, Projects');
  assert.ok(p.classList.contains('pill'));
  assert.equal(p.style.props.get('--hue'), String(avatarHue('Work, Projects')));
  assert.ok(pill({ kind: 'label', name: 'Inbox' }).classList.contains('is-system'));
  const tag = pill({ kind: 'tag', name: 'Urgent', color: '#CC0000' });
  assert.equal(tag.style.props.get('--term-color'), '#CC0000');
  assert.equal(tag.title, 'Tag: Urgent');
  assert.equal(pill({ kind: 'tag', name: 'x', color: 'url(evil)' }).style.props.has('--term-color'), false);
  assert.equal(pill({ kind: 'category', name: 'x' }).title, 'Category: x');
  const e = pill({ kind: 'label', name: evil });
  assert.equal(e.textContent, evil);
  assert.ok(!e.descendants().some((d) => d.tagName === 'IMG'));
  assert.equal(pill({ kind: 'label', name: 'x' }, { interactive: false }).tagName, 'SPAN');
  let got = null;
  pill({ kind: 'tag', name: 'n' }, { onClick: (t) => { got = t; } }).dispatch('click', { stopPropagation() {} });
  assert.deepEqual([got.kind, got.name], ['tag', 'n']);
});

test('rowPills: up to two pills, then +N with the rest as a tooltip', () => {
  assert.equal(rowPills({ labels: [], categories: [], tags: [] }), null);
  const r = rowPills({ labels: ['Inbox', 'Opened', 'Work'], categories: ['Finance'], tags: [{ name: 'T', color: '' }] });
  assert.deepEqual(r.querySelectorAll('.pill').map((p) => p.textContent), ['Work', 'Finance']);
  const more = r.querySelector('.pill-more');
  assert.equal(more.textContent, '+3');
  assert.equal(more.title, 'T\nInbox\nOpened');
});

const TERMS = [
  { name: 'alpha', color: '', count: 3, unread: 1 }, { name: 'Category Updates', color: '', count: 40, unread: 0 },
  { name: 'Inbox', color: '', count: 120, unread: 0 }, { name: evil, color: '#00FF00', count: 1, unread: 0 },
];

test('renderTermList: entries with counts, the active ones pressed, system labels last', () => {
  const ul = document.createElement('ul');
  const n = renderTermList(ul, 'label', TERMS, { isActive: (name) => name === 'Inbox' });
  assert.equal(n, 4);
  const btns = ul.querySelectorAll('.side-term');
  assert.deepEqual(btns.map((b) => b.dataset.name), ['alpha', evil, 'Category Updates', 'Inbox']);
  assert.ok(ul.querySelector('.term-sep'), 'system labels are set apart');
  assert.equal(btns[3].getAttribute('aria-pressed'), 'true');
  assert.equal(btns[0].getAttribute('aria-pressed'), 'false');
  assert.equal(btns[0].querySelector('.side-count').textContent, '3');
  assert.equal(btns[0].querySelector('.side-unread').textContent, '1');
  assert.equal(btns[1].querySelector('.side-name').textContent, evil);
  assert.ok(!ul.descendants().some((d) => d.tagName === 'IMG'));
});

test('renderTermList: tags show their colour; no system grouping for tags', () => {
  const ul = document.createElement('ul');
  renderTermList(ul, 'tag', TERMS, {});
  assert.equal(ul.querySelector('.term-sep'), null);
  const b = ul.querySelectorAll('.side-term').find((x) => x.dataset.name === evil);
  assert.equal(b.style.props.get('--term-color'), '#00FF00');
});

test('renderTermList filter narrows and says when nothing matches', () => {
  const ul = document.createElement('ul');
  assert.equal(renderTermList(ul, 'label', TERMS, { filter: 'ALP' }), 1);
  assert.equal(renderTermList(ul, 'label', TERMS, { filter: 'zzz' }), 0);
  assert.equal(ul.querySelector('.term-none').textContent, 'Nothing matches');
  assert.deepEqual(filterTerms(TERMS, '  ').length, 4);
  assert.ok(FILTER_FROM > 5);
});

test('markActiveTerms only flips aria-pressed on the existing entries', () => {
  const ul = document.createElement('ul');
  renderTermList(ul, 'label', TERMS, { isActive: (n) => n === 'alpha' });
  const before = ul.querySelectorAll('.side-term');
  markActiveTerms(ul, (n) => n === 'Inbox');
  const after = ul.querySelectorAll('.side-term');
  assert.ok(before.every((b, i) => b === after[i]), 'same elements');
  assert.deepEqual(after.filter((b) => b.getAttribute('aria-pressed') === 'true').map((b) => b.dataset.name), ['Inbox']);
});

test('row pills are not interactive (they sit inside listbox options)', () => {
  const r = rowPills({ labels: ['Work'], categories: [], tags: [] });
  const p = r.querySelector('.pill');
  assert.equal(p.tagName, 'SPAN');
  assert.equal(p.getAttribute('tabindex'), null);
  assert.equal(p.dataset.kind, 'label', 'a mouse click can still filter via delegation');
});
