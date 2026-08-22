/**
 * The sending side on the phone.
 *
 * The desktop cannot be fetched from by a phone, and a phone cannot be fetched
 * from at all, so this direction is neither of the two the desktop uses: the
 * phone pushes chunks, and each reply is a promise that those bytes are durably
 * on the computer's disk.
 *
 * Three things shape the whole file.
 *
 * **Chunks, not one request.** A phone locks its screen, drops Wi-Fi, and
 * backgrounds tabs. A single 10 GB POST would lose everything each time; a
 * chunk loses at most a chunk, and the offset endpoint says exactly where to
 * pick up. That is what makes FR-031 true from this side.
 *
 * **Memory does not grow with the file.** Only one chunk is ever held. The
 * hash has to see the bytes, so unlike the desktop's `fetch(blob)` — which
 * streams from disk without reading — a chunk is read into memory here. Which
 * is precisely why CHUNK_BYTES is small enough that an old phone can afford it,
 * whatever the file size. Constitution Principle III.
 *
 * **The digest is computed while reading, once.** BLAKE2b over the whole file,
 * fed chunk by chunk, so the file is read once rather than twice. The server
 * verifies against what it actually wrote before moving it out of staging
 * (FR-032), which is what makes a corrupted transfer fail rather than succeed
 * quietly.
 */

import { blake2b } from '@noble/hashes/blake2';
import { toBase64 } from '../crypto/envelope.js';
import { ApiFailure, type Session } from './session.js';
import type { ApiError } from './i18n.js';
import type { Transfer } from './transfers.js';
import { withResume, type ResumeOptions, type Waiting } from './resume.js';

/**
 * How much is sent per request.
 *
 * 4 MB is a compromise between two costs that pull in opposite directions: a
 * chunk is held in memory whole, so it bounds the footprint on the oldest phone
 * that has to work; and every chunk costs a round trip, so tiny chunks throttle
 * a gigabit link with latency. It is also the granularity of what is lost when
 * the screen locks mid-request.
 */
export const CHUNK_BYTES = 4 << 20;

/** BLAKE2b-256, matching internal/transfer/stream.go. */
const DIGEST_BYTES = 32;

export interface UploadProgress {
  transferId: string;
  itemIndex: number;
  /** Bytes of this item durably on the computer. */
  transferred: number;
  /** This item's total size. */
  total: number;
  /** Bytes per second over the recent window, or 0 before there is one. */
  rate: number;
  /** Seconds remaining, or -1 when it cannot be estimated yet. */
  remaining: number;
}

/** Raised when a transfer was stopped from this page. */
export class UploadCancelled extends Error {
  constructor() {
    super('the upload was cancelled');
    this.name = 'UploadCancelled';
  }
}

interface OffsetReply {
  item: number;
  committed_offset: number;
  size: number;
}

/**
 * Sends files from this phone to a computer.
 *
 * One item at a time, and one transfer at a time: the server allows a single
 * active transfer (FR-035a), and doing otherwise here would only mean queuing
 * against ourselves at higher memory cost.
 */
export class Uploader {
  private readonly aborts = new Map<string, AbortController>();

  constructor(private readonly session: Session) {}

  /** Declares a transfer. The server answers with the names it will use, which
   *  may differ from what was asked for. */
  async declare(targetDeviceId: string, files: File[]): Promise<Transfer> {
    return this.session.request<Transfer>('POST', '/api/transfers', {
      target_device_id: targetDeviceId,
      items: files.map((file) => ({
        name: file.name,
        relative_path: directoryOf(file),
        size: file.size,
      })),
    });
  }

  /**
   * Uploads every item of a declared transfer, in order.
   *
   * Resumable by construction: it asks the server where each item stands before
   * sending anything, so calling it again after an interruption continues
   * rather than restarts.
   */
  async run(
    transfer: Transfer,
    files: File[],
    onProgress?: (p: UploadProgress) => void,
    onWaiting?: (w: Waiting) => void,
  ): Promise<void> {
    const abort = new AbortController();
    this.aborts.set(transfer.id, abort);

    try {
      for (const item of transfer.items) {
        const file = files[item.index];
        if (!file) throw new Error(`no file for item ${item.index}`);

        // Retrying an item *is* resuming it: sendItem asks the computer where
        // it stands before sending anything, so a second attempt continues from
        // the committed offset rather than starting the file again. That is why
        // the whole item can be wrapped rather than each chunk.
        const options: ResumeOptions = { signal: abort.signal, onWaiting };
        await withResume(
          () => this.sendItem(transfer.id, item.index, file, abort.signal, onProgress),
          options,
        );
      }
    } finally {
      this.aborts.delete(transfer.id);
    }
  }

  /** Stops an upload from this side and tells the computer. */
  async cancel(transferId: string): Promise<void> {
    this.aborts.get(transferId)?.abort();
    this.aborts.delete(transferId);

    await this.session.request(
      'POST',
      `/api/transfers/${encodeURIComponent(transferId)}/cancel`,
      {},
    );
  }

  /** Whether this page is currently uploading a transfer. */
  running(transferId: string): boolean {
    return this.aborts.has(transferId);
  }

