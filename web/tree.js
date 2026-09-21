// Topic tree model. No DOM in here, so it runs under `node --test` as well.
//
// The server sends every topic name once and afterwards only (id, count)
// pairs. The tree keeps subtree totals up to date by pushing count deltas up
// the parent chain, and produces the flat list of visible rows that the
// virtual list renders.

const AUTO_EXPAND_DEPTH = 2; // levels that start out expanded

function makeNode(name, parent) {
  return {
    name,
    parent,
    depth: parent ? parent.depth + 1 : -1,
    kids: [],
    map: null, // child name -> node, created with the first child
    sorted: true,
    id: -1, // topic id, -1 for pure folders
    count: 0, // messages on this exact topic
    total: 0, // messages in this subtree, own included
    leaves: 0, // topics in this subtree, own included
    active: 0, // time of the last message in this subtree
    expanded: false,
    fcollapsed: false, // collapsed by the user while a filter is active
    mark: 0, // filter generation this node matched in
  };
}

export class TopicTree {
  constructor() {
    this.reset();
  }

  reset() {
    this.root = makeNode('', null);
    this.root.expanded = true;
    this.byId = [];
    this.names = [];
    this.lower = [];
    this.terms = [];
    this.gen = 0;
    this.matches = 0;
    this._rows = null;
  }

  get size() {
    return this.byId.length;
  }

  get filtering() {
    return this.terms.length > 0;
  }

  addTopics(firstId, names) {
    for (let i = 0; i < names.length; i++) {
      const id = firstId + i;
      if (this.byId[id]) continue;
      const name = names[i];
      let node = this.root;
      for (const part of name.split('/')) {
        let child = node.map && node.map.get(part);
        if (!child) {
          child = makeNode(part, node);
          child.expanded = child.depth < AUTO_EXPAND_DEPTH;
          (node.map ??= new Map()).set(part, child);
          node.kids.push(child);
          node.sorted = false;
        }
        node = child;
      }
      node.id = id;
      this.byId[id] = node;
      this.names[id] = name;
      this.lower[id] = name.toLowerCase();
      for (let n = node; n; n = n.parent) n.leaves++;
      if (this.filtering && this._matches(id)) this._mark(node);
    }
    this._rows = null;
  }

  // pairs is a flat Uint32Array of (id, count). Returns the number of topics updated.
  applyCounts(pairs, now) {
    let updated = 0;
    for (let i = 0; i + 1 < pairs.length; i += 2) {
      const node = this.byId[pairs[i]];
      if (!node) continue;
      const delta = pairs[i + 1] - node.count;
      if (delta === 0) continue;
      node.count = pairs[i + 1];
      for (let n = node; n; n = n.parent) {
        n.total += delta;
        n.active = now;
      }
      updated++;
    }
    return updated;
  }

  // Whitespace separates terms; a topic must contain all of them.
  setFilter(text) {
    this.terms = text.toLowerCase().split(/\s+/).filter(Boolean);
    this.gen++;
    this.matches = 0;
    if (this.filtering) {
      for (let id = 0; id < this.byId.length; id++) {
        if (this.byId[id] && this._matches(id)) this._mark(this.byId[id]);
      }
    }
    this._rows = null;
  }

  _matches(id) {
    const name = this.lower[id];
    for (const term of this.terms) {
      if (!name.includes(term)) return false;
    }
    return true;
  }

  _mark(node) {
    this.matches++;
    for (let n = node; n && n.mark !== this.gen; n = n.parent) {
      n.mark = this.gen;
      n.fcollapsed = false;
    }
  }

  isOpen(node) {
    return this.filtering ? !node.fcollapsed : node.expanded;
  }

  toggle(node, open = !this.isOpen(node)) {
    if (node.kids.length === 0) return;
    if (this.filtering) node.fcollapsed = !open;
    else node.expanded = open;
    this._rows = null;
  }

  // Opens every ancestor so the node is part of rows().
  reveal(node) {
    for (let n = node.parent; n && n !== this.root; n = n.parent) this.toggle(n, true);
  }

  path(node) {
    const parts = [];
    for (let n = node; n && n !== this.root; n = n.parent) parts.push(n.name);
    return parts.reverse().join('/');
  }

  // Flat list of the currently visible nodes, top to bottom. Cached until the
  // structure, the filter or an expansion state changes.
  rows() {
    if (this._rows) return this._rows;
    const rows = [];
    const stack = [];
    const pushKids = (node) => {
      if (!node.sorted) {
        node.kids.sort(byName);
        node.sorted = true;
      }
      for (let i = node.kids.length - 1; i >= 0; i--) stack.push(node.kids[i]);
    };
    pushKids(this.root);
    while (stack.length) {
      const node = stack.pop();
      if (this.filtering && node.mark !== this.gen) continue;
      rows.push(node);
      if (node.kids.length && this.isOpen(node)) pushKids(node);
    }
    return (this._rows = rows);
  }
}

function byName(a, b) {
  return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
}
