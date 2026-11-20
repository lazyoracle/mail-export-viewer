// Controller: wires the API, hash routing, the sidebar (sources, folders,
// flags, labels, categories, tags), the message list (search, chips,
// autocomplete, sort, deleted mode, conversations), the reader, the
// statistics view, keyboard shortcuts and the indexing banner.

import { api, PAGE_SIZE } from './api.js';
import { h, icon, clear, append, $ } from './dom.js';
import { plural, compactCount } from './format.js';
import {
  emptyList, startLoading, applyPage, failPage, clampIndex, progressInfo, shouldRefresh, countText, sourceWarnings, inputWarnings, visibleRows,
} from './state.js';
import { parseRoute, formatRoute, sameRoute, sameList, SORTS, apiSort } from './router.js';
import {
  OPERATORS, queryChips, chipLabel, removeToken, toggleOperator, hasOperator, hasTextQuery, highlightTerms,
  currentToken, suggest, applySuggestion,
} from './querychips.js';
import { debounce, latest } from './debounce.js';
import { createDispatcher, SHORTCUTS } from './shortcuts.js';
import { copyText } from './clipboard.js';
import { renderRow, rowId } from './list.js';
import { renderTermList, markActiveTerms, FILTER_FROM } from './labels.js';
import { FLAGS, flagQuery, markFlags } from './flags.js';
import { sourceTrees, scopeOrder, renderSourceTrees, kindLabel } from './tree.js';
import { renderMessage, renderConversation, renderReaderMessage } from './reader.js';
import { renderStats } from './stats.js';
import {
  deletedMode as modeFor, serverDeleted, openError, scopeTitle, threadSize, flagPressed, stepScope, loadCollapsed,
} from './logic.js';
import { loadTheme, saveTheme, nextTheme, themeLabel } from './theme.js';

const SEARCH_DELAY = 250;
const APP_NAME = 'Mail archive';
const KEY_PREFIX = 'mail-export-viewer-';

const storage = (() => { try { return window.localStorage; } catch { return null; } })();

function loadPref(name, fallback) {
  try {
    const v = storage && storage.getItem(KEY_PREFIX + name);
    return v === null || v === undefined ? fallback : JSON.parse(v);
  } catch {
    return fallback;
  }
}

function savePref(name, value) {
  try { if (storage) storage.setItem(KEY_PREFIX + name, JSON.stringify(value)); } catch { /* this page only */ }
}

const TERM_KINDS = ['label', 'category', 'tag'];
const TERM_LISTS = { label: 'labels', category: 'categories', tag: 'tags' };

const state = {
  route: null,
  status: null,
  statusFailed: false,
  facets: null,
  facetsError: null,
  trees: [],
  collapsed: loadCollapsed(loadPref('collapsed', []), null),
  termOpen: loadPref('terms-open', { label: true, category: true, tag: true }),
  termFilter: { label: '', category: '', tag: '' },
  list: emptyList(),
  rows: [], // the rows shown (conversations collapsed when threaded)
  seenThreads: new Set(),
  terms: [],
  active: -1,
  searching: false,
  view: 'html',
  message: null,
  thread: null,
  threadTruncated: false,
  threadCount: 0,
  threaded: loadPref('threaded', false) === true,
  convoExpanded: new Map(),
  convoLoaded: new Map(),
};

const el = {
  app: $('app'),
  banner: $('index-banner'),
  bannerText: $('index-text'),
  bannerBar: $('index-bar'),
  bannerDetails: $('index-details'),
  sidebar: $('sidebar'),
  sidebarOpen: $('sidebar-open'),
  sidebarClose: $('sidebar-close'),
  statsMenu: $('stats-menu'),
  scrim: $('scrim'),
  navAll: $('nav-all'),
  navAllCount: $('nav-all-count'),
  navStats: $('nav-stats'),
  flagList: $('flag-list'),
  sourceTree: $('source-tree'),
  facetsStatus: $('facets-status'),
  statusOpen: $('status-open'),
  statusDialog: $('status-dialog'),
  statusBody: $('status-body'),
  statusClose: $('status-close'),
  theme: $('theme-toggle'),
  themeIcon: $('theme-icon'),
  listTitle: $('list-title'),
  listCount: $('list-count'),
  listRefresh: $('list-refresh'),
  search: $('search'),
  suggestBox: $('search-suggest'),
  helpToggle: $('search-help-toggle'),
  help: $('search-help'),
  helpOps: $('search-help-ops'),
  chips: $('query-chips'),
  sort: $('sort'),
  deletedMode: $('deleted-mode'),
  threadToggle: $('thread-toggle'),
  listScroll: $('list-scroll'),
  list: $('message-list'),
  listStatus: $('list-status'),
  sentinel: $('list-sentinel'),
  reader: $('reader'),
  readerEmpty: $('reader-empty'),
  article: $('message'),
  stats: $('stats'),
  statsContent: $('stats-content'),
  shortcuts: $('shortcuts'),
  shortcutsList: $('shortcuts-list'),
  shortcutsOpen: $('shortcuts-open'),
  shortcutsClose: $('shortcuts-close'),
  toast: $('toast'),
};

const termSections = Object.fromEntries(TERM_KINDS.map((k) => {
  const section = $(`terms-${k}`);
  return [k, {
    section,
    toggle: $(`terms-${k}-head`),
    panel: $(`terms-${k}-panel`),
    total: section.querySelector('.side-total'),
    filterWrap: section.querySelector('.term-filter'),
    filter: section.querySelector('.term-filter').querySelector('input'),
    list: section.querySelector('.term-list'),
  }];
}));

const route = () => state.route || parseRoute('');
const narrow = () => window.matchMedia('(max-width: 759px)').matches;

/** The deleted mode in effect: the route's, else the server default. */
function deletedMode(r = route()) {
  return modeFor(r, state.status);
}

// ---- theme ----

let theme = loadTheme(storage);

function applyTheme() {
  if (theme === 'system') delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
  el.themeIcon.setAttribute('href', { system: '#i-auto', light: '#i-sun', dark: '#i-moon' }[theme]);
  el.theme.setAttribute('aria-label', themeLabel(theme));
  el.theme.title = `${themeLabel(theme)} (d)`;
}

function cycleTheme() {
  theme = nextTheme(theme);
  saveTheme(storage, theme);
  applyTheme();
  toast(`Theme: ${theme}`);
}

