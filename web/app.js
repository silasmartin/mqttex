import { TopicTree } from './tree.js';
import { flatten, columnLabels, buildRows, substituteDevice } from './compare.js';

const ROW_H = 24;
const OVERSCAN = 6;
const ACTIVE_MS = 1500; // how long a row counts as "just received a message"
const HISTORY_MAX = 500;
const MAX_COMPARED = 32; // the server ignores ids beyond this

const $ = (id) => document.getElementById(id);

// All times are shown in German local time, whatever the machine is set to.
const TZ = 'Europe/Berlin';
const timeFmt = new Intl.DateTimeFormat('de-DE', {
  timeZone: TZ, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hour12: false,
});
const dateFmt = new Intl.DateTimeFormat('de-DE', { timeZone: TZ, dateStyle: 'medium' });
const compactFmt = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });
const fullFmt = new Intl.NumberFormat('en');

const compact = (n) => (n < 10_000 ? fullFmt.format(n) : compactFmt.format(n));

function formatBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

function formatInterval(ms) {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`;
  return `${Math.floor(ms / 3_600_000)} h ${Math.round((ms % 3_600_000) / 60_000)} min`;
}

// ---------------------------------------------------------------- state

const tree = new TopicTree();
const previews = new Map(); // topic id -> latest preview, only for rows that were on screen
let epoch = 0;
let ws = null;
let wsUp = false;
let status = { state: 'disconnected' };
let stats = { topics: 0, messages: 0, bytes: 0, dropped: 0 };
let rateSamples = [];
let profiles = [];

let cursor = null; // keyboard position in the tree
let selectedId = -1;
let history = []; // oldest first
let pinnedN = null; // history entry shown instead of the latest one
let shownN = null; // entry currently rendered in the value box

// Compared topics are kept by name: ids are only valid within one epoch.
const COMPARE_KEY = 'mqttex.compare';
let compareNames = loadCompare();
const nameToId = new Map();
const compareValues = new Map(); // topic id -> latest message
let compareIds = new Set(); // resolved ids of compareNames
let lastCompareKey = null; // ids last sent to the server
let compareDirty = true; // the table has to be rebuilt
let activeTab = 'topic';

// ---------------------------------------------------------------- toasts

function toast(kind, text) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.textContent = text;
  $('toasts').append(el);
  setTimeout(() => el.remove(), kind === 'error' ? 8000 : 3500);
}

// A destructive button asks for a second click instead of opening a dialog.
function confirmClick(button, action) {
  let armed = null;
  const label = button.textContent;
  const disarm = () => {
    clearTimeout(armed);
    armed = null;
    button.textContent = label;
    button.classList.remove('armed');
  };
  button.addEventListener('click', () => {
    if (armed) {
      disarm();
      action();
      return;
    }
    button.textContent = 'Click again to confirm';
    button.classList.add('armed');
    armed = setTimeout(disarm, 3000);
  });
  return disarm;
}

// ---------------------------------------------------------------- api

async function api(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: method === 'POST' ? { 'Content-Type': 'application/json' } : {},
      body: method === 'POST' ? JSON.stringify(body ?? {}) : undefined,
    });
  } catch {
    throw new Error('the mqttex server is not reachable, is it still running?');
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

// ---------------------------------------------------------------- websocket

function connectWS() {
  ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`);
  ws.binaryType = 'arraybuffer';
  ws.onopen = () => {
    wsUp = true;
    resetClient(); // the server starts every socket from zero
    renderTop();
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') onTick(JSON.parse(ev.data));
    else onCounts(new Uint32Array(ev.data));
  };
  ws.onclose = () => {
    wsUp = false;
    renderTop();
    setTimeout(connectWS, 1000);
  };
}

function send(msg) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function resetClient() {
  tree.reset();
  if ($('filter').value) tree.setFilter($('filter').value);
  previews.clear();
  rateSamples = [];
  cursor = null;
  lastWatchKey = '';
  nameToId.clear();
  compareValues.clear();
  compareIds = new Set();
  lastCompareKey = null;
  compareDirty = true;
  deselect();
  scheduleRender();
}

