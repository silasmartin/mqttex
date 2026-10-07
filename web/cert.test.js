import test from 'node:test';
import assert from 'node:assert/strict';
import { certFileToPEM, describeCerts } from './cert.js';

const bytes = (s) => new TextEncoder().encode(s);

test('certFileToPEM: PEM files are kept, only trimmed', () => {
  const pem = '-----BEGIN CERTIFICATE-----\r\nMIIB\r\n-----END CERTIFICATE-----\r\n';
  assert.equal(certFileToPEM(bytes(`\n${pem}\n`)), pem.trim());
  const bundle = `subject=CN = Root\n${pem}${pem}`;
  assert.equal(certFileToPEM(bytes(bundle)), bundle.trim());
});

test('certFileToPEM: UTF-16 PEM files with a byte order mark are read as text', () => {
  const pem = '-----BEGIN CERTIFICATE-----\r\nMIIB\r\n-----END CERTIFICATE-----\r\n';
  const le = new Uint8Array(2 + 2 * pem.length);
  const be = new Uint8Array(2 + 2 * pem.length);
  le.set([0xff, 0xfe]);
  be.set([0xfe, 0xff]);
  for (let i = 0; i < pem.length; i++) {
    le[2 + 2 * i] = pem.charCodeAt(i);
    be[3 + 2 * i] = pem.charCodeAt(i);
  }
  assert.equal(certFileToPEM(le), pem.trim());
  assert.equal(certFileToPEM(be), pem.trim());
});

test('certFileToPEM: DER is wrapped in 64-character lines', () => {
  const der = new Uint8Array(100).map((_, i) => (i * 37 + 0x30) & 0xff); // starts like ASN.1, not text
  const pem = certFileToPEM(der);
  const lines = pem.split('\n');
  assert.equal(lines[0], '-----BEGIN CERTIFICATE-----');
  assert.equal(lines.at(-1), '-----END CERTIFICATE-----');
  const body = lines.slice(1, -1);
  assert.deepEqual(body.map((l) => l.length), [64, 64, 8]);
  assert.deepEqual(Uint8Array.from(atob(body.join('')), (c) => c.charCodeAt(0)), der);
});

test('certFileToPEM: an empty file is refused', () => {
  assert.throws(() => certFileToPEM(new Uint8Array(0)), /empty/);
});

test('describeCerts: validity and expiry', () => {
  const now = Date.parse('2026-10-07T12:00:00Z');
  const day = (d) => d.toISOString().slice(0, 10);
  const lines = describeCerts([
    { subject: 'Root CA', notAfter: '2030-01-02T03:04:05Z' },
    { subject: 'Old CA', notAfter: '2026-10-01T00:00:00Z' },
  ], now, day);
  assert.deepEqual(lines, [
    { expired: false, text: 'Root CA, valid until 2030-01-02' },
    { expired: true, text: 'Old CA expired on 2026-10-01' },
  ]);
  assert.deepEqual(describeCerts(undefined, now, day), []);
});
