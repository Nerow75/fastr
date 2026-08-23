<script lang="ts">
  import { onMount } from 'svelte';
  import { t, formatBytes } from './i18n.js';
  import { announce } from './a11y.js';
  import { ApiFailure, type Session } from './session.js';
  import { formatApiError } from './i18n.js';

  /**
   * What has finished here, newest first.
   *
   * The question this answers is "did last night's transfer actually work". A
   * product that cannot answer it sends the user to go and look through
   * folders, which is the work the whole thing exists to remove.
   *
   * Two things are shown that a log would not bother with. A failure names its
   * **cause with a corrective action** rather than a code (FR-038), and every
   * row says which **protection mode** it used — Principle V's honesty duty
   * makes that more than a curiosity, because in simple mode the content was
   * readable by anyone on the network and the user is entitled to know which of
   * their transfers that was true for.
   */

  interface Entry {
    transfer_id: string;
    direction: string;
    peer_name: string;
    item_count: number;
    total_bytes: number;
    outcome: string;
    failure_cause?: string;
    protection: string;
    ended_at: string;
  }

  interface Props {
    session: Session;
    /** Only the desktop may clear, so only it is offered the button. */
    canClear?: boolean;
    /** Bumped by the shell when a transfer ends, so the list refreshes. */
    revision?: number;
  }

  let { session, canClear = false, revision = 0 }: Props = $props();

  let entries = $state<Entry[]>([]);
  let failure = $state<string | null>(null);
  let confirming = $state(false);

  const causeKeys: Record<string, string> = {
    declined: 'error.transfer_declined',
    pairing_revoked: 'error.pairing_revoked',
  };

  async function load(): Promise<void> {
    try {
      const body = await session.request<{ entries: Entry[] }>('GET', '/api/history');
      entries = body.entries ?? [];
      failure = null;
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    }
  }

  onMount(load);

  // Reloaded whenever the shell says something ended. Cheap, and it keeps the
  // list from being a snapshot of whenever the page happened to open.
  $effect(() => {
    void revision;
    void load();
  });

  async function clear(): Promise<void> {
    try {
      await session.request('DELETE', '/api/history', {});
      entries = [];
      confirming = false;
      announce(t('history.cleared'));
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    }
  }

  function when(iso: string): string {
    const at = new Date(iso);
    return Number.isNaN(at.getTime()) ? '' : at.toLocaleString();
  }
</script>

<section aria-labelledby="history-title" class="panel">
  <h2 id="history-title">{t('history.title')}</h2>

  {#if entries.length === 0}
    <p class="muted">{t('history.empty')}</p>
  {:else}
    <ul class="entries">
      {#each entries as entry (entry.transfer_id)}
        <li>
          <div class="line">
            <span class="what">
              {t('history.entry', {
                count: entry.item_count,
                size: formatBytes(entry.total_bytes),
                outcome: t(`transfer.state.${entry.outcome}`),
              })}
            </span>
            <span class="when">{when(entry.ended_at)}</span>
          </div>
          <div class="line muted">
            <span>
              {entry.direction === 'incoming'
                ? t('history.from', { device: entry.peer_name })
                : t('history.to', { device: entry.peer_name })}
            </span>
            <!-- Which transfers were readable on the network, and which were
                 not. Principle V. -->
            <span class="protection">{t(`protection.${entry.protection}.short`)}</span>
          </div>
          {#if entry.failure_cause}
            <p class="cause">
              {t(causeKeys[entry.failure_cause] ?? `error.${entry.failure_cause}`)}
            </p>
          {/if}
        </li>
      {/each}
    </ul>

    {#if canClear}
      <div class="actions">
        {#if confirming}
          <!-- Confirmed once, because it cannot be undone and the button sits
               next to a list somebody may have been reading. -->
          <span class="muted">{t('history.clear_confirm')}</span>
          <button type="button" onclick={() => (confirming = false)}>{t('action.cancel')}</button>
          <button type="button" class="danger" onclick={clear}>{t('history.clear')}</button>
        {:else}
          <button type="button" onclick={() => (confirming = true)}>{t('history.clear')}</button>
        {/if}
      </div>
    {/if}
  {/if}

  {#if failure}
    <p class="warning" role="alert">{failure}</p>
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

  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .entries li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
  }

  .line {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
  }

  .what {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .when,
  .protection {
    flex-shrink: 0;
    font-size: 0.8125rem;
  }

  .muted {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0.15rem 0 0;
  }

  .cause {
    margin: 0.25rem 0 0;
    font-size: 0.875rem;
    color: var(--danger);
  }

  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }

  button {
    min-height: 2.75rem;
    padding: 0.5rem 1rem;
    font-size: 0.9375rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }

  .danger {
    border-color: var(--danger);
    color: var(--danger);
  }

  .warning {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }
</style>
