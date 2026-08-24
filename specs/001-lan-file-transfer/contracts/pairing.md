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

The key agreement is **CPace**, in the CPACE-RISTRETTO255-SHA512 suite of
[draft-irtf-cfrg-cpace-14](https://datatracker.ietf.org/doc/draft-irtf-cfrg-cpace/).

```text
Host                                              Phone
  │                                                 │
  │  displays 6-digit code C and a QR code          │
  │◄──────────── GET /connect ───────────────────── │  learns the host identifier H
  │                                                 │
  │                       phone picks sid, computes │
  │                       G  = map(C, H, sid)       │
  │                       Ya = ya·G                 │
  │◄──────────── POST /api/pair/init ────────────── │  { sid, message: Ya }
  │                                                 │
  │  G  = map(C, H, sid)                            │
  │  Yb = yb·G                                      │
  │  K  = yb·Ya                                     │
  │  ───────────────────────────────────────────►   │  { handshake_id, message: Yb }
  │                                                 │
  │                                   K = ya·Yb     │
  │                                                 │
  │◄──────────── POST /api/pair/confirm ─────────── │  { handshake_id, proof }
  │                                                 │
  │  human approves on the host  (FR-010)           │
  │  ───────────────────────────────────────────►   │  AEAD(key, session credential)
```

**The code is never transmitted.** It is an input to a derivation on each side and appears in no
request, in no response, and in no log. `test/integration/pairing_secrecy_test.go` and
`web/tests/e2e/pairing_secrecy.spec.ts` assert this against recorded traffic, the second against
the client that actually ships.

**What the observer sees**: a session identifier, two group elements, a handshake identifier, and
a confirmation tag. Every one of them is independent of the code: the two elements are uniformly
distributed whichever six digits produced them, so no candidate can be tested against any of it.

**What an attacker who answers gets**: one guess. Whoever plays the host's part has to choose a
candidate code before computing their message, and relating their generator to the honest one
needs a discrete logarithm. So an impersonation attempt tests exactly one of the million codes and
spends one of the five attempts, which is what makes twenty bits an acceptable amount to ask a
person to retype.

## Derivation

```text
G   = ristretto255_map( SHA-512( generator_string("CPaceRistretto255", C, H, sid) ) )
Ya  = ya·G                  ya, yb sampled uniformly, discarded after the exchange
Yb  = yb·G
K   = ya·Yb = yb·Ya         refused if it decodes badly or is the identity element

ISK = SHA-512( lv_cat("CPaceRistretto255_ISK", sid, K)
               || lv_cat(Ya, "") || lv_cat(Yb, handshake_id) )

session key  = ISK[0:32]
proof        = HMAC-SHA256(ISK[32:64], "fastr-pair-confirm-v2")
```

`H` is the host's device identifier, so an exchange run against one computer cannot be replayed at
another showing the same digits. `handshake_id` is the host's associated data, so it is bound into
the key rather than only into the tag. The proof comes from the half of the ISK that is not the
session key, so a tag that necessarily travels in the clear says nothing about the key that must
stay secret.

Both implementations are checked against the draft's published test vector, appendix B.3:
`internal/pairing/cpace_test.go` and `web/scripts/verify-crypto.ts`. They are also checked against
each other, from fixed inputs, in `test/testdata/crypto-vectors.json`.

## Code

- 6 decimal digits, from a cryptographic source, displayed on the host only.
- Single use (FR-012).
- Expires 3 minutes after display.
- Dies after 5 failed attempts (FR-013).
- Attempts are admitted at `init`, where the growing delay is enforced, and judged at `confirm`.
  Starting an exchange and abandoning it counts for nothing and resets nothing.
- Never sent, never logged, never echoed in a response, never shown again once used (FR-019).

## Protocol version

`2`. Version 1 sent the code in the clear and left an offline oracle in the transcript; it is not
accepted, and a device holding a version 1 credential pairs again.

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
