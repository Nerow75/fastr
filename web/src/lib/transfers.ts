/**
 * The sending side of the pipe, in the browser.
 *
 * The desktop page holds the File objects the user dropped. When the receiver
 * starts fetching, the server says which item and from which offset, and this
 * supplies exactly that slice.
 *
 * `fetch` with a Blob body streams from disk without reading the file into
 * memory, which is what makes a 10 GB send possible from a browser tab. Slicing
 * is what makes resuming possible: `file.slice(offset)` costs nothing and reads
 * nothing until the request needs bytes.
 */

import type { Session } from './session.js';

export interface DeclaredItem {
  index: number;
  name: string;
  stored_name: string;
  size: number;
  committed_offset: number;
  state: string;
}

export interface Transfer {
  id: string;
  direction: string;
  source_device_id: string;
  target_device_id: string;
  protection: string;
  state: string;
  failure_cause?: string;
  total_bytes: number;
  transferred_bytes: number;
  items: DeclaredItem[];
}

/**
 * Holds the files for transfers this page is sending.
 *
 * The map is the reason the tab has to stay open: nothing else in the system
 * can reach the bytes. Closing the page is a legitimate way to abandon a
 * transfer, and the receiver is told rather than left waiting.
 */
export class Sender {
  private readonly files = new Map<string, File[]>();
  private readonly inFlight = new Set<string>();

  constructor(private readonly session: Session) {}

  /** Declares a transfer and remembers its files. */
  async send(targetDeviceId: string, files: File[]): Promise<Transfer> {
    const transfer = await this.session.request<Transfer>('POST', '/api/transfers', {
      target_device_id: targetDeviceId,
      items: files.map((file) => ({
        name: file.name,
        // webkitRelativePath is set when a whole folder was chosen, and empty
        // otherwise. It is what preserves the structure on the far side.
        relative_path: directoryOf(file),
        size: file.size,
      })),
    });

    this.files.set(transfer.id, files);
    return transfer;
  }

  /** Whether this page is holding the files for a transfer. */
  holds(transferId: string): boolean {
    return this.files.has(transferId);
  }

  /** Forgets a finished transfer, releasing its file handles. */
  release(transferId: string): void {
    this.files.delete(transferId);
  }

  /**
   * Supplies one item from an offset, in response to a receiver fetching.
   *
   * Concurrent calls for the same item are ignored rather than queued: a
   * duplicate event, or a reconnect that arrives twice, must not open two
   * uploads for one demand.
   */
  async supply(transferId: string, itemIndex: number, offset: number): Promise<void> {
    const key = `${transferId}#${itemIndex}`;
    if (this.inFlight.has(key)) return;

    const files = this.files.get(transferId);
    const file = files?.[itemIndex];
    if (!file) return; // not ours, or already released

    this.inFlight.add(key);
    try {
      // slice() returns a view, not a copy. The body streams from disk.
      const body = offset > 0 ? file.slice(offset) : file;

      const response = await fetch(
        `/api/transfers/${encodeURIComponent(transferId)}` +
          `/items/${itemIndex}/supply?offset=${offset}`,
        { method: 'POST', body },
      );

      if (!response.ok) {
        // The receiver went away, or asked for a different offset. Either way
        // the next demand carries the correct one, so there is nothing to fix
        // here beyond not retrying blindly.
        return;
      }
    } finally {
      this.inFlight.delete(key);
    }
  }

  /**
   * Asks which items are waiting for content.
   *
   * Used after a reconnect: a page that missed the event announcing a demand
   * would otherwise leave the receiver hanging until the supply timeout.
   */
  async resumeWaiting(transferId: string): Promise<void> {
    if (!this.holds(transferId)) return;

    const { waiting } = await this.session.request<{
      waiting: { item: number; offset: number }[];
    }>('GET', `/api/transfers/${encodeURIComponent(transferId)}/waiting`);

    for (const demand of waiting) {
      void this.supply(transferId, demand.item, demand.offset);
    }
  }

  /** Stops a transfer from this side. */
  async cancel(transferId: string): Promise<void> {
    await this.session.request('POST', `/api/transfers/${encodeURIComponent(transferId)}/cancel`, {});
    this.release(transferId);
  }
}

/** The directory part of a folder selection, or empty for a loose file. */
function directoryOf(file: File): string {
  const full = (file as File & { webkitRelativePath?: string }).webkitRelativePath;
  if (!full) return '';
  const cut = full.lastIndexOf('/');
  return cut > 0 ? full.slice(0, cut) : '';
}

/**
 * Builds the URL a receiver opens to save an item.
 *
 * A plain link, not a fetch: the browser's own download manager writes it to
 * disk, which is the only mechanism that handles a multi-gigabyte file on a
 * phone without holding it in memory. It cannot set a header, so the URL
 * carries a ticket scoped to this one file. See internal/httpapi/tickets.go.
 */
export async function downloadURL(
  session: Session,
  transferId: string,
  itemIndex: number,
): Promise<string> {
  const { ticket } = await session.request<{ ticket: string }>(
    'POST',
    `/api/transfers/${encodeURIComponent(transferId)}/items/${itemIndex}/ticket`,
    {},
  );
  return (
    `/api/transfers/${encodeURIComponent(transferId)}/items/${itemIndex}` +
    `/content?ticket=${encodeURIComponent(ticket)}`
  );
}
