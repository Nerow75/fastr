<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    Session,
    isDesktop,
    ApiFailure,
    PairingRejected,
    PairingExpired,
    PairingAbandoned,
    PairingAlreadyCollected,
  } from '../lib/session.js';
  import { EventStream, type ServerEvent } from '../lib/events.js';
  import { installLiveRegions, focusView } from '../lib/a11y.js';
  import { t, negotiate, setLanguage, formatApiError } from '../lib/i18n.js';
  import { Sender, type Transfer } from '../lib/transfers.js';
  import { Uploader } from '../lib/upload.js';
  import PairingScreen from '../lib/PairingScreen.svelte';
  import ConnectionInvitation from '../lib/ConnectionInvitation.svelte';
  import PendingDevices from '../lib/PendingDevices.svelte';
  import ProtectionNotice from '../lib/ProtectionNotice.svelte';
  import SendPanel from '../lib/SendPanel.svelte';
  import MobilePicker from '../lib/MobilePicker.svelte';
  import DeviceList from '../lib/DeviceList.svelte';
  import QueueView from '../lib/QueueView.svelte';
  import HistoryView from '../lib/HistoryView.svelte';
  import RelayView from '../lib/RelayView.svelte';
  import TransferProgress from '../lib/TransferProgress.svelte';

  // The shell decides three things: which language to render in, whether this
  // device is paired, and whether it is the desktop or a phone. Everything else
  // is a view.

  interface Device {
    id: string;
    name: string;
    kind: string;
    paired: boolean;
    trust_mode?: string;
  }

  /** A computer seen on the network that this device has not paired with. */
  interface Discovered {
    id: string;
    name: string;
    label: string;
    kind: string;
    platform?: string;
    addresses: string[];
    reachable?: boolean;
    source: string;
    version: number;
  }

  let session = $state<Session | null>(null);
  let connected = $state(false);
  let error = $state<string | null>(null);
  // Something happened that the user should know about but need not act on: the
  // retention sweep removing data, so far. Dismissible, unlike an error.
  let notice = $state<string | null>(null);
  let main = $state<HTMLElement | null>(null);
  // Bumped on every pairing_pending event, so the approval panel refetches
  // without polling hard.
  let pendingRevision = $state(0);

  let devices = $state<Device[]>([]);
  let discovered = $state<Discovered[]>([]);
  // What is waiting and what is running, as the server sees it. Read from the
  // queue rather than derived from `transfers`, because the order is the
  // server's and two pages must not disagree about it.
  let queued = $state<Transfer[]>([]);
  let runningNow = $state<Transfer | null>(null);
  // Bumped whenever a transfer ends, so the history reloads without polling.
  let historyRevision = $state(0);
  let discovery = $state<{ available: boolean; reason?: string }>({ available: true });
  let transfers = $state<Transfer[]>([]);
  let sender = $state<Sender | null>(null);
  let uploader = $state<Uploader | null>(null);

  // The computer serving this page. A phone sending a file has to name it as
  // the target, and /connect is where it learns the identifier: it is
  // unauthenticated by design and carries nothing the mDNS record does not
  // already broadcast.
  let host = $state<{ name: string; device_id: string }>({ name: '', device_id: '' });

  const desktop = isDesktop();
  let stream: EventStream | null = null;

  // Everything except this device. The computer now holds a session of its own,
  // so it appears in its own device list; offering it as a destination would be
  // offering to send a file to itself, which the server refuses anyway.
  let peers = $derived(devices.filter((d) => d.id !== session?.deviceId));
  let hasPairedPeer = $derived(peers.some((d) => d.paired));

  // Transfers this device is sending that have not finished. After a reload
  // they come from the queue rather than from memory, and they are what lets
  // picking the same file again continue an upload instead of starting a
  // second copy of it.
  let resumable = $derived(
    transfers.filter(
      (tr) =>
        tr.source_device_id === session?.deviceId &&
        !['completed', 'failed', 'cancelled'].includes(tr.state),
    ),
  );

  onMount(() => {
    installLiveRegions();
    setLanguage(negotiate(localStorage.getItem('fastr.language')));

    if (!desktop) void loadHost();

    session = Session.restore();
    if (session) {
      begin(session);
      return;
    }

    // The computer's own page is the machine, not a device asking to be let in,
    // so it is granted a session rather than made to type a code at itself.
    // A phone still pairs, which is what the code and the QR are for.
    if (desktop) void adoptHostSession();
  });

  async function adoptHostSession(): Promise<void> {
    try {
      begin(await Session.adoptHost());
    } catch (failure) {
      onPairingError(failure);
    }
  }

  function begin(active: Session): void {
    session = active;
    startClients(active);
    openStream(active);
    void loadDevices(active);
    // Reconciled here as well as on every stream connect, and deliberately not
    // only there. A page that has a session can read the queue; making the list
    // of transfers wait for the event stream means a page whose stream is slow
    // to come up shows nothing at all, and on a loaded machine that is a real
    // state to be in for several seconds. FR-036 asks for transfers in progress
    // to be visible, not for them to be visible once SSE agrees.
    void reconcile(active);
  }

  function startClients(active: Session): void {
    // Two clients, because the two directions are genuinely different
    // mechanisms rather than one with a flag. The desktop holds files and
    // supplies them into a pipe; the phone pushes chunks to a file. See
    // internal/transfer/pipe.go and web/src/lib/upload.ts.
    sender = new Sender(active);
    uploader = new Uploader(active);
  }

  async function loadHost(): Promise<void> {
    try {
      const reply = await fetch('/connect');
      if (reply.ok) host = (await reply.json()) as { name: string; device_id: string };
    } catch {
      // Unreachable means the page is already broken in more visible ways.
    }
  }

  onDestroy(() => stream?.stop());

  function openStream(active: Session): void {
    stream?.stop();
    stream = new EventStream(active);
    stream.onConnectionChange((state) => {
      connected = state;
      if (!state) return;
      // Reconciled before anything else, because everything else depends on
      // knowing which transfers exist. A page learns about a transfer from the
      // event announcing it, and an event announced while nothing was listening
      // is gone: a phone that reloads mid-transfer would otherwise see no
      // progress, no Save button, and no way back to a file that is still
      // moving. Reading the queue answers the question the missed events would
      // have.
      void reconcile(active);
      // And a page that was away may have missed a demand, which would leave
      // the receiver waiting until the supply timeout.
      void resumeAllWaiting();
    });
    stream.on(handleEvent);
    stream.start();
  }

  async function loadDevices(active: Session): Promise<void> {
    try {
      const body = await active.request<{
        devices: Device[];
        discovered?: Discovered[];
        discovery?: { available: boolean; reason?: string };
      }>('GET', '/api/devices');
      devices = body.devices ?? [];
      discovered = body.discovered ?? [];
      discovery = body.discovery ?? { available: true };
    } catch (failure) {
      onPairingError(failure);
    }
  }

  async function refreshTransfer(id: string): Promise<void> {
    if (!session) return;
    try {
      const updated = await session.request<Transfer>('GET', `/api/transfers/${id}`);
      const index = transfers.findIndex((tr) => tr.id === id);
      transfers =
        index >= 0 ? transfers.map((tr) => (tr.id === id ? updated : tr)) : [updated, ...transfers];
    } catch {
      // Gone, or not ours. Neither is worth an error banner.
    }
  }

  /**
   * Reads the transfers this device is a party to and is not finished with.
   *
   * Everything not yet terminal is either the active transfer or one of the
   * waiting entries, so the queue is the complete answer rather than an
   * approximation of it. Merged rather than replacing: a transfer this page
   * declared a moment ago may not have reached the queue read yet, and losing it
   * from the list would be the same bug in the other direction.
   */
  async function reconcile(active: Session): Promise<void> {
    try {
      const queue = await active.request<{
        entries: Transfer[];
        active?: Transfer | null;
      }>('GET', '/api/queue');

      queued = queue.entries ?? [];
      runningNow = queue.active ?? null;

      const live = [...(queue.active ? [queue.active] : []), ...queued];
      const known = new Set(live.map((tr) => tr.id));

      transfers = [...live, ...transfers.filter((tr) => !known.has(tr.id))];
    } catch {
      // A page with no queue is a page that shows what it was told about, which
      // is where it started. Not worth an error banner.
    }
  }

  /**
   * Puts a transfer at the top of the list, replacing it if it is already
   * there.
   *
   * Resuming produces the same transfer twice: once from the queue on connect,
   * once from the panel that just continued it. Prepending both would give the
   * keyed each block two entries with one key, which Svelte refuses outright.
   */
  function noteTransfer(transfer: Transfer): void {
    transfers = [transfer, ...transfers.filter((tr) => tr.id !== transfer.id)];
  }

  async function resumeAllWaiting(): Promise<void> {
    if (!sender) return;
    for (const transfer of transfers) {
      if (sender.holds(transfer.id)) await sender.resumeWaiting(transfer.id);
    }
  }

  /**
   * Answers an incoming transfer from an ask-every-time device.
   *
   * Only ever reaches the server for a transfer this device is the target of;
   * the server refuses the rest, and the interface does not offer them.
   */
  async function answerTransfer(id: string, verb: 'accept' | 'decline'): Promise<void> {
    if (!session) return;
    try {
      await session.request('POST', `/api/transfers/${encodeURIComponent(id)}/${verb}`, {});
      void refreshTransfer(id);
      void reconcile(session);
    } catch (failure) {
      onPairingError(failure);
    }
  }

  async function cancelTransfer(id: string): Promise<void> {
    if (!session) return;
    try {
      if (sender?.holds(id)) {
        await sender.cancel(id);
      } else {
        await session.request('POST', `/api/transfers/${id}/cancel`, {});
      }
    } catch (failure) {
      onPairingError(failure);
    }
  }

  function handleEvent(event: ServerEvent): void {
    if (event.type === 'pairing_pending') {
      pendingRevision += 1;
      return;
    }

    // A device opened or closed its page, or the set of machines seen on the
    // network moved. Either way the list is otherwise a snapshot from when this
    // page loaded, and FR-008 forbids making the user refresh it.
    if (
      event.type === 'device_appeared' ||
      event.type === 'device_lost' ||
      event.type === 'discovery_changed'
    ) {
      if (session) void loadDevices(session);
      return;
    }

    if (event.type === 'pairing_changed') {
      if (Session.restore()) {
        if (session) void loadDevices(session);
        return;
      }
      // This device's own pairing was revoked from the computer.
      session = null;
      stream?.stop();
      return;
    }

    // The order changed, or an entry was removed, on some other page.
    if (event.type === 'queue_changed') {
      if (session) void reconcile(session);
      return;
    }

    // FR-034: the retention sweep tells the user what it took. It runs at
    // startup and daily, so this is usually the first thing a page hears.
    if (event.type === 'sweep_removed') {
      const removed = (event.payload?.removed ?? []) as { kind: string; id: string }[];
      const gone = new Set(removed.filter((r) => r.kind === 'transfer').map((r) => r.id));
      transfers = transfers.filter((tr) => !gone.has(tr.id));

      // Said by kind, because "3 things were removed" is not something anyone
      // can act on. A sweep after a long absence takes both.
      const pairings = removed.filter((r) => r.kind === 'pairing').length;
      const lines = [
        gone.size > 0 ? t('sweep.removed_partials', { count: gone.size }) : '',
        pairings > 0 ? t('sweep.expired_pairings', { count: pairings }) : '',
      ].filter(Boolean);

      if (lines.length > 0) notice = lines.join(' ');
      return;
    }

    if (!event.transfer_id) return;

    // The server asks whoever holds the files to supply an item from an
    // offset. Only the page that actually holds them can answer.
    const supply = event.payload?.supply;
    if (typeof supply === 'number' && sender?.holds(event.transfer_id)) {
      void sender.supply(event.transfer_id, supply, Number(event.payload?.offset ?? 0));
    }

    void refreshTransfer(event.transfer_id);

    // The queue moved. Read back rather than guessed at, because the order is
    // the server's and two pages must not disagree about it. Progress events
    // are excluded deliberately: they arrive four times a second and never
    // change what is queued.
    if (
      session &&
      ['transfer_queued', 'transfer_started', 'transfer_interrupted'].includes(event.type)
    ) {
      void reconcile(session);
    }

    if (['transfer_completed', 'transfer_failed', 'transfer_cancelled'].includes(event.type)) {
      sender?.release(event.transfer_id);
      // Something ended, so both the queue and the record of what happened
      // moved. FR-036 and FR-037.
      historyRevision += 1;
      if (session) void reconcile(session);
    }
  }

  async function onPaired(newSession: Session): Promise<void> {
    session = newSession;
    startClients(newSession);
    error = null;
    openStream(newSession);
    void loadDevices(newSession);
    // A screen reader user must be told the page became something else, or
    // their focus stays on the button they pressed.
    await Promise.resolve();
    focusView(main, t('app.name'));
  }

  /**
   * Renders a failed pairing.
   *
   * A refusal, a timeout, or an abandonment are answers a human gave or failed
   * to give. They are not server errors and must not read like one: "the
   * request was refused" tells the user what to do next, "something went wrong"
   * does not.
   */
  function onPairingError(failure: unknown): void {
    if (failure instanceof PairingRejected) {
      error = t('pairing.rejected');
    } else if (failure instanceof PairingExpired) {
      error = t('pairing.expired');
    } else if (failure instanceof PairingAbandoned) {
      error = t('pairing.abandoned');
    } else if (failure instanceof PairingAlreadyCollected) {
      error = t('pairing.already_collected');
    } else if (failure instanceof ApiFailure) {
      error = formatApiError(failure.body);
    } else {
      error = t('error.internal');
    }
  }
