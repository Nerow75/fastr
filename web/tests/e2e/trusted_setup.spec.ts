import { test, expect, pair, openPhone } from './fixtures.js';

/**
 * Setting up trusted mode, from the screen the user actually reads.
 *
 * FR-047d makes three demands of this walkthrough, and two of them are about
 * what happens *before* and *after* rather than during:
 *
 *  - it explains what trusted mode buys **before it asks for anything**;
 *  - it can be abandoned at any point;
 *  - abandoning leaves the existing simple-mode pairing working.
 *
 * The last is the one most likely to rot without anyone noticing, because
 * nothing visibly breaks when it does — the user simply finds their phone
 * locked out, some time later, for no reason they can connect to this.
 *
 * What is *not* tested here, and cannot be: whether a phone that installs the
 * certificate reaches a secure context. That needs a real device, and T135 is
 * the work of checking it.
 */

test.describe('trusted mode setup', () => {
  test('explains the cost before asking, and can be walked away from', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const panel = desktop.getByRole('region', { name: 'Trusted mode', exact: true });
    await expect(panel).toBeVisible({ timeout: 20_000 });

    // What it buys, and what it costs, both on screen before any control that
    // does anything. The second is not fine print: it is the reason this is
    // opt-in rather than the default.
    await expect(panel).toContainText('Encrypts file content end to end');
    await expect(panel).toContainText('impersonate other sites');

    // Nothing has been created: the only thing offered is the invitation to
    // read further.
    await expect(panel.getByRole('button', { name: 'Create the certificate' })).toHaveCount(0);

    await panel.getByRole('button', { name: 'Set up trusted mode', exact: true }).click();

    // One more explanation, and an explicit way out, before the button that
    // writes a key.
    await expect(panel).toContainText('You can stop at any point');
    const create = panel.getByRole('button', { name: 'Create the certificate', exact: true });
    await expect(create).toBeVisible();

    // Walking away.
    await panel.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(create).toHaveCount(0);

    // And the pairing still works, which is the whole of FR-047d's promise. A
    // file goes from the phone to the computer exactly as before.
    const send = phone.getByRole('region', { name: 'Send', exact: true });
    await send.locator('input[type=file]:not([capture])').setInputFiles({
      name: 'after-walking-away.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('the pairing survived'),
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    const transfers = phone.getByRole('region', { name: 'Transfers', exact: true });
    await expect(transfers).toContainText('after-walking-away.txt', { timeout: 30_000 });
    await expect(transfers).toContainText('Done', { timeout: 30_000 });
  });

  test('after setup, it says plainly that nothing has changed yet', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const panel = desktop.getByRole('region', { name: 'Trusted mode', exact: true });
    await panel.getByRole('button', { name: 'Set up trusted mode', exact: true }).click();
    await panel.getByRole('button', { name: 'Create the certificate', exact: true }).click();

    // The steps a person now has to perform, with the fingerprint they will be
    // asked to compare. Without it, "install this certificate" is a request to
    // trust whatever arrived.
    await expect(panel).toContainText('install the certificate', { timeout: 20_000 });
    await expect(panel.locator('.fingerprint')).toHaveText(/^([0-9A-F]{2}:){31}[0-9A-F]{2}$/);

    // And the honest state: the certificate exists on the computer, and not one
    // device is using trusted mode until it has finished the steps.
    await expect(panel).toContainText('No device is using trusted mode yet');
    await expect(panel).toContainText('still readable on this network');
  });
});
