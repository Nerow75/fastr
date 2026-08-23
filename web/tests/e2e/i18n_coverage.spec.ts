import { test, expect, pair, openDesktop, LABELS } from './fixtures.js';
import type { Browser, Page } from '@playwright/test';

/**
 * FR-039a and FR-039d, checked against what is actually on screen.
 *
 * The Go side already proves the catalogues agree with each other: every key in
 * one exists in the other, with the same placeholders. What it cannot see is
 * the interface — a component that renders `t('transfer.state.paused')` for a
 * state nobody added to the catalogue produces the literal string
 * `transfer.state.paused` on screen, and every catalogue test still passes.
 *
 * Two failures, and both look like ordinary text to a reader who is not looking
 * for them:
 *
 *  - a **translation key** shown as text, which is what a missing entry looks
 *    like;
 *  - a **raw identifier** where a name belongs, which is what a lookup that
 *    fell back to the primary key looks like.
 *
 * The second is the one worth the trouble. A device list that says
 * `01ARZ3NDEKTSV4RRFFQ69G5FAV` instead of "Test Phone" is not broken in any way
 * a test of the endpoints would notice, and it is useless to a person.
 */

/**
 * A translation key rendered as text.
 *
 * Anchored to the namespaces the catalogue actually uses, rather than any
 * dotted word: "fastr.db" and "photo.jpg" are file names, and a check that
 * flagged them would be turned off within a week.
 */
const KEY_PATTERN =
  /\b(app|nav|action|pairing|device|transfer|queue|history|settings|protection|trusted|relay|sweep|notification|error|a11y)\.[a-z_]+(\.[a-z_]+)*\b/;

/**
 * A ULID, which is what every identifier in this product is.
 *
 * Crockford base32, 26 characters, always starting with 0 for the next few
 * centuries. Identifiers are legitimate in URLs and attributes; what this
 * catches is one that reached the text a person reads.
 */
const RAW_IDENTIFIER = /\b0[0-9A-HJKMNP-TV-Z]{25}\b/;

async function visibleText(page: Page): Promise<string> {
  return page.locator('body').innerText();
}

function offendingLines(text: string, pattern: RegExp): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => pattern.test(line));
}

test.describe('what the interface renders', () => {
  test('shows no translation keys and no raw identifiers, in either language', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    for (const [locale, labels] of [
      ['en-US', LABELS.en],
      ['fr-FR', LABELS.fr],
    ] as const) {
      const desktop = await openDesktop(browser, fastr);
      const phone = await openLocalisedPhone(browser, fastr.mobileURL, locale);
      // Named per locale: both phones live in one server instance, and two
      // devices with one name would make the destination ambiguous.
      const deviceName = `Test Phone ${locale}`;
      await pair(desktop, phone, deviceName, labels);

      // A transfer, so the states, the queue, and the history are all on screen
      // rather than only the empty versions of each.
      const send = desktop.getByRole('region', { name: 'Send', exact: true });
      const select = send.locator('select#send-target');
      const option = select.locator('option', { hasText: deviceName });
      await expect(option).toHaveCount(1, { timeout: 20_000 });
      await select.selectOption((await option.getAttribute('value'))!);

      await send.locator('input[type=file][multiple]').setInputFiles({
        name: 'rendered.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('what the interface says about this'),
      });
      await send.getByRole('button', { name: 'Send', exact: true }).click();

      // The file name rather than a named region: the region is called
      // "Transfers" in one language and "Transferts" in the other, and a file
      // name is the same in both.
      await expect(phone.locator('body')).toContainText('rendered.txt', { timeout: 30_000 });

      for (const [name, page] of [
        [`the computer (${locale})`, desktop],
        [`the phone (${locale})`, phone],
      ] as const) {
        const text = await visibleText(page);

        const keys = offendingLines(text, KEY_PATTERN);
        expect(keys, `${name} shows a translation key:\n${keys.join('\n')}`).toEqual([]);

        const identifiers = offendingLines(text, RAW_IDENTIFIER);
        expect(
          identifiers,
          `${name} shows a raw identifier where a name belongs:\n${identifiers.join('\n')}`,
        ).toEqual([]);
      }

      await desktop.context().close();
      await phone.context().close();
    }
  });

  test('a French device gets French, not a fallback', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    // FR-039b: the language comes from the device's own preference. This is the
    // check that the negotiation is wired to something, rather than English
    // being served to everyone and the second catalogue being decorative —
    // which is exactly what it was until the language was set before the first
    // render instead of after it.
    const desktop = await openDesktop(browser, fastr);
    const phone = await openLocalisedPhone(browser, fastr.mobileURL, 'fr-FR');

    const form = phone.getByRole('region', { name: 'Connecter ce téléphone', exact: true });
    await expect(form).toBeVisible({ timeout: 20_000 });

    await pair(desktop, phone, 'Téléphone de test', LABELS.fr);

    // And the protection notice, which is the one sentence that has to be right
    // in every language: Principle V's honesty duty is not English-only, and a
    // French user reading a French interface must be told the same thing.
    await expect(phone.locator('body')).toContainText('lisible par toute personne sur ce réseau', {
      timeout: 20_000,
    });
  });
});

/** Opens the phone's page as a device whose owner reads a given language. */
async function openLocalisedPhone(
  browser: Browser,
  mobileURL: string,
  locale: string,
): Promise<Page> {
  const context = await browser.newContext({ locale });
  const page = await context.newPage();
  await page.goto(mobileURL);
  return page;
}
