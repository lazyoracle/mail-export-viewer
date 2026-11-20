// Pure query-string helpers for the search box: the chips under it, the
// sidebar filters that add or remove operators, and operator autocomplete.
// The tokenizer mirrors the server's parser (internal/index/query.go):
//   - "quoted phrases", -negation, operator:value and operator:"quoted value"
//   - a malformed operator value (before:soon, has:pdf, is:purple) is plain text
//   - inside quotes \" is a literal double quote and \\ a literal
//     backslash; any other backslash is kept (as on the server)

/** The operators, in the order the help popover and autocomplete show them. */
export const OPERATORS = [
  { key: 'from', help: 'Sender name or address', example: 'from:ann' },
  { key: 'to', help: 'A recipient (To or Cc)', example: 'to:team@example.com' },
  { key: 'cc', help: 'A Cc recipient', example: 'cc:bob' },
  { key: 'subject', help: 'Words in the subject', example: 'subject:invoice' },
  { key: 'folder', help: 'A folder path or its last part', example: 'folder:Archive' },
  { key: 'label', help: 'A Gmail label or an Outlook category', example: 'label:Work' },
  { key: 'category', help: 'An Outlook category', example: 'category:Finance' },
  { key: 'tag', help: 'A Thunderbird tag', example: 'tag:"To do"' },
  { key: 'is', help: 'unread, read, starred, answered, forwarded, draft, deleted, junk, important', example: 'is:unread' },
  { key: 'has', help: 'Messages with attachments', example: 'has:attachment' },
  { key: 'filename', help: 'Part of an attachment name', example: 'filename:pdf' },
  { key: 'after', help: 'On or after a date (YYYY, YYYY-MM or YYYY-MM-DD)', example: 'after:2024-03' },
  { key: 'before', help: 'Before a date (that date not included)', example: 'before:2025' },
];

const KEYS = new Set([...OPERATORS.map((o) => o.key), 'mailbox']);

// is: values, canonical names first, then the aliases the server accepts.
export const IS_VALUES = ['unread', 'read', 'starred', 'answered', 'forwarded', 'draft', 'deleted', 'junk', 'important', 'attachment'];
const IS_ALIASES = {
  flagged: 'starred', replied: 'answered', spam: 'junk', trash: 'deleted', unseen: 'unread',
  seen: 'read', 'has-attachment': 'attachment',
};
const HAS_VALUES = { attachment: 'attachment', attachments: 'attachment', att: 'attachment' };

const DATE = /^(\d{4})(?:[-/](\d{2})(?:[-/](\d{2}))?)?$/;
const SPACE = /[ \t\n\r\f\v]/;
const TOKEN_CHAR = /[\p{L}\p{N}\p{Co}]/u;

/** Whether v is a YYYY, YYYY-MM or YYYY-MM-DD date the server accepts. */
export function isQueryDate(v) {
  const m = DATE.exec(String(v || ''));
  if (!m) return false;
  const y = Number(m[1]);
  const mo = m[2] ? Number(m[2]) : 1;
  const d = m[3] ? Number(m[3]) : 1;
  const t = new Date(Date.UTC(y, mo - 1, d));
  return t.getUTCFullYear() === y && t.getUTCMonth() === mo - 1 && t.getUTCDate() === d;
}

/** The canonical form of an operator value, for comparing filters. */
function canonical(key, value) {
  const v = String(value).toLowerCase();
  if (key === 'is') return IS_ALIASES[v] || v;
  if (key === 'has') return HAS_VALUES[v] || v;
  if (key === 'mailbox') return v;
  return v;
}

const sameKey = (a, b) => a === b || (a === 'mailbox' && b === 'folder') || (a === 'folder' && b === 'mailbox');

function validOperator(key, value) {
  if (key === 'has') return Object.hasOwn(HAS_VALUES, value.toLowerCase());
  if (key === 'is') return IS_VALUES.includes(canonical('is', value));
  if (key === 'before' || key === 'after') return isQueryDate(value);
  return true;
}

function readQuoted(s, i) {
  let out = '';
  for (i++; i < s.length; i++) {
    const c = s[i];
    if (c === '"') return [out, i + 1];
    if (c === '\\' && (s[i + 1] === '"' || s[i + 1] === '\\')) { out += s[i + 1]; i++; continue; }
    out += c;
  }
  return [out, s.length];
}

