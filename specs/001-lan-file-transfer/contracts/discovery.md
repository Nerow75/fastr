# Contract: Discovery

**Date**: 2026-08-20 | **Plan**: [../plan.md](../plan.md)

## Service

```text
Service type: _fastr._tcp
Domain:       local.
Instance:     <device name> (<short id>)
Port:         the port the HTTP server bound to
```

The instance name carries the short identifier so that two devices sharing a name remain
distinguishable in the list, as FR-005 requires.

## TXT record

| Key | Value | Purpose |
|---|---|---|
| `v` | protocol version, integer | Refuse to pair across incompatible versions with a clear message rather than failing obscurely. |
| `id` | stable device identifier | The authoritative identity. Addresses are advisory. |
| `name` | user-visible device name | Displayed before any pairing exists. |
| `kind` | `computer` | Only computers advertise. Phones exist through their browser session. |
| `os` | `linux` or `windows` | Advisory only. Never an input to an access decision. |
| `tls` | `0` or `1` | Whether trusted mode is initialized on this instance. |

The TXT record carries no credential, no key, and nothing derived from user content. Anything on
the network can read it, and it is written on that assumption.

## Behavior

- **Advertising** starts only when the user starts the server, never before (FR-001), and stops
  when the server stops.
- **Browsing** runs continuously while the application runs, so devices appear and disappear
  without a manual refresh (FR-008).
- A device is marked unreachable rather than removed when its record goes away, so it does not
  vanish under a selection in progress (FR-004 scenario 3).
- **Reachability** is confirmed by a cheap request to `/connect`, not by the mDNS record alone. A
  record can outlive the process that published it.

## Manual fallback

Networks that block multicast are common in offices and on guest wifi. The user enters
`host:port` directly, the client fetches `/connect`, and the device joins the list with the same
identity fields it would have received over mDNS (FR-006). Nothing downstream distinguishes a
manually added device from a discovered one.

## Non-goals

- No discovery mechanism of the project's own design. If multicast is blocked, the manual path is
  the answer, not a second protocol.
- No discovery beyond the local link. Anything requiring a resolver outside the network violates
  Principle I.
