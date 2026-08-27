# Journal

What was done, why, and what it cost. Newest first.

`specs/001-lan-file-transfer/tasks.md` remains the source of truth for *what is
done*; this file is for the reasoning that does not belong in a task line, and
for what a future session needs in order to pick the work back up.

---

## 2026-08-27 — The key Windows was not actually protecting

Tasks: 150 → 153 of 161. T137b, the Windows half of T145, and two lines that
had been done for four days without being ticked.

`ca.go` wrote the authority's private key with mode 0600 and had done since
T128. On Windows that number is decoration. The platform does not implement
POSIX permission bits: the file reports 0666 back, and who may open it is
decided by an access control list this code never set. What the key got instead
was whatever it inherited from its parent directory.

The task said that was "user-only on a default installation" and therefore
probably fine. **It is worth seeing what "probably fine" measured on a real
machine.** Neutering the fix and reading the inherited list back on this
Windows 11 laptop gives four accounts on the key: SYSTEM, Administrators, and
*two other user accounts on this machine*. That is a temporary directory rather
than `%LOCALAPPDATA%`, so it is not the number a user would get — but it is
exactly the shape of the risk, on the one artefact where anything holding it can
impersonate any site to every phone that installed the authority.

### Protected, or it grants more than it names

`restrict_windows.go` writes the list explicitly, with a POSIX sibling that
returns nil because the mode bits there already are the restriction.

The load-bearing flag is `PROTECTED_DACL_SECURITY_INFORMATION`. Without it the
inherited entries are merged back into whatever is set, and the result grants
strictly more than it names — the failing run above is what that looks like.
With it, they are dropped.

What this does **not** do is put the key beyond an administrator, and nothing on
Windows can: an account holding `SeTakeOwnershipPrivilege` rewrites any list on
the machine. It removes every *other* account that happened to be named on the
parent.

### Two keys, and a directory

The task named the authority's key. `issue.go` writes `server.key` with the same
0600 and the same non-effect, so it is covered too: it is short-lived rather
than permanent, but anything holding it can serve this machine's address to a
phone that already trusts the authority.

The directory's entry is inheritable, so anything written there later starts out
restricted instead of depending on whoever writes it next to remember. Each key
still states its own, because inheritance is the fallback and not the guarantee.

`LoadOrCreate` repairs an authority left behind by a build that set no list.
That call, and not `Load`, because it is the one trusted-mode setup makes — the
moment the user is being told the key stays on their machine. `Load` remains a
read and does not write.

### The tests had to be told what the property is

The first version asserted the list held exactly one entry and failed on the
directory, which holds two: Windows splits an inheritable entry into one that
applies to the directory and one marked inherit-only for its children. Both name
this user. **Counting entries was asserting how Windows chose to write the list
down; the property is that every entry names this user**, and that is what it
checks now.

The repair test widens the list to include the world group and asserts it
widened before repairing it. A repair test that starts from an already-narrow
list passes whether or not anything repairs anything.

All three were checked to fail with the fix neutered and pass with it.

They read the list back as SDDL rather than walking the ACL, because
`x/sys/windows` does not export the structure's entry count — and because a
failure then prints something a person can read.

### The first push was red, and the test was the thing that was wrong

`TestTheAuthorityKeyIsReachableOnlyByThisUser` and its two siblings failed on
`windows-latest` while passing here. The implementation was right on both: the
list named exactly one account and that account was the current user. The
comparison was wrong.

**SDDL does not always write an account as a raw SID.** Some come back as a
two-letter alias, and which form appears depends on the account rather than on
anything this code does. A developer account is `S-1-5-21-…-1001` and prints
in full; the CI runner runs as the built-in Administrator, `S-1-5-21-…-500`,
which SDDL writes `LA`. The test compared strings, so it compared spellings
instead of identities.

