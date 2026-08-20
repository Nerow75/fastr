# Feature Specification: LAN File Transfer

**Feature Branch**: `001-lan-file-transfer`

**Created**: 2026-08-20

**Status**: Draft

**Input**: User description: "Baseline specification for fastr: local network file transfer between a desktop computer and phones. A desktop application runs a server on the local network; the phone connects with a browser only, through a short local URL presented as a QR code. Covers device discovery, pairing, desktop-to-phone and phone-to-desktop transfers, unlimited file size, progress and resume, transfer history, and onboarding. Out of scope: transfers outside the local network, any relay or internet fallback, macOS, native mobile apps, folder syncing, and file editing or previewing beyond what the browser does natively."

## Clarifications

### Session 2026-08-20

- Q: When a paired device sends a file, must the recipient accept it, or does the transfer start on its own? → A: Per-device trust setting. Each pairing carries a mode: accept automatically, or ask every time. Devices the user paired themselves default to automatic; devices added later default to asking.
- Q: Can several transfers run at once, or do they queue? → A: A single global sequential queue. One transfer is active at a time; the rest wait in a queue the user can see, reorder, and clear. The shared network medium means parallel transfers would divide the same throughput rather than add to it.
- Q: Should the interface be translatable from the start, or English only? → A: Translatable from the first line, shipping English and French. No hard-coded strings, localized size, date, and speed formats, and language detected from the device.
- Q: How long should abandoned partial transfers and inactive pairings be kept? → A: Partial transfer data for 7 days. Pairings expire on inactivity, with the window following the trust mode: 1 year for devices set to accept automatically, 30 days for devices set to ask every time.
- Q: What level of accessibility does the interface commit to? → A: WCAG 2.2 level AA on the essential flows (pairing, sending, receiving, progress, errors), verified automatically in CI rather than left as an intention.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Get a large file from the computer onto the phone (Priority: P1)

Someone has a 2 GB video on their laptop and wants it on their phone. Both are on the same
Wi-Fi. They open fastr on the laptop, which shows a QR code. They point the phone camera at it,
the phone opens a page in its browser, the laptop asks them to confirm the new device, and they
accept. They drag the video onto the laptop window, pick the phone as the target, and send. The
phone shows the transfer arriving with live progress, and when it finishes they save the video
to the phone.

**Why this priority**: This is the entire reason the product exists. It is the flow that today
requires a cloud drive or a cable, and it fails outright above 20 MB on the messaging
workarounds people currently use. Shipped alone, it already replaces the painful path.

**Independent Test**: With only this story built, a first-time user can move a file of any size
from a computer to a phone and open it there. Fully testable end to end with one computer and
one phone, no other feature present.

**Acceptance Scenarios**:

1. **Given** the desktop application is running and a phone is on the same network, **When** the
   user scans the displayed QR code with the phone, **Then** the phone opens the fastr page in
   its browser without installing anything.
2. **Given** a phone has just opened the page and is not yet paired, **When** it attempts to
   reach any file, **Then** the request is refused and the user is shown the pairing step
   instead.
3. **Given** an unpaired phone requests pairing, **When** the user confirms it on the computer,
   **Then** the phone appears as a target device on the computer and stays available for later
   sessions.
4. **Given** a paired phone, **When** the user drags a 2 GB file onto the computer window,
   selects the phone, and confirms, **Then** the transfer starts and both devices show live
   progress with transferred bytes, speed, and estimated time remaining.
5. **Given** a transfer has completed, **When** the user opens the received file on the phone,
   **Then** the file is byte-identical to the source and carries its original name.
6. **Given** the destination has less free space than the file requires, **When** the user
   attempts to send, **Then** the transfer is refused before any data moves and the user is told
   how much space is missing.

---

### User Story 2 - Get files from the phone onto the computer (Priority: P2)