el.theme.addEventListener('click', cycleTheme);

// ---- toast ----

let toastTimer = 0;

function toast(text) {
  el.toast.textContent = text;
  el.toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.toast.hidden = true; }, 2400);
}

// ---- panes and the sidebar drawer ----

function showPane(pane) {
  el.app.dataset.pane = pane;
}

let drawerReturn = null;

function setDrawer(open) {
  const was = el.app.classList.contains('drawer-open');
  if (was === open) return;
  el.app.classList.toggle('drawer-open', open);
  el.scrim.hidden = !open;
  for (const b of [el.sidebarOpen, el.statsMenu]) b.setAttribute('aria-expanded', String(open));
  if (open) {
    drawerReturn = document.activeElement;
    const target = el.sidebar.querySelector('[aria-current="page"]') || el.navAll;
    target.focus();
  } else if (drawerReturn && drawerReturn.focus && el.sidebar.contains(document.activeElement)) {
    drawerReturn.focus();
  }
}

el.sidebarOpen.addEventListener('click', () => setDrawer(true));
el.statsMenu.addEventListener('click', () => setDrawer(true));
el.sidebarClose.addEventListener('click', () => setDrawer(false));
el.scrim.addEventListener('click', () => setDrawer(false));

// ---- navigation ----

// The most recent history entry this app pushed (from -> to hashes), so
// "back to the list" can step back instead of stacking another entry.
let lastPush = null;

/**
 * Moves to route r. replace: rewrite the current history entry (used while
 * refining a search, so Back does not step through every keystroke).
 */
function navigate(r, { replace = false } = {}) {
  const target = formatRoute(r);
  setDrawer(false);
  if (location.hash === target) { applyRoute(parseRoute(target)); return; }
  if (replace && window.history && window.history.replaceState) {
    window.history.replaceState(null, '', target);
    applyRoute(parseRoute(target));
    return;
  }
  lastPush = { from: location.hash, to: target };
  applyRoute(parseRoute(target));
  location.hash = target;
}

/** A list route for a scope, keeping the sort and deleted mode. */
function scopeRoute(scope) {
  const r = route();
  return { view: 'mail', q: '', sort: r.sort, deleted: r.deleted, source: null, folderId: null, folder: '', ...scope };
}

const scopeHref = (scope) => formatRoute(scopeRoute(scope));

// Links in the sidebar and statistics: plain hash links, so they work with
// middle-click and copy; a click also closes the drawer.
el.sidebar.addEventListener('click', (e) => {
  const a = e.target.closest('a[href^="#"]');
  if (a) { setDrawer(false); if (a.getAttribute('href') === location.hash) applyRoute(parseRoute(location.hash)); }
});

// ---- sidebar ----

function renderSidebar() {
  const r = route();
  const f = state.facets;
  const allCurrent = r.view === 'mail' && !r.source && !r.folderId && !r.folder;
  el.navAll.setAttribute('href', scopeHref({}));
  if (allCurrent) el.navAll.setAttribute('aria-current', 'page'); else el.navAll.removeAttribute('aria-current');
  if (r.view === 'stats') el.navStats.setAttribute('aria-current', 'page'); else el.navStats.removeAttribute('aria-current');
  el.navAllCount.textContent = f ? f.sources.reduce((n, s) => n + s.count, 0).toLocaleString() : '';

  // Flags: quick filters with counts.
  const q = el.search.value;
  const flags = f ? f.flags : [];
  clear(el.flagList).append(...FLAGS.map((info) => {
    const fc = flags.find((x) => x.name === info.name);
    const n = fc ? fc.count : 0;
    const pressed = flagPressed(info.name, q, r);
    if (!n && !pressed) return null;
    return h('li', {}, h('button', {
      class: 'flag-btn', type: 'button', dataset: { flag: info.name },
      title: info.name === 'deleted' ? 'Show only messages flagged deleted' : `Add ${flagQuery(info.name).join(':')} to the search`,
      attrs: { 'aria-pressed': String(pressed) },
    }, icon(info.icon), h('span', { class: 'side-name', text: info.label }),
    h('span', { class: 'side-count', text: compactCount(n), attrs: { 'aria-label': plural(n, 'message') } })));
  }).filter(Boolean));
  el.flagList.closest('section').hidden = !el.flagList.children.length;

  renderSourceTrees(el.sourceTree, state.trees, {
    current: r.view === 'mail' ? { source: r.source, folderId: r.folderId } : {},
    collapsed: state.collapsed,
    href: scopeHref,
  });
  el.facetsStatus.className = state.facetsError ? 'side-status is-error' : 'side-status';
  el.facetsStatus.textContent = state.facetsError ? `Folders could not be loaded: ${state.facetsError}`
    : (f && !f.sources.length ? 'No mail has been indexed yet.' : '');

  for (const kind of TERM_KINDS) renderTerms(kind);
}

function renderTerms(kind) {
  const s = termSections[kind];
  const terms = (state.facets && state.facets[TERM_LISTS[kind]]) || [];
  s.section.hidden = !terms.length;
  if (!terms.length) return;
  const open = state.termOpen[kind] !== false;
  s.toggle.setAttribute('aria-expanded', String(open));
  s.panel.hidden = !open;
  s.total.textContent = terms.length.toLocaleString();
  s.filterWrap.hidden = terms.length < FILTER_FROM;
  const q = el.search.value;
  renderTermList(s.list, kind, terms, { isActive: (name) => hasOperator(q, kind, name), filter: state.termFilter[kind] });
}

for (const kind of TERM_KINDS) {
  const s = termSections[kind];
  s.toggle.addEventListener('click', () => {
    state.termOpen = { ...state.termOpen, [kind]: !(state.termOpen[kind] !== false) };
    savePref('terms-open', state.termOpen);
    renderTerms(kind);
  });
  const filterSoon = debounce((v) => { state.termFilter[kind] = v; renderTerms(kind); }, 100);
  s.filter.addEventListener('input', () => filterSoon(s.filter.value));
  s.filter.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && s.filter.value) {
      e.preventDefault();
      e.stopPropagation();
      filterSoon.cancel();
      s.filter.value = '';
      state.termFilter[kind] = '';
      renderTerms(kind);
    } else if (e.key === 'ArrowDown') {
      filterSoon.flush();
      const first = s.list.querySelector('.side-term');
      if (first) { e.preventDefault(); first.focus(); }
    }
  });
  s.list.addEventListener('click', (e) => {
    const b = e.target.closest('.side-term');
    if (!b) return;
    editQuery(toggleOperator(el.search.value, kind, b.dataset.name));
    setDrawer(false);
    if (narrow()) showPane('list');
  });
}

