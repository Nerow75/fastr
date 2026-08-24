/**
 * CPace, the browser half.
 *
 * This must match internal/pairing/cpace.go exactly, and both sides check the
 * published test vector from appendix B.3 of draft-irtf-cfrg-cpace-14. The
 * cipher suite is CPACE-RISTRETTO255-SHA512, and the reasoning for choosing a
 * password-authenticated exchange at all is written out in the Go file rather
 * than repeated here.
 *
 * The short version: the pairing code is six digits, so about twenty bits.
 * Anything that lets somebody test a candidate code without talking to the host
 * turns that into a search finished in seconds. CPace leaves no such value in
 * the transcript, and the code itself never goes on the wire.
 *
 * As in envelope.ts, `crypto.subtle` is unavailable: the mobile page is served
 * over a plain local address, which is never a secure context. @noble/curves is
 * audited pure JavaScript and works in both.
 */

import { ristretto255 } from '@noble/curves/ed25519';
import { hmac } from '@noble/hashes/hmac';
import { sha256 } from '@noble/hashes/sha256';
import { sha512 } from '@noble/hashes/sha512';
import { randomBytes } from '@noble/hashes/utils';

const DSI = 'CPaceRistretto255';
const DSI_ISK = 'CPaceRistretto255_ISK';

/** Separates the confirmation tag from any other use of the same key material. */
const CONFIRM_LABEL = 'fastr-pair-confirm-v2';

/** The length of the session key handed to the envelope. */
const KEY_SIZE = 32;

/** The joining device binds no associated data of its own. */
const EMPTY = new Uint8Array(0);

/** SHA-512's input block size, which the zero padding is measured against. */
const HASH_BLOCK_SIZE = 128;

/** The length of an encoded ristretto255 element. */
export const ELEMENT_SIZE = 32;

/** The length of the session identifier the joining device chooses. */
export const SESSION_ID_SIZE = 16;

/** Thrown when a peer sends something no honest implementation sends. */
export class BadElementError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BadElementError';
  }
}

const utf8 = new TextEncoder();

