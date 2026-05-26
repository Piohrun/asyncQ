import { DataQuery, DataSourceJsonData } from '@grafana/data';

export type ExecutionMode = 'sync' | 'async' | 'pluginAsync' | 'deferredAsync' | 'legacyAsync' | 'stream';
export type CompatibilityMode = 'native' | 'aquaq' | 'panopticon';
export type QueryCacheMode = 'default' | 'enabled' | 'disabled' | 'bypass' | 'refresh';
export type QueryCacheKeyMode = 'default' | 'strict' | 'shared';
export type LegacyAsyncRequestMode = 'queryText' | 'compiledQueryText' | 'requestDict' | 'panopticonDict';

export interface MyQuery extends DataQuery {
  queryText?: string;
  timeOut?: number;
  useTimeColumn: boolean;
  timeColumn: string;
  includeKeyColumns: boolean;
  executionMode?: ExecutionMode;
  compatibilityMode?: CompatibilityMode;
  deferredQueryWrapper?: string;
  panopticonQueryWrapper?: string;
  panopticonRequestFunction?: string;
  legacyAsyncSubmit?: string;
  legacyAsyncStatus?: string;
  legacyAsyncResult?: string;
  legacyAsyncCancel?: string;
  legacyAsyncRequestMode?: LegacyAsyncRequestMode;
  legacyAsyncJobIDPath?: string;
  legacyAsyncStatusPath?: string;
  legacyAsyncProgressPath?: string;
  legacyAsyncMessagePath?: string;
  legacyAsyncErrorPath?: string;
  legacyAsyncPayloadPath?: string;
  legacyAsyncQueuedValues?: string;
  legacyAsyncRunningValues?: string;
  legacyAsyncDoneValues?: string;
  legacyAsyncErrorValues?: string;
  legacyAsyncCancelledValues?: string;
  streamName?: string;
  pollIntervalMs?: number;
  maxStreamRows?: number;
  streamRetentionMs?: number;
  queryCacheMode?: QueryCacheMode;
  queryCacheKeyMode?: QueryCacheKeyMode;
  queryCacheTTLSeconds?: number;
  queryCacheStaleTTLSeconds?: number;
  queryCacheTimeBucketSeconds?: number;
}

/**
 * These are options configured for each DataSource instance.
 */
export interface MyDataSourceOptions extends DataSourceJsonData {
  host: string;
  port?: number;
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
  legacyAsyncSubmit?: string;
  legacyAsyncStatus?: string;
  legacyAsyncResult?: string;
  legacyAsyncCancel?: string;
  legacyAsyncRequestMode?: LegacyAsyncRequestMode;
  legacyAsyncJobIDPath?: string;
  legacyAsyncStatusPath?: string;
  legacyAsyncProgressPath?: string;
  legacyAsyncMessagePath?: string;
  legacyAsyncErrorPath?: string;
  legacyAsyncPayloadPath?: string;
  legacyAsyncQueuedValues?: string;
  legacyAsyncRunningValues?: string;
  legacyAsyncDoneValues?: string;
  legacyAsyncErrorValues?: string;
  legacyAsyncCancelledValues?: string;
  asyncMaxJobs?: number;
  syncMaxConnections?: number;
  queryCacheEnabled?: boolean;
  queryCacheTTLSeconds?: number;
  queryCacheMaxEntries?: number;
  queryCacheTimeBucketSeconds?: number;
  queryCacheStaleTTLSeconds?: number;
  queryCacheKeyMode?: 'strict' | 'shared';
  queryCacheDiskEnabled?: boolean;
  queryCacheDiskPath?: string;
  queryCacheDiskMaxBytes?: number;
  queryCacheDiskMaxEntries?: number;
  queryCacheControlEnabled?: boolean;
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
  legacyAsyncRequestMode: 'requestDict',
  legacyAsyncJobIDPath: 'jobId',
  legacyAsyncStatusPath: 'status',
  legacyAsyncProgressPath: 'progress',
  legacyAsyncMessagePath: 'message',
  legacyAsyncErrorPath: 'error',
  legacyAsyncPayloadPath: 'result',
  legacyAsyncQueuedValues: 'queued,pending',
  legacyAsyncRunningValues: 'running,executing',
  legacyAsyncDoneValues: 'done,complete,completed',
  legacyAsyncErrorValues: 'error,failed',
  legacyAsyncCancelledValues: 'cancelled,canceled',
  syncMaxConnections: 4,
  queryCacheEnabled: true,
  queryCacheTTLSeconds: 60,
  queryCacheMaxEntries: 128,
  queryCacheTimeBucketSeconds: 0,
  queryCacheStaleTTLSeconds: 0,
  queryCacheKeyMode: 'strict',
  queryCacheDiskEnabled: true,
  queryCacheDiskMaxBytes: 1073741824,
  queryCacheDiskMaxEntries: 10000,
  queryCacheControlEnabled: true,
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