function onTick(t) {
  const fresh = t.reset || t.epoch !== epoch;
  if (fresh) {
    if (epoch !== 0) resetClient();
    epoch = t.epoch;
  }
  if (t.names) {
    tree.addTopics(t.first, t.names);
    for (let i = 0; i < t.names.length; i++) nameToId.set(t.names[i], t.first + i);
  }
  // A reset tick can still carry values for ids of the old epoch.
  if (t.compare && !fresh) {
    for (const m of t.compare) {
      if (!compareIds.has(m.id)) continue;
      compareValues.set(m.id, m);
      compareDirty = true;
    }
  }
  syncCompare();
  if (t.previews) for (const p of t.previews) previews.set(p.id, p);
  if (previews.size > 20_000) {
    previews.clear();
    lastWatchKey = ''; // forces a new watch message, which refills the visible rows
  }
  if (t.history && t.selected === selectedId) appendHistory(t.history);

  stats = t.stats;
  status = t.status;
  const now = performance.now();
  if (rateSamples.length && stats.messages < rateSamples.at(-1).m) rateSamples = [];
  rateSamples.push({ t: now, m: stats.messages });
  while (rateSamples.length > 2 && now - rateSamples[0].t > 2000) rateSamples.shift();

  renderTop();
  scheduleRender();
}

// Binary frame: uint32 [type, n, id, count, id, count, ...]
function onCounts(frame) {
  if (frame[0] !== 1) return;
  tree.applyCounts(frame.subarray(2, 2 + 2 * frame[1]), performance.now());
  scheduleRender();
}

// ---------------------------------------------------------------- top bar

function renderTop() {
  $('stat-topics').textContent = compact(stats.topics);
  $('stat-topics').title = fullFmt.format(stats.topics);
  $('stat-messages').textContent = compact(stats.messages);
  $('stat-messages').title = fullFmt.format(stats.messages);
  $('stat-bytes').textContent = formatBytes(stats.bytes);
  let rate = 0;
  if (rateSamples.length > 1) {
    const a = rateSamples[0];
    const b = rateSamples.at(-1);
    rate = ((b.m - a.m) * 1000) / (b.t - a.t);
  }
  $('stat-rate').textContent = compact(Math.round(rate));

  const state = $('state');
  state.dataset.state = status.state;
  state.textContent = status.profileName ? `${status.state} · ${status.profileName}` : status.state;
  state.title = status.clientId ? `client id ${status.clientId}` : '';

  const connected = status.state !== 'disconnected';
  $('connect').textContent = connected ? 'Disconnect' : 'Connect';
  $('connect').disabled = !connected && profiles.length === 0;
  $('edit').disabled = profiles.length === 0;

  const problems = [];
  if (!wsUp) problems.push('Lost the connection to the mqttex server, retrying every second.');
  if (status.error) problems.push(`Broker: ${status.error}`);
  for (const e of status.subErrors ?? []) problems.push(`Subscription ${e}`);
  if (stats.dropped > 0) {
    problems.push(`${fullFmt.format(stats.dropped)} messages on new topics were ignored because the -max-topics limit is reached.`);
  }
  $('banner').hidden = problems.length === 0;
  $('banner').replaceChildren(...problems.map((p) => Object.assign(document.createElement('p'), { textContent: p })));
}

// ---------------------------------------------------------------- tree (virtual list)

const treeEl = $('tree');
const spacer = $('tree-spacer');
const pool = [];
let renderQueued = false;
let lastWatchKey = '';

function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    renderTree();
    renderDetail();
    renderCompare();
  });
}

function makeRow() {
  const el = document.createElement('div');
  el.className = 'row';
  el.setAttribute('role', 'treeitem');
  const part = (cls) => {
    const s = document.createElement('span');
    s.className = cls;
    el.append(s);
    return s;
  };
  el._indent = part('indent');
  el._chev = part('chev');
  el._dot = part('dot');
  el._name = part('name');
  el._cmp = part('cmp-badge');
  el._cmp.textContent = 'cmp';
  el._cmp.title = 'In the comparison';
  el._val = part('val');
  el._meta = part('meta');
  spacer.append(el);
  return el;
}

function setText(span, text) {
  if (span._t !== text) {
    span._t = text;
    span.textContent = text;
  }
}

