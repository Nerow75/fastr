import { mount } from 'svelte';
import App from './routes/App.svelte';
import { negotiate, setLanguage } from './lib/i18n.js';
import './app.css';

/**
 * The language is chosen *before* the first render, and that ordering is the
 * whole point of it being here rather than in the shell's onMount.
 *
 * `t()` reads a plain module variable, so a component that rendered before the
 * language was set keeps the English it was built with: nothing re-runs when
 * the variable changes later. Setting it in onMount therefore produced a French
 * device reading an English interface, with `<html lang="fr">` on it — the
 * catalogue was complete, negotiated correctly, and decorative.
 *
 * A runtime override (FR-039b) will need more than this: it changes the
 * language after the interface exists, which needs the current language to be
 * reactive state rather than a module variable. There is no override control
 * yet; when one is built, that is the work it requires.
 */
setLanguage(negotiate(localStorage.getItem('fastr.language')));

const target = document.getElementById('app');
if (!target) {
  throw new Error('missing #app mount point');
}

export default mount(App, { target });