Someone filmed a long video on their phone and wants it on their computer for editing. From the
fastr page already open in the phone browser, they pick the video from the phone, choose their
computer as the target, and send. The computer writes it into its receive folder and shows a
notification when it is done.

**Why this priority**: The other half of the original problem. Photos and videos shot on a phone
are exactly the files that exceed messaging limits, and the current workaround (upload to a
cloud drive, then download) is the slowest. It depends on the connection established in Story 1,
but the sending direction is independent and separately testable.

**Independent Test**: With a paired phone, select a file on the phone and send it to the
computer. Verify it lands in the receive folder, intact, with its original name.

**Acceptance Scenarios**:

1. **Given** a paired phone with the fastr page open, **When** the user picks one or more files
   from the phone, **Then** the selected files are listed with their names and sizes before
   sending.
2. **Given** a selection is ready, **When** the user picks a target computer and confirms,
   **Then** the transfer starts and progress is visible on both devices.
3. **Given** a transfer from a phone completes, **When** the user looks at the computer's receive
   folder, **Then** the files are present, intact, and named as they were on the phone.
4. **Given** a file with the same name already exists in the receive folder, **When** a new
   transfer would overwrite it, **Then** the existing file is preserved and the incoming file is
   saved under a distinct name.
5. **Given** a filename that is invalid on the destination operating system, **When** it is
   received, **Then** it is stored under a sanitized name and the user is told what was changed.

---

### User Story 3 - Survive an interrupted transfer (Priority: P3)

Someone starts sending a 10 GB file, walks out of Wi-Fi range, then comes back. The transfer
picks up where it stopped instead of starting over.

**Why this priority**: Without this, "no size limit" is a hollow promise. Large transfers over
Wi-Fi with a phone that locks its screen are interrupted routinely, and restarting a multi-gigabyte
transfer from zero makes the product unusable for exactly the files it exists to move.

**Independent Test**: Start a large transfer, cut the network at roughly the halfway point,
restore it, and verify the transfer completes without re-sending the part already delivered.

**Acceptance Scenarios**:

1. **Given** a transfer in progress, **When** the network drops, **Then** the transfer is shown
   as interrupted rather than failed, and reconnection is attempted automatically.
2. **Given** an interrupted transfer, **When** the network returns, **Then** the transfer resumes
   from where it stopped without re-sending more than a small trailing portion of the data
   already delivered.
3. **Given** a transfer in progress, **When** the phone screen locks or the browser goes to the
   background, **Then** the transfer either continues or is resumable on return, and never
   silently corrupts the result.
4. **Given** a transfer that completed, **When** the integrity check runs, **Then** a mismatch is
   reported as a failed transfer and the partial file is not presented as usable.
5. **Given** an interrupted transfer that is never resumed, **When** its retention window
   expires, **Then** its partial data is cleaned up automatically and the user is told it
   expired.

---

### User Story 4 - Pick among several devices on the network (Priority: P4)

A household has two computers and three phones. Any of them can be chosen as the destination
without typing an address.

**Why this priority**: The user asked to select a device, not just to pair one. It becomes
valuable once more than two devices exist, so it can follow the core transfer flows.

**Independent Test**: Run fastr on two computers, connect two phones, and verify every device
lists the others with recognizable names and can be chosen as a target.

**Acceptance Scenarios**:

1. **Given** several devices running fastr on the same network, **When** the user opens the
   device list, **Then** all reachable devices appear automatically with recognizable names and
   an indication of which are already paired.
2. **Given** a device joins the network, **When** the user is looking at the device list,
   **Then** the new device appears without a manual refresh.
3. **Given** a device leaves the network, **When** the user looks at the list, **Then** it is
   shown as unreachable rather than silently disappearing mid-selection.
4. **Given** automatic discovery does not work on a restrictive network, **When** the user enters
   a device address manually, **Then** the connection can still be established.
5. **Given** two devices report the same name, **When** they appear in the list, **Then** they
   are distinguishable from one another.

---

### User Story 5 - Manage devices and transfers (Priority: P5)

