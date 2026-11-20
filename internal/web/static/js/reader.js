// Message view: header block, view tabs, notices, attachments, and the
// body (sandboxed HTML, plain text, headers, properties or the raw source).

import { h, icon, clear, append } from './dom.js';
import {
  formatFullDate, formatListDate, formatSize, senderLabel, addressList, isInlineImage, plural, relativeTime, isoLocal,
} from './format.js';
import { avatar } from './list.js';
import { pill, itemTerms } from './labels.js';
import { flagMarks } from './flags.js';
import { kindLabel } from './tree.js';
import { linkify } from './text.js';
import { buildSrcdoc } from './srcdoc.js';
import { api, RAW_DISPLAY_MAX } from './api.js';

export const VIEWS = [
  { id: 'html', label: 'Message' },
  { id: 'text', label: 'Plain text' },
  { id: 'headers', label: 'Headers' },
  { id: 'props', label: 'Properties' },
  { id: 'raw', label: 'Raw' },
];

/** The view to show for msg given the preferred one. */
export function effectiveView(msg, preferred) {
  if (preferred === 'html' && !msg.bodyHtml) return 'text';
  return VIEWS.some((v) => v.id === preferred) ? preferred : 'html';
}

function addrRow(label, addrs) {
  const text = addressList(addrs);
  if (!text) return null;
  return h('div', { class: 'meta-row' }, h('span', { class: 'meta-key', text: label }), h('span', { class: 'meta-val', text }));
}

/**
 * Most characters of a text body shown in the page. Laying out megabytes
 * of text (worst: one huge line) freezes the browser for seconds; the
 * download is always whole.
 */
export const TEXT_DISPLAY_MAX = 512 * 1024;

function linkifiedText(msg) {
  const text = msg.text.length > TEXT_DISPLAY_MAX ? msg.text.slice(0, TEXT_DISPLAY_MAX) : msg.text;
  const pre = h('pre', { class: 'plain' });
  for (const seg of linkify(text)) {
    pre.append(seg.href
      ? h('a', { href: seg.href, text: seg.text, attrs: { target: '_blank', rel: 'noopener noreferrer' } })
      : document.createTextNode(seg.text));
  }
  if (text === msg.text) return pre;
  return h('div', {}, pre, h('p', { class: 'notice text-note', attrs: { role: 'note' } },
    h('span', { text: `Showing the first ${formatSize(TEXT_DISPLAY_MAX)} of the text. ` }),
    h('a', { href: api.rawURL(msg.id), text: 'Download the original' }),
    h('span', { text: ' to read all of it.' })));
}

function htmlBody(msg) {
  const frame = h('iframe', {
    class: 'html-frame',
    title: 'Message body',
    attrs: { sandbox: 'allow-popups allow-popups-to-escape-sandbox', referrerpolicy: 'no-referrer' },
  });
  frame.srcdoc = buildSrcdoc(msg.bodyHtml);
  return frame;
}

function kvTable(caption, rows, cls) {
  return h('table', { class: cls },
    h('caption', { class: 'visually-hidden', text: caption }),
    h('tbody', {}, rows.map(([k, v]) => h('tr', {}, h('th', { scope: 'row', text: k }), h('td', { text: v })))));
}

function headersBody(msg) {
  const rows = msg.headers || [];
  return h('div', { class: 'headers-wrap' },
    rows.length ? kvTable('All message headers', rows, 'headers')
      : h('p', { class: 'muted', text: 'This message has no transport headers (common for Outlook items).' }));
}

function propsBody(msg) {
  const src = msg.source || {};
  const base = [
    ['Source', src.name ? `${src.name} (${kindLabel(src.kind)})` : ''],
    ['Folder', msg.folder || ''],
    ['Message-ID', msg.messageId || ''],
    ['In-Reply-To', msg.inReplyTo || ''],
    ['References', (msg.references || []).join(' ')],
    ['Index id', String(msg.id)],
  ].filter(([, v]) => v);
  const props = (msg.props || []).map((p) => [p.key, p.value]);
  return h('div', { class: 'headers-wrap props-wrap' },
    kvTable('Message properties', [...base, ...props], 'headers props'),
    props.length ? null : h('p', { class: 'muted props-none', text: 'No other properties were stored for this message.' }));
}