function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((sum, p) => sum + p.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

/**
 * Returns the data with its length encoded as LEB128 in front.
 *
 * LEB128 rather than a fixed width because the draft says so, and the encoding
 * has to match for the generator to match. Nothing this application passes
 * reaches 128 bytes and needs the second byte, which is exactly why the loop is
 * here: the first person to widen the channel identifier past 127 bytes would
 * otherwise find out from a phone that will not pair.
 */
function prependLen(data: Uint8Array): Uint8Array {
  const encoded: number[] = [];
  let length = data.length;
  for (;;) {
    if (length < 128) {
      encoded.push(length);
    } else {
      encoded.push((length & 0x7f) + 0x80);
    }
    length >>= 7;
    if (length === 0) break;
  }
  return concat(Uint8Array.from(encoded), data);
}

/** Concatenates each part with its length in front. */
function lvCat(...parts: Uint8Array[]): Uint8Array {
  return concat(...parts.map(prependLen));
}

/**
 * Builds the input the generator is derived from.
 *
 * The zero padding makes the domain separator and the code fill the hash
 * function's whole first input block, so the compression of the block carrying
 * the code cannot be told apart from any other by how long it takes.
 */
export function generatorString(prs: Uint8Array, ci: Uint8Array, sid: Uint8Array): Uint8Array {
  const pad = Math.max(
    0,
    HASH_BLOCK_SIZE - 1 - prependLen(prs).length - prependLen(utf8.encode(DSI)).length,
  );
  return lvCat(utf8.encode(DSI), prs, new Uint8Array(pad), ci, sid);
}

/**
 * Maps the pairing code to the group element both sides raise their ephemeral
 * scalar against.
 *
 * `ristretto255.Point.hashToCurve` is the one-way map on 64 uniform bytes,
 * which is what the draft's `map_to_group_mod_ristretto255` is. It carries a
 * deprecation notice pointing at `ristretto255_hasher.hashToCurve`, and that
 * replacement is **not** usable here: it runs expand_message_xmd with a domain
 * separation tag first, which is a different function and lands on a different
 * point. Following the deprecation would break pairing between any two builds
 * that disagreed about it, so it is deliberately not followed, and the vector
 * test is what would catch it if somebody did.
 */
export function calculateGenerator(prs: Uint8Array, ci: Uint8Array, sid: Uint8Array) {
  return ristretto255.Point.hashToCurve(sha512(generatorString(prs, ci, sid)));
}

/** Interprets bytes as a little-endian integer. */
function bytesToNumberLE(bytes: Uint8Array): bigint {
  let value = 0n;
  for (let i = bytes.length - 1; i >= 0; i--) value = (value << 8n) | BigInt(bytes[i]);
  return value;
}

/** How many bytes a secret scalar is derived from. */
export const UNIFORM_SECRET_SIZE = 64;

/**
 * Reduces uniform bytes into an ephemeral secret scalar.
 *
 * Sixty-four bytes reduced into the scalar field, so the result is
 * indistinguishable from uniform rather than merely 252 bits wide. Separate
 * from the sampling so that a vector can pin a scalar without pinning a random
 * source, which is the only way this implementation and the Go one can be
 * compared on the same inputs.
 */
export function secretFrom(uniform: Uint8Array): bigint {
  if (uniform.length !== UNIFORM_SECRET_SIZE) {
    throw new Error(`secret needs ${UNIFORM_SECRET_SIZE} uniform bytes, got ${uniform.length}`);
  }
  return ristretto255.Point.Fn.create(bytesToNumberLE(uniform));
}

/** Samples an ephemeral secret scalar. */
export function randomScalar(): bigint {
  for (;;) {
    const candidate = secretFrom(randomBytes(UNIFORM_SECRET_SIZE));
    // Zero is not a usable secret and reducing can in principle produce it.
    // Never observed and never will be; cheaper to loop than to reason about.
    if (candidate !== 0n) return candidate;
  }
}

/**
 * Returns the group element a side publishes: its secret scalar applied to the
 * generator the pairing code maps to.
 */
export function message(secret: bigint, code: string, hostId: string, sid: Uint8Array): Uint8Array {
  return calculateGenerator(utf8.encode(code), utf8.encode(hostId), sid).multiply(secret).toBytes();
}

/**
 * Multiplies a received group element by a secret scalar, refusing anything an
 * honest peer would not have sent.
 *
 * Both refusals matter. An encoding that is not a canonical ristretto255
 * element is rejected on decode, which is what keeps a crafted point from
 * steering the result. A product equal to the identity is rejected here,
 * because that outcome is the same whatever the scalar was: accepting it would
 * hand a shared secret to somebody who never knew the code.
 */
export function scalarMultVfy(secret: bigint, encodedPeer: Uint8Array): Uint8Array {
  let peer;
  try {
    peer = ristretto255.Point.fromBytes(encodedPeer);
  } catch (cause) {
    throw new BadElementError(`not a ristretto255 element: ${String(cause)}`);
  }

  const product = peer.multiply(secret);
  if (product.is0()) throw new BadElementError('shared point is the identity');

  return product.toBytes();
}

/**
 * Derives the intermediate session key both sides end up holding.
 *
 *   ISK = SHA-512( lv_cat(DSI_ISK, sid, K) || lv_cat(Ya, ADa) || lv_cat(Yb, ADb) )
 *
 * The order is the initiator's message then the responder's, which is the
 * draft's transcript_ir. Fixed rather than sorted because this exchange has a
 * clear initiator: the device asking to be let in speaks first.
 */
export function isk(
  sid: Uint8Array,
  k: Uint8Array,
  ya: Uint8Array,
  adA: Uint8Array,
  yb: Uint8Array,
  adB: Uint8Array,
): Uint8Array {
  return sha512(concat(lvCat(utf8.encode(DSI_ISK), sid, k), lvCat(ya, adA), lvCat(yb, adB)));
}

/** Produces the identifier this device chooses for one exchange. */
export function newSessionId(): Uint8Array {
  return randomBytes(SESSION_ID_SIZE);
}

export interface Derived {
  /** The session key, used for every sealed payload afterwards. */
  key: Uint8Array;
  /** The tag proving this side reached the same key. */
  proof: Uint8Array;
}

export interface Begun {
  /** Kept until the host answers, then discarded. */
  secret: bigint;
  /** The message the host needs: Ya. */
  message: Uint8Array;
}

/**
 * The joining device's first step: map the code to a generator and produce the
 * message the host needs.
 *
 * `hostId` is the channel identifier, so a proof produced for the computer in
 * front of you cannot be replayed at another one showing the same digits.
 */
export function begin(code: string, hostId: string, sid: Uint8Array): Begun {
  const secret = randomScalar();
  return { secret, message: message(secret, code, hostId, sid) };
}

/**
 * The joining device's second step: agree with the host, then bind the whole
 * transcript into the key.
 *
 * This must land on the same two values as internal/pairing/handshake.go's
 * HostComplete, from the other secret. The peer's message is the host's, and
 * getting that wrong — multiplying by your own — would produce a key nobody
 * else can reach while every self-consistency check still passed, which is why
 * the cross-implementation vectors pin both sides rather than one.
 *
 * The handshake identifier is the responder's associated data, so it is bound
 * into the key itself rather than only into the tag that follows it.
 */
export function complete(
  secret: bigint,
  sid: Uint8Array,
  clientMessage: Uint8Array,
  serverMessage: Uint8Array,
  handshakeId: string,
): Derived {
  const shared = scalarMultVfy(secret, serverMessage);
  const derived = isk(sid, shared, clientMessage, EMPTY, serverMessage, utf8.encode(handshakeId));

  return { key: derived.slice(0, KEY_SIZE), proof: confirmProof(derived.slice(KEY_SIZE)) };
}

/**
 * The tag proving this side reached the same key.
 *
 * Over the second half of the exchange's output rather than the half used as
 * the session key, so a tag that necessarily travels in the clear says nothing
 * about the key that has to stay secret.
 */
function confirmProof(confirmKey: Uint8Array): Uint8Array {
  return hmac.create(sha256, confirmKey).update(utf8.encode(CONFIRM_LABEL)).digest();
}
