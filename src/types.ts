import { DataQuery, DataSourceJsonData } from '@grafana/data';

export interface MyQuery extends DataQuery {
  queryText?: string;
  timeOut: number;
  useTimeColumn: boolean;
  timeColumn: string;
  includeKeyColumns: boolean;
  executionMode?: 'sync' | 'async' | 'stream';
  streamName?: string;
  pollIntervalMs?: number;
  maxStreamRows?: number;
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
}

export const defaultConfig: Partial<MyDataSourceOptions> = {
  withTLS: false,
  skipVerifyTLS: false,
  withCACert: false,
  enableAsync: true,
  enableStreaming: true,
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
