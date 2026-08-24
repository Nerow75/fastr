# fastr

**Send files of any size between your computer and your phone, over your own Wi-Fi. No cloud, no account, no app to install on the phone.**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#project-status)

> ### Project status
>
> **Pre-alpha. There is no tagged release yet and nothing to download.**
>
> The six user stories work end to end and are covered by tests on Linux and Windows,
> and a real phone has moved files in both directions. Pairing now uses
> [CPace](specs/001-lan-file-transfer/contracts/pairing.md), so the six-digit code is
> never transmitted and cannot be attacked offline. What is still missing before a
> first release is named rather than implied:
>
> - **The setup has not been walked through on real hardware end to end**, only the
>   transfers have. Trusted mode in particular is written from the platforms'
>   documentation rather than from having performed it.
> - **No signed builds yet**, so Windows will warn about an unknown publisher.
>
> Build it yourself with `make build` if you want to try it. See
> [docs/using-fastr.md](docs/using-fastr.md) for what it does, and for what simple
> mode does and does not protect.

---

## The problem

Moving a file from a laptop to a phone on the same Wi-Fi is absurdly hard in 2026. The usual
workarounds all route your private files through someone else's servers, and they all cap out:

- a private Discord server caps attachments at a few megabytes
- cloud drives mean upload, wait, share, download, then remember to clean up
- messaging yourself compresses your photos and videos
- cables mean finding the cable, and half of them do not carry data

Every one of these sends your file across the internet and back, to reach a device sitting two
meters away.

## The approach

fastr runs a small server on your computer, on your local network only. Your phone opens a URL
in its browser, scanned from a QR code, and the two devices talk directly.

```
   Desktop app (Linux / Windows)              Phone (any browser)
   ┌──────────────────────────┐               ┌──────────────────┐
   │  local server + UI       │  ◄──────────► │  mobile web app  │
   │  device discovery (mDNS) │   local Wi-Fi │  no install      │
   │  receive folder          │   TLS, direct │  QR to connect   │
   └──────────────────────────┘               └──────────────────┘
                        no internet, no third party
```

**What that buys you:**

- **No size limit.** The only ceiling is free disk space on the destination.
- **Full local speed.** The transfer never leaves your network, so it runs at Wi-Fi speed
  instead of your upload speed.
- **Nothing to install on the phone.** A browser is enough, on Android and on iOS.
- **Nothing leaves your network.** No relay, no account, no telemetry.
- **Pick a device.** Machines on the network are discovered automatically; you choose the
  target from a list.

## Principles

These are non-negotiable and enforced by the [project constitution](.specify/memory/constitution.md):

| Principle | What it means in practice |
| --- | --- |
| **Local-first, no cloud** | No byte of your content ever leaves the local network. No account, no telemetry. |
| **Zero install on mobile** | Browser only. Nothing essential may depend on an API that iOS Safari lacks. |
| **No size limit** | Streamed transfers, constant memory, resume after a dropped connection, end-to-end checksum. |
| **Linux / Windows parity** | Every feature behaves identically on both, verified in CI. A failure on either blocks the release. |
| **Secure on shared networks** | Explicit pairing, never any filesystem exposure, revocable sessions. Two protection modes, see below. |
| **Effortless by default** | Three actions to send a file. First successful transfer in under two minutes, without reading docs. |
| **Open source in the open** | MIT, developed publicly, reproducible builds, no secrets in the repo. |

## Roadmap

- [x] Project constitution ratified
- [x] Feature specification
- [x] Technology stack and implementation plan
- [x] Device discovery and pairing
- [x] Desktop to phone transfer
- [x] Phone to desktop transfer
- [x] Resume after interruption, integrity verification
- [x] Queue, history, device management
- [x] Phone to phone through a relay
- [x] Trusted mode, apart from streamed decryption on the phone
- [x] Pairing hardened with CPace: the code is never sent, and guessing is online only
- [ ] Setup verified on physical Android and iOS hardware
- [ ] Signed builds for Linux and Windows

## A word on encryption, up front

Browsers grant a "secure context" only to loopback addresses, never to a local network address
like `192.168.1.20`. Outside a secure context, the browser's cryptography API and service workers
do not exist. Without a service worker, nothing can decrypt a stream while the browser writes it
to disk, so a large encrypted file could only be received by holding it whole in memory, which
iOS will not allow. Serving HTTPS with a self-signed certificate does not help: the browser shows
a security warning instead.

So fastr ships two modes, and says which one you are in:

- **Simple mode (default)**: pairing, credentials, and metadata are encrypted. **File content
  travels in the clear on your network.** Anyone on the same Wi-Fi can read it. Fine at home,
  not fine in a coworking space. Nothing to install, no warning, no setup.
- **Trusted mode (optional)**: a one-time setup per phone installs a certificate authority
  generated on your own machine. The phone then gets a real secure context, and content is
  encrypted end to end, with reliable resume and streamed writing as a bonus.

We would rather state this plainly than describe the default as "secure" and let you find out
later. [LocalSend](https://github.com/localsend/protocol), the closest comparable project, hits
the same wall and serves its browser mode over plain HTTP for exactly this reason.

## Technology stack

- **Desktop**: Go, compiled to a single static binary per platform. No runtime to install.
- **Web application**: Svelte and TypeScript, built with Vite and embedded in the binary.
- **Discovery**: mDNS / DNS-SD, advertising `_fastr._tcp`.
- **State**: a single bbolt file. No database server, no cgo.
- **Cryptography**: CPace over ristretto255 for pairing, so the six-digit code never travels;
  ChaCha20-Poly1305 for everything sealed afterwards. Audited pure-JavaScript implementations on
  the phone, where the browser's native API is unavailable.

Ten direct dependencies, each argued for in
[research.md](specs/001-lan-file-transfer/research.md#dependency-budget). No HTTP framework, no
ORM, nothing requiring cgo, and nothing that makes a network call you did not ask for.

## Contributing

Contributions are welcome, especially while the shape of the project is still open. Start with
[CONTRIBUTING.md](CONTRIBUTING.md), and see [SECURITY.md](SECURITY.md) for reporting a
vulnerability.

The project uses [GitHub Spec Kit](https://github.com/github/spec-kit): specifications come before
code, and they live in the repository alongside it.
`specs/001-lan-file-transfer/tasks.md` is the source of truth for what is done;
[docs/journal.md](docs/journal.md) carries the reasoning behind the decisions, the defects found
along the way, and what is deliberately still open.
[docs/using-fastr.md](docs/using-fastr.md) is the user-facing guide.

## License

[MIT](LICENSE)