/**
 * Splits q into tokens {raw, start, end, neg, key, value, quoted}. key is
 * the lower-case operator name of a well-formed operator token, otherwise
 * null (value is then the term or phrase text).
 */
export function tokenize(q) {
  const s = String(q || '');
  const out = [];
  let i = 0;
  while (i < s.length) {
    while (i < s.length && SPACE.test(s[i])) i++;
    if (i >= s.length) break;
    const begin = i;
    let neg = false;
    if (s[i] === '-' && i + 1 < s.length && !SPACE.test(s[i + 1])) { neg = true; i++; }
    const push = (t) => out.push({ raw: s.slice(begin, i), start: begin, end: i, neg, ...t });
    if (s[i] === '"') {
      const [ph, next] = readQuoted(s, i);
      i = next;
      push({ key: null, value: ph, quoted: true });
      continue;
    }
    const start = i;
    while (i < s.length && !SPACE.test(s[i]) && s[i] !== ':') i++;
    if (s[i] === ':' && KEYS.has(s.slice(start, i).toLowerCase())) {
      const key = s.slice(start, i).toLowerCase();
      i++;
      let value;
      let quoted = false;
      if (s[i] === '"') {
        quoted = true;
        [value, i] = readQuoted(s, i);
      } else {
        const vs = i;
        while (i < s.length && !SPACE.test(s[i])) i++;
        value = s.slice(vs, i);
      }
      value = value.trim();
      if (value && validOperator(key, value)) push({ key, value, quoted });
      else push({ key: null, value: s.slice(start, i), quoted: false, malformed: true });
      continue;
    }
    while (i < s.length && !SPACE.test(s[i])) i++;
    push({ key: null, value: s.slice(start, i), quoted: false });
  }
  return out;
}

/** The operator tokens of q as chips: {index, key, value, neg, raw}. */
export function queryChips(q) {
  return tokenize(q).map((t, index) => ({ ...t, index })).filter((t) => t.key);
}

const NAMES = {
  from: 'From', to: 'To', cc: 'Cc', subject: 'Subject', folder: 'Folder', mailbox: 'Folder',
  label: 'Label', category: 'Category', tag: 'Tag', filename: 'Attachment name',
};

/** What a chip filters by, in words ("From: ann", "Unread", "Before 2024"). */
export function chipLabel(c) {
  if (c.key === 'is' || c.key === 'has') {
    const v = canonical(c.key, c.value);
    if (v === 'attachment') return c.neg ? 'No attachments' : 'Has attachments';
    const word = v[0].toUpperCase() + v.slice(1);
    return c.neg ? `Not ${v}` : word;
  }
  if (c.key === 'before') return `${c.neg ? 'From' : 'Before'} ${c.value}`;
  if (c.key === 'after') return `${c.neg ? 'Before' : 'From'} ${c.value}`;
  const name = NAMES[c.key];
  return c.neg ? `Not ${name.toLowerCase()}: ${c.value}` : `${name}: ${c.value}`;
}

function joinTokens(raws, s) {
  const trailing = /[ \t\n\r\f\v]+$/.exec(s);
  if (!raws.length) return '';
  return raws.join(' ') + (trailing ? trailing[0] : '');
}

/** q without its token number index (other tokens verbatim). */
export function removeToken(q, index) {
  const tokens = tokenize(q);
  if (index < 0 || index >= tokens.length) return String(q || '');
  return joinTokens(tokens.filter((_, i) => i !== index).map((t) => t.raw), String(q || ''));
}

/**
 * An operator value, quoted when it holds spaces or a double quote (or
 * starts with one); inside quotes \ and " are backslash-escaped.
 */
export function formatValue(v) {
  const s = String(v ?? '').replace(/[\t\n\r\f\v]/g, ' ');
  if (s !== '' && !s.includes(' ') && !s.includes('"')) return s;
  return `"${s.replace(/[\\"]/g, '\\$&')}"`;
}

const matches = (t, key, value) => t.key && !t.neg && sameKey(t.key, key) && canonical(t.key, t.value) === canonical(key, value);

/** Whether q filters by the positive operator key:value (case-insensitive). */
export function hasOperator(q, key, value) {
  return tokenize(q).some((t) => matches(t, key, value));
}

/**
 * Removes every positive key:value token from q, or, when there is none,
 * puts one first (so the text being typed stays last).
 */
