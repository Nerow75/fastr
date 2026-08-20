# fastr

**Send files of any size between your computer and your phone, over your own Wi-Fi. No cloud, no account, no app to install on the phone.**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#project-status)

> ### Project status
>
> **Pre-alpha. There is no working build yet and nothing to download.**
>
> The project is in its specification phase: the [constitution](.specify/memory/constitution.md)
> is ratified, the feature specification and the technology stack are being written.
> Watch the repository or open an issue if you want to follow along or help shape it.

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
| **Secure on shared networks** | Explicit pairing, never any filesystem exposure, encrypted in transit, revocable sessions. |
| **Effortless by default** | Three actions to send a file. First successful transfer in under two minutes, without reading docs. |
| **Open source in the open** | MIT, developed publicly, reproducible builds, no secrets in the repo. |

## Roadmap

- [x] Project constitution ratified
- [ ] Feature specification
- [ ] Technology stack and implementation plan
- [ ] Device discovery and pairing
- [ ] Desktop to phone transfer
- [ ] Phone to desktop transfer
- [ ] Resume after interruption, integrity verification
- [ ] Signed builds for Linux and Windows

## Technology stack

Not decided yet. The stack is chosen during the planning phase, under the constraints set by the
constitution: a self-contained binary with no runtime to install beforehand, identical behavior on
Linux and Windows, and no external network dependency at runtime.

If you have a strong opinion, this is the right moment to open an issue.

## Contributing

Contributions are welcome, especially while the shape of the project is still open. Start with
[CONTRIBUTING.md](CONTRIBUTING.md), and see [SECURITY.md](SECURITY.md) for reporting a
vulnerability.

The project uses [GitHub Spec Kit](https://github.com/github/spec-kit): specifications come before
code, and they live in the repository alongside it.

## License

[MIT](LICENSE)
