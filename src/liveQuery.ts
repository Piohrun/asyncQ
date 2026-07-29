import type { ExecutionMode } from './types';

export const LIVE_ID_MAX_BYTES = 128;
export const LIVE_MAX_ROWS = 1_000_000;
export const LIVE_MAX_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
export const LIVE_MAX_DATA_POINTS = 10_000_000;
export const LIVE_MAX_INTERVAL_MS = 365 * 24 * 60 * 60 * 1000;
export const LIVE_CHANNEL_ENTROPY_BYTES = 16;

export type LiveMode = 'async' | 'stream';
export type LiveEntropySource = (byteLength: number) => Uint8Array;

export interface LiveChannelDescriptor {
  liveID: string;
  path: string;
}

export interface LiveBoundsInput {
  maxRows: unknown;
  fallbackMaxRows: unknown;
  retentionMs: unknown;
  maxDataPoints: unknown;
  intervalMs: unknown;
}

export interface NormalizedLiveBounds {
  maxRows: number;
  retentionMs: number;
  maxDataPoints: number;
  intervalMs: number;
}

export function effectiveExecutionMode(
  queryMode: ExecutionMode | undefined,
  datasourceMode: ExecutionMode | undefined
): ExecutionMode {
  if (queryMode) {
    return queryMode;
  }
  if (datasourceMode) {
    return datasourceMode;
  }
  return 'sync';
}

export function livePathFamily(executionMode: string): 'async' | 'stream' {
  return executionMode === 'stream' ? 'stream' : 'async';
}

export function normalizeLiveBounds(input: LiveBoundsInput): NormalizedLiveBounds {
  const fallbackRows = positiveBoundedInteger(input.fallbackMaxRows, 1000, LIVE_MAX_ROWS);
  return {
    maxRows: positiveBoundedInteger(input.maxRows, fallbackRows, LIVE_MAX_ROWS),
    retentionMs: boundedInteger(input.retentionMs, 0, 0, LIVE_MAX_RETENTION_MS),
    maxDataPoints: boundedInteger(input.maxDataPoints, 0, 0, LIVE_MAX_DATA_POINTS),
    intervalMs: boundedInteger(input.intervalMs, 0, 0, LIVE_MAX_INTERVAL_MS),
  };
}

export function liveChannelID(base: unknown, suffix: unknown): string {
  const safeBase = sanitizeLiveIDPart(base, 'channel');
  const safeSuffix = sanitizeLiveIDPart(suffix, 'id');
  if (safeSuffix.length >= LIVE_ID_MAX_BYTES) {
    return safeSuffix.slice(0, LIVE_ID_MAX_BYTES);
  }
  const baseLength = LIVE_ID_MAX_BYTES - safeSuffix.length - 1;
  const prefix = safeBase.slice(0, Math.max(0, baseLength));
  return prefix.length > 0 ? `${prefix}-${safeSuffix}` : safeSuffix;
}

export function browserLiveEntropySource(byteLength: number): Uint8Array {
  if (!Number.isSafeInteger(byteLength) || byteLength <= 0) {
    throw new Error('Secure Grafana Live channel entropy length is invalid');
  }
  const cryptoProvider = globalThis.crypto;
  if (!cryptoProvider || typeof cryptoProvider.getRandomValues !== 'function') {
    throw new Error('Secure Grafana Live channel entropy is unavailable');
  }
  const entropy = new Uint8Array(byteLength);
  cryptoProvider.getRandomValues(entropy);
  return entropy;
}

export function newLiveChannel(
  mode: LiveMode,
  base: unknown,
  entropySource: LiveEntropySource = browserLiveEntropySource
): LiveChannelDescriptor {
  let entropy: Uint8Array;
  try {
    entropy = entropySource(LIVE_CHANNEL_ENTROPY_BYTES);
  } catch (error) {
    throw new Error('Secure Grafana Live channel entropy is unavailable', { cause: error });
  }
  if (!(entropy instanceof Uint8Array) || entropy.byteLength !== LIVE_CHANNEL_ENTROPY_BYTES) {
    throw new Error(`Secure Grafana Live channel entropy must contain exactly ${LIVE_CHANNEL_ENTROPY_BYTES} bytes`);
  }
  const secret = Array.from(entropy, (value) => value.toString(16).padStart(2, '0')).join('');
  const liveID = liveChannelID(base, secret);
  return {
    liveID,
    path: `${mode}/${liveID}`,
  };
}

export function openLiveChannelSession<T extends LiveChannelDescriptor>(
  existing: T | undefined,
  active: boolean,
  mode: LiveMode,
  base: unknown,
  open: (channel: LiveChannelDescriptor) => T,
  entropySource: LiveEntropySource = browserLiveEntropySource
): T {
  if (existing && active) {
    return existing;
  }
  const channel = newLiveChannel(mode, base, entropySource);
  return open(channel);
}

function boundedInteger(value: unknown, fallback: number, minimum: number, maximum: number): number {
  const candidate = typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : fallback;
  return Math.min(maximum, Math.max(minimum, candidate));
}

function positiveBoundedInteger(value: unknown, fallback: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return fallback;
  }
  const candidate = Math.trunc(value);
  if (candidate <= 0) {
    return fallback;
  }
  return Math.min(maximum, candidate);
}

function sanitizeLiveIDPart(value: unknown, fallback: string): string {
  const text = typeof value === 'string' || typeof value === 'number' ? String(value) : '';
  const safe = text.slice(0, LIVE_ID_MAX_BYTES).replace(/[^A-Za-z0-9._~-]/g, '-');
  return safe.length > 0 ? safe : fallback;
}
