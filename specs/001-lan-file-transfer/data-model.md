# Phase 1 Data Model: LAN File Transfer

**Date**: 2026-08-20 | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md)

All state lives in one bbolt file. Bucket names are given per entity. Identifiers are ULIDs unless
stated otherwise, so that ordering by key is ordering by creation time.

---

## Device

Bucket: `devices`. A computer or a phone known to this instance.

| Field | Type | Rules |
|---|---|---|
| `id` | string | Stable across restarts. Generated once per installation for a computer, once per browser session store for a phone. |
| `name` | string | 1 to 64 characters after trimming. User editable. Not unique. |
| `platform` | enum | `linux`, `windows`, `android`, `ios`, `other`. Advisory only, never a permission input. |
| `kind` | enum | `computer` or `phone`. Determines whether it can relay. |
| `addresses` | []string | Last known network addresses. Refreshed by discovery, never trusted for authorization. |
| `port` | uint16 | Computers only. |
| `last_seen` | timestamp | Drives the reachable indicator. |
| `reachable` | bool | Derived, not persisted. |

**Rules**: two devices may share a `name`; the interface disambiguates with a short suffix of `id`
(FR-005). A device record alone grants nothing; authorization comes only from a Pairing.

---

## Pairing

Bucket: `pairings`, keyed by `device_id`. The trust relationship, and the only source of access.

| Field | Type | Rules |
|---|---|---|
| `device_id` | string | Foreign key to Device. One pairing per device. |
| `token_hash` | []byte | Hash of the session credential. The credential itself is never stored. |
| `session_key` | []byte | Derived from the pairing handshake. Encrypts control traffic in every mode. |
| `trust_mode` | enum | `auto` or `ask`. Governs acceptance and expiry (FR-016a, FR-016b). |
| `protection` | enum | `simple` or `trusted`. Which channel the device uses (FR-047b). |
| `require_trusted` | bool | When true, simple-mode connections from this device are refused (FR-047c). |
| `created_at` | timestamp | |
| `last_activity` | timestamp | Any successful request refreshes it. |
| `expires_at` | timestamp | Derived: `last_activity` + 1 year when `auto`, + 30 days when `ask` (FR-016). |
| `revoked_at` | timestamp | Nullable. Set means immediately unusable, kept for history. |

**State transitions**

```text
                  code accepted
   (none) ─────────────────────────► active
                                       │
          trust mode changed           │  recomputes expires_at from last_activity
          ────────────────────────────►│
                                       │
              inactivity past expiry   ▼
   active ──────────────────────────► expired ──── re-pair ───► active
      │
      │  user revokes
      ▼
   revoked  (terminal; re-pairing creates a new record)
```

**Rules**: `auto` is the default for a pairing the user created themselves, `ask` otherwise
(FR-016b). Changing `trust_mode` recomputes `expires_at` from `last_activity`, never from now.
Revocation takes effect on the next request with no grace period (FR-015).

---

## Transfer

Bucket: `transfers`. One send operation, whatever its direction.

| Field | Type | Rules |
|---|---|---|
| `id` | ULID | |
| `direction` | enum | `outgoing`, `incoming`, `relayed`. |
| `source_device_id` | string | |
| `target_device_id` | string | Must differ from source. |
| `relay_device_id` | string | Set only when `direction` is `relayed` (FR-053). |
| `protection` | enum | `simple` or `trusted`. Fixed at start; a downgrade requires explicit confirmation (FR-047e). |
| `items` | []TransferItem | At least one. |
| `total_bytes` | uint64 | Sum of item sizes, known before start (FR-028). |
| `transferred_bytes` | uint64 | Monotonic within an attempt, restored on resume. |
| `state` | enum | See below. |
| `failure_cause` | enum | Set only in `failed`. Maps to a translated message with a corrective action (FR-038). One of `insufficient_space`, `checksum_mismatch`, `network_lost`, `declined`, `acceptance_timeout`, `pairing_revoked`, `pairing_expired`, `relay_unavailable`, `trusted_mode_required`, `source_unreadable`, `destination_full`, `destination_unwritable`, `abandoned`. |
| `queued_at`, `started_at`, `ended_at` | timestamp | |
| `last_progress_at` | timestamp, nullable | When bytes last moved. The retention sweep uses it to tell a transfer nobody has touched in a week from one that is simply large and slow: `started_at` is stamped once, and a 10 GB file over a bad link legitimately takes days. Written by the same store update that advances a committed offset, so it costs nothing. |

**State machine**

```text
   queued ──► awaiting_acceptance ──► running ──► verifying ──► completed
      │              │                   │            │
      │              │ declined          │ network    │ checksum mismatch
      │              │ or timeout        │ lost       │
      │              ▼                   ▼            ▼
      └──────────► failed ◄──── interrupted ────► failed
                     ▲               │
                     │               │ retried
   cancelled ◄───────┴───────────────┘
   (from any non-terminal state, by either device)
```