function fillRow(el, node, index, now) {
  el._node = node;
  el.hidden = false;
  el.style.transform = `translateY(${index * ROW_H}px)`;
  el._indent.style.width = `${node.depth * 14}px`;

  const folder = node.kids.length > 0;
  const open = folder && tree.isOpen(node);
  setText(el._chev, folder ? (open ? '▾' : '▸') : '');
  setText(el._name, node.name === '' ? '(empty)' : node.name);
  el._name.classList.toggle('muted', node.name === '');
  el._dot.classList.toggle('on', now - node.active < ACTIVE_MS && node.active > 0);
  el._cmp.hidden = !compareIds.has(node.id);

  let value = '';
  if (node.id >= 0) {
    const p = previews.get(node.id);
    if (p) value = p.s !== undefined ? p.s : `binary, ${formatBytes(p.size)}`;
  }
  setText(el._val, value);

  let meta;
  if (folder) {
    meta = `${compact(node.leaves)} ${node.leaves === 1 ? 'topic' : 'topics'} · ${compact(node.total)} msgs`;
  } else {
    meta = `× ${compact(node.count)}`;
  }
  setText(el._meta, meta);

  el.classList.toggle('selected', node.id >= 0 && node.id === selectedId);
  el.classList.toggle('cursor', node === cursor);
  if (folder) el.setAttribute('aria-expanded', String(open));
  else el.removeAttribute('aria-expanded');
}

function renderTree() {
  const rows = tree.rows();
  spacer.style.height = `${rows.length * ROW_H}px`;

  const top = treeEl.scrollTop;
  const first = Math.max(0, Math.floor(top / ROW_H) - OVERSCAN);
  const last = Math.min(rows.length, Math.ceil((top + treeEl.clientHeight) / ROW_H) + OVERSCAN);
  const need = Math.max(0, last - first);
  while (pool.length < need) pool.push(makeRow());

  const now = performance.now();
  const watch = [];
  for (let i = 0; i < pool.length; i++) {
    if (i >= need) {
      pool[i].hidden = true;
      pool[i]._node = null;
      continue;
    }
    const node = rows[first + i];
    fillRow(pool[i], node, first + i, now);
    if (node.id >= 0) watch.push(node.id);
  }

  // Tell the server which topics are on screen; it only sends values for those.
  const key = watch.join();
  if (key !== lastWatchKey && epoch !== 0) {
    lastWatchKey = key;
    send({ t: 'watch', epoch, ids: watch });
  }

  $('filter-count').textContent = tree.filtering ? `${fullFmt.format(tree.matches)} of ${fullFmt.format(tree.size)}` : '';
  const empty = $('tree-empty');
  empty.hidden = rows.length > 0;
  if (rows.length === 0) empty.textContent = emptyTreeText();
}

function emptyTreeText() {
  if (tree.size > 0) return 'No topic matches this filter.';
  if (status.state === 'connected') {
    return `Connected. Waiting for messages on ${(status.subscriptions ?? []).join(', ')}`;
  }
  if (status.state === 'connecting') return `Connecting to ${status.profileName} ...`;
  return profiles.length ? 'Pick a connection and press Connect.' : 'Create a connection with "New" to get started.';
}

treeEl.addEventListener('scroll', scheduleRender, { passive: true });
new ResizeObserver(scheduleRender).observe(treeEl);
setInterval(scheduleRender, 1000); // lets activity dots fade when traffic stops

treeEl.addEventListener('click', (ev) => {
  const el = ev.target.closest('.row');
  if (!el || !el._node) return;
  const node = el._node;
  cursor = node;
  if (node.id >= 0 && !ev.target.classList.contains('chev')) select(node.id);
  else tree.toggle(node);
  scheduleRender();
});

