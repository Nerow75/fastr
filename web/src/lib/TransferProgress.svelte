<script lang="ts">
  import { t, formatBytes } from './i18n.js';
  import type { Transfer } from './transfers.js';
  import { downloadURL } from './transfers.js';
  import type { Session } from './session.js';

  /**
   * One transfer, with its progress and whatever the user can do about it.
   *
   * The progress bar is a real `<progress>` element, so assistive technology
   * reports it without help. It is not announced continuously: FR-039i keeps
   * spoken updates to the moments that carry meaning, and the event stream
   * handles those. Here the bar simply is what it is.
   */

  interface Props {
    transfer: Transfer;
    session: Session;
    /** True when this page is the one holding the files. */
    sending: boolean;
    oncancel: (id: string) => void;
  }

  let { transfer, session, sending, oncancel }: Props = $props();

  let percent = $derived(
    transfer.total_bytes === 0
      ? 100
      : Math.min(100, Math.round((transfer.transferred_bytes / transfer.total_bytes) * 100)),
  );

  let finished = $derived(['completed', 'failed', 'cancelled'].includes(transfer.state));

  /**
   * Interrupted is not failed, and the interface has to make that obvious.
   *
   * The committed offset is durable and the server keeps it for seven days, so
   * an interrupted transfer has lost nothing: it is waiting, and picking the
   * file again continues from where it stopped. Rendering it the way a failure
   * is rendered would make people start over, which is exactly the work this
   * story exists to avoid. FR-038.
   */
  let interrupted = $derived(transfer.state === 'interrupted');
  let done = $derived(
    transfer.items.reduce((sum, item) => sum + item.committed_offset, 0) ||
      transfer.transferred_bytes,
  );

  /**
   * Whether this page can still fetch the content.
   *
   * Saving is offered from the moment the transfer is queued, not once it has
   * completed. The transfer completes *because* of this download: the sending
   * page holds the file and only ever supplies it in answer to the receiver
   * fetching (see internal/transfer/pipe.go). Gating the button on completion
   * made the two wait for each other, and nothing ever arrived.
   */
  let canSave = $derived(!sending && transfer.state !== 'failed' && transfer.state !== 'cancelled');
  let saveHref = $state<string | null>(null);
  let savingItem = $state(0);

  /** Mints a scoped ticket and hands the URL to the browser's download manager,
   *  which is the only thing that can write a large file to a phone. */
  async function prepareSave(index: number): Promise<void> {
    savingItem = index;
    saveHref = await downloadURL(session, transfer.id, index);
  }

  function stateLabel(state: string): string {
    return t(`transfer.state.${state}`);
  }

  /**
   * The message for a failure cause.
   *
   * Causes and error codes are the same vocabulary with two exceptions, so they
   * share one set of translations rather than a parallel set that would drift
   * from it. Each message carries a corrective action, which is what FR-038
   * asks for and what a bare code cannot give.
   */
  const causeKeys: Record<string, string> = {
    declined: 'error.transfer_declined',
    pairing_revoked: 'error.pairing_revoked',
  };

  function causeMessage(cause: string): string {
    return t(causeKeys[cause] ?? `error.${cause}`);
  }
</script>

<article class="transfer" class:finished class:interrupted>
  <header>
    <h3>{transfer.items[0]?.name ?? transfer.id}</h3>
    <p class="state">{stateLabel(transfer.state)}</p>
  </header>

  {#if !finished}
    <progress
      max={transfer.total_bytes || 1}
      value={transfer.transferred_bytes}
      aria-label={stateLabel(transfer.state)}
    ></progress>
    <p class="numbers">
      {formatBytes(transfer.transferred_bytes)} / {formatBytes(transfer.total_bytes)}
      {#if percent > 0 && percent < 100}
        · {percent}%
      {/if}
    </p>
  {/if}

  {#if interrupted}
    <!--
      Says the thing the user needs to know and nothing else: what has already
      arrived is safe, and continuing costs only what is left. Without this the
      state word alone reads like a failure and people start over.
    -->
    <p class="waiting" role="status">
      {t('transfer.interrupted_hint', { transferred: formatBytes(done) })}
    </p>
  {/if}

  {#if transfer.state === 'failed' && transfer.failure_cause}
    <!-- A failure names its cause and what to do about it, never a bare code.
         FR-038. -->
    <p class="failure" role="alert">{causeMessage(transfer.failure_cause)}</p>
  {/if}

  <div class="actions">
    {#if !finished}
      <button type="button" onclick={() => oncancel(transfer.id)}>
        {t('transfer.cancel')}
      </button>
      {#if sending}
        <span class="hint">{t('transfer.keep_tab_open')}</span>
      {/if}
    {/if}

    {#if canSave}
      {#each transfer.items as item (item.index)}
        {#if saveHref !== null && savingItem === item.index}
          <!-- A real link, so the browser's download manager takes over. It is
               the only thing that writes a multi-gigabyte file to a phone
               without holding it in memory. -->
          <a class="save" href={saveHref} download={item.stored_name}>
            {t('transfer.save')} · {item.stored_name}
          </a>
        {:else}
          <button type="button" onclick={() => prepareSave(item.index)}>
            {t('transfer.save')} · {item.stored_name}
          </button>
        {/if}
      {/each}
    {/if}
  </div>
</article>

<style>
  .transfer {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    padding: 0.75rem 1rem;
    margin-bottom: 0.5rem;
  }

  .transfer.finished {
    opacity: 0.85;
  }

  /* A left border rather than a colour on the text: this is a state, not a
     warning, and it has to be distinguishable without relying on colour
     alone — the state word above says the same thing. */
  .transfer.interrupted {
    border-left: 3px solid var(--accent);
  }

  .waiting {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 1rem;
  }

  h3 {
    font-size: 0.9375rem;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .state {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  progress {
    width: 100%;
    height: 0.5rem;
    margin: 0.5rem 0 0.25rem;
  }

  .numbers {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--text-muted);
  }

  .failure {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }

  .actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }

  button,
  .save {
    min-height: 2.75rem;
    display: inline-flex;
    align-items: center;
    padding: 0.4rem 0.9rem;
    font-size: 0.9375rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
    text-decoration: none;
  }

  .save {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-text);
  }

  .hint {
    font-size: 0.8125rem;
    color: var(--text-muted);
  }
</style>