el.flagList.addEventListener('click', (e) => {
  const b = e.target.closest('.flag-btn');
  if (!b) return;
  const name = b.dataset.flag;
  setDrawer(false);
  if (name === 'deleted') {
    const r = route();
    navigate({ ...r, message: null, deleted: r.deleted === 'only' ? '' : 'only' });
    return;
  }
  editQuery(toggleOperator(el.search.value, ...flagQuery(name)));
});

el.sourceTree.addEventListener('click', (e) => {
  const t = e.target.closest('.twisty');
  if (!t) return;
  const key = t.dataset.key;
  if (state.collapsed.has(key)) state.collapsed.delete(key); else state.collapsed.add(key);
  savePref('collapsed', [...state.collapsed]);
  renderSidebar();
  const again = el.sourceTree.querySelector(`.twisty[data-key="${CSS.escape(key)}"]`);
  if (again) again.focus();
});

const facetsReq = latest();

async function loadFacets() {
  try {
    const res = await facetsReq.run((signal) => api.facets({ deleted: deletedMode() }, signal));
    if (res.stale) return;
    state.facets = res.value;
    state.facetsError = null;
    state.trees = sourceTrees(state.facets);
    state.collapsed = loadCollapsed([...state.collapsed], state.trees);
  } catch (e) {
    if (e && e.name === 'AbortError') return;
    state.facetsError = e.message;
  }
  renderSidebar();
  renderListHead();
}

// ---- search box, chips, suggestions, sort ----

const effectiveSort = (r) => (r.sort === 'relevance' && !hasTextQuery(r.q) ? 'newest' : r.sort);

/** Sends q (the search box text) as the current search. */
function commitSearch(q) {
  state.searching = false;
  const r = route();
  if (q === r.q && r.view === 'mail') { renderListHead(); return; }
  navigate({ ...r, view: 'mail', q }, { replace: Boolean(r.q) && Boolean(q) && r.view === 'mail' });
}

const searchSoon = debounce(commitSearch, SEARCH_DELAY);

/** Replaces the query (from a chip, flag or term) and searches right away. */
function editQuery(q) {
  searchSoon.cancel();
  el.search.value = q;
  hideSuggest();
  renderFilters();
  commitSearch(q);
}

/** Chips, sort, deleted mode and the conversation toggle; full: the sidebar too. */
function renderFilters({ full = true } = {}) {
  const q = el.search.value;
  const chips = queryChips(q);
  el.chips.hidden = !chips.length;
  clear(el.chips).append(...chips.map((c) => {
    const label = chipLabel(c);
    return h('li', { class: c.neg ? 'query-chip is-neg' : 'query-chip', title: c.raw },
      h('span', { class: 'query-chip-text', text: label }),
      h('button', { type: 'button', dataset: { index: String(c.index) }, attrs: { 'aria-label': `Remove filter: ${label}` } }, icon('close')));
  }));

  const textual = hasTextQuery(q);
  const r = route();
  clear(el.sort).append(...SORTS.map((s) => h('option', {
    value: s.id,
    text: s.label,
    disabled: s.id === 'relevance' && !textual,
  })));
  el.sort.value = effectiveSort({ q: r.q, sort: r.sort });
  el.deletedMode.value = deletedMode(r) || 'hide';
  el.threadToggle.setAttribute('aria-pressed', String(state.threaded));
  if (full) renderSidebar();
  else markSidebar();
}

/** While typing: only which flags and terms are pressed (no rebuild). */
function markSidebar() {
  const q = el.search.value;
  const r = route();
  markFlags(el.flagList, (name) => flagPressed(name, q, r));
  for (const kind of TERM_KINDS) markActiveTerms(termSections[kind].list, (name) => hasOperator(q, kind, name));
}

el.chips.addEventListener('click', (e) => {
  const b = e.target.closest('button[data-index]');
  if (!b) return;
  editQuery(removeToken(el.search.value, Number(b.dataset.index)));
  const next = el.chips.querySelector('button') || el.search;
  next.focus();
});

/** Brings the search box and filters in line with the route. */
function syncSearchUI() {
  const q = route().q;
  // Never overwrite what the user is still typing.
  if (!searchSoon.pending() && el.search.value !== q) el.search.value = q;
  renderFilters();
}

// Autocomplete: operators while typing a word, then values from the facets.
let suggestions = [];
let suggestActive = -1;
let suggestToken = null;

function hideSuggest() {
  suggestions = [];
  suggestActive = -1;
  el.suggestBox.hidden = true;
  el.search.setAttribute('aria-expanded', 'false');
  el.search.removeAttribute('aria-activedescendant');
}

function updateSuggest() {
  const v = el.search.value;
  const caret = el.search.selectionStart ?? v.length;
  suggestToken = currentToken(v, caret);
  suggestions = suggest(suggestToken, state.facets || {});
  suggestActive = -1;
  if (!suggestions.length || document.activeElement !== el.search) { hideSuggest(); return; }
  clear(el.suggestBox).append(...suggestions.map((s, i) => h('li', {
    id: `suggest-${i}`, class: 'suggest-item', attrs: { role: 'option', 'aria-selected': 'false' }, dataset: { index: String(i) },
  }, h('span', { class: 'suggest-insert', text: s.insert }), s.help ? h('span', { class: 'suggest-help', text: s.help }) : null)));
  el.suggestBox.hidden = false;
  el.help.hidden = true;
  el.helpToggle.setAttribute('aria-expanded', 'false');
  el.search.setAttribute('aria-expanded', 'true');
}

function markSuggest(i) {
  suggestActive = i;
  for (const li of el.suggestBox.querySelectorAll('.suggest-item')) {
    li.setAttribute('aria-selected', String(Number(li.dataset.index) === i));
  }
  if (i >= 0) {
    el.search.setAttribute('aria-activedescendant', `suggest-${i}`);
    const li = $(`suggest-${i}`);
    if (li) li.scrollIntoView({ block: 'nearest' });
  } else el.search.removeAttribute('aria-activedescendant');
}

