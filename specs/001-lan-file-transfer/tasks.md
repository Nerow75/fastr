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
- [X] T051e [US1] Report which devices can actually be reached, in `internal/httpapi/events.go`, `pairing_handlers.go`, and `web/src/lib/SendPanel.svelte`, per FR-004 and the derived `reachable` field in data-model.md. A pairing lasts a year, so the device list holds every phone ever connected; offering one as a destination with nothing to say it is closed produced a transfer that sat at nothing and never explained itself. Holding an event stream open is what makes a device reachable, and opening or closing one now publishes `device_appeared` / `device_lost` — the event types the contract already defined — so every other page's list stays true rather than being a snapshot from when it loaded. **Follow-up, from the first real computer-to-phone run:** reporting reachability was not enough while the panel still *preselected* the only paired device. A select shows its first option, so "Phone — not connected" was the resting state of the send panel for anyone whose phone had never opened its page. Only a reachable device is chosen for the user now, and a placeholder asks for a choice otherwise.
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

- [X] T078 [P] [US3] Integration test cutting the network mid-transfer and measuring re-sent bytes in `test/integration/us3_resume_test.go`, per SC-005. The cut is a real one: a raw connection, a half close partway through the body, and the server meeting an EOF where the rest should have been. A politely stopped request would have proved nothing about the bytes in the air when a phone leaves Wi-Fi, which is the only interesting part. Re-sent bytes come out at zero, because a short copy is still fsynced and still committed.
- [X] T079 [P] [US3] Integration test asserting a checksum mismatch fails the transfer and leaves no usable file in `test/integration/corruption_test.go`. **Found a defect**: a failed transfer could be completed again, and the second attempt re-opened a sink and re-created the staging file the failure had just deleted. `CommitItem` now refuses a terminal transfer, and `discardStaging` deletes by name as well as by open sink, so partial data with nothing in memory pointing at it is still reachable.
- [X] T080 [P] [US3] Integration test for the 7-day partial data sweep in `test/integration/retention_sweep_test.go`, per FR-034. Writing it exposed that `app.Declare` stamped `queued_at` from the wall clock rather than the store's, so a moved clock produced a record whose own timestamps disagreed; the test for "slow is not abandoned" passed with and without its fix until that was corrected.
- [X] T081 [P] [US3] End-to-end test of a backgrounded mobile browser resuming, in `web/tests/e2e/us3_resume.spec.ts` (under `web/` for the reason given at T049). The interruption is real at the network layer: the first chunk is delivered and every one after it is refused, then the page is reloaded — which is what iOS does to a backgrounded tab, taking the File objects with it. Checked to fail without the resume path.

### Implementation for User Story 3

