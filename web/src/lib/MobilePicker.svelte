<script lang="ts">
  import { t, formatBytes, formatDuration, formatRate } from './i18n.js';
  import { announce } from './a11y.js';
  import type { Uploader, UploadProgress } from './upload.js';
  import { UploadCancelled } from './upload.js';
  import type { Transfer } from './transfers.js';

  /**
   * The sending side, on the phone.
   *
   * There is no drag and drop here: a phone has no files to drag. The system
   * picker is the only way in, and it is also the only way out to the camera
   * roll, which is where the files this product exists for actually live.
   *
   * The selection is listed with names and sizes before anything is sent, per
   * User Story 2 scenario 1. That is not decoration: a phone picker returns
   * whatever the user tapped with no confirmation of its own, and a four-gigabyte
   * mistake is worth noticing before it starts rather than after.
   */

  interface Props {
    /** The computer this phone is paired with, from GET /connect. */
    targetName: string;
    targetDeviceId: string;
    uploader: Uploader;
    onsent: (transfer: Transfer) => void;
    onerror: (failure: unknown) => void;
  }

  let { targetName, targetDeviceId, uploader, onsent, onerror }: Props = $props();

  let files = $state<File[]>([]);
  let busy = $state(false);
  let progress = $state<UploadProgress | null>(null);
  let active = $state<string | null>(null);

  let picker: HTMLInputElement | null = $state(null);
  let cameraPicker: HTMLInputElement | null = $state(null);

  let totalBytes = $derived(files.reduce((sum, f) => sum + f.size, 0));
  let ready = $derived(files.length > 0 && targetDeviceId !== '' && !busy);

  let percent = $derived(
    progress && progress.total > 0 ? Math.round((progress.transferred / progress.total) * 100) : 0,
  );

  function onPicked(event: Event): void {
    const input = event.target as HTMLInputElement;
    const picked = Array.from(input.files ?? []);
    if (picked.length > 0) {
      files = [...files, ...picked];
      announce(t('transfer.selected', { count: files.length, size: formatBytes(totalBytes) }));
    }
    // Reset, or picking the same file twice in a row does nothing.
    input.value = '';
  }

  function remove(index: number): void {
    files = files.filter((_, i) => i !== index);
  }

  function clear(): void {
    files = [];
  }

  async function submit(): Promise<void> {
    if (!ready) return;

    busy = true;
    progress = null;

    try {
      const transfer = await uploader.declare(targetDeviceId, files);
      active = transfer.id;
      onsent(transfer);

      const sending = files;
      await uploader.run(transfer, sending, (p) => (progress = p));

      files = [];
      announce(t('a11y.transfer_completed', { name: sending[0]?.name ?? '' }));
    } catch (failure) {
      // Cancelling is the user's own decision, not something to report back to
      // them as if it had gone wrong.
      if (!(failure instanceof UploadCancelled)) onerror(failure);
    } finally {
      busy = false;
      active = null;
      progress = null;
    }
  }

  async function stop(): Promise<void> {
    if (!active) return;
    try {
      await uploader.cancel(active);
    } catch (failure) {
      onerror(failure);
    }
  }
</script>

<section aria-labelledby="mobile-send-title" class="panel">
  <h2 id="mobile-send-title">{t('transfer.send')}</h2>

  {#if targetDeviceId === ''}
    <p class="empty">{t('transfer.no_computer')}</p>
  {:else}
    <p class="target">{t('transfer.to')}: <strong>{targetName}</strong></p>

    <!--
      Two entry points, because a phone has two. "Choose files" reaches the
      picker; "Take a photo or video" opens the camera directly, which is the
      shortest path for the thing that was just filmed.
    -->
    <div class="pickers">
      <button type="button" onclick={() => picker?.click()} disabled={busy}>
        {t('transfer.choose_files')}
      </button>
      <button type="button" onclick={() => cameraPicker?.click()} disabled={busy}>
        {t('transfer.use_camera')}
      </button>
    </div>

    <input bind:this={picker} type="file" multiple onchange={onPicked} class="sr-only" />
    <input
      bind:this={cameraPicker}
      type="file"
      accept="image/*,video/*"
      capture="environment"
      onchange={onPicked}
      class="sr-only"
    />

    {#if files.length > 0}
      <p class="summary" aria-live="polite">
        {t('transfer.selected', { count: files.length, size: formatBytes(totalBytes) })}
      </p>

      <ul class="files">
        {#each files as file, i (file.name + file.size + i)}
          <li>
            <span class="file-name">{file.name}</span>
            <span class="file-size">{formatBytes(file.size)}</span>
            {#if !busy}
              <button
                type="button"
                class="remove"
                onclick={() => remove(i)}
                aria-label={t('transfer.remove_file', { name: file.name })}
              >
                ×
              </button>
            {/if}
          </li>
        {/each}
      </ul>

      {#if busy && progress}
        <!--
          The committed offset, not what has been handed to the network: it is
          the number that survives the screen locking, and it is the one the
          transfer would resume from.
        -->
        <div class="progress">
          <progress value={progress.transferred} max={progress.total}></progress>
          <p class="progress-text" aria-live="polite">
            {percent}% —
            {t('transfer.progress', {
              transferred: formatBytes(progress.transferred),
              total: formatBytes(progress.total),
              speed: formatRate(progress.rate),
              remaining: progress.remaining >= 0 ? formatDuration(progress.remaining) : '—',
            })}
          </p>
        </div>
      {/if}

      <div class="actions">
        {#if busy}
          <button type="button" class="secondary" onclick={stop}>{t('transfer.cancel')}</button>
        {:else}
          <button type="button" class="secondary" onclick={clear}>{t('transfer.clear')}</button>
          <button type="button" class="primary" disabled={!ready} onclick={submit}>
            {t('transfer.send')}
          </button>
        {/if}
      </div>

      <!--
        A phone that locks its screen suspends the page, and the transfer stops
        until it comes back. It resumes rather than restarts, but saying so
        beats letting the user watch a stalled bar and guess.
      -->
      <p class="note">{t('transfer.keep_screen_on')}</p>
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

  .target {
    margin: 0 0 0.75rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .pickers,
  .actions {
    display: flex;
    gap: 0.5rem;
  }

  .pickers {
    flex-wrap: wrap;
  }

  .actions {
    justify-content: flex-end;
    margin-top: 0.75rem;
  }

  button {
    /* A phone is touched, not clicked. 2.75rem is the smallest target that is
       reliably hit with a thumb. */
    min-height: 2.75rem;
    padding: 0.5rem 1rem;
    font-size: 1rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
    flex: 1 1 auto;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .primary {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-text);
  }

  .summary {
    margin: 0.75rem 0 0.25rem;
    font-weight: 600;
  }

  .files {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 14rem;
    overflow-y: auto;
  }

  .files li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.35rem 0;
    border-bottom: 1px solid var(--border);
    font-size: 0.875rem;
  }

  .file-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1 1 auto;
  }

  .file-size {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .remove {
    flex: 0 0 auto;
    min-width: 2.75rem;
    padding: 0.25rem 0.5rem;
    line-height: 1;
  }

  .progress {
    margin-top: 0.75rem;
  }

  progress {
    width: 100%;
    height: 0.5rem;
  }

  .progress-text {
    margin: 0.25rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .empty,
  .note {
    margin: 0.5rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
  }
</style>
