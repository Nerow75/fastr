<script lang="ts">
  import { t } from './i18n.js';
  import { announce } from './a11y.js';
  import { ApiFailure, type Session } from './session.js';
  import { formatApiError } from './i18n.js';

  /**
   * The devices around this computer, in one list.
   *
   * A person does not think in terms of "paired devices" and "computers on the
   * network": they think about the machines in the house, some of which they
   * have connected to before. So both sets are rendered here, in that order,
   * with each row saying which it is.
   *
   * The address field is not hidden behind the discovery failure. Multicast is
   * blocked on plenty of networks and the failure is not always detectable from
   * inside the browser, so a user who cannot see the machine next to them needs
   * the fallback to be visible rather than conditional. It is simply
   * emphasised, with a reason, when discovery is known not to be working.
   */

  interface PairedDevice {
    id: string;
    name: string;
    kind: string;
    paired: boolean;
    connected?: boolean;
    /** 'auto' or 'ask'. Absent for a device that is not paired. */
    trust_mode?: string;
  }

  interface DiscoveredDevice {
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

  interface Props {
    session: Session;
    devices: PairedDevice[];
    discovered: DiscoveredDevice[];
    /** Whether automatic discovery is working, and why not when it is not. */
    discovery: { available: boolean; reason?: string };
    /** Asks the shell to reload the list after a manual entry succeeds. */
    onchanged: () => void;
  }

  let { session, devices, discovered, discovery, onchanged }: Props = $props();

  let address = $state('');
  let adding = $state(false);
  let failure = $state<string | null>(null);
  /** The device whose removal is being confirmed, if any. */
  let removing = $state<string | null>(null);

  let canAdd = $derived(address.trim() !== '' && !adding);

  async function add(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!canAdd) return;

    adding = true;
    failure = null;
    try {
      const added = await session.request<{ label: string }>('POST', '/api/devices/manual', {
        address: address.trim(),
      });
      address = '';
      announce(t('device.manual_added', { name: added.label }));
      onchanged();
    } catch (error) {
      // The address field is the one place a person can type something wrong,
      // so it gets the reason rather than a generic failure: "nothing answered
      // there" and "that is this computer" call for different next steps.
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    } finally {
      adding = false;
    }
  }

  /**
   * Changes whether a device may write here without anyone looking.
   *
   * Takes effect on the next transfer rather than on the next pairing, which is
   * what FR-016b means by immediately: a phone that was mine becomes a phone I
   * lent to someone, and waiting a year for that to matter would be useless.
   */
  async function setTrust(device: PairedDevice, mode: string): Promise<void> {
    failure = null;
    try {
      await session.request('PATCH', `/api/pairings/${encodeURIComponent(device.id)}`, {
        trust_mode: mode,
      });
      announce(t('device.trust_changed', { name: device.name, mode: t(`device.trust.${mode}`) }));
      onchanged();
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    }
  }

  /** Removes a device's access. FR-015: immediately, not at the next restart. */
  async function revoke(device: PairedDevice): Promise<void> {
    failure = null;
    try {
      await session.request('DELETE', `/api/pairings/${encodeURIComponent(device.id)}`, {});
      removing = null;
      announce(t('device.revoked', { name: device.name }));
      onchanged();
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    }
  }

  function reachability(device: DiscoveredDevice): string {
    if (device.reachable === undefined) return t('device.checking');
    return device.reachable ? t('device.reachable') : t('device.unreachable');
  }
</script>