/** Raw view: filled in when loadRaw(id) resolves to {text, truncated}. */
function rawBody(msg, loadRaw) {
  const wrap = h('div', { class: 'raw-wrap' },
    h('p', { class: 'muted raw-status', text: 'Loading the original message', attrs: { role: 'status' } }));
  Promise.resolve()
    .then(() => loadRaw(msg.id))
    .then(({ text, truncated }) => {
      clear(wrap).append(h('pre', { class: 'raw', text }));
      if (truncated) {
        wrap.append(h('p', { class: 'notice raw-note', attrs: { role: 'note' } },
          h('span', { text: `Showing the first ${formatSize(RAW_DISPLAY_MAX)}. ` }),
          h('a', { href: api.rawURL(msg.id), text: 'Download the original' }),
          h('span', { text: ' for the whole message.' })));
      }
    }, (e) => {
      if (e && e.name === 'AbortError') return;
      clear(wrap).append(h('p', { class: 'raw-error', attrs: { role: 'alert' }, text: `The original message could not be loaded: ${e && e.message}` }));
    });
  return wrap;
}

function attachmentList(msg) {
  const atts = msg.attachments || [];
  if (!atts.length) return null;
  return h('section', { class: 'attachments', attrs: { 'aria-label': plural(atts.length, 'attachment') } },
    h('h3', { class: 'attachments-title', text: plural(atts.length, 'attachment') }),
    h('ul', { class: 'att-list' }, atts.map((a) => {
      const name = a.filename || `attachment-${a.index}`;
      // Built from the ids rather than taken from the payload, so a link can
      // only ever point at this message's attachment on this origin.
      const url = `/api/messages/${encodeURIComponent(msg.id)}/attachments/${encodeURIComponent(a.index)}`;
      const thumb = isInlineImage(a.contentType)
        ? h('img', { class: 'att-thumb', src: `${url}?inline=1`, alt: '', loading: 'lazy', decoding: 'async' })
        : h('span', { class: 'att-thumb att-generic' }, icon('file'));
      return h('li', {},
        h('a', { class: 'att', href: url, title: `Download ${name}` },
          thumb,
          h('span', { class: 'att-text' },
            h('span', { class: 'att-name', text: name }),
            h('span', { class: 'att-size', text: [formatSize(a.size), a.contentType, a.inline ? 'inline' : ''].filter(Boolean).join(', ') })),
          icon('download', 'icon att-dl')));
    })));
}

/** The message body element for a view. */
function bodyFor(msg, current, loadRaw) {
  const body = h('div', { class: `msg-body view-${current}` });
  if (current === 'html') body.append(htmlBody(msg));
  else if (current === 'text') body.append(msg.text ? linkifiedText(msg) : h('p', { class: 'muted empty-body', text: 'This message has no text.' }));
  else if (current === 'headers') body.append(headersBody(msg));
  else if (current === 'props') body.append(propsBody(msg));
  else body.append(rawBody(msg, loadRaw));
  return body;
}

/** Full date, with the relative age beside it and ISO local time as a tooltip. */
function dateBlock(iso, now) {
  const rel = relativeTime(iso, now);
  return h('time', { class: 'msg-date', title: isoLocal(iso) || null, attrs: { datetime: iso || null } },
    h('span', { text: formatFullDate(iso) }),
    rel ? h('span', { class: 'msg-ago', text: rel }) : null);
}

function metaBlock(msg, now) {
  const from = msg.from || {};
  return h('div', { class: 'msg-meta' },
    avatar(from),
    h('div', { class: 'meta-main' },
      h('div', { class: 'meta-from' },
        h('span', { class: 'from-name', text: senderLabel(from) }),
        from.name && from.email ? h('span', { class: 'from-email', text: from.email }) : null),
      addrRow('To', msg.to), addrRow('Cc', msg.cc), addrRow('Bcc', msg.bcc)),
    dateBlock(msg.date, now));
}

