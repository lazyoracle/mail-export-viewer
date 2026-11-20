// Thin client for the viewer's JSON API (same origin only; see docs/api.md).

export class ApiError extends Error {
  constructor(message, status, code = '') {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

/** Parses a fetch Response, turning {"error", "code"} bodies into ApiError. */
export async function readJSON(res) {
  let body = null;
  try {
    body = await res.json();
  } catch (e) {
    // An abort while the body streams in must stay an abort, so a
    // superseded request is dropped instead of reported as an error.
    if (e && e.name === 'AbortError') throw e;
    body = null;
  }
  if (!res.ok) {
    const msg = body && typeof body.error === 'string' && body.error ? body.error : `Request failed (HTTP ${res.status})`;
    const code = body && typeof body.code === 'string' ? body.code : '';
    throw new ApiError(msg, res.status, code);
  }
  if (body === null) throw new ApiError('The server sent an unreadable response', res.status);
  return body;
}

export const PAGE_SIZE = 100;

const DELETED = new Set(['hide', 'include', 'only']);

function withDeleted(path, deleted) {
  return DELETED.has(deleted) ? `${path}?deleted=${deleted}` : path;
}

/**
 * URL of one page of the message list. Scope: source (source id), folderId
 * (one source's folder), folder (a tree path in every source), deleted
 * (hide, include or only messages flagged deleted; '' is the server default).
 */
export function messagesURL({ q, sort, cursor, source, folderId, folder, deleted, limit = PAGE_SIZE } = {}) {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  if (source) p.set('source', String(source));
  if (folderId) p.set('folderId', String(folderId));
  if (folder) p.set('folder', folder);
  if (DELETED.has(deleted)) p.set('deleted', deleted);
  if (q) p.set('q', q);
  if (sort) p.set('sort', sort);
  if (cursor) p.set('cursor', cursor);
  return `/api/messages?${p}`;
}

async function send(url, signal, accept) {
  try {
    return await fetch(url, { signal, headers: { Accept: accept } });
  } catch (e) {
    if (e && e.name === 'AbortError') throw e;
    throw new ApiError('The viewer is not responding. Is mail-export-viewer still running?', 0);
  }
}

async function getJSON(url, signal) {
  return readJSON(await send(url, signal, 'application/json'));
}

/** Display cap for the in-page raw view; the download is always whole. */
export const RAW_DISPLAY_MAX = 2 * 1024 * 1024;

/**
 * Reads at most max bytes of a response body as UTF-8 (lenient) text.
 * Returns {text, truncated}.
 */
export async function readCapped(res, max) {
  if (!res.body) return { text: await res.text(), truncated: false };
  const chunks = [];
  let total = 0;
  let truncated = false;
  const reader = res.body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (total + value.length >= max) {
      chunks.push(value.subarray(0, max - total));
      truncated = total + value.length > max;
      total = max;
      if (!truncated) {
        const next = await reader.read();
        truncated = !next.done;
      }
      if (truncated) await reader.cancel().catch(() => {});
      break;
    }
    chunks.push(value);
    total += value.length;
  }
  const bytes = new Uint8Array(total);
  let off = 0;
  for (const c of chunks) { bytes.set(c, off); off += c.length; }
  return { text: new TextDecoder('utf-8').decode(bytes), truncated };
}

const id = (n) => encodeURIComponent(n);
const rawURL = (n) => `/api/messages/${id(n)}/raw`;

export const api = {
  status: (signal) => getJSON('/api/status', signal),
  /** deleted: as for messages; facets know no 'only', so it counts as include. */
  facets: ({ deleted } = {}, signal) => getJSON(withDeleted('/api/facets', deleted === 'only' ? 'include' : deleted), signal),
  stats: (signal) => getJSON('/api/stats', signal),
  messages: (params, signal) => getJSON(messagesURL(params), signal),
  message: (n, signal) => getJSON(`/api/messages/${id(n)}`, signal),
  thread: (n, { deleted } = {}, signal) => getJSON(withDeleted(`/api/threads/${id(n)}`, deleted), signal),
  rawURL,
  /** The original message as text, capped at max bytes for display. */
  raw: async (n, signal, max = RAW_DISPLAY_MAX) => {
    const res = await send(rawURL(n), signal, 'message/rfc822, text/plain');
    if (!res.ok) await readJSON(res); // throws ApiError with the server's message
    return readCapped(res, max);
  },
};
