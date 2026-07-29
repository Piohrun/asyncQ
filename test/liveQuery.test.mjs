import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import {
  LIVE_CHANNEL_ENTROPY_BYTES,
  LIVE_ID_MAX_BYTES,
  LIVE_MAX_DATA_POINTS,
  LIVE_MAX_INTERVAL_MS,
  LIVE_MAX_RETENTION_MS,
  LIVE_MAX_ROWS,
  effectiveExecutionMode,
  liveChannelID,
  livePathFamily,
  newLiveChannel,
  normalizeLiveBounds,
  openLiveChannelSession,
} from '../src/liveQuery.ts';

test('effective execution mode and path family stay explicit', () => {
  assert.equal(effectiveExecutionMode('pluginAsync', 'stream'), 'pluginAsync');
  assert.equal(effectiveExecutionMode('', 'legacyAsync'), 'legacyAsync');
  assert.equal(effectiveExecutionMode(undefined, undefined), 'sync');
  assert.equal(livePathFamily('stream'), 'stream');
  assert.equal(livePathFamily('pluginAsync'), 'async');
});

test('live bounds reject non-finite values and clamp hostile integers', () => {
  assert.deepEqual(
    normalizeLiveBounds({
      maxRows: Number.NaN,
      fallbackMaxRows: Number.POSITIVE_INFINITY,
      retentionMs: -1,
      maxDataPoints: LIVE_MAX_DATA_POINTS + 500,
      intervalMs: Number.NEGATIVE_INFINITY,
    }),
    {
      maxRows: 1000,
      retentionMs: 0,
      maxDataPoints: LIVE_MAX_DATA_POINTS,
      intervalMs: 0,
    }
  );
  assert.deepEqual(
    normalizeLiveBounds({
      maxRows: LIVE_MAX_ROWS + 1,
      fallbackMaxRows: 42,
      retentionMs: LIVE_MAX_RETENTION_MS + 1,
      maxDataPoints: -1,
      intervalMs: LIVE_MAX_INTERVAL_MS + 1,
    }),
    {
      maxRows: LIVE_MAX_ROWS,
      retentionMs: LIVE_MAX_RETENTION_MS,
      maxDataPoints: 0,
      intervalMs: LIVE_MAX_INTERVAL_MS,
    }
  );
  assert.equal(
    normalizeLiveBounds({
      maxRows: 12.9,
      fallbackMaxRows: 1,
      retentionMs: 1.9,
      maxDataPoints: 2.9,
      intervalMs: 3.9,
    }).maxRows,
    12
  );
});

test('live row fallbacks preserve legacy default and positive-bound semantics', () => {
  const rows = (maxRows, fallbackMaxRows) =>
    normalizeLiveBounds({
      maxRows,
      fallbackMaxRows,
      retentionMs: 0,
      maxDataPoints: 0,
      intervalMs: 0,
    }).maxRows;

  assert.equal(rows(undefined, undefined), 1000);
  assert.equal(rows(undefined, 0), 1000);
  assert.equal(rows(undefined, -1), 1000);
  assert.equal(rows(undefined, Number.NaN), 1000);
  assert.equal(rows(undefined, 1), 1);
  assert.equal(rows(undefined, LIVE_MAX_ROWS), LIVE_MAX_ROWS);
  assert.equal(rows(undefined, LIVE_MAX_ROWS + 1), LIVE_MAX_ROWS);

  assert.equal(rows(0, 42), 42);
  assert.equal(rows(-1, 42), 42);
  assert.equal(rows(1, 42), 1);
  assert.equal(rows(LIVE_MAX_ROWS, 42), LIVE_MAX_ROWS);
  assert.equal(rows(LIVE_MAX_ROWS + 1, 42), LIVE_MAX_ROWS);
});

test('live channel IDs use only backend-safe unreserved ASCII', () => {
  const id = liveChannelID('../unsafe/path=value snow', '/suffix=bad?fragment');
  assert.match(id, /^[A-Za-z0-9._~-]+$/);
  assert.ok(id.length > 0);
  assert.ok(id.length <= LIVE_ID_MAX_BYTES);
  assert.ok(!id.includes('/'));
  assert.ok(!id.includes('='));

  const bounded = liveChannelID('x'.repeat(500), 'y'.repeat(500));
  assert.equal(bounded.length, LIVE_ID_MAX_BYTES);
  assert.match(bounded, /^[A-Za-z0-9._~-]+$/);
  assert.equal(liveChannelID('', ''), 'channel-id');
});

