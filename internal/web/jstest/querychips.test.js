import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  tokenize, queryChips, removeToken, toggleOperator, hasOperator, formatValue, hasTextQuery, highlightTerms,
  isQueryDate, chipLabel, currentToken, suggest, applySuggestion, OPERATORS,
} from '../static/js/querychips.js';

test('tokenize mirrors the server parser', () => {
  const t = tokenize('from:ann "big deal" -spam folder:"My Box" x:y has:att is:Unread tag:Work category:Red cc:z');
  assert.deepEqual(t.map((x) => [x.raw, x.neg, x.key, x.value]), [
    ['from:ann', false, 'from', 'ann'],
    ['"big deal"', false, null, 'big deal'],
    ['-spam', true, null, 'spam'],
    ['folder:"My Box"', false, 'folder', 'My Box'],
    ['x:y', false, null, 'x:y'],
    ['has:att', false, 'has', 'att'],
    ['is:Unread', false, 'is', 'Unread'],
    ['tag:Work', false, 'tag', 'Work'],
    ['category:Red', false, 'category', 'Red'],
    ['cc:z', false, 'cc', 'z'],
  ]);
  assert.equal(tokenize('FROM:Bob')[0].key, 'from');
  assert.equal(tokenize('subject:"unterminated rest')[0].value, 'unterminated rest');
  assert.equal(tokenize('label:"Say \\"hi\\""')[0].value, 'Say "hi"');
  assert.deepEqual(tokenize('  '), []);
  assert.equal(tokenize('- x')[0].raw, '-', 'a lone dash is a term');
  const [a, b] = tokenize('ab  cd');
  assert.deepEqual([a.start, a.end, b.start, b.end], [0, 2, 4, 6]);
});

test('malformed operator values are plain text, as on the server', () => {
  for (const q of ['before:soon', 'has:pdf', 'is:purple', 'after:2023-13-01']) {
    assert.equal(tokenize(q)[0].key, null, q);
  }
  for (const q of ['is:read', 'is:seen', 'is:flagged', 'is:spam', 'is:trash', 'is:replied', 'is:important', 'is:attachment', 'after:2023/02', 'before:2024']) {
    assert.ok(tokenize(q)[0].key, q);
  }
  assert.ok(isQueryDate('2024-02-29'));
  assert.ok(!isQueryDate('2023-02-29'));
  assert.ok(isQueryDate('2024/02'));
});

test('queryChips lists operator tokens with their index', () => {
  const c = queryChips('invoice from:ann -label:"Old stuff" is:unread x:y');
  assert.deepEqual(c.map((x) => [x.index, x.key, x.value, x.neg]), [
    [1, 'from', 'ann', false], [2, 'label', 'Old stuff', true], [3, 'is', 'unread', false],
  ]);
  assert.deepEqual(queryChips(''), []);
});

test('chipLabel says what a chip does in words', () => {
  const l = (q) => chipLabel(queryChips(q)[0]);
  assert.equal(l('from:ann'), 'From: ann');
  assert.equal(l('-from:ann'), 'Not from: ann');
  assert.equal(l('is:unread'), 'Unread');
  assert.equal(l('is:read'), 'Read');
  assert.equal(l('-is:starred'), 'Not starred');
  assert.equal(l('is:flagged'), 'Starred');
  assert.equal(l('has:attachment'), 'Has attachments');
  assert.equal(l('-has:att'), 'No attachments');
  assert.equal(l('before:2024'), 'Before 2024');
  assert.equal(l('-before:2024'), 'From 2024');
  assert.equal(l('after:2024-03'), 'From 2024-03');
  assert.equal(l('mailbox:Inbox'), 'Folder: Inbox');
  assert.equal(l('tag:"To do"'), 'Tag: To do');
  assert.equal(l('filename:pdf'), 'Attachment name: pdf');
});

test('removeToken drops one token and keeps the rest verbatim', () => {
  assert.equal(removeToken('a from:ann  "b c"', 1), 'a "b c"');
  assert.equal(removeToken('from:ann', 0), '');
  assert.equal(removeToken('a b', 5), 'a b');
});

test('toggleOperator adds a positive operator first, or removes it', () => {
  assert.equal(toggleOperator('invoice', 'label', 'Work'), 'label:Work invoice');
  assert.equal(toggleOperator('label:work invoice', 'label', 'Work'), 'invoice', 'names compare ignoring case');
  assert.equal(toggleOperator('', 'tag', 'To do'), 'tag:"To do"');
  assert.equal(toggleOperator('-tag:x', 'tag', 'x'), 'tag:x -tag:x', 'a negated token is a different filter');
  assert.equal(toggleOperator('is:unread', 'is', 'unread'), '');
  assert.equal(toggleOperator('x ', 'is', 'starred'), 'is:starred x ', 'trailing space kept');
  assert.ok(hasOperator('a category:"Red Team"', 'category', 'red team'));
  assert.ok(!hasOperator('-category:Red', 'category', 'Red'));
  assert.ok(hasOperator('is:flagged', 'is', 'starred'), 'aliases count');
});

