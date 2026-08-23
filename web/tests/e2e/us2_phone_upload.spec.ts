import { readFileSync } from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect, pair, openPhone } from './fixtures.js';

/**
 * User Story 2, through two real browsers: a phone picks a file and sends it to
 * the computer, which writes it into its receive folder.
 *
 * The Go integration tests already prove the endpoints. What only a browser can
 * prove is the part above them — that the file picker, the chunked upload, and
 * the BLAKE2b computed in JavaScript agree with the server well enough for a
 * file to arrive intact — and that a person can get there by clicking.
 */

/** Picks one file on a phone page and sends it. */
async function send(page: Page, name: string, contents: string): Promise<void> {
  await page.locator('input[type=file]:not([capture])').setInputFiles({
    name,
    mimeType: 'text/plain',
    buffer: Buffer.from(contents),
  });
  await page
    .getByRole('region', { name: 'Send', exact: true })
    .getByRole('button', { name: 'Send', exact: true })
    .click();
}

test.describe('sending from a phone', () => {
  test('a file picked on the phone lands in the receive folder', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    // Large enough to cross several upload chunks rather than fitting in one,
    // so the offset arithmetic is exercised instead of skipped.
    const payload = Buffer.alloc(9 << 20);
    for (let i = 0; i < payload.length; i++) payload[i] = (i * 31) % 251;

    await phone.locator('input[type=file]:not([capture])').setInputFiles({
      name: 'holiday.mp4',
      mimeType: 'video/mp4',
      buffer: payload,
    });

    // Scenario 1: the selection is listed with names and sizes before anything
    // is sent, because a phone picker confirms nothing of its own.
    const panel = phone.getByRole('region', { name: 'Send', exact: true });
    await expect(panel).toContainText('holiday.mp4');

    await panel.getByRole('button', { name: 'Send', exact: true }).click();

    // Scenario 3: it is present, intact, and named as it was on the phone.
    const landed = path.join(fastr.receiveDir, 'holiday.mp4');
    await expect(async () => {
      const got = readFileSync(landed);
      expect(got.length).toBe(payload.length);
      expect(got.equals(payload)).toBe(true);
    }).toPass({ timeout: 60_000 });
  });

  test('a second tab does not fight the first for the counter', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const context = await browser.newContext();
    const phone = await context.newPage();
    await phone.goto(fastr.mobileURL);
    await pair(desktop, phone, 'Test Phone');

    // Two tabs of the same origin share one credential from site data, and each
    // used to keep its own counter starting at one.
    //
    // Retrying hides that when both are equally busy: each retry advances by
    // one and they take turns getting through. What does not heal is one tab
    // running far ahead — the idle one is left permanently behind the server's
    // high-water mark, and its every request is refused as a replay while the
    // other works normally. That is what was seen in the wild, so the busy tab
    // below sends a whole file first, and only then is the quiet one asked to
    // do anything.
    const second = await context.newPage();
    await second.goto(fastr.mobileURL);
    await expect(second.locator('header .status')).toContainText('is listening on this network', {
      timeout: 30_000,
    });

    await send(phone, 'from-the-busy-tab.txt', 'the first tab does the work');
    await expect(async () => {
      expect(readFileSync(path.join(fastr.receiveDir, 'from-the-busy-tab.txt'), 'utf8')).toBe(
        'the first tab does the work',
      );
    }).toPass({ timeout: 45_000 });

    // Now the tab that has been sitting still. Its counter has not moved while
    // the other raced ahead.
    await send(second, 'from-the-quiet-tab.txt', 'and the second tab still works');
    await expect(async () => {
      expect(readFileSync(path.join(fastr.receiveDir, 'from-the-quiet-tab.txt'), 'utf8')).toBe(
        'and the second tab still works',
      );
    }).toPass({ timeout: 45_000 });
  });

  test('an existing file is preserved rather than overwritten', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const panel = phone.getByRole('region', { name: 'Send', exact: true });

    // Scenario 4: send the same name twice. The first must survive.
    for (const contents of ['the first one', 'the second one']) {
      await phone.locator('input[type=file]:not([capture])').setInputFiles({
        name: 'note.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from(contents),
      });

      // The panel offers Cancel rather than Send while an upload is running,
      // and the previous iteration's assertion is satisfied the moment the file
      // is on disk — a beat before the upload actually finishes. Waiting for
      // Send is waiting for the panel to be ready, which is what a person
      // sending a second file would do.
      const button = panel.getByRole('button', { name: 'Send', exact: true });
      await expect(button).toBeEnabled({ timeout: 30_000 });
      await button.click();

      await expect(async () => {
        expect(readFileSync(path.join(fastr.receiveDir, 'note.txt'), 'utf8')).toBe('the first one');
      }).toPass({ timeout: 30_000 });
    }

    // Two files, and the original still holds what it held.
    await expect(async () => {
      const { readdirSync } = await import('node:fs');
      const names = readdirSync(fastr.receiveDir);
      expect(names.length).toBe(2);
      expect(names).toContain('note.txt');
    }).toPass({ timeout: 30_000 });
  });
});