- [X] T082 [US3] Implement the interrupted state and automatic reconnection in `internal/transfer/resume.go`, per FR-031. Split across two files, because `internal/transfer` deliberately does not know about `internal/store`: `resume.go` holds the classification (`transfer.Classify`), and the restart recovery it feeds lives in `app.Transfers.Recover`. Both halves answer the same question. Before this, *every* error interrupted a transfer, including a full disk, which left a sender retrying forever into a destination that could never accept it. A `running` transfer found at startup is a lie a dead process left holding the single active slot, and is now recovered to `interrupted` rather than blocking the queue behind a ghost.
- [X] T083 [US3] Implement offset negotiation and the `offset_mismatch` response in `internal/httpapi/content_upload.go`. Landed with User Story 2, since the upward path could not work without it; `TestUploadRefusesAChunkAtTheWrongOffset` is the contract test.
- [X] T084 [P] [US3] Implement client-side resume with backoff in `web/src/lib/resume.ts`. Nothing replays a request: the upload asks for the committed offset before sending anything, so **retrying an item is resuming it**, which is why the whole item can be wrapped rather than each chunk. What is worth retrying is a short list, and the default when it is unclear is "interrupted", because a transfer wrongly retried costs a round trip and one wrongly failed costs the user their partial upload. A wait ends early when the `online` event fires, so a phone coming out of a lift does not sit through the rest of its backoff.
- [X] T085 [US3] Implement the retention sweep for partial data and expired pairings in `internal/store/sweep.go`, per data-model.md. Runs at startup and daily. Abandoned transfers are *failed with a cause and recorded in history* before their records go, because a transfer that simply vanishes leaves the user unable to tell a sweep from a loss. Added `last_progress_at` to Transfer (data-model.md amended): without it the sweep cannot tell a transfer nobody has touched in a week from a 10 GB file crossing a bad link, and User Story 3 exists for the second one.
- [X] T086 [US3] Report sweep removals to the user through the event stream in `internal/app/transfers.go`, as `sweep_removed`, carrying what went rather than a count. Also a native notification, because the sweep runs at startup, which is exactly when no page is open to hear the event. Staging files are released before the records are removed: Windows will not delete a file anything holds open, and the reverse order would leave orphaned bytes with nothing pointing at them.
- [X] T087 [P] [US3] Surface interrupted and resumed states distinctly in `web/src/lib/TransferProgress.svelte` and `MobilePicker.svelte`, per FR-038. Interrupted says how much has already arrived and that it is kept for 7 days, because the state word alone reads like a failure and people start over. A retry in progress shows a countdown rather than a spinner: a bar that has simply stopped moving is indistinguishable from one that has broken. Failure causes now resolve to the `error.*` messages, which carry a corrective action — they previously rendered the bare code, which is what FR-038 forbids.
- [X] T087b [US3] Reconcile a client's view of its transfers when its event stream connects, in `web/src/routes/App.svelte` against `GET /api/queue` (`internal/httpapi/queue_handlers.go`). No new route was needed: a transfer that is neither terminal nor forgotten is by definition either the active one or a waiting entry, so the queue *is* the reconciliation set — scoped, as everywhere else, to what the caller is a party to. The wait in `web/tests/e2e/fixtures.ts` is gone, which is how the suite proves this landed. **Removing it exposed a defect in the harness itself**: `pair()` waited for the region named "Connect this phone" to disappear, but the pairing screen merely swaps that heading to "Waiting for approval" while it polls, so the check passed the moment the code was submitted and long before a credential existed. The stream wait had been covering that gap. It now waits for the session itself.
- [X] T088 [US3] Add translations for every string introduced by this story. The catalogues live at `internal/i18n/locales/` rather than `web/src/locales/` — the Go side needs them for native notifications, and one file per language serves both surfaces (T024). Added the four missing failure causes, the reconnect and resume wording, and the sweep notification.

**Checkpoint**: Large transfers survive real-world network conditions.

---

## Phase 6: User Story 4 - Several devices on the network (Priority: P4)

**Goal**: Every device on the network appears automatically, with a recognizable name, and can be chosen as a target.

**Independent Test**: Run two instances and two phones, and verify each lists the others within 5 seconds with distinguishable names.

### Tests for User Story 4

- [X] T089 [P] [US4] Integration test for discovery appearance within 5 seconds in `test/integration/us4_discovery_test.go`, per SC-011. Real multicast between two real instances, with the clock actually read: it appears in about 50 ms. The tests **skip** rather than pass when the machine has no usable multicast, because a container without it would otherwise report a green that means nothing; everything not needing the wire is covered separately.
- [X] T090 [P] [US4] Integration test for duplicate names remaining distinguishable in `test/integration/duplicate_names_test.go`, per FR-005. Two machines called "Laptop" is a household, not an edge case, and it is a protocol problem before it is a display one: the DNS-SD instance name is the uniqueness key, so without the short identifier the second responder collides and the stack renames it to something the user never chose. The identifier shown is the ULID's **tail**, because the head is a timestamp that two devices first run on the same day would share.
- [X] T091 [P] [US4] Integration test for the manual address fallback in `test/integration/manual_address_test.go`, per FR-006, including the boundary it sits on: the address field is the one place in the product where a person can name an arbitrary host, so a public address and a host name are both refused with a reason. Resolving a name is itself a request off the local network.

### Implementation for User Story 4