test('formatValue quotes values with spaces or quotes, escaping \\ and " inside', () => {
  assert.equal(formatValue('Inbox'), 'Inbox');
  assert.equal(formatValue('My Box'), '"My Box"');
  assert.equal(formatValue('say "hi"'), '"say \\"hi\\""');
  assert.equal(formatValue('C:\\x'), 'C:\\x', 'unquoted backslash is literal');
  assert.equal(formatValue('a \\ b'), '"a \\\\ b"');
  assert.equal(formatValue('a\tb'), '"a b"');
  assert.equal(formatValue(''), '""');
  for (const v of ['My Box', 'say "hi"', 'a \\ b', 'Work, Projects']) {
    assert.equal(tokenize(`label:${formatValue(v)}`)[0].value, v, v);
  }
});

test('hasTextQuery and highlightTerms', () => {
  assert.equal(hasTextQuery('hello'), true);
  assert.equal(hasTextQuery('from:ann'), true);
  assert.equal(hasTextQuery('cc:bob'), true);
  assert.equal(hasTextQuery('is:unread label:x -word'), false);
  assert.equal(hasTextQuery(''), false);
  assert.deepEqual(highlightTerms('budget "big deal" subject:plan from:ann -no'), ['budget', 'big deal', 'plan']);
});

test('currentToken finds the token under the caret', () => {
  assert.deepEqual(currentToken('from:a lab', 10), { start: 7, end: 10, text: 'lab', neg: false, key: null, value: 'lab' });
  assert.deepEqual(currentToken('x -is:un', 8), { start: 2, end: 8, text: '-is:un', neg: true, key: 'is', value: 'un' });
  assert.deepEqual(currentToken('x ', 2), { start: 2, end: 2, text: '', neg: false, key: null, value: '' });
});

const facets = {
  labels: [{ name: 'Inbox' }, { name: 'Work' }],
  tags: [{ name: 'To do' }, { name: 'Important' }],
  categories: [{ name: 'Finance' }],
  folders: [{ path: 'Inbox', children: [{ path: 'Inbox/Work', children: [] }] }],
};

test('suggest offers operators while typing a word', () => {
  const s = suggest(currentToken('fr', 2), facets);
  assert.deepEqual(s.map((x) => x.insert), ['from:']);
  assert.ok(suggest(currentToken('t', 1), facets).some((x) => x.insert === 'tag:'));
  assert.deepEqual(suggest(currentToken('', 0), facets), [], 'nothing typed: nothing offered');
  assert.deepEqual(suggest(currentToken('from:', 5), facets), [], 'free-form value');
});

test('suggest offers values from the facets', () => {
  const v = (q) => suggest(currentToken(q, q.length), facets).map((x) => x.insert);
  assert.deepEqual(v('is:un'), ['is:unread', 'is:junk'], 'prefix matches first, then substrings');
  assert.deepEqual(v('has:'), ['has:attachment']);
  assert.deepEqual(v('tag:to'), ['tag:"To do"']);
  assert.deepEqual(v('label:w'), ['label:Work']);
  assert.deepEqual(v('category:'), ['category:Finance']);
  assert.deepEqual(v('folder:wo'), ['folder:Inbox/Work']);
  assert.deepEqual(v('-is:star'), ['-is:starred']);
  assert.deepEqual(v('is:unread'), [], 'already complete');
});

test('applySuggestion replaces the token and places the caret', () => {
  const q = 'x fr y';
  assert.deepEqual(applySuggestion(q, currentToken(q, 4), 'from:'), { value: 'x from: y', caret: 7 });
  const q2 = 'a -is:un';
  assert.deepEqual(applySuggestion(q2, currentToken(q2, q2.length), '-is:unread'), { value: 'a -is:unread ', caret: 13 });
});

test('OPERATORS documents every server operator', () => {
  const keys = OPERATORS.map((o) => o.key);
  for (const k of ['from', 'to', 'cc', 'subject', 'folder', 'label', 'category', 'tag', 'is', 'has', 'filename', 'before', 'after']) {
    assert.ok(keys.includes(k), k);
  }
  for (const o of OPERATORS) assert.ok(o.help && o.example, o.key);
});
