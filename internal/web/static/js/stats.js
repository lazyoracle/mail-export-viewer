// Statistics view: totals, messages per year, a month
// heatmap, top senders, largest folders and sources. Every bar and cell
// links to the matching search. All names are text.

import { h } from './dom.js';
import { plural, relativeTime } from './format.js';
import { formatValue } from './querychips.js';
import { kindLabel } from './tree.js';

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
const count = (n) => n.toLocaleString('en-US');

/** 0 (none) to 5 (busiest), on a square-root scale so quiet months still show. */
export function heatLevel(c, max) {
  if (!c || !max) return 0;
  return Math.min(5, 1 + Math.floor(Math.sqrt(c / max) * 4.999));
}

/** "YYYY-MM" of the month after month mo (1-12) of year y. */
export function nextMonth(y, mo) {
  return mo === 12 ? `${y + 1}-01` : `${y}-${String(mo + 1).padStart(2, '0')}`;
}

export function senderQuery(s) {
  return `from:${formatValue(s.email || s.name)}`;
}

export function yearSpan(years) {
  if (!years.length) return '';
  if (years.length === 1) return `All from ${years[0].year}`;
  return `From ${years[0].year} to ${years[years.length - 1].year}`;
}

function block(id, title, ...content) {
  return h('section', { class: 'stats-block', attrs: { 'aria-labelledby': id } },
    h('h3', { id, class: 'stats-block-title', text: title }), ...content);
}

function yearsBlock(years, href) {
  const max = Math.max(...years.map((y) => y.count));
  const every = years.length > 24 ? 5 : years.length > 12 ? 2 : 1;
  return block('st-years', 'Messages per year',
    h('ul', { class: 'bars' }, years.map((y, i) => {
      const label = `${y.year}: ${plural(y.count, 'message')}`;
      const fill = h('span', { class: 'bar-fill' });
      fill.style.setProperty('--size', `${Math.max(1.5, (y.count / max) * 100).toFixed(4).replace(/\.?0+$/, '')}%`);
      return h('li', { class: 'bar-item' },
        h('a', { class: 'bar', href: href({ q: `after:${y.year} before:${y.year + 1}` }), title: label, attrs: { 'aria-label': label } },
          h('span', { class: 'bar-count', text: count(y.count), attrs: { 'aria-hidden': 'true' } }),
          h('span', { class: 'bar-track' }, fill),
          h('span', { class: (i % every && i !== years.length - 1) ? 'bar-label is-quiet' : 'bar-label', text: String(y.year), attrs: { 'aria-hidden': 'true' } })));
    })));
}

function monthsBlock(years, months, href) {
  const counts = new Map(months.map((m) => [m.month, m.count]));
  const max = Math.max(...months.map((m) => m.count));
  const grid = h('div', { class: 'heat', attrs: { role: 'table', 'aria-label': 'Messages per month' } },
    h('div', { class: 'heat-row heat-cols', attrs: { role: 'row' } },
      h('span', { class: 'heat-y', attrs: { role: 'columnheader' } }, h('span', { class: 'visually-hidden', text: 'Year' })),
      MONTHS.map((m) => h('span', { class: 'heat-m', text: m.slice(0, 3), title: m, attrs: { role: 'columnheader', 'aria-label': m } }))));
  for (const y of years.map((x) => x.year).reverse()) {
    grid.append(h('div', { class: 'heat-row', attrs: { role: 'row' } },
      h('span', { class: 'heat-y', text: String(y), attrs: { role: 'rowheader' } }),
      MONTHS.map((name, i) => {
        const mo = String(i + 1).padStart(2, '0');
        const c = counts.get(`${y}-${mo}`) || 0;
        const label = `${name} ${y}: ${plural(c, 'message')}`;
        const cell = c
          ? h('a', { class: `heat-c l${heatLevel(c, max)}`, href: href({ q: `after:${y}-${mo} before:${nextMonth(y, i + 1)}` }), title: label, attrs: { 'aria-label': label } })
          : h('span', { class: 'heat-c l0', title: label }, h('span', { class: 'visually-hidden', text: label }));
        return h('span', { class: 'heat-cell', attrs: { role: 'cell' } }, cell);
      })));
  }
  return block('st-months', 'Month by month',
    h('div', { class: 'heat-wrap' }, grid),
    h('p', { class: 'stats-note', text: 'Darker squares hold more mail. Months are in UTC. Select a square to read that month.' }));
}

