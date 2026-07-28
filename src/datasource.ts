import {
  DataFrame,
  DataQueryRequest,
  DataQueryResponse,
  DataSourceInstanceSettings,
  FieldType,
  LiveChannelScope,
  LoadingState,
  ScopedVars,
  dataFrameFromJSON,
} from '@grafana/data';
import {
  DataSourceWithBackend,
  getBackendSrv,
  getGrafanaLiveSrv,
  getTemplateSrv,
  toDataQueryResponse,
} from '@grafana/runtime';
import { defer, merge, Observable, of } from 'rxjs';
import { finalize, map, shareReplay, takeWhile } from 'rxjs/operators';

import { MyDataSourceOptions, MyQuery, MyVariableQuery } from './types';
import { expandPanopticonDashboardParameters } from './panopticonParameters';

const defaultMode = 'sync';

interface StreamSession {
  framesByKey: Map<string, DataFrame>;
  maxRows: number;
  retentionMs: number;
  response: Observable<DataQueryResponse>;
  state: LoadingState;
}

interface AsyncSession {
  framesByKey: Map<string, DataFrame>;
  maxRows: number;
  state: LoadingState;
}

interface LiveRequestData extends MyQuery {
  intervalMs: number;
  maxDataPoints?: number;
  timeRange: {
    from?: string;
    to?: string;
  };
}

export class DataSource extends DataSourceWithBackend<MyQuery, MyDataSourceOptions> {
  private options: MyDataSourceOptions;
  private readonly streamSessions = new Map<string, StreamSession>();

  constructor(instanceSettings: DataSourceInstanceSettings<MyDataSourceOptions>) {
    super(instanceSettings);
    this.options = instanceSettings.jsonData || ({} as MyDataSourceOptions);
  }

  getCacheStatus(includeEntries = false): Promise<any> {
    return this.getResource(includeEntries ? 'cache/entries' : 'cache/status');
  }

  clearCache(scope: 'memory' | 'disk' | 'both' = 'both'): Promise<any> {
    return this.postResource('cache/clear', { scope });
  }

  clearCacheEntry(key: string, scope: 'memory' | 'disk' | 'both' = 'both'): Promise<any> {
    return this.postResource('cache/clear-entry', { key, scope });
  }

  clearExpiredCache(scope: 'memory' | 'disk' | 'both' = 'both'): Promise<any> {
    return this.postResource('cache/clear-expired', { scope });
  }

  getExcelReportCatalog(): Promise<any> {
    return this.getResource('report/catalog');
  }

  validateExcelReports(): Promise<any> {
    return this.getResource('report/validate');
  }

