// Message flags (the order and names of /api/facets "flags"): their icons
// on list rows and in the reader, and the query each quick filter adds.

import { h, icon } from './dom.js';

export const FLAGS = [
  { name: 'unread', label: 'Unread', icon: 'dot' },
  { name: 'starred', label: 'Starred', icon: 'star' },
  { name: 'answered', label: 'Answered', icon: 'reply' },
  { name: 'forwarded', label: 'Forwarded', icon: 'forward' },
  { name: 'draft', label: 'Draft', icon: 'pencil' },
  { name: 'deleted', label: 'Deleted', icon: 'trash' },
  { name: 'junk', label: 'Junk', icon: 'junk' },
  { name: 'attachment', label: 'Attachments', icon: 'clip' },
  { name: 'important', label: 'Important', icon: 'bang' },
];

const BY_NAME = new Map(FLAGS.map((f) => [f.name, f]));

export const flagInfo = (name) => BY_NAME.get(name) || null;

/** The [operator, value] that filters by a flag. */
export function flagQuery(name) {
  return name === 'attachment' ? ['has', 'attachment'] : ['is', name];
}

/** Updates which quick-filter buttons in ul are pressed, in place. */
export function markFlags(ul, isPressed) {
  for (const b of ul.querySelectorAll('.flag-btn')) {
    const on = String(Boolean(isPressed(b.dataset.flag)));
    if (b.getAttribute('aria-pressed') !== on) b.setAttribute('aria-pressed', on);
  }
}

/**
 * Icons for a message's flags, each with its name for screen readers.
 * Unread is not an icon: the row is styled instead. null when none.
 */
export function flagMarks(flags, cls = 'row-flags') {
  const shown = FLAGS.filter((f) => f.name !== 'unread' && (flags || []).includes(f.name));
  if (!shown.length) return null;
  return h('span', { class: cls },
    shown.map((f) => h('span', { class: `flag flag-${f.name}`, dataset: { flag: f.name }, title: f.label },
      icon(f.icon), h('span', { class: 'visually-hidden', text: f.label }))));
}
