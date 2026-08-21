/**
 * The API client: credential storage, the sealed envelope, and the pairing
 * flow.
 *
 * A device's credential lives in the browser's site data. Clearing that data,
 * or connecting from a private window, loses the pairing and requires pairing
 * again. That is stated as an assumption in the spec rather than worked around,
 * because the alternatives all involve storing a credential somewhere the user
 * did not ask for.
 */

import { Envelope, ClientToServer, toBase64, fromBase64, ReplayError } from '../crypto/envelope.js';
import { derive, generateKeypair } from '../crypto/handshake.js';
import type { ApiError } from './i18n.js';

const STORAGE_KEY = 'fastr.session.v1';
const COUNTER_KEY = 'fastr.counter.v1';

/**
 * How many envelope counters each page load claims for itself.
 *
 * The server refuses any counter it has already seen, and remembers the highest
 * one per device for as long as it runs. A page that started again at zero after
 * a reload therefore had every request rejected as a replay, and the session
 * stayed dead until the server was restarted — a reload being, on a phone, one
 * of the most ordinary things that can happen.
 *
 * So each load reserves a block up front rather than counting from zero. Two
 * tabs get disjoint blocks because reserving is a read-then-write done once, at
 * construction, rather than on every request. The counter is 64 bits wide: at a
 * million per load this allows more page loads than the machine will ever see.
 */
const COUNTER_BLOCK = 1_000_000n;

/**
 * Claims the next block of counters and records where the following one starts.
 *
 * A failure to persist is not fatal. The block is still used, so this page
 * works; only a later page could collide, and it would collide with a counter
 * the server has already seen and refuse it, which is loud rather than silent.
 */
function reserveCounterBlock(): bigint {
  let start = 0n;
  try {
    start = BigInt(localStorage.getItem(COUNTER_KEY) ?? '0');
    if (start < 0n) start = 0n;
  } catch {
    start = 0n; // absent, or left unparseable by an older build
  }

  try {
    localStorage.setItem(COUNTER_KEY, String(start + COUNTER_BLOCK));
  } catch {
    // Storage full or blocked. Better to run with a block that may repeat than
    // to refuse to open the page.
  }
  return start;
}

interface StoredSession {
  credential: string;
  deviceId: string;
  /** The session key, base64. It is a shared secret with this computer and
   *  nothing else, and it never leaves this origin. */
  key: string;
}

export class ApiFailure extends Error {
  readonly body: ApiError;
  readonly status: number;

  constructor(status: number, body: ApiError) {
    super(body.error);
    this.name = 'ApiFailure';
    this.status = status;
    this.body = body;
  }
}

export class Session {
  private readonly credential: string;
  readonly deviceId: string;
  private readonly key: Uint8Array;
  private envelope: Envelope;

  private constructor(stored: StoredSession) {
    this.credential = stored.credential;
    this.deviceId = stored.deviceId;
    this.key = fromBase64(stored.key);
    // Resumes past every counter an earlier page load could have used, rather
    // than starting again at zero and being refused as a replay.
    this.envelope = new Envelope(this.key, ClientToServer, reserveCounterBlock());
  }

  /** Restores a session from site data, or null when this device is unpaired. */
  static restore(): Session | null {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    try {
      return new Session(JSON.parse(raw) as StoredSession);
    } catch {
      // Corrupt or from an older layout. Pairing again is the honest recovery.
      localStorage.removeItem(STORAGE_KEY);
      return null;
    }
  }

  /** Forgets the pairing on this device. */
  static clear(): void {
    localStorage.removeItem(STORAGE_KEY);
  }

