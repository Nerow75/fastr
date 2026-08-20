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

- [ ] T001 Initialize Go module and directory skeleton per plan.md in `go.mod`, `cmd/fastr/`, `internal/`, `test/`
- [ ] T002 [P] Scaffold the Svelte and TypeScript application with Vite in `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`
- [ ] T003 [P] Add `Makefile` with `build`, `build-all`, `test`, `test-e2e`, `test-a11y`, `test-security`, `test-network`, `test-large`, `fixture`, `capture` targets
- [ ] T004 [P] Configure `golangci-lint` in `.golangci.yml` with a linter forbidding `net/http` clients to non-local hosts
- [ ] T005 [P] Configure ESLint and Prettier for the web application in `web/eslint.config.js`
- [ ] T006 Wire `go:embed` of `web/dist` into `internal/httpapi/assets.go`, failing the build when the bundle is missing
- [ ] T007 [P] Add CI workflow running build and tests on Linux and Windows in `.github/workflows/ci.yml`
- [ ] T008 [P] Add secret scanning to CI in `.github/workflows/ci.yml`, per constitution quality gate 6
- [ ] T009 [P] Add cross-compilation release workflow producing checksummed binaries in `.github/workflows/release.yml`
- [ ] T010 [P] Add `test/testdata/.gitignore` excluding `large/`, and a `make fixture` generator in `test/testdata/generate.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The infrastructure every user story sits on. Nothing below can be tested without it.

**⚠️ CRITICAL**: No user story work begins until this phase is complete.

### Platform and configuration

- [ ] T011 [P] Define the platform abstraction interface in `internal/platform/platform.go` covering paths, free space, filename rules, autostart, tray
- [ ] T012 [P] Implement the Linux platform in `internal/platform/platform_linux.go` using `statfs` and XDG paths
- [ ] T013 [P] Implement the Windows platform in `internal/platform/platform_windows.go` using `GetDiskFreeSpaceEx`, `%LOCALAPPDATA%`, reserved-name and trailing-dot rules
- [ ] T014 Implement settings load, save, and defaults in `internal/config/config.go`, defaulting the receive folder outside system folders
- [ ] T015 [P] Write parity tests asserting identical behavior on both platforms in `test/integration/platform_parity_test.go`

### Persistence

- [ ] T016 Implement the bbolt store, bucket layout, and open/close lifecycle in `internal/store/store.go` per data-model.md
- [ ] T017 [P] Implement the `Device` and `Pairing` records with their validation rules in `internal/store/devices.go`
- [ ] T018 [P] Implement the `Transfer`, `TransferItem`, and queue records in `internal/store/transfers.go`
- [ ] T019 [P] Implement `HistoryEntry` and `RelaySession` records in `internal/store/history.go`
- [ ] T020 Implement the schema version and migration hook in `internal/store/migrate.go`

### Observability and errors

- [ ] T021 Implement structured logging with mandatory secret redaction in `internal/app/logging.go`, per FR-019
- [ ] T022 [P] Define the machine-readable error catalogue and its translation keys in `internal/app/errors.go`, per the contract error format
- [ ] T023 [P] Write a test asserting no key, token, or pairing code can reach a log line in `test/integration/log_hygiene_test.go`

### Internationalization

- [ ] T024 [P] Implement catalogue loading and language negotiation in `internal/i18n/i18n.go`
- [ ] T025 [P] Create the English and French catalogues in `web/src/locales/en.json` and `web/src/locales/fr.json`
- [ ] T026 [P] Implement the client translation helper with English fallback in `web/src/lib/i18n.ts`, per FR-039d

### HTTP server and security envelope

- [ ] T027 Implement the HTTP server, interface binding, and start/stop lifecycle in `internal/httpapi/server.go`, listening only when started per FR-001
- [ ] T028 Implement routing and the error response writer in `internal/httpapi/router.go`
- [ ] T029 Implement the ChaCha20-Poly1305 envelope with a monotonic counter in `internal/pairing/envelope.go`, per contracts/pairing.md
- [ ] T030 [P] Implement the matching envelope on the client in `web/src/crypto/envelope.ts` using `@noble/ciphers`
- [ ] T031 Implement the X25519 handshake, HKDF derivation, and code binding in `internal/pairing/handshake.go`
- [ ] T032 [P] Implement the client handshake in `web/src/crypto/handshake.ts` using `@noble/curves`
- [ ] T033 Implement pairing codes with single use, 3-minute expiry, 5-failure death, and rate limiting in `internal/pairing/code.go`, per FR-012 and FR-013
- [ ] T034 Implement session credentials and the authorization middleware in `internal/pairing/session.go`, refusing every unpaired request per FR-011
- [ ] T035 Implement the pairing endpoints in `internal/httpapi/pairing_handlers.go` per contracts/http-api.md
- [ ] T036 [P] Write a replay and out-of-order counter rejection test in `test/integration/envelope_replay_test.go`
- [ ] T037 [P] Write a test asserting an unpaired device is refused at every entry point in `test/integration/unpaired_access_test.go`

### Event stream and application shell

- [ ] T038 Implement the event bus and the SSE endpoint with 4-per-second progress throttling in `internal/httpapi/events.go`
- [ ] T039 [P] Implement the client event stream with reconnection in `web/src/lib/events.ts`
- [ ] T040 Implement the application shell, routing between desktop and mobile views in `web/src/routes/App.svelte`
- [ ] T041 [P] Establish the accessible base layer, focus ring, landmarks, and live region in `web/src/lib/a11y.ts` and `web/src/app.css`
- [ ] T042 Implement tray icon, background lifecycle, and single-instance guard in `internal/platform/tray.go` and `cmd/fastr/main.go`, per FR-048 and FR-050
- [ ] T043 [P] Implement the autostart toggle, off by default, in `internal/platform/autostart_linux.go` and `internal/platform/autostart_windows.go`, per FR-049
- [ ] T044 [P] Write the network boundary test asserting zero sockets leave the local network in `test/integration/network_boundary_test.go`, per Principle I

**Checkpoint**: A binary starts, serves an authenticated shell, pairs a device, and streams events. User stories can begin.

---

## Phase 3: User Story 1 - Large file from computer to phone (Priority: P1) 🎯 MVP

**Goal**: Scan a QR code, pair, drag a file of any size onto the desktop, and open it on the phone.

**Independent Test**: With one computer and one phone, move a 10 GB file end to end and verify the checksum matches, with memory flat throughout.

### Tests for User Story 1

- [ ] T045 [P] [US1] Contract test for the transfer declaration and content endpoints in `test/integration/transfer_contract_test.go`
- [ ] T046 [P] [US1] Integration test for the full pair-then-send journey in `test/integration/us1_send_to_phone_test.go`
- [ ] T047 [P] [US1] Memory-flatness test comparing a 10 MB and a 10 GB transfer in `test/integration/large_transfer_test.go`, per SC-003
- [ ] T048 [P] [US1] Path traversal rejection test in `test/integration/path_traversal_test.go`, per FR-018
- [ ] T049 [P] [US1] End-to-end browser test of the QR-to-received-file journey in `test/e2e/us1_first_transfer.spec.ts`

### Implementation for User Story 1

- [ ] T050 [P] [US1] Implement connection info and QR code generation in `internal/httpapi/connect.go`
- [ ] T051 [P] [US1] Build the pairing screen with code entry in `web/src/lib/PairingScreen.svelte`
- [ ] T052 [US1] Build the desktop approval screen for pending devices in `web/src/lib/PendingDevices.svelte`, per FR-010
- [ ] T053 [US1] Implement free space checking before a transfer starts in `internal/transfer/space.go`, per FR-028
- [ ] T054 [US1] Implement the constant-memory streaming engine in `internal/transfer/stream.go` using a fixed buffer, per FR-029
- [ ] T055 [US1] Implement BLAKE2b checksumming during the copy in `internal/transfer/checksum.go`, per FR-032
- [ ] T056 [US1] Implement destination filename sanitization and collision resolution in `internal/transfer/naming.go`, per FR-024 and FR-025
- [ ] T057 [US1] Implement the staging-then-verify-then-move write path in `internal/transfer/writer.go`, per FR-033
- [ ] T058 [US1] Implement the transfer declaration endpoint in `internal/httpapi/transfer_handlers.go`
- [ ] T059 [US1] Implement the downward content endpoint with range support in `internal/httpapi/content_download.go`
- [ ] T060 [US1] Implement the transfer orchestration service in `internal/app/transfers.go`
- [ ] T061 [P] [US1] Build the desktop send view with drag and drop and multi-selection in `web/src/lib/SendPanel.svelte`, per FR-020 and FR-021
- [ ] T062 [P] [US1] Build the progress view with speed and time remaining in `web/src/lib/TransferProgress.svelte`, per FR-030
- [ ] T063 [US1] Build the mobile receive and save view in `web/src/routes/Mobile.svelte`, per FR-026
- [ ] T064 [US1] Display the simple-mode protection statement at pairing and during transfers in `web/src/lib/ProtectionNotice.svelte`, per FR-047 and SC-016a
- [ ] T065 [US1] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: User Story 1 is fully functional and independently testable. This is the MVP.

---

## Phase 4: User Story 2 - Files from phone to computer (Priority: P2)

**Goal**: Pick files on the phone and send them to a chosen computer, which writes them to its receive folder.

**Independent Test**: With a paired phone, send a video to the computer and verify it lands intact in the receive folder with its original name.

### Tests for User Story 2

- [ ] T066 [P] [US2] Contract test for the upward content and offset endpoints in `test/integration/upload_contract_test.go`
- [ ] T067 [P] [US2] Integration test for the phone-to-computer journey in `test/integration/us2_send_to_computer_test.go`
- [ ] T068 [P] [US2] Collision and sanitization test covering Windows reserved names in `test/integration/filename_rules_test.go`
- [ ] T069 [P] [US2] End-to-end browser test of selecting and sending from the phone in `test/e2e/us2_phone_upload.spec.ts`

### Implementation for User Story 2

- [ ] T070 [US2] Implement the committed-offset upload endpoint in `internal/httpapi/content_upload.go`, per contracts/http-api.md
- [ ] T071 [US2] Implement durable offset commitment with fsync in `internal/transfer/offsets.go`
- [ ] T072 [US2] Implement the item completion and verification endpoint in `internal/httpapi/transfer_complete.go`
- [ ] T073 [P] [US2] Build the mobile file picker and selection list with names and sizes in `web/src/lib/MobilePicker.svelte`
- [ ] T074 [P] [US2] Implement chunked client upload from a file offset in `web/src/lib/upload.ts`
- [ ] T075 [US2] Implement receive folder configuration and its change semantics in `internal/config/receive_folder.go`, per FR-023
- [ ] T076 [US2] Add a desktop notification on completed incoming transfers in `internal/platform/notify_linux.go` and `internal/platform/notify_windows.go`
- [ ] T077 [US2] Add translations for every string introduced by this story to `web/src/locales/en.json` and `web/src/locales/fr.json`

**Checkpoint**: Both directions work independently.

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
