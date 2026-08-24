# Using fastr

How to move a file between your computer and your phone, and what fastr does
and does not protect while it does.

This document is written for somebody using the program, not building it. If you
want the reasoning behind the decisions, that is in [journal.md](journal.md).

---

## Before anything else: what is protected

fastr has two modes, and the difference matters enough to come first.

| | Simple mode | Trusted mode |
|---|---|---|
| Pairing and credentials | Encrypted | Encrypted |
| **File content** | **Readable by anyone on your network** | Encrypted end to end |
| Setup | None | Once per phone, a few minutes |
| Large files on a phone | Held in memory while saving | Written straight to storage |

**Simple mode is the default, and its file content is not private.** Anyone on
the same Wi-Fi can read what you send. That is fine at home. It is not fine in a
coworking space, a hotel, or on a network you do not control.

This is not a shortcut anybody took. A browser grants a "secure context" only to
`localhost`, never to an address like `192.168.1.20`. Outside a secure context
there is no service worker, and without one nothing can decrypt a stream while
the browser writes it to disk — so a large encrypted file could only be received
by holding it whole in memory, which iOS will not allow. Serving HTTPS with a
self-signed certificate does not help either: the browser shows a warning
instead of granting the context.

Trusted mode buys its way out of that by having you install a certificate from
your own computer onto your phone. That is a real decision, and it is described
in full [below](#trusted-mode).

---

## Starting fastr

Run the binary. It prints where it is listening and a pairing code:

```
fastr is listening.
  http://127.0.0.1:7420
  http://192.168.1.20:7420
Receiving into /home/you/Downloads/fastr

Pairing code: 481920
```

Nothing listens until you start it, and it listens only on your local network:
never on a public address, and never outside it.

Open the first address on the computer itself. That page is the computer's own,
and it does not need a code — reaching `127.0.0.1` means being on the machine.

---

## Connecting a phone

On the computer's page, the **Connect a phone** panel shows three things: the
address to open, a QR code for it, and a six-digit code.

1. Scan the QR code with the phone's camera, or type the address into its
   browser. There is nothing to install.
2. Type the six digits on the phone and give it a name.
3. Approve the request **on the computer**. Knowing the code is not enough:
   somebody has to say yes on the machine that will receive the files.

The code lasts three minutes, works once, and dies after five wrong attempts. A
new one appears on its own.

**The code is never sent over the network.** Your phone and your computer each
use it to compute a shared secret, and neither transmits it. That matters
because in simple mode this conversation is readable by anyone on the same
Wi-Fi: there is nothing there for them to read, and guessing costs one attempt
out of five rather than a search they could run on their own machine.

A pairing lasts a year of use for a device set to accept automatically, or
thirty days for one set to ask every time. You can remove any device at any
moment, and removal takes effect immediately — including for a transfer already
in flight.

---

## Sending a file

**From the computer to a phone.** Drop files on the send panel, or use the
buttons beside it, choose the phone, and press Send. On the phone, press **Save**
and the browser downloads it.

Keep the computer's tab open while it sends. The browser holds the file, and
nothing else on the machine can reach it — closing the tab abandons the
transfer, and the phone is told rather than left waiting.

**From a phone to the computer.** Choose files, or the camera, and press Send.
Files land in the receive folder shown at startup.

**From one phone to another.** Both phones must be connected to the same
computer. Choose the other phone as the destination; the computer passes the
data through and keeps nothing. You can see what is passing through it, and stop
it, from the computer's own page.

### One at a time

Only one transfer runs at a time, and the rest wait in a visible queue you can
reorder or clear. This is deliberate: two transfers sharing a link both finish
later than the same two in sequence.

### If something interrupts it

Nothing restarts from zero. A phone that locks its screen, loses Wi-Fi, or gets
carried out of range resumes from the byte it reached — the computer keeps that
point for seven days, and picking the same file again continues from it rather
than sending it twice.

A transfer that is interrupted says so, and says how much already arrived. A
transfer that *failed* says why and what to do about it.

---

## Trusted mode

Setting this up encrypts file content end to end and lets a phone write large
files straight to storage. It takes a few minutes per phone.

### What you are agreeing to

You install a certificate authority, generated on your computer, onto your
phone. **Anything holding that authority's private key could impersonate other
websites to that phone.**

fastr's side of that bargain:

- the key is generated on your machine and never leaves it;
- it is generated per installation — no copy is shipped, so there is no key
  shared between fastr users;
- it is stored with the most restrictive permissions the operating system
  offers;
- the certificate you install carries a fingerprint the computer displays, so
  you can check that what your phone is about to trust is what your computer
  issued.

If that is not a trade you want to make, simple mode keeps working and nothing
here is required.

### The steps

1. On the computer, open **Trusted mode** and read what it says before pressing
   anything. Press **Create the certificate**.
2. On the phone, open the address shown and download the certificate.
3. Install it, and then **turn on full trust for it**, which is a separate step:
   - **iPhone**: Settings → General → About → Certificate Trust Settings.
   - **Android**: Settings → Security → Encryption & credentials → Install a
     certificate → CA certificate.
4. Compare the fingerprint the phone shows against the one on the computer
   before accepting.
5. Open the `https://` address shown on the computer. The phone is now in
   trusted mode, and the computer says so.

You can stop at any point. Nothing here touches the pairing you already have,
and a phone that never finishes these steps keeps working exactly as before.

### Requiring it

Once a phone is set up, you can require trusted mode for it. After that, that
device is refused if it ever connects in the clear, rather than quietly falling
back. A transfer that would drop out of trusted mode is refused too, and you are
asked rather than told afterwards.

---

## Where things are kept

- **Received files**: the folder shown at startup, which you can change. It is
  never a system folder, and a file never overwrites one already there — a
  second file with the same name is saved beside the first.
- **Partial data**: a staging area outside the receive folder, so a half-arrived
  file can never be mistaken for one that arrived. It is deleted seven days
  after a transfer is abandoned, and you are told when that happens.
- **Relayed data**: a directory of its own, deleted the moment the transfer
  ends, whichever way it ended.
- **History**: what moved, with which device, when, how it turned out, and which
  protection mode it used. You can erase all of it, from the computer.

Nothing is sent anywhere else, at any point. No account, no telemetry, no update
check.

---

## When something goes wrong

Every failure names a cause and what to do about it, rather than a code. A few
that are worth knowing in advance:

**"This device is not open right now."** The other device needs its fastr page
open to receive. A phone in your pocket with the browser closed cannot be sent
to.

**"Another transfer is running."** Yours is queued and will start on its own.

**"Not enough space on the destination."** Checked before anything moves, so
this never appears at ninety percent of a large file.

**"The file did not arrive intact."** Every transfer is checksummed end to end.
A file that does not match is deleted rather than saved, and no partial file is
ever left where you might open it.

**"This network blocks automatic discovery."** Some networks do. Type the
address shown on the other computer instead; nothing downstream is different.

---

## Known limits

Stated here rather than discovered later:

- **Simple mode content is readable on your network.** Repeated because it is
  the single most important thing on this page.
- **Trusted mode on Windows** does not yet restrict the authority's private key
  with an explicit access control list; it relies on the permissions of your
  user profile directory.
- **The trusted-mode setup steps have not been walked through on physical
  hardware** at the time of writing, only against the platforms' documentation.
- **Streamed decryption on the phone** is not implemented yet, so trusted mode
  currently buys encryption on the wire rather than the larger-file handling it
  will eventually allow.
