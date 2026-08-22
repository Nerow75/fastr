# Contract: HTTP API

**Date**: 2026-08-20 | **Plan**: [../plan.md](../plan.md)

The same API serves the browser client and peer instances. Paths under `/api` carry JSON unless
noted. Bulk content endpoints carry raw octets and are the only ones that stream.

## Transport and framing

- HTTP/1.1 over plain TCP in simple mode, over TLS in trusted mode. Same paths, same semantics.
- Control payloads under `/api` are wrapped in a ChaCha20-Poly1305 envelope keyed by the pairing
  session key, in **both** modes. Bulk content endpoints are wrapped only in trusted mode.
- The envelope carries a monotonic counter per direction. A repeated or out-of-order counter is
  rejected, which is what stops replay on a plain channel.

## Authentication

Every request except the pairing handshake and the static assets carries:

```http
Authorization: Bearer <session credential>
```

An absent, unknown, expired, or revoked credential returns `401` with no body detail. There is no
anonymous read of anything, including the device list (FR-011).

## Error format

```json
{ "error": "insufficient_space", "detail_key": "error.insufficient_space", "params": { "needed": 2147483648, "available": 1073741824 } }
```

`error` is stable and machine-readable. `detail_key` indexes the translation catalogue, so the
message is rendered in the client's language, and `params` fills it. No message is ever assembled
server-side in a fixed language (FR-039a). Every error maps to a corrective action in the
catalogue (FR-038).

## Static and connection

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/` | The web application. Serves the desktop view for loopback origins, the mobile view otherwise. |
| `GET` | `/connect` | Connection information: address, port, protocol version, device name. Unauthenticated by design; carries no user data. |
| `GET` | `/ca.crt` | The local certificate authority, for trusted-mode setup only. Returns `404` when trusted mode has never been initialized. |

## Pairing

See [pairing.md](./pairing.md) for the handshake and key derivation.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/pair/init` | Client sends its ephemeral public key. Server replies with its own and a handshake identifier. |
| `POST` | `/api/pair/confirm` | Client proves knowledge of the pairing code. On success the server returns `202` with a `pending_id`: the device is queued for the human confirmation FR-010 requires, **not** granted access. Rate limited; a code dies after 5 failures or its expiry, whichever comes first (FR-012, FR-013). |
| `GET` | `/api/pair/status?pending={id}` | Polled by the waiting device. Returns `awaiting_approval`, `rejected`, `expired`, or `approved`. Only `approved` carries the credential, sealed with the handshake key, and only once. Unauthenticated by necessity: the device has no credential yet. |
| `GET` | `/api/pair/pending` | Host only. Lists devices awaiting confirmation. Carries no key material. |
| `POST` | `/api/pair/pending/{id}/approve` | Host only. The human saying yes. |
| `POST` | `/api/pair/pending/{id}/reject` | Host only. The human saying no. |

**"Host only" means loopback.** Reaching these routes requires a connection from
the machine itself, which is the same trust boundary the operating system
already enforces and exactly what "a human on the receiving device" means. They
deliberately do not require a pairing, because on first run the host's own page
has none. The decision is made on the connection's remote address, never on a
header a client could set.

## Devices and pairings

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/devices` | Known devices with reachability, trust mode, and protection mode. |
| `PATCH` | `/api/devices/{id}` | Rename. |
| `PATCH` | `/api/pairings/{id}` | Set `trust_mode` or `require_trusted`. Takes effect immediately (FR-016b, FR-047c). |
| `DELETE` | `/api/pairings/{id}` | Revoke. Any in-flight transfer from that device is cancelled. |

## Transfers

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/transfers` | Declare a transfer: target, items with names, relative paths, and sizes. Server validates free space and returns `409 insufficient_space` before any content moves (FR-028). Returns the transfer with per-item upload or download URLs. |
| `GET` | `/api/transfers/{id}` | Current state, per-item progress, committed offsets. |
| `POST` | `/api/transfers/{id}/accept` | Target side, `ask` mode only. |
| `POST` | `/api/transfers/{id}/decline` | Target side. |
| `POST` | `/api/transfers/{id}/cancel` | Either side, any non-terminal state (FR-035). |

### Content, downward

```http
POST /api/transfers/{id}/items/{index}/ticket  -> { "ticket": "...", "expires_in": 21600 }
GET  /api/transfers/{id}/items/{index}/content?ticket=...
Range: bytes=1073741824-
```

Responds `206` with `Content-Range` and streams to the end. `Accept-Ranges: bytes` is always
advertised, which is what lets a browser download manager resume without the application's help.
`Content-Disposition` carries the sanitized name, RFC 5987 encoded so a non-ASCII name survives.

This is the only route that accepts a **scoped ticket** in place of the bearer header. A file is
saved by the browser's own download manager, the only mechanism that writes a multi-gigabyte file
to a phone without holding it in memory, and it fetches a plain URL. Unlike a stream ticket a
content ticket is multi-use, because the download manager re-requests the URL when it resumes, and
it is bound to exactly one item of one transfer: leaking one exposes a file its holder was being
sent anyway. Revocation invalidates it immediately.

