// Keyboard shortcut dispatcher, pure: maps keydown events to action names.
// Shortcuts never fire while typing in a field, with Ctrl/Cmd/Alt held
// (those belong to the browser and the OS), or during IME composition.

/** keys: what the help overlay shows; seq: the key or two-key sequence. */
export const SHORTCUTS = [
  { action: 'next', keys: ['j'], label: 'Next message' },
  { action: 'prev', keys: ['k'], label: 'Previous message' },
  { action: 'open', keys: ['Enter', 'o'], label: 'Open message' },
  { action: 'back', keys: ['u'], label: 'Back to the list' },
  { action: 'search', keys: ['/'], label: 'Search' },
  { action: 'thread', keys: ['t'], label: 'Conversation view on or off' },
  { action: 'gotoAll', keys: ['g', 'a'], seq: true, label: 'Go to All mail' },
  { action: 'gotoStats', keys: ['g', 's'], seq: true, label: 'Go to Statistics' },
  { action: 'prevFolder', keys: ['['], label: 'Previous folder' },
  { action: 'nextFolder', keys: [']'], label: 'Next folder' },
  { action: 'theme', keys: ['d'], label: 'Switch theme' },
  { action: 'help', keys: ['?'], label: 'Show shortcuts' },
];

const SINGLE = new Map();
const SEQUENCES = new Map();
for (const s of SHORTCUTS) {
  if (s.seq) SEQUENCES.set(s.keys.join(' '), s.action);
  else for (const k of s.keys) SINGLE.set(k, s.action);
}
const PREFIXES = new Set([...SEQUENCES.keys()].map((k) => k.split(' ')[0]));

export function isEditable(t) {
  if (!t) return false;
  if (t.isContentEditable) return true;
  const tag = String(t.tagName || '').toUpperCase();
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true;
  if (tag === 'INPUT') return !['checkbox', 'radio', 'button', 'submit', 'reset', 'range', 'color'].includes(String(t.type || 'text').toLowerCase());
  return false;
}

export function createDispatcher({ timeout = 1200 } = {}) {
  let prefix = '';
  let prefixAt = 0;
  return {
    pending: () => prefix,
    reset() { prefix = ''; },
    /** The action for keydown event e, or null. now: ms timestamp. */
    handle(e, now = Date.now()) {
      if (e.ctrlKey || e.metaKey || e.altKey) { prefix = ''; return null; }
      if (e.isComposing || e.defaultPrevented || isEditable(e.target)) return null;
      const k = e.key;
      const tag = String((e.target && e.target.tagName) || '').toUpperCase();
      // Enter on a control activates it; leave that alone.
      if (k === 'Enter' && (tag === 'BUTTON' || tag === 'A' || tag === 'SUMMARY')) return null;
      if (prefix) {
        const seq = `${prefix} ${k}`;
        const live = now - prefixAt <= timeout;
        prefix = '';
        if (live && SEQUENCES.has(seq)) return SEQUENCES.get(seq);
      }
      if (PREFIXES.has(k)) { prefix = k; prefixAt = now; return null; }
      return SINGLE.get(k) || null;
    },
  };
}
