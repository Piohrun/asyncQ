import React, { useEffect, useMemo, useState } from 'react';
import { DataFrame, Field, FieldType, LoadingState, PanelProps } from '@grafana/data';
import { getDataSourceSrv } from '@grafana/runtime';
import { Button } from '@grafana/ui';

import { AsyncQMasterDataOptions, defaultOptions } from './types';

type CacheScope = 'memory' | 'disk' | 'both';

interface DataSummary {
  frame?: DataFrame;
  diagnostics: Record<string, any>;
  rowCount: number;
  fieldCount: number;
  latestTime?: Date;
  freshnessMs?: number;
  datasourceUid?: string;
  cacheKey?: string;
}

interface CacheStatus {
  enabled?: boolean;
  controlEnabled?: boolean;
  memory?: { entries?: number; maxEntries?: number };
  disk?: { enabled?: boolean; entries?: number; bytes?: number; maxBytes?: number; path?: string; error?: string };
}

const rootStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
  gap: 8,
  fontSize: 12,
};

const scrollContentStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflow: 'auto',
  gap: 8,
  paddingRight: 2,
};

const stripStyle: React.CSSProperties = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(110px, 1fr))',
  gap: 8,
};

const metricStyle: React.CSSProperties = {
  border: '1px solid rgba(128,128,128,0.25)',
  borderRadius: 4,
  padding: '6px 8px',
  minWidth: 0,
};

const labelStyle: React.CSSProperties = {
  color: 'var(--text-secondary)',
  fontSize: 11,
  whiteSpace: 'nowrap',
};

const valueStyle: React.CSSProperties = {
  fontWeight: 600,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

const tableWrapStyle: React.CSSProperties = {
  overflow: 'auto',
  minHeight: 0,
  maxHeight: 260,
  border: '1px solid rgba(128,128,128,0.2)',
  borderRadius: 4,
};

const tableStyle: React.CSSProperties = {
  width: '100%',
  borderCollapse: 'collapse',
  tableLayout: 'fixed',
};

const cellStyle: React.CSSProperties = {
  borderBottom: '1px solid rgba(128,128,128,0.18)',
  padding: '4px 6px',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

const buttonRowStyle: React.CSSProperties = {
  display: 'flex',
  flexWrap: 'wrap',
  gap: 6,
  alignItems: 'center',
};

const sectionStyle: React.CSSProperties = {
  borderTop: '1px solid rgba(128,128,128,0.18)',
  paddingTop: 8,
};

const sectionTitleStyle: React.CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  marginBottom: 6,
};

const timingGridStyle: React.CSSProperties = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))',
  gap: 6,
};

const timingRowStyle: React.CSSProperties = {
  display: 'grid',
  gridTemplateColumns: '92px minmax(70px, 1fr) 58px',
  gap: 6,
  alignItems: 'center',
  minWidth: 0,
};

const timingTrackStyle: React.CSSProperties = {
  display: 'block',
  height: 8,
  borderRadius: 4,
  background: 'rgba(128,128,128,0.18)',
  overflow: 'hidden',
};

