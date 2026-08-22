# Journal

What was done, why, and what it cost. Newest first.

`specs/001-lan-file-transfer/tasks.md` remains the source of truth for *what is
done*; this file is for the reasoning that does not belong in a task line, and
for what a future session needs in order to pick the work back up.

---

## 2026-08-21 — User Story 2, a first connection without a terminal, and the browser harness

Commits: `21a4e2e`, `ee612e1`. Tasks: 64 → 83 of 159.

### What shipped

**User Story 2, phone to computer (T066-T077).** This direction does not use the
pipe. The contract's upward path has the phone pushing chunks and the server
writing them, which made two things necessary that the downward path never
needed:

- An offset is acknowledged only after an `fsync`. The reply to a chunk is a
  promise the sender may forget those bytes, and without the sync "committed"
  would mean the kernel intends to write them eventually.
- The BLAKE2b state lives in memory beside the open staging file. The digest of
  a file arriving over forty requests cannot be stored between them, and
  rehashing the prefix on every chunk would turn a linear transfer quadratic. It
  is rebuilt by one pass when the process restarted mid-transfer.

**FR-002 (T050b, T051b).** It was not met. `/qr` rendered a code from a URL that
nothing supplied, and the pairing code only ever reached standard output, so a
first connection required reading a console — which Principle VI does not
survive. The host's invitation is now served from loopback only.

**The browser harness (T049, T069).** Drives the real binary through two
contexts: loopback as the computer, the machine's LAN address as the phone.

**Two follow-ups it forced (T051c, T051d, T051e).** See below.

### What the harness found on its first run

Three defects that every Go test passed straight over, because all three live
above the wire. This is the argument for the harness, and it paid for itself
immediately:

1. **Nothing could ever be received on the phone.** `TransferProgress.svelte`
   offered Save only once a transfer reached `completed`, but a transfer
   completes *because* the receiver fetches the content, and that fetch is what
   Save starts. The two waited for each other. User Story 1 had never worked in
   a browser.
2. **The desktop's supply request carried no `Authorization` header.** The
   server refused every one with `401`, and a bare `if (!response.ok) return;`
   swallowed it. The receiver waited out the 30-second rendezvous and its
   download was cancelled with nothing said anywhere.
3. **The invitation panel kept showing a code that pairing had just spent.**

### What running the application found

**The computer paired with itself, and nobody found the step (T051c).** A user
who scanned the QR paired their phone, saw the computer's screen unchanged, and
had no way to guess the page was waiting to be told about itself. Sending *from*
the computer was unreachable in practice.

The host's page is granted a session over loopback now. Principle V accepts "a
one-time code **or** a confirmation on the host device", and reaching loopback is
what being on the host device means — the same boundary
`/api/pair/pending/{id}/approve` already rests on.

> **The cost, stated plainly.** A local process can now obtain a credential
> without knowing a code, which it could not before. It gains little: a process
> running as this user can already read every file fastr could send and write
> into the receive folder. But the trade is real. `test/integration/host_session_test.go`
> pins the restriction, including that a paired device holding a valid
> credential is still refused.

**A page reload, or a second tab, killed the session (T051d).** Pre-existing, and
the worst of the lot. The envelope refuses every counter it has already seen and
the server keeps one high-water mark *per device*, while each page keeps its
counter in memory. So a reload started again at one and had everything refused as
a replay; two tabs locked each other out, the busy one racing ahead and the quiet
one permanently behind. On a phone, reloading is among the most ordinary things
that can happen.

A page now claims a block of counters at load, and a request refused as a replay
is retried once from a fresh block, so pages leapfrog instead of deadlocking.
Nothing is weakened: counters still only ever increase, which is the property the
check rests on.

*Two earlier attempts at a test for this passed with and without the fix and were
thrown away.* The one that survives was checked to fail 2/2 without it and pass
3/3 with it. A test that cannot fail is worse than no test.

**Devices that could not be reached were offered as destinations (T051e).** A
pairing lasts a year, so the list holds every phone ever connected. Sending to a
closed one produced a transfer that sat at nothing and never explained itself.
Holding an event stream open is what makes a device reachable, and opening or
closing one now publishes `device_appeared` / `device_lost` — event types the
contract had already defined but nothing emitted.