Someone wants to see what is running, stop a transfer they started by mistake, check whether
last night's transfer actually finished, and remove a phone they no longer own.

**Why this priority**: Control and trust rather than raw capability. It matters most once the
tool is used regularly, and each piece is small.

**Independent Test**: Start a transfer and cancel it; confirm it stops on both sides. Revoke a
paired device and confirm it can no longer transfer without pairing again.

**Acceptance Scenarios**:

1. **Given** a transfer in progress, **When** the user cancels it from either device, **Then** it
   stops on both ends and no partial file is left presented as complete.
2. **Given** past transfers, **When** the user opens the history, **Then** each entry shows what
   was transferred, to or from which device, when, and whether it succeeded or why it failed.
3. **Given** a list of paired devices, **When** the user revokes one, **Then** that device
   immediately loses access and must pair again before any further transfer.
4. **Given** a paired device that has not been used for the retention period, **When** the period
   elapses, **Then** its pairing expires and the user can see that it did.
5. **Given** the user changes the receive folder, **When** a new transfer arrives, **Then** it is
   written to the new location and previously received files are left untouched.

---

### User Story 6 - Send from one phone to another (Priority: P6)

A friend comes over and wants a set of photos from the visit. Both phones are paired to the
computer running fastr. From their browser page, the sender picks the photos and chooses the
other phone as the target. The computer passes the data through without keeping it.

**Why this priority**: A genuine use, but it depends on everything else already working, and it
serves visitors rather than the owner's daily loop. It also carries the most delicate handling,
since a machine ends up holding data that is not its own.

**Independent Test**: Pair two phones to one computer, send a file from one to the other, and
verify it arrives intact while nothing is left behind on the computer.

**Acceptance Scenarios**:

1. **Given** two phones paired to the same computer, **When** one selects the other as a target,
   **Then** the transfer is offered and works as any other transfer does.
2. **Given** a phone that is not paired with the relaying computer, **When** another phone tries
   to send to it, **Then** the transfer is refused and pairing is required first.
3. **Given** a relayed transfer that completes, **When** the relaying computer is inspected,
   **Then** no relayed data remains and nothing appears in its receive folder.
4. **Given** a relayed transfer in progress, **When** the relaying computer's user cancels it,
   **Then** it stops on both phones with a clear reason.
5. **Given** a relayed transfer in progress, **When** the relaying computer becomes unavailable,
   **Then** both phones are told why it stopped and the transfer resumes once a relay is back.

---

### Edge Cases

**Storage and filesystem**

- Destination runs out of space mid-transfer, after the initial check passed.
- Incoming filename collides with an existing file in the receive folder.
- Filename is legal on the sender and illegal on the receiver (reserved names, trailing dots,
  characters forbidden on one platform, case-only differences).
- Filename or path exceeds the destination's length limits, particularly for deep folder sends.
- Zero-byte files, files with no extension, and files whose names are entirely non-Latin.
- The source file is modified, moved, or deleted while it is being sent.
- The receive folder is deleted, renamed, or made read-only while the application runs.
- A folder send contains a symbolic link, a shortcut, or a file the sender cannot read.

**Network and session**

- The network has no support for automatic discovery, or blocks device-to-device traffic
  entirely.
- A device changes address mid-transfer, or is on a different subnet of the same Wi-Fi.
- The same file is queued twice, or queued again while an earlier copy of it is still
  transferring.
- The queue is long and a device in it becomes unreachable before its turn arrives.
- The browser tab is closed, refreshed, or navigated away mid-transfer.
- The computer sleeps, hibernates, or its network interface is disabled mid-transfer.
- A device is reachable for discovery but refuses the transfer connection.

**Security**

- An unpaired device on the same network attempts to list, read, or write files.
- A pairing code is entered incorrectly, repeatedly, or after it has expired.
- A request tries to reach outside the receive folder or the explicitly offered files.
- A previously paired device returns after being revoked.
- Two pairing attempts arrive at the same time.