function pickSuggest(i) {
  const s = suggestions[i];
  if (!s || !suggestToken) return;
  const { value, caret } = applySuggestion(el.search.value, suggestToken, s.insert);
  el.search.value = value;
  el.search.setSelectionRange(caret, caret);
  el.search.focus();
  state.searching = true;
  renderFilters({ full: false });
  searchSoon(value);
  updateSuggest();
}

el.suggestBox.addEventListener('mousedown', (e) => {
  const li = e.target.closest('.suggest-item');
  if (!li) return;
  e.preventDefault(); // keep focus in the search box
  pickSuggest(Number(li.dataset.index));
});

el.search.addEventListener('input', () => {
  state.searching = true;
  renderFilters({ full: false });
  renderListHead();
  searchSoon(el.search.value);
  updateSuggest();
});
el.search.addEventListener('click', updateSuggest);
el.search.addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== el.search) hideSuggest(); }, 0));

el.search.addEventListener('keydown', (e) => {
  const open = !el.suggestBox.hidden && suggestions.length;
  if (open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
    e.preventDefault();
    const n = suggestions.length;
    markSuggest(e.key === 'ArrowDown' ? (suggestActive + 1) % n : (suggestActive <= 0 ? n - 1 : suggestActive - 1));
    return;
  }
  if (open && (e.key === 'Enter' || e.key === 'Tab') && suggestActive >= 0) {
    e.preventDefault();
    pickSuggest(suggestActive);
    return;
  }
  if (e.key === 'Escape') {
    e.preventDefault();
    if (open) { hideSuggest(); return; }
    if (!el.help.hidden) { closeHelp(); return; }
    if (el.search.value) {
      el.search.value = '';
      searchSoon.cancel();
      commitSearch('');
      renderFilters();
    } else {
      searchSoon.flush();
      el.search.blur();
      el.list.focus({ preventScroll: true });
    }
  } else if (e.key === 'Enter') {
    e.preventDefault();
    hideSuggest();
    searchSoon.flush();
  } else if (e.key === 'ArrowDown') {
    e.preventDefault();
    searchSoon.flush();
    el.list.focus({ preventScroll: true });
  } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') {
    setTimeout(updateSuggest, 0);
  }
});

function buildHelp() {
  clear(el.helpOps).append(...OPERATORS.flatMap((o) => [
    h('dt', {}, h('button', { class: 'op-example', type: 'button', text: o.example, dataset: { insert: o.example } })),
    h('dd', { text: o.help }),
  ]), h('dt', {}, h('button', { class: 'op-example', type: 'button', text: '-from:noreply', dataset: { insert: '-from:noreply' } })),
  h('dd', { text: 'A - before any word or operator leaves those messages out' }));
}

function openHelp() {
  if (!el.helpOps.children.length) buildHelp();
  hideSuggest();
  el.help.hidden = false;
  el.helpToggle.setAttribute('aria-expanded', 'true');
}

function closeHelp() {
  el.help.hidden = true;
  el.helpToggle.setAttribute('aria-expanded', 'false');
}

el.helpToggle.addEventListener('click', () => (el.help.hidden ? openHelp() : closeHelp()));

el.help.addEventListener('click', (e) => {
  const b = e.target.closest('.op-example');
  if (!b) return;
  const v = el.search.value.trim();
  editQuery(v ? `${v} ${b.dataset.insert}` : b.dataset.insert);
  closeHelp();
  el.search.focus();
});

el.sort.addEventListener('change', () => navigate({ ...route(), sort: el.sort.value }));

el.deletedMode.addEventListener('change', () => {
  const v = el.deletedMode.value;
  const def = serverDeleted(state.status);
  navigate({ ...route(), message: null, deleted: v === def ? '' : v });
});

el.threadToggle.addEventListener('click', () => toggleThread());

// Clicks outside close the operator help.
window.addEventListener('click', (e) => {
  const t = e.target;
  if (!t || !t.closest) return;
  if (!el.help.hidden && !t.closest('.search-slot')) closeHelp();
});

// ---- message list ----

function renderListHead() {
  const r = route();
  const name = scopeTitle(r, state.facets, state.trees);
  el.listTitle.textContent = name;
  document.title = r.view === 'stats' ? `Statistics - ${APP_NAME}` : (r.q ? `${r.q} in ${name} - ${APP_NAME}` : `${name} - ${APP_NAME}`);
  const typed = state.searching ? el.search.value : r.q;
  let text = countText(state.list, { query: typed, searching: state.searching && Boolean(typed) });
  if (state.threaded && text && state.rows.length && state.rows.length < state.list.items.length) {
    text += `, ${plural(state.rows.length, 'conversation')} loaded`;
  }
  el.listCount.textContent = text;
}

function suggestion(text, onClick) {
  return h('li', {}, h('button', { class: 'link-btn', type: 'button', text, on: { click: onClick } }));
}

function renderListStatus() {
  const l = state.list;
  const r = route();
  const s = el.listStatus;
  clear(s);
  s.className = 'list-status';
  if (l.error) {
    s.classList.add('is-error');
    s.append(h('p', { text: `Messages could not be loaded: ${l.error}` }),
      h('button', { class: 'btn', type: 'button', text: 'Try again', on: { click: loadMore } }));
  } else if (l.loading) {
    const what = r.q ? 'Searching' : 'Loading messages';
    s.append(h('p', { class: 'loading', text: l.items.length ? 'Loading more messages' : what }));
  } else if (!l.items.length && (r.q || r.deleted === 'only')) {
    s.classList.add('is-empty');
    const tips = [];
    const scoped = r.source || r.folderId || r.folder;
    if (scoped) tips.push(suggestion('Search all mail', () => navigate({ ...r, source: null, folderId: null, folder: '', message: null })));
    if (queryChips(r.q).length) tips.push(suggestion('Remove the filters', () => editQuery(queryChips(r.q).reduceRight((q, c) => removeToken(q, c.index), r.q))));
    if (r.q) tips.push(suggestion('Clear the search', () => editQuery('')));
    if (r.deleted === 'only') tips.push(suggestion('Show messages that are not deleted', () => navigate({ ...r, deleted: '', message: null })));
    append(s, [icon('search', 'icon empty-mark'),
      h('p', { class: 'empty-title', text: r.q ? 'No messages match this search.' : 'No deleted messages here.' }),
      r.q ? h('p', { class: 'empty-tip', text: 'Check the spelling or use fewer words. A word shorter than 3 letters only matches whole words. after: includes its date; before: does not.' }) : null,
      h('ul', { class: 'suggestions' }, tips)]);
  } else if (!l.items.length) {
    const indexing = state.status && state.status.indexing && state.status.indexing.running;
    s.classList.add('is-empty');
    s.append(icon('folder', 'icon empty-mark'),
      h('p', { text: indexing ? 'No messages yet. They appear here as the archive is indexed.' : 'This folder has no messages.' }));
  }
}

