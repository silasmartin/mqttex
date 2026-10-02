import test from 'node:test';
import assert from 'node:assert/strict';
import { TopicTree } from './tree.js';

const labels = (tree) => tree.rows().map((n) => `${'  '.repeat(n.depth)}${n.name || '(empty)'}`);

test('builds a sorted tree; leading slash becomes an empty first level', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/topic/SN2', '/topic/SN1', 'other/x/deep']);
  // The first two levels start expanded.
  assert.deepEqual(labels(tree), ['(empty)', '  topic', '    SN1', '    SN2', 'other', '  x', '    deep']);

  const topic = tree.rows()[1];
  tree.toggle(topic);
  assert.deepEqual(labels(tree), ['(empty)', '  topic', 'other', '  x', '    deep']);
  assert.equal(topic.leaves, 2);
  assert.equal(tree.path(tree.byId[0]), '/topic/SN2');
  assert.equal(tree.size, 3);
});

test('a topic can also be a folder', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a', 'a/b']);
  const a = tree.rows()[0];
  assert.equal(a.id, 0);
  assert.equal(a.leaves, 2);
  assert.deepEqual(labels(tree), ['a', '  b']);
});

test('idOf finds topics by name, folders and unknown names give -1', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/topic/SN1', 'a', 'a/b', 'x//y']);
  assert.equal(tree.idOf('/topic/SN1'), 0);
  assert.equal(tree.idOf('a'), 1);
  assert.equal(tree.idOf('a/b'), 2);
  assert.equal(tree.idOf('x//y'), 3);
  assert.equal(tree.idOf('/topic'), -1); // a folder only
  assert.equal(tree.idOf('/topic/SN2'), -1);
  assert.equal(tree.idOf('x/y'), -1);
});

test('counts propagate as deltas to every ancestor', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/topic/SN1', '/topic/SN2']);
  assert.equal(tree.applyCounts(Uint32Array.of(0, 5, 1, 2), 1000), 2);
  const topic = tree.byId[0].parent;
  assert.equal(topic.total, 7);
  assert.equal(topic.parent.total, 7);
  assert.equal(topic.active, 1000);

  // Absolute counts: repeating one is a no-op, a higher one adds the difference.
  assert.equal(tree.applyCounts(Uint32Array.of(0, 5), 2000), 0);
  assert.equal(topic.active, 1000);
  tree.applyCounts(Uint32Array.of(0, 9, 77, 3), 3000);
  assert.equal(tree.byId[0].count, 9);
  assert.equal(topic.total, 11);
  assert.equal(tree.byId[1].active, 1000);
});

test('ids that were already delivered are not added twice', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a/b']);
  tree.addTopics(0, ['a/b', 'a/c']);
  assert.equal(tree.rows()[0].leaves, 2);
});

test('filter keeps matching topics with their ancestors and opens them', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/topic/SN100/status', '/topic/SN100/power', '/topic/SN200/status', 'sys/load']);
  tree.setFilter('sn100 STATUS');
  assert.equal(tree.matches, 1);
  assert.deepEqual(labels(tree), ['(empty)', '  topic', '    SN100', '      status']);

  // Topics arriving while the filter is active are matched as well.
  tree.addTopics(4, ['/topic/SN1001/status', '/topic/SN300/status']);
  assert.equal(tree.matches, 2);
  assert.deepEqual(labels(tree), ['(empty)', '  topic', '    SN100', '      status', '    SN1001', '      status']);

  // Collapsing under a filter does not touch the normal expansion state.
  const sn100 = tree.rows()[2];
  tree.toggle(sn100);
  assert.deepEqual(labels(tree), ['(empty)', '  topic', '    SN100', '    SN1001', '      status']);
  tree.setFilter('');
  assert.equal(tree.filtering, false);
  assert.deepEqual(labels(tree), ['(empty)', '  topic', '    SN100', '    SN1001', '    SN200', '    SN300', 'sys', '  load']);
});

test('reveal opens the ancestors of a node', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a/b/c/d']);
  assert.deepEqual(labels(tree), ['a', '  b', '    c']);
  tree.reveal(tree.byId[0]);
  assert.deepEqual(labels(tree), ['a', '  b', '    c', '      d']);
});