export function AsyncQMasterDataPanel(props: PanelProps<AsyncQMasterDataOptions>) {
  const options = { ...defaultOptions, ...props.options };
  const summary = useMemo(() => summarizeData(props.data.series, options), [props.data.series, options.timeColumn]);
  const [cacheStatus, setCacheStatus] = useState<CacheStatus | undefined>();
  const [actionState, setActionState] = useState<string>('');
  const [actionBusy, setActionBusy] = useState(false);
  const datasourceUid = options.datasourceUid || summary.datasourceUid;

  useEffect(() => {
    if (!datasourceUid || !options.showCache) {
      return;
    }
    let mounted = true;
    loadCacheStatus(datasourceUid)
      .then((status) => {
        if (mounted) {
          setCacheStatus(status);
        }
      })
      .catch((err) => {
        if (mounted) {
          setActionState(err instanceof Error ? err.message : String(err));
        }
      });
    return () => {
      mounted = false;
    };
  }, [datasourceUid, options.showCache, props.data.structureRev, props.renderCounter]);

  const runCacheAction = async (action: 'status' | 'clear' | 'clearExpired' | 'clearEntry', scope: CacheScope = 'both') => {
    if (!datasourceUid) {
      setActionState('No AsyncQ datasource UID found');
      return;
    }
    try {
      setActionBusy(true);
      const ds = (await getDataSourceSrv().get({ uid: datasourceUid })) as any;
      let result: any;
      if (action === 'status') {
        result = await callDatasourceMethod(ds, 'getCacheStatus', 'getResource', 'cache/status');
      } else if (action === 'clearEntry') {
        if (!summary.cacheKey) {
          setActionState('No cache key on current frame');
          return;
        }
        result = await callDatasourceMethod(ds, 'clearCacheEntry', 'postResource', 'cache/clear-entry', { key: summary.cacheKey, scope });
      } else if (action === 'clearExpired') {
        result = await callDatasourceMethod(ds, 'clearExpiredCache', 'postResource', 'cache/clear-expired', { scope });
      } else {
        result = await callDatasourceMethod(ds, 'clearCache', 'postResource', 'cache/clear', { scope });
      }
      setCacheStatus(result?.status || result);
      if (result?.ok === false && result?.error) {
        setActionState(result.error);
      } else {
        setActionState(action === 'status' ? 'Status refreshed' : 'Cache updated');
      }
    } catch (err) {
      setActionState(err instanceof Error ? err.message : String(err));
    } finally {
      setActionBusy(false);
    }
  };

  const freshness = freshnessLabel(summary, options);
  const isLoading = props.data.state === LoadingState.Loading || props.data.state === LoadingState.Streaming;
  const controlDisabled = actionBusy || cacheStatus?.controlEnabled === false;

  if (options.viewMode === 'freshness') {
    return (
      <div style={{ ...rootStyle, justifyContent: 'center' }}>
        <StatusStrip summary={summary} freshness={freshness} cacheStatus={cacheStatus} isLoading={isLoading} compact />
        {options.showControls && datasourceUid && (
          <div style={buttonRowStyle}>
            <Button size="sm" icon="sync" variant="secondary" onClick={() => runCacheAction('status')} disabled={actionBusy}>
              Refresh
            </Button>
            <Button size="sm" icon="history" variant="secondary" onClick={() => runCacheAction('clearExpired')} disabled={controlDisabled}>
              Expired
            </Button>
            {actionState && <span style={{ color: 'var(--text-secondary)' }}>{actionState}</span>}
          </div>
        )}
      </div>
    );
  }

  return (
    <div style={rootStyle}>
      <div style={scrollContentStyle}>
        <StatusStrip summary={summary} freshness={freshness} cacheStatus={cacheStatus} isLoading={isLoading} />
        {options.showControls && datasourceUid && (
          <div style={buttonRowStyle}>
            <Button size="sm" icon="sync" variant="secondary" onClick={() => runCacheAction('status')} disabled={actionBusy}>
              Refresh status
            </Button>
            <Button size="sm" icon="history" variant="secondary" onClick={() => runCacheAction('clearExpired')} disabled={controlDisabled}>
              Clear expired
            </Button>
            <Button size="sm" icon="trash-alt" variant="destructive" onClick={() => runCacheAction('clearEntry')} disabled={controlDisabled || !summary.cacheKey}>
              Clear entry
            </Button>
            <Button size="sm" icon="trash-alt" variant="destructive" onClick={() => runCacheAction('clear')} disabled={controlDisabled}>
              Clear cache
            </Button>
            {actionState && <span style={{ color: 'var(--text-secondary)' }}>{actionState}</span>}
          </div>
        )}
        <Profiler diagnostics={summary.diagnostics} />
        {options.viewMode !== 'diagnostics' && <FramePreview frame={summary.frame} rows={options.previewRows} />}
        {(options.viewMode === 'diagnostics' || options.showCache) && <Diagnostics diagnostics={summary.diagnostics} cacheStatus={cacheStatus} />}
      </div>
    </div>
  );
}