treeEl.addEventListener('keydown', (ev) => {
  if (ev.key === 'c' && !ev.metaKey && !ev.ctrlKey && !ev.altKey && !ev.target.matches('input, textarea, select')) {
    if (cursor && cursor.id >= 0 && !ev.repeat) {
      ev.preventDefault();
      toggleCompare(tree.names[cursor.id]);
    }
    return;
  }
  const rows = tree.rows();
  if (rows.length === 0) return;
  let i = cursor ? rows.indexOf(cursor) : -1;
  const open = cursor && cursor.kids.length > 0 && tree.isOpen(cursor);
  switch (ev.key) {
    case 'ArrowDown': i = Math.min(rows.length - 1, i + 1); break;
    case 'ArrowUp': i = Math.max(0, i - 1); break;
    case 'Home': i = 0; break;
    case 'End': i = rows.length - 1; break;
    case 'PageDown': i = Math.min(rows.length - 1, i + Math.floor(treeEl.clientHeight / ROW_H)); break;
    case 'PageUp': i = Math.max(0, i - Math.floor(treeEl.clientHeight / ROW_H)); break;
    case 'ArrowRight':
      if (!cursor) return;
      if (cursor.kids.length && !open) tree.toggle(cursor, true);
      else if (open) i++;
      break;
    case 'ArrowLeft':
      if (!cursor) return;
      if (open) tree.toggle(cursor, false);
      else if (cursor.parent !== tree.root) i = rows.indexOf(cursor.parent);
      break;
    case 'Enter':
    case ' ':
      if (!cursor) return;
      if (cursor.id >= 0) select(cursor.id);
      else tree.toggle(cursor);
      break;
    default:
      return;
  }
  ev.preventDefault();
  if (i >= 0 && i < rows.length) {
    cursor = rows[i];
    if (cursor.id >= 0) select(cursor.id);
    const y = i * ROW_H;
    if (y < treeEl.scrollTop) treeEl.scrollTop = y;
    else if (y + ROW_H > treeEl.scrollTop + treeEl.clientHeight) treeEl.scrollTop = y + ROW_H - treeEl.clientHeight;
  }
  scheduleRender();
});

// ---------------------------------------------------------------- filter

let filterTimer = null;
$('filter').addEventListener('input', () => {
  clearTimeout(filterTimer);
  filterTimer = setTimeout(() => {
    tree.setFilter($('filter').value);
    treeEl.scrollTop = 0;
    scheduleRender();
  }, 100);
});
$('filter').addEventListener('keydown', (ev) => {
  if (ev.key === 'ArrowDown') {
    ev.preventDefault();
    treeEl.focus();
  }
});
document.addEventListener('keydown', (ev) => {
  const typing = ev.target.matches('input, textarea, select');
  if (ev.key === '/' && !typing && !$('profile-dialog').open) {
    ev.preventDefault();
    $('filter').focus();
    $('filter').select();
  }
});

// ---------------------------------------------------------------- selected topic

function select(id) {
  if (id === selectedId) return;
  selectedId = id;
  history = [];
  pinnedN = null;
  shownN = null;
  $('d-history').replaceChildren();
  $('d-value').textContent = '';
  $('detail').hidden = false;
  $('detail-empty').hidden = true;
  $('d-topic').textContent = tree.names[id];
  $('p-topic').value = tree.names[id];
  updateCompareButton();
  send({ t: 'select', epoch, id });
}

function deselect() {
  if (selectedId >= 0) send({ t: 'select', epoch, id: -1 });
  selectedId = -1;
  history = [];
  pinnedN = null;
  shownN = null;
  $('detail').hidden = true;
  $('detail-empty').hidden = false;
}

function payloadText(m) {
  if (m.s !== undefined) return m.s;
  return `binary payload, base64:\n${m.b ?? ''}`;
}

function appendHistory(msgs) {
  const list = $('d-history');
  for (const m of msgs) {
    if (history.length && m.n <= history.at(-1).n) continue;
    const prev = history.at(-1);
    history.push(m);

    const li = document.createElement('li');
    li.dataset.n = m.n;
    li.tabIndex = 0;
    const cell = (cls, text) => {
      const s = document.createElement('span');
      s.className = cls;
      s.textContent = text;
      li.append(s);
    };
    cell('h-time', timeFmt.format(m.ts));
    cell('h-delta', prev ? `+${formatInterval(m.ts - prev.ts)}` : '');
    cell('h-size', formatBytes(m.size));
    cell('h-text', m.size === 0 ? '(empty payload)' : payloadText(m).slice(0, 200).replace(/\s+/g, ' '));
    list.prepend(li);
  }
  while (history.length > HISTORY_MAX) {
    history.shift();
    list.lastElementChild?.remove();
  }
  if (pinnedN !== null && !history.some((m) => m.n === pinnedN)) pinnedN = null;
}

