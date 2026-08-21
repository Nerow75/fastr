<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from './i18n.js';

  /**
   * How a phone gets in, per FR-002: the local address and a QR code encoding
   * it, side by side with the pairing code to type.
   *
   * Before this existed the code reached only the terminal's standard output,
   * so a first connection required reading a console. Principle VI asks for a
   * first transfer in under two minutes without reading documentation, and that
   * was not survivable.
   *
   * Everything here comes from `/api/pair/invitation`, which is restricted to
   * loopback: a live pairing code is what turns a stranger on the same Wi-Fi
   * into a paired device, so it is never served to the network.
   */

  interface Props {
    /** True while no phone is connected yet. The code is shown straight away
     *  then, because connecting one is the only thing on the screen to do. */
    expanded: boolean;
    onerror: (failure: unknown) => void;
  }

  let { expanded, onerror }: Props = $props();

  interface Invitation {
    code: string;
    expires_in: number;
    addresses: string[];
    url: string;
    qr?: string;
  }

  let invitation = $state<Invitation | null>(null);
  let revealed = $state(false);
  let remaining = $state(0);
  let loading = $state(false);
  let copied = $state(false);

  let ticker: ReturnType<typeof setInterval> | null = null;

  // With no phone connected there is nothing else on the page to do, so the
  // code is fetched immediately. Once one is connected it sits behind a button:
  // leaving a live code permanently on screen is an invitation to whoever walks
  // past the desk, and it would keep reissuing every three minutes for nobody.
  $effect(() => {
    if (expanded && !invitation && !loading) void load();
  });

  // A plain variable, not $state: this is a comparison against the previous
  // run of the effect, and making it reactive would have that effect retrigger
  // itself. It is left undefined until the effect first runs, rather than
  // seeded from `expanded` here, which would capture only the initial value.
  let previouslyExpanded: boolean | undefined;

  // A phone finished connecting, which spends the code that is on screen.
  // Without this the panel would go on displaying digits that no longer work,
  // and the next person to try them would be told the code was already used.
  $effect(() => {
    const nowExpanded = expanded;
    if (previouslyExpanded === true && !nowExpanded) hide();
    previouslyExpanded = nowExpanded;
  });

  onDestroy(stopTicking);

  async function load(): Promise<void> {
    loading = true;
    try {
      const response = await fetch('/api/pair/invitation');
      if (!response.ok) throw new Error(`invitation: ${response.status}`);

      invitation = (await response.json()) as Invitation;
      revealed = true;
      startTicking(invitation.expires_in);
    } catch (failure) {
      onerror(failure);
    } finally {
      loading = false;
    }
  }

  function startTicking(seconds: number): void {
    stopTicking();
    remaining = Math.max(0, seconds);

    ticker = setInterval(() => {
      remaining -= 1;
      // Expired. Fetching again issues a fresh one rather than leaving digits
      // on screen that stopped working, which used to mean restarting the
      // whole application.
      if (remaining <= 0) void load();
    }, 1000);
  }

  function stopTicking(): void {
    if (ticker !== null) clearInterval(ticker);
    ticker = null;
  }

  function hide(): void {
    stopTicking();
    revealed = false;
    invitation = null;
  }

  async function copyAddress(): Promise<void> {
    if (!invitation?.url) return;
    try {
      await navigator.clipboard.writeText(invitation.url);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      // No clipboard permission. The address is on screen to read, which is
      // what it is there for; a failed copy is not worth an error banner.
    }
  }

  let countdown = $derived(
    `${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, '0')}`,
  );
</script>

<section aria-labelledby="invitation-title" class="panel">
  <h2 id="invitation-title">{t('pairing.invite_title')}</h2>

  {#if !revealed}
    <p class="hint">{t('pairing.invite_hint')}</p>
    <button type="button" onclick={load} disabled={loading}>
      {t('pairing.invite_reveal')}
    </button>
  {:else if invitation}
    {#if invitation.url === ''}
      <!--
        Bound to loopback only. A real state, not a failure: nothing on the
        network can reach this instance, and saying so beats showing an address
        that cannot work.
      -->
      <p class="warning" role="status">{t('pairing.invite_no_address')}</p>
    {:else}
      <div class="layout">
        <div class="details">
          <p class="step">{t('pairing.invite_step_address')}</p>
          <p class="address">
            <span class="url">{invitation.url}</span>
            <button type="button" class="copy" onclick={copyAddress}>
              {copied ? t('pairing.invite_copied') : t('pairing.invite_copy')}
            </button>
          </p>

          <p class="step">{t('pairing.invite_step_code')}</p>
          <p class="code" aria-label={invitation.code.split('').join(' ')}>
            {invitation.code}
          </p>

          <p class="expiry" aria-live="off">
            {t('pairing.invite_expires', { countdown })}
          </p>

          <p class="step">{t('pairing.invite_step_approve')}</p>
        </div>

        {#if invitation.qr}
          <!--
            The QR encodes the address, never the code. Anyone who can see the
            screen well enough to scan it can read the digits anyway, but a code
            baked into a scannable image would survive in camera rolls and
            screenshots long after it expired.
          -->
          <img
            class="qr"
            src={invitation.qr}
            alt={t('pairing.invite_qr_alt')}
            width="180"
            height="180"
          />
        {/if}
      </div>
    {/if}

    {#if !expanded}
      <button type="button" class="secondary" onclick={hide}>{t('pairing.invite_done')}</button>
    {/if}
  {/if}
</section>

<style>
  .panel {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    padding: 1rem;
    margin: 0 0 var(--gap);
  }

  h2 {
    font-size: 1rem;
    margin: 0 0 0.75rem;
  }

  .layout {
    display: flex;
    flex-wrap: wrap;
    gap: var(--gap);
    align-items: flex-start;
  }

  .details {
    flex: 1 1 16rem;
    min-width: 0;
  }

  .step {
    margin: 0.75rem 0 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .step:first-child {
    margin-top: 0;
  }

  .address {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0;
  }

  .url {
    font-family: var(--mono);
    font-size: 1.125rem;
    word-break: break-all;
  }

  .code {
    margin: 0;
    font-family: var(--mono);
    font-size: 2rem;
    letter-spacing: 0.25em;
    font-weight: 600;
  }

  .expiry,
  .hint {
    margin: 0.25rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .warning {
    margin: 0;
    color: var(--danger);
  }

  .qr {
    flex: 0 0 auto;
    border-radius: var(--radius);
    background: #fff;
    padding: 0.5rem;
  }

  button {
    min-height: 2.75rem;
    padding: 0.5rem 1rem;
    font-size: 1rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .copy {
    min-height: 2.25rem;
    padding: 0.25rem 0.75rem;
    font-size: 0.875rem;
  }

  .secondary {
    margin-top: var(--gap);
  }
</style>
