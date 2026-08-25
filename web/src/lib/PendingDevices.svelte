<script lang="ts">
  import Panel from './Panel.svelte';
  import { onMount, onDestroy } from 'svelte';
  import { t, formatDateTime } from './i18n.js';
  import { announce } from './a11y.js';

  /**
   * The host's approval prompt. FR-010, and the moment the human decides.
   *
   * This is deliberately not a passive list. A device waiting to be let in is
   * the one thing on this screen that needs an answer, so it announces itself,
   * and Allow is never the default focus: refusing must be at least as easy as
   * accepting, and a stray Enter must not admit a stranger.
   *
   * Reached only from loopback, which is what makes it the *host* deciding
   * rather than whoever is on the network.
   */

  interface PendingRequest {
    id: string;
    device_name: string;
    platform: string;
    state: string;
    created_at: string;
    expires_at: string;
  }

  interface Props {
    /** Bumped by the shell when a pairing_pending event arrives. */
    revision?: number;
  }

  let { revision = 0 }: Props = $props();

  let requests = $state<PendingRequest[]>([]);
  let busy = $state<string | null>(null);
  let failure = $state<string | null>(null);

  let poll: number | null = null;

  onMount(() => {
    void refresh();
    // The event stream is the primary signal; this is the safety net for a
    // dropped stream, so it can be slow.
    poll = window.setInterval(() => void refresh(), 5000);
  });

  onDestroy(() => {
    if (poll !== null) window.clearInterval(poll);
  });

  // Refetch whenever the shell says a pending event arrived.
  $effect(() => {
    void revision;
    void refresh();
  });

  let previousCount = 0;

  async function refresh(): Promise<void> {
    try {
      const response = await fetch('/api/pair/pending');
      if (!response.ok) return; // not the host, or not listening
      const body = (await response.json()) as { pending: PendingRequest[] };

      const waiting = (body.pending ?? []).filter((r) => r.state === 'awaiting_approval');
      if (waiting.length > previousCount) {
        // A device asking to be let in is worth interrupting for.
        announce(t('pairing.approve_title'), 'assertive');
      }
      previousCount = waiting.length;
      requests = waiting;
    } catch {
      // A failed poll is not worth surfacing: the next one is a second away.
    }
  }

  async function answer(id: string, decision: 'approve' | 'reject'): Promise<void> {
    busy = id;
    failure = null;
    try {
      const response = await fetch(`/api/pair/pending/${encodeURIComponent(id)}/${decision}`, {
        method: 'POST',
      });
      if (!response.ok) {
        failure = t('error.invalid_request');
        return;
      }
      requests = requests.filter((r) => r.id !== id);
      previousCount = requests.length;
    } finally {
      busy = null;
    }
  }
</script>

{#if requests.length > 0}
  <!-- Somebody is waiting on an answer, so this outranks even the send zone and
       is never folded. It renders nothing when nothing is waiting. -->
  <Panel id="pending" title={t('pairing.approve_title')} tone="urgent">
    {#if failure}
      <p class="error" role="alert">{failure}</p>
    {/if}

    <ul>
      {#each requests as request (request.id)}
        <li>
          <div class="who">
            <p class="name">{request.device_name}</p>
            <p class="meta">
              {request.platform} · {formatDateTime(request.created_at)}
            </p>
          </div>

          <p class="question">{t('pairing.approve_question', { device: request.device_name })}</p>

          <div class="actions">
            <!--
              Refuse comes first in the DOM, so it is the first thing reached by
              keyboard and the first thing a screen reader offers. Allowing an
              unknown device should take one more keystroke than refusing it.
            -->
            <button
              type="button"
              class="reject"
              disabled={busy === request.id}
              onclick={() => answer(request.id, 'reject')}
            >
              {t('pairing.reject')}
            </button>
            <button
              type="button"
              class="approve"
              disabled={busy === request.id}
              onclick={() => answer(request.id, 'approve')}
            >
              {t('pairing.approve')}
            </button>
          </div>
        </li>
      {/each}
    </ul>
  </Panel>
{/if}

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.75rem;
  }

  li {
    border-top: 1px solid var(--border);
    padding-top: 0.75rem;
  }

  li:first-child {
    border-top: none;
    padding-top: 0;
  }

  .name {
    margin: 0;
    font-weight: 600;
  }

  .meta {
    margin: 0.125rem 0 0;
    font-size: 0.8125rem;
    color: var(--text-muted);
  }

  .question {
    margin: 0.5rem 0 0.75rem;
  }

  .actions {
    display: flex;
    gap: 0.5rem;
  }

  button {
    min-height: 2.75rem;
    padding: 0.5rem 1rem;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    font-size: 1rem;
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .approve {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-text);
  }

  .error {
    color: var(--danger);
    margin: 0 0 0.5rem;
  }
</style>