function StatusStrip({
  summary,
  freshness,
  cacheStatus,
  isLoading,
  compact,
}: {
  summary: DataSummary;
  freshness: { label: string; color: string; age?: string };
  cacheStatus?: CacheStatus;
  isLoading: boolean;
  compact?: boolean;
}) {
  return (
    <div style={stripStyle}>
      <Metric label="Freshness" value={freshness.age ? `${freshness.label} ${freshness.age}` : freshness.label} color={freshness.color} />
      {!compact && <Metric label="Rows" value={String(summary.rowCount)} />}
      {!compact && <Metric label="Fields" value={String(summary.fieldCount)} />}
      <Metric label="State" value={isLoading ? 'updating' : 'ready'} />
      {!compact && summary.diagnostics.durationMs !== undefined && <Metric label="Total" value={formatMs(summary.diagnostics.durationMs)} />}
      {!compact && summary.diagnostics.profileCachePathMs !== undefined && <Metric label="Cache/query" value={formatMs(summary.diagnostics.profileCachePathMs)} />}
      {!compact && summary.diagnostics.profileKdbCallMs !== undefined && <Metric label="kdb call" value={formatMs(summary.diagnostics.profileKdbCallMs)} />}
      {!compact && summary.diagnostics.profileFrameParseMs !== undefined && <Metric label="Frame parse" value={formatMs(summary.diagnostics.profileFrameParseMs)} />}
      {cacheStatus && <Metric label="Memory cache" value={`${cacheStatus.memory?.entries ?? 0}/${cacheStatus.memory?.maxEntries ?? '-'}`} />}
      {cacheStatus?.disk && <Metric label="Disk cache" value={`${cacheStatus.disk.entries ?? 0} files ${formatBytes(cacheStatus.disk.bytes || 0)}`} />}
    </div>
  );
}

function Metric({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div style={metricStyle}>
      <div style={labelStyle}>{label}</div>
      <div style={{ ...valueStyle, color }}>{value}</div>
    </div>
  );
}

interface TimingItem {
  label: string;
  value: number;
  color?: string;
}

function Profiler({ diagnostics }: { diagnostics: Record<string, any> }) {
  const total = numericDiagnostic(diagnostics.durationMs);
  const prepare = numericDiagnostic(diagnostics.profilePrepareMs);
  const cachePath = numericDiagnostic(diagnostics.profileCachePathMs);
  const topLevel: TimingItem[] = [
    { label: 'Prepare', value: prepare || 0, color: 'var(--info-text-color)' },
    { label: 'Cache/query', value: cachePath || 0, color: 'var(--success-text-color)' },
  ];
  if (total !== undefined) {
    const other = Math.max(0, total - topLevel.reduce((sum, item) => sum + item.value, 0));
    if (other > 0.001 || topLevel.every((item) => item.value === 0)) {
      topLevel.push({ label: 'Other', value: other, color: 'var(--text-secondary)' });
    }
  }

  const cacheDetail = [
    timingItem('Policy', diagnostics.profileCachePolicyMs),
    timingItem('Key', diagnostics.profileCacheKeyMs),
    timingItem('Memory', diagnostics.profileCacheMemoryLookupMs),
    timingItem('Disk', diagnostics.profileCacheDiskLookupMs),
    timingItem('Singleflight', diagnostics.profileCacheSingleflightMs),
    timingItem('Payload', diagnostics.profilePayloadBuildMs),
    timingItem('kdb call', diagnostics.profileKdbCallMs),
    timingItem('Frame parse', diagnostics.profileFrameParseMs),
  ].filter((item): item is TimingItem => item !== undefined);

  if (total === undefined && cacheDetail.length === 0 && topLevel.every((item) => item.value === 0)) {
    return null;
  }

  return (
    <div style={sectionStyle}>
      <div style={sectionTitleStyle}>Profile</div>
      <div style={timingGridStyle}>
        <div>
          <div style={labelStyle}>Top-level query path</div>
          <TimingBars items={topLevel} baseline={Math.max(total || 0, ...topLevel.map((item) => item.value), 0.001)} />
        </div>
        {cacheDetail.length > 0 && (
          <div>
            <div style={labelStyle}>Cache/query detail</div>
            <TimingBars items={cacheDetail} baseline={Math.max(cachePath || 0, ...cacheDetail.map((item) => item.value), 0.001)} />
          </div>
        )}
      </div>
    </div>
  );
}

function TimingBars({ items, baseline }: { items: TimingItem[]; baseline: number }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 4 }}>
      {items.map((item) => {
        const width = baseline > 0 ? Math.max(1, Math.min(100, (item.value / baseline) * 100)) : 1;
        return (
          <div key={item.label} style={timingRowStyle}>
            <span style={{ ...labelStyle, overflow: 'hidden', textOverflow: 'ellipsis' }}>{item.label}</span>
            <span style={timingTrackStyle}>
              <span
                style={{
                  display: 'block',
                  height: '100%',
                  width: `${width}%`,
                  background: item.color || 'var(--primary-text-color)',
                }}
              />
            </span>
            <span style={{ ...valueStyle, textAlign: 'right' }}>{formatMs(item.value)}</span>
          </div>
        );
      })}
    </div>
  );
}

