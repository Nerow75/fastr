---
description: "Task list for LAN File Transfer implementation"
---

# Tasks: LAN File Transfer

**Input**: Design documents from `/specs/001-lan-file-transfer/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

**Tests**: Included. Constitution v2.0.1 makes six test gates mandatory before merge, so test tasks are not optional here.

**Organization**: Tasks are grouped by user story so each can be implemented, tested, and shipped on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story the task belongs to
- Exact file paths are given in every task

## Path Conventions

Single Go module with an embedded web application, per [plan.md](./plan.md#project-structure):
`cmd/fastr/`, `internal/`, `web/src/`, `test/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Get a buildable, lintable, CI-covered skeleton on both operating systems.

- [X] T001 Initialize Go module and directory skeleton per plan.md in `go.mod`, `cmd/fastr/`, `internal/`, `test/`
- [X] T002 [P] Scaffold the Svelte and TypeScript application with Vite in `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`
- [X] T003 [P] Add `Makefile` with `build`, `build-all`, `test`, `test-e2e`, `test-a11y`, `test-security`, `test-network`, `test-large`, `fixture`, `capture` targets
- [X] T004 [P] Configure `golangci-lint` in `.golangci.yml` with a linter forbidding `net/http` clients to non-local hosts
- [X] T005 [P] Configure ESLint and Prettier for the web application in `web/eslint.config.js`
- [X] T006 Wire `go:embed` of `web/dist` into `web/embed.go`, with the serving handler in `internal/httpapi/assets.go`, failing the build when the bundle is missing. The embed lives in `web/` because a `go:embed` directive cannot reference a parent directory.
- [X] T007 [P] Add CI workflow running build and tests on Linux and Windows in `.github/workflows/ci.yml`
- [X] T008 [P] Add secret scanning to CI in `.github/workflows/ci.yml`, per constitution quality gate 6
- [X] T009 [P] Add cross-compilation release workflow producing checksummed binaries in `.github/workflows/release.yml`
- [X] T010 [P] Add `test/testdata/.gitignore` excluding `large/`, and a `make fixture` generator in `test/testdata/generate.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The infrastructure every user story sits on. Nothing below can be tested without it.

**⚠️ CRITICAL**: No user story work begins until this phase is complete.

### Platform and configuration

- [X] T011 [P] Define the platform abstraction interface in `internal/platform/platform.go` covering paths, free space, filename rules, autostart, tray
- [X] T012 [P] Implement the Linux platform in `internal/platform/platform_linux.go` using `statfs` and XDG paths
- [X] T013 [P] Implement the Windows platform in `internal/platform/platform_windows.go` using `GetDiskFreeSpaceEx`, `%LOCALAPPDATA%`, reserved-name and trailing-dot rules
- [X] T014 Implement settings load, save, and defaults in `internal/config/config.go`, defaulting the receive folder outside system folders
- [X] T015 [P] Write parity tests asserting identical behavior on both platforms in `test/integration/platform_parity_test.go`

### Persistence

- [X] T016 Implement the bbolt store, bucket layout, and open/close lifecycle in `internal/store/store.go` per data-model.md
- [X] T017 [P] Implement the `Device` and `Pairing` records with their validation rules in `internal/store/devices.go`
- [X] T018 [P] Implement the `Transfer`, `TransferItem`, and queue records in `internal/store/transfers.go`
- [X] T019 [P] Implement `HistoryEntry` and `RelaySession` records in `internal/store/history.go`
- [X] T020 Implement the schema version and migration hook in `internal/store/migrate.go`

### Observability and errors

- [X] T021 Implement structured logging with mandatory secret redaction in `internal/app/logging.go`, per FR-019
- [X] T022 [P] Define the machine-readable error catalogue and its translation keys in `internal/app/errors.go`, per the contract error format
- [X] T023 [P] Write a test asserting no key, token, or pairing code can reach a log line in `test/integration/log_hygiene_test.go`

### Internationalization

- [X] T024 [P] Implement catalogue loading and language negotiation in `internal/i18n/i18n.go`
- [X] T025 [P] Create the English and French catalogues in `web/src/locales/en.json` and `web/src/locales/fr.json`
- [X] T026 [P] Implement the client translation helper with English fallback in `web/src/lib/i18n.ts`, per FR-039d

### HTTP server and security envelope

- [X] T027 Implement the HTTP server, interface binding, and start/stop lifecycle in `internal/httpapi/server.go`, listening only when started per FR-001
- [X] T028 Implement routing and the error response writer in `internal/httpapi/router.go`
- [X] T029 Implement the ChaCha20-Poly1305 envelope with a monotonic counter in `internal/pairing/envelope.go`, per contracts/pairing.md
- [X] T030 [P] Implement the matching envelope on the client in `web/src/crypto/envelope.ts` using `@noble/ciphers`
- [X] T031 Implement the X25519 handshake, HKDF derivation, and code binding in `internal/pairing/handshake.go`
- [X] T032 [P] Implement the client handshake in `web/src/crypto/handshake.ts` using `@noble/curves`
- [X] T033 Implement pairing codes with single use, 3-minute expiry, 5-failure death, and rate limiting in `internal/pairing/code.go`, per FR-012 and FR-013
- [X] T034 Implement session credentials and the authorization middleware in `internal/pairing/session.go`, refusing every unpaired request per FR-011
- [X] T035 Implement the pairing endpoints in `internal/httpapi/pairing_handlers.go` per contracts/http-api.md
- [X] T036 [P] Write a replay and out-of-order counter rejection test in `test/integration/envelope_replay_test.go`
- [X] T037 [P] Write a test asserting an unpaired device is refused at every entry point in `test/integration/unpaired_access_test.go`

### Event stream and application shell

- [X] T038 Implement the event bus and the SSE endpoint with 4-per-second progress throttling in `internal/httpapi/events.go`
- [X] T039 [P] Implement the client event stream with reconnection in `web/src/lib/events.ts`
- [X] T040 Implement the application shell, routing between desktop and mobile views in `web/src/routes/App.svelte`
- [X] T041 [P] Establish the accessible base layer, focus ring, landmarks, and live region in `web/src/lib/a11y.ts` and `web/src/app.css`
- [X] T042a Implement the background lifecycle and single-instance guard in `internal/platform/tray.go`, `internal/platform/lock_linux.go`, `internal/platform/lock_windows.go`, and `cmd/fastr/main.go`, per FR-048 and FR-050
- [ ] T042b Attach an actual system tray icon and menu, wiring `platform.TrayMenu`. Deliberately not done yet: research item 3 (now T139) requires confirming the Linux tray dependency and its graceful degradation, and neither can be verified without a desktop session. The lifecycle it drives is already in place, so this is the icon and menu only.
- [X] T043 [P] Implement the autostart toggle, off by default, in `internal/platform/platform_linux.go` and `internal/platform/platform_windows.go`, per FR-049. Kept in the platform files rather than separate ones: it is three functions per OS and shares the config-directory logic already there.
- [X] T044 [P] Write the network boundary test asserting zero sockets leave the local network in `test/integration/network_boundary_test.go`, per Principle I

**Checkpoint**: A binary starts, serves an authenticated shell, pairs a device, and streams events. User stories can begin.

---

## Phase 3: User Story 1 - Large file from computer to phone (Priority: P1) 🎯 MVP

**Goal**: Scan a QR code, pair, drag a file of any size onto the desktop, and open it on the phone.

**Independent Test**: With one computer and one phone, move a 10 GB file end to end and verify the checksum matches, with memory flat throughout.

### Tests for User Story 1

- [X] T045 [P] [US1] Contract test for the transfer declaration and content endpoints in `test/integration/transfer_contract_test.go`
- [X] T046 [P] [US1] Integration test for the full pair-then-send journey, in `test/integration/transfer_test.go` rather than the filename the task named. It sits with the other transfer tests, which share a harness and a set of helpers.
- [X] T047a [P] [US1] Memory-flatness test at the engine level, comparing 1 MB and 512 MB through the same copy path, in `internal/transfer/transfer_test.go`. Runs in the normal suite.
- [ ] T047b [P] [US1] The full 10 GB variant of SC-003, end to end through the HTTP path, in `test/integration/large_transfer_test.go` behind the `large` build tag. Needs the `make fixture` generator and a nightly CI job; the property is already proven at the engine level, this is the number the success criterion actually names.
- [X] T048a [P] [US1] Path traversal rejection at the resolution level, in `internal/transfer/transfer_test.go`: nine hostile inputs against both platforms' rule sets, asserting nothing escapes the receive folder.
- [X] T048b [P] [US1] The same at the HTTP level, in `test/integration/filename_rules_test.go` rather than a file of its own, because the surface it attacks arrived with User Story 2 and the hostile inputs share that file's helpers. Nine name-and-relative-path pairs are declared, uploaded, and completed through the real endpoints; each must either be refused at declaration or land inside the receive folder, and a sibling directory is asserted empty either way.
- [X] T049 [P] [US1] End-to-end browser test of the QR-to-received-file journey, in `web/tests/e2e/us1_first_transfer.spec.ts`. It drives the real binary through two browser contexts — loopback is the computer, the machine's own LAN address is the phone — and covers the whole journey: the invitation panel's address and QR, pairing the computer's own browser, a fresh code for the phone, a 6 MB file dropped on the desktop, and the phone's download manager writing it out byte for byte. A second case asserts an unpaired phone is offered the pairing form and nothing else, least of all the pairing code.

### Implementation for User Story 1

- [X] T050 [P] [US1] Implement connection info and QR code generation in `internal/httpapi/connect.go`
- [X] T051 [P] [US1] Build the pairing screen with code entry in `web/src/lib/PairingScreen.svelte`
- [X] T050b [US1] Serve the host's invitation — the live pairing code, the reachable addresses, and the URL to encode — from `internal/httpapi/invitation.go`, restricted to loopback. **FR-002 is not met without this.** T050 generates a QR from a URL the caller supplies, and T051 accepts a typed code, but nothing tells the desktop page which URL to encode or which code to display: the code only ever reaches standard output. Issuing on demand also removes the dead end where the 3-minute expiry can be escaped only by restarting the binary.
- [X] T051c [US1] Grant the host's own page a session over loopback, in `internal/httpapi/host_session.go` and `web/src/lib/session.ts`, removing the step where the computer paired with itself. Found by running the application: a user who scanned the QR paired their phone, saw the computer's screen unchanged, and had no way to guess the page was waiting to be told about itself — so sending *from* the computer was unreachable. **This is a change to the pairing model and is deliberate.** Principle V accepts "a one-time code **or** a confirmation on the host device", and reaching loopback is what being on the host device means; it is the same boundary `/api/pair/pending/{id}/approve` already rests on. The honest cost: a local process can now obtain a credential without knowing a code, which it could not before. It gains little — a process running as this user can already read every file fastr could send, and write into the receive folder — but the trade is real and is recorded here rather than buried. `test/integration/host_session_test.go` pins the loopback restriction, including that a paired device holding a valid credential is still refused.
- [X] T051d [US1] Let a page survive a reload and a second tab, in `web/src/lib/session.ts`, `web/src/crypto/envelope.ts`, and `internal/pairing/envelope.go`. **A pre-existing defect, found by running the application, not by any change made here.** The envelope refuses every counter it has already seen and the server keeps one high-water mark per device, while each page keeps its counter in memory. So a page that reloaded started again at one and had everything it sent refused as a replay, and two tabs of one origin locked each other out — the busy one racing ahead, the quiet one permanently behind. On a phone, reloading is among the most ordinary things that can happen. A page now claims a block of counters at load, and a request refused as a replay is retried once from a fresh block, so pages leapfrog instead of deadlocking. Nothing is weakened: counters still only increase, which is the property the check rests on. Pinned by `test/integration/session_resume_test.go` and by the two-tab case in `web/tests/e2e/us2_phone_upload.spec.ts`, which was checked to fail without the fix.
- [X] T051e [US1] Report which devices can actually be reached, in `internal/httpapi/events.go`, `pairing_handlers.go`, and `web/src/lib/SendPanel.svelte`, per FR-004 and the derived `reachable` field in data-model.md. A pairing lasts a year, so the device list holds every phone ever connected; offering one as a destination with nothing to say it is closed produced a transfer that sat at nothing and never explained itself. Holding an event stream open is what makes a device reachable, and opening or closing one now publishes `device_appeared` / `device_lost` — the event types the contract already defined — so every other page's list stays true rather than being a snapshot from when it loaded.
- [X] T051b [US1] Build the desktop invitation panel in `web/src/lib/ConnectionInvitation.svelte`: the local address, the pairing code, and the QR image from `/qr`, per FR-002. Found by running the application: a first connection currently requires reading a terminal, which Principle VI ("a first successful transfer reachable in under two minutes, without reading documentation") does not survive.
- [X] T052 [US1] Build the desktop approval screen for pending devices in `web/src/lib/PendingDevices.svelte`, per FR-010
- [X] T053 [US1] Implement free space checking before a transfer starts in `internal/transfer/space.go`, per FR-028
- [X] T054 [US1] Implement the constant-memory streaming engine in `internal/transfer/stream.go` using a fixed buffer, per FR-029
- [X] T055 [US1] Implement BLAKE2b checksumming during the copy in `internal/transfer/checksum.go`, per FR-032
- [X] T056 [US1] Implement destination filename sanitization and collision resolution in `internal/transfer/naming.go`, per FR-024 and FR-025
- [X] T057 [US1] Implement the staging-then-verify-then-move write path in `internal/transfer/writer.go`, per FR-033
- [X] T058 [US1] Implement the transfer declaration endpoint in `internal/httpapi/transfer_handlers.go`
- [X] T059 [US1] Implement the downward content endpoint with range support in `internal/httpapi/content_download.go`
- [X] T060 [US1] Implement the transfer orchestration service in `internal/app/transfers.go`
- [X] T061 [P] [US1] Build the desktop send view with drag and drop and multi-selection in `web/src/lib/SendPanel.svelte`, per FR-020 and FR-021
- [X] T062 [P] [US1] Build the progress view with speed and time remaining in `web/src/lib/TransferProgress.svelte`, per FR-030
- [X] T063 [US1] Build the mobile receive and save view in `web/src/routes/Mobile.svelte`, per FR-026
- [X] T064 [US1] Display the simple-mode protection statement at pairing and during transfers in `web/src/lib/ProtectionNotice.svelte`, per FR-047 and SC-016a
- [X] T065 [US1] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: User Story 1 is fully functional and independently testable. This is the MVP.

**What the browser harness found**

The harness (`web/playwright.config.ts`, `web/tests/e2e/fixtures.ts`) starts the real binary with its state redirected into a temporary tree and drives it through two browser contexts. Adding it turned up three defects that every Go test passed straight over, because all three live above the wire:

1. **Nothing could ever be received on the phone.** `TransferProgress.svelte` offered the Save button only once the transfer reached `completed`, but a transfer completes *because* the receiver fetches the content, and that fetch is what the button starts. The two waited for each other. User Story 1 had never worked in a browser.
2. **The desktop's supply request carried no credential.** `web/src/lib/transfers.ts` posted the file body without an `Authorization` header, so the server refused every supply with `401`, and the failure was swallowed by a bare `if (!response.ok) return;`. The receiver simply waited out the 30-second rendezvous and its download was cancelled with nothing said anywhere.
3. **The invitation panel kept showing a spent code.** Pairing the computer's own browser consumes the displayed code; the panel went on showing it, and typing those digits into a phone met "already used" with no way forward.

The first two are why the tests are worth their cost: each is invisible to an integration test that speaks HTTP directly, and neither would have surfaced without a browser clicking the buttons in order. The third is now asserted directly in `us1_first_transfer.spec.ts`.

They also exposed a fourth, left open as **T087b**: a page learns of a transfer only from the event that announced it, so anything declared while it was not listening stays invisible to it.

---

## Phase 4: User Story 2 - Files from phone to computer (Priority: P2)

**Goal**: Pick files on the phone and send them to a chosen computer, which writes them to its receive folder.

**Independent Test**: With a paired phone, send a video to the computer and verify it lands intact in the receive folder with its original name.

### Tests for User Story 2

- [X] T066 [P] [US2] Contract test for the upward content and offset endpoints in `test/integration/upload_contract_test.go`. Covers the committed-offset reply, the offset endpoint, a chunk at the wrong offset returning `409 offset_mismatch` with the server's value in `params`, a checksum mismatch returning `422` with nothing left in either folder, a third device refused, and a body longer than the declared size being bounded rather than written.
- [X] T067 [P] [US2] Integration test for the phone-to-computer journey in `test/integration/us2_send_to_computer_test.go`: a 3 MB file in 256 KB chunks arriving intact under its original name, progress visible midway, resume from the committed offset, several files in one send, a folder send keeping its structure, cancellation leaving nothing behind, and a full disk refused before a byte moves.
- [X] T068 [P] [US2] Collision and sanitization test in `test/integration/filename_rules_test.go`. Windows reserved names, colons, wildcards, trailing dots and spaces are exercised at the HTTP level by setting the destination rule set explicitly, so a Linux runner proves the Windows behaviour and vice versa. Also asserts the same names survive unchanged under the Linux rule set, which is what stops sanitization becoming corruption on the platform that accepts them.
- [X] T069 [P] [US2] End-to-end browser test of selecting and sending from the phone, in `web/tests/e2e/us2_phone_upload.spec.ts`: a 9 MB file picked on the phone, listed with its name and size before sending, and arriving intact in the receive folder; and a second send of the same name leaving the first file untouched.

### Implementation for User Story 2

- [X] T070 [US2] Implement the committed-offset upload endpoint in `internal/httpapi/content_upload.go`, per contracts/http-api.md. Holds both `POST .../content?offset=N` and `GET .../offset`. The upload replies in the clear like the other bulk content routes; the offset endpoint is sealed, being control plane.
- [X] T071 [US2] Implement durable offset commitment with fsync in `internal/transfer/offsets.go`. A `Sink` pairs the open staging file with the rolling BLAKE2b state, because the digest of a file arriving over many requests cannot be stored between them and rehashing the prefix each time would make a linear transfer quadratic. State is rebuilt by one pass when the process restarted mid-transfer. A staging file longer than the acknowledged offset is truncated back to it: those bytes were never promised.
- [X] T072 [US2] Implement the item completion and verification endpoint in `internal/httpapi/transfer_complete.go`. Verification is the incoming case only; a piped transfer's bytes never touched this disk, so there is nothing here to check and the receiving browser checks its own copy.
- [X] T073 [P] [US2] Build the mobile file picker and selection list with names and sizes in `web/src/lib/MobilePicker.svelte`, with a camera entry point beside the file picker and live progress reported from the committed offset.
- [X] T074 [P] [US2] Implement chunked client upload from a file offset in `web/src/lib/upload.ts`. 4 MB chunks, one held at a time, with the digest computed while reading. A `409 offset_mismatch` rewinds to the server's offset and rebuilds the hash rather than writing a hole.
- [X] T075 [US2] Implement receive folder configuration and its change semantics in `internal/config/receive_folder.go`, per FR-023. Changing it moves nothing on disk and says so in its result. A folder that would contain the staging directory is refused rather than accommodated by relocating staging, and system folders and filesystem roots are refused outright.
- [X] T076 [US2] Add a desktop notification on completed incoming transfers in `internal/platform/notify_linux.go` and `internal/platform/notify_windows.go`, with the shared contract in `internal/platform/notify.go` and the translated wording in `internal/app/announce.go`. `notify-send` on Linux and a WinRT toast through PowerShell on Windows, both run as commands so the dependency budget is untouched, and both degrading to a logged no-op when the mechanism is absent.
- [X] T077 [US2] Add translations for every string introduced by this story. The catalogues live in `internal/i18n/locales/`, not `web/src/locales/` as this task said: there is one copy, embedded for the native surfaces and imported from there by the web application, so a translator edits one file and both follow.

**Checkpoint**: Both directions work independently.

**Notes on this phase**

Three pieces of work were needed that no task named:

- **A stable identity for this instance.** `deviceIdentity` in `cmd/fastr/main.go` returned a fresh ULID on the run that created the record and the literal string `"self"` on every run after, so a phone that paired on day one addressed a computer that answered to a different identifier on day two. It now lives in the store's `meta` bucket, minted once, with the device record keyed by it. Schema version 2 drops the old reserved record. Nothing in User Story 2 works without this, because a phone has to name the computer as the target.
- **`internal/app/incoming.go`**, the orchestration between the endpoint, the sink, and destination naming. It is the third content path and did not fit either existing file.
- **Checksum vectors** in `test/testdata/crypto-vectors.json`, verified by both `test/integration/crypto_vectors_test.go` and `web/scripts/verify-crypto.ts`. The phone hashes while reading and the computer hashes while writing; if the two BLAKE2b implementations disagreed, every upload from a real phone would fail verification with nothing to point at. Six cases, fed chunk by chunk exactly as `upload.ts` feeds them.

---

## Phase 5: User Story 3 - Surviving an interruption (Priority: P3)

**Goal**: A dropped network, a locked screen, or a sleep never restarts a transfer from zero.

**Independent Test**: Cut the network at the halfway point of a 10 GB transfer, restore it, and verify under 1% of delivered data is re-sent.

### Tests for User Story 3

- [ ] T078 [P] [US3] Integration test cutting the network mid-transfer and measuring re-sent bytes in `test/integration/us3_resume_test.go`, per SC-005
- [ ] T079 [P] [US3] Integration test asserting a checksum mismatch fails the transfer and leaves no usable file in `test/integration/corruption_test.go`
- [ ] T080 [P] [US3] Integration test for the 7-day partial data sweep in `test/integration/retention_sweep_test.go`, per FR-034
- [ ] T081 [P] [US3] End-to-end test of a backgrounded mobile browser resuming in `test/e2e/us3_resume.spec.ts`

### Implementation for User Story 3

- [ ] T082 [US3] Implement the interrupted state and automatic reconnection in `internal/transfer/resume.go`, per FR-031
- [ ] T083 [US3] Implement offset negotiation and the `offset_mismatch` response in `internal/httpapi/content_upload.go`
- [ ] T084 [P] [US3] Implement client-side resume with backoff in `web/src/lib/resume.ts`
- [ ] T085 [US3] Implement the retention sweep for partial data and expired pairings in `internal/store/sweep.go`, per data-model.md
- [ ] T086 [US3] Report sweep removals to the user through the event stream in `internal/app/transfers.go`
- [ ] T087 [P] [US3] Surface interrupted and resumed states distinctly in `web/src/lib/TransferProgress.svelte`, per FR-038
- [ ] T087b [US3] Reconcile a client's view of its transfers when its event stream connects, in `web/src/routes/App.svelte` and whatever endpoint it needs. Found by the browser tests: a page learns about a transfer only from the `transfer_queued` event, so anything declared while it was not listening is invisible to it forever. A phone that reloads mid-transfer, or that is sent a file in the seconds after pairing, sees nothing at all — no progress, no Save button, no way back. `web/tests/e2e/fixtures.ts` waits for the stream before sending precisely to step around this, and that wait should be deleted when this lands.
- [ ] T088 [US3] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: Large transfers survive real-world network conditions.

---

## Phase 6: User Story 4 - Several devices on the network (Priority: P4)

**Goal**: Every device on the network appears automatically, with a recognizable name, and can be chosen as a target.

**Independent Test**: Run two instances and two phones, and verify each lists the others within 5 seconds with distinguishable names.

### Tests for User Story 4

- [ ] T089 [P] [US4] Integration test for discovery appearance within 5 seconds in `test/integration/us4_discovery_test.go`, per SC-011
- [ ] T090 [P] [US4] Integration test for duplicate names remaining distinguishable in `test/integration/duplicate_names_test.go`, per FR-005
- [ ] T091 [P] [US4] Integration test for the manual address fallback in `test/integration/manual_address_test.go`, per FR-006

### Implementation for User Story 4

- [ ] T092 [US4] Implement mDNS advertising of `_fastr._tcp` with the TXT record in `internal/discovery/advertise.go`, per contracts/discovery.md
- [ ] T093 [US4] Implement continuous browsing and the device cache in `internal/discovery/browse.go`
- [ ] T094 [US4] Implement reachability confirmation through `/connect` rather than the record alone in `internal/discovery/reachability.go`
- [ ] T095 [US4] Implement manual address entry producing an identical device record in `internal/discovery/manual.go`
- [ ] T096 [US4] Implement graceful degradation when multicast is unavailable in `internal/discovery/browse.go`
- [ ] T097 [P] [US4] Build the device list with reachability, pairing state, and name disambiguation in `web/src/lib/DeviceList.svelte`
- [ ] T098 [US4] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: Device selection works across a real multi-device network.

---

## Phase 7: User Story 5 - Managing devices and transfers (Priority: P5)

**Goal**: See what is running, cancel it, review what happened, and revoke a device.

**Independent Test**: Queue five transfers, verify exactly one runs, cancel it, revoke a pairing, and confirm the device loses access immediately.

### Tests for User Story 5

- [ ] T099 [P] [US5] Integration test asserting exactly one transfer is ever active in `test/integration/us5_queue_test.go`, per SC-021
- [ ] T100 [P] [US5] Integration test for queue durability across a restart in `test/integration/queue_persistence_test.go`, per FR-035e
- [ ] T101 [P] [US5] Integration test for immediate revocation in `test/integration/revocation_test.go`, per FR-015
- [ ] T102 [P] [US5] Integration test for trust mode defaults, changes, and expiry recomputation in `test/integration/trust_mode_test.go`, per FR-016 and FR-016b
- [ ] T103 [P] [US5] End-to-end test of queue reordering and cancellation in `test/e2e/us5_queue.spec.ts`

### Implementation for User Story 5

- [ ] T104 [US5] Implement the sequential queue runner enforcing one active transfer in `internal/app/queue.go`, per FR-035a
- [ ] T105 [US5] Implement reorder, remove, and clear operations in `internal/httpapi/queue_handlers.go`, per FR-035c
- [ ] T106 [US5] Implement interrupted transfers yielding their place rather than blocking in `internal/app/queue.go`, per FR-035d
- [ ] T107 [US5] Implement bilateral cancellation in `internal/app/transfers.go`, per FR-035
- [ ] T108 [US5] Implement per-device trust mode and the acceptance decision in `internal/pairing/trust.go`, per FR-016a to FR-016d
- [ ] T109 [US5] Implement the bounded acceptance window for ask-mode transfers in `internal/app/transfers.go`, per FR-016d
- [ ] T110 [US5] Implement history recording and clearing in `internal/app/history.go`, per FR-037 and FR-039
- [ ] T111 [P] [US5] Build the queue view with reordering in `web/src/lib/QueueView.svelte`
- [ ] T112 [P] [US5] Build the paired devices view with trust mode and revocation in `web/src/lib/DeviceSettings.svelte`, per FR-016c
- [ ] T113 [P] [US5] Build the history view with outcomes, causes, and protection mode in `web/src/lib/HistoryView.svelte`
- [ ] T114 [US5] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: The tool is controllable and trustworthy for daily use.

---

## Phase 8: User Story 6 - Phone to phone through a relay (Priority: P6)

**Goal**: Two phones paired to the same computer exchange files, with the computer relaying and keeping nothing.

**Independent Test**: Send a file between two phones, then verify staging is empty and nothing appeared in the receive folder.

### Tests for User Story 6

- [ ] T115 [P] [US6] Integration test for a relayed transfer leaving zero bytes behind in `test/integration/us6_relay_test.go`, per SC-019
- [ ] T116 [P] [US6] Integration test asserting trust is not transitive between phones in `test/integration/relay_authorization_test.go`, per FR-054
- [ ] T117 [P] [US6] Integration test for relay failure when the computer becomes unavailable in `test/integration/relay_failure_test.go`, per FR-057

### Implementation for User Story 6

- [ ] T118 [US6] Implement relay session staging outside the receive folder in `internal/transfer/relay.go`, per FR-055
- [ ] T119 [US6] Implement per-hop authorization requiring both pairings in `internal/pairing/trust.go`, per FR-054
- [ ] T120 [US6] Implement relay staging space checks before starting in `internal/transfer/space.go`, per FR-058
- [ ] T121 [US6] Implement relay cleanup on every terminal state and on sweep in `internal/store/sweep.go`
- [ ] T122 [P] [US6] Build the relay visibility and cancellation view in `web/src/lib/RelayView.svelte`, per FR-056
- [ ] T123 [US6] Allow phones to be selected as targets in `web/src/lib/DeviceList.svelte`
- [ ] T124 [US6] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: All six user stories are independently functional.

---

## Phase 9: Trusted Mode

**Purpose**: The opt-in path to full content encryption, per FR-047a to FR-047e and constitution Principle V.

- [ ] T125 [P] Integration test asserting content is unreadable on the wire in trusted mode in `test/integration/trusted_capture_test.go`, per SC-016
- [ ] T126 [P] Integration test asserting `require_trusted` refuses plain connections in `test/integration/require_trusted_test.go`, per FR-047c
- [ ] T127 [P] Integration test asserting an abandoned setup leaves the simple pairing working in `test/integration/trust_abandon_test.go`, per FR-047d
- [ ] T128 Implement local certificate authority generation and storage with restrictive permissions in `internal/trust/ca.go`
- [ ] T129 Implement certificate issuance for the current network addresses in `internal/trust/issue.go`
- [ ] T130 Implement the TLS listener alongside the plain one in `internal/httpapi/server.go`
- [ ] T131 Implement the trusted mode endpoints in `internal/httpapi/trust_handlers.go`, per contracts/http-api.md
- [ ] T132 Implement downgrade refusal and explicit confirmation in `internal/app/transfers.go`, per FR-047e
- [ ] T133 [P] Implement the service worker performing streamed decryption in `web/src/sw/decrypt.ts`, registered in trusted mode only
- [ ] T134 [P] Build the guided setup walkthrough covering iOS full trust and Android user CA in `web/src/lib/TrustedSetup.svelte`
- [ ] T135 Verify both setup flows on real Android and iOS hardware and record the findings in `specs/001-lan-file-transfer/research.md`, per research item 4
- [ ] T136 Add translations for every string introduced by trusted mode to `web/src/locales/en.json` and `web/src/locales/fr.json`

---

## Phase 10: Polish & Cross-Cutting Concerns

- [ ] T137 Decide the pairing hardening question, implementing a PAKE or documenting the accepted risk, in `internal/pairing/handshake.go`, per research item 1
- [ ] T138 Confirm the mDNS library choice or switch to the identified fallback in `internal/discovery/browse.go`, per research item 2
- [ ] T139 Ensure a missing Linux tray dependency degrades to a headless background service in `internal/platform/tray.go`, per research item 3
- [ ] T140 [P] Run the full accessibility gate and fix every violation, in `test/e2e/a11y.spec.ts`, per FR-039f to FR-039j
- [ ] T141 [P] Verify progress announcements fire only at meaningful moments in `test/e2e/a11y_announcements.spec.ts`, per FR-039i
- [ ] T142 [P] Audit for untranslated strings and raw identifiers across the interface in `test/e2e/i18n_coverage.spec.ts`, per SC-022
- [ ] T143 [P] Audit that no screen describes simple mode as secure without qualification in `test/e2e/protection_honesty.spec.ts`, per SC-016a
- [ ] T144 Measure throughput against raw link speed and close any gap below 60% in `test/integration/throughput_test.go`, per SC-004
- [ ] T145 Measure idle background resource use against the budget in `test/integration/idle_budget_test.go`, per SC-018
- [ ] T146 [P] Verify every error in the catalogue names a cause and a corrective action in `test/integration/error_catalogue_test.go`, per SC-014
- [ ] T147 [P] Write the user documentation covering both protection modes in `docs/`
- [ ] T148 Update `README.md` with real install and run instructions and remove the pre-alpha banner
- [ ] T149 Run the full [quickstart.md](./quickstart.md) validation on Linux and Windows against real Android and iOS devices
- [ ] T150 Verify the release pipeline in `.github/workflows/release.yml` produces reproducible checksummed binaries from a tagged commit, per Principle VII

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)**: no dependencies, start immediately
- **Foundational (Phase 2)**: depends on Setup, **blocks every user story**
- **User Stories (Phases 3 to 8)**: all depend on Foundational, then proceed in priority order or in parallel
- **Trusted Mode (Phase 9)**: depends on Foundational and US1; independent of US2 to US6
- **Polish (Phase 10)**: depends on the stories being delivered

### User story dependencies

- **US1 (P1)**: depends only on Foundational. The MVP.
- **US2 (P2)**: depends only on Foundational. Reuses the streaming engine from US1 but is separately testable.
- **US3 (P3)**: depends on at least one transfer direction existing, so on US1 or US2.
- **US4 (P4)**: depends only on Foundational. Fully independent.
- **US5 (P5)**: depends on transfers existing, so on US1.
- **US6 (P6)**: depends on US1, US2, and US5, since a relay is both directions plus queue management.

### Within each story

- Tests are written first and must fail before implementation
- Store records before services, services before endpoints, endpoints before views
- Translations land with the story that introduces the strings, never in a later batch

### Parallel opportunities

- All of Phase 1 except T001 and T006 runs in parallel
- Platform implementations T012 and T013 run in parallel, as do all store records T017 to T019
- Go and TypeScript sides of the cryptography pair off: T029 with T030, T031 with T032
- Every test task inside a story runs in parallel with the others
- US1, US2, and US4 can be built simultaneously by different people once Foundational is done

---

## Parallel Example: User Story 1

```bash
# All tests for User Story 1 at once:
Task: "Contract test for the transfer declaration and content endpoints in test/integration/transfer_contract_test.go"
Task: "Integration test for the full pair-then-send journey in test/integration/us1_send_to_phone_test.go"
Task: "Memory-flatness test comparing a 10 MB and a 10 GB transfer in test/integration/large_transfer_test.go"
Task: "Path traversal rejection test in test/integration/path_traversal_test.go"
Task: "End-to-end browser test of the QR-to-received-file journey in test/e2e/us1_first_transfer.spec.ts"

# Independent views once the engine exists:
Task: "Build the desktop send view with drag and drop in web/src/lib/SendPanel.svelte"
Task: "Build the progress view with speed and time remaining in web/src/lib/TransferProgress.svelte"
```

---

## Implementation Strategy

### MVP first

1. Phase 1: Setup
2. Phase 2: Foundational, which is substantial here because pairing and the security envelope are prerequisites for everything
3. Phase 3: User Story 1
4. **Stop and validate**: run scenario 1 of quickstart.md with a real phone
5. That binary already replaces the Discord and Drive workaround for the original problem

### Incremental delivery

Each story is a shippable increment: US2 adds the return direction, US3 makes large transfers reliable, US4 makes it a multi-device tool, US5 makes it controllable, US6 serves visitors. Trusted mode can land at any point after US1 and is the right moment to invite security review, since the repository is public.

### Notes

- `[P]` means a different file with no dependency on incomplete work
- Commit after each task or coherent group
- Every story checkpoint is a valid place to stop and ship
- The four research items in Phase 10 are decisions, not chores; T137 in particular should be settled before the first tagged release rather than after
