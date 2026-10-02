// Number formatting for the UI. No DOM in here, so it runs under `node --test`
// as well.

export function formatBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

// Rounds before splitting, so 119.6 s is "2 min 0 s" and not "1 min 60 s".
export function formatInterval(ms) {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 59_995) return `${(ms / 1000).toFixed(2)} s`;
  const s = Math.round(ms / 1000);
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  const min = Math.round(ms / 60_000);
  return `${Math.floor(min / 60)} h ${min % 60} min`;
}
