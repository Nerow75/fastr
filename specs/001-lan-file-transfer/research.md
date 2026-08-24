# Phase 0 Research: LAN File Transfer

**Date**: 2026-08-20 | **Plan**: [plan.md](./plan.md)

Every unknown carried into planning is resolved below. The first finding is the one that shaped
everything else, so it comes first.

---

## 1. The browser secure-context wall

**Decision**: Accept that the default mobile experience runs outside a secure browser context,
and design around the capabilities that are actually available there. Provide an opt-in trusted
mode that obtains a secure context properly.

**Rationale**: Browsers grant a secure context to loopback addresses only. A local network
address such as `192.168.1.20` is never a secure context, regardless of what is served over it
([MDN, Secure contexts](https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Secure_Contexts)).
Outside a secure context, `crypto.subtle` is undefined
([MDN, SubtleCrypto](https://developer.mozilla.org/en-US/docs/Web/API/SubtleCrypto)) and service
workers cannot register.

The service worker restriction is the decisive one. A service worker is the only mechanism that
can sit between a download and the disk, decrypting a stream as the browser writes it. Without
one, receiving an encrypted 10 GB file would mean assembling it whole in memory before handing it
to the browser, which iOS will not permit at that size.

Serving HTTPS with a self-signed certificate does not escape this: the browser shows an
interstitial security warning, and FR-044 forbids exactly that. The only way out is a certificate
the phone already trusts, which means installing a certificate authority on the phone.

**External confirmation**: LocalSend, the closest comparable project, hits the same wall. Its
device-to-device transfers use TLS with self-signed certificates because both ends are its own
application, but its browser fallback mode serves plain HTTP precisely because browsers reject
self-signed certificates ([LocalSend protocol](https://github.com/localsend/protocol),
[localsend.org](https://localsend.org/)).

**Alternatives considered**:

- *A publicly trusted certificate for a private address.* Products such as Plex solve this with a
  registered domain whose DNS resolves to a LAN address, plus a real wildcard certificate. It
  requires internet-dependent DNS resolution at connection time and a domain the project
  controls, which violates Principle I outright.
- *WebRTC data channels*, which are encrypted by DTLS regardless of context. Encryption would be
  free, but the received bytes still land in JavaScript with no way to stream them to disk. It
  solves the wrong half of the problem and adds a substantial protocol surface.
- *Chunked download with in-memory decryption.* Chrome backs large blobs with disk and might
  survive; iOS Safari will not. Rejected as unreliable on a committed platform.

---

## 2. Implementation language

**Decision**: Go 1.24 or newer.

**Rationale**: It produces a static binary per platform with no runtime for the user to install,
which Principle VII's "clone, build, run" promise and the constitution's distribution constraint
both require. Cross-compilation to Linux and Windows from a single machine is a first-class
operation, which keeps CI and release simple. `io.Copy` gives constant-memory streaming from the
standard library rather than from a dependency. Most importantly for a public repository
maintained by one person, Go is the lowest barrier to a drive-by contribution among the
candidates.

**Alternatives considered**: Rust offers stronger memory and throughput guarantees, but Go already
meets every number in the success criteria, and the contribution barrier is materially higher.
C# with ahead-of-time compilation produces a single binary and is excellent on Windows, but its
mDNS and tray ecosystems are less comfortable on Linux. Node with Electron was rejected outright:
it contradicts the self-contained binary constraint and the idle memory budget.

**Constraint accepted**: everything must build without cgo, so that cross-compilation stays
trivial. This rules out the standard SQLite driver and any tray library that requires C bindings
at build time.

---

## 3. Web application framework

**Decision**: Svelte with TypeScript, built by Vite, embedded through `go:embed`.

**Rationale**: The interface has real state to coordinate: a live device list, a reorderable
queue, per-item progress, and pairing flows. Hand-rolling that in vanilla TypeScript gets messy
quickly, especially with translation and accessibility layered on. Svelte compiles away, so the
shipped bundle stays small, which matters when it is served to a phone over a first connection.
Node is a build-time tool only; the user never installs it.

**Alternatives considered**: React carries a runtime the bundle does not need. Vanilla TypeScript
avoids the build step but pushes reactivity, i18n interpolation, and focus management into hand
maintenance. Server-rendered HTML with progressive enhancement was tempting for accessibility, but
live progress and queue reordering want a client-side model.

---

## 4. Cryptography available without a secure context

**Decision**: CPace, the balanced password-authenticated key exchange selected by the CFRG, in the
CPACE-RISTRETTO255-SHA512 suite of
[draft-irtf-cfrg-cpace-14](https://datatracker.ietf.org/doc/draft-irtf-cfrg-cpace/), with
ChaCha20-Poly1305 as the AEAD. On the Go side, `github.com/gtank/ristretto255` and
`golang.org/x/crypto`. On the web side, `@noble/curves` and `@noble/ciphers`.

**Rationale**: The pairing exchange and every credential must be protected even in simple mode,
where the page is served over plain HTTP and `crypto.subtle` does not exist. The noble libraries
are audited, dependency-free, and implemented in pure JavaScript, so they work in a non-secure
context where the native API does not. ChaCha20-Poly1305 outperforms AES-GCM on phones lacking
hardware AES, which is the relevant case here.

**A six-digit code carries about twenty bits, and that number decides the design.** Twenty bits is
an acceptable secret only if every attempt to use it costs an interaction. It is a worthless one
the moment anything can be tested without the host's participation. CPace is what makes the first
true: the code is mapped to a group generator on each side and never sent, so the whole transcript
is independent of it, and an attacker has to commit to exactly one candidate before the exchange
begins. One interaction, one guess, five of them, then the code dies (FR-013).

**Why not the design this replaced.** The first implementation was an ordinary X25519 agreement
with the code stirred into HKDF, and it failed in two ways that were found by writing the tests
this section now points at:

- The joining device **sent the code in the body of the confirm request**. Simple mode is plain
  HTTP by construction, so an observer did not need an attack; they read the six digits. This was
  never the intent of the design, and it is the reason
  `test/integration/pairing_secrecy_test.go` asserts on recorded traffic rather than on a struct.
- Even with the code withheld, the confirmation tag was an **offline oracle**. Anyone who could
  play the host's part once — a rogue mDNS record, a spoofed address, a captive network — kept a
  tag testable against all 10^6 candidates at their leisure. The online defences do not apply to a
  search run offline.

**Why CPace rather than SPAKE2.** SPAKE2 was the earlier note's suggestion and would also work.
CPace is what the CFRG selected for the balanced case, it needs no nothing-up-my-sleeve points,
and, decisively for a project with two implementations that must agree byte for byte, its draft
publishes a complete test vector for exactly this suite. Both implementations check it: appendix
B.3, verified in `internal/pairing/cpace_test.go` and `web/scripts/verify-crypto.ts`. Two
implementations agreeing with each other proves only that they made the same mistake; agreeing
with a third party's published answer is the evidence available offline that this is CPace rather
than something shaped like it.

**Cost**: the browser bundle grew by about 8 kB compressed, for the ristretto255 arithmetic.
Devices paired under the earlier protocol have to pair again, which is one screen.

**Resolved**, and the release note it was blocking is gone with it.

---

## 5. Resume mechanism

**Decision**: Downward, HTTP range requests. Upward, a committed-offset protocol where the client
asks the server what it has, then continues from there.

**Rationale**: Range requests are what browser download managers already understand, so the
downward direction inherits resume from the platform rather than reimplementing it. Android's
download manager resumes on its own. Upward, no browser mechanism exists, so the client reads
from an offset using the File API's slicing and the server acknowledges a durable committed
offset after each chunk lands, which is what makes resumption exact rather than approximate.

**Alternatives considered**: tus, the resumable upload protocol, matches the upward direction
closely and is well specified. It was rejected for now as a dependency and a surface larger than
the two endpoints this actually needs, but its committed-offset semantics are the model being
copied.

---

## 6. Integrity verification

**Decision**: BLAKE2b over the whole file, computed while streaming, compared at the destination.
Per-chunk checks on resumed uploads.

**Rationale**: It is fast enough not to become the bottleneck, it is in `golang.org/x/crypto`, and
computing it during the copy avoids a second pass over a 10 GB file. A mismatch marks the transfer
failed and the partial file is never presented as usable, per FR-032 and FR-033.

**Alternatives considered**: SHA-256 is the obvious default and is fine, but is slower without
hardware acceleration, which is exactly the phone case. CRC32 is too weak to claim integrity.

---

## 7. Progress and events

**Decision**: Server-sent events for the server-to-client stream, ordinary requests for commands.

**Rationale**: `EventSource` works outside a secure context, reconnects on its own, and is far
less machinery than a bidirectional socket for what is a one-way firehose of progress updates.
Commands are infrequent and fit ordinary requests. One event stream per page keeps well inside
the per-origin connection limit.

**Alternatives considered**: WebSockets would work and would collapse both directions into one
connection, at the cost of framing, heartbeats, and reconnection logic written by hand. Polling
was rejected for the idle processor budget.

---

## 8. Discovery

**Decision**: mDNS and DNS-SD advertising `_fastr._tcp`, with manual address entry as the
documented fallback.

**Rationale**: It is what every comparable tool uses and what phones and desktops already
implement. The service record carries the device name, a stable device identifier, the protocol
version, and the port, which is enough to populate the device list without contacting anything.

**Library**: `hashicorp/mdns`, pure Go, no cgo.

*Amended 2026-08-23, at implementation time.* The original choice was
`libp2p/zeroconf/v2`, flagged here with maintenance activity to be confirmed when the code was
written and `hashicorp/mdns` named as the fallback. The check was made and the flag fired: the
last `libp2p/zeroconf/v2` release is v2.2.0 from August 2022, four years old, against v1.0.7 from
June 2026 for the fallback. `brutella/dnssd` was also weighed and rejected: more recent than
zeroconf but it pulls `vishvananda/netlink` and `netns`, Linux-specific plumbing that adds weight
and a parity risk on Windows for no benefit here.

The cost of the fallback is that it offers a query rather than a subscription, and does not
surface goodbye packets. Neither matters. A query whose listener is held open *is* a
subscription, because responders announce themselves unprompted when they start; and a record
going away was never allowed to remove a device anyway, since the contract confirms reachability
against `/connect` rather than against the record.

Both libraries rest on `miekg/dns`, which is the whole of the transitive cost either way. The
current one brings a newer copy of it, and with it `x/net`, `x/sync`, `x/mod` and `x/tools` as
indirect dependencies. Nothing here is a direct dependency beyond the one line in `go.mod`.

**Alternatives considered**: broadcast or multicast pings of the project's own design would avoid
a dependency but reimplement service discovery badly. Restrictive networks that block multicast
are handled by the manual fallback, not by inventing a second discovery scheme.

---

## 9. Durable state

**Decision**: A single bbolt file holding devices, pairings, the queue, and history.

**Rationale**: FR-035e requires the queue to survive a restart, and transfer progress updates
frequently while transfers run. A JSON file rewritten atomically handles the first requirement
poorly under the second. bbolt is pure Go, needs no cgo, is a single file, and gives
transactional writes, which is precisely the guarantee the queue needs.

**Alternatives considered**: SQLite through `modernc.org/sqlite` is pure Go and would give
queryable history, at the cost of a much larger dependency for data that never exceeds a few
thousand rows. Plain JSON files were rejected on durability.

---

## 10. Tray, autostart, and background operation

**Decision**: `fyne.io/systray` for the tray icon. Autostart implemented per platform in
`internal/platform`: a desktop entry in the user's autostart directory on Linux, a registry run
key on Windows.

**Rationale**: The background decision in FR-048 to FR-052 makes the tray the primary surface when
the window is closed. The desktop interface itself is the same web application opened in the
user's browser, which avoids embedding a webview and keeps the binary genuinely self-contained.
On the desktop the page is served from loopback, so it *is* a secure context and gets the full
browser API, unlike the phone.

**Alternatives considered**: bundling a webview through a framework such as Wails or Tauri gives a
native window, but pulls in WebView2 on Windows and WebKitGTK on Linux, the second being a system
package that may be absent. That contradicts the promise of a binary with nothing to install.
**Flagged**: the tray library may require `libayatana-appindicator` on some Linux desktops; the
application must degrade to a headless background service with a clear message rather than fail to
start.

---

## 11. Trusted mode

**Decision**: The desktop generates a local certificate authority on first use, issues a
certificate for its own network addresses, and guides the user through installing the authority on
the phone. Once installed, the phone reaches the same application over HTTPS, in a secure context.

**Rationale**: It is the only route to a secure context on a phone that does not involve the
internet. With it, content encryption, service-worker streamed writing, and reliable resume all
become available at once, which is why the mode is worth its complexity. The authority's private
key never leaves the machine and is stored with restrictive permissions.

**Alternatives considered**: covered in section 1. All the alternatives either reach the network or
show the warning FR-044 forbids.

**Flagged for the implementation phase**: iOS requires installing a configuration profile and then
enabling full trust in a separate settings screen, which the guidance must walk through
explicitly. Android installs a user certificate authority, which its browser honours. Both flows
need real-device verification.

---

## 12. Cross-platform filesystem behavior

**Decision**: Sanitize incoming filenames against the *destination's* rules, never the sender's.
Resolve collisions by appending a numeric suffix. Refuse before starting when free space is short.

**Rationale**: A file named `aux.txt` or `report .` is legal on Linux and rejected on Windows, and
the sender has no business deciding. Windows reserved device names, trailing dots and spaces, path
length limits, and case-insensitive collisions are all handled by the receiving side in
`internal/platform`. Free space comes from `statfs` on Linux and `GetDiskFreeSpaceEx` on Windows,
behind one interface.

**Alternatives considered**: normalizing to a lowest common denominator for every platform would
mangle names unnecessarily on Linux, and FR-024 requires preserving the original wherever it is
usable.

---

## 13. Relayed phone-to-phone transfers

**Decision**: The relaying computer stages relayed content in a dedicated directory outside the
receive folder, streams it through, and deletes it when the transfer ends or is abandoned.

**Rationale**: FR-055 forbids relayed data from appearing as a received file. A separate staging
directory makes that structural rather than a matter of care. Staging rather than piping straight
through is what allows the transfer to resume when the receiving phone reconnects, which a pure
pipe cannot offer.

**Alternatives considered**: a direct phone-to-phone connection through WebRTC would avoid staging
entirely, but reintroduces a large protocol surface and still cannot write to disk on the
receiving side without a service worker.

---

## 14. Internationalization

**Decision**: JSON catalogues per language, embedded in the binary, negotiated from the browser's
language header with a manual override. English and French ship; missing keys fall back to
English.

**Rationale**: FR-039a to FR-039e. Catalogues as plain JSON mean a translator contributes a file
without touching code, which matches Principle VII. Size, date, and speed formatting uses the
browser's own locale-aware formatting rather than a hand-rolled table.

---

## 15. Accessibility verification

**Decision**: `axe-core` assertions inside the Playwright end-to-end suite, plus keyboard-only
traversal tests for each essential flow.

**Rationale**: FR-039j requires accessibility to fail the same gate as everything else. Automated
checks catch contrast, naming, and role violations. Progress announcements are throttled to the
meaningful moments listed in FR-039i rather than tied to every update, which is a behavior test
rather than a static check.

---

## Dependency budget

Twelve direct dependencies, of which three are build-time only and never reach the user. Each is
justified per the constitution's rule that added complexity must be argued for.

The budget grew from the nine estimated during planning. `golang.org/x/sys` was an oversight: the
platform layer cannot do its job without syscalls. `rsc.io/qr` and `gtank/ristretto255` are
deliberate additions, argued in their rows below.

| Dependency | Why it is not the standard library |
|---|---|
| `hashicorp/mdns` | mDNS and DNS-SD are not in the standard library, and reimplementing service discovery is a project of its own. Replaced `libp2p/zeroconf/v2` at implementation time; see section 8. |
| `fyne.io/systray` | Tray integration is per-platform system API work. |
| `go.etcd.io/bbolt` | Transactional durable storage without cgo. |
| `golang.org/x/crypto` | ChaCha20-Poly1305 and BLAKE2b. Effectively an extension of the standard library. |
| `github.com/gtank/ristretto255` | The prime-order group CPace runs over, built on `filippo.io/edwards25519`. Not in the standard library, and a hand-rolled ristretto encoding is the kind of thing that is subtly wrong on one input in a million. Section 4. |
| `golang.org/x/sys` | Platform syscalls: free space, file locking, known folders, the registry. |
| `rsc.io/qr` | QR is a specified format with masking, error correction, and version selection. A subtly wrong implementation produces a code that scans on the phone you tested with and fails on the next one. |
| Svelte, Vite, TypeScript | Build-time only, never shipped to the user as a runtime. |
| `@noble/curves`, `@noble/ciphers` | The browser's native cryptography is unavailable in the context the mobile page runs in. `@noble/curves` also supplies ristretto255, which is what lets the browser speak the same CPace as the host. |

Explicitly rejected: any HTTP framework, any ORM, any logging framework, any dependency that
requires cgo, and any dependency that would perform a network call the user did not ask for.

## Items flagged for the implementation phase

1. ~~**Pairing hardening**: consider a full PAKE before the first release.~~ Done: CPace,
   2026-08-24. Section 4.
2. **mDNS library maintenance**: confirm before committing, fallback identified. Section 8.
3. **Linux tray dependency**: must degrade gracefully, not fail to start. Section 10.
4. **Trusted-mode flows on real devices**: iOS full-trust and Android user CA both need
   verification on hardware, not emulators. Section 11.
