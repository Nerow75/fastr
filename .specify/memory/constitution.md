<!--
Sync Impact Report
==================
Version change: TEMPLATE (unratified) -> 1.0.0
Bump type: initial ratification (skeleton replaced with a normative document)

Principles defined (initial document, no renames):
  - I. Local-First, No Cloud (NON-NEGOTIABLE)
  - II. Zero Install On Mobile
  - III. No Size Limit
  - IV. Linux / Windows Parity
  - V. Security On Shared Networks (NON-NEGOTIABLE)
  - VI. Effortless By Default
  - VII. Open Source In The Open

Sections added:
  - Technical Constraints (replaces [SECTION_2_NAME])
  - Development Workflow & Quality Gates (replaces [SECTION_3_NAME])
  - Governance (filled)

Sections removed: none

Remaining placeholders: none

Deferred items requiring manual follow-up:
  - LICENSE file (MIT) not yet created; tracked as a Next Action, not a constitution edit.
-->

# fastr Constitution

fastr is a Linux and Windows desktop application that runs a server on the local network so
that files of any size can be moved between a computer and Android or iOS phones through a
mobile web app, with no cloud service and no size cap.

## Core Principles

### I. Local-First, No Cloud (NON-NEGOTIABLE)

No byte of user content MUST ever leave the local network. No relay server, no remote
staging storage, no CDN. The application MUST NOT require an account, an email address, or
any third-party identity. No telemetry, analytics, or crash reporting is enabled by default;
any such reporting MUST be explicit opt-in and documented. Frontend assets (fonts, scripts,
styles, icons) MUST be served from the local binary: zero requests to any external domain.

Verification: an integration test MUST fail if an outbound connection beyond the local
network is attempted during a nominal scenario.

Rationale: the problem being solved is precisely the dependency on intermediaries such as
Discord or Drive, which impose caps, latency, and loss of control over the data.

### II. Zero Install On Mobile

The phone uses a browser and nothing else. No binary to install, no app store, no mobile
code signing. Access happens through a short local URL, also presented as a QR code by the
desktop application.

Every release MUST be validated on iOS Safari and Android Chrome (latest two major
versions). No essential capability (discover, send, receive, list, resume) MUST depend on a
web API that iOS Safari lacks. Advanced APIs are allowed only as progressive enhancement,
with a working fallback.

Rationale: the tool loses its value if something must be installed on every phone it is
meant to help.

### III. No Size Limit

No file size cap MUST be hard-coded. The only acceptable limit is free disk space on the
destination, and it MUST be detected and reported before a transfer starts.

Transfers MUST be streamed: the server MUST NEVER hold a whole file in memory. Memory
footprint MUST stay constant regardless of file size, verified by a test using a file of at
least 10 GB. Resume is mandatory: a Wi-Fi drop, a sleep, or a screen lock MUST NOT force a
transfer to restart from zero. Integrity MUST be verified end to end with a checksum, and a
corrupted transfer MUST be reported as failed, never as successful.

### IV. Linux / Windows Parity

Every shipped capability MUST behave identically on Linux and on Windows. CI MUST run the
full test suite on both systems; a failure on either one blocks the release.

Platform specifics (path separators, permissions, reserved filenames, network interfaces,
config locations) MUST be isolated behind an abstraction layer and MUST NOT leak into domain
logic. Any irreducible behavioral difference MUST be documented, justified, and surfaced in
the UI.

### V. Security On Shared Networks (NON-NEGOTIABLE)

Explicit pairing is required before any transfer: a one-time code or a confirmation on the
host device. Anonymous access is forbidden, including read access.

The server MUST NEVER expose the filesystem. Only a configured receive folder and files
explicitly offered for a transfer are reachable. Path traversal MUST be tested and rejected.
Encryption in transit is mandatory; a cleartext fallback is forbidden by default. The server
MUST NOT listen until the user starts it, and MUST bind only to the interfaces of the chosen
local network. Paired sessions MUST be listed in the UI, expirable, and revocable at any
time. Secrets (keys, pairing tokens) MUST NEVER appear in logs.