- [X] T092 [US4] Implement mDNS advertising of `_fastr._tcp` with the TXT record in `internal/discovery/advertise.go`, per contracts/discovery.md. **Library changed to `hashicorp/mdns`** (research.md amended): research item 8 flagged `libp2p/zeroconf/v2`'s maintenance to be confirmed at implementation time, and its last release is four years old against two months for the documented fallback. Advertising starts only when the server starts, never before: announcing a machine's name to a whole network is exactly what FR-001 exists to keep from happening on its own.
- [X] T093 [US4] Implement continuous browsing and the device cache in `internal/discovery/browse.go`. One long listening window rather than polling: a responder answers a query *and* announces itself unprompted, so a browser that keeps its socket open sees arrivals without asking again. It re-queries once a minute only as a safety net for lost packets — this is UDP multicast — which keeps the idle cost near zero. Nothing is ever removed from the cache; see T094 for why that is correct rather than lazy.
- [X] T094 [US4] Implement reachability confirmation through `/connect` rather than the record alone in `internal/discovery/reachability.go`, per FR-007. A record outlives the process that published it, so listing from the record and confirming against the device is the only honest combination. It also catches the case DHCP creates constantly: an address that now answers as **someone else**, which is refused rather than sent to.
- [X] T095 [US4] Implement manual address entry producing an identical device record in `internal/discovery/manual.go`. The user supplies an address, never an identity: every identity field comes from `/connect` on the machine itself, which is where it would have come from over mDNS. Only `source` differs, and only so the interface can confirm the entry took effect. An address that answers nothing is not remembered, because a remembered typo makes the list worse than empty.
- [X] T096 [US4] Implement graceful degradation when multicast is unavailable in `internal/discovery/browse.go`. Unavailability is a reported state, not a startup error: refusing to run would take the whole application down over a feature a user who only sends from their phone never touches. The device list carries the state, because a list that is merely empty looks like a network with no other computers on it and gives nobody a reason to look for the address field.
- [X] T097 [P] [US4] Build the device list with reachability, pairing state, and name disambiguation in `web/src/lib/DeviceList.svelte`. One list rather than two: a person thinks about the machines around them, some of which they have connected to before, and splitting the screen would make them do the merge. Disambiguation is computed in Go (`discovery.Labels`) so both surfaces show the same thing. The address field is always visible, and merely emphasised when discovery is known to be broken, because that failure is not always detectable from inside a browser.
- [X] T098 [US4] Add translations for every string introduced by this story, in `internal/i18n/locales/` (see T088 for why the catalogues live there).

**Checkpoint**: Device selection works across a real multi-device network.

---

## Phase 7: User Story 5 - Managing devices and transfers (Priority: P5)

**Goal**: See what is running, cancel it, review what happened, and revoke a device.

**Independent Test**: Queue five transfers, verify exactly one runs, cancel it, revoke a pairing, and confirm the device loses access immediately.

### Tests for User Story 5

- [X] T099 [P] [US5] Integration test asserting exactly one transfer is ever active in `test/integration/us5_queue_test.go`, per SC-021, with ten queued and every one of them trying to start in the same instant — which is the only way to test a mutual exclusion rather than a sequence.
- [X] T100 [P] [US5] Integration test for queue durability across a restart in `test/integration/queue_persistence_test.go`, per FR-035e. A real restart: the store is closed and another opened on the same file, so the second instance sees exactly the bytes the first left rather than anything held in memory between them.
- [X] T101 [P] [US5] Integration test for immediate revocation in `test/integration/revocation_test.go`, per FR-015. The moment itself was already pinned in `unpaired_access_test.go`, where the unpaired-access rules live; this covers everything around it — what happens to a transfer in flight, what the list shows afterwards, and that a revoked credential is dead at every entry point rather than only the one that was in use.
- [X] T102 [P] [US5] Integration test for trust mode defaults, changes, and expiry recomputation in `test/integration/trust_mode_test.go`, per FR-016 and FR-016b, including that changing the mode recomputes the window from the device's **last activity** rather than from now — otherwise anyone could extend a stale pairing indefinitely by toggling a switch.
- [X] T103 [P] [US5] End-to-end test of queue reordering and cancellation, in `web/tests/e2e/us5_queue.spec.ts` (under `web/` for the reason given at T049). It reloads the page after reordering, so what is asserted is the order the *server* holds rather than the one this page remembers. **Found a defect:** the queue view was refreshed only when a transfer ended, so a newly queued one never appeared until something else finished.