  applyTemplateVariables(query: MyQuery, scopedVars?: ScopedVars) {
    const templateSrv = getTemplateSrv();
    const dashboardVariables = templateSrv.getVariables();
    const executionMode = query.executionMode || this.options.executionMode || defaultMode;
    const compatibilityMode = query.compatibilityMode || this.options.compatibilityMode || 'native';
    let queryText = query.queryText ? templateSrv.replace(query.queryText, scopedVars) : '';
    let panopticonQueryWrapper = query.panopticonQueryWrapper || this.options.panopticonQueryWrapper || '';

    if (compatibilityMode === 'panopticon') {
      queryText = expandPanopticonDashboardParameters(queryText, scopedVars, dashboardVariables);
      panopticonQueryWrapper = expandPanopticonDashboardParameters(
        templateSrv.replace(panopticonQueryWrapper, scopedVars),
        scopedVars,
        dashboardVariables
      );
    }

    return {
      ...query,
      queryText,
      executionMode,
      compatibilityMode,
      deferredQueryWrapper: query.deferredQueryWrapper || this.options.deferredQueryWrapper || '',
      panopticonQueryWrapper,
      panopticonRequestFunction: query.panopticonRequestFunction || this.options.panopticonRequestFunction || '',
      legacyAsyncSubmit: query.legacyAsyncSubmit || this.options.legacyAsyncSubmit || '',
      legacyAsyncStatus: query.legacyAsyncStatus || this.options.legacyAsyncStatus || '',
      legacyAsyncResult: query.legacyAsyncResult || this.options.legacyAsyncResult || '',
      legacyAsyncCancel: query.legacyAsyncCancel || this.options.legacyAsyncCancel || '',
      legacyAsyncRequestMode: query.legacyAsyncRequestMode || this.options.legacyAsyncRequestMode || 'requestDict',
      legacyAsyncJobIDPath: query.legacyAsyncJobIDPath || this.options.legacyAsyncJobIDPath || 'jobId',
      legacyAsyncStatusPath: query.legacyAsyncStatusPath || this.options.legacyAsyncStatusPath || 'status',
      legacyAsyncProgressPath: query.legacyAsyncProgressPath || this.options.legacyAsyncProgressPath || 'progress',
      legacyAsyncMessagePath: query.legacyAsyncMessagePath || this.options.legacyAsyncMessagePath || 'message',
      legacyAsyncErrorPath: query.legacyAsyncErrorPath || this.options.legacyAsyncErrorPath || 'error',
      legacyAsyncPayloadPath: query.legacyAsyncPayloadPath || this.options.legacyAsyncPayloadPath || 'result',
      legacyAsyncQueuedValues:
        query.legacyAsyncQueuedValues || this.options.legacyAsyncQueuedValues || 'queued,pending',
      legacyAsyncRunningValues:
        query.legacyAsyncRunningValues || this.options.legacyAsyncRunningValues || 'running,executing',
      legacyAsyncDoneValues:
        query.legacyAsyncDoneValues || this.options.legacyAsyncDoneValues || 'done,complete,completed',
      legacyAsyncErrorValues: query.legacyAsyncErrorValues || this.options.legacyAsyncErrorValues || 'error,failed',
      legacyAsyncCancelledValues:
        query.legacyAsyncCancelledValues || this.options.legacyAsyncCancelledValues || 'cancelled,canceled',
    };
  }

  query(request: DataQueryRequest<MyQuery>): Observable<DataQueryResponse> {
    const syncTargets = request.targets.filter((target) => this.targetMode(target) === 'sync');
    const liveTargets = request.targets.filter((target) => this.targetMode(target) !== 'sync');
    const streams: Array<Observable<DataQueryResponse>> = [];

    if (syncTargets.length > 0) {
      streams.push(super.query({ ...request, targets: syncTargets }));
    }

    for (const target of liveTargets) {
      streams.push(this.runLiveQuery(target, request));
    }

    if (streams.length === 0) {
      return of({ data: [] });
    }
    if (streams.length === 1) {
      return streams[0];
    }
    return merge(...streams);
  }

  private targetMode(target: MyQuery): string {
    return target.executionMode || this.options.executionMode || defaultMode;
  }

  private runLiveQuery(target: MyQuery, request: DataQueryRequest<MyQuery>): Observable<DataQueryResponse> {
    const live = getGrafanaLiveSrv();
    if (!live) {
      return of({ data: [], state: LoadingState.Error, error: new Error('Grafana Live is not available') });
    }

    const query = this.applyTemplateVariables(target, request.scopedVars);
    const mode = query.executionMode === 'stream' ? 'stream' : 'async';
    const maxRows = query.maxStreamRows || request.maxDataPoints || 1000;
    const retentionMs = query.streamRetentionMs || 0;
    const liveRequestData = this.liveRequestData(query, request);
    const requestIdentity = this.liveRequestIdentity(mode, liveRequestData, request, maxRows, retentionMs);
    const liveID = this.liveID(query, mode, request, requestIdentity);
    const path = `${mode}/${liveID}`;

    if (mode === 'stream') {
      return defer(() => {
        const session = this.getOrCreateStreamSession(
          live,
          requestIdentity,
          liveRequestData,
          path,
          liveID,
          maxRows,
          retentionMs
        );
        session.maxRows = Math.max(session.maxRows, maxRows);
        session.retentionMs = retentionMs;
        const snapshot = this.snapshotFrames(session.framesByKey, maxRows, retentionMs);
        if (snapshot.length > 0) {
          return merge(of({ data: snapshot, state: session.state, key: liveID }), session.response);
        }
        return session.response;
      });
    }

    return this.runAsyncLiveQuery(live, liveRequestData, path, liveID, maxRows);
  }

