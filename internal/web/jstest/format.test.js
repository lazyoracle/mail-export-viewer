import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  formatListDate, formatFullDate, formatSize, initials, senderLabel,
  addressLabel, addressList, avatarHue, isInlineImage, plural,
} from '../static/js/format.js';

// ICU versions differ on narrow vs. regular spaces before AM/PM.
const sp = (s) => s.replace(/[\u202f\u00a0]/g, ' ');
const opts = { locale: 'en-US', timeZone: 'UTC' };
const now = new Date('2026-11-20T15:30:00Z'); // a Friday

test('formatListDate: same day shows the time', () => {
  assert.equal(sp(formatListDate('2026-11-20T09:05:00Z', now, opts)), '9:05 AM');
});

test('formatListDate: within the last week shows the weekday', () => {
  assert.equal(formatListDate('2026-11-17T09:05:00Z', now, opts), 'Tue');
  assert.equal(formatListDate('2026-11-19T23:59:00Z', now, opts), 'Thu');
});

test('formatListDate: earlier this year shows month and day', () => {
  assert.equal(formatListDate('2026-03-04T12:00:00Z', now, opts), 'Mar 4');
  // exactly 7 days back is no longer "this week"
  assert.equal(formatListDate('2026-11-13T12:00:00Z', now, opts), 'Nov 13');
});

test('formatListDate: other years show the full short date', () => {
  assert.equal(formatListDate('2019-03-04T12:00:00Z', now, opts), '3/4/19');
});

test('formatListDate: future dates (clock skew) show the full short date', () => {
  assert.equal(formatListDate('2027-01-02T12:00:00Z', now, opts), '1/2/27');
  assert.equal(formatListDate('2026-11-21T12:00:00Z', now, opts), '11/21/26');
});

test('formatListDate: day boundaries follow the time zone', () => {
  // 01:00 UTC on the 20th is still the 19th in New York
  const ny = { locale: 'en-US', timeZone: 'America/New_York' };
  assert.equal(formatListDate('2026-11-20T01:00:00Z', now, ny), 'Thu');
});

test('formatListDate: missing or invalid dates are empty', () => {
  assert.equal(formatListDate(null, now, opts), '');
  assert.equal(formatListDate(undefined, now, opts), '');
  assert.equal(formatListDate('not a date', now, opts), '');
});

test('formatFullDate', () => {
  assert.equal(sp(formatFullDate('2023-01-02T10:00:00Z', opts)), 'Mon, Jan 2, 2023, 10:00 AM');
  assert.equal(formatFullDate(null, opts), 'Unknown date');
});

test('formatSize', () => {
  assert.equal(formatSize(0), '0 B');
  assert.equal(formatSize(1023), '1023 B');
  assert.equal(formatSize(1024), '1.0 KB');
  assert.equal(formatSize(1536), '1.5 KB');
  assert.equal(formatSize(20 * 1024), '20 KB');
  assert.equal(formatSize(5.25 * 1024 * 1024), '5.3 MB');
  assert.equal(formatSize(3 * 1024 ** 3), '3.0 GB');
  assert.equal(formatSize(-1), '');
  assert.equal(formatSize(NaN), '');
});

test('initials', () => {
  assert.equal(initials({ name: 'Ada Lovelace', email: 'ada@example.org' }), 'AL');
  assert.equal(initials({ name: 'Lovelace, Ada', email: '' }), 'AL');
  assert.equal(initials({ name: '"Grace"', email: '' }), 'G');
  assert.equal(initials({ name: 'Mary Ann Evans' }), 'ME');
  assert.equal(initials({ name: '', email: 'bob.smith@example.org' }), 'B');
  assert.equal(initials({ name: 'émile zola' }), 'ÉZ');
  assert.equal(initials({ name: '李 小龍' }), '李小');
  assert.equal(initials({ name: '', email: '' }), '?');
  assert.equal(initials(null), '?');
  assert.equal(initials({ name: '123 !!', email: '' }), '?');
});

