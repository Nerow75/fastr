<script lang="ts">
  import Panel from './Panel.svelte';
  import { t } from './i18n.js';
  import { announce } from './a11y.js';
  import { ApiFailure, type Session } from './session.js';
  import { formatApiError } from './i18n.js';

  /**
   * Setting up trusted mode, per FR-047d.
   *
   * Three rules the requirement states and this panel takes literally.
   *
   * **It explains what it buys before it asks for anything.** The first thing
   * on screen is what changes and what it costs, and no key is generated until
   * the user presses a button having read that. Installing a certificate
   * authority on a phone is a real security decision — anything holding its
   * private key can impersonate any site to that device — and a walkthrough
   * that leads with "step 1 of 4" is asking somebody to agree to something they
   * have not been told.
   *
   * **It can be abandoned at any point.** Nothing here writes anything the
   * simple-mode path reads. Closing the panel halfway leaves the existing
   * pairing working exactly as before, which is the property most likely to rot
   * quietly: nothing visibly breaks when it does, the user simply finds their
   * phone locked out.
   *
   * **It is honest about what is still true.** Until a phone has installed the
   * certificate *and* reached the HTTPS address, that phone is still in simple
   * mode and its content is still readable on this network. The panel says so
   * rather than showing a green tick for work that is half done.
   */

  interface TrustDevice {
    device_id: string;
    name: string;
    protection: string;
    require_trusted: boolean;
  }

  interface Status {
    available: boolean;
    ready: boolean;
    fingerprint?: string;
    addresses: string[];
    certificate_url?: string;
    trusted: boolean;
    devices: TrustDevice[];
  }

  interface Props {
    session: Session;
    /** Bumped by the shell when a pairing changes, so the list follows. */
    revision?: number;
  }

  let { session, revision = 0 }: Props = $props();

  let status = $state<Status | null>(null);
  let expanded = $state(false);
  let working = $state(false);
  let failure = $state<string | null>(null);

  let trustedDevices = $derived(status?.devices.filter((d) => d.protection === 'trusted') ?? []);
  let httpsAddress = $derived(status?.addresses[0] ?? '');

  async function load(): Promise<void> {
    try {
      status = await session.request<Status>('GET', '/api/trust/status');
    } catch {
      // Trusted mode not being available is a working configuration, not a
      // failure worth a banner.
      status = null;
    }
  }

  $effect(() => {
    void revision;
    void load();
  });

  /**
   * Creates the authority and starts serving.
   *
   * The only step that writes anything, and it happens after the explanation
   * rather than before it.
   */
  async function begin(): Promise<void> {
    working = true;
    failure = null;
    try {
      // Sealed like every other control-plane request. It is also restricted
      // to loopback on the server, which is a separate question: this page is
      // the machine, and creating an authority is the machine's decision.
      await session.request('POST', '/api/trust/init', {});
      await load();
      announce(t('trusted.ready_announcement'));
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    } finally {
      working = false;
    }
  }
</script>

{#if status?.available}
  <!--
    Folded away by default. Setting up trusted mode is a real decision made
    once, not something anybody needs in front of them while sending a file —
    and the honesty it owes the user is carried by the protection notice, which
    is never foldable.
  -->
  <Panel
    id="trusted"
    title={t('trusted.title')}
    hint={status.ready ? t('panel.hint_trusted_ready') : t('panel.hint_trusted_off')}
    tone="quiet"
    collapsible
    open={false}
  >
    <!--
      What it buys and what it costs, in that order, before any button. The
      second half is not fine print: it is the reason this is opt-in.
    -->
    <p>{t('trusted.what_it_buys')}</p>
    <p class="cost">{t('trusted.what_it_costs')}</p>

    {#if !status.ready}
      {#if expanded}
        <p class="muted">{t('trusted.before_you_start')}</p>
        <div class="actions">
          <button type="button" onclick={() => (expanded = false)}>{t('action.cancel')}</button>
          <button type="button" class="primary" disabled={working} onclick={begin}>
            {t('trusted.create')}
          </button>
        </div>
      {:else}
        <div class="actions">
          <button type="button" onclick={() => (expanded = true)}>{t('trusted.setup_cta')}</button>
        </div>
      {/if}
    {:else}
      <ol class="steps">
        <li>
          <p>{t('trusted.step_install')}</p>
          {#if status.certificate_url}
            <a class="download" href={status.certificate_url} download="fastr-ca.crt">
              {t('trusted.download_certificate')}
            </a>
          {/if}
          <!--
            The fingerprint is what turns "install this" from a leap into a
            check. It is shown here so it can be compared with what the phone
            displays when it asks.
          -->
          <p class="fingerprint" aria-label={t('trusted.fingerprint_label')}>
            {status.fingerprint}
          </p>
          <p class="muted">{t('trusted.fingerprint_hint')}</p>
        </li>
        <li>
          <p>{t('trusted.step_trust')}</p>
          <p class="muted">{t('trusted.step_trust_ios')}</p>
          <p class="muted">{t('trusted.step_trust_android')}</p>
        </li>
        <li>
          <p>{t('trusted.step_open')}</p>
          {#if httpsAddress}
            <p class="address"><code>https://{httpsAddress}</code></p>
          {:else}
            <p class="muted">{t('trusted.no_address')}</p>
          {/if}
        </li>
      </ol>

      {#if trustedDevices.length > 0}
        <p class="done">
          {t('trusted.devices_done', { names: trustedDevices.map((d) => d.name).join(', ') })}
        </p>
      {:else}
        <!-- Honest while the work is half done: nothing has changed for any
             device until one has actually arrived over HTTPS. -->
        <p class="muted">{t('trusted.none_yet')}</p>
      {/if}
    {/if}

    {#if failure}
      <p class="warning" role="alert">{failure}</p>
    {/if}
  </Panel>
{/if}

<style>
  p {
    margin: 0 0 0.5rem;
    font-size: 0.9375rem;
  }

  .cost {
    color: var(--text-muted);
  }

  .muted {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .steps {
    margin: 0.75rem 0 0;
    padding-left: 1.25rem;
  }

  .steps li {
    margin-bottom: 0.75rem;
  }

  .fingerprint {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.75rem;
    word-break: break-all;
    margin: 0.25rem 0;
  }

  .address code {
    font-size: 0.9375rem;
    word-break: break-all;
  }

  .done {
    color: var(--accent);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }

  button,
  .download {
    min-height: 2.75rem;
    display: inline-flex;
    align-items: center;
    padding: 0.5rem 1rem;
    font-size: 0.9375rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
    text-decoration: none;
  }

  .primary {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-text);
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .warning {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }
</style>
