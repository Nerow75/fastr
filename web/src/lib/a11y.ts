/**
 * The accessibility layer: announcements, focus, and reduced motion.
 *
 * FR-039f to FR-039j. The rule that matters most here is FR-039i: a running
 * transfer emits several events a second, and announcing each would flood a
 * screen reader user with noise until the transfer finished. Only the moments
 * that carry meaning are spoken.
 *
 * The server decides which events those are and marks them, so the rule has one
 * definition rather than one in Go and a second here that can drift.
 */

let politeRegion: HTMLElement | null = null;
let assertiveRegion: HTMLElement | null = null;

/** Creates the two live regions. Called once, from the application shell. */
export function installLiveRegions(): void {
  if (politeRegion) return;

  politeRegion = createRegion('polite');
  assertiveRegion = createRegion('assertive');
}

function createRegion(politeness: 'polite' | 'assertive'): HTMLElement {
  const region = document.createElement('div');
  region.setAttribute('aria-live', politeness);
  region.setAttribute('aria-atomic', 'true');
  region.className = 'sr-only';
  document.body.appendChild(region);
  return region;
}

let lastAnnouncement = '';

/**
 * Speaks a message to assistive technology.
 *
 * `assertive` interrupts whatever is being read and is reserved for failures:
 * a transfer that died is worth interrupting for, a transfer that completed is
 * not.
 */
export function announce(message: string, urgency: 'polite' | 'assertive' = 'polite'): void {
  const region = urgency === 'assertive' ? assertiveRegion : politeRegion;
  if (!region || message === '') return;

  // A live region only fires when its content changes. Repeating an identical
  // message needs a nudge, or the second one is silent.
  if (message === lastAnnouncement) {
    region.textContent = '';
  }
  lastAnnouncement = message;

  // A tick of delay so the region is empty when the new text lands, which is
  // what makes screen readers reliably pick it up.
  window.setTimeout(() => {
    region.textContent = message;
  }, 50);
}

/** Whether the user asked for less motion. Honoured for every transition. */
export function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/**
 * Moves focus to an element and announces its heading.
 *
 * Used when the view changes without a page load. Without this, a screen reader
 * user's focus stays on the control they activated and they are never told the
 * page became something else.
 */
export function focusView(element: HTMLElement | null, announcement?: string): void {
  if (!element) return;

  if (!element.hasAttribute('tabindex')) {
    // -1 makes it programmatically focusable without adding it to the tab order.
    element.setAttribute('tabindex', '-1');
  }
  element.focus({ preventScroll: false });

  if (announcement) announce(announcement);
}

/**
 * Traps focus inside a container, for a dialog such as the pairing approval.
 *
 * Returns a function that releases the trap and restores focus to whatever had
 * it before, which is the part people forget and the part that strands users.
 */
export function trapFocus(container: HTMLElement): () => void {
  const previous = document.activeElement as HTMLElement | null;

  const selector = [
    'a[href]',
    'button:not([disabled])',
    'input:not([disabled])',
    'select:not([disabled])',
    'textarea:not([disabled])',
    '[tabindex]:not([tabindex="-1"])',
  ].join(',');

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Tab') return;

    const focusable = Array.from(container.querySelectorAll<HTMLElement>(selector)).filter(
      (el) => el.offsetParent !== null,
    );
    if (focusable.length === 0) return;

    const first = focusable[0];
    const last = focusable[focusable.length - 1];

    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  container.addEventListener('keydown', onKeydown);

  return () => {
    container.removeEventListener('keydown', onKeydown);
    previous?.focus();
  };
}
