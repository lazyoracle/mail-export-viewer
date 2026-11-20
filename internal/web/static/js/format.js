// Pure display formatting: dates, sizes, names. No DOM access.

const DAY_MS = 86400000;

function parseDate(iso) {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

// dayNumber is the calendar day of d in timeZone, as days since the epoch,
// so "same day" and "days ago" follow the viewer's wall clock.
function dayNumber(d, timeZone) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone, year: 'numeric', month: 'numeric', day: 'numeric',
  }).formatToParts(d);
  const get = (t) => Number(parts.find((p) => p.type === t).value);
  return Math.floor(Date.UTC(get('year'), get('month') - 1, get('day')) / DAY_MS);
}

function yearOf(d, timeZone) {
  return Number(new Intl.DateTimeFormat('en-US', { timeZone, year: 'numeric' }).format(d));
}

/**
 * Smart short date for list rows: the time today, the weekday within the
 * last six days, month and day earlier this year, a numeric date otherwise.
 * opts: {locale, timeZone} (defaults: the viewer's).
 */
export function formatListDate(iso, now = new Date(), opts = {}) {
  const d = parseDate(iso);
  if (!d) return '';
  const { locale, timeZone } = opts;
  const days = dayNumber(now, timeZone) - dayNumber(d, timeZone);
  let f;
  if (days === 0) f = { hour: 'numeric', minute: '2-digit' };
  else if (days > 0 && days < 7) f = { weekday: 'short' };
  else if (days > 0 && yearOf(d, timeZone) === yearOf(now, timeZone)) f = { month: 'short', day: 'numeric' };
  else f = { year: '2-digit', month: 'numeric', day: 'numeric' };
  return new Intl.DateTimeFormat(locale, { ...f, timeZone }).format(d);
}

/** Full date and time for the message header. */
export function formatFullDate(iso, opts = {}) {
  const d = parseDate(iso);
  if (!d) return 'Unknown date';
  return new Intl.DateTimeFormat(opts.locale, {
    weekday: 'short', year: 'numeric', month: 'short', day: 'numeric',
    hour: 'numeric', minute: '2-digit', timeZone: opts.timeZone,
  }).format(d);
}

/** Human file size in binary units ("1.5 KB", "20 KB", "5.3 MB"). */
export function formatSize(n) {
  if (typeof n !== 'number' || !Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const letter = /\p{L}/u;

function firstLetter(word) {
  for (const ch of word) if (letter.test(ch)) return ch.toLocaleUpperCase();
  return '';
}

/** One or two letters for a sender's monogram. */
export function initials(addr) {
  const name = (addr && addr.name) || '';
  let words = name.split(/[\s"'(),<>]+/).filter((w) => w && letter.test(w));
  if (name.includes(',') && words.length === 2) words = [words[1], words[0]]; // "Last, First"
  if (words.length) {
    const a = firstLetter(words[0]);
    const b = words.length > 1 ? firstLetter(words[words.length - 1]) : '';
    return a + b;
  }
  const email = (addr && addr.email) || '';
  return firstLetter(email.split('@')[0]) || '?';
}

/** List label for a sender: the display name, else the address. */
export function senderLabel(addr) {
  return (addr && (addr.name || addr.email)) || '(unknown sender)';
}

/** "Name <email>", or whichever part exists. */
export function addressLabel(addr) {
  if (!addr) return '';
  if (addr.name && addr.email) return `${addr.name} <${addr.email}>`;
  return addr.name || addr.email || '';
}

export function addressList(addrs) {
  return (addrs || []).map(addressLabel).filter(Boolean).join(', ');
}

/** Stable hue (0-359) derived from a string, for monogram tints. */
export function avatarHue(s) {
  let h = 2166136261;
  for (const ch of String(s || '')) {
    h ^= ch.codePointAt(0);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0) % 360;
}

// Same set the server will serve with Content-Disposition: inline.
const INLINE_IMAGES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/bmp', 'image/avif']);

export function isInlineImage(contentType) {
  const t = String(contentType || '').split(';')[0].trim().toLowerCase();
  return INLINE_IMAGES.has(t);
}

export function plural(n, word) {
  return `${n.toLocaleString('en-US')} ${word}${n === 1 ? '' : 's'}`;
}

/** "3 days ago", "just now", "tomorrow"; '' for a missing date. */
export function relativeTime(iso, now = new Date(), locale) {
  const d = parseDate(iso);
  if (!d) return '';
  const sec = (d.getTime() - now.getTime()) / 1000;
  const abs = Math.abs(sec);
  if (abs < 45) return 'just now';
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  const steps = [[3600, 60, 'minute'], [86400, 3600, 'hour'], [86400 * 26, 86400, 'day'], [86400 * 320, 86400 * 30.44, 'month']];
  for (const [limit, unit, name] of steps) {
    if (abs < limit) return rtf.format(Math.round(sec / unit), name);
  }
  return rtf.format(Math.round(sec / (86400 * 365.25)), 'year');
}

/** Full ISO 8601 date-time with offset in timeZone (default: the viewer's). */
export function isoLocal(iso, timeZone) {
  const d = parseDate(iso);
  if (!d) return '';
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).formatToParts(d);
  const get = (t) => parts.find((p) => p.type === t).value;
  const wall = Date.UTC(Number(get('year')), Number(get('month')) - 1, Number(get('day')),
    Number(get('hour')), Number(get('minute')), Number(get('second')));
  const off = Math.round((wall - Math.floor(d.getTime() / 1000) * 1000) / 60000);
  const sign = off < 0 ? '-' : '+';
  const pad = (n) => String(n).padStart(2, '0');
  const a = Math.abs(off);
  return `${get('year')}-${get('month')}-${get('day')}T${get('hour')}:${get('minute')}:${get('second')}${sign}${pad(Math.floor(a / 60))}:${pad(a % 60)}`;
}