  private runAsyncLiveQuery(
    live: ReturnType<typeof getGrafanaLiveSrv>,
    liveRequestData: LiveRequestData,
    path: string,
    liveID: string,
    maxRows: number
  ): Observable<DataQueryResponse> {
    return defer(() => {
      const session: AsyncSession = {
        framesByKey: new Map<string, DataFrame>(),
        maxRows,
        state: LoadingState.Streaming,
      };
      const responseKey = `async-${this.stableHash(liveID)}`;

      return live!
        .getStream({
          scope: LiveChannelScope.DataSource,
          stream: this.uid,
          path,
          data: liveRequestData,
        } as any)
        .pipe(
          map((event: any) => {
            if (!event?.message) {
              return {
                data: this.snapshotFrames(session.framesByKey, session.maxRows),
                state: session.state,
                key: responseKey,
              };
            }

            const frame = dataFrameFromJSON(event.message);
            const custom: any = frame.meta?.custom || {};

            if (custom.asyncqControl) {
              const nextState = String(custom.asyncqState || '').toLowerCase();
              if (custom.asyncqTerminal) {
                session.state = nextState === 'error' ? LoadingState.Error : LoadingState.Done;
              } else {
                session.state = LoadingState.Streaming;
              }
              return {
                data: this.snapshotFrames(session.framesByKey, session.maxRows),
                state: session.state,
                key: responseKey,
              };
            }

            const frameKey = `${frame.refId || liveRequestData.refId || 'A'}/${frame.name || 'response'}`;
            session.framesByKey.set(frameKey, this.trimFrame(frame, session.maxRows));
            session.state = LoadingState.Streaming;
            return {
              data: this.snapshotFrames(session.framesByKey, session.maxRows),
              state: LoadingState.Streaming,
              key: responseKey,
            };
          }),
          takeWhile((response) => response.state !== LoadingState.Done && response.state !== LoadingState.Error, true),
          finalize(() => session.framesByKey.clear())
        );
    });
  }

  private liveRequestData(query: MyQuery, request: DataQueryRequest<MyQuery>): LiveRequestData {
    return {
      ...query,
      refId: query.refId,
      maxDataPoints: request.maxDataPoints,
      intervalMs: request.intervalMs,
      timeRange: {
        from: request.range?.from?.toISOString(),
        to: request.range?.to?.toISOString(),
      },
    };
  }

  private liveRequestIdentity(
    mode: string,
    liveRequestData: LiveRequestData,
    request: DataQueryRequest<MyQuery>,
    maxRows: number,
    retentionMs: number
  ): string {
    return this.stableSerialize({
      datasourceUID: this.uid,
      mode,
      interval: request.interval,
      maxRows,
      retentionMs,
      request: liveRequestData,
    });
  }

  private liveID(query: MyQuery, mode: string, request: DataQueryRequest<MyQuery>, requestIdentity: string): string {
    // Grafana Live keys backend subscriptions by path, so the path must identify the effective request data.
    const identityHash = this.stableHash(requestIdentity);
    if (mode === 'stream') {
      const base = query.streamName
        ? `${query.streamName}-${query.refId || 'A'}`
        : `${request.panelId || 'panel'}-${query.refId || 'A'}`;
      return this.liveChannelID(base, identityHash);
    }

    const nonce = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    return this.liveChannelID(
      `${request.requestId || request.panelId || 'query'}-${query.refId || 'A'}`,
      `${identityHash}-${nonce}`
    );
  }

  private liveChannelID(base: string, suffix: string): string {
    const safeBase = base.replace(/[^A-Za-z0-9_\-./=]/g, '-');
    const safeSuffix = suffix.replace(/[^A-Za-z0-9_\-./=]/g, '-');
    const baseLength = Math.max(0, 96 - safeSuffix.length - 1);
    return `${safeBase.slice(0, baseLength)}-${safeSuffix}`;
  }

