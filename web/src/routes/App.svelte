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
  import PairingScreen from '../lib/PairingScreen.svelte';
  import PendingDevices from '../lib/PendingDevices.svelte';
  import ProtectionNotice from '../lib/ProtectionNotice.svelte';
  import SendPanel from '../lib/SendPanel.svelte';
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

  const desktop = isDesktop();
  let stream: EventStream | null = null;

  onMount(() => {
    installLiveRegions();
    setLanguage(negotiate(localStorage.getItem('fastr.language')));

    session = Session.restore();
    if (session) {
      sender = new Sender(session);
      openStream(session);
      void loadDevices(session);
    }
  });

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
        index >= 0
          ? transfers.map((tr) => (tr.id === id ? updated : tr))
          : [updated, ...transfers];
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
    sender = new Sender(newSession);
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

  <!--
    The host's approval prompt sits above everything, paired or not: on first
    run the computer's own page has no pairing of its own, and a device waiting
    to be let in still needs an answer. It renders nothing when nothing waits.
  -->
  {#if desktop}
    <PendingDevices revision={pendingRevision} />
  {/if}

  {#if !session}
    <PairingScreen {desktop} onpaired={onPaired} onerror={onPairingError} />
  {:else}
    <!--
      Constitution v2.0.1, Principle V: the interface must never claim a
      protection it does not provide, and must say plainly that simple-mode
      content is readable by anyone on the same network. This notice is not
      decoration and is not dismissible.
    -->
    <ProtectionNotice mode="simple" />

    {#if sender}
      <SendPanel
        {devices}
        {sender}
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