**Mobile specifics**

- The phone denies the browser permission to save files, or its storage is full.
- The phone's browser restricts saving several files at once.
- The browser is in private mode, or clears its site data between sessions, losing the pairing.
- The phone rotates, backgrounds the browser, or receives a call mid-transfer.
- The browser aggressively caches the page and serves a stale version after an update.

**Background operation**

- The user's session is locked while a transfer is running, or a transfer arrives while it is.
- The application is started twice, or is already running when the user launches it again.
- The user stops the application while a transfer is in flight.
- The machine changes network, joining a public Wi-Fi with the application still listening.

**Relayed transfers**

- The relaying computer runs out of temporary space partway through.
- The relaying computer is stopped, sleeps, or leaves the network mid-relay.
- The receiving phone disconnects while the relay still holds data for it.
- Two relayed transfers run through the same computer at once.
- A phone is revoked on the relaying computer while it is relaying for it.

## Requirements *(mandatory)*

### Functional Requirements

**Connection and discovery**

- **FR-001**: The desktop application MUST run a server reachable only on the local network, and
  MUST NOT listen until the user has started it.
- **FR-002**: The system MUST present a connection entry point as both a short local address and
  a QR code encoding it.
- **FR-003**: A phone MUST be able to reach the full experience using only its browser, with no
  installation of any kind.
- **FR-004**: The system MUST discover other fastr devices on the same network automatically and
  list them with a human-recognizable name.
- **FR-005**: The system MUST let the user name their device, and MUST distinguish devices that
  report identical names.
- **FR-006**: The system MUST offer manual address entry as a fallback when automatic discovery
  is unavailable.
- **FR-007**: The system MUST show, for each listed device, whether it is currently reachable and
  whether it is already paired.
- **FR-008**: The system MUST reflect devices joining and leaving the network without requiring a
  manual refresh.
- **FR-009**: The system MUST NOT make any network request beyond the local network at any point,
  including for fonts, icons, scripts, time, or update checks.

**Pairing and access control**

- **FR-010**: The system MUST require an explicit pairing step, confirmed by a human on the
  receiving device, before any transfer is possible.
- **FR-011**: The system MUST refuse every file operation from an unpaired device, including
  listing.
- **FR-012**: Pairing codes MUST be single-use and MUST expire after a short window.
- **FR-013**: The system MUST resist repeated pairing attempts by rate-limiting and by
  invalidating a code after a small number of failures.
- **FR-014**: The system MUST remember paired devices across restarts so that pairing is a
  one-time step per device.
- **FR-015**: The system MUST list paired devices and MUST let the user revoke any of them, with
  revocation taking effect immediately.
- **FR-016**: Pairings MUST expire after a period of inactivity, and the user MUST be able to see
  that they expired. The window follows the trust mode: 1 year for a device set to accept
  automatically, 30 days for a device set to ask every time. Changing the trust mode MUST
  recompute the window from the device's last activity.
- **FR-016a**: Every pairing MUST carry a trust mode chosen by the user: accept incoming
  transfers automatically, or ask for confirmation each time.
- **FR-016b**: A pairing the user created themselves MUST default to accepting automatically; a
  pairing created for a device that is not theirs MUST default to asking. The user MUST be able
  to change the mode of any pairing at any time, and the change MUST take effect immediately.
- **FR-016c**: The trust mode of each paired device MUST be visible wherever paired devices are
  listed, so the user can tell at a glance which devices can write to their machine unattended.
- **FR-016d**: A transfer from an ask-every-time device MUST be refused, not queued indefinitely,
  when the recipient does not answer within a bounded window, and the sender MUST be told why.
- **FR-017**: Pairing exchanges, credentials, and transfer metadata MUST always be encrypted, in
  every mode. File content is encrypted in trusted mode and travels in the clear in simple mode.
