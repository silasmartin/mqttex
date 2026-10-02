import test from 'node:test';
import assert from 'node:assert/strict';
import { formatBytes, formatInterval } from './format.js';

test('formatBytes: plain bytes, then one decimal', () => {
  assert.equal(formatBytes(0), '0 B');
  assert.equal(formatBytes(1023), '1023 B');
  assert.equal(formatBytes(1536), '1.5 KB');
  assert.equal(formatBytes(64 << 20), '64.0 MB');
});

test('formatInterval: ms, seconds, minutes, hours', () => {
  assert.equal(formatInterval(999), '999 ms');
  assert.equal(formatInterval(1500), '1.50 s');
  assert.equal(formatInterval(59_994), '59.99 s');
  assert.equal(formatInterval(90_000), '1 min 30 s');
  assert.equal(formatInterval(3_599_400), '59 min 59 s');
  assert.equal(formatInterval(5_400_000), '1 h 30 min');
});

test('formatInterval: rounding never shows 60 s or 60 min', () => {
  assert.equal(formatInterval(59_995), '1 min 0 s');
  assert.equal(formatInterval(119_600), '2 min 0 s');
  assert.equal(formatInterval(3_599_700), '1 h 0 min');
  assert.equal(formatInterval(7_170_000), '2 h 0 min');
});