test('new live channels request and encode exactly 128 bits of entropy', () => {
  const requestedLengths = [];
  const channel = newLiveChannel('async', 'panel/A', (byteLength) => {
    requestedLengths.push(byteLength);
    return Uint8Array.from({ length: byteLength }, (_value, index) => index);
  });

  const secret = '000102030405060708090a0b0c0d0e0f';
  assert.deepEqual(requestedLengths, [LIVE_CHANNEL_ENTROPY_BYTES]);
  assert.equal(LIVE_CHANNEL_ENTROPY_BYTES, 16);
  assert.equal(channel.liveID, `panel-A-${secret}`);
  assert.equal(channel.path, `async/panel-A-${secret}`);
});

test('new async channels and independent stream requests never share channel identity', () => {
  let sequence = 0;
  const entropySource = (byteLength) => new Uint8Array(byteLength).fill(++sequence);
  const open = (channel) => ({ ...channel, opened: true });

  const firstAsync = openLiveChannelSession(undefined, false, 'async', 'request-A', open, entropySource);
  const secondAsync = openLiveChannelSession(undefined, false, 'async', 'request-A', open, entropySource);
  const firstStream = openLiveChannelSession(undefined, false, 'stream', 'stream-A', open, entropySource);
  const differentRequest = openLiveChannelSession(undefined, false, 'stream', 'stream-B', open, entropySource);

  assert.notEqual(firstAsync.liveID, secondAsync.liveID);
  assert.notEqual(firstStream.liveID, differentRequest.liveID);
  assert.notEqual(firstStream.path, differentRequest.path);
  assert.equal(sequence, 4);
});

test('an active stream session reuses its stored secret while an expired session rotates it', () => {
  let entropyCalls = 0;
  let openCalls = 0;
  const entropySource = (byteLength) => new Uint8Array(byteLength).fill(++entropyCalls);
  const open = (channel) => {
    openCalls++;
    return { ...channel, session: openCalls };
  };

  const first = openLiveChannelSession(undefined, false, 'stream', 'stream-A', open, entropySource);
  const reused = openLiveChannelSession(first, true, 'stream', 'stream-A', open, entropySource);
  assert.strictEqual(reused, first);
  assert.equal(reused.liveID, first.liveID);
  assert.equal(reused.path, first.path);
  assert.equal(entropyCalls, 1);
  assert.equal(openCalls, 1);

  const replacement = openLiveChannelSession(first, false, 'stream', 'stream-A', open, entropySource);
  assert.notEqual(replacement.liveID, first.liveID);
  assert.notEqual(replacement.path, first.path);
  assert.equal(entropyCalls, 2);
  assert.equal(openCalls, 2);
});

test('entropy failures fail closed before a live stream is opened', () => {
  const invalidSources = [
    () => {
      throw new Error('crypto unavailable');
    },
    () => new Uint8Array(LIVE_CHANNEL_ENTROPY_BYTES - 1),
    () => undefined,
  ];
  let getStreamCalls = 0;

  for (const entropySource of invalidSources) {
    assert.throws(
      () =>
        openLiveChannelSession(
          undefined,
          false,
          'async',
          'request-A',
          (channel) => {
            getStreamCalls++;
            return channel;
          },
          entropySource
        ),
      /Secure Grafana Live channel entropy/
    );
  }
  assert.equal(getStreamCalls, 0);
});

test('live channel setup has no time or pseudo-random fallback', () => {
  const source = [
    readFileSync(new URL('../src/liveQuery.ts', import.meta.url), 'utf8'),
    readFileSync(new URL('../src/datasource.ts', import.meta.url), 'utf8'),
  ].join('\n');
  assert.doesNotMatch(source, /Math\.random/);
  assert.doesNotMatch(source, /Date\.now/);
});