/** A ranked list of shares: name, optional detail, a bar and the count. */
function shares(id, title, rows, cls) {
  const max = Math.max(...rows.map((r) => r.count));
  return block(id, title, h('ol', { class: 'shares' }, rows.map((r) => {
    const fill = h('span', { class: 'share-fill' });
    fill.style.setProperty('--size', `${((r.count / max) * 100).toFixed(2)}%`);
    return h('li', {},
      h('a', { class: `share ${cls}`, href: r.href, title: r.title },
        r.badge || null,
        h('span', { class: 'share-name sender-name', text: r.name }),
        r.detail ? h('span', { class: 'share-detail', text: r.detail }) : null,
        h('span', { class: 'share-bar', attrs: { 'aria-hidden': 'true' } }, fill),
        h('span', { class: 'share-count', text: count(r.count), attrs: { 'aria-label': plural(r.count, 'message') } })));
  })));
}

function tile(n, label) {
  return h('div', { class: 'stat-tile' }, h('span', { class: 'stat-num', text: count(n) }), h('span', { class: 'stat-label', text: label }));
}

/**
 * Renders /api/stats st into el. opts.href(route) builds the link for a
 * list route ({q}, {folder} or {source}).
 */
export function renderStats(el, st, { href, now = new Date() }) {
  const years = st.byYear || [];
  const notes = [yearSpan(years),
    st.outOfRange ? `${count(st.outOfRange)} with an implausible date (before 1980 or in the future) are not charted` : '']
    .filter(Boolean).join('. ');
  const parts = [
    h('header', { class: 'stats-head' },
      h('h2', { class: 'stats-title', text: plural(st.total || 0, 'message') }),
      notes ? h('p', { class: 'stats-sub', text: `${notes}.` }) : null,
      st.partial ? h('p', { class: 'stats-partial', attrs: { role: 'note' }, text: `Indexing is still running, so these numbers are partial (counted ${relativeTime(st.computedAt, now) || 'just now'}).` }) : null,
      h('div', { class: 'stat-tiles' },
        tile(st.unread || 0, 'unread'), tile(st.withAttachments || 0, 'with attachments'), tile(st.undated || 0, 'without a date'))),
  ];
  if (!st.total) parts.push(h('p', { class: 'stats-empty', text: 'There is no mail to count yet.' }));
  if (years.length) parts.push(yearsBlock(years, href));
  if ((st.byMonth || []).length && years.length) parts.push(monthsBlock(years, st.byMonth, href));
  const side = [];
  if ((st.topSenders || []).length) {
    side.push(shares('st-senders', 'Who wrote most', st.topSenders.map((s) => ({
      name: s.name || s.email, detail: s.name && s.email ? s.email : '', count: s.count,
      href: href({ q: senderQuery(s) }), title: `Show mail from ${s.email || s.name}`,
    })), 'sender'));
  }
  if ((st.byFolder || []).length) {
    side.push(shares('st-folders', 'Largest folders', st.byFolder.map((f) => ({
      name: f.path, count: f.count, href: href({ folder: f.path }), title: `Open ${f.path}`,
    })), 'stat-folder'));
  }
  if ((st.bySource || []).length > 1) {
    side.push(shares('st-sources', 'Sources', st.bySource.map((s) => ({
      name: s.name, count: s.count, href: href({ source: s.id }), title: `Open ${s.name}`,
      badge: h('span', { class: `kind-badge kind-${s.kind}`, text: kindLabel(s.kind) }),
    })), 'stat-source'));
  }
  if (side.length) parts.push(h('div', { class: 'stats-cols' }, side));
  el.replaceChildren(...parts);
}