- **FR-018**: The system MUST NEVER expose the filesystem beyond the configured receive folder
  and the files explicitly offered for a transfer, and MUST reject any attempt to reach outside
  those bounds.
- **FR-019**: Pairing codes, keys, and session credentials MUST NEVER appear in logs, error
  messages, or the interface after use.

**Sending and receiving**

- **FR-020**: Users MUST be able to send from the computer by drag and drop as well as by a file
  picker.
- **FR-021**: Users MUST be able to select several files at once, and to send a whole folder with
  its structure preserved.
- **FR-022**: Users MUST be able to send files from the phone, chosen through the phone's own
  picker, to a selected computer.
- **FR-023**: The receiving computer MUST write incoming files into a user-configurable receive
  folder that defaults outside system folders.
- **FR-024**: The system MUST preserve original filenames, and when a name is unusable on the
  destination it MUST store a sanitized name and report the change.
- **FR-025**: The system MUST NOT overwrite an existing file at the destination without the user
  choosing to.
- **FR-026**: The receiving side MUST let the user save received files to the device.

**Size, progress, and reliability**

- **FR-027**: The system MUST NOT impose any file size limit other than free space at the
  destination.
- **FR-028**: The system MUST check free space at the destination before a transfer starts and
  refuse it with a clear message when there is not enough.
- **FR-029**: The system MUST stream transfers so that memory use does not grow with file size.
- **FR-030**: The system MUST show live progress on both devices, including bytes transferred,
  current speed, and estimated time remaining.
- **FR-031**: The system MUST resume an interrupted transfer from where it stopped rather than
  restarting it.
- **FR-032**: The system MUST verify integrity end to end and MUST report a mismatch as a failed
  transfer, never as a success.
- **FR-033**: The system MUST NOT present an incomplete or failed file as if it were complete.
- **FR-034**: The system MUST delete partial data from transfers abandoned for more than 7 days,
  and MUST tell the user when it does.
- **FR-035**: The system MUST let the user cancel an in-progress transfer from either device, and
  cancellation MUST take effect on both.

**History and feedback**

- **FR-035a**: The system MUST run one transfer at a time and hold the others in a queue, rather
  than running several in parallel.
- **FR-035b**: The queue MUST be visible, showing what is waiting, in which order, and for which
  device.
- **FR-035c**: Users MUST be able to reorder the queue, remove an entry from it, and clear it
  entirely.
- **FR-035d**: An interrupted transfer MUST NOT block the queue: the system MUST move on and
  retry it later rather than stalling everything behind it.
- **FR-035e**: Queued transfers MUST survive a restart of the application, or be reported as
  abandoned with their reason.
- **FR-036**: The system MUST show transfers in progress, with the ability to act on each.
- **FR-037**: The system MUST keep a history of completed and failed transfers, recording what
  moved, to or from which device, when, and the outcome.
- **FR-038**: Every failure MUST be reported with a cause and a concrete corrective action, never
  a bare code.
- **FR-039**: The user MUST be able to clear the transfer history.

**Language and presentation**

- **FR-039a**: No user-facing text MUST be hard-coded. Every string MUST come from a translation
  catalogue that a contributor can extend without touching application logic.
- **FR-039b**: The system MUST ship English and French, and MUST select a language from the
  device's own preference, with a manual override the user can set.
- **FR-039c**: File sizes, dates, times, and transfer speeds MUST be formatted according to the
  selected language and regional conventions.
- **FR-039d**: A missing translation MUST fall back to English rather than showing a blank or a
  raw identifier.
- **FR-039e**: The interface MUST remain usable when a translation is longer than the English
  original, without truncating or overlapping text.

**Accessibility**

- **FR-039f**: The essential flows (pairing, sending, receiving, progress, and error recovery)
  MUST meet WCAG 2.2 level AA, on both the desktop application and the mobile page.
- **FR-039g**: Every essential flow MUST be completable with a keyboard alone, with a visible
  focus indicator and a focus order that follows the visual order.
