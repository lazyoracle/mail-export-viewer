import { test } from 'node:test';
import assert from 'node:assert/strict';
import { highlightSegments, linkify, decodeEntities } from '../static/js/text.js';

test('decodeEntities handles the escapes the server emits', () => {
  assert.equal(decodeEntities('a &amp; b &lt;c&gt; &quot;d&quot; &#39;e&#39; &#x27;f&#34;'), `a & b <c> "d" 'e' 'f"`);
  assert.equal(decodeEntities('&amp;lt;'), '&lt;');
  assert.equal(decodeEntities('&bogus; &#xZZ;'), '&bogus; &#xZZ;');
  assert.equal(decodeEntities('&#0; &#x110000;'), '&#0; &#x110000;');
});

test('highlightSegments splits on <mark> only', () => {
  assert.deepEqual(highlightSegments('the <mark>quick</mark> fox'), [
    { text: 'the ', mark: false }, { text: 'quick', mark: true }, { text: ' fox', mark: false },
  ]);
});

test('highlightSegments keeps any other markup as literal text', () => {
  const segs = highlightSegments('&lt;b&gt; <img src=x onerror=alert(1)> <mark>&amp;</mark>');
  assert.deepEqual(segs, [
    { text: '<b> <img src=x onerror=alert(1)> ', mark: false }, { text: '&', mark: true },
  ]);
});

test('highlightSegments tolerates unbalanced and nested marks', () => {
  assert.deepEqual(highlightSegments('a</mark>b<mark>c'), [
    { text: 'ab', mark: false }, { text: 'c', mark: true },
  ]);
  assert.deepEqual(highlightSegments('<mark><mark>x</mark></mark>y'), [
    { text: 'x', mark: true }, { text: 'y', mark: false },
  ]);
  assert.deepEqual(highlightSegments(''), []);
  assert.deepEqual(highlightSegments(null), []);
});

const H = 'http' + 's:' + '//'; // keep scheme literals out of shipped files only

test('linkify finds web and mailto links', () => {
  assert.deepEqual(linkify(`see ${H}example.com/a?b=1 now`), [
    { text: 'see ' }, { text: `${H}example.com/a?b=1`, href: `${H}example.com/a?b=1` }, { text: ' now' },
  ]);
  assert.deepEqual(linkify('mail mailto:a@example.org.'), [
    { text: 'mail ' }, { text: 'mailto:a@example.org', href: 'mailto:a@example.org' }, { text: '.' },
  ]);
});

test('linkify trims trailing punctuation but keeps balanced parens', () => {
  assert.deepEqual(linkify(`(${H}x.org/a_(b))`), [
    { text: '(' }, { text: `${H}x.org/a_(b)`, href: `${H}x.org/a_(b)` }, { text: ')' },
  ]);
  assert.deepEqual(linkify(`${H}x.org/, next`), [
    { text: `${H}x.org/`, href: `${H}x.org/` }, { text: ', next' },
  ]);
});

test('linkify never produces script or data links', () => {
  for (const s of ['javascript:alert(1)', 'data:text/html,<b>', 'vbscript:x', 'file:///etc/passwd']) {
    assert.deepEqual(linkify(s), [{ text: s }]);
  }
  // A scheme with no host is not a link.
  assert.deepEqual(linkify('http' + '://'), [{ text: 'http' + '://' }]);
});

test('linkify of plain text is a single segment; empty is empty', () => {
  assert.deepEqual(linkify('no links here'), [{ text: 'no links here' }]);
  assert.deepEqual(linkify(''), []);
});

import { markTerms } from '../static/js/text.js';

const marked = (segs) => segs.map((s) => (s.mark ? `[${s.text}]` : s.text)).join('');

test('markTerms marks word-start matches, case-insensitively', () => {
  assert.equal(marked(markTerms('Quarterly report: Reports due', ['report'])), 'Quarterly [report]: [Report]s due');
  assert.equal(marked(markTerms('unreported', ['report'])), 'unreported', 'not mid-word');
  assert.equal(marked(markTerms('The big deal is big', ['big deal'])), 'The [big deal] is big');
  assert.equal(marked(markTerms('Re: Lunch?', ['lunch', 're'])), '[Re]: [Lunch]?');
  assert.equal(marked(markTerms('Café olé', ['cafe', 'OLE'])), '[Café] [olé]', 'diacritics folded like the index');
  assert.equal(marked(markTerms('aaa', ['a', 'aa'])), '[aa]a', 'longest match wins, no overlaps');
  assert.equal(marked(markTerms('plain', [])), 'plain');
  assert.deepEqual(markTerms('', ['x']), []);
  assert.equal(marked(markTerms('x <b>y</b>', ['<b>y'])), 'x [<b>y]</b>', 'text stays text');
});

// Hostile inputs: each must finish quickly (linear time), not freeze the tab.
const timed = (fn) => { const t = performance.now(); const v = fn(); return [v, performance.now() - t]; };
const LONG = 300000;

test('linkify is linear on a URL with a huge tail of closing parens or dots', () => {
  for (const tail of [')'.repeat(LONG), '.'.repeat(LONG), ').'.repeat(LONG / 2), `${'('.repeat(LONG / 2)}${')'.repeat(LONG / 2)}`]) {
    const s = `${H}x.org/a${tail}`;
    const [segs, ms] = timed(() => linkify(s));
    assert.ok(ms < 500, `took ${ms} ms`);
    assert.equal(segs.map((x) => x.text).join(''), s, 'no text lost');
  }
});

test('linkify leaves URLs over 2048 characters as plain text', () => {
  const long = `${H}x.org/${'a'.repeat(3000)}`;
  assert.deepEqual(linkify(`see ${long}`), [{ text: `see ${long}` }]);
  const ok = `${H}x.org/${'a'.repeat(1900)}`;
  assert.equal(linkify(ok)[0].href, ok);
});

test('linkify trims a long run of trailing punctuation correctly', () => {
  assert.deepEqual(linkify(`${H}x.org/a)))...`), [{ text: `${H}x.org/a`, href: `${H}x.org/a` }, { text: ')))...' }]);
  assert.deepEqual(linkify(`${H}x.org/a_(b)).`), [{ text: `${H}x.org/a_(b)`, href: `${H}x.org/a_(b)` }, { text: ').' }]);
});

test('linkify, highlightSegments, markTerms and decodeEntities stay fast on long text', () => {
  const words = 'lorem ipsum http://x.org/p dolor '.replace('http', 'ht' + 'tp').repeat(10000);
  assert.ok(timed(() => linkify(words))[1] < 1000);
  const hl = '<mark>a</mark>&amp;b '.repeat(30000);
  assert.ok(timed(() => highlightSegments(hl))[1] < 1000);
  assert.ok(timed(() => decodeEntities('&amp;&#x41;'.repeat(50000)))[1] < 1000);
  assert.ok(timed(() => markTerms('ab'.repeat(50000), ['ab', 'a', 'abab']))[1] < 1000);
  assert.ok(timed(() => markTerms('é'.repeat(LONG / 3), ['e']))[1] < 2000);
});
