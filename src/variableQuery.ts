import type { LegacyMetricFindQueryOptions, MetricFindValue } from '@grafana/data';

import type { MyVariableQuery } from './types';

export const VARIABLE_QUERY_REF_ID = 'variable-query';
export const VARIABLE_QUERY_DEFAULT_TIMEOUT_MS = 10_000;
export const VARIABLE_QUERY_MAX_TIMEOUT_MS = 300_000;

export interface VariableQueryDatasource {
  uid: string;
  type: string;
}

export interface VariableQueryRequest {
  from?: string;
  to?: string;
  queries: [
    Omit<MyVariableQuery, 'queryText' | 'refId' | 'timeOut'> & {
      datasource: VariableQueryDatasource;
      executionMode: 'sync';
      queryText: string;
      refId: typeof VARIABLE_QUERY_REF_ID;
      timeOut: number;
    },
  ];
}

export function normalizeVariableQueryTimeout(value: unknown): number {
  const text = typeof value === 'string' ? value.trim() : String(value ?? '');
  if (!/^\d+$/.test(text)) {
    return VARIABLE_QUERY_DEFAULT_TIMEOUT_MS;
  }

  const timeout = Number(text);
  if (!Number.isSafeInteger(timeout)) {
    return VARIABLE_QUERY_DEFAULT_TIMEOUT_MS;
  }

  return Math.min(Math.max(timeout, 1), VARIABLE_QUERY_MAX_TIMEOUT_MS);
}

export function normalizeVariableQuery(query: MyVariableQuery): MyVariableQuery {
  return {
    ...query,
    queryText: query.queryText ?? '',
    timeOut: String(normalizeVariableQueryTimeout(query.timeOut)),
  };
}

export function variableQueryDefinition(query: MyVariableQuery): string {
  const normalized = normalizeVariableQuery(query);
  const text = normalized.queryText?.trim() || 'Variable query';
  return `${text} (${normalized.timeOut} ms)`;
}

export function buildVariableQueryRequest(
  query: MyVariableQuery,
  queryText: string,
  options: LegacyMetricFindQueryOptions | undefined,
  datasource: VariableQueryDatasource
): VariableQueryRequest {
  const range = options?.range;
  const from = rangeMilliseconds(range?.from);
  const to = rangeMilliseconds(range?.to);

  return {
    ...(from && to ? { from, to } : {}),
    queries: [
      {
        ...query,
        datasource,
        executionMode: 'sync',
        queryText,
        refId: VARIABLE_QUERY_REF_ID,
        timeOut: normalizeVariableQueryTimeout(query.timeOut),
      },
    ],
  };
}

function rangeMilliseconds(value: unknown): string | undefined {
  if (!value || typeof value !== 'object') {
    return undefined;
  }

  const valueOf = (value as { valueOf?: unknown }).valueOf;
  if (typeof valueOf !== 'function') {
    return undefined;
  }

  const milliseconds = valueOf.call(value);
  return typeof milliseconds === 'number' && Number.isFinite(milliseconds) ? milliseconds.toString() : undefined;
}

type FrameLike = {
  fields?: Array<{
    values?: unknown;
  }>;
};

type LegacyVector = {
  toArray: () => unknown;
};

export function variableQueryValuesToArray(values: unknown): unknown[] {
  if (Array.isArray(values)) {
    return values;
  }

  if (values && typeof (values as LegacyVector).toArray === 'function') {
    const array = (values as LegacyVector).toArray();
    return Array.isArray(array) ? array : [];
  }

  return [];
}

export function extractVariableQueryValues(frames: readonly unknown[]): MetricFindValue[] {
  const result: MetricFindValue[] = [];

  for (const frame of frames) {
    const firstField = (frame as FrameLike | undefined)?.fields?.[0];
    for (const value of variableQueryValuesToArray(firstField?.values)) {
      if (value !== null && value !== undefined) {
        result.push({ text: String(value) });
      }
    }
  }

  return result;
}

export function variableQueryErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }

  if (error && typeof error === 'object') {
    const candidate = error as {
      data?: { error?: unknown; message?: unknown };
      message?: unknown;
      statusText?: unknown;
    };
    const message = candidate.data?.message ?? candidate.data?.error ?? candidate.message ?? candidate.statusText;
    if (typeof message === 'string' && message) {
      return message;
    }
  }

  return 'Unknown backend error';
}
