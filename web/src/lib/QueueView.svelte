<script lang="ts">
  import Panel from './Panel.svelte';
  import { t, formatBytes } from './i18n.js';
  import { announce } from './a11y.js';
  import { ApiFailure, type Session } from './session.js';
  import { formatApiError } from './i18n.js';
  import type { Transfer } from './transfers.js';

  /**
   * What is waiting, in which order, and what the user can do about it.
   *
   * One transfer runs at a time (FR-035a), which is a deliberate choice rather
   * than a limitation: two transfers sharing a link finish later than the same
   * two in sequence, and the queue is what makes that visible instead of
   * mysterious. So the panel says the rule out loud rather than leaving people
   * to infer it from a stalled second file.
   *
   * Reordering sends the **whole** order rather than a move. Two pages nudging
   * entries at the same time with relative moves would interleave into an order
   * neither asked for; a full ordering is either the queue as it stands, or it
   * is refused.
   */

  interface Props {
    session: Session;
    /** The waiting entries, in order, as the server reports them. */
    entries: Transfer[];
    /** The one that is running, if any. */
    active?: Transfer | null;
    /** Asks the shell to read the queue back after a change. */
    onchanged: () => void;
  }

  let { session, entries, active = null, onchanged }: Props = $props();

  let busy = $state(false);
  let failure = $state<string | null>(null);

  // A queue with something in it is live state, not a drawer: it opens itself.
  // An empty one folds away, which is what it is nearly always.
  let live = $derived(active !== null || entries.length > 0);

  let hint = $derived(
    [
      active ? t('transfer.state.running') : '',
      entries.length > 0 ? t('panel.hint_waiting', { count: entries.length }) : '',
    ]
      .filter(Boolean)
      .join(' · ') || t('queue.empty'),
  );

  function nameOf(transfer: Transfer): string {
    const first = transfer.items[0]?.name ?? transfer.id;
    if (transfer.items.length <= 1) return first;
    return t('queue.entry_more', { name: first, count: transfer.items.length - 1 });
  }

  async function act(run: () => Promise<unknown>, said: string): Promise<void> {
    busy = true;
    failure = null;
    try {
      await run();
      announce(said);
      onchanged();
    } catch (error) {
      failure = error instanceof ApiFailure ? formatApiError(error.body) : t('error.internal');
    } finally {
      busy = false;
    }
  }

  /** Moves one entry by a step, and sends the resulting order whole. */
  function move(index: number, by: number): void {
    const to = index + by;
    if (to < 0 || to >= entries.length) return;

    const order = entries.map((tr) => tr.id);
    [order[index], order[to]] = [order[to], order[index]];

    void act(
      () => session.request('POST', '/api/queue/reorder', { order }),
      t('queue.moved', { name: nameOf(entries[index]), position: to + 1 }),
    );
  }

  function remove(transfer: Transfer): void {
    void act(
      () => session.request('DELETE', `/api/queue/${encodeURIComponent(transfer.id)}`, {}),
      t('queue.removed', { name: nameOf(transfer) }),
    );
  }

  function clear(): void {
    void act(() => session.request('DELETE', '/api/queue', {}), t('queue.cleared'));
  }
</script>

<Panel
  id="queue"
  title={t('queue.title')}
  {hint}
  tone={live ? 'plain' : 'quiet'}
  collapsible
  open={live}
>
  {#if active}
    <p class="active">
      <span class="running">{t('transfer.state.running')}</span>
      {nameOf(active)}
    </p>
  {/if}

  {#if entries.length === 0}
    <p class="muted">{t('queue.empty')}</p>
  {:else}
    <ol class="entries">
      {#each entries as transfer, index (transfer.id)}
        <li>
          <span class="position" aria-hidden="true">{index + 1}</span>
          <span class="name">{nameOf(transfer)}</span>
          <span class="size">{formatBytes(transfer.total_bytes)}</span>

          <!--
            Buttons rather than drag and drop. FR-039g wants every essential
            flow reachable from a keyboard, and a list that can only be
            reordered by dragging is not.
          -->
          <button
            type="button"
            disabled={busy || index === 0}
            onclick={() => move(index, -1)}
            aria-label={t('queue.move_up_named', { name: nameOf(transfer) })}
          >
            ↑
          </button>
          <button
            type="button"
            disabled={busy || index === entries.length - 1}
            onclick={() => move(index, 1)}
            aria-label={t('queue.move_down_named', { name: nameOf(transfer) })}
          >
            ↓
          </button>
          <button
            type="button"
            disabled={busy}
            onclick={() => remove(transfer)}
            aria-label={t('queue.remove_named', { name: nameOf(transfer) })}
          >
            ×
          </button>
        </li>
      {/each}
    </ol>

    <div class="actions">
      <button type="button" disabled={busy} onclick={clear}>{t('queue.clear')}</button>
    </div>
  {/if}

  <!-- Said out loud, because a second file that has not started looks broken
       until you know it is a queue. -->
  <p class="muted">{t('queue.one_at_a_time')}</p>

  {#if failure}
    <p class="warning" role="alert">{failure}</p>
  {/if}
</Panel>

<style>
  .active {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0 0 0.5rem;
    font-size: 0.9375rem;
  }

  .running {
    flex-shrink: 0;
    font-size: 0.8125rem;
    color: var(--accent);
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    padding: 0.1rem 0.45rem;
  }

  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
    counter-reset: queue;
  }

  .entries li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0;
    border-bottom: 1px solid var(--border);
  }

  .position {
    flex-shrink: 0;
    width: 1.5rem;
    text-align: right;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .name {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .size {
    flex-shrink: 0;
    font-size: 0.8125rem;
    color: var(--text-muted);
  }

  button {
    min-width: 2.75rem;
    min-height: 2.75rem;
    padding: 0.3rem 0.7rem;
    font-size: 1rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    margin-top: 0.75rem;
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