function listParams(cursor) {
  const r = route();
  return {
    q: r.q, sort: apiSort(effectiveSort(r)), cursor,
    source: r.source, folderId: r.folderId, folder: r.folder, deleted: deletedMode(r),
  };
}

// One list request at a time; a newer list (new search, scope or sort)
// aborts the old request and its late result is dropped.
const listReq = latest();

async function loadMore() {
  const l = state.list;
  if (l.loading || l.done) return;
  state.list = startLoading(l);
  renderListStatus();
  renderListHead();
  const params = listParams(l.cursor);
  let res;
  try {
    res = await listReq.run((signal) => api.messages(params, signal));
  } catch (e) {
    if (e && e.name === 'AbortError') return;
    state.list = failPage(state.list, e);
    renderListHead();
    renderListStatus();
    return;
  }
  if (res.stale) return;
  const before = state.list.items.length;
  state.list = applyPage(state.list, res.value);
  appendRows(state.list.items.slice(before));
  renderListHead();
  renderListStatus();
  // Keep filling while the sentinel is still visible (tall screens, or
  // pages that collapse into few conversations).
  requestAnimationFrame(checkSentinel);
}

function appendRows(items) {
  const now = new Date();
  const r = route();
  const showFolder = !r.folderId && !r.folder;
  const frag = document.createDocumentFragment();
  for (const it of visibleRows(items, state.seenThreads, state.threaded)) {
    state.rows.push(it);
    frag.append(renderRow(it, now, state.terms, { showFolder }));
  }
  el.list.append(frag);
  markSelected();
}

function clearRows() {
  state.rows = [];
  state.seenThreads = new Set();
  state.active = -1;
  clear(el.list);
  el.list.removeAttribute('aria-activedescendant');
}

/** Reloads the list from the first page. */
function resetList() {
  listReq.cancel();
  redrawGen++;
  state.list = emptyList();
  state.terms = highlightTerms(route().q);
  clearRows();
  el.listRefresh.hidden = true;
  el.listScroll.scrollTop = 0;
  renderListHead();
  loadMore();
}

const REDRAW_CHUNK = 300;
let redrawGen = 0;

/**
 * Redraws the loaded items (after the conversation toggle) a chunk per
 * frame, so thousands of loaded rows do not block the page; holds off
 * paging until done.
 */
function redrawRows() {
  const activeId = state.rows[state.active]?.id;
  const items = state.list.items;
  const gen = ++redrawGen;
  const wasLoading = state.list.loading;
  clearRows();
  state.list = { ...state.list, loading: true };
  let next = 0;
  const step = () => {
    if (gen !== redrawGen) return;
    appendRows(items.slice(next, next + REDRAW_CHUNK));
    next += REDRAW_CHUNK;
    const i = state.rows.findIndex((it) => it.id === activeId);
    if (i >= 0 && state.active !== i) setActive(i, { scroll: false });
    if (next < items.length) { requestAnimationFrame(step); return; }
    state.list = { ...state.list, loading: wasLoading };
    renderListHead();
    requestAnimationFrame(checkSentinel);
  };
  step();
}

/**
 * Fetches the first page again and swaps the rows in one step (no flash
 * of an empty list), keeping the keyboard-focused message if still there.
 */
async function reloadInPlace() {
  if (state.list.loading) return;
  const activeId = state.rows[state.active]?.id;
  state.list = { ...state.list, loading: true }; // holds off loadMore
  const params = listParams('');
  let res;
  try {
    res = await listReq.run((signal) => api.messages(params, signal));
  } catch {
    state.list = { ...state.list, loading: false };
    return; // keep what is shown; the next status change retries
  }
  if (res.stale) return;
  state.list = applyPage(emptyList(), res.value);
  clearRows();
  appendRows(state.list.items);
  const i = state.rows.findIndex((it) => it.id === activeId);
  if (i >= 0) setActive(i, { scroll: false });
  renderListHead();
  renderListStatus();
}

// The row marked aria-selected (the open message), tracked so selection
// changes touch two rows rather than the whole list.
let selectedRow = null;

function markSelected() {
  const id = route().message;
  const row = id ? document.getElementById(rowId(id)) : null;
  if (selectedRow && selectedRow !== row) selectedRow.setAttribute('aria-selected', 'false');
  if (row) row.setAttribute('aria-selected', 'true');
  selectedRow = row;
}

function setActive(i, { scroll = true } = {}) {
  const prev = el.list.querySelector('.row.is-active');
  if (prev) prev.classList.remove('is-active');
  state.active = i;
  const item = state.rows[i];
  if (!item) { el.list.removeAttribute('aria-activedescendant'); return; }
  const li = document.getElementById(rowId(item.id));
  if (!li) return;
  li.classList.add('is-active');
  el.list.setAttribute('aria-activedescendant', li.id);
  if (scroll) li.scrollIntoView({ block: 'nearest' });
}

function openMessage(id) {
  const r = route();
  if (r.message === id) { showPane('message'); return; }
  navigate({ ...r, view: 'mail', message: id });
}

let arrowTimer = 0;

el.list.addEventListener('click', (e) => {
  // A pill filters by its term instead of opening the row.
  const p = e.target.closest('.pill');
  if (p) { editQuery(toggleOperator(el.search.value, p.dataset.kind, p.dataset.name)); return; }
  const li = e.target.closest('.row');
  if (!li) return;
  clearTimeout(arrowTimer);
  const id = Number(li.dataset.id);
  setActive(state.rows.findIndex((it) => it.id === id), { scroll: false });
  openMessage(id);
});