test('senderLabel and addressLabel', () => {
  assert.equal(senderLabel({ name: 'Ann', email: 'a@example.com' }), 'Ann');
  assert.equal(senderLabel({ name: '', email: 'a@example.com' }), 'a@example.com');
  assert.equal(senderLabel({ name: '', email: '' }), '(unknown sender)');
  assert.equal(senderLabel(undefined), '(unknown sender)');
  assert.equal(addressLabel({ name: 'Ann', email: 'a@example.com' }), 'Ann <a@example.com>');
  assert.equal(addressLabel({ name: '', email: 'a@example.com' }), 'a@example.com');
  assert.equal(addressLabel({ name: 'Ann', email: '' }), 'Ann');
  assert.equal(addressList([{ name: 'A', email: 'a@example.com' }, { name: '', email: 'b@example.com' }]), 'A <a@example.com>, b@example.com');
  assert.equal(addressList(null), '');
});

test('avatarHue is stable and in range', () => {
  const h = avatarHue('ann@example.com');
  assert.equal(h, avatarHue('ann@example.com'));
  assert.ok(h >= 0 && h < 360 && Number.isInteger(h));
  assert.notEqual(avatarHue('a'), avatarHue('b'));
  assert.equal(avatarHue(''), avatarHue(undefined));
});

test('isInlineImage mirrors the server allow-list (never SVG)', () => {
  for (const t of ['image/png', 'image/JPEG', 'image/gif; name=x.gif', 'image/webp', 'image/bmp', 'image/avif']) {
    assert.ok(isInlineImage(t), t);
  }
  for (const t of ['image/svg+xml', 'image/tiff', 'text/html', '', undefined]) {
    assert.ok(!isInlineImage(t), String(t));
  }
});

test('plural', () => {
  assert.equal(plural(1, 'message'), '1 message');
  assert.equal(plural(2, 'message'), '2 messages');
  assert.equal(plural(12345, 'message'), '12,345 messages');
});

import { relativeTime, isoLocal } from '../static/js/format.js';

test('relativeTime', () => {
  const n = new Date('2026-11-20T15:30:00Z');
  const rt = (iso) => relativeTime(iso, n, 'en-US');
  assert.equal(rt('2026-11-20T15:29:40Z'), 'just now');
  assert.equal(rt('2026-11-20T15:00:00Z'), '30 minutes ago');
  assert.equal(rt('2026-11-20T10:30:00Z'), '5 hours ago');
  assert.equal(rt('2026-11-17T15:30:00Z'), '3 days ago');
  assert.equal(rt('2026-10-01T15:30:00Z'), '2 months ago');
  assert.equal(rt('2023-11-01T15:30:00Z'), '3 years ago');
  assert.equal(rt('2026-11-21T15:30:00Z'), 'tomorrow');
  assert.equal(rt(null), '');
  assert.equal(rt('garbage'), '');
});

test('isoLocal: full ISO 8601 in the viewer time zone', () => {
  assert.equal(isoLocal('2026-11-20T15:30:05Z', 'UTC'), '2026-11-20T15:30:05+00:00');
  assert.equal(isoLocal('2026-11-20T15:30:05Z', 'Asia/Kolkata'), '2026-11-20T21:00:05+05:30');
  assert.equal(isoLocal('2026-07-01T00:00:00Z', 'America/New_York'), '2026-06-30T20:00:00-04:00');
  assert.equal(isoLocal('', 'UTC'), '');
});

import { compactCount } from '../static/js/format.js';

test('compactCount shortens large sidebar counts', () => {
  assert.equal(compactCount(0), '0');
  assert.equal(compactCount(999), '999');
  assert.equal(compactCount(1234), '1.2K');
  assert.equal(compactCount(40005), '40K');
  assert.equal(compactCount(1250000), '1.3M');
});

test('date formatting reuses its Intl.DateTimeFormat objects (issue #22)', () => {
  const Orig = Intl.DateTimeFormat;
  let made = 0;
  Intl.DateTimeFormat = function (...args) { made++; return new Orig(...args); };
  try {
    const now = new Date('2024-05-10T12:00:00Z');
    for (let i = 0; i < 200; i++) {
      formatListDate(new Date(Date.UTC(2020 + (i % 5), i % 12, 1 + (i % 28))).toISOString(), now, { timeZone: 'UTC' });
      isoLocal('2024-05-10T12:00:00Z', 'UTC');
    }
  } finally {
    Intl.DateTimeFormat = Orig;
  }
  assert.ok(made <= 10, `${made} formatters built for 400 calls`);
  assert.equal(formatListDate('2024-05-10T09:30:00Z', new Date('2024-05-10T12:00:00Z'), { timeZone: 'UTC', locale: 'en-US' }), '9:30 AM');
});
