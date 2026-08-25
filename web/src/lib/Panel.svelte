<script lang="ts">
  import type { Snippet } from 'svelte';

  /**
   * The one place that decides what a section of this interface looks like.
   *
   * Every panel used to carry its own copy of the same card: one border, one
   * radius, one heading at one size. Eight identical copies meant the page had
   * no hierarchy at all — the thing the user came to do and the record of what
   * happened last week were drawn with exactly the same weight, and the send
   * zone sat in the middle of the stack where it was hardest to find.
   *
   * `tone` is that hierarchy, stated once. `collapsible` is the other half:
   * anything that is not the current task folds away behind its own title, so
   * the screen shows one thing to do and a list of places to look.
   *
   * The heading stays a real `<h2>` and the section stays a landmark in both
   * shapes, because folding something away must not remove it from the outline
   * a screen reader navigates by. The name of the landmark is the title alone —
   * `hint` sits outside the labelling span, so "Queue" stays "Queue" however
   * many files are waiting.
   */
  interface Props {
    /** Stable identifier; the heading's labelling span becomes `${id}-title`. */
    id: string;
    title: string;
    /** One line of state, next to the title. Read by the same heading. */
    hint?: string;
    /**
     * hero: the thing this screen exists for, at most one per screen.
     * plain: live state worth reading without being asked for.
     * quiet: a drawer, closed until wanted.
     * urgent: something is waiting for an answer.
     */
    tone?: 'hero' | 'plain' | 'quiet' | 'urgent';
    collapsible?: boolean;
    /** Only meaningful when collapsible. Steers the initial state. */
    open?: boolean;
    children: Snippet;
  }

  let {
    id,
    title,
    hint,
    tone = 'plain',
    collapsible = false,
    open = true,
    children,
  }: Props = $props();
</script>

<section class="panel {tone}" class:collapsible aria-labelledby="{id}-title">
  {#if collapsible}
    <details {open}>
      <summary>
        <h2>
          <span class="marker" aria-hidden="true"></span>
          <span class="text" id="{id}-title">{title}</span>
          {#if hint}<span class="hint">{hint}</span>{/if}
        </h2>
      </summary>
      <div class="body">
        {@render children()}
      </div>
    </details>
  {:else}
    <h2>
      <span class="text" id="{id}-title">{title}</span>
      {#if hint}<span class="hint">{hint}</span>{/if}
    </h2>
    <div class="body">
      {@render children()}
    </div>
  {/if}
</section>

<style>
  .panel {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    margin: 0 0 var(--gap);
  }

  /* Not collapsible: the heading is part of the card, so the padding is the
     card's. Collapsible: the summary is the padding, so the card has none. */
  .panel:not(.collapsible) {
    padding: var(--pad);
  }

  /*
   * The hero. One per screen, and it has to win by more than a hairline: a
   * slightly thicker border reads as a mistake, a clear step up in surface,
   * space, and type reads as a decision.
   */
  .hero {
    background: var(--surface-raised);
    border-color: var(--border-strong);
    box-shadow: var(--shadow-raised);
    --pad: var(--pad-lg);
  }

  /* A drawer. It recedes: no fill of its own, and a title that does not
     compete with the hero's. */
  .quiet {
    background: transparent;
    border-color: var(--border-soft);
  }

  .urgent {
    border-color: var(--accent);
    background: var(--accent-soft);
  }

  h2 {
    display: flex;
    align-items: baseline;
    gap: 0.625rem;
    margin: 0;
    font-size: var(--size-title);
    font-weight: 600;
    letter-spacing: -0.01em;
    min-width: 0;
  }

  .hero h2 {
    font-size: var(--size-hero);
  }

  .quiet h2 {
    font-size: var(--size-title);
    font-weight: 500;
    color: var(--text-muted);
  }

  .panel:not(.collapsible) > h2 {
    margin-bottom: 0.75rem;
  }

  /* The state of the section, in the words the section would use. Wraps rather
     than truncates: a French hint is routinely half again as long as its
     English original (FR-039e). */
  .hint {
    font-size: var(--size-small);
    font-weight: 400;
    color: var(--text-subtle);
    min-width: 0;
  }

  summary {
    display: block;
    padding: 0.75rem var(--pad);
    cursor: pointer;
    border-radius: var(--radius);
  }

  /* The default triangle is drawn differently by every engine and cannot be
     positioned; this one is ours and follows the text colour. */
  summary::-webkit-details-marker {
    display: none;
  }

  summary::marker {
    content: '';
  }

  summary:hover h2 {
    color: var(--text);
  }

  .marker {
    flex: 0 0 auto;
    align-self: center;
    width: 0.5rem;
    height: 0.5rem;
    border-right: 2px solid currentColor;
    border-bottom: 2px solid currentColor;
    transform: rotate(-45deg);
    transition: transform 120ms ease;
    opacity: 0.7;
  }

  details[open] .marker {
    transform: rotate(45deg);
  }

  .collapsible .body {
    padding: 0 var(--pad) var(--pad);
  }

  /* Focus belongs on the summary, which is the control; the ring follows the
     card's corners rather than the text's. */
  summary:focus-visible {
    outline: 3px solid var(--accent);
    outline-offset: -3px;
  }
</style>
