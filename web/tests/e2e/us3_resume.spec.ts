import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test, expect, pair, openPhone } from './fixtures.js';

/**
 * User Story 3 from the phone's side: an upload survives the page going away.
 *
 * This is the ordinary failure on a phone and it is invisible to every other
 * test in the suite. iOS discards a backgrounded tab, and when the user comes
 * back the page has been reloaded: the File objects are gone, the in-flight
 * request is gone, and nothing in memory remembers there was a transfer at all.
 * Everything the user has left is the file, still sitting in their camera roll.
 *
 * Two things have to be true for that to end well, and neither can be checked
 * below the browser:
 *
 *  1. The reloaded page finds the transfer again. It learns about transfers
 *     from events, and the events it needed were announced while it did not
 *     exist. Reading the queue on connect is what answers that (T087b).
 *  2. Picking the same file again continues rather than starts over. The
 *     committed offset is on the computer; what is missing is the page
 *     recognising that these bytes belong to that transfer.
 *
 * The interruption here is a real one at the network layer: the first chunk is
 * delivered and every one after it is refused, which is what a phone losing
 * Wi-Fi mid-upload looks like from inside the page.
 */

/** A chunk boundary, matching CHUNK_BYTES in web/src/lib/upload.ts. */
const CHUNK = 4 << 20;

test.describe('an interrupted upload', () => {
  test('survives the page being discarded, and resumes rather than restarts', async ({
    browser,
    fastr,
  }) => {
    test.skip(fastr.mobileURL === '', 'no LAN address: nothing can play the part of a phone');

    const desktop = await browser.newPage();
    await desktop.goto(fastr.desktopURL);

    const phone = await openPhone(browser, fastr);
    await pair(desktop, phone, 'Test Phone');

    // Two chunks' worth, so exactly one can be delivered and one refused.
    const payload = Buffer.alloc(CHUNK + (2 << 20));
    for (let i = 0; i < payload.length; i++) payload[i] = (i * 31 + 7) % 251;

    // Let the first chunk through; refuse the rest. `abort` fails the request
    // before it leaves, which is what the page sees when the network goes: a
    // bare TypeError out of fetch, with nothing to say what happened.
    //
    // Decided on the offset in the URL rather than on a counter. A counter says
    // "the first request I saw", which is not the same as "the first chunk": a
    // retry, a reordering, or a request that slipped past interception makes
    // the two diverge, and then the test's precondition quietly stops holding.
    // The offset is what the request is actually about.
    let cutOff!: () => void;
    const interrupted = new Promise<void>((resolve) => (cutOff = resolve));

    const contentRoute = /\/items\/\d+\/content\?offset=/;
    await phone.route(contentRoute, async (route) => {
      if (offsetOf(route.request().url()) === 0) {
        await route.continue();
        return;
      }
      // Aborted first, resolved second. The other order lets the test reload
      // the page while the abort is still being applied, and a route handler
      // that never finishes lets its request through — which delivers the chunk
      // this test needs withheld, leaves nothing to resume, and fails on an
      // assertion that has nothing to do with the cause.
      await route.abort('failed');
      cutOff();
    });

    const send = phone.getByRole('region', { name: 'Send', exact: true });
    await send.locator('input[type=file][multiple]').setInputFiles({
      name: 'from-the-camera-roll.mp4',
      mimeType: 'video/mp4',
      buffer: payload,
    });
    await send.getByRole('button', { name: 'Send', exact: true }).click();

    await interrupted;

    // The phone is backgrounded and the page is discarded. Everything it held
    // goes with it, including the file.
    await phone.reload();
    await phone.unroute(contentRoute);

    // 1. It finds the transfer again, from the queue rather than from an event
    //    it was not there to hear.
    const transfers = phone.getByRole('region', { name: 'Transfers', exact: true });
    await expect(transfers).toContainText('from-the-camera-roll.mp4', { timeout: 30_000 });

    // 2. Picking the same file offers to continue, and says what that saves.
    const back = phone.getByRole('region', { name: 'Send', exact: true });
    await back.locator('input[type=file][multiple]').setInputFiles({
      name: 'from-the-camera-roll.mp4',
      mimeType: 'video/mp4',
      buffer: payload,
    });

    const resume = back.getByRole('button', { name: 'Resume', exact: true });
    await expect(resume).toBeVisible({ timeout: 20_000 });
    await expect(back).toContainText('already being sent');

    // Measured from here: exactly what the resumed upload puts on the wire.
    //
    // Through a route rather than a request listener. A route is the same
    // mechanism that withheld the chunk above, and it is authoritative in a way
    // an observer is not: a request that is not intercepted is not sent, so the
    // count cannot silently miss one. The bytes matter more than the count —
    // they are what SC-005 is about — and the offset says the resume began in
    // the right place rather than merely sending less.
    let uploaded = 0;
    const resumedOffsets: number[] = [];
    await phone.route(contentRoute, async (route) => {
      uploaded += route.request().postDataBuffer()?.length ?? 0;
      resumedOffsets.push(offsetOf(route.request().url()));
      await route.continue();
    });

    await resume.click();

    // The computer wrote the file, which is the only proof that matters.
    await expect(async () => {
      const received = readFileSync(path.join(fastr.receiveDir, 'from-the-camera-roll.mp4'));
      expect(received.length).toBe(payload.length);
      expect(received.equals(payload)).toBe(true);
    }).toPass({ timeout: 60_000 });

    // The whole point, in two numbers: only the missing part was sent, and it
    // was sent from where the interruption stopped.
    expect(uploaded, 'a resumed upload re-sent bytes the computer already had').toBe(
      payload.length - CHUNK,
    );
    expect(resumedOffsets, 'a resumed upload did not continue from the committed offset').toEqual([
      CHUNK,
    ]);
  });
});

/** The `offset` query parameter of an upload request. */
function offsetOf(url: string): number {
  return Number(new URL(url).searchParams.get('offset') ?? -1);
}