function timingItem(label: string, value: any): TimingItem | undefined {
  const numeric = numericDiagnostic(value);
  if (numeric === undefined) {
    return undefined;
  }
  return { label, value: numeric };
}

function FramePreview({ frame, rows }: { frame?: DataFrame; rows: number }) {
  if (!frame || frame.fields.length === 0) {
    return <div style={{ color: 'var(--text-secondary)' }}>No data</div>;
  }
  const previewFields = frame.fields.slice(0, 10);
  const rowCount = Math.min(Math.max(rows, 1), frameLength(frame), 50);
  return (
    <div style={tableWrapStyle}>
      <table style={tableStyle}>
        <thead>
          <tr>
            {previewFields.map((field) => (
              <th key={field.name} style={{ ...cellStyle, textAlign: 'left', fontWeight: 600 }}>
                {field.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rowCount }).map((_, row) => (
            <tr key={row}>
              {previewFields.map((field) => (
                <td key={`${field.name}-${row}`} style={cellStyle} title={formatValue(fieldValue(field, row))}>
                  {formatValue(fieldValue(field, row))}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Diagnostics({ diagnostics, cacheStatus }: { diagnostics: Record<string, any>; cacheStatus?: CacheStatus }) {
  const keys = [
    'requestID',
    'datasourceUID',
    'executionMode',
    'compatibilityMode',
    'durationMs',
    'profileDecodeMs',
    'profilePrepareMs',
    'profileCachePathMs',
    'profileCachePolicyMs',
    'profileCacheKeyMs',
    'profileCacheMemoryLookupMs',
    'profileCacheDiskLookupMs',
    'profileCacheSingleflightMs',
    'profilePayloadBuildMs',
    'profileKdbCallMs',
    'profileFrameParseMs',
    'profileFrameRows',
    'profileFrameFields',
    'profileFrameCells',
    'queryCacheStatus',
    'queryCacheStorage',
    'queryCacheAgeMs',
    'queryCacheKey',
    'frameCount',
  ];
  return (
    <div style={sectionStyle}>
      <div style={sectionTitleStyle}>Diagnostics</div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 6 }}>
        {keys
          .filter((key) => diagnostics[key] !== undefined && diagnostics[key] !== '')
          .map((key) => (
            <Metric key={key} label={key} value={formatValue(diagnostics[key])} />
          ))}
        {cacheStatus?.disk?.error && <Metric label="disk error" value={cacheStatus.disk.error} color="var(--error-text-color)" />}
      </div>
    </div>
  );
}

function summarizeData(frames: DataFrame[], options: AsyncQMasterDataOptions): DataSummary {
  const frame = frames.find((candidate) => candidate.fields.length > 0);
  const diagnostics = extractDiagnostics(frames);
  const rowCount = frame ? frameLength(frame) : 0;
  const fieldCount = frame?.fields.length || 0;
  const latestTime = frame ? latestFrameTime(frame, options.timeColumn) : undefined;
  return {
    frame,
    diagnostics,
    rowCount,
    fieldCount,
    latestTime,
    freshnessMs: latestTime ? Date.now() - latestTime.getTime() : undefined,
    datasourceUid: options.datasourceUid || String(diagnostics.datasourceUID || datasourceUidFromFrames(frames) || ''),
    cacheKey: typeof diagnostics.queryCacheKey === 'string' ? diagnostics.queryCacheKey : undefined,
  };
}

function extractDiagnostics(frames: DataFrame[]): Record<string, any> {
  for (const frame of frames) {
    const custom = frame.meta?.custom as any;
    if (custom?.asyncqDiagnostics) {
      return custom.asyncqDiagnostics;
    }
  }
  return {};
}

function datasourceUidFromFrames(frames: DataFrame[]): string | undefined {
  for (const frame of frames) {
    const custom = frame.meta?.custom as any;
    if (custom?.datasourceUID) {
      return String(custom.datasourceUID);
    }
  }
  return undefined;
}

function latestFrameTime(frame: DataFrame, timeColumn?: string): Date | undefined {
  const field =
    (timeColumn && frame.fields.find((candidate) => candidate.name === timeColumn)) ||
    frame.fields.find((candidate) => candidate.type === FieldType.time) ||
    frame.fields.find((candidate) => /time|timestamp|datetime/i.test(candidate.name));
  if (!field) {
    return undefined;
  }
  let latest: Date | undefined;
  const rows = frameLength(frame);
  for (let i = 0; i < rows; i++) {
    const parsed = parseTime(fieldValue(field, i));
    if (parsed && (!latest || parsed > latest)) {
      latest = parsed;
    }
  }
  return latest;
}

function freshnessLabel(summary: DataSummary, options: AsyncQMasterDataOptions): { label: string; color: string; age?: string } {
  if (summary.freshnessMs === undefined) {
    return { label: 'unknown', color: 'var(--text-secondary)' };
  }
  const ageSeconds = Math.max(0, Math.round(summary.freshnessMs / 1000));
  if (ageSeconds >= options.criticalAfterSeconds) {
    return { label: 'stale', color: 'var(--error-text-color)', age: formatAge(ageSeconds) };
  }
  if (ageSeconds >= options.warnAfterSeconds) {
    return { label: 'aging', color: 'var(--warning-text-color)', age: formatAge(ageSeconds) };
  }
  return { label: 'fresh', color: 'var(--success-text-color)', age: formatAge(ageSeconds) };
}

function frameLength(frame: DataFrame): number {
  const frameWithLength = frame as DataFrame & { length?: number };
  if (typeof frameWithLength.length === 'number') {
    return frameWithLength.length;
  }
  return frame.fields[0]?.values?.length || 0;
}

function fieldValue(field: Field, index: number): any {
  const values = field.values as any;
  if (values && typeof values.get === 'function') {
    return values.get(index);
  }
  return values?.[index];
}

function parseTime(value: any): Date | undefined {
  if (value instanceof Date) {
    return value;
  }
  if (typeof value === 'number') {
    return new Date(value);
  }
  if (typeof value === 'string' && value !== '') {
    const parsed = new Date(value);
    if (!Number.isNaN(parsed.getTime())) {
      return parsed;
    }
  }
  return undefined;
}

function formatAge(seconds: number): string {
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest === 0 ? `${minutes}m` : `${minutes}m ${rest}s`;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return '0 B';
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ['KiB', 'MiB', 'GiB'];
  let value = bytes / 1024;
  let unit = units[0];
  for (let i = 1; i < units.length && value >= 1024; i++) {
    value = value / 1024;
    unit = units[i];
  }
  return `${value.toFixed(value >= 10 ? 0 : 1)} ${unit}`;
}

function formatMs(value: any): string {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) {
    return formatValue(value);
  }
  const abs = Math.abs(numeric);
  if (abs === 0) {
    return '0 ms';
  }
  if (abs < 1) {
    return `${numeric.toFixed(3)} ms`;
  }
  if (abs < 10) {
    return `${numeric.toFixed(2)} ms`;
  }
  if (abs < 100) {
    return `${numeric.toFixed(1)} ms`;
  }
  return `${Math.round(numeric)} ms`;
}

function numericDiagnostic(value: any): number | undefined {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? numeric : undefined;
}

function formatValue(value: any): string {
  if (value === null || value === undefined) {
    return '';
  }
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (Array.isArray(value)) {
    return value.join(', ');
  }
  if (typeof value === 'object') {
    return JSON.stringify(value);
  }
  return String(value);
}

async function loadCacheStatus(datasourceUid: string): Promise<CacheStatus> {
  const ds = (await getDataSourceSrv().get({ uid: datasourceUid })) as any;
  return callDatasourceMethod(ds, 'getCacheStatus', 'getResource', 'cache/status');
}

async function callDatasourceMethod(ds: any, method: string, fallbackMethod: string, path: string, body?: any): Promise<any> {
  if (typeof ds?.[method] === 'function') {
    if (method === 'getCacheStatus') {
      return ds[method](false);
    }
    if (method === 'clearCache' || method === 'clearExpiredCache') {
      return ds[method](body?.scope || 'both');
    }
    if (method === 'clearCacheEntry') {
      return ds[method](body?.key, body?.scope || 'both');
    }
  }
  if (typeof ds?.[fallbackMethod] === 'function') {
    return fallbackMethod === 'getResource' ? ds[fallbackMethod](path) : ds[fallbackMethod](path, body);
  }
  throw new Error('Datasource does not expose AsyncQ cache resources');
}