- **FR-039h**: Every control and status MUST expose a name, a role, and a value to assistive
  technology, including the device list, the queue, and transfer states.
- **FR-039i**: Transfer progress MUST be announced to assistive technology at meaningful moments
  (start, interruption, resumption, completion, failure) rather than on every update, so that a
  running transfer does not flood the user with announcements.
- **FR-039j**: Accessibility MUST be verified automatically in the same gate as the other tests,
  so that a regression blocks a merge rather than being discovered later.

**Onboarding and parity**

- **FR-040**: A first-time user MUST be able to reach a successful transfer without reading
  documentation.
- **FR-041**: Once two devices are paired, sending a file MUST take at most three user actions.
- **FR-042**: Every capability MUST behave identically on Linux and on Windows.
- **FR-043**: Every capability MUST work on current Android and iOS browsers, and no essential
  flow MUST depend on a capability that either platform lacks.

**Protection modes**

- **FR-044**: Reaching the default mode MUST NOT present the user with any browser security
  warning, certificate prompt, or installation step.
- **FR-045**: In simple mode, pairing exchanges, credentials, and metadata MUST be encrypted by
  the application using a key established during pairing, while file content travels in the
  clear on the local network.
- **FR-046**: Simple mode MUST NOT depend on any capability a browser reserves for secure
  contexts, and MUST provide its own equivalent wherever an essential flow needs one.
- **FR-047**: The system MUST state plainly, at pairing time and in the transfer view, that
  simple-mode content is readable by anyone on the same network. It MUST NEVER describe simple
  mode as private, secure, or encrypted without that qualification.
- **FR-047a**: The system MUST offer an optional trusted mode, set up once per device, that
  encrypts file content end to end and unlocks reliable resume and streamed writing on the
  phone.
- **FR-047b**: The mode in use MUST be visible for every device, wherever devices are listed and
  during every transfer.
- **FR-047c**: The user MUST be able to require trusted mode for a given device, after which
  simple-mode connections from that device MUST be refused with a clear explanation.
- **FR-047d**: Setting up trusted mode MUST be guided step by step, MUST be abandonable at any
  point without breaking the existing simple-mode pairing, and MUST explain what it buys before
  it asks for anything.
- **FR-047e**: A transfer MUST NOT silently downgrade from trusted mode to simple mode. A
  downgrade MUST be refused or explicitly confirmed by the user.

**Background availability**

- **FR-048**: The desktop application MUST stay reachable by paired devices while running in the
  background, with its window closed.
- **FR-049**: The system MUST offer to start with the user's session, and the user MUST be able
  to turn that off.
- **FR-050**: The user MUST always be able to tell whether fastr is currently listening, and MUST
  be able to stop it completely in one action.
- **FR-051**: While running in the background, the system MUST still refuse every unpaired
  device, MUST apply each pairing's trust mode, and MUST notify the user of every incoming
  transfer, including those accepted automatically.
- **FR-052**: Background operation MUST NOT consume noticeable resources while idle.

**Phone-to-phone transfers**

- **FR-053**: Two phones paired to the same computer MUST be able to transfer files to each
  other, with that computer relaying between them.
- **FR-054**: A relayed transfer MUST require both phones to be paired with the relaying
  computer; pairing MUST NOT be transitive between phones without it.
- **FR-055**: Data held by a computer solely to relay a transfer MUST NOT be written into its
  receive folder, MUST NOT appear as a file it received, and MUST be deleted once the transfer
  ends or is abandoned.
- **FR-056**: The relaying computer's user MUST be able to see relayed transfers passing through
  their machine, and to cancel any of them.
- **FR-057**: A relayed transfer MUST report clearly when it fails because the relaying computer
  became unavailable, and MUST be resumable once a relay is available again.
- **FR-058**: The system MUST refuse to start a relayed transfer when the relaying computer lacks
  the temporary space it requires, and MUST say so before any data moves.