### Implementation for User Story 5

- [X] T104 [US5] Implement the sequential queue runner enforcing one active transfer, per FR-035a. **No runner was written, and the task's shape is worth correcting rather than satisfying.** A runner implies the server drives transfers; it cannot. In both directions the bytes come from a browser — pushed as chunks, or supplied into a pipe — so nothing here can *start* anything. The invariant lives in `store.Activate`, which is the single place a transfer can take the active slot and therefore the single place it can be refused. `TestOnlyOneTransferIsEverActive` holds it under ten simultaneous attempts.
- [X] T105 [US5] Implement reorder, remove, and clear operations in `internal/httpapi/queue_handlers.go`, per FR-035c. Landed with T087b, which needed the same endpoint. Reordering sends the **whole** order rather than a move: two pages nudging entries at once with relative moves would interleave into an order neither asked for. Removing an entry cancels it rather than merely dequeuing, or the transfer would survive as a record nothing will ever run.
- [X] T106 [US5] Implement interrupted transfers yielding their place rather than blocking, per FR-035d. In `app.Interrupt` and `store.Deactivate(id, requeue)` rather than a queue file, for the reason at T104. An interrupted transfer releases the slot and goes to the back of the queue, keeping its committed offset.
- [X] T107 [US5] Implement bilateral cancellation in `internal/app/transfers.go`, per FR-035. Cancelling releases the pipe first, so a waiting fetch learns immediately rather than sitting out the rendezvous timeout.
- [X] T108 [US5] Implement per-device trust mode and the acceptance decision in `internal/pairing/trust.go`, per FR-016a to FR-016d. Pairing answers "may this device talk to me at all"; the trust mode answers "may it write to my disk without anyone looking", which comes up on every transfer and whose answer changes inside the year a pairing lasts. An incoming transfer from an ask-mode device now waits in `awaiting_acceptance`, and **nothing it sends reaches staging before a human answers**. Required amending the state machine: accepting returns a transfer to `queued` rather than jumping to `running`, since acceptance and scheduling are different questions.
- [X] T109 [US5] Implement the bounded acceptance window for ask-mode transfers in `internal/app/transfers.go`, per FR-016d. Two minutes — roughly how long it takes to walk to another room — checked every fifteen seconds, because a sender watching a spinner deserves an answer close to when it expires. An unanswered transfer fails with `acceptance_timeout` rather than holding both the sender's attention and a place in a queue that runs one thing at a time.
- [X] T110 [US5] Implement history recording and clearing in `internal/app/history.go` and `internal/httpapi/history_handlers.go`, per FR-037 and FR-039. The endpoints did not exist at all. It is **this machine's** history rather than a per-device view: the person asking is the one sitting here, and what they want to know includes the phone that is no longer in the house. Clearing is loopback only, because a paired phone erasing the record of what it sent is exactly backwards.
- [X] T111 [P] [US5] Build the queue view with reordering in `web/src/lib/QueueView.svelte`. Buttons rather than drag and drop: FR-039g wants every essential flow reachable from a keyboard, and a list that can only be reordered by dragging is not. It says the one-at-a-time rule out loud, because a second file that has not started looks broken until you know it is a queue.
- [X] T112 [P] [US5] Build the paired devices view with trust mode and revocation, per FR-016c. **Folded into `web/src/lib/DeviceList.svelte` rather than a separate `DeviceSettings.svelte`**: FR-016c asks for the trust mode to be visible *wherever paired devices are listed*, and a second panel listing the same devices would give the user two lists and a question about which one to use. The mode is a control rather than a label, because noticing it and wanting to change it are the same moment.
- [X] T113 [P] [US5] Build the history view with outcomes, causes, and protection mode in `web/src/lib/HistoryView.svelte`. Every row says which protection mode was used: in simple mode the content was readable by anyone on the network, and Principle V's honesty duty makes it the user's business which of their transfers that was true for.
- [X] T114 [US5] Add translations for every string introduced by this story, in `internal/i18n/locales/` (see T088 for why the catalogues live there).