Rationale: a home Wi-Fi, an office, or a guest network are shared environments. "Local" does
not mean "trusted".

### VI. Effortless By Default

Once two devices are paired, sending a file MUST take at most three actions. Discovery of
devices on the network MUST be automatic; entering an address by hand is a fallback, never
the nominal path.

A first successful transfer MUST be reachable in under two minutes from install, without
reading documentation. Drag and drop, multi-selection, and whole-folder sending MUST be
supported on the desktop side. Every error shown MUST state a concrete corrective action,
never a bare code.

### VII. Open Source In The Open

fastr is developed in public on GitHub under the MIT license. Development happens in the
open: no private fork holding the real work, no code drop without history.

- Repository artifacts (code, comments, README, docs, commits, issues, PRs) MUST be written
  in English so external contributors can participate.
- No secret, key, certificate, personal path, or local IP MUST ever be committed. Generated
  credentials belong to runtime state, never to the repository.
- Every release MUST ship a reproducible build from a tagged commit, with checksums for the
  published binaries.
- Contribution entry points (README, CONTRIBUTING, issue templates, a "good first issue"
  path) MUST exist and stay accurate.
- A newcomer MUST be able to clone, build, and run the project from documented commands
  alone, on both Linux and Windows.

Rationale: a tool that handles personal files on a private network earns trust only if its
behavior can be audited by anyone.

## Technical Constraints

- **Desktop targets**: Linux and Windows are committed and tested. macOS is not committed,
  but no decision MUST make it unreachable without cause.
- **Mobile clients**: Android browsers (Chrome, Firefox) and iOS Safari, latest two major
  versions.
- **Distribution**: self-contained binary. The user MUST NOT need to install a runtime or a
  package manager first.
- **Runtime dependencies**: no reliance on any external network service at runtime,
  including for time, fonts, and icons.
- **Discovery**: mDNS / DNS-SD on the local network, with manual address entry as fallback.
- **Transport**: HTTP over TLS with a locally provisioned certificate, designed for a context
  where no certificate authority is available.
- **Local state**: configuration and pairing state stored in each platform's standard
  locations, never inside the install directory.
- **Receive folder**: configurable, defaulting outside system folders, and never widened
  implicitly.
- **License**: MIT, applied at the repository root and referenced from the README.

## Development Workflow & Quality Gates

All work goes through the Spec Kit flow: constitution, `specify`, `plan`, `tasks`,
`implement`. No feature is implemented without a written specification.

Every shipped user story MUST be independently testable and deliver value the user can
observe.

Mandatory gates before merge:

1. Unit and integration tests green on Linux **and** Windows.
2. End-to-end transfer tests covering: small file, large file, resume after a network drop,
   and integrity verification.
3. Network regression test: no outbound request beyond the local network.
4. Security tests: unpaired access rejected, path traversal rejected, secrets absent from
   logs.
5. No throughput regression above 10% against the previous release on the reference
   scenario.
6. Secret scanning clean on every commit reaching the public repository.

Any added complexity (dependency, service, abstraction layer) MUST be justified in writing in
the corresponding plan. When in doubt, the simpler option wins.

## Governance

This constitution supersedes any other practice, convention, or tooling preference. When a
plan or a specification conflicts with the constitution, the constitution wins.

**Amendment**: any change requires a written proposal covering motivation, impact on existing
specifications and plans, and a migration path where applicable. The amendment lands in this
document before any code that depends on it.

**Versioning**: semantic versioning.

- MAJOR: incompatible removal or redefinition of a principle or governance rule.
- MINOR: a new principle or section, or a material expansion of an existing rule.
- PATCH: clarification, rewording, or correction with no semantic effect.

**Compliance**: every plan MUST include a constitution compliance check. Any violation MUST
be listed and explicitly justified, otherwise the plan is rejected. A compliance review runs
at every merge and every release.

**Version**: 1.0.0 | **Ratified**: 2026-08-20 | **Last Amended**: 2026-08-20
