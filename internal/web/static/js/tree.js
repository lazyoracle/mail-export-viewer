// The folder tree, grouped by source. /api/facets gives one merged tree
// keyed by path, each node listing the sources that have that folder;
// sourceTrees() cuts it into one tree per source with that source's counts.

import { h, icon } from './dom.js';
import { plural, compactCount } from './format.js';

const KIND_LABELS = { mbox: 'mbox', eml: 'EML', olm: 'OLM', pst: 'PST' };

/** The short badge text for a source kind. */
export function kindLabel(kind) {
  return KIND_LABELS[kind] || String(kind || '').toUpperCase();
}

function prune(nodes, sourceId) {
  const out = [];
  for (const n of nodes || []) {
    const ref = (n.sources || []).find((s) => s.sourceId === sourceId);
    const children = prune(n.children, sourceId);
    if (!ref && !children.length) continue;
    out.push({
      name: n.name,
      path: n.path,
      folderId: ref ? ref.folderId : null,
      count: ref ? ref.count : 0,
      unread: ref ? ref.unread : 0,
      key: ref ? `f:${ref.folderId}` : `f:p:${sourceId}:${n.path}`,
      children,
    });
  }
  return out;
}

/**
 * A short sidebar label for a source path: its last two segments, with
 * Thunderbird's ".sbd" directory suffix dropped. The full name is the title.
 */
export function sourceLabel(name) {
  return String(name || '').split('/').slice(-2).map((p, i, a) => (i < a.length - 1 ? p.replace(/\.sbd$/i, '') : p)).join('/');
}

const baseName = (s) => String(s || '').split('/').pop().replace(/\.[^.]*$/, '').toLowerCase();

/**
 * [{source, nodes}] in facets.sources order. The source heading already
 * names the file, so wrappers that repeat it are dropped: directory nodes
 * this source has no folder for, a PST/OLM file's root node named after
 * the file when it holds no mail, and a lone leaf folder named like the
 * file (an mbox file is usually exactly one folder; the source row opens it).
 */
export function sourceTrees(facets) {
  return ((facets && facets.sources) || []).map((source) => {
    let nodes = prune(facets.folders, source.id);
    for (;;) {
      const only = nodes.length === 1 ? nodes[0] : null;
      if (!only || !only.children.length) break;
      const wrapper = only.folderId === null ||
        (!only.count && only.name.toLowerCase() === baseName(source.name));
      if (!wrapper) break;
      nodes = only.children;
    }
    if (nodes.length === 1 && !nodes[0].children.length && nodes[0].name.toLowerCase() === baseName(source.name)) nodes = [];
    return { source, nodes };
  });
}

/** The first node (depth first, in source order) for which pred holds, or null. */
export function findNode(trees, pred) {
  const walk = (nodes) => {
    for (const n of nodes) {
      if (pred(n)) return n;
      const f = walk(n.children);
      if (f) return f;
    }
    return null;
  };
  for (const t of trees) {
    const f = walk(t.nodes);
    if (f) return f;
  }
  return null;
}

/**
 * The scopes [ and ] step through: All mail, then each source followed by
 * its folders that are visible (not inside a collapsed node).
 */
export function scopeOrder(trees, collapsed) {
  const out = [{ kind: 'all', id: 0 }];
  const walk = (nodes) => {
    for (const n of nodes) {
      if (n.folderId) out.push({ kind: 'folder', id: n.folderId });
      if (!collapsed.has(n.key)) walk(n.children);
    }
  };
  for (const t of trees) {
    out.push({ kind: 'source', id: t.source.id });
    if (!collapsed.has(`s:${t.source.id}`)) walk(t.nodes);
  }
  return out;
}

function counts(count, unread) {
  return [
    unread ? h('span', { class: 'side-unread', text: compactCount(unread), attrs: { 'aria-label': `${unread.toLocaleString()} unread` } }) : null,
    h('span', { class: 'side-count', text: compactCount(count), attrs: { 'aria-label': plural(count, 'message') } }),
  ];
}

function twisty(key, open, label) {
  return h('button', {
    class: 'twisty',
    type: 'button',
    dataset: { key },
    attrs: { 'aria-expanded': String(open), 'aria-label': `${open ? 'Collapse' : 'Expand'} ${label}` },
  }, icon('chevron'));
}

function nodeItem(n, opts, depth) {
  const open = !opts.collapsed.has(n.key);
  const current = n.folderId && opts.current.folderId === n.folderId;
  const label = n.folderId
    ? h('a', {
      class: 'node-label',
      href: opts.href({ folderId: n.folderId }),
      title: n.path,
      attrs: { 'aria-current': current ? 'page' : null },
    }, icon('folder'), h('span', { class: 'node-name', text: n.name }), counts(n.count, n.unread))
    : h('span', { class: 'node-label is-virtual', title: n.path }, icon('folder'), h('span', { class: 'node-name', text: n.name }));
  const row = h('div', { class: 'node-row' },
    n.children.length ? twisty(n.key, open, n.name) : h('span', { class: 'twisty-space', attrs: { 'aria-hidden': 'true' } }),
    label);
  row.style.setProperty('--depth', String(depth));
  return h('li', { class: 'node', dataset: { key: n.key } },
    row,
    n.children.length ? h('ul', { class: 'nodes', hidden: !open }, n.children.map((c) => nodeItem(c, opts, depth + 1))) : null);
}

/**
 * Fills container with one collapsible section per source. opts: current
 * ({source, folderId} of the route), collapsed (Set of keys: "s:<id>" for
 * a source, node.key for a folder), href(scope) for links.
 */
export function renderSourceTrees(container, trees, opts) {
  const o = { current: {}, collapsed: new Set(), ...opts };
  container.replaceChildren(...trees.map(({ source, nodes }) => {
    const key = `s:${source.id}`;
    const open = !o.collapsed.has(key);
    const current = o.current.source === source.id && !o.current.folderId;
    return h('section', { class: 'source', dataset: { key } },
      h('div', { class: 'source-row' },
        nodes.length ? twisty(key, open, source.name) : h('span', { class: 'twisty-space', attrs: { 'aria-hidden': 'true' } }),
        h('a', {
          class: 'source-label',
          href: o.href({ source: source.id }),
          title: `${source.name} (${kindLabel(source.kind)})`,
          attrs: { 'aria-current': current ? 'page' : null },
        },
        h('span', { class: `kind-badge kind-${source.kind}`, text: kindLabel(source.kind) }),
        h('span', { class: 'source-name', text: sourceLabel(source.name) }),
        counts(source.count, source.unread))),
      nodes.length ? h('ul', { class: 'nodes', hidden: !open, attrs: { 'aria-label': `Folders in ${source.name}` } },
        nodes.map((n) => nodeItem(n, o, 1))) : null);
  }));
}