### Key Entities

- **Device**: A computer or a phone participating in transfers. Has a user-visible name, a
  platform, a reachability state, and a pairing state. A phone exists as a device only through
  its browser session.
- **Pairing**: The trust relationship between two devices. Created by an explicit human
  confirmation, remembered across restarts, and revocable at any time. Carries a trust mode,
  either accept automatically or ask every time, which the user can change and which also sets
  how long the pairing survives inactivity.
- **Transfer Queue**: The ordered list of transfers waiting behind the single active one. Visible
  to the user, reorderable, clearable, and durable across restarts of the application.
- **Transfer**: One send operation between two devices. Has a direction, a source and a target
  device, a set of items, a total size, a state (pending, running, interrupted, completed,
  failed, cancelled), progress, and, when it ends badly, a cause.
- **Transfer Item**: A single file inside a transfer. Has an original name, a relative path when
  it came from a folder, a size, an integrity fingerprint, and its own progress and outcome.
- **Receive Folder**: The single location on a computer where incoming files are written. User
  configurable, defaulting outside system folders, and never widened implicitly.
- **Relay Session**: The temporary role a computer takes when passing a transfer between two
  phones. Holds data that belongs to neither the relaying machine nor its receive folder, is
  visible and cancellable by that machine's user, and is destroyed when the transfer ends or is
  abandoned.
- **History Entry**: The durable record of a finished transfer, kept for review and clearable by
  the user.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A first-time user completes their first successful transfer within 2 minutes of
  first launching the application, without consulting documentation, in at least 9 out of 10
  observed attempts.
- **SC-002**: Once two devices are paired, sending a file takes at most 3 user actions, measured
  from the file being ready to the transfer starting.
- **SC-003**: A 10 GB file transfers successfully, and the application's memory use during it
  stays within 10% of its memory use while transferring a 10 MB file.
- **SC-004**: Transfers sustain at least 60% of the raw throughput measured between the same two
  devices on the same network.
- **SC-005**: A transfer interrupted at any point resumes and completes, re-sending no more than
  1% of the data already delivered.
- **SC-006**: 100% of transfers reported as successful produce a file byte-identical to the
  source, across at least 500 test transfers spanning sizes from 0 bytes to 10 GB.
- **SC-007**: 100% of transfers reported as failed leave no file that could be mistaken for a
  complete one.
- **SC-008**: Zero network connections leave the local network during any user scenario,
  verified by monitoring at the network boundary.
- **SC-009**: 100% of file operations attempted by an unpaired device are refused, across every
  entry point.
- **SC-010**: 100% of attempts to reach a path outside the receive folder or the explicitly
  offered files are refused.
- **SC-011**: A device that joins the network appears in the device list within 5 seconds.
- **SC-012**: All primary flows succeed on current Android and iOS browsers and on both Linux and
  Windows, with no capability available on one platform and missing on another.
- **SC-013**: Moving a 500 MB file from a computer to a phone takes fewer than 5 user actions
  and no upload to any third party, compared with the 10 or more actions and full round trip
  through the internet that the cloud-drive workaround requires.
- **SC-014**: Every user-facing error message names a cause and a corrective action, verified
  across the full catalogue of error states.
- **SC-015**: Connecting a new phone produces zero browser security warnings and zero certificate
  prompts, on both Android and iOS browsers.
- **SC-016**: In trusted mode, transfer content is unreadable to a party capturing traffic on the
  local network, verified by inspecting a captured transfer. In simple mode, pairing exchanges,
  credentials, and metadata are unreadable in the same capture, and no credential is recoverable
  from it.
- **SC-016a**: Every screen where a simple-mode transfer is set up or shown states that content
  is readable on the local network, verified across the full interface. Zero screens describe
  simple mode as private, secure, or encrypted without that qualification.
- **SC-016b**: Setting up trusted mode on a phone takes under 3 minutes following the in-app
  guidance alone, and abandoning it midway leaves the existing pairing working.