function renderDetail() {
  if (selectedId < 0) return;
  const node = tree.byId[selectedId];
  if (!node) return;
  $('d-count').textContent = fullFmt.format(node.count);

  const latest = history.at(-1);
  if (!latest) return;
  const before = history.at(-2);
  $('d-last').textContent = timeFmt.format(latest.ts);
  $('d-last').title = dateFmt.format(latest.ts);
  $('d-interval').textContent = before ? formatInterval(latest.ts - before.ts) : 'waiting for the next message';

  const shown = pinnedN === null ? latest : history.find((m) => m.n === pinnedN) ?? latest;
  $('d-value-title').textContent = pinnedN === null ? 'Latest value' : `Message #${fullFmt.format(shown.n)} from ${timeFmt.format(shown.ts)}`;
  $('d-live').hidden = pinnedN === null;
  if (shown.n === shownN) return;

  const flash = shownN !== null && pinnedN === null;
  shownN = shown.n;
  $('d-size').textContent = formatBytes(shown.size);
  $('d-qos').textContent = String(shown.qos);
  $('d-retain').textContent = shown.retain ? 'yes' : 'no';

  let text = payloadText(shown);
  if (shown.s !== undefined && !shown.trunc && /^\s*[[{]/.test(text)) {
    try {
      text = JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      // not JSON after all, show it as received
    }
  }
  if (shown.size === 0) text = '(empty payload)';
  if (shown.trunc) text += `\n\n(cut off, the full payload is ${formatBytes(shown.size)})`;
  const value = $('d-value');
  value.textContent = text;
  if (flash) {
    value.classList.remove('flash');
    void value.offsetWidth; // restart the animation
    value.classList.add('flash');
  }

  const props = [];
  if (shown.contentType) props.push(`content-type: ${shown.contentType}`);
  for (const [k, v] of Object.entries(shown.userProps ?? {})) props.push(`${k}: ${v}`);
  $('d-props').hidden = props.length === 0;
  $('d-props').textContent = props.join('   ');

  for (const li of $('d-history').children) li.classList.toggle('pinned', Number(li.dataset.n) === pinnedN);
}

function pinFromEvent(ev) {
  const li = ev.target.closest('li');
  if (!li) return;
  pinnedN = Number(li.dataset.n);
  shownN = null;
  scheduleRender();
}
$('d-history').addEventListener('click', pinFromEvent);
$('d-history').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' || ev.key === ' ') {
    ev.preventDefault();
    pinFromEvent(ev);
  }
});
$('d-live').addEventListener('click', () => {
  pinnedN = null;
  shownN = null;
  scheduleRender();
});

async function copy(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    toast('ok', `Copied the ${what}.`);
  } catch {
    toast('error', `Could not copy the ${what}: the browser denied clipboard access.`);
  }
}
$('d-copy').addEventListener('click', () => copy($('d-topic').textContent, 'topic'));
$('d-compare').addEventListener('click', () => {
  if (selectedId >= 0) toggleCompare(tree.names[selectedId]);
});
$('d-copy-value').addEventListener('click', () => copy($('d-value').textContent, 'value'));

// ---------------------------------------------------------------- compare

function loadCompare() {
  try {
    const list = JSON.parse(localStorage.getItem(COMPARE_KEY) ?? '[]');
    if (Array.isArray(list)) return list.filter((n) => typeof n === 'string').slice(0, MAX_COMPARED);
  } catch {
    // private window, blocked storage or a broken entry: start empty
  }
  return [];
}

function saveCompare() {
  try {
    localStorage.setItem(COMPARE_KEY, JSON.stringify(compareNames));
  } catch {
    // only a convenience, like the last profile
  }
}

// Sends the resolved ids whenever they change: after edits, after a reset and
// when the names of stored topics arrive. Names the server has not delivered
// yet stay out until they show up in tick.names.
function syncCompare() {
  const ids = [];
  for (const name of compareNames) {
    const id = nameToId.get(name);
    if (id !== undefined && ids.length < MAX_COMPARED) ids.push(id);
  }
  const key = ids.join();
  if (key === lastCompareKey || epoch === 0) return;
  lastCompareKey = key;
  compareIds = new Set(ids);
  for (const id of compareValues.keys()) if (!compareIds.has(id)) compareValues.delete(id);
  compareDirty = true;
  send({ t: 'compare', epoch, ids });
}

function setCompare(names) {
  compareNames = names;
  saveCompare();
  compareDirty = true;
  syncCompare();
  updateCompareButton();
  scheduleRender();
}