test('reset forgets everything', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a']);
  tree.setFilter('a');
  tree.reset();
  assert.equal(tree.size, 0);
  assert.equal(tree.filtering, false);
  assert.deepEqual(tree.rows(), []);
});

const filtered = (tree, text) => {
  tree.setFilter(text);
  return tree.matchingIds().map((id) => tree.names[id]);
};

test('commas list alternatives (OR)', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/t/SN1/post/json', '/t/SN2/post/json', '/t/SN3/post/json', '/t/SN4/post/json']);
  assert.deepEqual(filtered(tree, 'SN1,SN3'), ['/t/SN1/post/json', '/t/SN3/post/json']);
  assert.equal(tree.matches, 2);
  assert.equal(tree.filtering, true);
});

test('alternatives combine with further terms (AND)', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['/t/SN1/post/json', '/t/SN1/get/json', '/t/SN2/post/json', '/t/SN2/get/json', '/t/SN3/post/json']);
  assert.deepEqual(filtered(tree, 'SN1, SN2, SN3 post/json'), ['/t/SN1/post/json', '/t/SN2/post/json', '/t/SN3/post/json']);
  assert.deepEqual(filtered(tree, 'post/json sn2,sn9'), ['/t/SN2/post/json']);
});

test('spaces around commas do not split terms', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a/SN1', 'a/SN2', 'a/SN3']);
  assert.deepEqual(filtered(tree, 'sn1 ,sn2'), ['a/SN1', 'a/SN2']);
  assert.deepEqual(filtered(tree, 'sn1 ,   sn3'), ['a/SN1', 'a/SN3']);
});

test('empty alternatives are dropped; only commas means no filter', () => {
  const tree = new TopicTree();
  tree.addTopics(0, ['a/SN1', 'a/SN2']);
  assert.deepEqual(filtered(tree, ',sn1,,'), ['a/SN1']);
  assert.deepEqual(filtered(tree, 'sn2 , ,'), ['a/SN2']);
  for (const text of [',', ' , ,, ', '']) {
    tree.setFilter(text);
    assert.equal(tree.filtering, false, JSON.stringify(text));
    assert.deepEqual(tree.matchingIds(), []);
  }
});

test('matchingIds follows tree order, skips folders and ignores collapsed nodes', () => {
  const tree = new TopicTree();
  // ids are deliberately not in tree order
  tree.addTopics(0, ['z/SN2', 'a/b/c/SN1', 'a/SN3', 'a']);
  tree.setFilter('sn,a');
  tree.toggle(tree.byId[3], false);
  assert.deepEqual(
    tree.matchingIds().map((id) => tree.names[id]),
    ['a', 'a/SN3', 'a/b/c/SN1', 'z/SN2'],
  );
  assert.deepEqual(filtered(tree, 'sn'), ['a/SN3', 'a/b/c/SN1', 'z/SN2']);
  assert.deepEqual(filtered(tree, 'nothing'), []);
});

test('40 000 topics: build, count, filter and flatten stay fast', () => {
  const tree = new TopicTree();
  const names = Array.from({ length: 40_000 }, (_, i) => `/topic/SN${String(i).padStart(8, '0')}`);
  const pairs = new Uint32Array(80_000);
  for (let i = 0; i < 40_000; i++) {
    pairs[2 * i] = i;
    pairs[2 * i + 1] = 1;
  }
  const start = performance.now();
  tree.addTopics(0, names);
  tree.applyCounts(pairs, 1);
  assert.equal(tree.rows().length, 40_002);
  tree.setFilter('sn0000123');
  assert.equal(tree.matches, 10);
  assert.equal(tree.rows().length, 12);
  tree.setFilter('sn0000123, sn0000456 , sn0003999 topic');
  assert.equal(tree.matches, 30);
  assert.equal(tree.matchingIds().length, 30);
  const elapsed = performance.now() - start;
  assert.ok(elapsed < 1000, `took ${elapsed.toFixed(0)} ms`);
});