const stripExt = (s) => String(s || '').replace(/\.[^./]*$/, '');

/**
 * "source › folder", without repeating the file: a PST/OLM folder path
 * starts with the file's base name, and an mbox file is usually its folder.
 */
export function whereText(src, folder) {
  const name = (src && src.name) || '';
  let f = folder || '';
  const stem = stripExt(name);
  if (f === stem) f = '';
  const base = stem.split('/').pop();
  if (base && f.startsWith(`${base}/`)) f = f.slice(base.length + 1);
  return [name, f].filter(Boolean).join(' › ');
}

/** Where the message lives (source and folder), its flags and its terms. */
function tagsBlock(msg, onTerm) {
  const src = msg.source || {};
  const terms = itemTerms(msg);
  return h('div', { class: 'msg-tags' },
    h('span', { class: 'msg-where', title: 'Source file and folder' },
      src.kind ? h('span', { class: `kind-badge kind-${src.kind}`, text: kindLabel(src.kind) }) : null,
      h('span', { class: 'where-text', text: whereText(src, msg.folder) })),
    flagMarks(msg.flags, 'msg-flags'),
    terms.length ? h('ul', { class: 'labels', attrs: { 'aria-label': 'Labels, categories and tags' } },
      terms.map((t) => h('li', {}, pill(t, { onClick: onTerm })))) : null);
}

function notice(cls, iconName, text) {
  return h('p', { class: `notice ${cls}`, attrs: { role: 'note' } }, icon(iconName), h('span', { text }));
}

function notices(msg, current) {
  return [
    msg.remoteContentBlocked && current === 'html'
      ? notice('notice-remote', 'shield', 'Remote images and content were blocked. This viewer never loads anything from the internet.') : null,
    (current === 'html' && msg.htmlTruncated) || (current === 'text' && msg.textTruncated)
      ? notice('notice-truncated', 'file', 'This body is very long and was cut at 8 MB. Download the original to read all of it.') : null,
    msg.parseError ? notice('notice-parse', 'file', 'Part of this message could not be read when it was indexed. What could be read is shown.') : null,
  ];
}

function viewTabs(msg, current, onView) {
  const tabs = h('div', { class: 'tabs', attrs: { role: 'tablist', 'aria-label': 'Message view' } },
    VIEWS.map((v) => {
      const disabled = v.id === 'html' && !msg.bodyHtml;
      return h('button', {
        id: `tab-${v.id}`,
        class: 'tab',
        type: 'button',
        text: v.label,
        disabled,
        title: disabled ? 'This message has no HTML part' : null,
        attrs: { role: 'tab', 'aria-selected': String(v.id === current), 'aria-controls': 'msg-body', tabindex: v.id === current ? '0' : '-1' },
        on: { click: () => onView(v.id) },
      });
    }));
  tabs.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const enabled = [...tabs.querySelectorAll('.tab:not([disabled])')];
    const i = enabled.indexOf(document.activeElement);
    const next = enabled[(i + (e.key === 'ArrowRight' ? 1 : enabled.length - 1)) % enabled.length];
    if (next) { next.focus(); next.click(); }
    e.preventDefault();
  });
  return tabs;
}

/**
 * The toolbar: back (narrow screens), view tabs, conversation toggle
 * (when the thread has more than one message), copy Message-ID, download.
 */
function toolbar(msg, current, { onView, onBack, threadCount = 0, threadView = false, onThread, onCopyId }) {
  return h('div', { class: 'msg-toolbar' },
    h('button', { class: 'icon-btn only-narrow', type: 'button', attrs: { 'aria-label': 'Back to message list' }, on: { click: onBack } }, icon('back')),
    viewTabs(msg, current, onView),
    h('div', { class: 'tools' },
      threadCount > 1 && onThread ? h('button', {
        class: 'tool-btn tool-thread',
        type: 'button',
        title: 'Show the whole conversation (t)',
        attrs: { 'aria-pressed': String(threadView) },
        on: { click: onThread },
      }, icon('thread'), h('span', { text: `Conversation (${threadCount})` })) : null,
      msg.messageId && onCopyId ? h('button', {
        class: 'tool-btn tool-copy',
        type: 'button',
        title: `Copy the Message-ID: ${msg.messageId}`,
        on: { click: () => onCopyId(msg.messageId) },
      }, icon('copy'), h('span', { text: 'Copy ID' })) : null,
      h('a', { class: 'tool-link', href: api.rawURL(msg.id), title: 'Download the original message (.eml)' },
        icon('download'), h('span', { text: 'Download .eml' }))));
}