**Checkpoint**: The tool is controllable and trustworthy for daily use.

---

## Phase 8: User Story 6 - Phone to phone through a relay (Priority: P6)

**Goal**: Two phones paired to the same computer exchange files, with the computer relaying and keeping nothing.

**Independent Test**: Send a file between two phones, then verify staging is empty and nothing appeared in the receive folder.

### Tests for User Story 6

- [X] T115 [P] [US6] Integration test for a relayed transfer leaving zero bytes behind in `test/integration/us6_relay_test.go`, per SC-019 — for **all four** endings, not just the happy one: completed, cancelled, failed, and swept. **Found a race in the test itself:** the client's read finishes when the last byte arrives, while the server is still finishing the transfer, so the residue check has to wait for the ending it is asking about.
- [X] T116 [P] [US6] Integration test asserting trust is not transitive between phones in `test/integration/relay_authorization_test.go`, per FR-054. A device identifier is not a secret — it travels in the device list every paired device can read — so this cannot rest on nobody knowing it. It rests on the relay asking about the *target's* pairing every time, including after a revocation and after an expiry.
- [X] T117 [P] [US6] Integration test for relay failure when the computer becomes unavailable in `test/integration/relay_failure_test.go`, per FR-057, covering both halves: the upload resumes from the committed offset, the download resumes with a Range, and a restart leaves the staged bytes intact with the transfer interrupted rather than failed.

### Implementation for User Story 6

- [X] T118 [US6] Implement relay session staging outside the receive folder in `internal/transfer/relay.go` and `internal/app/relay.go`, per FR-055. **Staged rather than piped, deliberately**: the desktop-to-phone path joins a fetching receiver to a supplying sender and never touches disk, and that cannot work here, because the supplying side is a phone and a phone holding one streaming request open for the length of a file loses everything when its screen locks. It costs a round trip through the disk and buys a transfer that survives a locked screen on either side. Relayed bytes live in a directory of their own, so "never appears as a file this computer received" is a property of the layout rather than a promise about cleanup code.
- [X] T119 [US6] Implement per-hop authorization requiring both pairings, per FR-054, in `app.Declare` using `pairing.Decide` for each hop. The relaying computer's own trust mode applies to the sender as well: the bytes land on its disk, so "may this device write here unattended" is exactly as relevant as for a file addressed to it.
- [X] T120 [US6] Implement relay staging space checks before starting, per FR-058: the check runs against the *staging* filesystem for a relayed transfer rather than the receive folder, since that is where the bytes actually go.
- [X] T121 [US6] Implement relay cleanup on every terminal state and on sweep. Complete, fail, and cancel each discard; the retention sweep clears what a process killed mid-relay left behind, because FR-055 has no exception for a crash.
- [X] T122 [P] [US6] Build the relay visibility and cancellation view in `web/src/lib/RelayView.svelte`, per FR-056. It shows how much is on this disk **right now**, read from the filesystem rather than from the transfer record, because that is the number the person whose disk it is cares about. It renders nothing when nothing is passing through, which is almost always. The endpoint behind it is loopback only: the answer names two other people's devices and their files.
- [X] T123 [US6] Allow phones to be selected as targets — in `web/src/lib/MobilePicker.svelte` rather than `DeviceList.svelte`, because the phone is where a relayed transfer is *started* and the device list is a desktop panel. Only phones with their page open are offered: a relayed transfer to a closed phone would sit in the computer's staging area waiting for a collection that is not coming. The picker says plainly that the computer passes the file through without keeping it.
- [X] T124 [US6] Add translations for every string introduced by this story, in `internal/i18n/locales/` (see T088 for why the catalogues live there).

