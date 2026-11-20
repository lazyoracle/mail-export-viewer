// Message list rows.

import { h } from './dom.js';
import { formatListDate, formatFullDate, isoLocal, senderLabel, initials, avatarHue } from './format.js';
import { highlightSegments, markTerms } from './text.js';
import { rowPills } from './labels.js';
import { flagMarks } from './flags.js';

/** Snippet content: the search highlight (only <mark> honoured) or the plain snippet. */
function snippetNodes(item) {
  if (!item.highlightHtml) return [item.snippet || ''];
  return highlightSegments(item.highlightHtml).map((s) => (s.mark ? h('mark', { text: s.text }) : s.text));
}

/** Text with the given search terms wrapped in <mark> elements. */
export function markedNodes(text, terms) {
  if (!terms || !terms.length) return [text];
  return markTerms(text, terms).map((s) => (s.mark ? h('mark', { text: s.text }) : s.text));
}

export function rowId(id) {
  return `msg-${id}`;
}

/** A sender monogram with a stable tint. */
export function avatar(from, cls = 'avatar') {
  const el = h('span', { class: cls, text: initials(from), attrs: { 'aria-hidden': 'true' } });
  el.style.setProperty('--hue', String(avatarHue((from && (from.email || from.name)) || '')));
  return el;
}

/**
 * One list row. terms: words from the search query to mark in the subject.
 * opts.showFolder adds the folder path (for lists spanning folders).
 */
export function renderRow(item, now, terms = [], { showFolder = false } = {}) {
  const flags = item.flags || [];
  const unread = flags.includes('unread');
  const from = senderLabel(item.from);
  const subject = item.subject || '';
  const count = item.threadCount > 1 ? item.threadCount : 0;
  const cls = ['row'];
  if (unread) cls.push('is-unread');
  if (flags.includes('deleted')) cls.push('is-deleted');
  return h('li', {
    id: rowId(item.id),
    class: cls.join(' '),
    attrs: { role: 'option', 'aria-selected': 'false' },
    dataset: { id: String(item.id) },
  },
  unread ? h('span', { class: 'unread-dot', title: 'Unread' }, h('span', { class: 'visually-hidden', text: 'Unread. ' })) : null,
  avatar(item.from, 'avatar avatar-s'),
  h('div', { class: 'row-main' },
    h('div', { class: 'row-top' },
      h('span', { class: 'row-from', text: from, title: item.from && item.from.email ? item.from.email : null }),
      count ? h('span', {
        class: 'row-thread',
        text: String(count),
        title: `${count} messages in conversation`,
        attrs: { 'aria-label': `${count} messages in conversation` },
      }) : null,
      flagMarks(flags),
      h('time', {
        class: 'row-date',
        text: formatListDate(item.date, now),
        title: item.date ? `${formatFullDate(item.date)}\n${isoLocal(item.date)}` : null,
        attrs: { datetime: item.date || null },
      })),
    h('div', { class: 'row-line' },
      h('div', { class: subject ? 'row-subject' : 'row-subject is-empty' }, subject ? markedNodes(subject, terms) : '(no subject)'),
      rowPills(item)),
    h('div', { class: 'row-snippet' }, snippetNodes(item)),
    showFolder && item.folder ? h('div', { class: 'row-folder', text: item.folder }) : null));
}
