// Pure UI state helpers: list paging, counts, conversation collapsing,
// indexing progress and warnings. (Hash routes live in router.js.)

/** Message list state. total is the first page's count (later pages say -1;
 * a first page may too, meaning "not counted"). */
export function emptyList() {
  return { items: [], ids: new Set(), total: null, cursor: '', done: false, loading: false, error: null };
}

export function startLoading(list) {
  return { ...list, loading: true, error: null };
}

export function applyPage(list, page) {
  const first = list.items.length === 0 && !list.cursor;
  // The item array and id set grow in place (a list state is replaced, never
  // reused), so loading page after page stays linear.
  const ids = list.ids || new Set(list.items.map((i) => i.id));
  const items = list.items;
  for (const it of page.items || []) {
    if (ids.has(it.id)) continue;
    ids.add(it.id);
    items.push(it);
  }
  let total = list.total;
  const cursor = page.nextCursor || '';
  // -1: the server did not count; exact only if this is the last page.
  if (first) total = page.total >= 0 ? page.total : (cursor ? -1 : items.length);
  return { items, ids, total, cursor, done: !cursor, loading: false, error: null };
}

export function failPage(list, err) {
  return { ...list, loading: false, error: (err && err.message) || String(err) };
}

/** Moves index i by delta within [0, length); -1 when empty. */
export function clampIndex(i, delta, length) {
  if (length <= 0) return -1;
  return Math.min(length - 1, Math.max(0, i + delta));
}

/**
 * One row per conversation: the first item (in list order) of each
 * threadId. threadId 0 (threading has not run yet) keeps every item.
 */
export function collapseThreads(items) {
  const seen = new Set();
  return items.filter((it) => {
    if (!it.threadId) return true;
    if (seen.has(it.threadId)) return false;
    seen.add(it.threadId);
    return true;
  });
}

const RUN_STATES = new Set(['queued', 'indexing', 'done', 'cached', 'failed']);
const FINISHED_STATES = new Set(['done', 'cached', 'failed']);

function sourceFraction(s) {
  if (FINISHED_STATES.has(s.state)) return 1;
  if (s.state !== 'indexing') return 0;
  return s.total > 0 ? Math.min(1, Math.max(0, s.done / s.total)) : null;
}

/**
 * Normalises an /api/status response for the progress banner: fraction is
 * the share of this run's sources read (the one being read counts by its
 * done/total), or null when the current source has no known total.
 */
export function progressInfo(s) {
  const ix = (s && s.indexing) || {};
  const sources = ((s && s.sources) || []).filter((x) => RUN_STATES.has(x.state));
  const active = Boolean(ix.running);
  const reading = sources.find((x) => x.state === 'indexing');
  let fraction = 1;
  if (active) {
    const parts = sources.map(sourceFraction);
    fraction = parts.includes(null) || !parts.length ? null : Math.min(1, parts.reduce((a, b) => a + b, 0) / parts.length);
  }
  return {
    active,
    fraction,
    // The run total can lag behind the per-source counts while a source is read.
    messages: Math.max(ix.messages || 0, sources.reduce((n, x) => n + (x.messages || 0), 0)),
    warnings: ix.warnings || 0,
    current: active && reading ? reading.name : '',
    error: ix.error || '',
    sources: sources.length,
    finished: sources.filter((x) => FINISHED_STATES.has(x.state)).length,
  };
}

/** Whether new index data warrants reloading the sidebar and the list. */
export function shouldRefresh(prev, next) {
  if (!prev) return true;
  const a = prev.indexing || {};
  const b = next.indexing || {};
  return a.messages !== b.messages || Boolean(a.running) !== Boolean(b.running);
}

/**
 * Sources with an error or warnings, for the warnings viewer: failed ones
 * first, then by warning count. more: warnings not included in the list.
 */
export function sourceWarnings(s) {
  return ((s && s.sources) || [])
    .filter((x) => x.error || x.warningCount > 0)
    .map((x) => ({
      name: x.name, kind: x.kind, state: x.state, error: x.error || '',
      count: x.warningCount || 0, warnings: x.warnings || [],
      more: Math.max(0, (x.warningCount || 0) - (x.warnings || []).length),
    }))
    .sort((a, b) => (Boolean(b.error) - Boolean(a.error)) || (b.count - a.count));
}

/** The list header count: "3 messages", "12 results", "Many results", "Searching…". */
export function countText(list, { query = '', searching = false } = {}) {
  if (query && (searching || (list.loading && list.total === null))) return 'Searching…';
  const t = list.total;
  if (t == null) return '';
  if (t < 0) return query ? 'Many results' : 'Many messages';
  const word = query ? 'result' : 'message';
  return `${t.toLocaleString('en-US')} ${word}${t === 1 ? '' : 's'}`;
}
