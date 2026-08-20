import { mount } from 'svelte';
import App from './routes/App.svelte';
import './app.css';

const target = document.getElementById('app');
if (!target) {
  throw new Error('missing #app mount point');
}

export default mount(App, { target });
