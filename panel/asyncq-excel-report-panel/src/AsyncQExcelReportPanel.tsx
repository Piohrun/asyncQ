import React, { useEffect, useMemo, useRef, useState } from 'react';
import { PanelProps } from '@grafana/data';
import { config, getTemplateSrv } from '@grafana/runtime';
import { Button } from '@grafana/ui';

import { AsyncQExcelReportOptions, defaultOptions, ExcelReportCatalog, ExcelReportDefinition } from './types';

const rootStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  justifyContent: 'flex-start',
  height: '100%',
  minHeight: 0,
  gap: 8,
  overflow: 'auto',
  fontSize: 12,
};

const rowStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'flex-end',
  flexWrap: 'wrap',
  gap: 8,
  minWidth: 0,
};

const fileNameFieldStyle: React.CSSProperties = {
  display: 'flex',
  flex: '1 1 200px',
  flexDirection: 'column',
  gap: 2,
  minWidth: 160,
};

const labelStyle: React.CSSProperties = {
  color: 'var(--text-secondary)',
  fontSize: 11,
  lineHeight: '14px',
};

const inputStyle: React.CSSProperties = {
  background: 'var(--input-bg)',
  border: '1px solid var(--input-border-color, var(--border-weak))',
  borderRadius: 2,
  color: 'var(--input-text-color, var(--text-primary))',
  height: 32,
  lineHeight: '30px',
  minWidth: 0,
  padding: '0 8px',
  width: '100%',
};

const metaStyle: React.CSSProperties = {
  maxWidth: '100%',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  color: 'var(--text-secondary)',
};

const statusBannerStyle: React.CSSProperties = {
  border: '1px solid var(--primary-border, var(--border-weak))',
  borderRadius: 4,
  background: 'var(--background-secondary, var(--panel-bg))',
  color: 'var(--text-primary)',
  display: 'flex',
  flexDirection: 'column',
  gap: 6,
  padding: '8px 10px',
  width: '100%',
};

const statusTitleStyle: React.CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  lineHeight: '16px',
};

const statusTextStyle: React.CSSProperties = {
  color: 'var(--text-secondary)',
  fontSize: 12,
  lineHeight: '16px',
  overflowWrap: 'anywhere',
};

const successBannerStyle: React.CSSProperties = {
  ...statusBannerStyle,
  borderColor: 'var(--success-border, var(--border-weak))',
};

const errorStyle: React.CSSProperties = {
  ...metaStyle,
  color: 'var(--error-text-color)',
};

const progressRowStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 8,
  minWidth: 0,
  width: '100%',
};

const progressStyle: React.CSSProperties = {
  flex: '1 1 auto',
  accentColor: 'var(--primary-text-color, #1f60c4)',
  height: 10,
  minWidth: 80,
};

