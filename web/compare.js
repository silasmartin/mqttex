// Field table for comparing several topics side by side. No DOM in here, so it
// runs under `node --test` as well.

const PAYLOAD = '(payload)';
const TRUNCATED = '(truncated)';

// Turns a payload into field path -> text. Objects become a.b.c, arrays a[0].
// Anything that is not a JSON object or array is one field, "(payload)".
export function flatten(text, trunc = false) {
  const out = new Map();
  let root;
  // A cut-off payload never parses, and a half document would be misleading.
  if (!trunc && /^\s*[[{]/.test(text)) {
    try {
      root = JSON.parse(text);
    } catch {
      root = undefined;
    }
  }
  if (root === undefined || root === null || typeof root !== 'object') {
    out.set(PAYLOAD, text);
  } else {
    walk(root, '', out);
  }
  if (trunc) out.set(TRUNCATED, 'yes');
  return out;
}

function walk(v, path, out) {
  if (v !== null && typeof v === 'object') {
    const entries = Array.isArray(v) ? v.map((x, i) => [`${path}[${i}]`, x]) : Object.keys(v).map((k) => [path ? `${path}.${k}` : k, v[k]]);
    if (entries.length === 0) {
      out.set(path || PAYLOAD, Array.isArray(v) ? '[]' : '{}');
      return;
    }
    for (const [p, x] of entries) walk(x, p, out);
    return;
  }
  out.set(path, typeof v === 'string' ? v : String(v));
}

// Short column titles: the topic segments all names share at the start and at
// the end are dropped, so /topic/SN1/V0/post/json becomes SN1.
export function columnLabels(names) {
  if (names.length < 2) return [...names];
  const parts = names.map((n) => n.split('/'));
  const min = Math.min(...parts.map((p) => p.length));
  let pre = 0;
  while (pre < min && parts.every((p) => p[pre] === parts[0][pre])) pre++;
  let suf = 0;
  while (pre + suf < min && parts.every((p) => p[p.length - 1 - suf] === parts[0][parts[0].length - 1 - suf])) suf++;
  return parts.map((p, i) => {
    const label = p.slice(pre, p.length - suf).join('/');
    return label === '' ? names[i] : label;
  });
}

// columns: one Map per topic (from flatten), or null while a topic has no
// message yet. Rows keep the order in which their path first shows up.
export function buildRows(columns) {
  const order = [];
  const seen = new Set();
  for (const col of columns) {
    if (!col) continue;
    for (const path of col.keys()) {
      if (!seen.has(path)) {
        seen.add(path);
        order.push(path);
      }
    }
  }
  const present = columns.filter(Boolean).length;
  return order.map((path) => {
    const values = columns.map((col) => (col ? col.get(path) : undefined));
    // Topics without any message are left out: they would mark every row.
    let differs = false;
    if (present > 1) {
      let first;
      let have = false;
      for (let i = 0; i < columns.length; i++) {
        if (!columns[i]) continue;
        if (!have) {
          first = values[i];
          have = true;
        } else if (values[i] !== first) {
          differs = true;
          break;
        }
      }
    }
    return { path, values, differs };
  });
}

// Builds the topic of another device from an existing one: the label segment
// (see columnLabels) is replaced by newValue. A value containing "/" is taken
// as a complete topic. With a single column the label is the whole topic, so
// the segment is guessed: same length as newValue first, then the longest
// segment that contains a digit. Returns null when nothing fits.
export function substituteDevice(templateName, labelSegment, newValue) {
  const value = newValue.trim();
  if (value === '') return null;
  if (value.includes('/')) return value;
  const parts = templateName.split('/');

  if (labelSegment && labelSegment !== templateName) {
    const label = labelSegment.split('/');
    for (let i = 0; i + label.length <= parts.length; i++) {
      if (label.every((s, j) => parts[i + j] === s)) {
        return [...parts.slice(0, i), value, ...parts.slice(i + label.length)].join('/');
      }
    }
  }

  let best = -1;
  let bestScore = 0;
  parts.forEach((s, i) => {
    if (s === '') return;
    const digit = /\d/.test(s);
    if (s.length !== value.length && !digit) return;
    const score = (s.length === value.length && digit ? 2000 : s.length === value.length ? 1000 : 0) + s.length;
    if (score > bestScore) {
      best = i;
      bestScore = score;
    }
  });
  if (best < 0) return null;
  parts[best] = value;
  return parts.join('/');
}