el.list.addEventListener('keydown', (e) => {
  const n = state.rows.length;
  let i = null;
  if (e.key === 'ArrowDown') i = clampIndex(state.active, 1, n);
  else if (e.key === 'ArrowUp') i = clampIndex(state.active < 0 ? 1 : state.active, -1, n);
  else if (e.key === 'Home') i = clampIndex(0, 0, n);
  else if (e.key === 'End') i = clampIndex(n - 1, 0, n);
  else if (e.key === 'Enter' || e.key === ' ') {
    const item = state.rows[state.active];
    clearTimeout(arrowTimer);
    if (item) { openMessage(item.id); e.preventDefault(); }
    return;
  } else return;
  e.preventDefault();
  if (i < 0) return;
  moveTo(i, false);
});

/**
 * Makes row i active. Like a desktop mail client, the message opens once
 * the key settles (wide screens), so holding a key does not fetch every
 * message; now: open immediately.
 */
function moveTo(i, now) {
  setActive(i);
  clearTimeout(arrowTimer);
  const id = state.rows[i].id;
  if (now) openMessage(id);
  else if (!narrow()) arrowTimer = setTimeout(() => openMessage(id), 140);
}

el.list.addEventListener('focus', () => {
  if (state.active < 0 && state.rows.length) {
    const sel = state.rows.findIndex((it) => it.id === route().message);
    setActive(sel >= 0 ? sel : 0);
  }
});

function checkSentinel() {
  if (route().view !== 'mail') return;
  const r = el.sentinel.getBoundingClientRect();
  const box = el.listScroll.getBoundingClientRect();
  if (r.top < box.bottom + 600 && !state.list.loading && !state.list.done && !state.list.error) loadMore();
}

new IntersectionObserver((entries) => {
  if (entries.some((en) => en.isIntersecting)) checkSentinel();
}, { root: el.listScroll, rootMargin: '0px 0px 600px 0px' }).observe(el.sentinel);

el.listRefresh.addEventListener('click', () => resetList());

// ---- message view ----

let msgAbort = null;

async function showMessage(id) {
  if (msgAbort) msgAbort.abort();
  if (!id) {
    state.message = null;
    state.thread = null;
    el.article.hidden = true;
    clear(el.article);
    el.readerEmpty.hidden = false;
    return;
  }
  const ctrl = new AbortController();
  msgAbort = ctrl;
  el.readerEmpty.hidden = true;
  el.article.hidden = false;
  const back = () => goBackToList();
  const item = state.list.items.find((it) => it.id === id);
  // The conversation is fetched alongside when it can matter.
  const wantThread = state.threaded && (!item || item.threadCount > 1);
  // Show a loading state only if the fetch is not near-instant.
  const slow = setTimeout(() => renderReaderMessage(el.article, 'Loading message', { onBack: back }), 150);
  try {
    const [msg, thread] = await Promise.all([
      api.message(id, ctrl.signal),
      wantThread ? api.thread(id, { deleted: deletedMode() }, ctrl.signal).catch((e) => {
        if (e && e.name === 'AbortError') throw e;
        return null; // the message alone still shows
      }) : null,
    ]);
    clearTimeout(slow);
    if (ctrl.signal.aborted) return;
    // Headers, Properties and Raw are looked up for one message; a newly
    // opened one starts with its body again.
    if (!state.message || state.message.id !== msg.id) {
      if (state.view !== 'text') state.view = 'html';
    }
    state.message = msg;
    setThread(thread, item);
    state.convoExpanded = new Map();
    state.convoLoaded = new Map();
    drawMessage();
    el.reader.scrollTop = 0;
    if (narrow()) { const s = el.article.querySelector('.msg-subject'); if (s) s.focus({ preventScroll: true }); }
  } catch (e) {
    clearTimeout(slow);
    // A newer selection aborted this request: never paint over it.
    if (ctrl.signal.aborted || e.name === 'AbortError') return;
    const err = openError(e);
    renderReaderMessage(el.article, err[0], {
      error: true, detail: err[1], onBack: back,
      onRetry: err.retry ? () => showMessage(id) : null,
    });
  }
}

function setThread(res, item) {
  state.thread = res ? res.items || [] : null;
  state.threadTruncated = Boolean(res && res.truncated);
  state.threadCount = threadSize(state.thread, item);
}

async function copyMessageId(mid) {
  const ok = await copyText(mid);
  toast(ok ? 'Message-ID copied' : 'Could not copy. The Message-ID is in the Properties tab.');
}

function drawMessage() {
  if (!state.message) return;
  const focusedTab = document.activeElement && document.activeElement.classList && document.activeElement.classList.contains('tab');
  const signal = msgAbort && msgAbort.signal;
  const opts = {
    view: state.view,
    onView: (v) => { state.view = v; drawMessage(); },
    onBack: () => goBackToList(),
    loadRaw: (id) => api.raw(id, signal),
    loadMessage: (id) => api.message(id, signal),
    onOpen: (id) => openMessage(id),
    onCopyId: copyMessageId,
    threadCount: state.threadCount,
    threadView: state.threaded,
    onThread: () => toggleThread(),
    onTerm: (t) => { editQuery(toggleOperator(el.search.value, t.kind, t.name)); if (narrow()) showPane('list'); },
    expanded: state.convoExpanded,
    loaded: state.convoLoaded,
    truncated: state.threadTruncated,
  };
  if (state.threaded && state.thread && state.thread.length > 1) {
    renderConversation(el.article, state.message, state.thread, opts);
  } else {
    renderMessage(el.article, state.message, opts);
  }
  if (focusedTab) {
    const t = el.article.querySelector('.tab[aria-selected="true"]');
    if (t) t.focus();
  }
}

/** Loads the open message's conversation if needed, then redraws. */
async function loadThreadThenDraw() {
  const msg = state.message;
  if (msg && state.threaded && !state.thread) {
    const signal = msgAbort && msgAbort.signal;
    try {
      const res = await api.thread(msg.id, { deleted: deletedMode() }, signal);
      if (state.message !== msg) return;
      setThread(res, null);
    } catch (e) {
      if (e && e.name === 'AbortError') return;
      toast(`The conversation could not be loaded: ${e.message}`);
    }
  }
  drawMessage();
}

function toggleThread() {
  state.threaded = !state.threaded;
  savePref('threaded', state.threaded);
  el.threadToggle.setAttribute('aria-pressed', String(state.threaded));
  toast(state.threaded ? 'Grouped by conversation' : 'Showing every message');
  redrawRows();
  loadThreadThenDraw();
}

