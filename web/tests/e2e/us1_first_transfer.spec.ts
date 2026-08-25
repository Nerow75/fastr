import { readFileSync } from 'node:fs';
import { test, expect, pair, openPhone, expand } from './fixtures.js';

/**
 * User Story 1, end to end: scan the code, pair, drop a file on the computer,
 * open it on the phone.
 *
 * This is the journey the product exists for, and it is the only test that
 * covers all of it at once. Everything it touches has narrower tests below it;
 * what none of those can tell you is whether someone starting from a running
 * binary and a phone ends up with the file.
 *
 * The computer's page is not made to pair with itself. It used to be — read six
 * digits from its own screen, type them into its own form, approve its own
 * request — and nobody found the step, which made sending *from* the computer
 * unreachable in practice. It is now granted a session on load, over loopback.
 */

test.describe('the first transfer', () => {
  test('the computer offers a way in, and a dropped file reaches the phone', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    // FR-002: the entry point is presented as a short local address and a QR
    // code encoding it. Before this panel existed the code reached only the
    // terminal, so a first connection meant reading a console.
    const invitation = desktop.getByRole('region', { name: 'Connect a phone', exact: true });
    await expect(invitation).toBeVisible();

    const qr = invitation.locator('img.qr');
    await expect(qr).toBeVisible();

    const src = await qr.getAttribute('src');
    expect(src).toContain('/qr?url=');
    // The address encoded is the one a phone can reach, never loopback.
    expect(decodeURIComponent(src ?? '')).toContain(fastr.mobileURL);

    await expect(invitation).toContainText(fastr.mobileURL);

    // The computer is already able to send: no code, no form, no step to find.
    await expect(
      desktop.getByRole('region', { name: 'Connect this phone', exact: true }),
    ).toHaveCount(0);
    await expect(desktop.getByRole('region', { name: 'Send', exact: true })).toBeVisible({
      timeout: 20_000,
    });

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    // The phone's pairing spent that code, so the panel must stop showing it.
    // It used to keep the spent digits up, and the next person to try them was
    // told the code was already used, with no way forward.
    //
    // It also folds away at this point: with a phone connected, connecting a
    // second one is an errand rather than the task, and a live code left on
    // screen is an invitation to whoever walks past the desk. So the digits
    // must be gone from the page whether the panel is open or shut, and what
    // is behind it is the offer to fetch a fresh one.
    await expect(invitation.locator('.code')).toHaveCount(0, { timeout: 20_000 });

    await expand(invitation);
    await expect(
      invitation.getByRole('button', { name: 'Connect another device', exact: true }),
    ).toBeVisible({ timeout: 20_000 });
    await expect(invitation.locator('.code')).toHaveCount(0);

    // Big enough that it is streamed rather than handled in one gulp.
    const payload = Buffer.alloc(6 << 20);
    for (let i = 0; i < payload.length; i++) payload[i] = (i * 17) % 253;

    const send = desktop.getByRole('region', { name: 'Send', exact: true });
    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'lecture.mp4',
      mimeType: 'video/mp4',
      buffer: payload,
    });

    // Selected by value rather than by label: the label carries the device's
    // reachability, so matching the bare name would break the moment that text
    // changes — which is exactly the sort of churn a test should not care about.
    const option = send.locator('select#send-target option', { hasText: 'Test Phone' });
    await expect(option).toHaveCount(1);
    await send.locator('select#send-target').selectOption((await option.getAttribute('value'))!);
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    // The phone is told there is something for it, and saves it. The two steps
    // are separate because the second is a real link handed to the browser's
    // own download manager: nothing else writes a multi-gigabyte file to a
    // phone without holding it in memory.
    const prepare = phone.getByRole('button', { name: /^Save · lecture\.mp4$/ });
    await expect(prepare).toBeVisible({ timeout: 60_000 });
    await prepare.click();

    const link = phone.getByRole('link', { name: /^Save · lecture\.mp4$/ });
    await expect(link).toBeVisible({ timeout: 30_000 });

    // Generous, because the response only begins once the sending page has
    // attached its body to the waiting request: the bytes go from one browser
    // to the other through the server without touching disk, so the headers
    // wait for the rendezvous. internal/transfer/pipe.go allows 30s for it.
    const download = await Promise.all([
      phone.waitForEvent('download', { timeout: 60_000 }),
      link.click(),
    ]).then(([d]) => d);

    expect(download.suggestedFilename()).toBe('lecture.mp4');

    // Reported before asking for the file: a cancelled download returns a null
    // path with no reason attached, and the reason is the whole diagnosis.
    expect(await download.failure()).toBeNull();

    const saved = await download.path();
    expect(saved).toBeTruthy();

    const got = readFileSync(saved!);
    expect(got.length).toBe(payload.length);
    expect(got.equals(payload)).toBe(true);
  });

  test('a device that cannot be reached is never chosen for you', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    const select = desktop
      .getByRole('region', { name: 'Send', exact: true })
      .locator('select#send-target');

    // Reachable and alone: choosing it is a convenience worth keeping.
    await expect(select).not.toHaveValue('', { timeout: 20_000 });

    // A pairing lasts a year, so a phone that closed its page stays in this
    // list. Close it, and wait for the computer to have noticed: the label
    // carries the reachability, so it is also the signal that the event
    // arrived. Reloading before it does would read the stale answer.
    const context = phone.context();
    await phone.close();

    const option = select.locator('option', { hasText: 'Test Phone' });
    await expect(option).toHaveText(/not connected/, { timeout: 20_000 });

    // The resting state of a fresh page, which is what a person actually
    // meets. A select shows its first option, so preselecting the only paired
    // phone put "Test Phone — not connected" in the box of someone whose phone
    // had never opened its page, and sending to it did nothing and said
    // nothing. The placeholder says what to do instead.
    await desktop.reload();
    await expect(select).toHaveValue('', { timeout: 20_000 });
    await expect(select.locator('option:checked')).toHaveText('Choose a device');

    // And the phone coming back is enough to be offered again: same context,
    // so it still holds its credential and does not pair a second time.
    const returned = await context.newPage();
    await returned.goto(fastr.mobileURL);
    await expect(select).not.toHaveValue('', { timeout: 30_000 });
  });

  test('an unpaired phone is offered nothing but the pairing form', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const phone = await openPhone(browser, fastr);

    // FR-011: this is what an unpaired device sees, and it is all it sees.
    await expect(
      phone.getByRole('region', { name: 'Connect this phone', exact: true }),
    ).toBeVisible();

    // No device list, no transfers, and above all no invitation panel: the
    // pairing code is restricted to the host's own screen, and a phone that
    // could read it could let itself in.
    await expect(phone.getByRole('region', { name: 'Connect a phone', exact: true })).toHaveCount(
      0,
    );
    await expect(phone.locator('.code')).toHaveCount(0);
  });
});
