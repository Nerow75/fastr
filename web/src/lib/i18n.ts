/**
 * Translation for the web application.
 *
 * Catalogues are bundled, not fetched: Principle I forbids any request leaving
 * the local network, and a translation file fetched at runtime would also mean
 * an untranslated first paint on a slow phone.
 *
 * A missing key falls back to English, and a key missing everywhere renders as
 * itself so the gap is visible in development rather than leaving a hole in a
 * sentence (FR-039d).
 */

import en from '../../../internal/i18n/locales/en.json';
import fr from '../../../internal/i18n/locales/fr.json';

export type Lang = 'en' | 'fr';

const catalogues: Record<Lang, Record<string, string>> = { en, fr };
const FALLBACK: Lang = 'en';

export const AVAILABLE_LANGUAGES: Lang[] = ['en', 'fr'];

/** Reduces "fr-CA" to "fr". Regional catalogues can be added later. */
function normalize(tag: string): string {
  const lower = tag.toLowerCase().trim();
  const sep = lower.search(/[-_]/);
  return sep > 0 ? lower.slice(0, sep) : lower;
}

function isAvailable(tag: string): tag is Lang {
  return (AVAILABLE_LANGUAGES as string[]).includes(tag);
}

/**
 * Picks a language from the device's preferences, honouring an explicit user
 * override. FR-039b: a stated choice outranks the device.
 */
export function negotiate(override?: string | null): Lang {
  if (override) {
    const wanted = normalize(override);
    if (isAvailable(wanted)) return wanted;
  }
  for (const tag of navigator.languages ?? [navigator.language]) {
    const candidate = normalize(tag);
    if (isAvailable(candidate)) return candidate;
  }
  return FALLBACK;
}

let current: Lang = FALLBACK;

export function setLanguage(lang: Lang): void {
  current = lang;
  document.documentElement.lang = lang;
}

export function getLanguage(): Lang {
  return current;
}

/** Replaces {name} placeholders. An unmatched one is left alone, so a
 *  translator's typo is visible rather than producing a gap. */
function interpolate(message: string, params?: Record<string, unknown>): string {
  if (!params || !message.includes('{')) return message;
  return message.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole,
  );
}

/** Translates a key in the current language. */
export function t(key: string, params?: Record<string, unknown>): string {
  const message = catalogues[current][key] ?? catalogues[FALLBACK][key];
  if (message === undefined) {
    // Visible in development, and never a blank in production.
    if (import.meta.env.DEV) console.warn(`missing translation: ${key}`);
    return key;
  }
  return interpolate(message, params);
}

/**
 * Locale-aware formatting. FR-039c requires sizes, dates, times, and speeds to
 * follow the selected language, which means using the platform's formatter
 * rather than a hand-rolled table.
 */

/** Formats a byte count, for example "1.4 GB" or "1,4 Go". */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return new Intl.NumberFormat(current, {
    style: 'unit',
    unit: pickByteUnit(bytes),
    unitDisplay: 'short',
    maximumFractionDigits: bytes < 1024 ** 2 ? 0 : 1,
  }).format(scaleBytes(bytes));
}

type ByteUnit = 'byte' | 'kilobyte' | 'megabyte' | 'gigabyte' | 'terabyte';

function pickByteUnit(bytes: number): ByteUnit {
  if (bytes < 1024) return 'byte';
  if (bytes < 1024 ** 2) return 'kilobyte';
  if (bytes < 1024 ** 3) return 'megabyte';
  if (bytes < 1024 ** 4) return 'gigabyte';
  return 'terabyte';
}

function scaleBytes(bytes: number): number {
  if (bytes < 1024) return bytes;
  if (bytes < 1024 ** 2) return bytes / 1024;
  if (bytes < 1024 ** 3) return bytes / 1024 ** 2;
  if (bytes < 1024 ** 4) return bytes / 1024 ** 3;
  return bytes / 1024 ** 4;
}

/** Formats a transfer rate, for example "12.4 MB". The caller appends the
 *  "per second" wording from the catalogue, because that phrasing differs by
 *  language in ways a unit formatter cannot express. */
export function formatRate(bytesPerSecond: number): string {
  return formatBytes(bytesPerSecond);
}

/** Formats a remaining duration in whole units, for example "3 min". */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';

  const [unit, value, digits]: ['second' | 'minute' | 'hour', number, number] =
    seconds < 60
      ? ['second', seconds, 0]
      : seconds < 3600
        ? ['minute', seconds / 60, 0]
        : ['hour', seconds / 3600, 1];

  return new Intl.NumberFormat(current, {
    style: 'unit',
    unit,
    unitDisplay: 'short',
    maximumFractionDigits: digits,
  }).format(value);
}

/** Formats an absolute date and time for the history view. */
export function formatDateTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(current, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

/**
 * Renders a server error.
 *
 * The API returns a stable code, a translation key, and parameters, never an
 * assembled message (contracts/http-api.md). This is where they become a
 * sentence, in the reader's language, carrying a corrective action.
 */
export interface ApiError {
  error: string;
  detail_key: string;
  params?: Record<string, unknown>;
}

export function formatApiError(err: ApiError): string {
  const params = { ...err.params };
  // Byte counts arrive as numbers and must be shown in the reader's units.
  for (const key of ['needed', 'available', 'size', 'total']) {
    if (typeof params[key] === 'number') {
      params[key] = formatBytes(params[key] as number);
    }
  }
  return t(err.detail_key, params);
}