export function AsyncQExcelReportPanel(props: PanelProps<AsyncQExcelReportOptions>) {
  const options = { ...defaultOptions, ...props.options };
  const datasourceUid = options.datasourceUid.trim();
  const reportId = options.reportId.trim();
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');
  const [fileName, setFileName] = useState('');
  const [startedAt, setStartedAt] = useState(0);
  const [elapsedSeconds, setElapsedSeconds] = useState(0);
  const [catalog, setCatalog] = useState<ExcelReportCatalog | undefined>();
  const previousSuggestion = useRef('');

  useEffect(() => {
    if (!datasourceUid) {
      setCatalog(undefined);
      return;
    }
    let mounted = true;
    fetchReportCatalog(datasourceUid)
      .then((result) => {
        if (mounted) {
          setCatalog(result);
          setError('');
        }
      })
      .catch((err) => {
        if (mounted) {
          setCatalog(undefined);
          setError(formatError(err));
        }
      });
    return () => {
      mounted = false;
    };
  }, [datasourceUid]);

  const report = useMemo(() => findReport(catalog, reportId), [catalog, reportId]);
  const suggestedFileName = useMemo(() => reportFileNameSuggestion(report, reportId), [report, reportId]);
  const buttonText = options.buttonText.trim() || defaultOptions.buttonText;
  const disabled = busy || !datasourceUid || !reportId;

  useEffect(() => {
    const oldSuggestion = previousSuggestion.current;
    previousSuggestion.current = suggestedFileName;
    setFileName((current) => {
      if (!current || current === oldSuggestion) {
        return suggestedFileName;
      }
      return current;
    });
  }, [suggestedFileName]);

  useEffect(() => {
    if (!busy || !startedAt) {
      setElapsedSeconds(0);
      return undefined;
    }
    const timer = window.setInterval(() => {
      setElapsedSeconds(Math.floor((Date.now() - startedAt) / 1000));
    }, 250);
    return () => window.clearInterval(timer);
  }, [busy, startedAt]);

  const generate = async () => {
    if (!datasourceUid || !reportId) {
      setError('Datasource UID and report ID are required');
      return;
    }
    setBusy(true);
    setStartedAt(Date.now());
    setError('');
    setStatus('Writing workbook');
    try {
      const requestedFileName = options.showFileNameInput ? fileName.trim() || suggestedFileName : '';
      const generated = await generateReportDownloadLink(
        datasourceUid,
        {
          reportId,
          fileName: requestedFileName,
          timeRange: {
            from: toIsoString(props.timeRange.from),
            to: toIsoString(props.timeRange.to),
          },
          variables: collectDashboardVariables(),
          user: currentGrafanaUser(),
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '',
          frames: serializeFrames(props.data.series, props.data.request?.targets),
        },
        requestedFileName || suggestedFileName
      );
      downloadReportLink(datasourceUid, generated.token, generated.fileName);
      setStatus(`Report generated in ${formatDurationSeconds(generated.generationMs)}: ${generated.fileName}`);
    } catch (err) {
      setError(formatError(err));
      setStatus('');
    } finally {
      setBusy(false);
      setStartedAt(0);
    }
  };

  return (
    <div style={rootStyle}>
      <div style={rowStyle}>
        {options.showFileNameInput && (
          <label style={fileNameFieldStyle}>
            <span style={labelStyle}>Excel file name</span>
            <input
              aria-label="Excel file name"
              value={fileName}
              placeholder={suggestedFileName}
              disabled={busy}
              onChange={(event) => setFileName(event.currentTarget.value)}
              style={inputStyle}
            />
          </label>
        )}
        <Button icon="file-download" onClick={generate} disabled={disabled}>
          {busy ? 'Generating' : buttonText}
        </Button>
      </div>
      {busy && (
        <div role="status" aria-live="polite" style={statusBannerStyle}>
          <div style={statusTitleStyle}>Generating report</div>
          <div style={statusTextStyle}>
            {status || 'Writing workbook'}{elapsedSeconds > 0 ? ` (${elapsedSeconds}s)` : ''}
          </div>
          <div style={progressRowStyle}>
            <progress aria-label="Excel report generation progress" style={progressStyle} />
          </div>
        </div>
      )}
      {!busy && status && !error && (
        <div role="status" aria-live="polite" style={successBannerStyle}>
          <div style={statusTitleStyle}>Report ready</div>
          <div style={statusTextStyle}>{status}</div>
        </div>
      )}
      {options.showReportName && <div style={metaStyle}>{report?.name || reportId || 'No report selected'}</div>}
      {error && <div style={errorStyle} title={error}>{error}</div>}
    </div>
  );
}

async function fetchReportCatalog(datasourceUid: string): Promise<ExcelReportCatalog> {
  const response = await fetch(reportResourceUrl(datasourceUid, 'report/catalog'), {
    credentials: 'same-origin',
  });
  if (!response.ok) {
    throw new Error(await responseError(response));
  }
  return response.json();
}

function reportResourceUrl(datasourceUid: string, path: string): string {
  return `/api/datasources/uid/${encodeURIComponent(datasourceUid)}/resources/${path}`;
}

function findReport(catalog: ExcelReportCatalog | undefined, reportId: string): ExcelReportDefinition | undefined {
  return catalog?.reports?.find((candidate) => candidate.id === reportId);
}

function reportFileNameSuggestion(report: ExcelReportDefinition | undefined, reportId: string): string {
  const template = report?.outputName?.trim() || `${reportId || 'asyncq-report'}-{timestamp}.xlsx`;
  return ensureXlsxExtension(sanitizeFileName(renderFileNameTemplate(template, report, currentGrafanaUser(), new Date())));
}

function renderFileNameTemplate(
  template: string,
  report: ExcelReportDefinition | undefined,
  user: Record<string, any>,
  now: Date
): string {
  const reportType = String(report?.metadata?.reportType || report?.id || '');
  const replacements: Record<string, string> = {
    '{reportId}': report?.id || '',
    '{ReportID}': report?.id || '',
    '{reportName}': report?.name || '',
    '{ReportName}': report?.name || '',
    '{reportType}': reportType,
    '{ReportType}': reportType,
    '{userId}': String(user.id || ''),
    '{UserID}': String(user.id || ''),
    '{userUid}': String(user.uid || ''),
    '{UserUID}': String(user.uid || ''),
    '{login}': String(user.login || ''),
    '{userLogin}': String(user.login || ''),
    '{email}': String(user.email || ''),
    '{userEmail}': String(user.email || ''),
    '{userName}': String(user.name || ''),
    '{timestamp}': formatDate(now, true),
    '{yyyymmdd}': formatDate(now, false).slice(0, 8),
    '{yyyymmddhhmm}': formatDate(now, false),
    yyyymmddhhmm: formatDate(now, false),
    '{yyyymmddhhmmss}': formatDate(now, true).replace('-', ''),
    yyyymmddhhmmss: formatDate(now, true).replace('-', ''),
  };
  return Object.keys(replacements)
    .sort()
    .reduce((value, token) => value.split(token).join(replacements[token]), template);
}

