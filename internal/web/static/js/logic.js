// Pure decisions of the controller (app.js), kept here so they can be
// unit-tested: the deleted mode in effect, error wording, list titles,
// thread sizes, quick-filter state, [ / ] stepping and stored tree state.

import { hasOperator } from './querychips.js';
import { flagQuery } from './flags.js';
import { findNode } from './tree.js';

/** The server's default deleted mode ('' until /api/status is known). */
export function serverDeleted(status) {
  if (!status) return '';
  return status.includeDeleted ? 'include' : 'hide';
}

/** The deleted mode in effect: the route's, else the server default. */
export function deletedMode(route, status) {
  return route.deleted || serverDeleted(status);
}

/**
 * [title, detail] for a message that cannot be opened, by error code;
 * .retry says whether trying again can help.
 */
export function openError(e) {
  let out;
  let retry = true;
  switch (e && e.code) {
    case 'not_found':
      out = ['This message is no longer in the index.', 'The archive was re-indexed since this link was made. Find the message again in the list.'];
      retry = false;
      break;
    case 'stale': out = ['The source file changed after it was indexed.', 'Restart the viewer to index the new version.']; break;
    case 'gone': out = ['The source file is missing or no longer open.', 'Check that the file is still there, then restart the viewer.']; break;
    case 'too_large':
      out = ['This message is too large to open (over 256 MB).', 'Download the original from your mail program instead.'];
      retry = false;
      break;
    default: out = [`This message could not be opened: ${e && e.message}`, ''];
  }
  out.retry = retry;
  return out;
}

/** The list heading for a route. */
export function scopeTitle(route, facets, trees) {
  let name = 'All mail';
  if (route.folderId) {
    const n = findNode(trees || [], (x) => x.folderId === route.folderId);
    name = n ? n.path : 'Folder';
  } else if (route.folder) name = route.folder;
  else if (route.source) {
    const s = facets && facets.sources.find((x) => x.id === route.source);
    name = s ? s.name : 'Source';
  }
  return route.deleted === 'only' ? `${name}: deleted` : name;
}

/** A conversation's full size: threadCount survives the server's cap. */
export function threadSize(thread, item) {
  const fromItems = thread && thread.length ? Math.max(...thread.map((t) => t.threadCount || 0)) : 0;
  return Math.max(fromItems, thread ? thread.length : 0, (item && item.threadCount) || 0);
}

/** Whether a "Show only" flag filter is on for query q and route. */
export function flagPressed(name, q, route) {
  return name === 'deleted' ? route.deleted === 'only' : hasOperator(q, ...flagQuery(name));
}

/** The scope a route lists, as a scopeOrder key; a path maps to its first folder. */
export function scopeKey(route, trees) {
  if (route.folderId) return `folder:${route.folderId}`;
  if (route.folder) {
    const n = findNode(trees || [], (x) => x.folderId && x.path === route.folder);
    if (n) return `folder:${n.folderId}`;
  }
  if (route.source) return `source:${route.source}`;
  return 'all:0';
}

/** The scope delta steps away from the route's in order (clamped), as route fields. */
export function stepScope(order, route, trees, delta) {
  const i = order.findIndex((s) => `${s.kind}:${s.id}` === scopeKey(route, trees));
  const next = order[Math.min(order.length - 1, Math.max(0, (i < 0 ? 0 : i) + delta))];
  if (!next || next.kind === 'all') return {};
  return next.kind === 'folder' ? { folderId: next.id } : { source: next.id };
}

/**
 * The collapsed-node set from a stored value: string keys of an array
 * only, and, once the trees are known, only keys that still exist (ids
 * change when the index is rebuilt).
 */
export function loadCollapsed(stored, trees) {
  const keys = Array.isArray(stored) ? stored.filter((k) => typeof k === 'string') : [];
  if (!trees) return new Set(keys);
  const valid = new Set();
  const walk = (nodes) => { for (const n of nodes) { valid.add(n.key); walk(n.children); } };
  for (const t of trees) { valid.add(`s:${t.source.id}`); walk(t.nodes); }
  return new Set(keys.filter((k) => valid.has(k)));
}
