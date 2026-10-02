import test from 'node:test';
import assert from 'node:assert/strict';
import { flatten, digest, columnLabels, buildRows, deviceTopics } from './compare.js';

const obj = (m) => Object.fromEntries(m);

test('flatten: nested objects and arrays become paths, scalars text', () => {
  const m = flatten('{"a":{"b":1,"c":"x"},"l":[true,null,{"d":2.5}],"e":{},"f":[]}');
  assert.deepEqual([...m.keys()], ['a.b', 'a.c', 'l[0]', 'l[1]', 'l[2].d', 'e', 'f']);
  assert.deepEqual(obj(m), { 'a.b': '1', 'a.c': '"x"', 'l[0]': 'true', 'l[1]': 'null', 'l[2].d': '2.5', e: '{}', f: '[]' });
});

test('flatten: strings and numbers stay apart, numbers keep their digits', () => {
  const a = flatten('{"t":"21","n":null,"id":12345678901234567891,"v":1.0}');
  const b = flatten('{"t":21,"n":"null","id":12345678901234567892,"v":1}');
  assert.deepEqual(obj(a), { t: '"21"', n: 'null', id: '12345678901234567891', v: '1.0' });
  assert.deepEqual(obj(b), { t: '21', n: '"null"', id: '12345678901234567892', v: '1' });
  assert.ok(buildRows([a, b]).every((r) => r.differs));
});

test('flatten: keys that look like path syntax are quoted, nothing collides', () => {
  assert.deepEqual(obj(flatten('{"a.b":1,"a":{"b":2}}')), { '["a.b"]': '1', 'a.b': '2' });
  assert.deepEqual(obj(flatten('{"":{"x":1},"x":2}')), { '[""].x': '1', x: '2' });
  assert.deepEqual(obj(flatten('{"o":{"[0]":1,"q\\"":2},"o2":[3]}')), { 'o["[0]"]': '1', 'o["q\\""]': '2', 'o2[0]': '3' });
  // Not the same as the (payload) row of a plain 1, nor of a nested key.
  assert.deepEqual(obj(flatten('{"(payload)":1,"o":{"(x)":2}}')), { '["(payload)"]': '1', 'o.(x)': '2' });
});

test('flatten: long values are cut, the hash keeps them apart', () => {
  const long = 'x'.repeat(300);
  const a = flatten(`{"s":"${long}a"}`).get('s');
  const b = flatten(`{"s":"${long}b"}`).get('s');
  assert.ok(a.startsWith(`"${'x'.repeat(199)} ... (303 characters, #`), a);
  assert.ok(a.length < 260);
  assert.notEqual(a, b);
  const raw = flatten(`not json ${long}`).get('(payload)');
  assert.ok(raw.endsWith(`(309 characters, #${digest(`not json ${long}`)})`), raw);
  assert.equal(flatten('{"s":"short"}').get('s'), '"short"');
});

test('digest: stable 8-digit hex', () => {
  assert.equal(digest(''), '811c9dc5');
  assert.equal(digest('a'), 'e40c292c');
  assert.notEqual(digest('ab'), digest('ba'));
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

test('deviceTopics: the segment where compared topics differ comes first', () => {
  const two = ['/topic/SN1/V0/post/json', '/topic/SN2/V0/post/json'];
  assert.equal(deviceTopics(two, 'SN7')[0], '/topic/SN7/V0/post/json');
  assert.equal(deviceTopics(two, '  SN7 ')[0], '/topic/SN7/V0/post/json');
  // Replaced by position, not by the first equal text.
  assert.equal(deviceTopics(['/1/topic/1/x', '/1/topic/2/x'], '7')[0], '/1/topic/7/x');
});

test('deviceTopics: several differing segments keep the others of each topic', () => {
  const g = deviceTopics(['/topic/SN1/V0/post/json', '/topic/SN2/V1/post/json'], 'SN7');
  assert.deepEqual(g.slice(0, 4), ['/topic/SN7/V0/post/json', '/topic/SN1/SN7/post/json', '/topic/SN7/V1/post/json', '/topic/SN2/SN7/post/json']);
});

test('deviceTopics: topics of one device are guessed, not cut by their label', () => {
  const g = deviceTopics(['/topic/SN1/V0/post/json', '/topic/SN1/V0/response'], 'SN7');
  assert.deepEqual(g.slice(0, 2), ['/topic/SN7/V0/post/json', '/topic/SN1/SN7/post/json']);
  assert.ok(g.includes('/topic/SN7/V0/response'));
});

test('deviceTopics: single topic, the guessed segment', () => {
  const t = ['/topic/SN1/V0/post/json'];
  assert.equal(deviceTopics(t, 'SN10')[0], '/topic/SN10/V0/post/json');
  assert.equal(deviceTopics(t, 'S2')[0], '/topic/S2/V0/post/json');
  assert.equal(deviceTopics(t, 'X99')[0], '/topic/X99/V0/post/json');
  const u = ['/topic/A1B2C3D4/V0/post/json'];
  assert.equal(deviceTopics(u, 'Z9Y8X7W6')[0], '/topic/Z9Y8X7W6/V0/post/json');
  assert.equal(deviceTopics(u, 'SN9')[0], '/topic/SN9/V0/post/json');
  // Every candidate is offered, so the caller can pick the one that exists.
  assert.ok(deviceTopics(u, 'SN9').includes('/topic/A1B2C3D4/SN9/post/json'));
});

test('deviceTopics: full topic, empty input, nothing that fits', () => {
  assert.deepEqual(deviceTopics(['/topic/SN1/V0/post/json'], '/other/topic'), ['/other/topic']);
  assert.deepEqual(deviceTopics([], ' /other/topic '), ['/other/topic']);
  assert.deepEqual(deviceTopics(['/topic/SN1/V0/post/json'], '  '), []);
  assert.deepEqual(deviceTopics(['a/b'], 'xyz'), []);
  assert.deepEqual(deviceTopics([], 'SN1'), []);
});