**Checkpoint**: All six user stories are independently functional.

---

## Phase 9: Trusted Mode

**Purpose**: The opt-in path to full content encryption, per FR-047a to FR-047e and constitution Principle V.

- [X] T125 [P] Integration test asserting content is unreadable on the wire in trusted mode, in `test/integration/trusted_mode_test.go` alongside the other trusted-mode refusals rather than a file of its own. It captures the bytes below TLS through a proxy and looks for the payload, **and runs the same payload over a plain connection as a control** — otherwise a test that finds nothing proves nothing.
- [X] T126 [P] Integration test asserting `require_trusted` refuses plain connections, per FR-047c, in `test/integration/trusted_mode_test.go`. **Found a defect:** a confirmed downgrade overrode the setting. The user asked for that device never to connect in the clear; a confirmation on one transfer is not a reason to override a standing instruction, or the setting would mean "ask me", which is what the other one already means.
- [X] T127 [P] Integration test asserting an abandoned setup leaves the simple pairing working, per FR-047d, in Go and in the browser (`web/tests/e2e/trusted_setup.spec.ts`). The property most likely to rot without anyone noticing: nothing visibly breaks when it does, the user simply finds their phone locked out some time later for no reason they can connect to this.
- [X] T128 Implement local certificate authority generation and storage with restrictive permissions in `internal/trust/ca.go`. Generated per installation and never shipped — a shared authority would be a master key for every fastr user alive. The key is written 0600 into a directory created 0700, is never handed out, and the certificate carries a fingerprint to compare against what the phone shows, because otherwise "install this" means "trust whatever arrived". A test caught that `MaxPathLen` alone means *unset*, so the authority had quietly permitted intermediates. **Permissions are not enforced on Windows; see T137b.**
- [X] T129 Implement certificate issuance for the current network addresses in `internal/trust/issue.go`. Addresses, never host names: resolving a name is a request off the local network. Non-local addresses are filtered out rather than trusted — one naming a public address could impersonate something on the internet to every phone that installed the authority. Leaves are short-lived and reissued on every start, because a DHCP lease moves and a certificate for an address this machine no longer holds is worse than useless.
- [X] T130 Implement the TLS listener alongside the plain one, in `internal/httpapi/tls_listener.go`. **Alongside, not instead**: trusted mode is opt-in per device, and a phone that never set it up must keep working. Two ports rather than one port sniffing its first bytes — sniffing works, and it is one more thing that can be wrong on a protocol whose whole job is to be trustworthy. A machine where nobody set it up serves no TLS and creates no key, verified by running the binary.
- [X] T131 Implement the trusted mode endpoints in `internal/httpapi/trust_handlers.go`, per contracts/http-api.md. `verify` rests entirely on the connection: it can only succeed over the TLS listener, so a phone asserting it is trusted over plain HTTP is asserting exactly what it has not done. The certificate itself is served unauthenticated, which is correct — the phone needs it *before* it can be trusted, and what makes installing it safe is the fingerprint the user compares, not the secrecy of the download. **A browser test found** that init answered unsealed while the client called it sealed; it is sealed now, like every other control-plane answer.
- [X] T132 Implement downgrade refusal and explicit confirmation in `internal/app/transfers.go`, per FR-047e. The failure this prevents is silent by nature: the transfer works, the file arrives, and the only difference is that the content crossed the network readable when the user had every reason to believe it would not. Refused rather than upgraded, because this machine cannot make the sender's connection secure and pretending otherwise is the exact dishonesty Principle V forbids. The transfer records the mode it actually used, so history cannot claim one it did not have.
- [ ] T133 [P] Implement the service worker performing streamed decryption in `web/src/sw/decrypt.ts`, registered in trusted mode only. **Blocked on hardware, not on effort.** A service worker exists only in a secure context, so this code cannot run — let alone be tested — until a real phone has installed the authority and reached the HTTPS origin. Everything it depends on is now in place (T128 to T132), so this is the next thing to do once T135 has been performed once.
- [X] T134 [P] Build the guided setup walkthrough covering iOS full trust and Android user CA in `web/src/lib/TrustedSetup.svelte`. It leads with what trusted mode buys **and what it costs**, before any control that does anything: a walkthrough that opens with "step 1 of 4" is asking somebody to agree to something they have not been told. It stays honest while the work is half done — until a phone has installed the certificate *and* arrived over HTTPS, it says so and says the content is still readable on this network.
- [ ] T135 Verify both setup flows on real Android and iOS hardware and record the findings in `specs/001-lan-file-transfer/research.md`, per research item 4. **Requires a physical phone and cannot be simulated.** Playwright over a LAN address is not a secure context either, so no browser test substitutes for it. The instructions in the walkthrough are written from documentation rather than from having performed them, and iOS in particular has two separate steps (install the profile, then enable full trust under Certificate Trust Settings) that people miss.
- [ ] T137b Restrict the certificate authority's private key on Windows, in `internal/trust/` with a platform file. **Found by CI, not by design.** Windows does not implement POSIX permission bits: the key is written with 0600 and reports 0666, because access there is decided by an ACL this code never sets. In practice it inherits the ACL of `%LOCALAPPDATA%`, which is user-only on a default installation — but that is the operating system's arrangement rather than a guarantee fastr makes, and this is the most sensitive artefact in the product: anything holding it can impersonate any site to every phone that installed the authority. Needs `windows.SetNamedSecurityInfo` with an explicit DACL, and a Windows-only test asserting the resulting DACL names only the current user. **Blocks trusted mode on Windows**, not the rest of it; Principle IV would otherwise be satisfied only in letter.
- [X] T136 Add translations for every string introduced by trusted mode, in `internal/i18n/locales/` (see T088 for why the catalogues live there).

