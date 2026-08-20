# Implementation Plan: LAN File Transfer

**Branch**: `001-lan-file-transfer` | **Date**: 2026-08-20 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-lan-file-transfer/spec.md`

## Summary

A single Go binary runs on Linux and Windows, serving a Svelte web application over the local
network and advertising itself over mDNS. Phones reach it with a browser alone, through a short
address shown as a QR code. Devices pair with a one-time code, and a X25519 key agreement
authenticated by that code protects credentials and metadata even though the page is served over
plain HTTP, which browsers refuse to treat as a secure context.

File content streams through `io.Copy` at constant memory, resumes through HTTP range requests
downward and committed-offset uploads upward, and is verified with BLAKE2b. One transfer runs at
a time behind a durable queue held in bbolt. An optional trusted mode installs a locally issued
certificate authority on the phone, which restores a secure context and with it end-to-end
content encryption, service-worker-backed streamed writing, and reliable resume.

## Technical Context

**Language/Version**: Go 1.24 or newer for the binary; TypeScript 5 for the web application.

**Primary Dependencies**: Go standard library for HTTP, streaming, and TLS. `libp2p/zeroconf/v2`
for mDNS. `fyne.io/systray` for the tray icon. `go.etcd.io/bbolt` for durable state.
`golang.org/x/crypto` for X25519, HKDF, ChaCha20-Poly1305, and BLAKE2b. On the web side, Svelte
with Vite, `@noble/curves` and `@noble/ciphers` for cryptography that cannot use the browser's
native API. Nine direct dependencies in total, each justified in
[research.md](./research.md#dependency-budget).

**Storage**: A single bbolt file per installation, holding devices, pairings, the transfer queue,
and history. Received files go to a user-configured receive folder. Partial transfers live in a
staging directory beside it. No database server, no cgo.

**Testing**: `go test` with the standard library for unit and integration tests, Playwright for
browser end-to-end tests against real Chromium and WebKit, `axe-core` for accessibility, and a
network-boundary assertion that fails the suite if any socket leaves the local network.

**Target Platform**: Linux and Windows desktops, x86-64 and arm64. Android Chrome and iOS Safari,
latest two major versions, as browser clients.

**Project Type**: Desktop application serving an embedded web application, with a local network
protocol between instances.

**Performance Goals**: Sustain at least 60% of raw link throughput. Constant memory regardless of
file size, within 10% between a 10 MB and a 10 GB transfer. Under 1% average processor use and
under 100 MB memory while idle in the background. Devices appear within 5 seconds of joining.

**Constraints**: No outbound connection beyond the local network, ever. Single static binary with
no runtime to install. Identical behavior on Linux and Windows, enforced in CI. The mobile page
runs outside a secure browser context in the default mode, so neither `crypto.subtle` nor service
workers are available to it.

**Scale/Scope**: A household or a small office. Tens of devices, hundreds of queued items,
thousands of history entries. Files from 0 bytes to the free space of the destination.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Checked against constitution v2.0.0.

| Principle | Verdict | How the design satisfies it |
|---|---|---|
| I. Local-First, No Cloud | PASS | No dependency reaches the network at runtime. Fonts, icons, scripts, and translations are embedded with `go:embed`. A CI test asserts zero sockets leave the local network. No account, no telemetry. |
| II. Zero Install On Mobile | PASS | The default path is a browser and nothing else. Trusted mode asks the user to install a certificate authority, which constitution v2.0.1 permits explicitly as an opt-in exception, under four conditions the design meets: opt-in, never required to transfer a file, abandonable without breaking the default path, and never an application or store package. |
| III. No Size Limit | PASS | `io.Copy` streams with a fixed buffer. No size constant anywhere. Free space is checked before starting. A 10 GB fixture runs in CI with a memory assertion. |
| IV. Linux / Windows Parity | PASS | All platform specifics live in `internal/platform`. CI runs the full suite on both. Filename sanitization is driven by the destination's rules, not the sender's. |
| V. Security On Shared Networks | PASS | Explicit pairing, no anonymous access, filesystem confined to the receive folder with traversal tests, binding restricted to chosen interfaces, revocable sessions, secrets never logged. Both protection modes are implemented as the amended principle requires, with the honesty duty enforced by SC-016a. |
| VI. Effortless By Default | PASS | Scan, confirm, send. Three actions once paired. Discovery is automatic. No documentation needed for the default path. |
| VII. Open Source In The Open | PASS | MIT, English artifacts, reproducible builds from a tagged commit with published checksums, no secret in the repository. |

**Gate result: PASS.** One tension is recorded below rather than dismissed.

**Post-design re-check (after Phase 1)**: still PASS. The design added no dependency that reaches
the network, no size constant, and no platform-specific behavior outside `internal/platform`. The
three entries in Complexity Tracking are the complete set of deviations; nothing new appeared
while writing the data model or the contracts.

## Project Structure

### Documentation (this feature)

```text
specs/001-lan-file-transfer/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   ├── http-api.md      # Device-to-device and browser-to-server contract
│   ├── discovery.md     # mDNS service definition
│   └── pairing.md       # Pairing handshake and key derivation
├── checklists/
│   └── requirements.md
└── tasks.md             # Phase 2 output (/speckit-tasks, not created here)
```

### Source Code (repository root)

```text
cmd/
└── fastr/                    # Entry point: flags, wiring, lifecycle