function toggleCompare(name) {
  if (compareNames.includes(name)) {
    setCompare(compareNames.filter((n) => n !== name));
    return;
  }
  if (compareNames.length >= MAX_COMPARED) {
    toast('error', `Could not add ${name}: at most ${MAX_COMPARED} topics can be compared.`);
    return;
  }
  setCompare([...compareNames, name]);
}

function updateCompareButton() {
  if (selectedId < 0) return;
  const inList = compareNames.includes(tree.names[selectedId]);
  $('d-compare').textContent = inList ? '− Compare' : '+ Compare';
  $('d-compare').title = inList ? 'Remove this topic from the comparison ( c )' : 'Add this topic to the comparison ( c )';
}

function showTab(tab) {
  if (tab === activeTab) return;
  activeTab = tab;
  $('tab-topic').setAttribute('aria-selected', String(tab === 'topic'));
  $('tab-compare').setAttribute('aria-selected', String(tab === 'compare'));
  $('panel-topic').hidden = tab !== 'topic';
  $('panel-compare').hidden = tab !== 'compare';
  // Changes that happened while the table was hidden should not all flash.
  shownCells = new Map();
  compareDirty = true;
  scheduleRender();
}
$('tab-topic').addEventListener('click', () => showTab('topic'));
$('tab-compare').addEventListener('click', () => showTab('compare'));

const flatCache = new WeakMap(); // message -> fields, so a payload is parsed once
let shownCells = new Map(); // topic + field -> value last rendered, to flash changes
let ageCells = [];

function fieldsOf(m) {
  let f = flatCache.get(m);
  if (!f) {
    f = flatten(m.size === 0 ? '(empty payload)' : payloadText(m), m.trunc);
    flatCache.set(m, f);
  }
  return f;
}

function ageText(name, now) {
  const id = nameToId.get(name);
  if (id === undefined) return 'not seen yet';
  const m = compareValues.get(id);
  if (!m) return 'waiting for data';
  const ms = Math.max(0, now - m.ts);
  if (ms < 1000) return 'just now';
  if (ms < 60_000) return `${Math.floor(ms / 1000)} s ago`;
  return `${formatInterval(ms)} ago`;
}

function renderCompare() {
  setText($('tab-compare'), `Compare (${compareNames.length})`);
  if (activeTab !== 'compare') return;
  const empty = compareNames.length === 0;
  $('c-empty').hidden = !empty;
  $('c-wrap').hidden = empty;
  // Rebuilding the table on every tick would be wasteful: only new values or
  // a changed column list mark it dirty. The ages refresh every second.
  if (compareDirty) {
    compareDirty = false;
    buildCompareTable();
  }
  const now = Date.now();
  for (const { el, name } of ageCells) setText(el, ageText(name, now));
}

function buildCompareTable() {
  const labels = columnLabels(compareNames);
  const columns = compareNames.map((name) => {
    const id = nameToId.get(name);
    const m = id === undefined ? undefined : compareValues.get(id);
    return m ? fieldsOf(m) : null;
  });
  let rows = buildRows(columns);
  const onlyDiffs = $('c-diff').checked;
  if (onlyDiffs) rows = rows.filter((r) => r.differs);

  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };

  ageCells = [];
  const head = el('tr');
  head.append(el('th', '', 'Field'));
  compareNames.forEach((name, i) => {
    const th = el('th');
    th.title = name;
    const top = el('div', 'c-col');
    const remove = el('button', 'c-remove', '×');
    remove.type = 'button';
    remove.dataset.name = name;
    remove.title = 'Remove from the comparison';
    remove.setAttribute('aria-label', `Remove ${name} from the comparison`);
    top.append(el('span', 'c-label', labels[i]), remove);
    const age = el('span', 'c-age');
    ageCells.push({ el: age, name });
    th.append(top, age);
    head.append(th);
  });

  const next = new Map();
  columns.forEach((col, i) => {
    if (col) for (const [path, v] of col) next.set(`${compareNames[i]}\n${path}`, v);
  });

  const body = [];
  for (const row of rows) {
    const tr = el('tr', row.differs ? 'differs' : '');
    const th = el('th', '', row.path);
    th.scope = 'row';
    tr.append(th);
    row.values.forEach((v, i) => {
      if (v === undefined) {
        tr.append(el('td', 'missing', '–'));
        return;
      }
      const prev = shownCells.get(`${compareNames[i]}\n${row.path}`);
      tr.append(el('td', prev !== undefined && prev !== v ? 'flash' : '', v));
    });
    body.push(tr);
  }
  if (body.length === 0) {
    const td = el('td', 'missing', columns.some(Boolean) && onlyDiffs ? 'No differences.' : 'Waiting for messages on these topics.');
    td.colSpan = compareNames.length + 1;
    const tr = el('tr');
    tr.append(td);
    body.push(tr);
  }
  shownCells = next;

  const table = $('c-table');
  table.tHead.replaceChildren(head);
  table.tBodies[0].replaceChildren(...body);
}

