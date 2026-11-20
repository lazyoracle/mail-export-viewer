// Pure text helpers that turn untrusted strings into plain segments. The
// caller renders segments with text nodes, never as HTML.

const NAMED = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' };

/** Decodes the HTML character references an escaper produces. */
export function decodeEntities(s) {
  return String(s).replace(/&(#[xX][0-9a-fA-F]+|#[0-9]+|[a-zA-Z]+);/g, (m, ref) => {
    if (ref[0] === '#') {
      const cp = ref[1] === 'x' || ref[1] === 'X' ? parseInt(ref.slice(2), 16) : parseInt(ref.slice(1), 10);
      if (!Number.isFinite(cp) || cp <= 0 || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) return m;
      return String.fromCodePoint(cp);
    }
    return Object.hasOwn(NAMED, ref) ? NAMED[ref] : m;
  });
}

/**
 * Splits a search highlight (escaped text with <mark> tags) into
 * [{text, mark}] segments. Only <mark>/</mark> are interpreted; anything
 * else stays literal text.
 */
export function highlightSegments(html) {
  const out = [];
  if (!html) return out;
  let depth = 0;
  for (const part of String(html).split(/(<\/?mark>)/i)) {
    const tag = part.toLowerCase();
    if (tag === '<mark>') { depth++; continue; }
    if (tag === '</mark>') { depth = Math.max(0, depth - 1); continue; }
    if (!part) continue;
    const text = decodeEntities(part);
    const mark = depth > 0;
    const last = out[out.length - 1];
    if (last && last.mark === mark) last.text += text;
    else out.push({ text, mark });
  }
  return out;
}

const LINK = /\b(?:https?:\/\/[^\s<>"'`]+|mailto:[^\s<>"'`]+)/gi;
const SAFE_PROTOCOLS = new Set(['https:', 'http:', 'mailto:']);
const TRAILING = new Set(['.', ',', ';', ':', '!', '?', "'", '"', '*', '_', ']', '}', '>']);

/** Longer "URLs" stay plain text (and keep the trimming below bounded). */
export const MAX_LINK = 2048;

/**
 * Drops trailing punctuation, and closing parens that have no opening
 * partner, from a URL candidate. Linear: the parens are counted once and
 * the end moves back by index.
 */
function trimLink(s) {
  let open = 0;
  let close = 0;
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '(') open++;
    else if (s[i] === ')') close++;
  }
  let end = s.length;
  while (end > 0) {
    const c = s[end - 1];
    if (TRAILING.has(c)) { end--; continue; }
    if (c === ')' && close > open) { end--; close--; continue; }
    break;
  }
  return s.slice(0, end);
}

function safeHref(s) {
  try {
    const u = new URL(s);
    if (!SAFE_PROTOCOLS.has(u.protocol)) return null;
    if (u.protocol !== 'mailto:' && !u.hostname) return null;
    if (u.protocol === 'mailto:' && !u.pathname) return null;
    return s;
  } catch {
    return null;
  }
}

/** Splits plain text into [{text}] and [{text, href}] link segments. */
export function linkify(text) {
  const out = [];
  const s = String(text || '');
  let last = 0;
  const push = (t) => {
    if (!t) return;
    const prev = out[out.length - 1];
    if (prev && !prev.href) prev.text += t;
    else out.push({ text: t });
  };
  for (const m of s.matchAll(LINK)) {
    if (m[0].length > MAX_LINK) continue;
    const raw = trimLink(m[0]);
    const href = safeHref(raw);
    if (!href) continue;
    push(s.slice(last, m.index));
    out.push({ text: raw, href });
    last = m.index + raw.length;
  }
  push(s.slice(last));
  return out;
}

const WORD = /[\p{L}\p{N}]/u;

// Folds one character the way the search index does: no diacritics,
// lower case.
function fold(ch) {
  return ch.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
}

/**
 * Splits text into [{text, mark}] segments, marking matches of terms that
 * start at a word boundary (prefix matching, as in type-ahead search).
 * Case and diacritics are ignored. Longer terms win; matches never overlap.
 */
export function markTerms(text, terms) {
  const s = String(text || '');
  if (!s) return [];
  // Folded text plus, per folded char, the original [start, end) range.
  let f = '';
  const from = [];
  const to = [];
  for (let i = 0; i < s.length;) {
    const ch = String.fromCodePoint(s.codePointAt(i));
    const end = i + ch.length;
    for (const c of fold(ch)) {
      f += c;
      for (let k = 0; k < c.length; k++) { from.push(i); to.push(end); }
    }
    i = end;
  }
  const folded = [...new Set((terms || []).map((t) => [...String(t)].map(fold).join('').trim()).filter(Boolean))]
    .sort((a, b) => b.length - a.length);
  const ranges = [];
  if (folded.length) {
    for (let j = 0; j < f.length;) {
      const atStart = j === 0 || !WORD.test(f[j - 1]);
      const t = atStart ? folded.find((x) => f.startsWith(x, j)) : null;
      if (t) {
        ranges.push([from[j], to[j + t.length - 1]]);
        j += t.length;
      } else j++;
    }
  }
  const out = [];
  let last = 0;
  for (const [a, b] of ranges) {
    if (a < last) continue;
    if (a > last) out.push({ text: s.slice(last, a), mark: false });
    out.push({ text: s.slice(a, b), mark: true });
    last = b;
  }
  if (last < s.length) out.push({ text: s.slice(last), mark: false });
  return out;
}
