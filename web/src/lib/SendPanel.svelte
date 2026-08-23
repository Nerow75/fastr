<script lang="ts">
  import { t, formatBytes } from './i18n.js';
  import { announce } from './a11y.js';
  import type { Sender, Transfer } from './transfers.js';

  /**
   * The sending side, on the desktop.
   *
   * Drag and drop is the fast path, and a file picker is next to it because
   * drag and drop is not reachable by keyboard. FR-039g requires every
   * essential flow to work with a keyboard alone, and "drop a file here" with
   * no alternative fails that outright.
   */

  interface Device {
    id: string;
    name: string;
    kind: string;
    paired: boolean;
    /** Whether the device is holding an event stream open right now. */
    connected?: boolean;
  }

  interface Props {
    devices: Device[];
    sender: Sender;
    onsent: (transfer: Transfer) => void;
    onerror: (failure: unknown) => void;
  }

  let { devices, sender, onsent, onerror }: Props = $props();

  let files = $state<File[]>([]);
  let target = $state('');
  let dragging = $state(false);
  let busy = $state(false);

  let picker: HTMLInputElement | null = $state(null);
  let folderPicker: HTMLInputElement | null = $state(null);

  let targets = $derived(devices.filter((d) => d.paired));
  let totalBytes = $derived(files.reduce((sum, f) => sum + f.size, 0));
  let ready = $derived(files.length > 0 && target !== '' && !busy);

  // A pairing lasts a year, so this list keeps every phone ever connected. The
  // one being sent to has to have its page open, because the bytes go from this
  // tab to that one; saying so beats a transfer that sits at nothing and never
  // explains itself.
  let chosen = $derived(targets.find((d) => d.id === target));
  let unreachable = $derived(chosen !== undefined && chosen.connected === false);

  // Preselect the only reachable device, and nothing else. A device that is not
  // connected is never chosen for the user: a select shows its first option, so
  // preselecting the only paired phone made "Phone — not connected" the resting
  // state of the panel even when no phone had ever opened its page. The
  // placeholder says what to do instead.
  $effect(() => {
    if (target !== '') return;
    const reachable = targets.filter((d) => d.connected);
    if (reachable.length === 1) target = reachable[0].id;
  });

  function onDrop(event: DragEvent): void {
    event.preventDefault();
    dragging = false;

    const dropped = Array.from(event.dataTransfer?.files ?? []);
    if (dropped.length > 0) add(dropped);
  }

  function add(incoming: File[]): void {
    files = [...files, ...incoming];
    announce(t('transfer.selected', { count: files.length, size: formatBytes(totalBytes) }));
  }

  function onPicked(event: Event): void {
    const input = event.target as HTMLInputElement;
    add(Array.from(input.files ?? []));
    // Reset, or picking the same file twice in a row does nothing.
    input.value = '';
  }

  function clear(): void {
    files = [];
  }

  async function submit(): Promise<void> {
    if (!ready) return;

    busy = true;
    try {
      const transfer = await sender.send(target, files);
      files = [];
      onsent(transfer);
    } catch (failure) {
      onerror(failure);
    } finally {
      busy = false;
    }
  }
</script>

<section aria-labelledby="send-title" class="panel">
  <h2 id="send-title">{t('transfer.send')}</h2>

  {#if targets.length === 0}
    <p class="empty">{t('transfer.no_devices')}</p>
  {:else}
    <div class="field">
      <label for="send-target">{t('transfer.to')}</label>
      <select id="send-target" bind:value={target}>
        <option value="" disabled>{t('transfer.pick_device')}</option>
        {#each targets as device (device.id)}
          <option value={device.id}>
            {device.connected ? device.name : t('device.offline_option', { name: device.name })}
          </option>
        {/each}
      </select>
      {#if unreachable}
        <p class="warning" role="status">{t('device.offline_hint')}</p>
      {/if}
    </div>

    <!--
      The drop zone is a convenience, not the only way in: the two buttons below
      do the same job and are reachable by keyboard. It is not focusable itself,
      because a region that accepts a drop but does nothing on Enter is a trap
      rather than a control.
    -->
    <div
      class="dropzone"
      class:dragging
      role="presentation"
      ondragover={(e) => {
        e.preventDefault();
        dragging = true;
      }}
      ondragleave={() => (dragging = false)}
      ondrop={onDrop}
    >
      <p>{t('transfer.drop_files')}</p>

      <div class="pickers">
        <button type="button" onclick={() => picker?.click()}>
          {t('transfer.choose_files')}
        </button>
        <button type="button" onclick={() => folderPicker?.click()}>
          {t('transfer.choose_folder')}
        </button>
      </div>

      <!--
        Hidden from assistive technology, not just from view. The button beside
        each of these is the control: a screen reader user meeting both would
        find two things that do one job, and the input is the one with no name.
        `tabindex="-1"` goes with `aria-hidden`, because hiding something that
        can still be focused is its own failure.
      -->
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
        bind:this={folderPicker}
        type="file"
        webkitdirectory
        onchange={onPicked}
        class="sr-only"
        aria-hidden="true"
        tabindex="-1"
      />
    </div>

    {#if files.length > 0}
      <p class="summary" aria-live="polite">
        {t('transfer.selected', { count: files.length, size: formatBytes(totalBytes) })}
      </p>
      <ul class="files">
        {#each files as file, i (file.name + i)}
          <li>
            <span class="file-name">{file.name}</span>
            <span class="file-size">{formatBytes(file.size)}</span>
          </li>
        {/each}
      </ul>

      <div class="actions">
        <button type="button" class="secondary" onclick={clear}>{t('transfer.clear')}</button>
        <button type="button" class="primary" disabled={!ready} onclick={submit}>
          {t('transfer.send')}
        </button>
      </div>

      <!--
        The tab holds the files: nothing else in the system can reach them.
        Saying so beats letting the user close it and wonder why the transfer
        stopped.
      -->
      <p class="note">{t('transfer.keep_tab_open')}</p>
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

  .field {
    margin-bottom: 0.75rem;
  }

  label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  select {
    width: 100%;
    font-size: 1rem;
    min-height: 2.75rem;
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text);
  }

  .dropzone {
    border: 2px dashed var(--border);
    border-radius: var(--radius);
    padding: 1.25rem;
    text-align: center;
    transition: border-color 120ms ease;
  }

  .dropzone.dragging {
    border-color: var(--accent);
  }

  .dropzone p {
    margin: 0 0 0.75rem;
    color: var(--text-muted);
  }

  .pickers,
  .actions {
    display: flex;
    gap: 0.5rem;
    justify-content: center;
  }

  .actions {
    justify-content: flex-end;
    margin-top: 0.75rem;
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
    max-height: 12rem;
    overflow-y: auto;
  }

  .files li {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.25rem 0;
    border-bottom: 1px solid var(--border);
    font-size: 0.875rem;
  }

  .file-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .file-size {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .empty,
  .note {
    margin: 0.5rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .warning {
    margin: 0.35rem 0 0;
    color: var(--danger);
    font-size: 0.875rem;
  }
</style>