</script>

<a class="skip-link" href="#main">{t('nav.skip_to_content')}</a>

<header>
  <h1>{t('app.name')}</h1>
  {#if session}
    <p class="status" aria-live="polite">
      <span class="dot" class:connected aria-hidden="true"></span>
      {connected ? t('settings.running') : t('settings.stopped')}
    </p>
  {/if}
</header>

<main id="main" bind:this={main}>
  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  {#if notice}
    <!-- status rather than alert: it is worth reading, not worth interrupting. -->
    <p class="notice" role="status">
      {notice}
      <button type="button" class="dismiss" onclick={() => (notice = null)}>
        {t('action.dismiss')}
      </button>
    </p>
  {/if}

  {#if desktop}
    <!--
      FR-002: the address and the QR that encodes it, with the code to type on
      the phone. Expanded until a phone is actually connected, because until
      then it is the only thing on this screen worth doing.

      Above the approval prompt because it comes first in time — a device has to
      be invited before there is anything to approve.
    -->
    <ConnectionInvitation expanded={!hasPairedPeer} onerror={onPairingError} />
    <!--
      A device waiting to be let in still needs an answer, and it renders
      nothing when nothing waits.
    -->
    <PendingDevices revision={pendingRevision} />
  {/if}

  {#if !session}
    <!--
      Only a phone ever gets here. The computer's page is granted its session on
      load, because it is the machine rather than a device asking to be let in.
    -->
    {#if !desktop}
      <PairingScreen {desktop} onpaired={onPaired} onerror={onPairingError} />
    {/if}
  {:else}
    <!--
      Constitution v2.0.1, Principle V: the interface must never claim a
      protection it does not provide, and must say plainly that simple-mode
      content is readable by anyone on the same network. This notice is not
      decoration and is not dismissible.
    -->
    <ProtectionNotice mode="simple" />

    <!--
      The desktop drops files into a pipe; the phone pushes them to a file.
      Which panel appears follows from which device this is, not from a
      preference, because only one of the two mechanisms works on each.
    -->
    <!--
      Only the desktop. Discovery is computer to computer — a phone exists
      through its browser and never advertises — and adding a device by address
      is restricted to loopback for the same reason every other trust change is.
    -->
    {#if desktop && session}
      <DeviceList
        {session}
        devices={peers}
        {discovered}
        {discovery}
        onchanged={() => session && loadDevices(session)}
      />
    {/if}

    {#if desktop && sender}
      <SendPanel devices={peers} {sender} onsent={noteTransfer} onerror={onPairingError} />
    {:else if !desktop && uploader}
      <MobilePicker
        targetName={host.name}
        targetDeviceId={host.device_id}
        {uploader}
        peers={peers.filter((d) => d.paired)}
        unfinished={resumable}
        onsent={noteTransfer}
        onerror={onPairingError}
      />
    {/if}

    <!--
      The queue and the record of what happened. Both are the desktop's: the
      queue is this machine's single slot, and the history is what happened
      here, including with a phone that is no longer in the house.
    -->
    <!--
      What is passing through this machine between two other devices. It renders
      nothing unless something is, which is almost always. FR-056.
    -->
    {#if desktop && session}
      <RelayView {session} revision={historyRevision} oncancel={cancelTransfer} />
    {/if}

    {#if desktop && session}
      <QueueView
        {session}
        entries={queued}
        active={runningNow}
        onchanged={() => session && reconcile(session)}
      />
    {/if}

    <section aria-labelledby="transfers-title">
      <h2 id="transfers-title">{t('nav.transfers')}</h2>
      {#if transfers.length === 0}
        <p class="muted">{t('transfer.none')}</p>
      {:else}
        {#each transfers as transfer (transfer.id)}
          <TransferProgress
            {transfer}
            {session}
            sending={sender?.holds(transfer.id) ?? false}
            oncancel={cancelTransfer}
            onanswer={answerTransfer}
          />
        {/each}
      {/if}
    </section>

    {#if session}
      <HistoryView {session} canClear={desktop} revision={historyRevision} />
    {/if}
  {/if}
</main>

<style>
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--gap);
    max-width: 60rem;
    margin: 0 auto;
    padding: var(--gap) var(--gap) 0;
  }

  h1 {
    font-size: 1.5rem;
    margin: 0;
    letter-spacing: -0.02em;
  }

  .status {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background: var(--text-muted);
  }

  /* Colour is not the only signal: the adjacent text says the same thing,
     so a user who cannot distinguish these still knows the state. */
  .dot.connected {
    background: var(--accent);
  }

  main {
    max-width: 60rem;
    margin: 0 auto;
    padding: var(--gap);
  }

  h2 {
    font-size: 1rem;
    margin: var(--gap) 0 0.5rem;
  }

  .muted {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .error {
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    padding: 0.75rem 1rem;
    color: var(--danger);
    background: var(--surface);
  }

  .notice {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.75rem 1rem;
    background: var(--surface);
    font-size: 0.9375rem;
  }

  .dismiss {
    min-height: 2.75rem;
    flex-shrink: 0;
    padding: 0.4rem 0.9rem;
    font-size: 0.875rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }
</style>
