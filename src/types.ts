import { DataQuery, DataSourceJsonData } from '@grafana/data';

export type ExecutionMode = 'sync' | 'async' | 'pluginAsync' | 'deferredAsync' | 'stream';
export type CompatibilityMode = 'native' | 'aquaq' | 'panopticon';

export interface MyQuery extends DataQuery {
  queryText?: string;
  timeOut: number;
  useTimeColumn: boolean;
  timeColumn: string;
  includeKeyColumns: boolean;
  executionMode?: ExecutionMode;
  compatibilityMode?: CompatibilityMode;
  deferredQueryWrapper?: string;
  panopticonQueryWrapper?: string;
  panopticonRequestFunction?: string;
  streamName?: string;
  pollIntervalMs?: number;
  maxStreamRows?: number;
  streamRetentionMs?: number;
}

/**
 * These are options configured for each DataSource instance.
 */
export interface MyDataSourceOptions extends DataSourceJsonData {
  host: string;
  port: number;
  timeout?: string;
  withTLS: boolean;
  skipVerifyTLS: boolean;
  withCACert: boolean;
  enableAsync?: boolean;
  enableStreaming?: boolean;
  executionMode?: ExecutionMode;
  compatibilityMode?: CompatibilityMode;
  deferredQueryWrapper?: string;
  panopticonQueryWrapper?: string;
  panopticonRequestFunction?: string;
  asyncMaxJobs?: number;
  diagnosticsEnabled?: boolean;
  diagnosticsLogQueryText?: boolean;
}

export const defaultConfig: Partial<MyDataSourceOptions> = {
  withTLS: false,
  skipVerifyTLS: false,
  withCACert: false,
  enableAsync: true,
  enableStreaming: true,
  executionMode: 'sync',
  compatibilityMode: 'native',
  diagnosticsEnabled: false,
  diagnosticsLogQueryText: false,
};

/**
 * Value that is used in the backend, but never sent over HTTP to the frontend
 */
export interface MySecureJsonData {
  username: string;
  password: string;
  tlsCertificate?: string;
  tlsKey?: string;
  caCert?: string;
}

export interface MyVariableQuery extends DataQuery{
  queryText?: string;
  timeOut: string;
}
