/**
 * Coming back after an interruption, per FR-031 and FR-035d.
 *
 * The phone is the side that gets interrupted. It locks its screen, it walks out
 * of range, it backgrounds the tab, and any of those kills whatever request was
 * open. The server already treats that as an interruption rather than a failure
 * and keeps the resume point; this is the other half, and without it the
 * interruption still reaches the user as an error they have to act on.
 *
 * **Retrying is resuming.** Nothing here replays a request. The upload asks the
 * server where it stands before sending anything, so calling it again after a
 * failure continues from the committed offset by construction. That is why this
 * file is a schedule and a classifier rather than a queue of pending writes.
 *
 * **What is worth retrying is a short list.** A dropped connection, a server
 * that is momentarily busy, a request that never got an answer. A refused
 * credential, a corrupted file, a transfer that no longer exists: retrying any
 * of those produces the same answer forever while the interface says
 * "reconnecting", which is worse than saying what happened.
 */

/** How the delay between attempts grows. */
export interface RetryPolicy {
  /** How many times to try in total, including the first. */
  attempts: number;
  /** The wait before the second attempt, in milliseconds. */
  first: number;
  /** The ceiling, so a long outage does not back off into next week. */
  max: number;
  /** What each wait is multiplied by. */
  factor: number;
}

/**
 * The default schedule: 1s, 2s, 4s, 8s, 16s, 30s, capped, over six attempts.
 *
 * A little over a minute of trying, which covers the ordinary interruptions —
 * a lift, a screen lock, an access point handover — without leaving a page
 * claiming to reconnect long after the user has given up on it. Beyond that the
 * transfer is interrupted rather than failed on the server, so nothing is lost
 * by stopping: the resume point survives for seven days and picking the file
 * again continues from it.
 */
export const DEFAULT_RETRY: RetryPolicy = {
  attempts: 6,
  first: 1_000,
  max: 30_000,
  factor: 2,
};

/** The wait before the given attempt, counting the first attempt as 0. */
export function delayFor(policy: RetryPolicy, attempt: number): number {
  if (attempt <= 0) return 0;
  const grown = policy.first * Math.pow(policy.factor, attempt - 1);
  return Math.min(policy.max, grown);
}

/** Raised when every attempt has been spent. Carries what actually failed. */
export class GaveUp extends Error {
  constructor(
    readonly attempts: number,
    readonly cause: unknown,
  ) {
    super(`gave up after ${attempts} attempts`);
    this.name = 'GaveUp';
  }
}

/** What the caller is told while it waits, so the interface can say so. */
export interface Waiting {
  /** Which attempt is about to be made, counting from 1. */
  attempt: number;
  /** How long until it is, in milliseconds. */
  delay: number;
  /** What went wrong last time. */
  cause: unknown;
}

export interface ResumeOptions {
  policy?: RetryPolicy;
  /** Cancels the whole thing, including a wait in progress. */
  signal?: AbortSignal;
  /** Called before each wait, for "reconnecting in 4 s". */
  onWaiting?: (w: Waiting) => void;
  /** Injected in tests. Real code uses the browser's timer and online event. */
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

/**
 * Runs an operation, retrying it while the failures look like the network.
 *
 * The operation must be safe to run again from the start, which is the whole
 * reason the upload asks for the committed offset first.
 */
export async function withResume<T>(
  operation: () => Promise<T>,
  options: ResumeOptions = {},
): Promise<T> {
  const policy = options.policy ?? DEFAULT_RETRY;
  const sleep = options.sleep ?? waitFor;

  let last: unknown;
  for (let attempt = 0; attempt < policy.attempts; attempt++) {
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('aborted');

    if (attempt > 0) {
      const delay = delayFor(policy, attempt);
      options.onWaiting?.({ attempt: attempt + 1, delay, cause: last });
      await sleep(delay, options.signal);
    }

    try {
      return await operation();
    } catch (failure) {
      // Anything that will answer the same way forever is reported now rather
      // than five attempts from now.
      if (!isTransient(failure)) throw failure;
      last = failure;
    }
  }

  throw new GaveUp(policy.attempts, last);
}

/**
 * Whether a failure is worth trying again.
 *
 * Errors are matched by name and shape rather than by class, because they cross
 * three boundaries on the way here: `fetch` raises a bare `TypeError` for every
 * network failure, the server answers with a catalogue code, and an abort
 * arrives as a `DOMException`. Nothing in the middle preserves a class.
 */
export function isTransient(failure: unknown): boolean {
  if (failure === null || typeof failure !== 'object') return false;

  const named = failure as {
    name?: string;
    status?: number;
    /** ApiFailure carries the catalogue code one level down. */
    body?: { error?: string };
    error?: string;
  };
  const code = named.body?.error ?? named.error;

  // A cancellation is a decision, not an interruption. Retrying one would
  // restart what the user just stopped.
  if (named.name === 'AbortError' || named.name === 'UploadCancelled') return false;

  // fetch rejects with TypeError, and only with TypeError, when the request
  // never completed: no connection, DNS gone, the Wi-Fi dropped mid-body. It is
  // the single most common interruption on a phone and it carries no detail at
  // all, which is why the name is all there is to match on.
  if (named.name === 'TypeError') return true;

  // `queue_busy`: another transfer holds the single active slot (FR-035a), and
  // waiting is exactly the right response — it is a queue, not a refusal.
  // `internal`: the server had a problem with this attempt, not with this
  // transfer. Everything else, from a refused credential to a corrupt file,
  // answers the same way forever.
  if (code === 'queue_busy' || code === 'internal') return true;

  if (typeof named.status === 'number') {
    return named.status === 429 || (named.status >= 500 && named.status < 600);
  }
  return false;
}

/**
 * Waits, and waits for the network to come back if it is known to be away.
 *
 * `navigator.onLine` is unreliable as a signal that a connection *works*, but
 * a false is trustworthy in the direction that matters here: the device knows
 * it has no network. Sleeping the full delay in that state wastes the attempt,
 * so the wait ends on whichever comes first, the timer or the network.
 */
function waitFor(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const done = (): void => {
      clearTimeout(timer);
      globalThis.removeEventListener?.('online', done);
      signal?.removeEventListener('abort', cancelled);
      resolve();
    };
    const cancelled = (): void => {
      clearTimeout(timer);
      globalThis.removeEventListener?.('online', done);
      reject(signal?.reason ?? new Error('aborted'));
    };

    const timer = setTimeout(done, ms);

    if (typeof navigator !== 'undefined' && navigator.onLine === false) {
      globalThis.addEventListener?.('online', done, { once: true });
    }
    signal?.addEventListener('abort', cancelled, { once: true });
  });
}
