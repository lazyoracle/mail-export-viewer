// Gmail labels, Outlook categories and Thunderbird tags ("terms"): pills on
// rows and in the reader, and the sidebar lists. Every name is text
// (textContent, title, data-*); nothing parses HTML.

import { h } from './dom.js';
import { avatarHue, plural, compactCount } from './format.js';

// Labels Gmail itself sets (Takeout spells them like this). They are shown
// like any other label, only quieter and after the user's own.
const SYSTEM = new Set([
  'Inbox', 'Sent', 'Important', 'Starred', 'Unread', 'Opened', 'Archived', 'Draft', 'Drafts',
  'Spam', 'Trash', 'Chat', 'Chats', 'Snoozed', 'Scheduled', 'Muted',
]);

export const KIND_NAMES = { label: 'Label', category: 'Category', tag: 'Tag' };

/** Whether name is one of Gmail's own labels (exact, case-sensitive). */
export function isSystemLabel(name) {
  return SYSTEM.has(name) || /^Category [A-Z]/.test(name);
}

/** User labels first, then system labels, each group in its given order. */
export function orderLabels(names) {
  const list = names || [];
  return [...list.filter((n) => !isSystemLabel(n)), ...list.filter(isSystemLabel)];
}

/** c when it is a "#RRGGBB" colour, else ''. */
export function safeColor(c) {
  return /^#[0-9a-f]{6}$/i.test(String(c || '')) ? c : '';
}

/** A message's terms as {kind, name, color}: user labels, categories, tags, system labels. */
export function itemTerms(item) {
  const labels = (item && item.labels) || [];
  const user = labels.filter((n) => !isSystemLabel(n)).map((name) => ({ kind: 'label', name, color: '' }));
  const sys = labels.filter(isSystemLabel).map((name) => ({ kind: 'label', name, color: '' }));
  const cats = ((item && item.categories) || []).map((name) => ({ kind: 'category', name, color: '' }));
  const tags = ((item && item.tags) || []).map((t) => ({ kind: 'tag', name: t.name, color: safeColor(t.color) }));
  return [...user, ...cats, ...tags, ...sys];
}

function tint(el, t) {
  const c = safeColor(t.color);
  if (c) el.style.setProperty('--term-color', c);
  else el.style.setProperty('--hue', String(avatarHue(t.name)));
  return el;
}

/**
 * A term pill: a button that filters by the term; onClick(term) handles
 * clicks. interactive: false makes a plain span instead, for list rows:
 * rows are options in a listbox, which must not contain controls (the
 * keyboard path to a term is the reader or the sidebar; a mouse click on
 * a row pill still filters, through the list's delegated handler).
 */
export function pill(t, { interactive = true, onClick = null } = {}) {
  const cls = ['pill', `pill-${t.kind}`];
  if (t.kind === 'label' && isSystemLabel(t.name)) cls.push('is-system');
  if (safeColor(t.color)) cls.push('has-color');
  const b = h(interactive ? 'button' : 'span', {
    class: cls.join(' '),
    type: interactive ? 'button' : null,
    title: `${KIND_NAMES[t.kind]}: ${t.name}`,
    dataset: { kind: t.kind, name: t.name },
    on: interactive && onClick ? { click: (e) => { if (e && e.stopPropagation) e.stopPropagation(); onClick(t); } } : null,
  }, h('span', { class: 'pill-dot', attrs: { 'aria-hidden': 'true' } }), h('span', { class: 'pill-text', text: t.name }));
  return tint(b, t);
}

/** List-row pills: up to max, then "+N" for the rest. */
export function rowPills(item, max = 2) {
  const terms = itemTerms(item);
  if (!terms.length) return null;
  const shown = terms.slice(0, max);
  const rest = terms.slice(max);
  return h('span', { class: 'row-labels' },
    shown.map((t) => pill(t, { interactive: false })),
    rest.length ? h('span', {
      class: 'pill-more',
      text: `+${rest.length}`,
      title: rest.map((t) => t.name).join('\n'),
      attrs: { 'aria-label': `${plural(rest.length, 'more label')}: ${rest.map((t) => t.name).join(', ')}` },
    }) : null);
}

/** Terms whose name contains text (case-insensitive); all for blank text. */
export function filterTerms(terms, text) {
  const t = String(text || '').trim().toLocaleLowerCase();
  if (!t) return terms;
  return terms.filter((l) => l.name.toLocaleLowerCase().includes(t));
}

function sideTerm(kind, t, active) {
  const b = h('button', {
    class: 'side-term',
    type: 'button',
    dataset: { kind, name: t.name },
    title: t.name,
    attrs: { 'aria-pressed': String(Boolean(active)) },
  },
  h('span', { class: 'side-dot', attrs: { 'aria-hidden': 'true' } }),
  h('span', { class: 'side-name', text: t.name }),
  t.unread ? h('span', { class: 'side-unread', text: compactCount(t.unread), attrs: { 'aria-label': `${t.unread.toLocaleString()} unread` } }) : null,
  h('span', { class: 'side-count', text: compactCount(t.count), attrs: { 'aria-label': plural(t.count, 'message') } }));
  if (kind === 'label' && isSystemLabel(t.name)) b.classList.add('is-system');
  return h('li', {}, tint(b, t));
}

/**
 * Fills ul with sidebar entries for terms ([{name, color, count, unread}],
 * sorted by name) of one kind, narrowed by opts.filter; opts.isActive(name)
 * marks the ones the query filters by. Gmail's own labels go last.
 * Returns the number shown.
 */
export function renderTermList(ul, kind, terms, { isActive = () => false, filter = '' } = {}) {
  const shown = filterTerms(terms || [], filter);
  const sysOf = (t) => kind === 'label' && isSystemLabel(t.name);
  const user = shown.filter((t) => !sysOf(t));
  const sys = shown.filter(sysOf);
  const items = user.map((t) => sideTerm(kind, t, isActive(t.name)));
  if (user.length && sys.length) items.push(h('li', { class: 'term-sep', attrs: { role: 'separator', 'aria-label': 'Gmail labels' } }));
  for (const t of sys) items.push(sideTerm(kind, t, isActive(t.name)));
  if (!shown.length) items.push(h('li', { class: 'term-none', text: 'Nothing matches' }));
  ul.replaceChildren(...items);
  return shown.length;
}

/** Updates which entries of a rendered list are pressed, in place. */
export function markActiveTerms(ul, isActive) {
  for (const b of ul.querySelectorAll('.side-term')) {
    const on = String(Boolean(isActive(b.dataset.name)));
    if (b.getAttribute('aria-pressed') !== on) b.setAttribute('aria-pressed', on);
  }
}

/** Above this many terms a sidebar section offers a filter box. */
export const FILTER_FROM = 12;
