# Quickstart: Validating LAN File Transfer

**Date**: 2026-08-20 | **Plan**: [plan.md](./plan.md)

How to build, run, and prove the feature works. Written for someone who has just cloned the
repository and has never run it.

## Prerequisites

| Tool | Why |
|---|---|
| Go 1.24 or newer | Builds the binary. |
| Node 22 or newer | Builds the web application. Build time only; users never install it. |
| A phone on the same network | Android or iOS. A real device, not an emulator, for anything touching browser behavior. |

No database, no service, no account, nothing to configure before the first run.

## Build

```bash
make build          # builds web/ with Vite, embeds it, produces ./bin/fastr
make build-all      # cross-compiles Linux and Windows, both architectures
```

Expected: a single binary with no dynamic dependency beyond the platform's own libraries. Verify
that the promise holds:

```bash
ldd ./bin/fastr     # Linux: should report a static binary or only core system libraries
```

## Run

```bash
./bin/fastr
```

Expected: a tray icon appears, the browser opens on the desktop view, and the window shows a
connection address and a QR code. The server binds only to the interfaces listed in settings, and
nothing was listening before this command ran.

## Scenario 1: first connection and a large transfer (User Story 1, P1)

1. Scan the QR code with the phone camera. The browser opens the mobile view.
2. Expected: the phone shows a pairing step, **not** the device list. Nothing is reachable yet.
3. Enter the 6-digit code from the desktop. Approve the device on the desktop.
4. Expected: the phone appears in the desktop device list, marked **simple mode**, with a plain
   statement that content is readable by anyone on the network.
5. Generate a large fixture and send it:

```bash
make fixture SIZE=10G    # writes test/testdata/large/10G.bin, gitignored
```

6. Drag the fixture onto the desktop window, pick the phone, send.
7. Expected: both screens show progress with speed and time remaining. The desktop process memory
   stays flat throughout. Watch it:

```bash
make watch-memory PID=$(pgrep fastr)
```

8. Expected on completion: the phone can save the file, and its checksum matches.

```bash
b2sum test/testdata/large/10G.bin      # compare with the received file
```

**Fails if**: the phone reaches anything before pairing, memory grows with the file, or the
interface describes simple mode as secure or private without qualification.

## Scenario 2: phone to computer (User Story 2, P2)

1. From the phone, pick a photo or video, choose the computer, send.
2. Expected: the file lands in the receive folder with its original name.
3. Send the same file again without deleting the first.
4. Expected: the existing file is untouched and the new one is stored under a distinct name.
5. On Windows, send a file named `aux.txt` from a Linux machine.
6. Expected: it is stored under a sanitized name, and the interface says what was changed.

## Scenario 3: surviving an interruption (User Story 3, P3)

1. Start the 10 GB transfer.
2. At roughly halfway, drop the network:

```bash
make netcut DURATION=30s     # blocks the port, restores it after the delay
```

3. Expected: the transfer shows as **interrupted**, not failed, and reconnection is attempted.
4. Expected on restore: it resumes from where it stopped. Compare bytes sent against file size;
   re-sent data must stay under 1% (SC-005).
5. Corrupt a staged file mid-transfer and let it complete.
6. Expected: `checksum_mismatch`, the transfer is reported failed, and no file appears at the
   final path.

## Scenario 4: several devices (User Story 4, P4)

Run a second instance on another machine, or locally on another port:

```bash
./bin/fastr --port 8443 --state-dir /tmp/fastr-second
```

Expected: each instance lists the other within 5 seconds, with distinguishable names even if the
names match. Stop one; it becomes unreachable rather than disappearing.

## Scenario 5: queue, revocation, history (User Story 5, P5)

1. Queue five transfers at once.
2. Expected: exactly one runs, four wait, and the queue is reorderable. Verify only one is active:

```bash
curl -s localhost:8443/api/queue -H "Authorization: Bearer $TOKEN" | jq '.active_id'
```

3. Cancel the running one. Expected: it stops on both ends, the next starts.
4. Revoke the phone's pairing. Expected: the phone loses access on its next request, immediately.
5. Restart the application. Expected: the queue is still there, or its entries are reported
   abandoned with a reason.

## Scenario 6: phone to phone (User Story 6, P6)

1. Pair a second phone to the same computer.
2. From phone A, pick phone B as the target, send.
3. Expected: the transfer works, and the relaying computer shows it passing through.
4. Expected after completion:

```bash
find "$STAGING_FOLDER" -type f      # must be empty
find "$RECEIVE_FOLDER" -newer /tmp/marker   # must not contain the relayed file
```

5. Pair a third phone but revoke it, then have phone A target it. Expected: refused, pairing
   required first.

## Scenario 7: trusted mode

1. On the desktop, start trusted-mode setup for a phone.
2. Follow the in-app guidance to install the certificate authority. On iOS this includes enabling
   full trust in a separate settings screen.
3. Expected: the phone reaches the HTTPS origin with no security warning, and the device is now
   marked **trusted**.
4. Expected: content is encrypted. Confirm on the wire:

```bash
make capture         # captures a transfer; the fixture's magic bytes must not appear
```

5. Set `require_trusted` on that device, then connect it over the plain channel.
6. Expected: `426 trusted_mode_required`, with an explanation rather than a bare code.
7. Abandon a setup halfway on another phone. Expected: its simple-mode pairing still works.

## Test suites

```bash
make test              # unit and integration
make test-e2e          # Playwright against Chromium and WebKit
make test-a11y         # axe-core assertions plus keyboard-only traversal
make test-security     # unpaired access, path traversal, replay, log hygiene
make test-network      # asserts zero sockets leave the local network
make test-large        # the 10 GB memory-flatness test; slow, runs in CI nightly
```

All of these run on Linux **and** Windows in CI. A failure on either blocks the release, per
Principle IV.

## What "done" looks like

Every scenario above passes on both operating systems, against a real Android phone and a real
iPhone, with `make test-network` reporting zero outbound connections throughout.
