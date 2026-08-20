<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Session, isDesktop, ApiFailure } from '../lib/session.js';
  import { EventStream, type ServerEvent } from '../lib/events.js';
  import { installLiveRegions, focusView } from '../lib/a11y.js';
  import { t, negotiate, setLanguage, formatApiError } from '../lib/i18n.js';
  import PairingScreen from '../lib/PairingScreen.svelte';
  import ProtectionNotice from '../lib/ProtectionNotice.svelte';

  // The shell decides three things: which language to render in, whether this
  // device is paired, and whether it is the desktop or a phone. Everything else
  // is a view.

  let session = $state<Session | null>(null);
  let connected = $state(false);
  let error = $state<string | null>(null);
  let main = $state<HTMLElement | null>(null);

  const desktop = isDesktop();
  let stream: EventStream | null = null;

  onMount(() => {
    installLiveRegions();
    setLanguage(negotiate(localStorage.getItem('fastr.language')));

    session = Session.restore();
    if (session) openStream(session);
  });

  onDestroy(() => stream?.stop());

  function openStream(active: Session): void {
    stream?.stop();
    stream = new EventStream(active);
    stream.onConnectionChange((state) => (connected = state));
    stream.on(handleEvent);
    stream.start();
  }

  function handleEvent(event: ServerEvent): void {
    // Phase 2 wires the transport. The views that consume these arrive with
    // their user stories; until then the shell only needs to know the stream
    // is alive.
    if (event.type === 'pairing_changed' && !Session.restore()) {
      // This device's own pairing was revoked from the computer.
      session = null;
      stream?.stop();
    }
  }

  async function onPaired(newSession: Session): Promise<void> {
    session = newSession;
    error = null;
    openStream(newSession);
    // A screen reader user must be told the page became something else, or
    // their focus stays on the button they pressed.
    await Promise.resolve();
    focusView(main, t('app.name'));
  }

  function onPairingError(failure: unknown): void {
    error = failure instanceof ApiFailure ? formatApiError(failure.body) : t('error.internal');
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

    <p>{desktop ? t('nav.devices') : t('transfer.send')}</p>
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

  .error {
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    padding: 0.75rem 1rem;
    color: var(--danger);
    background: var(--surface);
  }
</style>
