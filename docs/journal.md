# Journal

What was done, why, and what it cost. Newest first.

`specs/001-lan-file-transfer/tasks.md` remains the source of truth for *what is
done*; this file is for the reasoning that does not belong in a task line, and
for what a future session needs in order to pick the work back up.

---

## 2026-08-23 — User Story 4: several devices on the network

Tasks: 95 → 105 of 159. Phase 6 complete.

### The library decision the plan deferred

research.md item 8 chose `libp2p/zeroconf/v2` and **flagged its maintenance to be
confirmed at implementation time**, naming `hashicorp/mdns` as the fallback.
That check came due, and the flag fired: zeroconf's last release is four years
old, the fallback's is two months. `brutella/dnssd` was weighed and rejected —
newer, but it pulls Linux-specific netlink plumbing that adds a parity risk on
Windows for no benefit. research.md is amended with the reasoning rather than
quietly updated.

The fallback offers a query where the other offered a subscription, and neither
matters: a query whose listener stays open *is* a subscription, because
responders announce themselves unprompted when they start. That is why discovery
lands in about 50 ms and why the idle cost is one re-query a minute rather than
a poll.

### The record is a hint, never an authority

This is the rule the whole package is built on. A service record can outlive the
process that published it, and anything on the network can publish one. So:

- The record populates the list; **`/connect` decides the dot**. A machine that
  closed its lid leaves a record behind, and offering it as a destination
  produces a transfer that sits at nothing.
- An address answering as a *different* device is refused. DHCP reassigns
  addresses constantly, and sending a file to whoever currently holds one is
  precisely what must not happen.
- Nothing in a record is ever an input to an access decision. Being on the
  network buys an address to connect to; pairing is still a code typed by a
  human.
- A record naming a public address is dropped at parse time rather than probed.

### The first outbound request this project has ever made

Reachability is the first time fastr connects *out* to anything, so it came with
`internal/localnet`: the address classifier moved there from the HTTP layer, and
it now also owns the only HTTP client the application is allowed to build — one
whose dialer refuses a non-local address, whose redirects are refused outright,
and which ignores proxy environment variables. The linter already forbade
`http.Get`, `http.DefaultClient` and `net.Dial`; this is what it was forbidding
them *in favour of*.

The manual address field is the one place in the product where a person can name
an arbitrary host. A public address is refused with a reason, and so is a host
name — resolving one is itself a request off the local network.

### A flaky browser test, and what it actually was

Adding discovery made the browser suite fail intermittently, which looked like
discovery slowing the server down. It was not: the same tests fail under the
same artificial load on the commit *before* discovery, verified in a worktree.
Two real problems came out of chasing it:

1. **Reconciliation waited for the event stream.** A page could read the queue
   as soon as it had a session, but only did so when SSE connected — so a page
   whose stream was slow showed no transfers at all. It now reconciles on load
   as well. FR-036 asks for transfers in progress to be visible, not for them to
   be visible once SSE agrees.
2. **The test measured with the wrong instrument.** It counted upload requests
   with a `page.on('request')` listener, which under load reported none while
   the file demonstrably arrived. Measuring through a **route** instead — the
   same mechanism that withheld the chunk — is authoritative: a request that is
   not intercepted is not sent. The assertion is now bytes and offset rather
   than a count, which is also a better statement of what SC-005 means.

### Windows, and what CI can and cannot say about discovery

The first push went green on Linux and red on Windows: the two wire-level
discovery tests saw nothing. The cause is the runner, not the code — a machine
that silently drops its own multicast is indistinguishable, from inside, from a
network with nothing on it, which is exactly what those tests were reading as a
failure.

They now **probe rather than guess**: a throwaway service is published and
looked for once per run, and if it does not come back then nothing on that
machine could. Where the probe passes, a failure is a real failure. Where it
does not, the tests skip with that reason.

The honest consequence, recorded here rather than papered over: **discovery is
not verified on Windows by CI.** Everything that does not need the wire is —
the record's contents, reachability, name disambiguation, the manual fallback —
and the Windows build and the rest of the suite are green. A real two-computer
test before a release would close it.

### The `Lint` job now actually runs

It never had. `golangci-lint-action` downloads a prebuilt binary, and a binary
built with an older Go refuses a config targeting a newer one, so the job failed
before checking anything. `install-mode: goinstall` builds it with the toolchain
the job already sets up.

Turning it on surfaced 26 issues, all pre-existing and all small. They are now
zero, and the split is worth remembering: about half were real (unchecked closes,
a deprecated bbolt symbol, a test helper that leaked an `*http.Response` whose
body it had already closed), and about half were deliberate decisions the code
had simply never justified in writing — 0755 on the user's own receive folder,
0644 on received files, constructed paths that gosec cannot see are constructed.
Those carry a `//nolint` with the reason now, which is more useful than the
silence they had before.

