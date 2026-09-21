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
  const elapsed = performance.now() - start;
  assert.ok(elapsed < 1000, `took ${elapsed.toFixed(0)} ms`);
});
