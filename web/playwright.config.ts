import { defineConfig, devices } from '@playwright/test';

/**
 * Browser tests, per constitution Principle II.
 *
 * The principle requires every release to work on iOS Safari and Android
 * Chrome. Playwright's WebKit is the closest thing to Safari that runs
 * unattended, and Chromium stands in for Android Chrome. Neither is the real
 * thing, and these tests do not pretend otherwise: they catch regressions in
 * flows that already worked, they do not replace testing on a physical phone.
 *
 * **WebKit does not run on every developer machine.** Playwright ships an
 * Ubuntu-only WebKit build that links against Debian library versions, so on
 * Fedora and other non-Debian systems it downloads and then refuses to launch.
 * Rather than fail the whole suite there, the WebKit project is skipped when its
 * browser is unavailable and CI runs both on Ubuntu. That keeps the local run
 * useful without letting the gate quietly disappear: CI is where the release is
 * blocked, and CI has both.
 *
 * The tests live under web/ rather than in test/e2e as the task list first
 * said. They test the built binary, so test/ would have been the tidier home,
 * but Node resolves dependencies by walking up from the file: a spec in
 * test/e2e cannot see @playwright/test in web/node_modules, and the fixes for
 * that are a root workspace, a NODE_PATH, or a symlink — three kinds of magic
 * to save one directory. Here, npm ci, ESLint and Prettier already cover them.
 */

const webkitAvailable = process.env.FASTR_SKIP_WEBKIT !== '1';

export default defineConfig({
  testDir: './tests/e2e',
  // A transfer is not instant, and pairing involves a human-scale poll.
  timeout: 90_000,
  expect: { timeout: 15_000 },

  // A shared binary with one store and one queue: two files arriving at once
  // would contend for the single active transfer slot, which is a real
  // constraint (FR-035a) and not something to work around here.
  workers: 1,
  fullyParallel: false,

  // A flaky end-to-end test that passes on retry hides exactly the kind of race
  // this suite exists to find.
  retries: 0,
  forbidOnly: !!process.env.CI,

  reporter: process.env.CI ? [['github'], ['list']] : [['list']],

  use: {
    // Pinned, because the interface negotiates its language from the browser
    // and every selector below is an English string. A runner with a French
    // locale would otherwise fail the whole suite for the wrong reason.
    locale: 'en-US',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // Nothing here should ever reach outside the machine. A test that tries is
    // a bug worth failing on rather than waiting out.
    actionTimeout: 15_000,
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    ...(webkitAvailable
      ? [
          {
            name: 'webkit',
            use: { ...devices['Desktop Safari'] },
          },
        ]
      : []),
  ],
});
