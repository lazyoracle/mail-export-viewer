// Hash routes (deep links), pure. The server has no catch-all route, so
// every client-side location lives in the hash:
//   #/                     the message list
//   #/m/<id>               the list with message <id> open
//   #/stats                statistics
// followed by ?q=&sort=&src=&fid=&folder=&deleted=1 for the list.

export const SORTS = [
  { id: 'newest', label: 'Newest first', api: 'date_desc' },
  { id: 'oldest', label: 'Oldest first', api: 'date_asc' },
  { id: 'sender', label: 'Sender', api: 'from' },
  { id: 'subject', label: 'Subject', api: 'subject' },
  { id: 'relevance', label: 'Best match', api: 'relevance' },
];

const SORT_IDS = new Set(SORTS.map((s) => s.id));

/** The API sort parameter for a UI sort name. */
export function apiSort(id) {
  const s = SORTS.find((x) => x.id === id);
  return s ? s.api : 'date_desc';
}

function posInt(s) {
  if (!/^[0-9]{1,16}$/.test(s || '')) return null;
  const n = Number(s);
  return n > 0 && n <= Number.MAX_SAFE_INTEGER ? n : null;
}

export const defaultRoute = () => ({
  view: 'mail', message: null, q: '', sort: 'newest', source: null, folderId: null, folder: '', deleted: false,
});

export function parseRoute(hash) {
  const r = defaultRoute();
  const s = String(hash || '').replace(/^#/, '');
  const qi = s.indexOf('?');
  const path = (qi < 0 ? s : s.slice(0, qi)).replace(/^\/+|\/+$/g, '');
  const parts = path ? path.split('/') : [];
  if (parts.length === 1 && parts[0] === 'stats') r.view = 'stats';
  else if (parts.length === 2 && parts[0] === 'm') r.message = posInt(parts[1]);
  let p;
  try {
    // URLSearchParams never throws on bad escapes; check them ourselves.
    const query = qi < 0 ? '' : s.slice(qi + 1);
    decodeURIComponent(query.replace(/\+/g, ' '));
    p = new URLSearchParams(query);
  } catch {
    return r;
  }
  r.q = p.get('q') || '';
  if (SORT_IDS.has(p.get('sort'))) r.sort = p.get('sort');
  r.source = posInt(p.get('src'));
  r.folderId = posInt(p.get('fid'));
  r.folder = p.get('folder') || '';
  r.deleted = p.get('deleted') === '1';
  if (r.view === 'stats') return { ...defaultRoute(), view: 'stats' };
  return r;
}

export function formatRoute(route) {
  const r = { ...defaultRoute(), ...route };
  if (r.view === 'stats') return '#/stats';
  const p = new URLSearchParams();
  if (r.q) p.set('q', r.q);
  if (r.sort && r.sort !== 'newest') p.set('sort', r.sort);
  if (r.source) p.set('src', String(r.source));
  if (r.folderId) p.set('fid', String(r.folderId));
  if (r.folder) p.set('folder', r.folder);
  if (r.deleted) p.set('deleted', '1');
  const qs = p.toString();
  return `#/${r.message ? `m/${r.message}` : ''}${qs ? `?${qs}` : ''}`;
}

/** Whether a and b show the same list (the open message aside). */
export function sameList(a, b) {
  return a.view === b.view && (a.q || '') === (b.q || '') && (a.sort || 'newest') === (b.sort || 'newest') &&
    (a.source || null) === (b.source || null) && (a.folderId || null) === (b.folderId || null) &&
    (a.folder || '') === (b.folder || '') && Boolean(a.deleted) === Boolean(b.deleted);
}

export function sameRoute(a, b) {
  return sameList(a, b) && (a.message || null) === (b.message || null);
}
