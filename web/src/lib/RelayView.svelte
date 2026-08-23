<script lang="ts">
  import { onMount } from 'svelte';
  import { t, formatBytes } from './i18n.js';
  import { ApiFailure, type Session } from './session.js';
  import { formatApiError } from './i18n.js';

  /**
   * What is passing through this computer between two phones, per FR-056.
   *
   * This panel exists because of what the relay is: for as long as one is in
   * flight, this machine holds files that are not its own. Asking somebody to
   * run that without showing them what is on their disk, or letting them stop
   * it, is not a reasonable thing to ask.
   *
   * So it says who it is between, what it is, and **how much of it is on this
   * disk right now** — read from the filesystem rather than from the transfer
   * record, because that is the number the person whose disk it is cares about.
   *
   * It renders nothing when nothing is passing through, which is almost always.
   */

  interface Relayed {
    transfer_id: string;
    from_name: string;
    to_name: string;
    item_count: number;
    total_bytes: number;
    staged_bytes: number;
    state: string;
    name: string;
  }

  interface Props {
    session: Session;
    /** Bumped by the shell when a transfer changes, so the list follows. */
    revision?: number;
    oncancel: (id: string) => void;
  }

  let { session, revision = 0, oncancel }: Props = $props();

  let relayed = $state<Relayed[]>([]);
  let failure = $state<string | null>(null);

  async function load(): Promise<void> {
    try {
      const body = await session.request<{ relayed: Relayed[] }>('GET', '/api/relayed');
      relayed = body.relayed ?? [];
      failure = null;
    } catch (error) {
      // A phone reaching this would be refused, and a phone is not shown the
      // panel in the first place. Anything else is worth saying once.
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    }
  }

  onMount(load);

  $effect(() => {
    void revision;
    void load();
  });
</script>

{#if relayed.length > 0}
  <section aria-labelledby="relay-title" class="panel">
    <h2 id="relay-title">{t('relay.title')}</h2>
    <p class="muted">{t('relay.explanation')}</p>

    <ul class="entries">
      {#each relayed as entry (entry.transfer_id)}
        <li>
          <div class="line">
            <span class="what">{entry.name}</span>
            <span class="staged"
              >{t('relay.staged', { size: formatBytes(entry.staged_bytes) })}</span
            >
          </div>
          <div class="line muted">
            <span>{t('relay.between', { from: entry.from_name, to: entry.to_name })}</span>
            <button type="button" onclick={() => oncancel(entry.transfer_id)}>
              {t('transfer.cancel')}
            </button>
          </div>
        </li>
      {/each}
    </ul>
  </section>
{/if}

{#if failure}
  <p class="warning" role="alert">{failure}</p>
{/if}

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
    margin: 0 0 0.5rem;
  }

  .entries {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
  }

  .entries li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
  }

  .line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  .what {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .staged {
    flex-shrink: 0;
    font-size: 0.8125rem;
    color: var(--accent);
  }

  .muted {
    margin: 0.25rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  button {
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

  .warning {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }
</style>