  private getOrCreateStreamSession(
    live: ReturnType<typeof getGrafanaLiveSrv>,
    cacheKey: string,
    liveRequestData: LiveRequestData,
    path: string,
    liveID: string,
    maxRows: number,
    retentionMs: number
  ): StreamSession {
    const existing = this.streamSessions.get(cacheKey);
    if (existing?.state === LoadingState.Streaming) {
      return existing;
    }
    if (existing) {
      this.streamSessions.delete(cacheKey);
    }

    const session: StreamSession = {
      framesByKey: new Map<string, DataFrame>(),
      maxRows,
      retentionMs,
      response: of({ data: [], state: LoadingState.Streaming, key: liveID }),
      state: LoadingState.Streaming,
    };

    session.response = live!
      .getStream({
        scope: LiveChannelScope.DataSource,
        stream: this.uid,
        path,
        data: liveRequestData,
      } as any)
      .pipe(
        map((event: any) => {
          if (!event?.message) {
            return {
              data: this.snapshotFrames(session.framesByKey, session.maxRows, session.retentionMs),
              state: session.state,
              key: liveID,
            };
          }

          const frame = dataFrameFromJSON(event.message);
          const custom: any = frame.meta?.custom || {};

          if (custom.asyncqControl) {
            const nextState = String(custom.asyncqState || '').toLowerCase();
            if (custom.asyncqTerminal) {
              session.state = nextState === 'error' ? LoadingState.Error : LoadingState.Done;
            } else {
              session.state = LoadingState.Streaming;
            }
            return {
              data: this.snapshotFrames(session.framesByKey, session.maxRows, session.retentionMs),
              state: session.state,
              key: liveID,
            };
          }

          const frameKey = `${frame.refId || liveRequestData.refId || 'A'}/${frame.name || 'response'}`;
          session.framesByKey.set(
            frameKey,
            this.appendFrame(session.framesByKey.get(frameKey), frame, session.maxRows, session.retentionMs)
          );
          session.state = LoadingState.Streaming;
          return {
            data: this.snapshotFrames(session.framesByKey, session.maxRows, session.retentionMs),
            state: session.state,
            key: liveID,
          };
        }),
        takeWhile((response) => response.state !== LoadingState.Done && response.state !== LoadingState.Error, true),
        // Keep cleanup upstream of shareReplay so it runs once when the shared source disconnects.
        finalize(() => {
          if (this.streamSessions.get(cacheKey) === session) {
            this.streamSessions.delete(cacheKey);
          }
          session.framesByKey.clear();
        }),
        shareReplay({ bufferSize: 1, refCount: true })
      );

    this.streamSessions.set(cacheKey, session);
    return session;
  }

  private snapshotFrames(framesByKey: Map<string, DataFrame>, maxRows: number, retentionMs = 0): DataFrame[] {
    return Array.from(framesByKey.values()).map((frame) => this.cloneFrame(frame, maxRows, retentionMs));
  }

  private cloneFrame(frame: DataFrame, maxRows: number, retentionMs = 0): DataFrame {
    const startIndex = this.retentionStartIndex(frame, maxRows, retentionMs);
    return {
      ...frame,
      fields: frame.fields.map((field) => ({
        ...field,
        values: this.valuesToArray(field.values).slice(startIndex),
      })),
    };
  }

  private stableHash(value: string): string {
    let first = 0x811c9dc5;
    let second = 0x9e3779b9;
    let third = 0x85ebca6b;
    let fourth = 0xc2b2ae35;
    for (let i = 0; i < value.length; i++) {
      const code = value.charCodeAt(i);
      first = Math.imul(first ^ code, 0x01000193);
      second = Math.imul(second ^ code, 0x27d4eb2d);
      third = Math.imul(third ^ code, 0x165667b1);
      fourth = Math.imul(fourth ^ code, 0x85ebca77);
    }
    return [first, second, third, fourth].map((hash) => (hash >>> 0).toString(36).padStart(7, '0')).join('');
  }

  private stableSerialize(value: unknown): string {
    return (
      JSON.stringify(value, (_key, item: unknown) => {
        if (!item || typeof item !== 'object' || Array.isArray(item)) {
          return item;
        }
        return Object.keys(item as Record<string, unknown>)
          .sort()
          .reduce<Record<string, unknown>>((sorted, key) => {
            sorted[key] = (item as Record<string, unknown>)[key];
            return sorted;
          }, {});
      }) || ''
    );
  }

