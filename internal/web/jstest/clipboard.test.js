import { test } from 'node:test';
import assert from 'node:assert/strict';
import { copyText } from '../static/js/clipboard.js';

function fakeDoc(execResult) {
  const log = [];
  const body = { append: (n) => { log.push(['append', n.value]); n.parent = body; } };
  return {
    log,
    body,
    activeElement: { focus: () => log.push(['refocus']) },
    createElement: (tag) => ({
      tag, value: '', className: '', attrs: {},
      setAttribute(k, v) { this.attrs[k] = v; },
      select() { log.push(['select', this.value]); },
      remove() { log.push(['remove']); },
    }),
    execCommand: (cmd) => { log.push(['exec', cmd]); if (execResult instanceof Error) throw execResult; return execResult; },
  };
}

test('copyText uses the async clipboard when available', async () => {
  let got;
  const doc = fakeDoc(true);
  assert.equal(await copyText('<a@b>', { nav: { clipboard: { writeText: async (t) => { got = t; } } }, doc }), true);
  assert.equal(got, '<a@b>');
  assert.deepEqual(doc.log, [], 'no fallback');
});

test('copyText falls back to selecting text and copying', async () => {
  const doc = fakeDoc(true);
  const nav = { clipboard: { writeText: async () => { throw new Error('denied'); } } };
  assert.equal(await copyText('id@x', { nav, doc }), true);
  assert.deepEqual(doc.log, [['append', 'id@x'], ['select', 'id@x'], ['exec', 'copy'], ['remove'], ['refocus']]);
});

test('copyText reports failure when nothing works', async () => {
  assert.equal(await copyText('x', { nav: {}, doc: fakeDoc(false) }), false);
  assert.equal(await copyText('x', { nav: {}, doc: fakeDoc(new Error('nope')) }), false);
});