- **SC-017**: A phone can send a file to a computer whose window has never been opened during
  that session, in 100% of attempts where the computer is powered on and on the network.
- **SC-018**: While idle in the background, the application stays under 1% average processor use
  and under 100 MB of memory.
- **SC-019**: After a relayed phone-to-phone transfer ends, whether it succeeded, failed, or was
  cancelled, zero bytes of relayed content remain on the relaying computer.
- **SC-020**: Trusted-mode encryption costs no more than 25% of the throughput measured for the
  same transfer in simple mode, on a mid-range phone.
- **SC-021**: At most one transfer is active at any moment, verified while ten transfers are
  queued across several devices, and the queue completes all ten without user intervention.
- **SC-022**: Every user-facing string is available in English and in French, with zero
  untranslated strings and zero raw identifiers shown, verified across the full catalogue.
- **SC-023**: Every essential flow is completable using a keyboard alone, and automated
  accessibility checks report zero WCAG 2.2 level AA violations on those flows.
- **SC-024**: Partial data from a transfer abandoned more than 7 days earlier is absent from
  disk, and the user was told it was removed.
- **SC-025**: A device set to ask every time cannot write a single byte to the recipient until a
  human accepts, verified across every entry point.

## Assumptions

- **Both devices are on the same local network.** Any scenario spanning different networks is
  out of scope for this baseline, including any relay or internet fallback.
- **The computer is the hub.** Phones participate through a browser page served by a computer
  running fastr. A phone never serves files on its own, and phone-to-phone transfers therefore
  require a computer to relay them.
- **The desktop application runs in the background.** It stays available with its window closed
  and offers to start with the user's session, so a phone can reach it without anyone touching
  the computer. The user can disable autostart and stop it entirely at any time.
- **Two protection modes, simple by default.** Browsers grant a secure context only to loopback
  addresses, never to a local network address, and outside a secure context neither the native
  cryptography API nor service workers exist. Without service workers, nothing can decrypt a
  stream while the browser writes it to disk, so a large encrypted file could only be received by
  holding it whole in memory, which iOS will not allow.

  The project therefore ships **simple mode** by default: pairing, credentials, and metadata
  encrypted by the application, file content in the clear on the local network, stated plainly in
  the interface. An optional **trusted mode**, set up once per device, establishes a
  browser-trusted channel and restores full content encryption along with reliable resume and
  streamed writing.

  This is a deliberate arbitration between three project principles that cannot all hold at once,
  recorded in constitution v2.0.0. The obligation that replaces the lost guarantee is honesty:
  the interface never claims a protection it does not provide.
- **One receive folder per computer**, rather than a per-device or per-transfer destination
  chosen at send time. Chosen for simplicity; a per-transfer destination can be added later.
- **Transfers are one-shot, not a sync.** The system never mirrors, watches, or reconciles
  folders, and never deletes anything at a destination.
- **Existing files at the destination are never overwritten silently.** Collisions resolve to a
  distinct name.
- **Phones save received files through their browser's normal saving mechanism**, which is what
  the platform allows rather than a folder fastr controls.
- **Pairing lives in the phone browser's site data.** A user who clears that data, or connects
  from private mode, will need to pair again.
- **Only files are transferred**, not clipboard content, text snippets, links, or messages.
- **A single user's own devices** are the target situation. There are no accounts, no roles, no
  per-user permissions, and no multi-tenant concerns.
- **Linux and Windows are the committed desktop platforms.** macOS is neither supported nor
  deliberately broken.
- **Android and iOS browsers, latest two major versions**, are the committed mobile targets.
- **Wired connections are not excluded.** Nothing depends on Wi-Fi specifically, only on the
  devices sharing a local network.
- **Retention windows are fixed and visible to the user**: 7 days for abandoned partial
  transfers, and for pairings 1 year of inactivity when set to accept automatically, 30 days when
  set to ask every time.
