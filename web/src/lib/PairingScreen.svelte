<script lang="ts">
  import { Session } from './session.js';
  import { t } from './i18n.js';

  /**
   * The pairing step.
   *
   * FR-011: this is what an unpaired device sees, and it is all it sees. It
   * cannot reach a device list or anything else first.
   *
   * The full first-connection flow, with the QR code on the desktop and the
   * approval prompt, arrives with User Story 1. This is the shell's share:
   * enter the code, get a session.
   */
  interface Props {
    desktop: boolean;
    onpaired: (session: Session) => void;
    onerror: (failure: unknown) => void;
  }

  let { desktop, onpaired, onerror }: Props = $props();

  let code = $state('');
  let deviceName = $state('');
  let busy = $state(false);
  let waiting = $state(false);
  let controller: AbortController | null = null;

  const CODE_LENGTH = 6;
  let valid = $derived(/^\d{6}$/.test(code));

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!valid || busy) return;

    busy = true;
    controller = new AbortController();
    try {
      const session = await Session.pair(
        code,
        deviceName.trim() || defaultName(),
        () => (waiting = true),
        controller.signal,
      );
      onpaired(session);
    } catch (failure) {
      onerror(failure);
      // The code is single use on the server, so a failed attempt can never be
      // retried with the same digits. Clearing the field says so without a
      // second message.
      code = '';
    } finally {
      busy = false;
      waiting = false;
      controller = null;
    }
  }

  function abandon(): void {
    controller?.abort();
  }

  function defaultName(): string {
    return desktop ? 'This computer' : 'Phone';
  }
</script>

<section aria-labelledby="pairing-title">
  {#if waiting}
    <!--
      The request is queued and a human has to answer it on the computer. Saying
      so beats a spinner: the user has somewhere to go and something to do.
    -->
    <h2 id="pairing-title">{t('pairing.waiting_title')}</h2>
    <p aria-live="polite">{t('pairing.waiting_body', { device: t('app.name') })}</p>
    <button type="button" class="secondary" onclick={abandon}>{t('pairing.cancel')}</button>
  {:else}
    <h2 id="pairing-title">{t('pairing.title')}</h2>
    <p>{t('pairing.scan_instruction')}</p>

    <form onsubmit={submit}>
      <div class="field">
        <label for="pairing-code">{t('pairing.code_label')}</label>
        <input
          id="pairing-code"
          name="code"
          bind:value={code}
          type="text"
          inputmode="numeric"
          autocomplete="one-time-code"
          maxlength={CODE_LENGTH}
          pattern="[0-9]{'{'}{CODE_LENGTH}{'}'}"
          required
        />
      </div>

      <div class="field">
        <label for="device-name">{t('settings.device_name')}</label>
        <input
          id="device-name"
          name="device_name"
          bind:value={deviceName}
          type="text"
          maxlength="64"
          placeholder={defaultName()}
        />
      </div>

      <button type="submit" disabled={!valid || busy}>
        {t('pairing.submit')}
      </button>
    </form>
  {/if}
</section>

<style>
  section {
    max-width: 24rem;
  }

  h2 {
    font-size: 1.125rem;
    margin: 0 0 0.5rem;
  }

  .field {
    margin: var(--gap) 0;
  }

  label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  input {
    width: 100%;
    /* 16px minimum, or iOS Safari zooms the whole page on focus and the user
       is left scrolled sideways on a form they were halfway through. */
    font-size: 1rem;
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text);
  }

  #pairing-code {
    font-family: var(--mono);
    font-size: 1.5rem;
    letter-spacing: 0.3em;
    text-align: center;
  }

  button {
    width: 100%;
    font-size: 1rem;
    /* 44px tall: below that, a target is uncomfortable on a phone and fails
       WCAG 2.2 target size. */
    min-height: 2.75rem;
    padding: 0.6rem 1rem;
    border: none;
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--accent-text);
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .secondary {
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
  }
</style>
