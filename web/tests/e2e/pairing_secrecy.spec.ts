import { test, expect, openDesktop, openPhone, readPairingCode } from './fixtures.js';

/**
 * The pairing code must never leave the phone, checked against what the real
 * browser client actually sends.
 *
 * There is a Go version of this in test/integration/pairing_secrecy_test.go and
 * it is not the same test. That one drives a client written for the test, so it
 * proves the endpoints do not require the code. This one drives
 * web/src/lib/session.ts, which is the code that ships, and proves the phone
 * does not send it — including anything a well-meaning change might add later
 * "so the server can check it".
 *
 * The exchange runs over plain HTTP: a browser grants no secure context to a
 * local address, so there is nothing to encrypt this with before a key exists.
 * Everything a phone sends during pairing is readable by anyone on the network,
 * and the design before CPace put the six digits in there.
 */
test.describe('@security what the phone sends while pairing', () => {
  test('the pairing code appears in no request the phone makes', async ({ browser, fastr }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await openDesktop(browser, fastr);
    const phone = await openPhone(browser, fastr);

    const code = await readPairingCode(desktop);

    // Every body the phone sends, recorded before it goes out.
    //
    // `page.on('request')` rather than a route interception, because this must
    // observe the real exchange rather than stand in the middle of it: a
    // handshake answered by the test is not a handshake that proves anything
    // about the one that ships.
    const sent: string[] = [];
    phone.on('request', (request) => {
      const body = request.postData();
      if (body !== null) sent.push(`${request.method()} ${request.url()}\n${body}`);
    });

    const form = phone.getByRole('region', { name: 'Connect this phone', exact: true });
    await form.locator('#pairing-code').fill(code);
    await form.locator('#device-name').fill('Quiet Phone');
    await form.getByRole('button', { name: 'Connect', exact: true }).click();

    const request = desktop.getByRole('region', {
      name: 'A device wants to connect',
      exact: true,
    });
    await expect(request).toContainText('Quiet Phone', { timeout: 20_000 });
    await request.getByRole('button', { name: 'Allow', exact: true }).click();

    // Paired, so the exchange really did complete: a test that recorded nothing
    // because pairing failed would pass this file's assertion for the wrong
    // reason.
    await expect(phone.locator('header .status')).toBeVisible({ timeout: 30_000 });

    expect(sent.length).toBeGreaterThan(0);

    const traffic = sent.join('\n\n');
    expect(traffic, 'the pairing code was sent by the phone').not.toContain(code);

    // And not in an encoding either. Base64 is how it would come back after
    // somebody changed a serialisation without thinking about this file.
    const encoded = Buffer.from(code, 'utf8').toString('base64');
    expect(traffic, 'the pairing code was sent base64-encoded').not.toContain(encoded);

    await desktop.context().close();
    await phone.context().close();
  });
});
