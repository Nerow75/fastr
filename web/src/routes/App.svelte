<script lang="ts">
  // Phase 2 fills this in. For now it proves the bundle builds and embeds.
  const isDesktop = ['localhost', '127.0.0.1', '::1'].includes(window.location.hostname);
</script>

<a class="skip-link" href="#main">Skip to content</a>

<main id="main">
  <h1>fastr</h1>
  <p>{isDesktop ? 'Desktop view' : 'Mobile view'}</p>
</main>

<style>
  main {
    max-width: 60rem;
    margin: 0 auto;
    padding: var(--gap);
  }
</style>
