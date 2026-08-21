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
  import TransferProgress from '../lib/TransferProgress.svelte';

  // The shell decides three things: which language to render in, whether this
  // device is paired, and whether it is the desktop or a phone. Everything else
  // is a view.

  interface Device {
    id: string;
    name: string;
    kind: string;
    paired: boolean;
  }

  let session = $state<Session | null>(null);
  let connected = $state(false);
  let error = $state<string | null>(null);
  let main = $state<HTMLElement | null>(null);
  // Bumped on every pairing_pending event, so the approval panel refetches
  // without polling hard.
  let pendingRevision = $state(0);

  let devices = $state<Device[]>([]);
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
      // A page that was away may have missed a demand, which would otherwise
      // leave the receiver waiting until the supply timeout.
      if (state) void resumeAllWaiting();
    });
    stream.on(handleEvent);
    stream.start();
  }

  async function loadDevices(active: Session): Promise<void> {
    try {
      const body = await active.request<{ devices: Device[] }>('GET', '/api/devices');
      devices = body.devices ?? [];
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

  async function resumeAllWaiting(): Promise<void> {
    if (!sender) return;
    for (const transfer of transfers) {
      if (sender.holds(transfer.id)) await sender.resumeWaiting(transfer.id);
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

    // A device opened or closed its page, which changes whether it can be sent
    // to. The list is otherwise a snapshot from when this page loaded.
    if (event.type === 'device_appeared' || event.type === 'device_lost') {
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

    if (!event.transfer_id) return;

    // The server asks whoever holds the files to supply an item from an
    // offset. Only the page that actually holds them can answer.
    const supply = event.payload?.supply;
    if (typeof supply === 'number' && sender?.holds(event.transfer_id)) {
      void sender.supply(event.transfer_id, supply, Number(event.payload?.offset ?? 0));
    }

    void refreshTransfer(event.transfer_id);

    if (['transfer_completed', 'transfer_failed', 'transfer_cancelled'].includes(event.type)) {
      sender?.release(event.transfer_id);
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
    {#if desktop && sender}
      <SendPanel
        devices={peers}
        {sender}
        onsent={(transfer) => (transfers = [transfer, ...transfers])}
        onerror={onPairingError}
      />
    {:else if !desktop && uploader}
      <MobilePicker
        targetName={host.name}
        targetDeviceId={host.device_id}
        {uploader}
        onsent={(transfer) => (transfers = [transfer, ...transfers])}
        onerror={onPairingError}
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
          />
        {/each}
      {/if}
    </section>
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
</style>