### Verified

326 Go tests, 7 browser tests, three full browser runs under load, and a clean
`golangci-lint run`. The real binary was checked to be discoverable from a
separate process, at its LAN address, with reachability confirmed.

---

## 2026-08-22 — User Story 3: surviving an interruption

Tasks: 83 → 95 of 159. Phase 5 complete.

### The shape of it

A dropped network is the ordinary case on a phone, and the whole story is about
making it cost a round trip instead of a file. Three pieces, and the middle one
is the one that was missing:

1. **The server already kept the resume point.** An offset is only acknowledged
   after an `fsync`, and a short copy is still committed, so a severed
   connection loses nothing that arrived. `TestAnInterruptedUploadResendsAlmostNothing`
   cuts a connection at the TCP layer — a raw socket, a half close partway
   through the body — and measures re-sent bytes at zero. SC-005 allows 1%.
2. **Nothing brought the sender back.** `web/src/lib/resume.ts` now does, and
   the design worth remembering is that *retrying is resuming*: the upload asks
   for the committed offset before sending anything, so running an item again
   continues it. Nothing replays a request; there is no queue of pending writes.
3. **Nothing survived a reload.** See T087b below.

### What changed underneath

**Every error used to interrupt a transfer, including a full disk.** The sender
would then retry forever into a destination that could never accept it, while
the interface said "interrupted". `transfer.Classify` separates the two, and its
default is interruption on purpose: a transfer wrongly interrupted keeps its
bytes and is swept after seven days, one wrongly failed throws them away now.

**A `running` transfer found at startup is a lie a dead process left holding the
single active slot.** Until `Transfers.Recover`, that ghost blocked every real
transfer behind it (FR-035a allows exactly one at a time). They become
interrupted, which is the state they were always in.

**The retention sweep fails abandoned transfers with a cause and records them in
history before removing them.** A transfer that simply vanished would be
indistinguishable, from where the user sits, from one that was lost.

### T087b, and what removing a workaround exposed

A page learned about a transfer only from the event announcing it. `GET /api/queue`
now answers that on connect, and it needed no new route: a transfer that is
neither terminal nor forgotten is either the active one or a waiting entry, so
the queue *is* the reconciliation set.

The wait in `web/tests/e2e/fixtures.ts` that stepped around this is gone — which
is how the suite proves the fix. Removing it broke a test, and the reason was
not the fix: **`pair()` never actually waited for pairing.** It waited for the
region named "Connect this phone" to disappear, and the pairing screen merely
swaps that heading to "Waiting for approval" while it polls. The check passed
the moment the code was submitted, before anyone had approved anything and
before a credential existed. The stream wait had been covering the gap for as
long as both existed. A helper that lies about its postcondition is worse than
one that is slow.

### Defects found by writing the tests

- **A failed transfer could be completed a second time**, and that attempt
  re-opened a sink and re-created the staging file the failure had just deleted.
- **`Declare` stamped `queued_at` from the wall clock** while every other
  timestamp came from the store's, so a record disagreed with itself. Found
  because a retention test passed with *and* without its fix until this was
  corrected.
- **Failure causes rendered as bare codes.** `error.destination_full` and three
  others did not exist, so a failed transfer showed the literal string
  `error.abandoned`. FR-038 exists to forbid exactly that.

### Added to the model

`last_progress_at` on Transfer (data-model.md amended). Without it the sweep
cannot tell a transfer nobody has touched in a week from a 10 GB file crossing a
bad link, and this story exists for the second one. It costs nothing: the record
is rewritten on every committed offset anyway.

`destination_unwritable` as a failure cause, because "the disk is full" and "the
folder is read-only" have different corrective actions and FR-038 promises an
action rather than a category.

### Verified

311 Go tests, 7 browser tests. The two claims worth doubting were checked by
breaking the code: the resume test fails without the resume path, and the
sweep's "slow is not abandoned" test fails without `last_progress_at`.

---

## 2026-08-22 — Computer to phone, on a real phone

**User Story 1 works on real hardware.** A file went from Fedora to a physical
iPhone over the LAN, which is the direction that had never been tried outside a
browser test. Both directions are now confirmed on a real device, and nothing in
the two-mode encryption story or the non-secure-context bet is left unverified.

One thing came back from that run. The send panel preselected the only paired
device even when it was closed, so its resting state read "Phone — not
connected": a select shows its first option, and there was no other. T051e had
already made reachability *visible*, which turned out to be half the fix. Only a
reachable device is chosen for the user now; otherwise the box asks for a
choice.

The browser test for it (`us1_first_transfer.spec.ts`) pairs a phone, closes its
page, waits for the computer to actually observe the loss, then reloads and
asserts the placeholder. Checked to fail on the assertion that matters without
the fix.

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
test. *(Done on 2026-08-22; see the entry above.)*

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