  private appendFrame(
    existing: DataFrame | undefined,
    incoming: DataFrame,
    maxRows: number,
    retentionMs = 0
  ): DataFrame {
    if (!existing || !this.sameSchema(existing, incoming)) {
      return this.trimFrame(incoming, maxRows, retentionMs);
    }

    for (let i = 0; i < incoming.fields.length; i++) {
      const oldValues = this.valuesToArray(existing.fields[i].values);
      const newValues = this.valuesToArray(incoming.fields[i].values);
      (incoming.fields[i] as any).values = oldValues.concat(newValues);
    }
    return this.trimFrame(incoming, maxRows, retentionMs);
  }

  private trimFrame(frame: DataFrame, maxRows: number, retentionMs = 0): DataFrame {
    const startIndex = this.retentionStartIndex(frame, maxRows, retentionMs);
    for (const field of frame.fields) {
      const values = this.valuesToArray(field.values);
      (field as any).values = values.slice(startIndex);
    }
    return frame;
  }

  private retentionStartIndex(frame: DataFrame, maxRows: number, retentionMs: number): number {
    const rowCount = frame.fields[0] ? this.valuesToArray(frame.fields[0].values).length : 0;
    let startIndex = Math.max(0, rowCount - maxRows);
    if (retentionMs <= 0 || rowCount === 0) {
      return startIndex;
    }

    const timeField = frame.fields.find(
      (field) => field.type === FieldType.time || field.name.toLowerCase() === 'time'
    );
    if (!timeField) {
      return startIndex;
    }
    const times = this.valuesToArray(timeField.values);
    const latest = this.toMillis(times[times.length - 1]);
    if (latest === undefined) {
      return startIndex;
    }
    const cutoff = latest - retentionMs;
    const retentionIndex = times.findIndex((value) => {
      const millis = this.toMillis(value);
      return millis !== undefined && millis >= cutoff;
    });
    if (retentionIndex >= 0) {
      startIndex = Math.max(startIndex, retentionIndex);
    }
    return startIndex;
  }

  private sameSchema(a: DataFrame, b: DataFrame): boolean {
    if (a.fields.length !== b.fields.length) {
      return false;
    }
    return a.fields.every((field, index) => field.name === b.fields[index].name && field.type === b.fields[index].type);
  }

  private valuesToArray(values: any): any[] {
    if (Array.isArray(values)) {
      return values;
    }
    if (values?.toArray) {
      return values.toArray();
    }
    return [];
  }

  private toMillis(value: any): number | undefined {
    if (value instanceof Date) {
      return value.getTime();
    }
    if (typeof value === 'number') {
      return value;
    }
    if (typeof value === 'string') {
      const parsed = Date.parse(value);
      return Number.isNaN(parsed) ? undefined : parsed;
    }
    return undefined;
  }

  async metricFindQuery(query: MyVariableQuery, options?: any): Promise<any> {
    const templateSrv = getTemplateSrv();
    let timeout = parseInt(query.timeOut, 10);
    const body: any = {
      queries: [
        {
          datasourceId: this.id,
          orgId: this.id,
          queryText: query.queryText ? templateSrv.replace(query.queryText) : '',
          timeOut: timeout,
          executionMode: 'sync',
        },
      ],
    };

    const backendQuery = getBackendSrv()
      .datasourceRequest({
        url: '/api/ds/query',
        method: 'POST',
        data: body,
      })
      .then((response: any) => {
        let parsedResponse = toDataQueryResponse(response);
        let responseValues: any[] = [];
        for (let frame in parsedResponse.data) {
          responseValues = responseValues.concat(
            parsedResponse.data[frame].fields[0].values.toArray().map((x: any) => {
              return { text: x };
            })
          );
        }
        return responseValues;
      })
      .catch((err) => {
        console.log(err);
        err.isHandled = true;
        return { text: 'ERROR' };
      });
    return backendQuery;
  }
}