function formatDate(value: Date, includeSeconds: boolean): string {
  const yyyy = String(value.getFullYear());
  const mm = String(value.getMonth() + 1).padStart(2, '0');
  const dd = String(value.getDate()).padStart(2, '0');
  const hh = String(value.getHours()).padStart(2, '0');
  const min = String(value.getMinutes()).padStart(2, '0');
  const sec = String(value.getSeconds()).padStart(2, '0');
  return includeSeconds ? `${yyyy}${mm}${dd}-${hh}${min}${sec}` : `${yyyy}${mm}${dd}${hh}${min}`;
}

function sanitizeFileName(value: string): string {
  const sanitized = value.trim().replace(/[\\/:*?"<>|]/g, '_');
  return sanitized || 'asyncq-report.xlsx';
}

function ensureXlsxExtension(value: string): string {
  return value.toLowerCase().endsWith('.xlsx') ? value : `${value}.xlsx`;
}

function currentGrafanaUser(): Record<string, any> {
  const user = config.bootData?.user || {};
  return {
    id: user.id,
    uid: user.uid,
    login: user.login,
    email: user.email,
    name: user.name,
  };
}

function collectDashboardVariables(): Record<string, any> {
  const templateSrv = getTemplateSrv();
  const variables = templateSrv.getVariables() as any[];
  const result: Record<string, any> = {};
  for (const variable of variables || []) {
    const name = String(variable?.name || '');
    if (!name) {
      continue;
    }
    result[name] = normalizeVariableValue(variable);
  }
  return result;
}

function serializeFrames(frames: any[], targets: any[] | undefined): any[] {
  return (frames || [])
    .map((frame, index) => {
      const target = targets?.[index] || {};
      const refId = String(frame.refId || target.refId || '');
      const fields = (frame.fields || []).map((field: any) => ({
        name: String(field.name || ''),
        values: fieldValues(field, frameLength(frame)),
      }));
      return {
        refId,
        targetRefId: String(target.refId || ''),
        panelId: target.panelId,
        name: String(frame.name || ''),
        fields,
      };
    })
    .filter((frame) => frame.fields.length > 0);
}

function frameLength(frame: any): number {
  if (typeof frame?.length === 'number') {
    return frame.length;
  }
  const first = frame?.fields?.[0];
  const values = first?.values as any;
  if (typeof values?.length === 'number') {
    return values.length;
  }
  if (typeof values?.toArray === 'function') {
    return values.toArray().length;
  }
  return 0;
}

function fieldValues(field: any, rows: number): any[] {
  const values = field?.values as any;
  const result: any[] = [];
  for (let i = 0; i < rows; i++) {
    const value = values && typeof values.get === 'function' ? values.get(i) : values?.[i];
    result.push(serializeValue(value));
  }
  return result;
}

function serializeValue(value: any): any {
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (value === undefined) {
    return null;
  }
  return value;
}

function normalizeVariableValue(variable: any): any {
  const current = variable?.current || {};
  const value = current.value !== undefined ? current.value : current.text;
  if (value === '$__all' && Array.isArray(variable?.options)) {
    return variable.options
      .filter((option: any) => option?.value !== '$__all')
      .map((option: any) => option?.value)
      .filter((option: any) => option !== undefined && option !== null)
      .map(String);
  }
  if (Array.isArray(value)) {
    return value.map((item) => String(item));
  }
  if (value === undefined || value === null) {
    return '';
  }
  return String(value);
}

function toIsoString(value: any): string {
  if (value && typeof value.toISOString === 'function') {
    return value.toISOString();
  }
  if (value && typeof value.toDate === 'function') {
    return value.toDate().toISOString();
  }
  return new Date(value).toISOString();
}

async function responseError(response: Response): Promise<string> {
  const text = await response.text();
  if (!text) {
    return `${response.status} ${response.statusText}`;
  }
  try {
    const parsed = JSON.parse(text);
    return parsed?.error || text;
  } catch {
    return text;
  }
}

async function generateReportDownloadLink(
  datasourceUid: string,
  payload: Record<string, any>,
  fallbackFileName: string
): Promise<{ token: string; fileName: string; generationMs: number }> {
  const response = await fetch(reportResourceUrl(datasourceUid, 'report/generate-link'), {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'content-type': 'application/json',
    },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw new Error(await responseError(response));
  }
  const result = await response.json();
  if (!result?.token) {
    throw new Error(result?.error || 'Report generation did not return a download token');
  }
  return {
    token: String(result.token),
    fileName: ensureXlsxExtension(sanitizeFileName(String(result.fileName || fallbackFileName))),
    generationMs: Number(result.generationMs || 0),
  };
}

function downloadReportLink(datasourceUid: string, token: string, fileName: string) {
  const iframe = document.createElement('iframe');
  iframe.src = `${reportResourceUrl(datasourceUid, 'report/download')}?token=${encodeURIComponent(token)}`;
  iframe.title = ensureXlsxExtension(sanitizeFileName(fileName));
  iframe.style.display = 'none';
  document.body.appendChild(iframe);
  window.setTimeout(() => iframe.remove(), 60000);
}

function formatDurationSeconds(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return '0.0s';
  }
  return `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)}s`;
}

function formatError(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
