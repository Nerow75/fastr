/**
 * The sealed envelope, client side.
 *
 * This must match internal/pairing/envelope.go byte for byte. The wire format
 * and the nonce layout are specified in specs/001-lan-file-transfer/contracts/pairing.md;
 * anything here that drifts from the Go implementation produces an
 * authentication failure rather than a silent misinterpretation, which is the
 * one saving grace of an AEAD.
 *
 * `crypto.subtle` is deliberately not used. The mobile page is served over a
 * plain local address, which browsers never treat as a secure context, so the
 * native cryptography API does not exist there. @noble/ciphers is audited and
 * pure JavaScript, so it works in both contexts.
 */

import { chacha20poly1305 } from '@noble/ciphers/chacha';

export const PROTOCOL_VERSION = 1;

/** Which counter space a message belongs to. Separating the directions is what
 *  stops a server message being reflected back at the server. */
export const ClientToServer = 0;
export const ServerToClient = 1;

const COUNTER_SIZE = 8;
const NONCE_SIZE = 12;
const KEY_SIZE = 32;

export class ReplayError extends Error {
  constructor() {
    super('replayed or out-of-order counter');
    this.name = 'ReplayError';
  }
}

export class AuthenticationError extends Error {
  constructor() {
    super('envelope failed to authenticate');
    this.name = 'AuthenticationError';
  }
}

/** Builds the 12-byte nonce: direction, then the counter, then zero padding. */
function nonce(direction: number, counter: bigint): Uint8Array {
  const out = new Uint8Array(NONCE_SIZE);
  out[0] = direction;
  new DataView(out.buffer).setBigUint64(1, counter, false);
  return out;
}

/**
 * Builds the associated data that binds a message to its endpoint.
 *
 * Layout: method, a zero byte, path, a zero byte, then the version as a
 * big-endian uint32. Without this, an envelope captured from a harmless
 * endpoint could be replayed against a dangerous one.
 */
function associatedData(method: string, path: string, version: number): Uint8Array {
  const encoder = new TextEncoder();
  const m = encoder.encode(method);
  const p = encoder.encode(path);

  const out = new Uint8Array(m.length + 1 + p.length + 1 + 4);
  let offset = 0;
  out.set(m, offset);
  offset += m.length + 1; // the separator is already zero
  out.set(p, offset);
  offset += p.length + 1;
  new DataView(out.buffer).setUint32(offset, version, false);
  return out;
}

/** Seals and opens control payloads for one session. */
export class Envelope {
  private readonly key: Uint8Array;
  private readonly sendDirection: number;
  private readonly recvDirection: number;

  private sendCounter = 0n;
  private recvHighWater = 0n;
  private seenAny = false;

  constructor(key: Uint8Array, sendDirection: number = ClientToServer) {
    if (key.length !== KEY_SIZE) {
      throw new Error(`session key must be ${KEY_SIZE} bytes, got ${key.length}`);
    }
    this.key = key;
    this.sendDirection = sendDirection;
    this.recvDirection = sendDirection === ServerToClient ? ClientToServer : ServerToClient;
  }

  /** Encrypts a payload for one request. */
  seal(method: string, path: string, plaintext: Uint8Array): Uint8Array {
    this.sendCounter += 1n;

    const sealed = chacha20poly1305(
      this.key,
      nonce(this.sendDirection, this.sendCounter),
      associatedData(method, path, PROTOCOL_VERSION),
    ).encrypt(plaintext);

    const out = new Uint8Array(COUNTER_SIZE + sealed.length);
    new DataView(out.buffer).setBigUint64(0, this.sendCounter, false);
    out.set(sealed, COUNTER_SIZE);
    return out;
  }

  /** Decrypts a payload, refusing replays and out-of-order counters. */
  open(method: string, path: string, sealed: Uint8Array): Uint8Array {
    if (sealed.length < COUNTER_SIZE) {
      throw new AuthenticationError();
    }

    const counter = new DataView(
      sealed.buffer,
      sealed.byteOffset,
      sealed.byteLength,
    ).getBigUint64(0, false);

    // Strictly increasing. Counters start at 1, so zero is malformed.
    if (counter === 0n) throw new AuthenticationError();
    if (this.seenAny && counter <= this.recvHighWater) throw new ReplayError();

    let plaintext: Uint8Array;
    try {
      plaintext = chacha20poly1305(
        this.key,
        nonce(this.recvDirection, counter),
        associatedData(method, path, PROTOCOL_VERSION),
      ).decrypt(sealed.subarray(COUNTER_SIZE));
    } catch {
      // The high-water mark is deliberately not advanced on failure, so a
      // forged message with a huge counter cannot wedge the session.
      throw new AuthenticationError();
    }

    this.recvHighWater = counter;
    this.seenAny = true;
    return plaintext;
  }
}

/** Base64 helpers. The wire carries base64 because the payload travels in a
 *  request body that must survive whatever a browser does to it. */
export function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

export function fromBase64(text: string): Uint8Array {
  const binary = atob(text.trim());
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}