---

## Phase 10: Polish & Cross-Cutting Concerns

- [ ] T137 Decide the pairing hardening question, implementing a PAKE or documenting the accepted risk, in `internal/pairing/handshake.go`, per research item 1
- [X] T138 Confirm the mDNS library choice or switch to the identified fallback, per research item 8 (the task said item 2; the mDNS decision is item 8). **The flag fired.** `libp2p/zeroconf/v2`'s last release is four years old against two months for the documented fallback, so the fallback it is; `brutella/dnssd` was weighed and rejected for pulling Linux-specific netlink plumbing that adds a parity risk on Windows for no benefit. research.md section 8 carries the reasoning and what the change costs. Done with User Story 4.
- [ ] T139 Ensure a missing Linux tray dependency degrades to a headless background service in `internal/platform/tray.go`, per research item 3
- [ ] T140 [P] Run the full accessibility gate and fix every violation, in `test/e2e/a11y.spec.ts`, per FR-039f to FR-039j
- [ ] T141 [P] Verify progress announcements fire only at meaningful moments in `test/e2e/a11y_announcements.spec.ts`, per FR-039i
- [X] T142 [P] Audit for untranslated strings and raw identifiers across the interface, in `web/tests/e2e/i18n_coverage.spec.ts`, per SC-022. **Found that French was decorative.** The catalogue was complete, negotiation picked it correctly, `<html lang="fr">` was set — and the interface rendered English, because `t()` reads a module variable and the language was set in the shell's `onMount`, after every component had already rendered with the default. Setting it before the first render fixes it; a *runtime* override will additionally need the current language to be reactive state, which is noted where the next person will look. Verified to fail by deleting a rendered key from both catalogues.
- [X] T143 [P] Audit that no screen describes simple mode as secure without qualification, in `web/tests/e2e/protection_honesty.spec.ts`, per SC-016a. **The rule had to be stated precisely before it could be tested**, and the first attempt was too blunt: it flagged "pairing and credentials are encrypted", which is true, precise, and about something else, and a check that pushes the interface towards saying *less* about what is protected is the opposite of the point. It now flags a reassuring word only when the sentence is about files, and it excludes the trusted-mode panel by region rather than by wording — that is the one place "encrypts file content end to end" is simply true. Verified to fail by putting "Your files are sent securely" into the protection notice.
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