export function toggleOperator(q, key, value) {
  const s = String(q || '');
  const tokens = tokenize(s);
  if (tokens.some((t) => matches(t, key, value))) {
    return joinTokens(tokens.filter((t) => !matches(t, key, value)).map((t) => t.raw), s);
  }
  return joinTokens([`${key}:${formatValue(value)}`, ...tokens.map((t) => t.raw)], s);
}

const hasTokenChars = (s) => TOKEN_CHAR.test(s);

/** Whether q has positive full-text items (so relevance sorting applies). */
export function hasTextQuery(q) {
  return tokenize(q).some((t) => !t.neg && hasTokenChars(t.value) &&
    (t.key === null || t.key === 'from' || t.key === 'to' || t.key === 'cc' || t.key === 'subject'));
}

/** Words and phrases worth marking in a subject line. */
export function highlightTerms(q) {
  return tokenize(q)
    .filter((t) => !t.neg && hasTokenChars(t.value) && (t.key === null || t.key === 'subject'))
    .map((t) => t.value);
}

// ---- autocomplete ----

/**
 * The token containing the caret, read loosely (an operator being typed
 * has a key even before its value is valid): {start, end, text, neg, key, value}.
 */
export function currentToken(q, caret) {
  const s = String(q || '');
  for (const t of tokenize(s)) {
    if (t.start <= caret && caret <= t.end) {
      const body = t.neg ? t.raw.slice(1) : t.raw;
      const m = /^([a-z]+):(.*)$/is.exec(body);
      const key = m && KEYS.has(m[1].toLowerCase()) ? m[1].toLowerCase() : null;
      const value = key ? m[2].replace(/^"/, '').replace(/"$/, '') : t.value;
      return { start: t.start, end: t.end, text: t.raw, neg: t.neg, key, value };
    }
  }
  return { start: caret, end: caret, text: '', neg: false, key: null, value: '' };
}

function folderPaths(nodes, out = []) {
  for (const n of nodes || []) {
    out.push(n.path);
    folderPaths(n.children, out);
  }
  return out;
}

function valuesFor(key, facets) {
  const names = (list) => (list || []).map((x) => x.name);
  switch (key) {
    case 'is': return IS_VALUES;
    case 'has': return ['attachment'];
    case 'label': return [...names(facets.labels), ...names(facets.categories)];
    case 'category': return names(facets.categories);
    case 'tag': return names(facets.tags);
    case 'folder': case 'mailbox': return folderPaths(facets.folders);
    default: return null; // free-form value
  }
}

const MAX_SUGGESTIONS = 8;

/**
 * Suggestions for token tok: operators while a word is typed, then values
 * from the facets (flags, labels, tags, categories, folders). Each is
 * {insert, label, help}.
 */
export function suggest(tok, facets = {}) {
  const sign = tok.neg ? '-' : '';
  if (!tok.key) {
    const typed = tok.text.replace(/^-/, '').toLowerCase();
    if (!typed || typed.includes(':') || typed.includes('"')) return [];
    return OPERATORS.filter((o) => `${o.key}:`.startsWith(typed) && `${o.key}:` !== typed)
      .map((o) => ({ insert: `${sign}${o.key}:`, label: `${o.key}:`, help: o.help }));
  }
  const values = valuesFor(tok.key, facets);
  if (!values) return [];
  const typed = tok.value.toLowerCase();
  const seen = new Set();
  const pick = (pred) => values.filter((v) => {
    const l = String(v).toLowerCase();
    if (seen.has(l) || l === typed || !pred(l)) return false;
    seen.add(l);
    return true;
  });
  const ordered = [...pick((l) => l.startsWith(typed)), ...pick((l) => l.includes(typed))];
  return ordered.slice(0, MAX_SUGGESTIONS).map((v) => ({
    insert: `${sign}${tok.key}:${formatValue(v)}`, label: String(v), help: '',
  }));
}

/** q with tok replaced by insert; a complete operator gets a trailing space. */
export function applySuggestion(q, tok, insert) {
  const s = String(q || '');
  const rep = insert.endsWith(':') ? insert : `${insert} `;
  const rest = s.slice(tok.end);
  const value = s.slice(0, tok.start) + rep + (rep.endsWith(' ') ? rest.replace(/^[ \t]+/, '') : rest);
  return { value, caret: tok.start + rep.length };
}