function subjectHeading(msg) {
  return h('h2', { class: msg.subject ? 'msg-subject' : 'msg-subject is-empty', text: msg.subject || '(no subject)', attrs: { tabindex: '-1', id: 'msg-subject' } });
}

function tabPanel(body, current) {
  body.id = 'msg-body';
  body.setAttribute('role', 'tabpanel');
  body.setAttribute('aria-labelledby', `tab-${current}`);
  body.setAttribute('tabindex', '-1');
  return body;
}

/**
 * Renders msg into the article element. opts: view, onView(view) for tab
 * switches, onBack for the narrow-screen back button, loadRaw(id) ->
 * {text, truncated} for the Raw tab, onCopyId(messageId), onTerm(term) for
 * label/category/tag pills, and for the conversation toggle threadCount,
 * threadView and onThread.
 */
export function renderMessage(article, msg, opts) {
  const { view, loadRaw = (id) => api.raw(id), now = new Date() } = opts;
  renders.delete(article); // stop a conversation still being mounted here
  const current = effectiveView(msg, view);
  const body = tabPanel(bodyFor(msg, current, loadRaw), current);
  append(clear(article), [
    h('header', { class: 'msg-head' },
      toolbar(msg, current, opts),
      subjectHeading(msg),
      metaBlock(msg, now),
      tagsBlock(msg, opts.onTerm)),
    notices(msg, current),
    attachmentList(msg),
    body]);
}

/**
 * Conversation view: every message of thread (list items, oldest first) as
 * a collapsible entry; msg (the open one) starts expanded with the chosen
 * view, the others load their bodies on first expand via
 * opts.loadMessage(id) -> Promise<message>.
 *
 * opts.truncated: the server capped the thread (threadCount is its size).
 * opts.expanded (Map id -> bool) and opts.loaded (Map id -> message), when
 * given, carry the user's expand/collapse choices and the bodies already
 * fetched across redraws (a view-tab switch redraws the whole article).
 */
