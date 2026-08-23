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

  /** Another device this phone can send to, through the computer. */
  interface Peer {
    id: string;
    name: string;
    paired: boolean;
    connected?: boolean;
  }

  interface Props {
    /** The computer this phone is paired with, from GET /connect. */
    targetName: string;
    targetDeviceId: string;
    /**
     * Other phones paired with the same computer, which it will relay to.
     * FR-053: a friend's phone is a destination like any other, and the
     * computer passes the data through without keeping it.
     */
    peers?: Peer[];
    uploader: Uploader;
    /**
     * Transfers from this phone that have not finished, as the computer knows
     * them. After a reload this is the only trace of what was in flight: the
     * page reconciles them from the queue, and picking the same file again
     * continues one of them rather than starting a second copy.
     */
    unfinished?: Transfer[];
    onsent: (transfer: Transfer) => void;
    onerror: (failure: unknown) => void;
  }

  let {
    targetName,
    targetDeviceId,
    peers = [],
    uploader,
    unfinished = [],
    onsent,
    onerror,
  }: Props = $props();

  /**
   * Where the files are going. The computer by default, because that is what
   * this is for most of the time; another phone when the user picks one.
   */
  let chosen = $state('');
  let destination = $derived(chosen === '' ? targetDeviceId : chosen);

  // Only phones that have their page open. A relayed transfer to a closed
  // phone would sit in the computer's staging area waiting for a collection
  // that is not coming, which is the failure T051e was about in the other
  // direction.
  let reachablePeers = $derived(
    peers.filter((p) => p.paired && p.connected && p.id !== targetDeviceId),
  );

  let files = $state<File[]>([]);
  let busy = $state(false);
  let progress = $state<UploadProgress | null>(null);
  let active = $state<string | null>(null);
  // Set while the upload is between attempts. A phone that lost Wi-Fi is the
  // ordinary case, and a bar that simply stops moving is indistinguishable from
  // one that has broken. FR-038: say what is happening and what happens next.
  let retryAt = $state<number | null>(null);
  let retrySeconds = $state(0);

  let picker: HTMLInputElement | null = $state(null);
  let cameraPicker: HTMLInputElement | null = $state(null);

  let totalBytes = $derived(files.reduce((sum, f) => sum + f.size, 0));
  let ready = $derived(files.length > 0 && destination !== '' && !busy);

  /**
   * The unfinished transfer these files belong to, if there is one.
   *
   * A phone that was backgrounded mid-upload comes back with no memory of what
   * it was doing: iOS discards the page, and the File objects go with it. The
   * only way back to the transfer is for the user to pick the same file again,
   * so that is what this looks for.
   *
   * Matched on names and sizes in order. That is a heuristic, and it is allowed
   * to be, because it is not what makes the file correct: the digest is
   * computed over what is actually read and verified against what was actually
   * written, so picking the wrong file of the same name and size fails
   * verification rather than producing a wrong file (FR-032).
   */
  let resumable = $derived(
    files.length === 0
      ? undefined
      : unfinished.find(
          (tr) =>
            tr.items.length === files.length &&
            tr.items.every((item, i) => item.name === files[i].name && item.size === files[i].size),
        ),
  );

  /** How much of the resumable transfer the computer already holds. */
  let alreadyThere = $derived(
    resumable ? resumable.items.reduce((sum, item) => sum + item.committed_offset, 0) : 0,
  );

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
      // Continued rather than declared again when the computer still holds a
      // transfer for these files: run() asks for the committed offset before
      // sending anything, so this resumes by construction.
      const transfer = resumable ?? (await uploader.declare(destination, files));
      active = transfer.id;
      onsent(transfer);

      const sending = files;
      await uploader.run(
        transfer,
        sending,
        (p) => {
          // Any progress at all means the connection came back.
          retryAt = null;
          progress = p;
        },
        (waiting) => {
          retryAt = Date.now() + waiting.delay;
          announce(t('transfer.reconnecting_in', { seconds: Math.ceil(waiting.delay / 1000) }));
        },
      );

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
      retryAt = null;
    }
  }

  // A countdown rather than a spinner: "reconnecting" with no number is the
  // same as a frozen bar, and the number is the thing that says the page has
  // not given up.
  $effect(() => {
    if (retryAt === null) {
      retrySeconds = 0;
      return;
    }
    const tick = (): void => {
      retrySeconds = Math.max(0, Math.ceil((retryAt! - Date.now()) / 1000));
    };
    tick();
    const timer = setInterval(tick, 500);
    return () => clearInterval(timer);
  });

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
    {#if reachablePeers.length === 0}
      <p class="target">{t('transfer.to')}: <strong>{targetName}</strong></p>
    {:else}
      <!--
        A choice only when there is one. A select with a single option is a
        worse way of saying the same thing as a line of text.
      -->
      <div class="field">
        <label for="mobile-target">{t('transfer.to')}</label>
        <select id="mobile-target" bind:value={chosen} disabled={busy}>
          <option value="">{targetName}</option>
          {#each reachablePeers as peer (peer.id)}
            <option value={peer.id}>{peer.name}</option>
          {/each}
        </select>
      </div>
      {#if chosen !== ''}
        <!-- Said plainly: the computer holds the file for a moment on the way
             through, and the user is entitled to know that before sending. -->
        <p class="note">{t('transfer.via_computer', { name: targetName })}</p>
      {/if}
    {/if}

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

    <input
      bind:this={picker}
      type="file"
      multiple
      onchange={onPicked}
      class="sr-only"
      aria-hidden="true"
      tabindex="-1"
    />
    <input
      bind:this={cameraPicker}
      type="file"
      accept="image/*,video/*"
      capture="environment"
      onchange={onPicked}
      class="sr-only"
      aria-hidden="true"
      tabindex="-1"
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

      {#if resumable && !busy}
        <!--
          Said before the button is pressed, because "Resume" alone does not
          tell the user that the wait will be shorter than the first attempt.
        -->
        <p class="resuming" role="status">
          {t('transfer.resume_hint', { transferred: formatBytes(alreadyThere) })}
        </p>
      {/if}

      {#if retryAt !== null}
        <!--
          Interrupted, not failed. The computer keeps the committed offset for
          seven days, so nothing that arrived is lost and the next attempt
          continues from it.
        -->
        <p class="reconnecting" role="status">
          {t('transfer.reconnecting_in', { seconds: retrySeconds })}
        </p>
      {/if}

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
            {resumable ? t('transfer.resume') : t('transfer.send')}
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

  .field {
    margin-bottom: 0.5rem;
  }

  label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  select {
    width: 100%;
    min-height: 2.75rem;
    font-size: 1rem;
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text);
  }

  .resuming {
    margin: 0.75rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  /* Not styled as an error: nothing has gone wrong yet, and the committed
     offset means nothing has been lost either. */
  .reconnecting {
    margin: 0.75rem 0 0;
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    font-size: 0.875rem;
  }
</style>