internal/
├── app/                      # Orchestration: sessions, queue runner, event bus
├── config/                   # User settings, receive folder, per-OS locations
├── discovery/                # mDNS advertise and browse, manual address fallback
├── httpapi/                  # Routes, handlers, SSE stream, range and offset logic
├── pairing/                  # Codes, X25519 handshake, tokens, trust modes
├── transfer/                 # Streaming engine, resume, checksums, relay sessions
├── store/                    # bbolt buckets, migrations, retention sweeps
├── trust/                    # Local CA, certificate issuance, trusted-mode setup
├── platform/                 # Paths, autostart, tray, filename and free-space rules
└── i18n/                     # Catalogue loading and negotiation

web/
├── src/
│   ├── routes/               # Desktop view and mobile view
│   ├── lib/                  # Components: device list, queue, progress, pairing
│   ├── crypto/               # noble wrappers, key agreement, AEAD framing
│   ├── locales/              # en.json, fr.json
│   └── sw/                   # Service worker, trusted mode only
└── dist/                     # Built assets, embedded via go:embed

test/
├── integration/              # Two instances over a loopback network
├── e2e/                      # Playwright against Chromium and WebKit
└── testdata/                 # Small fixtures; large ones generated at run time

.github/workflows/            # CI: build, test, and release on Linux and Windows
```

**Structure Decision**: A single Go module with an embedded web application, rather than separate
backend and frontend projects. The web application is not deployed independently and has no life
outside the binary, so splitting it into its own project would add coordination cost with no
benefit. `internal/` prevents any of this becoming an accidental public API. `web/` is built by
Vite and embedded, so a release remains one file.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| The desktop browser pipes file bytes through the server to the phone, rather than the server reading the file itself | A browser never reveals a file's path, so the server cannot read the file the user dropped. A phone cannot accept an inbound connection either, so it must fetch. The two halves are joined by a rendezvous in `internal/transfer/pipe.go`, streaming with nothing written to disk. | Staging the file on the sender first would need 10 GB free to send a 10 GB file already on that disk, and take twice as long. A native desktop window with a real file picker would avoid the problem entirely, but contradicts the decision not to embed a webview and reintroduces the Linux system dependency research rejected. Accepted cost: the desktop tab must stay open while a transfer runs. |
| Two protection modes rather than one | The three principles I, V, and VI cannot hold at once for mobile reception of large files, as established in research. | A single mode forces one of: a security warning at onboarding, unencrypted content with no opt-out, or dropping browser-only access. All three were rejected by the project owner in favour of a default plus an opt-in. |
| A service worker exists but only in trusted mode | It is the only mechanism that can decrypt a stream while the browser writes it to disk. | Buffering a decrypted 10 GB file in memory is impossible on iOS. Chunked manual saving produces an unusable interface. |

**Resolved**: the tension between trusted mode and Principle II was settled by constitution
v2.0.1, which scopes Principle II to the default path and permits the trusted-mode exception under
four conditions. Both remaining entries above are genuine trade-offs accepted by the project
owner, not oversights.