**This instance's identity was unstable across restarts.** `deviceIdentity`
returned a fresh ULID on the run that created the record and the literal string
`"self"` on every run after, so a phone that paired on day one addressed a
computer that answered to something else on day two. Schema version 2 drops the
old reserved record.

### Windows

The first push went out green on Linux and red on Windows. One of the three
failures was a real defect, not a test problem:

**An open staging file cannot be deleted on Windows.** A `Sink` released its
handle only on commit, cancellation, or failure, so a transfer that was merely
abandoned — the phone closed, nobody cancelled anything — held one open for the
life of the process. Linux let that pass unnoticed; Windows would have blocked
the retention sweep (T085) from ever clearing the partial data. Exactly the kind
of divergence Principle IV exists to keep out of the domain logic.

The other two were mine: a test measuring NTFS rather than this code, and one
reading a transfer's state while the server was still finishing its bookkeeping.
`TestTransferMovesAFileEndToEnd` had been failing on Windows *before* any of this
work, for the same race; it is fixed.

### Verified

- 293 Go tests, 5 browser tests, 20 cross-implementation crypto vectors.
- CI green on ubuntu-latest, windows-latest, and the browser job — which runs
  **both Chromium and WebKit**.
- Builds for Linux and Windows.

### Real-device result

A 29 MB video went from a physical iPhone to Fedora over plain HTTP on the LAN.
That settles the bets that mattered: pairing works in a non-secure browser
context with `@noble` crypto, the browser's BLAKE2b agrees with Go's on real
data, and the chunked upload path holds end to end.

The other direction — computer to phone — is fixed but has **not** been tried on
a real phone. It never worked before this session, so it is the first thing to
test.

---

## Open, and worth not losing

**Blocks a release**

- **T137, pairing hardening.** A 6-digit code carries ~20 bits, so an observer
  who captures the whole handshake can search the code space *offline* against
  the confirm ciphertext. The online defences do not apply to an offline
  attempt. A PAKE such as SPAKE2 removes this by construction.

**Known gaps**

- **T087b.** A page learns of a transfer only from the event announcing it, so
  anything declared while it was not listening stays invisible to it forever. A
  phone that reloads mid-transfer loses sight of it. Fixing it properly needs
  `/api/queue`, which does not exist yet. `web/tests/e2e/fixtures.ts` waits for
  the event stream before sending purely to step around this; delete that wait
  when T087b lands.
- **The `Lint` CI job has never actually run.** `golangci-lint-action@v6`
  installs a binary built with Go 1.24, which refuses a config targeting Go
  1.27. Fixing the action is one line — and will then surface **24 pre-existing
  issues** (mostly `gosec` on file permissions) measured locally. That trades a
  configuration red for an honest red; clearing the backlog is a real piece of
  work. A decision, not an oversight.
- **Prettier** wants three files reformatted that nothing in this session
  touched: `web/src/crypto/envelope.ts`, `handshake.ts`, `lib/transfers.ts`.
- **T042b**, the tray icon, and **T139**, its graceful degradation. Neither is
  verifiable without a desktop session.
- **T047b**, the 10 GB memory-flatness run. The property is proven at the engine
  level at 512 MB; this is the number SC-003 actually names.
- **T076's notifications** were written but never seen to fire. The wording and
  the fallback are tested; the mechanism is not.

**A design question left open, deliberately**

The desktop and the phone hold one credential per *device*, and the server keeps
one envelope per device. Multiple pages of the same device therefore share a
counter space they cannot coordinate on. The block-and-retry scheme above makes
that survivable, not clean. A per-page session identity — an epoch in the AAD,
or a credential per page — would make it correct by construction, at the cost of
a protocol change and new state on the server. Not attempted here.

**Environment note**

WebKit's Playwright build is Ubuntu-only and will not launch on Fedora: it wants
`libicu74` and `libjpeg-turbo8`, Debian sonames that do not exist there. Run
`FASTR_SKIP_WEBKIT=1` locally; CI has both.