<section aria-labelledby="devices-title" class="panel">
  <h2 id="devices-title">{t('device.list_title')}</h2>

  {#if devices.length === 0 && discovered.length === 0}
    <p class="muted">{t('device.empty')}</p>
  {/if}

  {#if devices.length > 0}
    <ul class="devices">
      {#each devices as device (device.id)}
        <li>
          <span class="name">{device.name}</span>
          <!-- Two facts, not one. Paired is a lasting relationship; connected
               is whether the page is open right now, and a user needs both to
               understand why a device cannot be sent to. -->
          <span class="tag">{device.paired ? t('device.paired') : t('device.not_paired')}</span>
          <span class="tag" class:live={device.connected}>
            {device.connected ? t('device.reachable') : t('device.unreachable')}
          </span>

          {#if device.paired}
            <!--
              FR-016c: the trust mode is visible wherever paired devices are
              listed, so the user can tell at a glance which of them can write
              to this machine unattended. It is a control rather than a label,
              because seeing it and wanting to change it are the same moment.
            -->
            <label class="sr-only" for={`trust-${device.id}`}>
              {t('device.trust_label', { name: device.name })}
            </label>
            <select
              id={`trust-${device.id}`}
              value={device.trust_mode ?? 'auto'}
              onchange={(event) =>
                setTrust(device, (event.currentTarget as HTMLSelectElement).value)}
            >
              <option value="auto">{t('device.trust.auto')}</option>
              <option value="ask">{t('device.trust.ask')}</option>
            </select>

            {#if removing === device.id}
              <span class="confirm">{t('device.revoke_confirm', { name: device.name })}</span>
              <button type="button" onclick={() => (removing = null)}>{t('action.cancel')}</button>
              <button type="button" class="danger" onclick={() => revoke(device)}>
                {t('device.revoke')}
              </button>
            {:else}
              <button
                type="button"
                onclick={() => (removing = device.id)}
                aria-label={t('device.revoke_named', { name: device.name })}
              >
                {t('device.revoke')}
              </button>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if discovered.length > 0}
    <h3>{t('device.discovered_title')}</h3>
    <ul class="devices">
      {#each discovered as device (device.id)}
        <li>
          <span class="name">{device.label}</span>
          <span class="tag" class:live={device.reachable}>{reachability(device)}</span>
          {#if device.source === 'manual'}
            <span class="tag">{t('device.source_manual')}</span>
          {/if}
          {#if device.addresses.length > 0}
            <!--
              A link rather than a button. Pairing needs the code shown on the
              other machine's own screen, so the only thing this side can
              usefully do is take the user there.
            -->
            <a class="open" href={`http://${device.addresses[0]}`} target="_blank" rel="noreferrer">
              {t('device.open')}
            </a>
          {/if}
        </li>
      {/each}
    </ul>
    <p class="muted">{t('device.discovered_hint')}</p>
  {/if}

  {#if !discovery.available}
    <p class="warning" role="status">{t('device.discovery_unavailable')}</p>
  {/if}

  <form onsubmit={add} class="manual">
    <label for="manual-address">{t('device.manual_entry')}</label>
    <div class="row">
      <input
        id="manual-address"
        type="text"
        inputmode="numeric"
        autocomplete="off"
        placeholder="192.168.1.20:7420"
        bind:value={address}
        disabled={adding}
      />
      <button type="submit" disabled={!canAdd}>{t('device.manual_add')}</button>
    </div>
    <p class="muted">{t('device.manual_hint')}</p>
    {#if failure}
      <p class="warning" role="alert">{failure}</p>
    {/if}
  </form>
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

  h3 {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--text-muted);
    margin: 1rem 0 0.5rem;
  }

  .devices {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .devices li {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
  }

  .name {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* A tag reads as text, not as colour alone: the word is the signal and the
     tint is decoration, so this stays legible to anyone who cannot tell them
     apart. */
  .tag {
    flex-shrink: 0;
    font-size: 0.8125rem;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.1rem 0.45rem;
  }

  .tag.live {
    color: var(--accent);
    border-color: var(--accent);
  }

  select {
    min-height: 2.75rem;
    font-size: 0.875rem;
    padding: 0.2rem 0.4rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text);
  }

  .confirm {
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .danger {
    border-color: var(--danger);
    color: var(--danger);
  }

  .open {
    min-height: 2.75rem;
    display: inline-flex;
    align-items: center;
    padding: 0.3rem 0.8rem;
    font-size: 0.875rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    text-decoration: none;
  }

  .manual {
    margin-top: 1rem;
  }

  label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .row {
    display: flex;
    gap: 0.5rem;
  }

  input {
    flex: 1 1 auto;
    min-height: 2.75rem;
    font-size: 1rem;
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text);
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

  .muted {
    margin: 0.5rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .warning {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }
</style>
