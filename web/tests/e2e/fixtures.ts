import { test as base, expect, type Browser, type Locator, type Page } from '@playwright/test';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * A real fastr instance, driven through two browsers at once.
 *
 * These tests run the built binary, not a mock and not the web application in
 * isolation. That is the point: everything either side of the wire has its own
 * unit and integration tests already, and what is left untested is the join —
 * whether a person with a computer and a phone can actually move a file.
 *
 * **Two contexts, because the application serves two different views.** It
 * decides which from the address in the browser's location bar: loopback is the
 * computer, anything else is a phone (see `isDesktop` in web/src/lib/session.ts).
 * So the "phone" here is a second browser context pointed at the machine's own
 * LAN address. It is not a phone, but it is on the far side of every decision
 * the server makes about who is asking — including the loopback restriction on
 * approving a device, which it is genuinely subject to.
 *
 * **State is isolated per run.** The binary reads its settings from XDG
 * directories, so those are redirected into a temporary tree and a settings file
 * is written before it starts. Without that, a test run would pair devices into
 * the developer's real store and write files into their real Downloads folder.
 */

// The package is an ES module, so __dirname does not exist here.
const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..', '..', '..');

export interface Instance {
  /** Where the computer's own browser connects. Always loopback. */
  desktopURL: string;
  /** Where a phone connects, or empty when the machine has no LAN address. */
  mobileURL: string;
  /** Where incoming files land, for asserting against the filesystem. */
  receiveDir: string;
  /** Everything the binary printed, for diagnosing a failure. */
  output(): string;
}

export const test = base.extend<{ fastr: Instance }>({
  // Per-test rather than per-worker: a pairing or a queued transfer left behind
  // by one test would silently change what the next one is testing.
  //
  // The empty pattern is how Playwright is told this fixture depends on no
  // other; it is required syntax, not an oversight.
  // eslint-disable-next-line no-empty-pattern
  fastr: async ({}, use, testInfo) => {
    const root = mkdtempSync(path.join(tmpdir(), 'fastr-e2e-'));
    const receiveDir = path.join(root, 'received');
    const stagingDir = path.join(root, 'staging');
    const configDir = path.join(root, 'config', 'fastr');

    mkdirSync(receiveDir, { recursive: true });
    mkdirSync(stagingDir, { recursive: true });
    mkdirSync(configDir, { recursive: true });

    // Written rather than left to the defaults, so the receive folder is a
    // known temporary path instead of the developer's Downloads directory.
    writeFileSync(
      path.join(configDir, 'settings.json'),
      JSON.stringify(
        {
          device_name: 'Test Computer',
          receive_folder: receiveDir,
          staging_folder: stagingDir,
          autostart: false,
          port: 0,
        },
        null,
        2,
      ),
    );

    // Verbose, because the only thing attached to a failure is what the binary
    // said, and a browser test that fails without it is a guessing game.
    const child = spawn(path.join(repoRoot, 'bin', 'fastr'), ['--port', '0', '--verbose'], {
      cwd: repoRoot,
      env: {
        ...process.env,
        XDG_CONFIG_HOME: path.join(root, 'config'),
        XDG_DATA_HOME: path.join(root, 'data'),
      },
    });

    let output = '';
    child.stdout.on('data', (chunk: Buffer) => (output += chunk.toString()));
    child.stderr.on('data', (chunk: Buffer) => (output += chunk.toString()));

    const addresses = await waitForAddresses(child, () => output);

    const loopback = addresses.find(isLoopback);
    if (!loopback) throw new Error(`no loopback address in:\n${output}`);

    const instance: Instance = {
      desktopURL: `http://${loopback}`,
      mobileURL: addresses.filter((a) => !isLoopback(a)).map((a) => `http://${a}`)[0] ?? '',
      receiveDir,
      output: () => output,
    };

    await use(instance);

    if (testInfo.status !== testInfo.expectedStatus) {
      testInfo.attach('fastr output', { body: output, contentType: 'text/plain' });
    }

    child.kill('SIGTERM');
    await new Promise((resolve) => child.once('exit', resolve));
    rmSync(root, { recursive: true, force: true });
  },
});

export { expect };

/**
 * Waits for the binary to say where it is listening.
 *
 * Its own output is the source of truth rather than a port chosen in advance:
 * asking the operating system for a free port and then handing it over leaves a
 * window in which something else takes it, and a test that fails one run in
 * fifty for that reason is worse than no test.
 */
