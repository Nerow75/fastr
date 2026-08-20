# Contract: Pairing and Key Derivation

**Date**: 2026-08-20 | **Plan**: [../plan.md](../plan.md)

The pairing exchange runs over a plain HTTP channel in simple mode, so it is designed on the
assumption that a passive observer is reading every byte of it.

## Goal

At the end of a successful pairing, both sides hold a shared session key that a network observer
cannot derive, and the host has recorded a human's explicit approval. The session key protects
control traffic in **both** protection modes, which is what keeps FR-017 true even when file
content travels in the clear.

## Handshake

```text
Host                                              Phone
  │                                                 │
  │  displays 6-digit code C and a QR code          │
  │◄──────────── GET /connect ───────────────────── │
  │                                                 │
  │◄──────────── POST /api/pair/init ────────────── │  { client_pub: X25519 public key }
  │  ───────────────────────────────────────────►   │  { server_pub, handshake_id, salt }
  │                                                 │
  │            both compute:                        │
  │            shared = X25519(own_priv, peer_pub)  │
  │            key    = HKDF(shared, salt, C, transcript)
  │                                                 │
  │◄──────────── POST /api/pair/confirm ─────────── │  AEAD(key, handshake_id || proof)
  │                                                 │
  │  human approves on the host  (FR-010)           │
  │  ───────────────────────────────────────────►   │  AEAD(key, session credential)
```

**What the observer sees**: two public keys, a salt, a handshake identifier, and ciphertext. The
shared secret is not derivable from the public keys, and the session credential never appears in
the clear. Mixing the code `C` into the derivation means an observer who did not see the code
cannot produce a valid `confirm`.

## Derivation

```text
shared     = X25519(ephemeral private, peer ephemeral public)
transcript = client_pub || server_pub || handshake_id
key        = HKDF-SHA256(ikm = shared, salt = salt, info = "fastr-pair-v1" || transcript || C)
```

Ephemeral keys are generated per handshake and discarded after it. The session key is stored on
the host in the Pairing record and in the browser's site data on the phone.

## Code

- 6 decimal digits, from a cryptographic source, displayed on the host only.
- Single use (FR-012).
- Expires 3 minutes after display.
- Dies after 5 failed `confirm` attempts (FR-013).
- Attempts are rate limited per source address, with a delay that grows after each failure.
- Never logged, never echoed in a response, never shown again once used (FR-019).

## Known limitation, flagged

A 6-digit code carries about 20 bits. An observer who captures the full handshake can attempt an
offline search over the code space against the `confirm` ciphertext. The online defences above do
not apply to an offline attempt.

A password-authenticated key exchange such as SPAKE2 removes this by construction, because no
value in the transcript can be tested against a candidate code offline. It is recorded in
[research.md](../research.md#4-cryptography-available-without-a-secure-context) as a hardening step
to settle **before the first release**, not after.

## Session credential

- 256 bits from a cryptographic source.
- Stored hashed on the host; the plaintext exists only in the phone's site data.
- Sent as a bearer token on every request (see [http-api.md](./http-api.md#authentication)).
- Invalidated immediately on revocation or expiry, with no grace period (FR-015).

## Envelope

Every `/api` payload in both modes is wrapped:

```text
AEAD-ChaCha20-Poly1305(key = session key,
                       nonce = direction || counter,
                       aad = method || path || protocol version)
```

The counter is monotonic per direction. A repeated or regressing counter is rejected as
`replay_detected`. Binding the method and path into the associated data stops a captured envelope
from being replayed against a different endpoint.

## Trust is never transitive

Two phones paired to the same computer do **not** thereby trust each other. A relayed
phone-to-phone transfer requires each phone to hold its own active pairing with the relaying
computer, and every hop is authorized separately (FR-054).