function goBackToList() {
  const list = formatRoute({ ...route(), message: null });
  // When the list is the entry right before this one, step back to it, so
  // the browser's Back button does not return to the message afterwards.
  if (lastPush && lastPush.to === location.hash && sameRoute(parseRoute(lastPush.from), parseRoute(list)) &&
      window.history && typeof window.history.back === 'function') {
    lastPush = null;
    window.history.back();
    return;
  }
  navigate({ ...route(), message: null });
}

// ---- statistics ----

const statsReq = latest();

async function showStats() {
  clear(el.statsContent).append(h('p', { class: 'muted', attrs: { role: 'status' }, text: 'Counting messages…' }));
  try {
    const res = await statsReq.run((signal) => api.stats(signal));
    if (res.stale || route().view !== 'stats') return;
    renderStats(el.statsContent, res.value, { href: scopeHref });
  } catch (e) {
    if (e && e.name === 'AbortError') return;
    clear(el.statsContent).append(h('div', { class: 'reader-state is-error', attrs: { role: 'alert' } },
      h('p', { class: 'reader-state-title', text: 'Statistics could not be loaded.' }),
      h('p', { class: 'reader-state-detail', text: e.message }),
      h('button', { class: 'btn', type: 'button', text: 'Try again', on: { click: showStats } })));
  }
}

// ---- routing ----

function applyRoute(r) {
  const prev = state.route;
  if (prev && sameRoute(prev, r)) return;
  state.route = r;
  el.app.dataset.view = r.view;
  el.stats.hidden = r.view !== 'stats';
  const deletedChanged = !prev || deletedMode(prev) !== deletedMode(r);
  if (prev && deletedChanged) loadFacets();
  if (r.view === 'stats') {
    el.article.hidden = true;
    el.readerEmpty.hidden = true;
    showPane('message');
    renderSidebar();
    renderListHead();
    showStats();
    return;
  }
  const listChanged = !prev || prev.view !== 'mail' || !sameList(prev, r) || effectiveSort(prev) !== effectiveSort(r);
  syncSearchUI();
  if (listChanged) resetList();
  else renderListHead();
  if (!prev || prev.view !== 'mail' || r.message !== prev.message) {
    markSelected();
    showMessage(r.message);
  }
  if (r.message) showPane('message');
  else if (el.app.dataset.pane === 'message') {
    showPane('list');
    el.list.focus({ preventScroll: true });
  }
}

window.addEventListener('hashchange', () => { setDrawer(false); applyRoute(parseRoute(location.hash)); });

// ---- dialogs: shortcuts and index status ----

function openDialog(d) {
  if (typeof d.showModal === 'function') d.showModal();
  else d.setAttribute('open', '');
}

function closeDialog(d) {
  if (typeof d.close === 'function') d.close();
  else d.removeAttribute('open');
}

function keyNames(s) {
  return s.keys.map((k, i) => [i ? h('span', { class: 'key-sep', text: s.seq ? 'then' : 'or' }) : null, h('kbd', { text: k })]);
}

function buildShortcutList() {
  const rows = [...SHORTCUTS,
    { keys: ['↑', '↓'], label: 'Move in the list (opens after a pause)' },
    { keys: ['Esc'], label: 'Clear the search, or leave the search box' }];
  clear(el.shortcutsList).append(...rows.map((s) => h('div', { class: 'key-row' },
    h('dt', {}, keyNames(s)), h('dd', { text: s.label }))));
}

function openShortcuts() {
  if (!el.shortcutsList.children.length) buildShortcutList();
  openDialog(el.shortcuts);
  el.shortcutsClose.focus();
}

el.shortcutsOpen.addEventListener('click', openShortcuts);
el.shortcutsClose.addEventListener('click', () => closeDialog(el.shortcuts));
for (const d of [el.shortcuts, el.statusDialog]) {
  d.addEventListener('click', (e) => { if (e.target === d) closeDialog(d); }); // backdrop
}

const STATE_NAMES = {
  queued: 'Waiting', indexing: 'Indexing', done: 'Indexed', cached: 'Up to date', failed: 'Failed', incomplete: 'Incomplete',
};

function renderStatusDialog() {
  const s = state.status;
  if (!s) { clear(el.statusBody).append(h('p', { class: 'muted', text: 'The index status is not known yet.' })); return; }
  const p = progressInfo(s);
  const warned = new Map(sourceWarnings(s).map((w) => [w.name, w]));
  const skipped = inputWarnings(s);
  const summary = p.active
    ? `Indexing: ${p.finished} of ${plural(p.sources, 'source')} done, ${plural(p.messages, 'message')} read so far.`
    : `${plural(s.sources.reduce((n, x) => n + (x.count || 0), 0), 'message')} indexed from ${plural(s.sources.length, 'source')}.`;
  append(clear(el.statusBody), [
    h('p', { class: 'status-summary', text: summary }),
    p.error ? h('p', { class: 'status-error', text: `Indexing stopped: ${p.error}` }) : null,
    h('p', { class: 'muted', text: `Version ${s.version || 'unknown'}. Deleted messages are ${s.includeDeleted ? 'shown' : 'hidden'} by default.` }),
    skipped.warnings.length ? h('section', { class: 'status-inputs' },
      h('h3', { class: 'status-inputs-title', text: 'Input problems' }),
      h('ul', { class: 'status-warnings', attrs: { 'aria-label': 'Input problems' } },
        skipped.warnings.map((t) => h('li', { text: t }))),
      skipped.more ? h('p', { class: 'status-more', text: `${plural(skipped.more, 'more warning')} not listed.` }) : null) : null,
    h('ul', { class: 'status-sources' }, s.sources.map((x) => {
      const w = warned.get(x.name);
      const progress = x.state === 'indexing'
        ? (x.total > 0 ? `${Math.round((x.done / x.total) * 100)}%` : `${x.done.toLocaleString()} ${x.unit === 'bytes' ? 'bytes' : 'messages'} read`)
        : '';
      return h('li', { class: 'status-source' },
        h('div', { class: 'status-source-head' },
          h('span', { class: `kind-badge kind-${x.kind}`, text: kindLabel(x.kind) }),
          h('span', { class: 'status-source-name', text: x.name }),
          h('span', { class: `status-state is-${x.state}`, text: STATE_NAMES[x.state] || x.state })),
        h('p', { class: 'status-meta', text: [plural(x.count || 0, 'message'), x.unread ? `${x.unread.toLocaleString()} unread` : '',
          progress, x.warningCount ? plural(x.warningCount, 'warning') : ''].filter(Boolean).join(', ') }),
        x.error ? h('p', { class: 'status-error', text: x.error }) : null,
        w && w.warnings.length ? h('ul', { class: 'status-warnings', attrs: { 'aria-label': `Warnings for ${x.name}` } },
          w.warnings.map((t) => h('li', { text: t }))) : null,
        w && w.more ? h('p', { class: 'status-more', text: `${plural(w.more, 'more warning')} not listed.` }) : null);
    }))]);
}