  /**
   * Takes the session the host grants its own page, with no code to type.
   *
   * The computer's page used to pair with itself: read six digits from its own
   * screen, type them into its own form, approve its own request. Nobody found
   * it, so sending *from* the computer was effectively unreachable.
   *
   * Only ever succeeds on loopback, which is the same boundary the approval
   * endpoints rest on: being there means being on this machine. See
   * internal/httpapi/host_session.go for why that is enough.
   */
  static async adoptHost(): Promise<Session> {
    const granted = await postPlain<{ device_id: string; credential: string; key: string }>(
      '/api/pair/host',
      {},
    );

    const stored: StoredSession = {
      credential: granted.credential,
      deviceId: granted.device_id,
      key: granted.key,
    };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(stored));
    return new Session(stored);
  }

  /**
   * Runs the pairing handshake, then waits for a human on the computer.
   *
   * Knowing the code is not access: FR-010 wants someone on the receiving
   * device to say yes. So this submits the proof, then polls until it is
   * answered.
   *
   * The ephemeral private key never leaves this function, and the credential
   * arrives once: the server keeps only its hash.
   *
   * `onWaiting` fires when the request has been queued, so the interface can
   * tell the user to go and approve it rather than appearing to hang.
   */
  static async pair(
    code: string,
    deviceName: string,
    onWaiting?: () => void,
    signal?: AbortSignal,
  ): Promise<Session> {
    const { privateKey, publicKey } = generateKeypair();

    const init = await postPlain<{ handshake_id: string; server_pub: string; salt: string }>(
      '/api/pair/init',
      { client_pub: toBase64(publicKey), device_name: deviceName, platform: detectPlatform() },
    );

    const { key, proof } = derive(
      privateKey,
      fromBase64(init.server_pub),
      publicKey,
      fromBase64(init.salt),
      code,
      init.handshake_id,
    );

    const confirm = await postPlain<{ pending_id: string; state: string }>('/api/pair/confirm', {
      handshake_id: init.handshake_id,
      code,
      proof: toBase64(proof),
      device_name: deviceName,
      platform: detectPlatform(),
    });

    onWaiting?.();

    const approved = await waitForApproval(confirm.pending_id, signal);

    // A throwaway envelope, not the session's. Opening with the session
    // envelope would advance its receive counter, and the first real response
    // would then look like a replay.
    const opener = new Envelope(key, ClientToServer);
    const credential = new TextDecoder().decode(
      opener.open('GET', PAIR_STATUS_PATH, fromBase64(approved.credential)),
    );

    const stored: StoredSession = {
      credential,
      deviceId: approved.device_id,
      key: toBase64(key),
    };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(stored));
    return new Session(stored);
  }

  /**
   * The Authorization header value, for the bulk content routes.
   *
   * Those routes carry raw octets and are not sealed, so they cannot go through
   * `request`, but they still authenticate the same way. The credential itself
   * stays private: this hands out the header, not the secret, so no caller ends
   * up storing or logging it separately.
   */
  authorization(): string {
    return `Bearer ${this.credential}`;
  }

  /**
   * Sends a sealed request and returns the decrypted payload.
   *
   * A refusal for a replayed counter is retried once, from a fresh block. The
   * server keeps one high-water mark per device while every page keeps its own
   * counter, so whichever page is behind — a reload, or a second tab that has
   * been sitting idle while the first did work — has its counter refused
   * through no fault of its own. Jumping past the other page's range recovers
   * in one round trip, and the two leapfrog rather than deadlock.
   *
   * Nothing is weakened by this: the server still refuses every counter it has
   * already seen, which is the property the replay check rests on.
   */
  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    try {
      return await this.attempt<T>(method, path, body);
    } catch (failure) {
      if (!isReplay(failure)) throw failure;

      this.envelope = new Envelope(this.key, ClientToServer, reserveCounterBlock());
      return await this.attempt<T>(method, path, body);
    }
  }

  private async attempt<T>(method: string, path: string, body?: unknown): Promise<T> {
    const init: RequestInit = {
      method,
      headers: { Authorization: `Bearer ${this.credential}` },
    };

    if (body !== undefined) {
      const plain = new TextEncoder().encode(JSON.stringify(body));
      init.body = toBase64(this.envelope.seal(method, path, plain));
    }

    const response = await fetch(path, init);

    if (!response.ok) {
      const failure = (await response.json()) as ApiError;
      // A revoked or expired pairing is terminal for this credential. Dropping
      // it here means the interface offers pairing again rather than looping
      // on requests that can never succeed.
      if (failure.error === 'pairing_revoked' || failure.error === 'pairing_expired') {
        Session.clear();
      }
      throw new ApiFailure(response.status, failure);
    }

    const raw = await response.text();
    if (raw.length === 0) return undefined as T;

    try {
      const plain = this.envelope.open(method, path, fromBase64(raw));
      return JSON.parse(new TextDecoder().decode(plain)) as T;
    } catch (err) {
      if (err instanceof ReplayError) {
        // Two tabs sharing one credential each keep their own counter, so one
        // of them will see the other's replies as out of order. Reloading
        // starts a clean session rather than silently dropping messages.
        throw new ApiFailure(400, {
          error: 'replay_detected',
          detail_key: 'error.replay_detected',
        });
      }
      throw err;
    }
  }
}

