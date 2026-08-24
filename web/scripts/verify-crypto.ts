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
import { blake2b } from '@noble/hashes/blake2';
import {
  calculateGenerator,
  complete,
  generatorString,
  isk,
  message,
  scalarMultVfy,
  secretFrom,
} from '../src/crypto/cpace.js';
import { Envelope, fromBase64, toBase64 } from '../src/crypto/envelope.js';

interface HandshakeVector {
  name: string;
  code: string;
  host_id: string;
  sid: string;
  client_secret: string;
  server_secret: string;
  handshake_id: string;
  client_message: string;
  server_message: string;
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

/**
 * The integrity digest, fed in chunks.
 *
 * The phone hashes while reading and the computer hashes while writing, and a
 * mismatch fails the transfer (FR-032). Disagreement between the two would make
 * every upload from a real phone fail its verification with nothing to point at,
 * so it is pinned here rather than discovered on a device.
 */
interface ChecksumVector {
  name: string;
  chunks: string[];
  digest: string;
}

const vectors = JSON.parse(
  readFileSync(new URL('../../test/testdata/crypto-vectors.json', import.meta.url), 'utf8'),
) as {
  handshakes: HandshakeVector[];
  envelopes: EnvelopeVector[];
  checksums: ChecksumVector[];
};

let failures = 0;

function check(name: string, got: string, want: string): void {
  if (got === want) {
    console.log(`  ok   ${name}`);
    return;
  }
  failures += 1;
  console.error(`  FAIL ${name}\n       got  ${got}\n       want ${want}`);
}

/**
 * The published test vector for CPACE-RISTRETTO255-SHA512, appendix B.3 of
 * draft-irtf-cfrg-cpace-14.
 *
 * Checked here as well as in internal/pairing/cpace_test.go, deliberately.
 * Everything below this checks that the two implementations agree with each
 * other, which two implementations of the same misreading also do. This checks
 * that they agree with the specification, and it is the only thing here that
 * can catch both sides being wrong in the same way.
 */
const draft = {
  prs: 'Password',
  ci: '6f630b425f726573706f6e6465720b415f696e69746961746f72',
  sid: '7e4b4791d6a8ef019b936c79fb7f2c57',
  generatorString:
    '11435061636552697374726574746f3235350850617373776f726464' +
    '00000000000000000000000000000000000000000000000000000000' +
    '00000000000000000000000000000000000000000000000000000000' +
    '00000000000000000000000000000000000000000000000000000000' +
    '000000000000000000000000000000001a6f630b425f726573706f6e' +
    '6465720b415f696e69746961746f72107e4b4791d6a8ef019b936c79' +
    'fb7f2c57',
  generator: 'a6fc82c3b8968fbb2e06fee81ca858586dea50d248f0c7ca6a18b0902a30b36b',
  scalarA: 'da3d23700a9e5699258aef94dc060dfda5ebb61f02a5ea77fad53f4ff0976d08',
  ya: 'd40fb265a7abeaee7939d91a585fe59f7053f982c296ec413c624c669308f87a',
  scalarB: 'd2316b454718c35362d83d69df6320f38578ed5984651435e2949762d900b80d',
  yb: '08bcf6e9777a9c313a3db6daa510f2d398403319c2341bd506a92e672eb7e307',
  k: 'e22b1ef7788f661478f3cddd4c600774fc0f41e6b711569190ff88fa0e607e09',
  isk:
    '4c5469a16b2364c4b944ebc1a79e51d1674ad47db26e8718154f59faebfaa52d' +
    '8346f30aa58377117eb20d527f2cbc5c76381f7fd372e89df8239f87f2e02ed1',
};

function fromHex(hex: string): Uint8Array {
  return Uint8Array.from(hex.match(/../g)!.map((byte) => parseInt(byte, 16)));
}

function toHex(bytes: Uint8Array): string {
  return [...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('');
}

/** The scalars are given little-endian, as the draft prints them. */
function scalarFromHex(hex: string): bigint {
  const bytes = fromHex(hex);
  let value = 0n;
  for (let i = bytes.length - 1; i >= 0; i--) value = (value << 8n) | BigInt(bytes[i]);
  return value;
}

console.log('CPace, against the draft vector');
{
  const prs = new TextEncoder().encode(draft.prs);
  const ci = fromHex(draft.ci);
  const sid = fromHex(draft.sid);

  check('generator string', toHex(generatorString(prs, ci, sid)), draft.generatorString);

  const g = calculateGenerator(prs, ci, sid);
  check('generator', toHex(g.toBytes()), draft.generator);

  const ya = g.multiply(scalarFromHex(draft.scalarA)).toBytes();
  const yb = g.multiply(scalarFromHex(draft.scalarB)).toBytes();
  check('Ya', toHex(ya), draft.ya);
  check('Yb', toHex(yb), draft.yb);

  const k = scalarMultVfy(scalarFromHex(draft.scalarA), fromHex(draft.yb));
  check('K', toHex(k), draft.k);

  const encoder = new TextEncoder();
  check('ISK', toHex(isk(sid, k, ya, encoder.encode('ADa'), yb, encoder.encode('ADb'))), draft.isk);
}

console.log('handshake vectors');
for (const v of vectors.handshakes) {
  const sid = fromBase64(v.sid);
  const clientSecret = secretFrom(fromBase64(v.client_secret));
  const serverSecret = secretFrom(fromBase64(v.server_secret));

  // The messages are recomputed rather than read, so that a disagreement about
  // how a secret reduces into the scalar field is caught here rather than
  // hiding behind a value copied out of the file.
  const clientMessage = message(clientSecret, v.code, v.host_id, sid);
  const serverMessage = message(serverSecret, v.code, v.host_id, sid);
  check(`${v.name}: client message`, toBase64(clientMessage), v.client_message);
  check(`${v.name}: server message`, toBase64(serverMessage), v.server_message);

  const { key, proof } = complete(
    clientSecret,
    sid,
    clientMessage,
    fromBase64(v.server_message),
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

console.log('checksum vectors');
for (const v of vectors.checksums) {
  // Fed chunk by chunk, exactly as web/src/lib/upload.ts feeds it, so what is
  // checked is the incremental path rather than a one-shot convenience call.
  const hasher = blake2b.create({ dkLen: 32 });
  for (const chunk of v.chunks) hasher.update(fromBase64(chunk));

  check(`${v.name}: digest`, toBase64(hasher.digest()), v.digest);
}

if (failures > 0) {
  console.error(`\n${failures} vector(s) disagree between Go and TypeScript.`);
  process.exit(1);
}
console.log(
  `\nall ${
    vectors.handshakes.length * 4 + vectors.envelopes.length * 2 + vectors.checksums.length + 7
  } vectors agree`,
);
