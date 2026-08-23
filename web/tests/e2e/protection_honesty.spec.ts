import { test, expect, pair, openPhone } from './fixtures.js';
import type { Page } from '@playwright/test';

/**
 * SC-016a, and the honesty duty in constitution Principle V.
 *
 * In simple mode **file content travels in the clear on the local network**.
 * Anyone on the same Wi-Fi can read it. That is a deliberate trade — it is what
 * buys "nothing to install on the phone" — and the constitution's answer to
 * making it acceptable is that the interface never lets anyone believe
 * otherwise.
 *
 * So this is not a copy check. It is the test that keeps a whole design
 * decision honest, and it fails in the direction that matters: a screen that
 * *adds* a reassuring word is what it is looking for, because that is the
 * change somebody makes in good faith while tidying up a paragraph.
 *
 * Two rules, from SC-016a:
 *
 *  - every screen where a simple-mode transfer is set up or shown says content
 *    is readable on this network;
 *  - **zero** screens describe simple mode as private, secure, or encrypted
 *    without that qualification.
 */

/**
 * Words that would reassure somebody about their files.
 *
 * The rule is narrower than "these words never appear": "pairing and
 * credentials are encrypted" is true, precise, and about something else
 * entirely, and a check that flagged it would push the interface towards saying
 * *less* about what is protected, which is the opposite of the point.
 */
const REASSURING = [
  'private',
  'secure',
  'securely',
  'encrypted',
  'encrypts',
  'end to end',
  'end-to-end',
  'protected',
  'safe',
];

/** What the claim has to be *about* for it to be a claim about simple mode. */
const ABOUT_CONTENT = ['file', 'files', 'content', 'transfer', 'transfers', 'photo', 'photos'];

/** The sentence that has to be present wherever simple mode is on screen. */
const QUALIFICATION = /readable by anyone on this network|readable on this network/i;

/**
 * Reads the visible text of a page, minus the trusted-mode panel.
 *
 * innerText rather than textContent, because what is being audited is what a
 * person reads and textContent includes text hidden from view.
 *
 * The trusted-mode panel is excluded because it is the one place on screen
 * where "encrypts file content end to end" is simply true. Excluding it by
 * region rather than by wording keeps the rule honest: everywhere else, that
 * sentence would be a lie.
 */
async function simpleModeText(page: Page): Promise<string> {
  const trusted = page.getByRole('region', { name: 'Trusted mode', exact: true });
  const trustedText = (await trusted.count()) > 0 ? await trusted.innerText() : '';

  const whole = await page.locator('body').innerText();
  return trustedText === '' ? whole : whole.replace(trustedText, '');
}

/**
 * Finds claims about files that are not next to the qualification.
 *
 * Sentence by sentence rather than page-wide: a page that says "content is
 * readable on this network" in one corner and "your files are safe" in another
 * has still told somebody the wrong thing, and a page-wide check would pass it.
 */
function unqualifiedClaims(text: string): string[] {
  const sentences = text.split(/[.!?\n]+/);
  const found: string[] = [];

  for (const sentence of sentences) {
    if (QUALIFICATION.test(sentence)) continue;

    const lower = sentence.toLowerCase();
    // Word boundaries throughout, so "unprotected" and "insecure" are not read
    // as claims of the opposite of what they say.
    const aboutFiles = ABOUT_CONTENT.some((word) => new RegExp(`\\b${word}\\b`).test(lower));
    if (!aboutFiles) continue;

    for (const word of REASSURING) {
      if (new RegExp(`\\b${word}\\b`).test(lower)) {
        found.push(`${word}: ${sentence.trim()}`);
      }
    }
  }
  return found;
}

test.describe('what the interface says about simple mode', () => {
  test('says content is readable, on both devices, before and during a transfer', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    // Both devices, once paired: this is where a transfer is set up.
    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const text = await simpleModeText(page);
      expect(text, `${name} does not say content is readable on this network`).toMatch(
        QUALIFICATION,
      );
    }

    // And during a transfer, which is the moment somebody is most likely to
    // assume otherwise.
    const send = desktop.getByRole('region', { name: 'Send', exact: true });
    const select = send.locator('select#send-target');
    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1, { timeout: 20_000 });
    await select.selectOption((await option.getAttribute('value'))!);

    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'in-the-clear.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('anyone on this network can read this'),
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    const transfers = phone.getByRole('region', { name: 'Transfers', exact: true });
    await expect(transfers).toContainText('in-the-clear.txt', { timeout: 30_000 });

    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const text = await simpleModeText(page);
      expect(text, `${name} stops saying it during a transfer`).toMatch(QUALIFICATION);
    }
  });

  test('never calls simple mode private, secure, or encrypted', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);

    // Before pairing: the first screens anybody sees.
    for (const [name, page] of [
      ['the computer before pairing', desktop],
      ['the phone before pairing', phone],
    ] as const) {
      const claims = unqualifiedClaims(await simpleModeText(page));
      expect(claims, `${name} makes an unqualified claim`).toEqual([]);
    }

    await pair(desktop, phone, 'Test Phone');

    // And after, with every panel the paired interface renders. This is where
    // the trusted-mode explanation lives, which is the one place words like
    // "encrypted" legitimately appear — and it is exactly why the check is per
    // sentence rather than per page.
    for (const [name, page] of [
      ['the computer', desktop],
      ['the phone', phone],
    ] as const) {
      const claims = unqualifiedClaims(await simpleModeText(page));
      expect(claims, `${name} makes an unqualified claim`).toEqual([]);
    }
  });
});
