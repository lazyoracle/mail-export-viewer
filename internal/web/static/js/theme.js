// Theme preference: 'system' (follow prefers-color-scheme), 'light' or
// 'dark', stored in localStorage when available.

const KEY = 'mail-export-viewer-theme';
export const THEMES = ['system', 'light', 'dark'];

/** The theme after t in the toggle cycle. */
export function nextTheme(t) {
  return THEMES[(THEMES.indexOf(t) + 1) % THEMES.length];
}

export function loadTheme(storage) {
  try {
    const t = storage && storage.getItem(KEY);
    return t === 'light' || t === 'dark' ? t : 'system';
  } catch {
    return 'system';
  }
}

export function saveTheme(storage, t) {
  try {
    if (!storage) return;
    if (t === 'system') storage.removeItem(KEY);
    else storage.setItem(KEY, t);
  } catch {
    // Private mode or blocked storage: the choice lasts for this page only.
  }
}

/** Text for the toggle button. */
export function themeLabel(t) {
  const next = nextTheme(t);
  const name = { system: 'system', light: 'light', dark: 'dark' };
  return `Theme: ${name[t]}. Switch to ${name[next]}`;
}