function openStatus() {
  renderStatusDialog();
  openDialog(el.statusDialog);
  el.statusClose.focus();
}

el.statusOpen.addEventListener('click', openStatus);
el.bannerDetails.addEventListener('click', openStatus);
el.statusClose.addEventListener('click', () => closeDialog(el.statusDialog));

// ---- keyboard shortcuts ----

function step(delta) {
  const n = state.rows.length;
  if (!n || route().view !== 'mail') return;
  let from = state.active;
  if (from < 0) from = state.rows.findIndex((it) => it.id === route().message);
  const i = from < 0 ? 0 : clampIndex(from, delta, n);
  moveTo(i, Boolean(route().message));
}

function stepFolder(delta) {
  navigate(scopeRoute(stepScope(scopeOrder(state.trees, state.collapsed), route(), state.trees, delta)));
}

const ACTIONS = {
  next: () => step(1),
  prev: () => step(-1),
  open: () => {
    const item = state.rows[state.active] || state.rows[0];
    if (item) openMessage(item.id);
  },
  search: () => { if (route().view !== 'mail') navigate(scopeRoute({})); el.search.focus(); if (el.search.select) el.search.select(); },
  back: () => { if (route().message) goBackToList(); el.list.focus({ preventScroll: true }); },
  help: () => openShortcuts(),
  thread: () => toggleThread(),
  theme: () => cycleTheme(),
  gotoAll: () => navigate(scopeRoute({})),
  gotoStats: () => navigate({ view: 'stats' }),
  prevFolder: () => stepFolder(-1),
  nextFolder: () => stepFolder(1),
};

const keys = createDispatcher();

window.addEventListener('keydown', (e) => {
  if (e.defaultPrevented) return;
  if (el.shortcuts.open || el.statusDialog.open) {
    if (e.key === '?' && el.shortcuts.open) { e.preventDefault(); closeDialog(el.shortcuts); }
    return; // native dialogs handle Esc and keep focus inside
  }
  if (e.key === 'Escape') {
    if (el.app.classList.contains('drawer-open')) { setDrawer(false); e.preventDefault(); return; }
    if (!el.help.hidden) {
      const inside = el.help.contains(document.activeElement);
      closeHelp();
      if (inside) el.helpToggle.focus();
      e.preventDefault();
    }
    return;
  }
  const action = keys.handle(e);
  if (!action) return;
  e.preventDefault();
  ACTIONS[action]();
});

// ---- indexing status ----

let lastListRefresh = 0;
let bannerTimer = 0;

function renderBanner(prev) {
  const s = state.status;
  const b = el.banner;
  b.classList.remove('is-error', 'is-done', 'is-indeterminate');
  clearTimeout(bannerTimer);
  if (state.statusFailed) {
    b.hidden = false;
    b.classList.add('is-error', 'is-done');
    el.bannerText.textContent = 'The viewer is not responding. Is mail-export-viewer still running? Retrying…';
    el.bannerDetails.hidden = true;
    return;
  }
  const p = progressInfo(s);
  el.bannerDetails.hidden = false;
  el.statusOpen.textContent = p.warnings ? `Index status: ${plural(p.warnings, 'warning')}` : 'Index status';
  el.statusOpen.classList.toggle('has-warnings', p.warnings > 0);
  if (p.active) {
    b.hidden = false;
    const where = p.current ? ` ${p.current}` : '';
    const of = p.sources > 1 ? ` (${p.finished + 1 > p.sources ? p.sources : p.finished + 1} of ${p.sources})` : '';
    el.bannerText.textContent = `Indexing${where}${of}: ${plural(p.messages, 'message')} so far` +
      (p.warnings ? `, ${plural(p.warnings, 'warning')}` : '');
    if (p.fraction === null) b.classList.add('is-indeterminate');
    else el.bannerBar.style.setProperty('--progress', `${(p.fraction * 100).toFixed(1)}%`);
  } else if (p.error) {
    b.hidden = false;
    b.classList.add('is-error', 'is-done');
    el.bannerText.textContent = `Indexing stopped: ${p.error}`;
  } else if (prev && prev.indexing && prev.indexing.running) {
    b.hidden = false;
    b.classList.add('is-done');
    el.bannerText.textContent = `Indexing finished: ${plural(p.messages, 'message')} read` +
      (p.warnings ? `, ${plural(p.warnings, 'warning')}` : '');
    bannerTimer = setTimeout(() => { b.hidden = true; }, p.warnings ? 9000 : 4000);
  } else {
    b.hidden = true;
  }
  if (el.statusDialog.open) renderStatusDialog();
}

function refreshForNewData(finished) {
  loadFacets();
  const now = Date.now();
  // Reload the list in place only when the reader is at the top of a
  // single page; otherwise offer a button so the scroll position stays.
  const atTop = el.listScroll.scrollTop < 40 && state.list.items.length <= PAGE_SIZE;
  if (route().view !== 'mail') return;
  if (atTop && (finished || now - lastListRefresh > 2500)) {
    lastListRefresh = now;
    reloadInPlace();
  } else if (!atTop) {
    el.listRefresh.hidden = false;
  }
}

async function pollStatus() {
  let delay = 1000;
  try {
    const s = await api.status();
    const prev = state.status;
    const first = !prev;
    state.status = s;
    const wasFailed = state.statusFailed;
    state.statusFailed = false;
    renderBanner(prev);
    if (first) {
      // The server default for deleted messages is known now.
      el.deletedMode.value = deletedMode() || 'hide';
    }
    if ((prev && shouldRefresh(prev, s)) || wasFailed) refreshForNewData(!s.indexing.running);
    if (!s.indexing.running) return;
  } catch {
    state.statusFailed = true;
    renderBanner(null);
    delay = 4000; // server busy or restarting: try again more slowly
  }
  setTimeout(pollStatus, delay);
}

// ---- start ----

applyTheme();
loadFacets();
applyRoute(parseRoute(location.hash));
pollStatus();