- `awaiting_acceptance` is entered only when the target pairing is `ask` mode. It has a bounded
  window, after which the transfer fails rather than waiting forever (FR-016d).
- `interrupted` is distinct from `failed`: it retains resume state and does not block the queue
  (FR-035d).
- Terminal states are `completed`, `failed`, and `cancelled`. Only terminal transfers become
  History Entries.

---

## TransferItem

Embedded in Transfer, not a separate bucket.

| Field | Type | Rules |
|---|---|---|
| `original_name` | string | As given by the sender. |
| `stored_name` | string | After destination sanitization and collision resolution. Differs from `original_name` only when necessary, and the difference is reported (FR-024). |
| `relative_path` | string | Set for folder sends. Must resolve inside the destination root; any traversal is rejected (FR-018). |
| `size` | uint64 | Zero is valid. |
| `checksum` | []byte | BLAKE2b of the source, computed while streaming. |
| `committed_offset` | uint64 | Durably acknowledged bytes. The resume point (FR-031). |
| `state` | enum | Same lifecycle as its Transfer, tracked per item. |

**Rules**: `stored_name` is computed by the receiver, never the sender. A partial item is never
exposed at its final path; it lives in staging until verification passes (FR-033).

---

## TransferQueue

Bucket: `queue`, an ordered list of Transfer identifiers.

| Field | Type | Rules |
|---|---|---|
| `entries` | []ULID | Order is user controlled (FR-035c). |
| `active_id` | ULID | At most one, ever (FR-035a). |

**Rules**: exactly zero or one transfer is `running` at any moment. Reordering never touches the
active entry. The queue is durable, so a restart resumes it or reports its entries as abandoned
with a reason (FR-035e). An `interrupted` transfer is moved behind the next runnable entry rather
than holding the head.

---

## RelaySession

Bucket: `relays`. The temporary role a computer takes between two phones.

| Field | Type | Rules |
|---|---|---|
| `transfer_id` | ULID | |
| `staging_path` | string | Inside the staging directory, never inside the receive folder (FR-055). |
| `bytes_staged` | uint64 | |
| `expires_at` | timestamp | `created_at` + 7 days, same window as any abandoned partial. |

**Rules**: both phones must hold an active pairing with the relaying computer; trust is never
transitive (FR-054). Staged bytes are deleted when the transfer reaches any terminal state, and
swept on expiry. The relaying user can list and cancel these (FR-056).

---

## HistoryEntry

Bucket: `history`, keyed by transfer ULID so that iteration is reverse chronological.

| Field | Type |
|---|---|
| `transfer_id` | ULID |
| `direction`, `peer_name`, `peer_device_id` | denormalized, so history survives device deletion |
| `item_count`, `total_bytes` | uint64 |
| `outcome` | `completed`, `failed`, `cancelled` |
| `failure_cause` | enum, nullable |
| `protection` | enum, so the user can see which transfers were encrypted |
| `ended_at` | timestamp |

**Rules**: user clearable in full (FR-039). Never contains file content, and never contains a
path outside the receive folder.

---

## Settings

Bucket: `settings`, a single record.

| Field | Type | Rules |
|---|---|---|
| `device_name` | string | Defaults to the machine hostname. |
| `receive_folder` | string | Defaults outside system folders. Changing it leaves previously received files untouched (FR-025 scenario 5). |
| `staging_folder` | string | Derived from `receive_folder` unless overridden. Never equal to it. |
| `language` | string | Nullable; null means negotiate from the browser. |
| `autostart` | bool | Defaults false; the user is offered it, never opted in silently (FR-049). |
| `bound_interfaces` | []string | Explicit. The server binds nowhere until started (FR-001). |
| `ca_certificate`, `ca_key_path` | string | Trusted mode only. Key stored with restrictive permissions, never in the repository or a log. |

---

## Retention sweeps

A single background sweep, run at startup and daily:

| Target | Window | Requirement |
|---|---|---|
| Abandoned partial transfers and their staging data | 7 days | FR-034 |
| Relay staging data | 7 days | Relay rules above |
| Pairings in `auto` mode | 1 year of inactivity | FR-016 |
| Pairings in `ask` mode | 30 days of inactivity | FR-016 |

Every sweep that removes something tells the user what went and why (FR-034).

---

## Invariants

1. No access decision ever reads a Device field. Authorization comes only from a non-revoked,
   non-expired Pairing.
2. At most one Transfer is `running` across the whole application.
3. No file is written to its final path before its checksum verifies.
4. No relayed byte is ever written inside the receive folder.
5. `stored_name` and `relative_path` always resolve inside the destination root after
   normalization, checked at write time rather than at request time.
6. No credential, key, or pairing code is persisted in plaintext or written to a log.
