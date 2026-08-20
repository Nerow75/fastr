<script lang="ts">
  import { t } from './i18n.js';

  /**
   * States which protection mode is in force, in plain language.
   *
   * Constitution v2.0.1, Principle V, carries an explicit duty of honesty: the
   * system must never claim a protection it does not provide, and must state
   * that simple-mode content is readable by anyone on the same network.
   *
   * So this is not a dismissible banner and not a tooltip. It is the one place
   * where the product tells the truth about what it does and does not protect,
   * and SC-016a checks that every screen showing a simple-mode transfer carries
   * it.
   */
  interface Props {
    mode: 'simple' | 'trusted';
    compact?: boolean;
  }

  let { mode, compact = false }: Props = $props();
</script>

<div class="notice" class:trusted={mode === 'trusted'} class:compact>
  <p class="label">
    {mode === 'trusted' ? t('protection.trusted.label') : t('protection.simple.label')}
  </p>
  <p class="detail">
    {#if compact}
      {mode === 'trusted' ? t('protection.trusted.notice') : t('protection.simple.short')}
    {:else}
      {mode === 'trusted' ? t('protection.trusted.notice') : t('protection.simple.notice')}
    {/if}
  </p>
  {#if mode === 'simple' && !compact}
    <p class="cta">{t('protection.setup_cta')}</p>
  {/if}
</div>

<style>
  .notice {
    border: 1px solid var(--warn-border);
    background: var(--warn-bg);
    color: var(--warn-text);
    border-radius: var(--radius);
    padding: 0.75rem 1rem;
    margin: 0 0 var(--gap);
  }

  .notice.trusted {
    border-color: var(--border);
    background: var(--surface);
    color: var(--text);
  }

  .notice.compact {
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
  }

  .label {
    margin: 0;
    font-weight: 600;
  }

  .detail {
    margin: 0.25rem 0 0;
  }

  .cta {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    opacity: 0.9;
  }
</style>