$('c-table').addEventListener('click', (ev) => {
  const btn = ev.target.closest('.c-remove');
  if (btn) setCompare(compareNames.filter((n) => n !== btn.dataset.name));
});

$('c-diff').addEventListener('change', () => {
  compareDirty = true;
  scheduleRender();
});

confirmClick($('c-clear'), () => setCompare([]));

$('c-add').addEventListener('submit', (ev) => {
  ev.preventDefault();
  const value = $('c-device').value.trim();
  if (!value) return;
  let name = value.includes('/') ? value : null;
  if (!name) {
    if (compareNames.length === 0) {
      toast('error', 'Could not add the device: add a first topic with + Compare or c in the tree, or enter a full topic.');
      return;
    }
    name = substituteDevice(compareNames[0], columnLabels(compareNames)[0], value);
    if (!name) {
      toast('error', `Could not tell which part of ${compareNames[0]} is the device. Enter the full topic instead.`);
      return;
    }
  }
  if (compareNames.includes(name)) {
    toast('error', `${name} is already in the comparison.`);
    return;
  }
  if (!nameToId.has(name)) {
    toast('error', `Could not add ${name}: no message was received on this topic so far.`);
    return;
  }
  if (compareNames.length >= MAX_COMPARED) {
    toast('error', `Could not add ${name}: at most ${MAX_COMPARED} topics can be compared.`);
    return;
  }
  $('c-device').value = '';
  setCompare([...compareNames, name]);
});

// ---------------------------------------------------------------- publish

async function publish(body, done) {
  try {
    await api('POST', '/api/publish', body);
    toast('ok', done);
  } catch (err) {
    toast('error', `Could not publish to ${body.topic || 'the topic'}: ${err.message}`);
  }
}

$('publish').addEventListener('submit', (ev) => {
  ev.preventDefault();
  const topic = $('p-topic').value.trim();
  publish(
    { topic, payload: $('p-payload').value, qos: Number($('p-qos').value), retain: $('p-retain').checked },
    `Published to ${topic}.`,
  );
});

confirmClick($('p-clear'), () => {
  const topic = $('p-topic').value.trim();
  if (!topic) {
    toast('error', 'Could not clear the retained message: enter a topic first.');
    return;
  }
  publish({ topic, payload: '', qos: 0, retain: true }, `Cleared the retained message on ${topic}.`);
});

// ---------------------------------------------------------------- connection + profiles

const LAST_PROFILE = 'mqttex.lastProfile';

function remember(id) {
  try {
    localStorage.setItem(LAST_PROFILE, id);
  } catch {
    // private window or blocked storage: only a convenience
  }
}

async function loadProfiles(selectId) {
  try {
    profiles = await api('GET', '/api/profiles');
  } catch (err) {
    toast('error', `Could not load the saved connections: ${err.message}`);
    return;
  }
  let wanted = selectId ?? $('profile').value;
  if (!wanted) {
    try {
      wanted = localStorage.getItem(LAST_PROFILE);
    } catch {
      wanted = null;
    }
  }
  $('profile').replaceChildren(
    ...profiles.map((p) => Object.assign(document.createElement('option'), { value: p.id, textContent: `${p.name}  (${p.protocol}://${p.host}:${p.port})` })),
  );
  if (profiles.some((p) => p.id === wanted)) $('profile').value = wanted;
  renderTop();
  scheduleRender();
}

$('connect').addEventListener('click', async () => {
  if (status.state !== 'disconnected') {
    try {
      status = await api('POST', '/api/disconnect');
    } catch (err) {
      toast('error', `Could not disconnect: ${err.message}`);
    }
    renderTop();
    return;
  }
  const id = $('profile').value;
  const name = profiles.find((p) => p.id === id)?.name ?? 'the broker';
  try {
    status = await api('POST', '/api/connect', { id });
    remember(id);
  } catch (err) {
    toast('error', `Could not start connecting to ${name}: ${err.message}`);
  }
  renderTop();
});