async function waitForAddresses(
  child: ChildProcessWithoutNullStreams,
  output: () => string,
): Promise<string[]> {
  const deadline = Date.now() + 20_000;

  for (;;) {
    if (child.exitCode !== null) {
      throw new Error(`fastr exited with ${child.exitCode}:\n${output()}`);
    }

    const found = [...output().matchAll(/http:\/\/(\S+:\d+)/g)].map((m) => m[1]);
    if (found.length > 0 && output().includes('Pairing code:')) return found;

    if (Date.now() > deadline) {
      throw new Error(`fastr did not report an address in time:\n${output()}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

function isLoopback(hostPort: string): boolean {
  return hostPort.startsWith('127.') || hostPort.startsWith('[::1]');
}

/**
 * Unfolds a panel, and leaves an unfolded one alone.
 *
 * The interface shows one thing to do — the send zone — and folds everything
 * else behind its own title, so a test that drives a drawer has to open it the
 * way a person does: by pressing the title. Panels that do not fold, and ones
 * already open, are left exactly as they are.
 */
export async function expand(region: Locator): Promise<void> {
  const details = region.locator('details').first();
  if ((await details.count()) === 0) return;
  if (await details.evaluate((node) => (node as HTMLDetailsElement).open)) return;
  await region.locator('summary').first().click();
}

/**
 * Unfolds every panel on a page.
 *
 * For the audits — accessibility and translation coverage — which read the
 * whole screen and would otherwise stop seeing anything that folds. Folding is
 * a presentation decision; it must not quietly narrow what those two check.
 */
export async function expandAll(page: Page): Promise<void> {
  const summaries = page.locator('details:not([open]) > summary');
  for (let i = (await summaries.count()) - 1; i >= 0; i--) {
    await summaries.nth(i).click();
  }
}

/**
 * Opens the computer's own page and reads the pairing code it displays.
 *
 * Read from the interface rather than from the binary's output on purpose: that
 * the code is reachable without a terminal is the thing FR-002 requires, and a
 * test that read standard output would keep passing if the panel disappeared.
 */
export async function readPairingCode(page: Page): Promise<string> {
  // Exact names throughout. Four strings in this interface begin with
  // "Connect" — the invitation panel, the pairing screen's heading, its submit
  // button, and the reveal button — and a loose regex matches the wrong one.
  const panel = page.getByRole('region', { name: 'Connect a phone', exact: true });
  await expect(panel).toBeVisible();

  const code = panel.locator('.code');
  const reveal = panel.getByRole('button', { name: 'Connect another device', exact: true });

  // Two legitimate states: showing a code while no phone is connected, or
  // offering to fetch one once there is. Which one is in front can change under
  // us — a phone finishing its pairing collapses the panel — so the whole check
  // retries rather than deciding once and hoping. Once a device is connected
  // the panel is also folded away, so it is unfolded first: the reveal button
  // is inside it.
  await expect(async () => {
    await expand(panel);
    if (await reveal.isVisible()) await reveal.click();
    await expect(code).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 20_000 });

  const digits = (await code.textContent())?.trim() ?? '';
  expect(digits).toMatch(/^\d{6}$/);
  return digits;
}

/**
 * Runs a full pairing: read the code on the computer, type it on the phone, and
 * approve it on the computer.
 *
 * Only a phone ever goes through this. The computer's own page is granted a
 * session on load, because it is the machine rather than a device asking to be
 * let in; it used to have to pair with itself, and nobody ever found that step.
 */
export interface PairingLabels {
  /** The pairing form's accessible name, on the client. */
  form: string;
  /** The submit button on the client. */
  submit: string;
}

/** What the pairing screen is called in each language it ships in. */
export const LABELS: Record<'en' | 'fr', PairingLabels> = {
  en: { form: 'Connect this phone', submit: 'Connect' },
  fr: { form: 'Connecter ce téléphone', submit: 'Connecter' },
};

export async function pair(
  desktop: Page,
  client: Page,
  deviceName: string,
  labels: PairingLabels = LABELS.en,
): Promise<void> {
  const code = await readPairingCode(desktop);

  // Named in the client's own language: a fixture that only knows English can
  // only pair an English device, and the interface ships in two.
  const form = client.getByRole('region', { name: labels.form, exact: true });

  await form.locator('#pairing-code').fill(code);
  await form.locator('#device-name').fill(deviceName);
  await form.getByRole('button', { name: labels.submit, exact: true }).click();

  // The request is queued, not granted: FR-010 wants a human on the receiving
  // device to say yes, and this is that human.
  const request = desktop.getByRole('region', { name: 'A device wants to connect', exact: true });
  await expect(request).toContainText(deviceName, { timeout: 20_000 });

  await request.getByRole('button', { name: 'Allow', exact: true }).click();

  // Paired means the page holds a session, and the status line in the header is
  // what that renders as: it is behind the same `{#if session}` as everything
  // else the page gains by pairing.
  //
  // Not "the pairing form is gone", which is what this used to check and what
  // it looks like it should check. The pairing screen keeps its region and
  // swaps the heading to "Waiting for approval" while it polls, so a check for
  // the absence of "Connect this phone" passes the moment the code is
  // submitted — before anyone has approved anything, and before a credential
  // exists. It went unnoticed because the wait that followed it happened to
  // cover the gap.
  await expect(client.locator('header .status')).toBeVisible({ timeout: 30_000 });

  // And deliberately *not* waiting for the event stream to be live.
  //
  // That wait used to be here, because a transfer declared before the receiver
  // was listening was one it never heard about: nothing reconciled state on
  // connect, so the event was lost and the phone sat there showing nothing.
  // T087b closed that, and removing the wait is how these tests prove it — a
  // send that begins before the stream is up now reaches the phone anyway.
}

/**
 * Opens the computer's own page, in a context of its own.
 *
 * A context rather than `browser.newPage()`, which creates an implicit one:
 * axe-core refuses to run against a page from an implicit context, and having
 * the desktop and the phone built the same way keeps that from being a
 * surprise in one file only.
 */
export async function openDesktop(browser: Browser, instance: Instance): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(instance.desktopURL);
  return page;
}

/** Opens a page as a device on the far side of the network. */
export async function openPhone(browser: Browser, instance: Instance): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(instance.mobileURL);
  return page;
}
