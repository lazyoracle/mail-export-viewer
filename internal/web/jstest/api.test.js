import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readJSON, readCapped, ApiError, api, messagesURL } from '../static/js/api.js';

const abortError = () => new DOMException('The operation was aborted.', 'AbortError');

test('readJSON rethrows an abort that happens while reading the body', async () => {
  const ctrl = new AbortController();
  const res = {
    ok: true, status: 200,
    json: () => new Promise((_, reject) => ctrl.signal.addEventListener('abort', () => reject(abortError()))),
  };
  const p = readJSON(res);
  ctrl.abort();
  await assert.rejects(p, (e) => e.name === 'AbortError' && !(e instanceof ApiError));
});

test('readJSON rethrows abort on error responses too', async () => {
  const res = { ok: false, status: 500, json: async () => { throw abortError(); } };
  await assert.rejects(readJSON(res), (e) => e.name === 'AbortError');
});

test('api.message propagates an abort mid-body from fetch', async (t) => {
  const ctrl = new AbortController();
  t.mock.method(globalThis, 'fetch', async (url, opts) => ({
    ok: true, status: 200,
    json: () => new Promise((_, reject) => opts.signal.addEventListener('abort', () => reject(abortError()))),
  }));
  const p = api.message(5, ctrl.signal);
  await new Promise((r) => setTimeout(r, 0));
  ctrl.abort();
  await assert.rejects(p, (e) => e.name === 'AbortError');
});

test('readCapped returns whole small bodies', async () => {
  const r = await readCapped(new Response('From: a\r\n\r\nhé'), 1024);
  assert.deepEqual(r, { text: 'From: a\r\n\r\nhé', truncated: false });
});

test('readCapped stops at the byte cap', async () => {
  const body = new ReadableStream({
    start(c) { for (let i = 0; i < 5; i++) c.enqueue(new TextEncoder().encode('abcd')); c.close(); },
  });
  const r = await readCapped(new Response(body), 10);
  assert.deepEqual(r, { text: 'abcdabcdab', truncated: true });
});

test('api.raw: error bodies become ApiError, success is capped text', async (t) => {
  const fetchMock = t.mock.method(globalThis, 'fetch', async (url) => (url.endsWith('/9/raw')
    ? new Response(JSON.stringify({ error: 'not found' }), { status: 404 })
    : new Response('x'.repeat(50))));
  await assert.rejects(api.raw(9, undefined, 10), (e) => e instanceof ApiError && e.status === 404 && e.message === 'not found');
  assert.deepEqual(await api.raw(3, undefined, 10), { text: 'x'.repeat(10), truncated: true });
  assert.equal(fetchMock.mock.calls[1].arguments[0], '/api/messages/3/raw');
});

test('messagesURL builds the API query with every scope parameter', () => {
  assert.equal(messagesURL({}), '/api/messages?limit=100');
  assert.equal(messagesURL({ limit: 50 }), '/api/messages?limit=50');
  assert.equal(messagesURL({ q: 'from:ann x', sort: 'date_asc', cursor: 'a+b/=' }),
    '/api/messages?limit=100&q=from%3Aann+x&sort=date_asc&cursor=a%2Bb%2F%3D');
  assert.equal(messagesURL({ source: 3, folderId: 7, folder: 'Mail/In box', deleted: true }),
    '/api/messages?limit=100&source=3&folderId=7&folder=Mail%2FIn+box&deleted=only');
  assert.equal(messagesURL({ source: null, folderId: 0, folder: '', deleted: false, sort: '' }), '/api/messages?limit=100');
});

test('api endpoints hit the documented paths', async (t) => {
  const urls = [];
  t.mock.method(globalThis, 'fetch', async (url) => { urls.push(url); return new Response('{"items":[]}'); });
  await api.status();
  await api.facets();
  await api.stats();
  await api.message(12);
  await api.thread(12);
  await api.messages({ q: 'x' });
  assert.deepEqual(urls, ['/api/status', '/api/facets', '/api/stats', '/api/messages/12', '/api/threads/12', '/api/messages?limit=100&q=x']);
  assert.equal(api.rawURL(12), '/api/messages/12/raw');
});

test('a network failure becomes an ApiError with status 0', async (t) => {
  t.mock.method(globalThis, 'fetch', async () => { throw new TypeError('Failed to fetch'); });
  await assert.rejects(api.status(), (e) => e instanceof ApiError && e.status === 0 && /not responding/.test(e.message));
});
