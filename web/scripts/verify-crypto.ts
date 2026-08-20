/**
 * Verifies the TypeScript crypto against the committed vectors.
 *
 * The Go and TypeScript implementations of the handshake and the envelope must
 * agree byte for byte. When they do not, pairing fails on a real phone with an
 * authentication error and no indication of which side is wrong. Both sides
 * check the same committed vectors, so a drift fails a build instead.
 *
 * Run with `make test-crypto`. Regenerate the vectors, deliberately, with
 * `go run ./test/tools/genvectors`.
 */

import { readFileSync } from 'node:fs';
import { derive } from '../src/crypto/handshake.js';
import { Envelope, fromBase64, toBase64 } from '../src/crypto/envelope.js';

interface HandshakeVector {
  name: string;
  client_priv: string;
  client_pub: string;
  server_pub: string;
  salt: string;
  code: string;
  handshake_id: string;
  key: string;
  proof: string;
}

interface EnvelopeVector {
  name: string;
  key: string;
  direction: number;
  method: string;
  path: string;
  plaintext: string;
  sealed: string;
}

const vectors = JSON.parse(
  readFileSync(new URL('../../test/testdata/crypto-vectors.json', import.meta.url), 'utf8'),
) as { handshakes: HandshakeVector[]; envelopes: EnvelopeVector[] };

let failures = 0;

function check(name: string, got: string, want: string): void {
  if (got === want) {
    console.log(`  ok   ${name}`);
    return;
  }
  failures += 1;
  console.error(`  FAIL ${name}\n       got  ${got}\n       want ${want}`);
}

console.log('handshake vectors');
for (const v of vectors.handshakes) {
  const { key, proof } = derive(
    fromBase64(v.client_priv),
    fromBase64(v.server_pub),
    fromBase64(v.client_pub),
    fromBase64(v.salt),
    v.code,
    v.handshake_id,
  );
  check(`${v.name}: session key`, toBase64(key), v.key);
  check(`${v.name}: confirmation proof`, toBase64(proof), v.proof);
}

console.log('envelope vectors');
for (const v of vectors.envelopes) {
  // Sealing is deterministic here because the counter starts at zero and this
  // is the first message, which is exactly how the Go side produced it.
  const sealer = new Envelope(fromBase64(v.key), v.direction);
  const sealed = sealer.seal(v.method, v.path, fromBase64(v.plaintext));
  check(`${v.name}: sealed bytes`, toBase64(sealed), v.sealed);

  // And the Go-produced bytes must open here.
  const opener = new Envelope(fromBase64(v.key), v.direction === 0 ? 1 : 0);
  const opened = opener.open(v.method, v.path, fromBase64(v.sealed));
  check(`${v.name}: opens Go output`, toBase64(opened), v.plaintext);
}

if (failures > 0) {
  console.error(`\n${failures} vector(s) disagree between Go and TypeScript.`);
  process.exit(1);
}
console.log(`\nall ${vectors.handshakes.length * 2 + vectors.envelopes.length * 2} vectors agree`);