### Content, sideways: how the bytes get there

A browser never reveals a file's path, so the server cannot read the file the user dropped on the
desktop page, and a phone cannot accept an inbound connection. The receiver's fetch registers a
demand and waits; the sender attaches its body to it:

```http
POST /api/transfers/{id}/items/{index}/supply?offset=1073741824
GET  /api/transfers/{id}/waiting  -> { "waiting": [ { "item": 0, "offset": 0 } ] }
```

The bytes stream from one request to the other and are never written to disk. The supply request
does not return until they have arrived, so the sending page never reports a file as sent while it
is still in flight. A supplier whose offset does not match the demand is refused: a hole in a file
still passes a length check. `/waiting` exists so a page that reconnects mid-transfer can resume
without having seen the event it missed.

### Content, upward

```http
POST /api/transfers/{id}/items/{index}/content?offset=1073741824
Content-Type: application/octet-stream
```

The body is a chunk starting at `offset`. The server appends, fsyncs, and replies with the new
committed offset:

```json
{ "committed_offset": 1207959552 }
```

A client resuming asks first:

```http
GET /api/transfers/{id}/items/{index}/offset
```

An `offset` that does not match the committed offset returns `409 offset_mismatch` with the
server's value, so the client corrects rather than corrupts (FR-031).

### Completion

```http
POST /api/transfers/{id}/items/{index}/complete
{ "checksum": "<base64 BLAKE2b>" }
```

The server verifies while moving the file out of staging. A mismatch returns `422
checksum_mismatch`, marks the item failed, and deletes the partial file. Nothing reaches the final
path before this succeeds (FR-032, FR-033).

## Queue

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/queue` | Ordered entries and the active one, as full transfer objects rather than identifiers, and scoped to what the caller is a party to. Serving them whole makes this the **reconciliation** endpoint as well: a transfer that is neither terminal nor forgotten is by definition either the active one or a waiting entry, so a page that reads this on connect recovers every transfer whose announcing event it was not there to hear. Without it a phone that reloads mid-transfer never sees that transfer again. |
| `POST` | `/api/queue/reorder` | Full ordering of non-active entries. Rejects any attempt to move the active entry. |
| `DELETE` | `/api/queue/{transfer_id}` | Remove one waiting entry. |
| `DELETE` | `/api/queue` | Clear all waiting entries. The active transfer is untouched (FR-035c). |

## History and settings

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/history` | Paged, reverse chronological. |
| `DELETE` | `/api/history` | Clear (FR-039). |
| `GET` | `/api/settings` | Never returns key material or paths outside what the user set. |
| `PATCH` | `/api/settings` | Device name, receive folder, language, autostart, bound interfaces. |

## Events

```http
POST /api/events/ticket     -> { "ticket": "...", "expires_in": 30 }
GET  /api/events?ticket=... -> text/event-stream
```

`EventSource` cannot set a request header, so the stream cannot carry a bearer
credential. It is not put in the query string either: a URL lands in browser
history, in a referrer, and in the access log of anything between the two
devices, and a long-lived credential belongs in none of those. Instead the
client spends one authenticated, sealed request on a **single-use ticket that
expires in 30 seconds**. A ticket recovered from history is worthless by the
time anyone reads it, and revocation invalidates an unredeemed one.

One stream per client. Event types: `device_appeared`, `device_lost`, `pairing_pending`,
`pairing_changed`, `transfer_queued`, `transfer_started`, `transfer_progress`,
`transfer_interrupted`, `transfer_resumed`, `transfer_completed`, `transfer_failed`,
`transfer_cancelled`, `queue_changed`, `sweep_removed`.

`transfer_progress` is throttled to at most 4 events per second per transfer. The accessibility
layer announces only `transfer_started`, `transfer_interrupted`, `transfer_resumed`,
`transfer_completed`, and `transfer_failed`, never `transfer_progress`, which is what keeps a
running transfer from flooding a screen reader (FR-039i).

## Trusted mode

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/trust/init` | Generate the local authority if absent and issue a certificate for the current addresses. |
| `GET` | `/api/trust/status` | Whether the authority exists, which devices use trusted mode, and the setup step each is on. |
| `POST` | `/api/trust/verify` | Called by the phone once it reaches the HTTPS origin, to record that the device is now trusted. |

Abandoning setup at any point leaves the existing simple-mode pairing working (FR-047d).

## Refusals that must be tested

| Situation | Response |
|---|---|
| No or invalid credential | `401`, no detail |
| Valid credential, revoked or expired pairing | `401`, `pairing_revoked` or `pairing_expired` |
| `require_trusted` device connecting over the plain channel | `426 trusted_mode_required` |
| Path escaping the destination root after normalization | `400 invalid_path`, logged as a security event without the offending path |
| Free space short at declaration | `409 insufficient_space` |
| Replayed or out-of-order envelope counter | `400 replay_detected` |
| Second concurrent activation attempt on the queue | `409 queue_busy` |
