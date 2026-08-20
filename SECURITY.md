# Security Policy

fastr moves personal files across a shared network. Security reports are taken seriously and are
welcome.

## Supported versions

The project is pre-alpha and has no release yet. Until the first tagged release, only the `main`
branch is supported.

## Reporting a vulnerability

**Do not open a public issue.**

Use GitHub's private vulnerability reporting on this repository:
**Security → Report a vulnerability**. It creates a private thread visible only to the maintainers.

Please include:

- the affected component (desktop server, mobile web app, pairing, discovery)
- the operating systems and browsers involved
- reproduction steps, and a proof of concept if you have one
- what an attacker gains, and what access they need to start

You can expect an acknowledgement within 7 days, an assessment within 30 days, and credit in the
fix notes unless you prefer otherwise.

## Scope

The threat model assumes an untrusted local network. A home Wi-Fi, an office, or a guest network
are shared environments, and "local" does not mean "trusted".

**In scope:**

- reaching files outside the configured receive folder or the files explicitly offered
- transferring anything without completing pairing
- path traversal, symlink escape, or reserved-filename tricks on Windows
- interception or downgrade of a transfer on the local network
- secrets (keys, pairing tokens) leaking into logs, error messages, or the UI
- a pairing that survives revocation or expiry
- the application reaching the internet at all, which principle I forbids outright

**Out of scope:**

- an attacker who already has an interactive session on the host machine
- physical access to an unlocked, already-paired device
- denial of service by saturating the local network
- vulnerabilities in the operating system or the browser themselves

## Disclosure

Coordinated disclosure. Once a fix is available and released, the report is made public with
credit to the reporter.