export function renderConversation(article, msg, thread, opts) {
  const { view, loadRaw = (id) => api.raw(id), now = new Date(), loadMessage, onOpen } = opts;
  const expanded = opts.expanded || new Map();
  const loadedMsgs = opts.loaded || new Map();
  const current = effectiveView(msg, view);
  const full = Math.max(thread.length, ...thread.map((it) => it.threadCount || 0));
  const makeItem = (it) => {
    const isCurrent = it.id === msg.id;
    const startOpen = expanded.has(it.id) ? expanded.get(it.id) : isCurrent;
    const panelId = `convo-body-${it.id}`;
    const toggle = h('button', {
      class: 'convo-toggle',
      type: 'button',
      attrs: { 'aria-expanded': String(startOpen), 'aria-controls': panelId },
    },
    avatar(it.from, 'avatar avatar-s'),
    h('span', { class: 'convo-from', text: senderLabel(it.from) }),
    h('span', { class: 'convo-snippet', text: it.snippet || '' }),
    h('time', { class: 'convo-date', text: formatListDate(it.date, now), title: isoLocal(it.date) || null, attrs: { datetime: it.date || null } }));
    const panel = h('div', { class: 'convo-body', id: panelId, hidden: !startOpen });
    const classes = ['convo-item'];
    if (isCurrent) classes.push('is-current');
    if (startOpen) classes.push('is-open');
    if ((it.flags || []).includes('unread')) classes.push('is-unread');
    const li = h('li', { class: classes.join(' '), dataset: { id: String(it.id) }, attrs: { 'aria-current': isCurrent ? 'true' : null } }, toggle, panel);
    let loaded = false;
    const fill = (m, v, isTabbed) => {
      const body = bodyFor(m, v, loadRaw);
      append(clear(panel), [
        metaBlock(m, now),
        tagsBlock(m, opts.onTerm),
        notices(m, v),
        attachmentList(m),
        isTabbed ? tabPanel(body, v) : body,
        !isTabbed && onOpen ? h('p', { class: 'convo-open' }, h('button', { class: 'btn', type: 'button', text: 'Open this message', on: { click: () => onOpen(m.id) } })) : null]);
    };
    const load = () => {
      loaded = true;
      if (loadedMsgs.has(it.id)) {
        const m = loadedMsgs.get(it.id);
        fill(m, effectiveView(m, 'html'), false);
        return;
      }
      panel.append(h('p', { class: 'muted convo-loading', text: 'Loading message', attrs: { role: 'status' } }));
      Promise.resolve().then(() => loadMessage(it.id)).then((m) => {
        loadedMsgs.set(it.id, m);
        fill(m, effectiveView(m, 'html'), false);
      }, (e) => {
        if (e && e.name === 'AbortError') return;
        loaded = false;
        clear(panel).append(h('p', { class: 'raw-error', attrs: { role: 'alert' }, text: `This message could not be opened: ${e && e.message}` }));
      });
    };
    if (isCurrent) { fill(msg, current, true); loaded = true; } else if (startOpen) load();
    toggle.addEventListener('click', () => {
      const open = toggle.getAttribute('aria-expanded') !== 'true';
      expanded.set(it.id, open);
      toggle.setAttribute('aria-expanded', String(open));
      panel.hidden = !open;
      li.classList.toggle('is-open', open);
      if (!open || loaded) return;
      load();
    });
    return li;
  };
  const ol = h('ol', { class: 'convo', attrs: { 'aria-label': 'Conversation' } });
  append(clear(article), [
    h('header', { class: 'msg-head convo-head' },
      toolbar(msg, current, { ...opts, threadCount: thread.length, threadView: true }),
      subjectHeading(msg),
      h('p', { class: 'convo-count', text: opts.truncated
        ? `Showing ${thread.length.toLocaleString('en-US')} of ${plural(full, 'message')} in this conversation`
        : `${plural(thread.length, 'message')} in this conversation` })),
    opts.truncated ? notice('notice-truncated', 'thread', `This conversation has ${plural(full, 'message')}. Only the first ${thread.length.toLocaleString('en-US')} are shown here; search to find the others.`) : null,
    ol]);
  // Long (up to 5000) threads are mounted a chunk per frame so the page
  // stays responsive; a newer render of the same article stops the old one.
  const token = {};
  renders.set(article, token);
  const schedule = opts.schedule || defaultSchedule;
  let next = 0;
  const mount = () => {
    if (renders.get(article) !== token) return;
    const end = Math.min(thread.length, next + CONVO_CHUNK);
    for (; next < end; next++) ol.append(makeItem(thread[next]));
    if (next < thread.length) schedule(mount);
  };
  mount();
}

const CONVO_CHUNK = 200;
const renders = new WeakMap();
const defaultSchedule = (fn) => (typeof requestAnimationFrame === 'function' ? requestAnimationFrame(fn) : setTimeout(fn, 0));

/** A status or error in place of the message, with optional retry. */
export function renderReaderMessage(article, text, { error = false, detail = '', onRetry, onBack } = {}) {
  renders.delete(article);
  clear(article).append(h('div', { class: error ? 'reader-state is-error' : 'reader-state', attrs: { role: error ? 'alert' : 'status' } },
    h('button', { class: 'icon-btn only-narrow reader-state-back', type: 'button', attrs: { 'aria-label': 'Back to message list' }, on: { click: onBack } }, icon('back')),
    h('p', { class: 'reader-state-title', text }),
    detail ? h('p', { class: 'reader-state-detail', text: detail }) : null,
    onRetry ? h('button', { class: 'btn', type: 'button', text: 'Try again', on: { click: onRetry } }) : null));
}