/** Whether a failure is the server refusing a counter it has already seen. */
function isReplay(failure: unknown): boolean {
  return failure instanceof ApiFailure && failure.body.error === 'replay_detected';
}

/** Sends an unsealed request, for the pairing endpoints, which by definition
 *  have no session yet. */
async function postPlain<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    throw new ApiFailure(response.status, (await response.json()) as ApiError);
  }
  return (await response.json()) as T;
}

/** Reports the platform for display only. It is never an input to an access
 *  decision, on either side. */
function detectPlatform(): string {
  const ua = navigator.userAgent;
  if (/Android/i.test(ua)) return 'android';
  if (/iPhone|iPad|iPod/i.test(ua)) return 'ios';
  if (/Windows/i.test(ua)) return 'windows';
  if (/Linux/i.test(ua)) return 'linux';
  return 'other';
}

/** Whether this page is the desktop view. Loopback is the desktop; anything
 *  else is a phone that scanned the code. */
export function isDesktop(): boolean {
  return ['localhost', '127.0.0.1', '::1', '[::1]'].includes(window.location.hostname);
}

const PAIR_STATUS_PATH = '/api/pair/status';

/** How often the waiting device asks whether a human has answered. */
const APPROVAL_POLL_MS = 1000;

interface ApprovedPairing {
  credential: string;
  device_id: string;
}

/**
 * Polls until the request is answered.
 *
 * Polling rather than a held connection: the answer may take a minute while
 * someone walks to their computer, and a phone that locks its screen will drop
 * a held request anyway. A poll that misses simply happens again.
 */
async function waitForApproval(pendingId: string, signal?: AbortSignal): Promise<ApprovedPairing> {
  for (;;) {
    if (signal?.aborted) throw new PairingAbandoned();

    const status = await postlessGet<{
      state: string;
      credential?: string;
      device_id?: string;
    }>(`${PAIR_STATUS_PATH}?pending=${encodeURIComponent(pendingId)}`);

    switch (status.state) {
      case 'approved':
        if (!status.credential || !status.device_id) {
          // Approved, but this poll did not carry the credential: another tab
          // collected it. Recovering is not possible, and saying so is better
          // than looping forever.
          throw new PairingAlreadyCollected();
        }
        return { credential: status.credential, device_id: status.device_id };
      case 'rejected':
        throw new PairingRejected();
      case 'expired':
        throw new PairingExpired();
      default:
        await sleep(APPROVAL_POLL_MS, signal);
    }
  }
}

export class PairingRejected extends Error {
  readonly detailKey = 'pairing.rejected';
  constructor() {
    super('the request was refused on the computer');
    this.name = 'PairingRejected';
  }
}

export class PairingExpired extends Error {
  readonly detailKey = 'pairing.expired';
  constructor() {
    super('nobody answered in time');
    this.name = 'PairingExpired';
  }
}

export class PairingAbandoned extends Error {
  readonly detailKey = 'pairing.abandoned';
  constructor() {
    super('the pairing was abandoned');
    this.name = 'PairingAbandoned';
  }
}

export class PairingAlreadyCollected extends Error {
  readonly detailKey = 'pairing.already_collected';
  constructor() {
    super('the credential was already collected');
    this.name = 'PairingAlreadyCollected';
  }
}

async function postlessGet<T>(path: string): Promise<T> {
  const response = await fetch(path);
  if (!response.ok) {
    throw new ApiFailure(response.status, (await response.json()) as ApiError);
  }
  return (await response.json()) as T;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, ms);
    signal?.addEventListener(
      'abort',
      () => {
        window.clearTimeout(timer);
        reject(new PairingAbandoned());
      },
      { once: true },
    );
  });
}
