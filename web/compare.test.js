import test from 'node:test';
import assert from 'node:assert/strict';
import { flatten, columnLabels, buildRows, substituteDevice } from './compare.js';

const obj = (m) => Object.fromEntries(m);

test('flatten: nested objects and arrays become paths, scalars text', () => {
  const m = flatten('{"a":{"b":1,"c":"x"},"l":[true,null,{"d":2.5}],"e":{},"f":[]}');
  assert.deepEqual([...m.keys()], ['a.b', 'a.c', 'l[0]', 'l[1]', 'l[2].d', 'e', 'f']);
  assert.deepEqual(obj(m), { 'a.b': '1', 'a.c': 'x', 'l[0]': 'true', 'l[1]': 'null', 'l[2].d': '2.5', e: '{}', f: '[]' });
});

test('flatten: top-level array uses index paths', () => {
  assert.deepEqual(obj(flatten(' [1, [2]] ')), { '[0]': '1', '[1][0]': '2' });
});

test('flatten: anything else is one (payload) field', () => {
  assert.deepEqual(obj(flatten('on')), { '(payload)': 'on' });
  assert.deepEqual(obj(flatten('42')), { '(payload)': '42' });
  assert.deepEqual(obj(flatten('{broken')), { '(payload)': '{broken' });
  assert.deepEqual(obj(flatten('')), { '(payload)': '' });
  assert.deepEqual(obj(flatten('{}')), { '(payload)': '{}' });
});

test('flatten: a cut-off payload is shown raw with a hint', () => {
  assert.deepEqual(obj(flatten('{"a":1', true)), { '(payload)': '{"a":1', '(truncated)': 'yes' });
});

test('columnLabels: drops shared leading and trailing segments', () => {
  assert.deepEqual(columnLabels(['/topic/SN1/V0/post/json', '/topic/SN2/V0/post/json']), ['SN1', 'SN2']);
  assert.deepEqual(columnLabels(['a/x/y/z', 'a/q/z']), ['x/y', 'q']);
  assert.deepEqual(columnLabels(['/topic/SN1/V0/post/json', '/topic/SN1/V0/response']), ['post/json', 'response']);
});

test('columnLabels: one topic or empty rest keeps the full name', () => {
  assert.deepEqual(columnLabels([]), []);
  assert.deepEqual(columnLabels(['/topic/SN1/V0/post/json']), ['/topic/SN1/V0/post/json']);
  assert.deepEqual(columnLabels(['a/b', 'a/b/c']), ['a/b', 'c']);
});

test('buildRows: order of first appearance, missing values, differences', () => {
  const rows = buildRows([new Map([['a', '1'], ['b', '2']]), new Map([['a', '1'], ['c', '3'], ['b', '9']])]);
  assert.deepEqual(rows, [
    { path: 'a', values: ['1', '1'], differs: false },
    { path: 'b', values: ['2', '9'], differs: true },
    { path: 'c', values: [undefined, '3'], differs: true },
  ]);
});

test('buildRows: topics without a message do not count as different', () => {
  const rows = buildRows([null, new Map([['a', '1']]), new Map([['a', '1']])]);
  assert.deepEqual(rows, [{ path: 'a', values: [undefined, '1', '1'], differs: false }]);
  assert.deepEqual(buildRows([new Map([['a', '1']])]), [{ path: 'a', values: ['1'], differs: false }]);
  assert.deepEqual(buildRows([null, null]), []);
});

test('substituteDevice: replaces the label segment', () => {
  assert.equal(substituteDevice('/topic/SN1/V0/post/json', 'SN1', 'SN7'), '/topic/SN7/V0/post/json');
  assert.equal(substituteDevice('a/x/y/z', 'x/y', 'q'), 'a/q/z');
  assert.equal(substituteDevice('/topic/SN1/V0/post/json', 'SN1', '  SN7 '), '/topic/SN7/V0/post/json');
});

test('substituteDevice: full topic, empty input, single-column guess', () => {
  assert.equal(substituteDevice('/topic/SN1/V0/post/json', 'SN1', '/other/topic'), '/other/topic');
  assert.equal(substituteDevice('/topic/SN1/V0/post/json', 'SN1', '  '), null);
  // One column: the label is the whole topic, so the device segment is guessed.
  const t = '/topic/A1B2C3D4/V0/post/json';
  assert.equal(substituteDevice(t, t, 'Z9Y8X7W6'), '/topic/Z9Y8X7W6/V0/post/json');
  assert.equal(substituteDevice(t, t, 'SN9'), '/topic/SN9/V0/post/json');
  assert.equal(substituteDevice('a/b', 'a/b', 'xyz'), null);
});
