import { test, expect, pair, openPhone, openDesktop } from './fixtures.js';
import type { Page } from '@playwright/test';

/**
 * FR-039i: transfer progress is announced at meaningful moments — start,
 * interruption, resumption, completion, failure — and **not on every update**.
 *
 * The requirement exists because the failure is not a missing feature but an
 * unusable one. A screen reader reads a live region every time it changes; a
 * progress bar that announced each update would speak four times a second for
 * the length of a transfer, and the person would learn to turn the whole thing
 * off. Announcing too much is how a product becomes inaccessible while passing
 * every check for whether it announces at all.
 *
 * So this test counts. It watches the live regions through a whole transfer and
 * asserts that what was spoken is a handful of moments rather than a running
 * commentary, and that the moments that matter are among them.
 */

/**
 * Records everything written to the live regions from now on.
 *
 * A MutationObserver rather than polling: an announcement that is written and
 * replaced between two polls is one a person heard and this test would miss,
 * and the whole question here is how many there were.
 */
async function recordAnnouncements(page: Page): Promise<void> {
  await page.evaluate(() => {
    const spoken: string[] = [];
    (window as unknown as { __spoken: string[] }).__spoken = spoken;

    for (const region of document.querySelectorAll('[aria-live]')) {
      new MutationObserver(() => {
        const text = region.textContent?.trim() ?? '';
        if (text !== '' && spoken[spoken.length - 1] !== text) spoken.push(text);
      }).observe(region, { childList: true, characterData: true, subtree: true });
    }
  });
}

async function announcements(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __spoken: string[] }).__spoken ?? []);
}

test.describe('@a11y what gets announced', () => {
  test('a whole transfer speaks a handful of times, not continuously', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);
    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const send = desktop.getByRole('region', { name: 'Send', exact: true });
    const select = send.locator('select#send-target');
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    // Big enough that a per-update announcement would be unmistakable: this
    // many chunks would speak dozens of times.
    const payload = Buffer.alloc(6 << 20);
    for (let i = 0; i < payload.length; i++) payload[i] = (i * 7 + 3) % 251;

    await recordAnnouncements(phone);
    await recordAnnouncements(desktop);

    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'spoken-about.bin',
      mimeType: 'application/octet-stream',
      buffer: payload,
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    // Received on the phone, which is where progress is watched.
    const prepare = phone.getByRole('button', { name: /^Save · spoken-about\.bin$/ });
    await expect(prepare).toBeVisible({ timeout: 60_000 });
    await prepare.click();

    const link = phone.getByRole('link', { name: /^Save · spoken-about\.bin$/ });
    await expect(link).toBeVisible({ timeout: 30_000 });

    const download = await Promise.all([
      phone.waitForEvent('download', { timeout: 60_000 }),
      link.click(),
    ]).then(([d]) => d);
    expect(await download.failure()).toBeNull();

    // Waited for rather than read immediately: the transfer completes on the
    // server once the last byte is written, and the event reaches the phone a
    // moment later. Asserting "few announcements" against a list that is empty
    // because nothing has arrived yet would prove nothing at all.
    await expect(async () => {
      const spoken = await announcements(phone);
      expect(spoken.join('\n')).toMatch(/complete/i);
    }).toPass({ timeout: 30_000 });

    for (const [name, page] of [
      ['the phone', phone],
      ['the computer', desktop],
    ] as const) {
      const spoken = await announcements(page);

      // Something was said, on both devices. A receiving device that is told
      // nothing at all passes a count check trivially and leaves the person
      // waiting on it with no idea the file arrived.
      expect(spoken.length, `${name} was never told anything`).toBeGreaterThan(0);

      // The number is the assertion. A transfer of this size moves through
      // dozens of progress events; anything approaching that count means
      // progress is being read out.
      expect(
        spoken.length,
        `${name} spoke ${spoken.length} times during one transfer:\n${spoken.join('\n')}`,
      ).toBeLessThanOrEqual(6);

      // And nothing spoken is a progress reading, which is the specific thing
      // FR-039i forbids.
      for (const line of spoken) {
        expect(line, `${name} read out progress: ${line}`).not.toMatch(/\d+\s*%/);
      }
    }
  });

  test('the moments that matter are spoken', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);
    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    await recordAnnouncements(phone);

    // A transfer from the phone, so completion is announced on the device that
    // performed it.
    const send = phone.getByRole('region', { name: 'Send', exact: true });
    await send.locator('input[type=file]:not([capture])').setInputFiles({
      name: 'announced.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('something worth saying'),
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    // Completion is one of the five moments FR-039i names.
    await expect(async () => {
      const spoken = await announcements(phone);
      expect(spoken.join('\n')).toMatch(/complete/i);
    }).toPass({ timeout: 45_000 });

    // Selecting files is also announced, because a person who cannot see the
    // list has no other way to know what they picked.
    const spoken = await announcements(phone);
    expect(spoken.join('\n')).toMatch(/announced\.txt|1 file/i);
  });
});
