// CA certificate handling for the connection dialog. No DOM in here, so it
// runs under `node --test` as well.

const decoder = new TextDecoder();

// A PEM file is passed through. Anything else is taken for a binary DER
// certificate (.cer, .der) and wrapped as PEM, which is what the server
// stores; if it is not a certificate either, saving says so.
export function certFileToPEM(bytes) {
  if (bytes.length === 0) throw new Error('the file is empty');
  const text = decoder.decode(bytes);
  if (text.includes('-----BEGIN')) return text.trim();
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  const body = btoa(bin).replace(/.{1,64}/g, '$&\n');
  return `-----BEGIN CERTIFICATE-----\n${body}-----END CERTIFICATE-----`;
}

// One line per stored certificate, as the server describes them
// ({ subject, notAfter }), with expired ones flagged.
export function describeCerts(certs, now, formatDate) {
  return (certs ?? []).map((c) => {
    const end = new Date(c.notAfter);
    const expired = end.getTime() <= now;
    return {
      expired,
      text: expired ? `${c.subject} expired on ${formatDate(end)}` : `${c.subject}, valid until ${formatDate(end)}`,
    };
  });
}
