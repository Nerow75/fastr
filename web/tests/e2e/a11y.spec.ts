import AxeBuilder from '@axe-core/playwright';
import { test, expect, pair, openPhone, openDesktop } from './fixtures.js';
import type { Page } from '@playwright/test';

/**
 * The accessibility gate, per FR-039f to FR-039j.
 *
 * FR-039j is the one that decides how this file is shaped: accessibility is
 * verified **in the same gate as everything else**, so a regression blocks a
 * merge rather than being found by somebody who could not use the product. It
 * is not a separate audit anyone can forget to run — `make test-a11y` exists
 * for running it alone, and CI runs it with the rest.
 *
 * Two kinds of check, because they catch different things:
 *
 *  - **axe-core**, which finds the mechanical failures: contrast, missing
 *    names, wrong roles, orphaned labels. It is thorough and it is blind to
 *    whether the thing can actually be *done*.
 *  - **keyboard-only traversal**, which is the part that matters to somebody
 *    who cannot use a pointer. FR-039g asks for every essential flow to be
 *    completable with a keyboard alone, and only driving it that way answers
 *    that.
 *
 * The essential flows named by FR-039f are pairing, sending, receiving,
 * progress, and error recovery, so those are what is covered here rather than
 * every screen.
 */

/**
 * Runs axe against a page and returns the violations that matter.
 *
 * WCAG 2.2 AA, as FR-039f names, and nothing beyond it: `best-practice` rules
 * are opinions rather than the standard, and a gate that fails on an opinion is
 * a gate people learn to bypass.
 */
async function violationsOn(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze();

  return results.violations.map(
    (violation) =>
      `${violation.id} (${violation.impact}): ${violation.help}\n    ${violation.nodes
        .map((node) => node.target.join(' '))
        .join('\n    ')}`,
  );
}

test.describe('@a11y the essential flows', () => {
  test('the first screens on both devices have no violations', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);
    // The invitation panel is the first thing anybody meets; wait for it rather
    // than auditing a half-rendered page.
    await expect(desktop.getByRole('region', { name: 'Connect a phone', exact: true })).toBeVisible(
      {
        timeout: 20_000,
      },
    );

    const phone = await openPhone(browser, fastr);
    await expect(
      phone.getByRole('region', { name: 'Connect this phone', exact: true }),
    ).toBeVisible({ timeout: 20_000 });

    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const violations = await violationsOn(page);
      expect(violations, `${name}, before pairing:\n${violations.join('\n')}`).toEqual([]);
    }
  });

  test('the paired interface has no violations, on either device', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    // Every panel the paired interface renders: devices, queue, transfers,
    // history, and the trusted-mode walkthrough.
    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const violations = await violationsOn(page);
      expect(violations, `${name}, once paired:\n${violations.join('\n')}`).toEqual([]);
    }
  });

  test('a transfer in progress has no violations', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const send = desktop.getByRole('region', { name: 'Send', exact: true });
    const select = send.locator('select#send-target');
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'being-sent.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('a transfer somebody is watching'),
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    // The progress bar, the queue entry, and the Save button on the phone: the
    // states FR-039f names as essential.
    await expect(phone.getByRole('region', { name: 'Transfers', exact: true })).toContainText(
      'being-sent.txt',
      { timeout: 30_000 },
    );

    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const violations = await violationsOn(page);
      expect(violations, `${name}, mid-transfer:\n${violations.join('\n')}`).toEqual([]);
    }
  });

  /**
   * FR-039g, and the check axe cannot make.
   *
   * Pairing a phone with the keyboard alone: tab to the code field, type it,
   * tab to the name, tab to the button, press it. If any step needs a pointer,
   * the flow is not completable and this fails — which is the whole point, since
   * drag and drop and click handlers are exactly what tends to creep in.
   */
  test('a phone can be paired with a keyboard alone', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);

    const phone = await openPhone(browser, fastr);

    // The code, read from the screen the way a person reads it.
    const panel = desktop.getByRole('region', { name: 'Connect a phone', exact: true });
    const reveal = panel.getByRole('button', { name: 'Connect another device', exact: true });
    await expect(async () => {
      if (await reveal.isVisible()) await reveal.click();
      await expect(panel.locator('.code')).toBeVisible({ timeout: 2_000 });
    }).toPass({ timeout: 20_000 });
    const code = ((await panel.locator('.code').textContent()) ?? '').trim();

    // From here on, only the keyboard.
    const form = phone.getByRole('region', { name: 'Connect this phone', exact: true });
    await expect(form).toBeVisible();

    await phone.keyboard.press('Tab'); // skip link
    await phone.keyboard.press('Tab'); // the code field

    const focused = await phone.evaluate(() => document.activeElement?.id ?? '');
    expect(focused, 'the pairing code field is not the first control reachable').toBe(
      'pairing-code',
    );

    await phone.keyboard.type(code);
    await phone.keyboard.press('Tab');
    await phone.keyboard.type('Keyboard Phone');
    await phone.keyboard.press('Tab');
    await phone.keyboard.press('Enter');

    // The computer approves, also from the keyboard.
    const request = desktop.getByRole('region', {
      name: 'A device wants to connect',
      exact: true,
    });
    await expect(request).toContainText('Keyboard Phone', { timeout: 20_000 });

    const allow = request.getByRole('button', { name: 'Allow', exact: true });
    await allow.focus();
    await desktop.keyboard.press('Enter');

    // And the phone is in, having never been pointed at.
    await expect(phone.locator('header .status')).toBeVisible({ timeout: 30_000 });
  });

  /**
   * FR-039g again, on the sending flow, which is where a pointer-only control
   * is most likely to appear: the drop zone.
   *
   * The drop zone is deliberately not focusable — a region that accepts a drop
   * but does nothing on Enter is a trap rather than a control — so the buttons
   * beside it are what has to work.
   */
  test('a file can be chosen and sent with a keyboard alone', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const send = desktop.getByRole('region', { name: 'Send', exact: true });

    // The destination, chosen from the keyboard.
    const select = send.locator('select#send-target');
    await select.focus();
    await expect(select).toBeFocused();
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    // Choosing files opens a native dialog no test can drive, so the file is
    // supplied directly — what is being checked is that the *control* is
    // reachable and operable, not that Chromium's file picker works.
    const choose = send.getByRole('button', { name: 'Choose files', exact: true });
    await choose.focus();
    await expect(choose).toBeFocused();

    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'from-the-keyboard.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('sent without a pointer'),
    });

    const button = send.getByRole('button', { name: 'Send', exact: true });
    await button.focus();
    await expect(button).toBeFocused();
    await desktop.keyboard.press('Enter');

    await expect(phone.getByRole('region', { name: 'Transfers', exact: true })).toContainText(
      'from-the-keyboard.txt',
      { timeout: 30_000 },
    );
  });
});