  private async sendItem(
    transferId: string,
    index: number,
    file: File,
    signal: AbortSignal,
    onProgress?: (p: UploadProgress) => void,
  ): Promise<void> {
    // Where the computer says it stands. Asked rather than assumed: this page
    // may be a reload, or a second attempt after the screen locked.
    let offset = await this.serverOffset(transferId, index);

    let hasher = blake2b.create({ dkLen: DIGEST_BYTES });

    // The hash state did not survive whatever interrupted us, so the prefix the
    // computer already holds has to be read again to rebuild it. It costs one
    // pass over bytes that are not re-sent, which is the price of resuming at
    // all; the server pays the same price on its side.
    if (offset > 0) await hashPrefix(hasher, file, offset, signal);

    const rate = new RateEstimate(offset);

    while (offset < file.size) {
      throwIfAborted(signal);

      const end = Math.min(offset + CHUNK_BYTES, file.size);
      const chunk = new Uint8Array(await file.slice(offset, end).arrayBuffer());

      const reply = await this.postChunk(transferId, index, offset, chunk, signal);

      if (reply.corrected !== undefined) {
        // The computer holds a different amount than we believed: another tab,
        // or a reply we never saw. Its number is the truth — it is the one
        // backed by bytes on a disk — so rewind, rebuild the hash, and continue
        // rather than writing a hole.
        offset = reply.corrected;
        hasher = blake2b.create({ dkLen: DIGEST_BYTES });
        if (offset > 0) await hashPrefix(hasher, file, offset, signal);
        continue;
      }

      // Hashed only once the bytes are acknowledged, so a chunk that has to be
      // sent again is not counted twice.
      hasher.update(chunk);
      offset = reply.committed;

      onProgress?.({
        transferId,
        itemIndex: index,
        transferred: offset,
        total: file.size,
        ...rate.sample(offset, file.size),
      });
    }

    await this.session.request(
      'POST',
      `/api/transfers/${encodeURIComponent(transferId)}/items/${index}/complete`,
      { checksum: toBase64(hasher.digest()) },
    );
  }

  /** Asks where to resume from. */
  private async serverOffset(transferId: string, index: number): Promise<number> {
    const reply = await this.session.request<OffsetReply>(
      'GET',
      `/api/transfers/${encodeURIComponent(transferId)}/items/${index}/offset`,
    );
    return reply.committed_offset;
  }

  /**
   * Sends one chunk.
   *
   * Not through Session.request: a content route carries raw octets and is not
   * sealed, exactly like the desktop's supply route. Constitution v2.0.1 is
   * explicit that file content travels in the clear in simple mode, and the
   * interface says so rather than implying otherwise.
   */
  private async postChunk(
    transferId: string,
    index: number,
    offset: number,
    chunk: Uint8Array,
    signal: AbortSignal,
  ): Promise<{ committed: number; corrected?: number }> {
    const response = await fetch(
      `/api/transfers/${encodeURIComponent(transferId)}/items/${index}/content?offset=${offset}`,
      {
        method: 'POST',
        headers: {
          Authorization: this.session.authorization(),
          'Content-Type': 'application/octet-stream',
        },
        body: chunk as BodyInit,
        signal,
      },
    );

    if (response.ok) {
      const body = (await response.json()) as { committed_offset: number };
      return { committed: body.committed_offset };
    }

    const failure = (await response.json()) as ApiError;

    if (failure.error === 'offset_mismatch') {
      const corrected = failure.params?.committed_offset;
      if (typeof corrected === 'number') return { committed: offset, corrected };
    }

    // Thrown as an ApiFailure rather than a plain Error so the retry policy can
    // read the catalogue code: a busy queue is worth waiting for and a refused
    // credential is not, and a formatted message hides which one this is.
    throw new ApiFailure(response.status, failure);
  }
}

/** Feeds the first `length` bytes of a file into a hasher, in chunks. */
async function hashPrefix(
  hasher: { update(data: Uint8Array): unknown },
  file: File,
  length: number,
  signal: AbortSignal,
): Promise<void> {
  for (let at = 0; at < length; at += CHUNK_BYTES) {
    throwIfAborted(signal);
    const end = Math.min(at + CHUNK_BYTES, length);
    hasher.update(new Uint8Array(await file.slice(at, end).arrayBuffer()));
  }
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw new UploadCancelled();
}

/**
 * Turns a byte count into a rate and an estimate.
 *
 * Measured over a sliding window rather than since the start, so a transfer
 * that slowed down says so instead of averaging the bad news away. Mirrors
 * rateReporter in internal/transfer/stream.go.
 */
class RateEstimate {
  private lastAt = performance.now();

  constructor(private lastBytes: number) {}

  sample(transferred: number, total: number): { rate: number; remaining: number } {
    const now = performance.now();
    const elapsed = (now - this.lastAt) / 1000;

    if (elapsed <= 0) return { rate: 0, remaining: -1 };

    const rate = (transferred - this.lastBytes) / elapsed;
    this.lastAt = now;
    this.lastBytes = transferred;

    const remaining = rate > 0 && total > transferred ? (total - transferred) / rate : -1;
    return { rate, remaining };
  }
}

/** The directory part of a folder selection, or empty for a loose file. */
function directoryOf(file: File): string {
  const full = (file as File & { webkitRelativePath?: string }).webkitRelativePath;
  if (!full) return '';
  const cut = full.lastIndexOf('/');
  return cut > 0 ? full.slice(0, cut) : '';
}
