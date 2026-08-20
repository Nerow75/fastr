/**
 * The event stream, client side.
 *
 * EventSource rather than a socket: it works outside a secure browser context,
 * which the mobile page always is in simple mode, and it reconnects on its own.
 * What the browser does not do is tell the user it lost the connection, or back
 * off sensibly, so this wraps it.
 */

import { announce } from './a11y.js';
import { t } from './i18n.js';
import type { Session } from './session.js';

export type EventType =
  | 'device_appeared'
  | 'device_lost'
  | 'pairing_pending'
  | 'pairing_changed'
  | 'transfer_queued'
  | 'transfer_started'
  | 'transfer_progress'
  | 'transfer_interrupted'
  | 'transfer_resumed'
  | 'transfer_completed'
  | 'transfer_failed'
  | 'transfer_cancelled'
  | 'queue_changed'
  | 'sweep_removed';

export interface ServerEvent {
  type: EventType;
  device_id?: string;
  transfer_id?: string;
  payload?: Record<string, unknown>;
  /** Whether assistive technology should speak this event. Decided by the
   *  server so FR-039i has one definition rather than two that can drift. */
  announce: boolean;
}

type Listener = (event: ServerEvent) => void;

/** Backoff between reconnection attempts. It caps rather than growing without
 *  bound: the computer is on the same network, so a long outage is unusual and
 *  a user watching a stalled page should not wait minutes for a retry. */
const RECONNECT_DELAYS_MS = [500, 1000, 2000, 5000, 10000];

/** Maps an event to the announcement key. Only the events the server marked as
 *  worth speaking reach here. */
const ANNOUNCEMENT_KEYS: Partial<Record<EventType, string>> = {
  transfer_started: 'a11y.transfer_started',
  transfer_interrupted: 'a11y.transfer_interrupted',
  transfer_resumed: 'a11y.transfer_resumed',
  transfer_completed: 'a11y.transfer_completed',
  transfer_failed: 'a11y.transfer_failed',
};

export class EventStream {
  private source: EventSource | null = null;
  private readonly listeners = new Set<Listener>();
  private attempt = 0;
  private closed = false;
  private reconnectTimer: number | null = null;

  private readonly connectionListeners = new Set<(connected: boolean) => void>();

  constructor(private readonly session: Session) {}

  /** Opens the stream and keeps it open. */
  start(): void {
    this.closed = false;
    void this.connect();
  }

  /** Closes the stream and stops reconnecting. */
  stop(): void {
    this.closed = true;
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.source?.close();
    this.source = null;
  }

  /** Registers an event listener. Returns an unsubscribe. */
  on(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  /** Registers a connection-state listener, so the interface can say plainly
   *  that it lost touch rather than showing stale state as if it were live. */
  onConnectionChange(listener: (connected: boolean) => void): () => void {
    this.connectionListeners.add(listener);
    return () => this.connectionListeners.delete(listener);
  }

  private async connect(): Promise<void> {
    if (this.closed) return;

    // EventSource cannot set a header. Rather than put the session credential
    // in the URL, where it would land in browser history and in any proxy log,
    // spend an authenticated request for a single-use ticket that expires in
    // thirty seconds. See internal/httpapi/tickets.go.
    let ticket: string;
    try {
      const issued = await this.session.request<{ ticket: string }>(
        'POST',
        '/api/events/ticket',
        {},
      );
      ticket = issued.ticket;
    } catch {
      this.notifyConnection(false);
      this.scheduleReconnect();
      return;
    }
    if (this.closed) return;

    const source = new EventSource(`/api/events?ticket=${encodeURIComponent(ticket)}`);
    this.source = source;

    source.onopen = () => {
      this.attempt = 0;
      this.notifyConnection(true);
    };

    source.onerror = () => {
      // EventSource reports errors without saying whether it will retry, so we
      // take over: close it and schedule our own attempt with a known delay.
      source.close();
      this.source = null;
      this.notifyConnection(false);
      this.scheduleReconnect();
    };

    source.onmessage = (message) => this.dispatch(message.data as string);

    // Named events arrive on their own handlers rather than onmessage.
    for (const type of Object.keys(ANNOUNCEMENT_KEYS) as EventType[]) {
      source.addEventListener(type, (message) =>
        this.dispatch((message as MessageEvent).data as string),
      );
    }
    for (const type of [
      'device_appeared',
      'device_lost',
      'pairing_pending',
      'pairing_changed',
      'transfer_queued',
      'transfer_progress',
      'transfer_cancelled',
      'queue_changed',
      'sweep_removed',
    ] as EventType[]) {
      source.addEventListener(type, (message) =>
        this.dispatch((message as MessageEvent).data as string),
      );
    }
  }

  private scheduleReconnect(): void {
    if (this.closed) return;

    const delay = RECONNECT_DELAYS_MS[Math.min(this.attempt, RECONNECT_DELAYS_MS.length - 1)];
    this.attempt += 1;
    this.reconnectTimer = window.setTimeout(() => void this.connect(), delay);
  }

  private dispatch(data: string): void {
    let event: ServerEvent;
    try {
      event = JSON.parse(data) as ServerEvent;
    } catch {
      return; // a malformed frame is not worth tearing the stream down for
    }

    if (event.announce) {
      const key = ANNOUNCEMENT_KEYS[event.type];
      if (key) {
        const name = (event.payload?.name as string | undefined) ?? '';
        announce(t(key, { name }), event.type === 'transfer_failed' ? 'assertive' : 'polite');
      }
    }

    for (const listener of this.listeners) listener(event);
  }

  private notifyConnection(connected: boolean): void {
    for (const listener of this.connectionListeners) listener(connected);
  }
}
