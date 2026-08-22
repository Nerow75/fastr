/**
 * The pairing handshake, client side.
 *
 * This must match internal/pairing/handshake.go exactly. The protocol is
 * specified in specs/001-lan-file-transfer/contracts/pairing.md.
 *
 * As in envelope.ts, `crypto.subtle` is unavailable: the mobile page is served
 * over a plain local address, which is never a secure context. @noble/curves
 * and @noble/hashes are audited pure JavaScript and work in both.
 */

import { x25519 } from '@noble/curves/ed25519';
import { hkdf } from '@noble/hashes/hkdf';
import { hmac } from '@noble/hashes/hmac';
import { sha256 } from '@noble/hashes/sha256';
import { randomBytes } from '@noble/hashes/utils';

const HKDF_INFO = 'fastr-pair-v1';
const CONFIRM_INFO = 'fastr-confirm-v1';
const KEY_SIZE = 32;

export interface Keypair {
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

/** Generates an ephemeral keypair, discarded after one handshake. */
export function generateKeypair(): Keypair {
  const privateKey = randomBytes(32);
  return { privateKey, publicKey: x25519.getPublicKey(privateKey) };
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const total = parts.reduce((sum, p) => sum + p.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

/**
 * Builds the transcript both sides bind into the derivation.
 *
 * Order matters and matches the Go side: client public key, server public key,
 * handshake identifier. Including it means a tampered public key produces a
 * different key rather than a silent downgrade.
 */
function transcript(
  clientPublic: Uint8Array,
  serverPublic: Uint8Array,
  handshakeId: string,
): Uint8Array {
  return concat(clientPublic, serverPublic, new TextEncoder().encode(handshakeId));
}

export interface Derived {
  /** The session key, used for every sealed payload afterwards. */
  key: Uint8Array;
  /** The tag proving this side knew the pairing code. */
  proof: Uint8Array;
}

/**
 * Completes the client half of the key agreement.
 *
 *   shared = X25519(clientPrivate, serverPublic)
 *   key    = HKDF-SHA256(shared, salt, "fastr-pair-v1" || transcript || code)
 *   proof  = HMAC-SHA256(key, "fastr-confirm-v1" || handshakeId)
 *
 * The code goes into the info parameter rather than the salt, matching the Go
 * side: omitting it still derives something, but not what an honest peer
 * derives, so the confirmation fails rather than a weak key being accepted.
 */
export function derive(
  clientPrivate: Uint8Array,
  serverPublic: Uint8Array,
  clientPublic: Uint8Array,
  salt: Uint8Array,
  code: string,
  handshakeId: string,
): Derived {
  const shared = x25519.getSharedSecret(clientPrivate, serverPublic);

  const info = concat(
    new TextEncoder().encode(HKDF_INFO),
    transcript(clientPublic, serverPublic, handshakeId),
    new TextEncoder().encode(code),
  );

  const key = hkdf(sha256, shared, salt, info, KEY_SIZE);

  const proof = hmac
    .create(sha256, key)
    .update(new TextEncoder().encode(CONFIRM_INFO))
    .update(new TextEncoder().encode(handshakeId))
    .digest();

  return { key, proof };
}