`daclOf` now resolves every token through `StringToSid`, which takes both forms,
and the assertion is `sid.Equals(me)`. The raw token is kept alongside so a
failure still names something a person can look up.

This was a known risk when the test was written and was accepted rather than
handled, on the reasoning that the account was unlikely to be a well-known one.
The reasoning was fine and the conclusion was wrong: an assertion that depends
on how an operating system chooses to spell something should compare the thing,
not the spelling. `TestTheListReaderUnderstandsBothSpellings` now pins both
forms, including the exact alias that broke the build.

### The budget nobody had ever measured on Windows

T145 read `/proc`, so SC-018 was a criterion checked on one of the two supported
systems and asserted on the other. The Windows half is `GetProcessTimes` and
`K32GetProcessMemoryInfo` against a process handle, in a file beside the Linux
one.

`K32GetProcessMemoryInfo` is declared here rather than imported, because
`x/sys/windows` does not export it — it has `GetProcessWorkingSetSizeEx`, which
reports the *limits* on a working set rather than what a process is using, and
those answer different questions. Twenty lines against a dependency for one
function, which the budget in research.md would not have had.

The counters are chosen so the two platforms are held to comparable numbers:
kernel plus user time against `/proc/<pid>/stat`'s system plus user ticks, and
`WorkingSetSize` against `VmRSS`. `PagefileUsage` was the tempting alternative
and would have measured something Linux is not measuring — for a parity
criterion, two numbers that are not comparable are worse than one number.

**Windows reads 0 ms of processor time across twelve idle seconds and 18 MB
resident**, against budgets of 1% and 100 MB. Cross-checked against
`Get-Process`, which reports the same 18 MB to the megabyte; the reader was not
trusted on its own, because the first run of a freshly built binary reported
61 MB and that number had to be explained before anything could be written down.
It was a cold start: the loader mapping in a 15 MB executable written seconds
earlier. Three subsequent runs give 18 MB, flat. Worth knowing, because CI always
launches a freshly built binary and may well see the higher figure — still
inside the budget, on a thinner margin than Linux's 11 MB.

### The test was passing on a process that was not running

Found by the number being wrong, which is the argument for cross-checking it.
Before the web bundle existed on this machine the binary refused to start, the
child exited within a second, and the test reported **0 ms and 0 MB: a perfect
score against both budgets.**

`idle_budget_test.go` already worried about exactly this shape of failure — "a
reader that always returned zero would report a perfect idle score for a program
pegging a core, and the test above would be worse than none" — and proved the
*reader* against a busy loop. It never proved the *subject*. So the one thing it
was built to avoid was the thing it did.

Three changes, none of them Windows-specific:

- the instance's output is kept rather than discarded, because what it said
  before stopping is the only useful thing to print;
- it is asserted alive at both ends of the measurement window, with that output
  in the failure;
- a memory reading under a megabyte is an error. Nothing running the Go runtime
  is that small, so that number means a broken reader, and a broken reader
  passes the budget with room to spare.

There is no third implementation and no fallback stub. fastr ships for two
systems, `internal/platform` has files for exactly those two, and a package that
cannot build for a third would fail there long before reaching a stub here. One
was written and then deleted for claiming a support that does not exist.

### The Lint job had never seen a line of Windows code

Found while running the gate locally for the first time, which is the point of
running it locally. The `Lint` job runs on `ubuntu-latest` with the default
`GOOS`, and a linter type-checks one `GOOS` at a time — so every
`//go:build windows` file in the repository was invisible to it. Seven of them,
now including the one that restricts the authority's private key. **The job was
green because it was not looking.** Principle IV makes a failure on either
system block the release, and this job was enforcing that on one.

Three findings had been sitting there, none of them new:

- two `errcheck` on `defer k.Close()` in `platform_windows.go`, where the rest
  of the tree writes `defer func() { _ = x.Close() }()`;
