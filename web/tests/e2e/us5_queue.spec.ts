import { test, expect, pair, openPhone } from './fixtures.js';
import type { Page } from '@playwright/test';

/**
 * User Story 5, through the browser: the queue is visible and under control.
 *
 * One transfer runs at a time (FR-035a). That is a deliberate choice rather
 * than a limitation — two transfers sharing a link both finish later than the
 * same two in sequence — but it is only defensible if the user can see the
 * queue and change it. A second file that has not started looks broken until
 * you know it is waiting.
 *
 * What only a browser can prove here is the part above the endpoints: that the
 * order shown is the order the server holds, that reordering survives the round
 * trip, and that removing an entry reaches the device that was sending it.
 */

/** Queues one file from the desktop to the phone, without waiting for it. */
async function queueFile(desktop: Page, name: string): Promise<void> {
  const send = desktop.getByRole('region', { name: 'Send', exact: true });

  await send.locator('input[type=file][multiple]').setInputFiles({
    name,
    mimeType: 'text/plain',
    buffer: Buffer.from(`the contents of ${name}`),
  });
  await send.getByRole('button', { name: 'Send', exact: true }).click();

  // The panel clears its selection once the transfer is declared, which is the
  // signal that the next one may be chosen.
  await expect(send.locator('.files li')).toHaveCount(0, { timeout: 20_000 });
}

test.describe('the queue', () => {
  test('shows what is waiting, in an order the user can change', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const select = desktop
      .getByRole('region', { name: 'Send', exact: true })
      .locator('select#send-target');
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    for (const name of ['first.txt', 'second.txt', 'third.txt']) {
      await queueFile(desktop, name);
    }

    const queue = desktop.getByRole('region', { name: 'Queue', exact: true });
    const entries = queue.locator('ol.entries li');
    await expect(entries).toHaveCount(3, { timeout: 20_000 });

    // Declared in order, so listed in order.
    await expect(entries.nth(0)).toContainText('first.txt');
    await expect(entries.nth(2)).toContainText('third.txt');

    // Moving the last one up is a keyboard-reachable button, not a drag:
    // FR-039g wants every essential flow usable without a pointer.
    await queue.getByRole('button', { name: 'Move third.txt up', exact: true }).click();
    await expect(entries.nth(1)).toContainText('third.txt', { timeout: 20_000 });
    await expect(entries.nth(2)).toContainText('second.txt');

    // The order came back from the server rather than from this page's memory:
    // a reload shows the same thing.
    await desktop.reload();
    const afterReload = desktop
      .getByRole('region', { name: 'Queue', exact: true })
      .locator('ol.entries li');
    await expect(afterReload).toHaveCount(3, { timeout: 30_000 });
    await expect(afterReload.nth(1)).toContainText('third.txt');
  });

  test('removing an entry cancels it, and the other device is told', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const select = desktop
      .getByRole('region', { name: 'Send', exact: true })
      .locator('select#send-target');
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    await queueFile(desktop, 'changed-my-mind.txt');

    // The phone knows about it, which is what makes the removal worth telling
    // it about.
    const phoneTransfers = phone.getByRole('region', { name: 'Transfers', exact: true });
    await expect(phoneTransfers).toContainText('changed-my-mind.txt', { timeout: 30_000 });

    const queue = desktop.getByRole('region', { name: 'Queue', exact: true });
    await queue
      .getByRole('button', { name: 'Remove changed-my-mind.txt from the queue', exact: true })
      .click();

    await expect(queue.locator('ol.entries li')).toHaveCount(0, { timeout: 20_000 });

    // And the phone stops offering to save something that will never arrive.
    await expect(phoneTransfers).toContainText('Cancelled', { timeout: 30_000 });
  });
});