$('clear').addEventListener('click', async () => {
  try {
    await api('POST', '/api/clear');
  } catch (err) {
    toast('error', `Could not clear the topic tree: ${err.message}`);
  }
});

const dialog = $('profile-dialog');
let editingId = '';

function syncProtocolFields() {
  const proto = $('pf-protocol').value;
  const isWS = proto === 'ws' || proto === 'wss';
  const isTLS = proto === 'mqtts' || proto === 'wss';
  for (const el of dialog.querySelectorAll('[data-ws]')) el.hidden = !isWS;
  for (const el of dialog.querySelectorAll('[data-tls]')) el.hidden = !isTLS;
  const port = $('pf-port').value;
  if (proto === 'mqtts' && port === '1883') $('pf-port').value = '8883';
  if (proto === 'mqtt' && port === '8883') $('pf-port').value = '1883';
}

function checkSubscriptions() {
  const star = $('pf-subs').value.split('\n').some((l) => l.includes('*'));
  $('pf-subs-warning').hidden = !star;
  $('pf-subs-warning').textContent = star
    ? '"*" is not an MQTT wildcard and only matches a topic literally named "*". Use # or + instead.'
    : '';
}

let disarmDelete = () => {};

function openDialog(p) {
  editingId = p?.id ?? '';
  $('pf-title').textContent = p ? `Edit ${p.name}` : 'New connection';
  $('pf-name').value = p?.name ?? '';
  $('pf-protocol').value = p?.protocol ?? 'mqtt';
  $('pf-host').value = p?.host ?? '';
  $('pf-port').value = p?.port ?? 1883;
  $('pf-path').value = p?.path ?? '';
  $('pf-username').value = p?.username ?? '';
  $('pf-password').value = '';
  $('pf-password').placeholder = p?.hasPassword ? 'unchanged' : '';
  $('pf-clientid').value = p?.clientId ?? '';
  $('pf-insecure').checked = p?.tlsInsecure ?? false;
  $('pf-subs').value = (p?.subscriptions ?? ['#']).join('\n');
  $('pf-delete').hidden = !p;
  $('pf-error').hidden = true;
  disarmDelete();
  syncProtocolFields();
  checkSubscriptions();
  dialog.showModal();
}

$('pf-protocol').addEventListener('change', syncProtocolFields);
$('pf-subs').addEventListener('input', checkSubscriptions);
$('new').addEventListener('click', () => openDialog(null));
$('edit').addEventListener('click', () => openDialog(profiles.find((p) => p.id === $('profile').value) ?? null));
$('pf-cancel').addEventListener('click', () => dialog.close());

$('profile-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const username = $('pf-username').value.trim();
  const body = {
    id: editingId,
    name: $('pf-name').value,
    protocol: $('pf-protocol').value,
    host: $('pf-host').value,
    port: Number($('pf-port').value),
    path: $('pf-path').value.trim(),
    username,
    password: $('pf-password').value,
    clearPassword: username === '',
    clientId: $('pf-clientid').value.trim(),
    tlsInsecure: $('pf-insecure').checked,
    subscriptions: $('pf-subs').value.split('\n'),
  };
  try {
    const saved = await api('POST', '/api/profiles', body);
    dialog.close();
    await loadProfiles(saved.id);
    const active = status.profileId === saved.id && status.state !== 'disconnected';
    toast('ok', active ? `Saved ${saved.name}. Reconnect to apply the changes.` : `Saved ${saved.name}.`);
  } catch (err) {
    $('pf-error').hidden = false;
    $('pf-error').textContent = `Could not save the connection: ${err.message}`;
  }
});

disarmDelete = confirmClick($('pf-delete'), async () => {
  try {
    await api('DELETE', `/api/profiles/${encodeURIComponent(editingId)}`);
    dialog.close();
    await loadProfiles();
    toast('ok', 'Deleted the connection.');
  } catch (err) {
    $('pf-error').hidden = false;
    $('pf-error').textContent = `Could not delete the connection: ${err.message}`;
  }
});

// ---------------------------------------------------------------- start

loadProfiles();
connectWS();
renderTop();
scheduleRender();