- one gosec `G204` on the PowerShell call in `notify_windows.go`. The argument
  against it is already written, one file away: `notify_linux.go` carries
  `//nolint:gosec // fixed binary, argv arguments, no shell` for the same shape
  of call. It holds harder on Windows, where a shell genuinely is involved and
  the user's text reaches it through the environment rather than the command
  line — which is why the script reads `$env:` values. The Windows file simply
  never got the comment, because nothing ever asked it for one.

The job now runs a second pass with `GOOS: windows`. No Windows runner is
needed: the linter cross-checks from Linux the same way the compiler does.

### Two things about this machine, not about the code

Both cost time before they were understood, so they are written down.

**Smart App Control is enforced on this laptop** and refuses to launch the
integration package's test binary out of the build cache: `go test
./test/integration/` dies with "a policy has blocked this file" before a single
test runs. Every other package is fine. Compiling it to a normal location —
`go test -c -o bin/integration.test.exe ./test/integration/` — and running that
works.

**The gofmt findings `golangci-lint` reports here are not real**, and the
giveaway is that the list changes between runs. The tree is checked out with
`core.autocrlf=true` and carries no `.gitattributes`, so every file on disk has
CRLF endings. Copying all 122 Go files with LF endings and running `gofmt -l`
over them reports nothing. Worth remembering when writing a Go file from a
Windows editor, too: left as it lands, it shows up in `git diff` as the whole
file.

### Verified

- 396 Go tests pass on Windows 11 with go1.27.0, 5 skipped, none failing. Up
  from 393 passing and 7 skipped: two of the skips are now measurements.
- The three access-list tests fail 3/3 with the restriction neutered.
- Both SDDL spellings resolve to the same account, `LA` included.
- The Windows memory reader agrees with `Get-Process` to the megabyte.
- `go build -trimpath ./...` clean; `go vet ./...` clean for both `GOOS`.
- `golangci-lint` clean under `GOOS=windows` and `GOOS=linux`.
- Not run here: the browser suite, and anything needing a Linux runner. Neither
  is touched by this change.

### Cost

One new platform file and a no-op sibling, three calls in `ca.go` and
`issue.go`, one new test file, three one-line fixes in `internal/platform`, one
step in the CI workflow, and the idle budget split into a shared test with a
per-platform accounting file. No new dependency: `golang.org/x/sys` was already
direct, for the instance lock.

---

## 2026-08-25 — One thing to do, and drawers for the rest

Tasks: 149 → 150 of 161. T151, the interface.

Nothing was wrong with any panel taken on its own. The problem was that there
were nine of them and every one was drawn exactly the same way: the same border,
the same radius, the same 1rem heading, each component carrying its own copy of
that CSS. A page built that way has no hierarchy at all — it cannot say what it
is for, because everything on it claims the same importance.

What that cost is specific rather than aesthetic. **Principle VI puts a ceiling
of three actions on sending a file once two devices are paired**, and the send
zone sat fifth in a stack of identical cards. Finding it spent an action the
principle does not budget for.

### One component owns the decision now

`web/src/lib/Panel.svelte` replaces the copy in all nine. It takes a `tone` —
`hero`, `plain`, `quiet`, `urgent` — and that is where the hierarchy lives, in
one file, instead of being re-decided by whoever wrote the next panel. The
second half is folding: anything that is not the current task sits behind its
own title with one line of state next to it, so the resting screen is one thing
to do and six lines saying where to look.

The hero is whichever of two things the screen is actually for. With no phone
connected it is the invitation, because connecting one is the only thing worth
doing; with one connected it is the send zone and the invitation folds down into
the drawers, which also stops a live pairing code sitting on a screen anybody
can walk past.

### What is not allowed to fold

Three things, and each for a reason that outranks tidiness.

- **The protection notice.** Principle V makes stating what is and is not
  protected a duty, and SC-016a checks it on every screen where a transfer is
  set up or shown. It sits directly under the send zone, which is both places at
  once. It does not fold and it is not dismissible.
- **A device waiting for approval.** Somebody on the other side of the room is
  looking at a spinner. It outranks even the send zone.
- **Anything live.** A running transfer, a queue with something in it, a file
  passing through this machine: each opens itself and stays open while it holds
  something, and folds away when it does not.

### The audits had to be told about folding

This is the part that would have rotted quietly. `a11y.spec.ts` runs axe over
the whole page, `i18n_coverage.spec.ts` reads every visible string, and
`protection_honesty.spec.ts` reads every sentence about files. A folded panel
renders nothing, so all three would have gone on passing while checking half the
interface — and the honesty rule in particular would have become satisfiable by
saying the wrong thing somewhere folded.

All three now unfold every panel before reading the screen. A presentation
decision must not be a way to shrink a gate.

### Cost

About 4 kB of CSS, one new component, nine components losing their duplicated
card styles, and ten catalogue keys in both languages for the state lines. Three
tests learned to open a drawer: the trusted-mode walkthrough is one now, and
`readPairingCode` unfolds the invitation before looking for the reveal button.

### Verified

- 23 browser tests on Chromium, the full Go suite, `svelte-check` and `eslint`
  clean. The accessibility gate passes with **every drawer open**, which is
  strictly more than it audited before.
- Read off screenshots rather than assumed: the idle desktop, the desktop before
  pairing, a transfer in flight, the phone, and dark mode. The empty
  `Transfers` panel was a full-height card saying nothing in the first pass;
  that is what the screenshots were for.

---

## 2026-08-24 — The pairing code stops travelling

Tasks: 148 → 149 of 160. T137, the one thing the README said blocked a release.

The task was "implement a PAKE or document the accepted risk". Implemented
**CPace**, the CFRG's balanced password-authenticated key exchange, in the
CPACE-RISTRETTO255-SHA512 suite of draft-irtf-cfrg-cpace-14.

### The flagged risk was the second-worst thing here

What was written down, in `research.md` and in the README, was an offline
dictionary attack: six digits are about twenty bits, and anyone who could play
the host's part once kept a confirmation tag they could test against all 10^6
candidates at their leisure. Real, and worth fixing.

Reading `handlePairConfirm` to fix it turned up something plainer. The joining
device **put the six digits in the request body**:

```go
type pairConfirmRequest struct {
	HandshakeID string `json:"handshake_id"`
	Code        string `json:"code"`      // ← on the wire, in the clear
	Proof       string `json:"proof"`
	...
}
```

Simple mode is plain HTTP by construction — a browser grants no secure context
to a LAN address, so there is nothing to encrypt this with before a key exists.
An observer did not need an offline search. They read the code.

That was never the intent: the derivation mixed the code in precisely so it
would not have to travel, and then a second code field was added so the server
could check it against its own. Both halves are defensible on their own. It is
why the tests that came out of this assert on **recorded traffic** rather than on
the shape of a struct: a rule about what a request may contain is one somebody
re-adds in good faith.

### What CPace buys, precisely

Both sides map the code to a group generator only they can compute, and exchange
elements over it:

```
G  = map_to_ristretto255(SHA-512(generator_string(DSI, code, host_id, sid)))
Ya = ya·G          Yb = yb·G          K = ya·Yb = yb·Ya
```

The code never leaves the device holding it. Everything on the wire is
independent of it — the two elements are uniformly distributed whichever six
digits produced them — so no candidate can be tested against any of it.

And an attacker who *answers* has to choose their candidate **before** computing
their message. Relating their generator to the honest one is a discrete
logarithm. So one interaction tests exactly one of the million codes and spends
one of the five attempts. That is what makes twenty bits an acceptable amount to
ask a person to retype: not the entropy, the cost of a guess.

### Why CPace and not SPAKE2

The earlier note said SPAKE2, which would also work. The deciding argument was
not cryptographic.

This protocol has **two implementations that must agree byte for byte**, in Go
and in TypeScript, and no way to test one against the other except by testing
them against each other. Two implementations agreeing proves they made the same
mistake just as readily as it proves they are right. The CPace draft publishes a
complete test vector for exactly this suite — generator string, hash, generator,
both scalars, both messages, K, and the ISK — and both sides now reproduce it:
`internal/pairing/cpace_test.go` and `web/scripts/verify-crypto.ts`. That is a
third party's answer, and it is the only external evidence available offline that
this is CPace rather than something shaped like it.

It paid for itself immediately. The Go implementation failed the vector on the
first run — on my transcription of the vector, not on the code, which is exactly
the kind of thing that would otherwise have been "fixed" in the implementation.
The test now keeps the draft's own line breaks so a slip shows up as a line that
is not 56 characters.

### The mistake that would have been silent

`ClientComplete` and `HostComplete` are two functions rather than one with a
flag. The only difference between them is which message is the peer's, and
multiplying by your *own* message instead of the other side's produces a key
nobody else can reach — silently, with every self-consistency test still
passing, because a single implementation talking to itself is consistent.

The vector generator caught it, because it checks that the two sides land in the
same place from different secrets. Both the committed vectors and the Go test now
run both halves for that reason.

### The accounting had to move

A guess used to be counted where the code was compared. There is no comparison
now: the host feeds the code to a derivation and finds out a round trip later
whether it was the right one. So `Codes.Verify` became `Codes.Live` — admit a
guess, enforce the growing delay — and `Codes.Settle(code, ok)`, which records
how it turned out.

`Settle` names the code rather than assuming the current one. Three minutes is
long enough for the host to have moved on, and counting a stale failure against a
fresh code would spend a budget the attempt never touched.

Starting an exchange and walking away therefore counts for nothing — and resets
nothing, which is the part with a test of its own. If abandoning an attempt
cleared the delay, five guesses spread over three minutes would become five as
fast as the network allows.

### Verified

- The draft's appendix B.3 vector, in Go and in TypeScript, at every intermediate
  step rather than only the final key.
- 33 cross-implementation vectors, both sides of each exchange.
- 409 Go tests, 23 browser tests on Chromium, both lints clean.
- Two secrecy tests asserting the code appears in no recorded body: one over the
  real endpoints, one driving the client that actually ships. **Both verified to
  fail** by putting the code back in the confirm request.

### Cost

Protocol version 2. Existing pairings are invalidated and devices pair again,
which is one screen. The browser bundle grew about 8 kB compressed for the
ristretto255 arithmetic. One new dependency, `github.com/gtank/ristretto255`.

**`ristretto255.Point.hashToCurve` is deprecated in @noble/curves and is
deliberately still used.** The replacement it points at runs expand_message_xmd
with a domain separation tag first, which is a different function landing on a
different point. Following the deprecation would break pairing between any two
builds that disagreed about it. The vector test is what would catch that.

---

## 2026-08-23 — The audits, and the two things they found

Tasks: 141 → 146 of 159.

Four audits: accessibility, announcements, translations, and honesty about
simple mode. Each is the kind of test that looks like bookkeeping until it runs,
and two of them found defects nothing else could have.

### French was decorative

The catalogue was complete. Negotiation picked French correctly. `<html
lang="fr">` was set. And the interface rendered **English**.

`t()` reads a plain module variable, and the language was set in the shell's
`onMount` — after every component had already rendered with the default.
Nothing re-runs when that variable changes, so the second catalogue was a file
nobody ever saw. Every catalogue test passed throughout, because they compare
the catalogues to each other and never to a screen.

Setting the language before the first render fixes it. A *runtime* override
(FR-039b) needs more: the current language has to be reactive state rather than
a module variable. There is no override control yet, and the note is where the
person who builds one will look.

### An unnamed control, at critical severity

The hidden file inputs behind "Choose files" had no accessible name. They are
visually hidden but were still in the accessibility tree, so somebody using a
screen reader met an unlabelled file input next to a button that does the same
thing.

Hidden from assistive technology now rather than labelled, because two controls
doing one job is worse for that person, not better — with `tabindex="-1"`, since
hiding something still focusable is its own failure.

### What the audits are for, stated once

- **Accessibility** is axe-core *plus* keyboard-only traversal. axe finds a
  missing name and is blind to whether the flow can be completed; only driving
  pairing and sending from the keyboard answers FR-039g.
- **Announcements** are *counted*. A progress bar that announced every update
  would speak four times a second and the person would turn the whole thing off.
  Announcing too much is how a product becomes unusable while passing every
  check for whether it announces at all. The test also asserts the receiving
  device is told something, because a count check against an empty list proves
  nothing.
- **Honesty** flags a reassuring word only in a sentence about files, and
  excludes the trusted-mode panel by region. The first version flagged "pairing
  and credentials are encrypted" — true, precise, about something else — and a
  check that pushes the interface towards saying *less* about what is protected
  is the opposite of the point.

Each was verified to fail: a deleted translation key, an injected "Your files
are sent securely", an unlabelled input.

### Verified

391 Go tests, 22 browser tests, `golangci-lint` clean. The pairing fixture
speaks both languages now, because a fixture that only knows English can only
test an English product.

---

## 2026-08-23 — Trusted mode, as far as it can go without a phone

Tasks: 131 → 141 of 159. Phase 9 is complete except for the two things that
need hardware, and those are marked rather than glossed.

### What it is, and why it exists at all

Not a preference. Browsers grant a secure context only to loopback, and outside
one there is no service worker — so nothing can decrypt a stream while the
browser writes it to disk, so a large encrypted file could only be received by
holding it whole in memory, which iOS will not allow. A certificate the device
already trusts is the **only** route to that context on a LAN address.

Which means asking somebody to install a certificate authority on their phone.
That is a real security decision: anything holding its private key can
impersonate any site to that device. So the key is generated per installation
and never shipped — a shared authority would be a master key for every fastr
user alive — written 0600 in a directory created 0700, and never handed out. The
certificate carries a fingerprint to compare against what the phone displays,
because otherwise "install this" means "trust whatever arrived".

### Alongside, never instead

The TLS listener runs beside the plain one. A phone that never set trusted mode
up keeps working exactly as before; a machine where nobody set it up serves no
TLS and creates no key at all, which was verified by running the binary and
finding no trust directory.

Two ports rather than one port sniffing its first bytes. Sniffing works, and it
is one more thing that can be wrong on a protocol whose whole job is to be
trustworthy.

Whether a request is trusted is read from the connection and never from a
header. `/api/trust/verify` rests entirely on that: it can only succeed over the
TLS listener, so a phone asserting over plain HTTP that it is trusted is
asserting precisely what it has not done.

### Three defects, all found by tests

- **A confirmed downgrade overrode `require_trusted`.** The user set that device
  to never connect in the clear; a confirmation on one transfer is not a reason
  to override a standing instruction, or the setting would mean "ask me", which
  is what the other one already means.
- **Setup on a machine with no network address returned a 500.** That is a
  laptop with its Wi-Fi off — something a person can fix — so it says so now.
- **`init` answered unsealed while the client called it sealed.** Found by the
  browser test, which is the only place the two halves meet.

And one the linter found: `MaxPathLen` alone means *unset* in Go's x509 struct,
so the authority had quietly permitted intermediate authorities — a longer chain
of trust than the user agreed to when they installed it.

### What is left, and why it is left

**T133, the service worker**, and **T135, verifying the setup on real hardware.**
Both need a physical phone. A service worker exists only in a secure context, so
that code cannot run — let alone be tested — until an actual device has
installed the authority and reached the HTTPS origin. Playwright over a LAN
address is not a secure context either, so no browser test substitutes for it.

The walkthrough's instructions are written from documentation rather than from
having performed them. iOS in particular has two separate steps — install the
profile, then enable full trust under Certificate Trust Settings — and people
miss the second.

**T137b**, the Windows key permissions, is the other open one, and CI found it:
Windows does not implement POSIX permission bits, so the authority's key is
written 0600 and reports 0666. In practice it inherits the ACL of
`%LOCALAPPDATA%`, but that is the operating system's arrangement rather than a
guarantee fastr makes, and this is the most sensitive artefact in the product.
It blocks trusted mode on Windows and nothing else.

### Verified

391 Go tests, 11 browser tests, `golangci-lint` clean. The capture test that
holds SC-016 runs the same payload over a plain connection as a control,
because a test that finds nothing proves nothing.

---

## 2026-08-23 — User Story 6: relaying between two phones

Tasks: 121 → 131 of 159. Phase 8 complete, and with it all six user stories.

### Staged, not piped, and why that is the whole design

The desktop-to-phone path joins a fetching receiver to a supplying sender and
never touches the disk. The obvious move was to reuse it here. It cannot work:
the supplying side is a phone, and a phone holding one streaming request open
for the length of a file loses the whole thing the moment its screen locks —
which is the failure User Story 3 exists to prevent.

So a relay has **two halves**. The sending phone pushes chunks exactly as it
would to the computer itself; the bytes wait on disk, verified; the receiving
phone fetches them exactly as it would fetch anything else. It costs a round
trip through the disk and buys a transfer that survives a locked screen on
either side. Neither half needed a mechanism of its own, which is the argument
for it: resume and Range both came for free.

A consequence worth stating: a relayed item that has finished uploading sits in
`verifying`, not `completed`. Marking it complete when the sender finished would
tell them the file had arrived somewhere it had not.

### Holding data that is not ours

This is the only place a machine ends up holding someone else's files, and every
rule follows from taking that seriously.

- Relayed bytes go to a **directory of their own**, so "never appears as a file
  this computer received" is a property of the layout rather than a promise
  about cleanup code.
- They are deleted on **every** ending — completed, cancelled, failed,
  abandoned — and the retention sweep clears what a process killed mid-relay
  left behind. FR-055 has no exception for a crash.
- The relaying user can **see what is passing through and stop it**, with how
  much is on their disk read from the filesystem rather than from a record.
- FR-054, trust is never transitive: both phones must be paired here. A device
  identifier is not a secret — it travels in the device list every paired device
  can read — so this rests on asking about the target's pairing every time, not
  on nobody knowing it.

### A topology thirteen tests had wrong

Classifying two paired phones as a relay broke six pipe tests, and the reason
was worth the interruption: they were sending **phone to phone** to exercise the
desktop-to-phone pipe. That topology does not exist in the product. Two phones
going through this computer is a relay; the pipe is for the *host* supplying to
a phone, which is the one case where the sender can hold a request open.

Thirteen tests now send from `h.host(t)`. They were passing, and they were
testing a shape the product does not have.

### Verified

369 Go tests, 9 browser tests, `golangci-lint` clean. SC-019 is checked for all
four endings rather than the happy one — and writing that found a race in the
test itself: a client's read finishes when the last byte arrives, while the
server is still finishing the transfer, so "nothing remains after it ends" has
to wait for the ending it is asking about.

---

## 2026-08-23 — User Story 5: controllable, and trustworthy

Tasks: 105 → 121 of 159. Phase 7 complete.

### A task worth correcting rather than satisfying

T104 asked for "the sequential queue runner". No runner was written, and that is
the point: **a runner implies the server drives transfers, and it cannot.** In
both directions the bytes come from a browser — pushed as chunks, or supplied
into a pipe — so nothing on this machine can start anything. The queue does not
run transfers; it decides which one is allowed to move when its owner next
tries.

The invariant therefore lives where it can actually be enforced, in
`store.Activate`: the single place a transfer can take the active slot, and so
the single place it can be refused. Ten transfers all trying to start in the
same instant produce exactly one winner, which is what SC-021 asks and what a
runner would have made harder to prove.

### The gap that was actually there

Pairing answers "may this device talk to me at all". The trust mode answers a
narrower question that comes up on every transfer: **may it write to my disk
without anyone looking?** The two are separate because a pairing lasts a year
and the second answer changes inside one — a phone that was mine becomes a phone
I lent to someone.

`ask` mode existed in the store and in the interface and did nothing. Now an
incoming transfer from such a device waits in `awaiting_acceptance`, and nothing
it sends reaches staging before a human answers. Nobody answering is also an
answer, after two minutes: FR-016d, because a transfer queued forever holds both
the sender's attention and a place in a queue that runs one thing at a time.

That required amending the state machine. Accepting returns a transfer to
`queued` rather than jumping to `running`: acceptance and scheduling are
different questions, and jumping would take the active slot from whatever is
already using it.

One thing the tests caught: accepting a transfer that had already ended returned
success. Two taps on a button should not fail, but "fine" for something that
timed out or was declined is the opposite of the truth. It now reports which.

### The history had no endpoints at all

`RecordHistory` had been writing entries since User Story 1 and nothing could
read them. It is **this machine's** history rather than a per-device view,
because the person asking is the one sitting here and what they want to know
includes the phone that is no longer in the house. Clearing is loopback only: a
paired phone erasing the record of what it sent is exactly backwards.

Every row says which protection mode was used. In simple mode the content was
readable by anyone on the network, and Principle V's honesty duty makes it the
user's business which of their transfers that was true for.

### Two shapes changed on the way

**T112 was folded into the existing device list** rather than built as a second
`DeviceSettings.svelte`. FR-016c asks for the trust mode to be visible *wherever
paired devices are listed*, and a second panel listing the same devices would
have given the user two lists and a question about which one to use.

**A defect the browser test found:** the queue view refreshed only when a
transfer *ended*, so a newly queued one never appeared until something else
finished — which is precisely backwards for a panel whose job is to show what is
waiting. It now reads the queue back on the events that change it, and
deliberately not on progress, which arrives four times a second and never moves
anything.

### Verified

351 Go tests, 9 browser tests, `golangci-lint` clean, Windows builds. The queue
test reloads the page after reordering, so what it asserts is the order the
server holds rather than the one the page remembers.

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

It never had, and the reason recorded here for months was wrong. The theory was
a Go version mismatch: a prebuilt linter built with an older toolchain refusing
a config that targets a newer one. The actual cause, visible only once the job
got far enough to say anything at all, is simpler. **The action's major version
has to match the linter's.** This job sat on `@v6`, which installs golangci-lint
v1, while `.golangci.yml` is a v2 config — so every run died on "the
configuration contains invalid elements" before reaching a single linter. `@v9`
installs v2.

The linter version is pinned rather than `latest`, so a new release cannot turn
this red on a morning when nothing in the repository changed.

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

- ~~**T137, pairing hardening.**~~ Done 2026-08-24; see that entry. CPace, and
  the code no longer travels at all.
- **T149**, the quickstart walked through on real Android and iOS hardware.
  Needs a phone in hand.

**Known gaps**

- ~~**T087b.** A page learns of a transfer only from the event announcing it.~~
  Closed with User Story 5: `/api/queue` exists, `App.svelte` reconciles against
  it on connect, and the wait in `web/tests/e2e/fixtures.ts` that stepped around
  it is gone.
- ~~**The `Lint` CI job has never actually run.**~~ Fixed 2026-08-23; see that
  entry. It runs, and it is clean.
- ~~**Prettier** wants three files reformatted.~~ Done; the tree is clean.
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
