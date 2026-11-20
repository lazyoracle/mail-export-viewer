import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createDispatcher, SHORTCUTS, isEditable } from '../static/js/shortcuts.js';

const key = (k, extra = {}) => ({ key: k, target: { tagName: 'UL' }, ...extra });

test('single keys map to actions', () => {
  const d = createDispatcher();
  assert.equal(d.handle(key('j')), 'next');
  assert.equal(d.handle(key('k')), 'prev');
  assert.equal(d.handle(key('Enter')), 'open');
  assert.equal(d.handle(key('o')), 'open');
  assert.equal(d.handle(key('/')), 'search');
  assert.equal(d.handle(key('u')), 'back');
  assert.equal(d.handle(key('?', { shiftKey: true })), 'help');
  assert.equal(d.handle(key('t')), 'thread');
  assert.equal(d.handle(key('d')), 'theme');
  assert.equal(d.handle(key('[')), 'prevFolder');
  assert.equal(d.handle(key(']')), 'nextFolder');
  assert.equal(d.handle(key('x')), null);
  assert.equal(d.handle(key('J', { shiftKey: true })), null, 'shift+letter is not the letter');
});

test('g then a within the timeout jumps to All mail', () => {
  const d = createDispatcher({ timeout: 1000 });
  assert.equal(d.handle(key('g'), 0), null);
  assert.equal(d.pending(), 'g');
  assert.equal(d.handle(key('a'), 500), 'gotoAll');
  d.handle(key('g'), 0);
  assert.equal(d.handle(key('s'), 10), 'gotoStats');
  assert.equal(d.pending(), '');
  d.handle(key('g'), 0);
  assert.equal(d.handle(key('a'), 1500), null, 'too late: a alone does nothing');
  d.handle(key('g'), 0);
  assert.equal(d.handle(key('j'), 10), 'next', 'an unbound second key runs on its own');
});

test('modifier combos are never hijacked', () => {
  const d = createDispatcher();
  for (const m of ['ctrlKey', 'metaKey', 'altKey']) assert.equal(d.handle(key('j', { [m]: true })), null, m);
  d.handle(key('g'), 0);
  assert.equal(d.handle(key('a', { metaKey: true }), 1), null);
  assert.equal(d.pending(), '', 'a modifier combo cancels a sequence');
});

test('typing in fields is ignored', () => {
  const d = createDispatcher();
  for (const tagName of ['INPUT', 'TEXTAREA', 'SELECT']) assert.equal(d.handle(key('j', { target: { tagName } })), null);
  assert.equal(d.handle(key('j', { target: { tagName: 'DIV', isContentEditable: true } })), null);
  assert.equal(d.handle(key('j', { isComposing: true })), null);
  assert.equal(d.handle(key('j', { defaultPrevented: true })), null);
  assert.equal(d.handle(key('Enter', { target: { tagName: 'BUTTON' } })), null, 'Enter on a button clicks it');
  assert.equal(d.handle(key('Enter', { target: { tagName: 'A' } })), null);
  assert.equal(d.handle(key('j', { target: { tagName: 'BUTTON' } })), 'next', 'letters still work on buttons');
});

test('isEditable', () => {
  assert.equal(isEditable({ tagName: 'INPUT', type: 'checkbox' }), false);
  assert.equal(isEditable({ tagName: 'INPUT', type: 'search' }), true);
  assert.equal(isEditable(null), false);
});

test('the help list covers every binding', () => {
  const d = createDispatcher();
  const actions = new Set(SHORTCUTS.map((s) => s.action));
  for (const a of ['next', 'prev', 'open', 'search', 'back', 'help', 'thread', 'theme', 'gotoAll', 'gotoStats', 'prevFolder', 'nextFolder']) {
    assert.ok(actions.has(a), a);
  }
  for (const s of SHORTCUTS) assert.ok(s.keys.length && s.label, s.action);
  assert.ok(d);
});
