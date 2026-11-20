import { test } from 'node:test';
import assert from 'node:assert/strict';
import { nextTheme, loadTheme, saveTheme, themeLabel } from '../static/js/theme.js';

function memStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), m };
}

test('nextTheme cycles system -> light -> dark -> system', () => {
  assert.equal(nextTheme('system'), 'light');
  assert.equal(nextTheme('light'), 'dark');
  assert.equal(nextTheme('dark'), 'system');
});

test('load and save round-trip; system clears the key', () => {
  const s = memStorage();
  assert.equal(loadTheme(s), 'system');
  saveTheme(s, 'dark');
  assert.equal(loadTheme(s), 'dark');
  assert.equal(s.m.get('mail-export-viewer-theme'), 'dark', 'own storage key');
  saveTheme(s, 'system');
  assert.equal(s.m.size, 0);
  s.setItem('mail-export-viewer-theme', 'neon');
  assert.equal(loadTheme(s), 'system');
});

test('storage failures are swallowed', () => {
  const broken = { getItem() { throw new Error('denied'); }, setItem() { throw new Error('denied'); }, removeItem() { throw new Error('denied'); } };
  assert.equal(loadTheme(broken), 'system');
  assert.doesNotThrow(() => saveTheme(broken, 'dark'));
  assert.equal(loadTheme(null), 'system');
  assert.doesNotThrow(() => saveTheme(undefined, 'dark'));
});

test('themeLabel names current and next theme', () => {
  assert.equal(themeLabel('dark'), 'Theme: dark. Switch to system');
});
