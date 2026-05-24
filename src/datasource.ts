import {
  DataFrame,
  DataQueryRequest,
  DataQueryResponse,
  DataSourceInstanceSettings,
  LiveChannelScope,
  LoadingState,
  ScopedVars,
  dataFrameFromJSON,
} from '@grafana/data';
import { DataSourceWithBackend, getBackendSrv, getGrafanaLiveSrv, getTemplateSrv, toDataQueryResponse } from '@grafana/runtime';
import { merge, Observable, of } from 'rxjs';
import { map, shareReplay, takeWhile } from 'rxjs/operators';

import { MyDataSourceOptions, MyQuery, MyVariableQuery } from './types';

const defaultMode = 'sync';

interface StreamSession {
  framesByKey: Map<string, DataFrame>;
  maxRows: number;
  response: Observable<DataQueryResponse>;
  state: LoadingState;
}

interface AsyncSession {
  framesByKey: Map<string, DataFrame>;
  maxRows: number;
  state: LoadingState;
}

const streamSessions = new Map<string, StreamSession>();
const asyncSessions = new Map<string, AsyncSession>();
const maxAsyncSessions = 100;

export class DataSource extends DataSourceWithBackend<MyQuery, MyDataSourceOptions> {
  private options: MyDataSourceOptions;

  constructor(instanceSettings: DataSourceInstanceSettings<MyDataSourceOptions>) {
    super(instanceSettings);
    this.options = instanceSettings.jsonData || ({} as MyDataSourceOptions);
  }

  applyTemplateVariables(query: MyQuery, scopedVars?: ScopedVars) {
    const templateSrv = getTemplateSrv();
    return {
      ...query,
      queryText: query.queryText ? templateSrv.replace(query.queryText, scopedVars) : '',
      executionMode: query.executionMode || this.options.executionMode || defaultMode,
      compatibilityMode: query.compatibilityMode || this.options.compatibilityMode || 'native',
      deferredQueryWrapper: query.deferredQueryWrapper || this.options.deferredQueryWrapper || '',
      panopticonQueryWrapper: query.panopticonQueryWrapper || this.options.panopticonQueryWrapper || '',
      panopticonRequestFunction: query.panopticonRequestFunction || this.options.panopticonRequestFunction || '',
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
    const liveID = this.liveID(query, mode, request);
    const path = `${mode}/${liveID}`;
    const cacheKey = this.liveCacheKey(query, mode, path, request);
    const maxRows = query.maxStreamRows || request.maxDataPoints || 1000;

    if (mode === 'stream') {
      const session = this.getOrCreateStreamSession(live, cacheKey, query, request, path, liveID, maxRows);
      session.maxRows = Math.max(session.maxRows, maxRows);
      const snapshot = this.snapshotFrames(session.framesByKey, maxRows);
      if (snapshot.length > 0) {
        return merge(of({ data: snapshot, state: session.state, key: liveID }), session.response);
      }
      return session.response;
    }

    return this.runAsyncLiveQuery(live, cacheKey, query, request, path, maxRows);
  }

  private runAsyncLiveQuery(
    live: ReturnType<typeof getGrafanaLiveSrv>,
    cacheKey: string,
    query: MyQuery,
    request: DataQueryRequest<MyQuery>,
    path: string,
    maxRows: number
  ): Observable<DataQueryResponse> {
    const session = this.getOrCreateAsyncSession(cacheKey, maxRows);
    session.maxRows = Math.max(session.maxRows, maxRows);
    session.state = LoadingState.Streaming;
    const responseKey = `async-${this.stableHash(cacheKey)}`;

    const response = live!
      .getStream({
        scope: LiveChannelScope.DataSource,
        stream: this.uid,
        path,
        data: {
          ...query,
          refId: query.refId,
          maxDataPoints: request.maxDataPoints,
          intervalMs: request.intervalMs,
          timeRange: {
            from: request.range?.from?.toISOString(),
            to: request.range?.to?.toISOString(),
          },
        },
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

          const frameKey = `${frame.refId || query.refId || 'A'}/${frame.name || 'response'}`;
          session.framesByKey.set(frameKey, this.trimFrame(frame, session.maxRows));
          session.state = LoadingState.Streaming;
          return {
            data: this.snapshotFrames(session.framesByKey, session.maxRows),
            state: LoadingState.Streaming,
            key: responseKey,
          };
        }),
        takeWhile((response) => response.state !== LoadingState.Done && response.state !== LoadingState.Error, true)
      );

    const snapshot = this.snapshotFrames(session.framesByKey, maxRows);
    if (snapshot.length > 0) {
      return merge(of({ data: snapshot, state: LoadingState.Streaming, key: responseKey }), response);
    }
    return response;
  }

  private liveID(query: MyQuery, mode: string, request: DataQueryRequest<MyQuery>): string {
    const raw =
      mode === 'stream' && query.streamName
        ? `${query.streamName}-${query.refId || 'A'}`
        : mode === 'stream'
        ? `${request.panelId || 'panel'}-${query.refId || 'A'}-${this.stableHash(query.queryText || '')}`
        : `${request.requestId || request.panelId || 'query'}-${query.refId || 'A'}-${Date.now()}-${Math.random()
            .toString(36)
            .slice(2)}`;
    return raw.replace(/[^A-Za-z0-9_\-./=]/g, '-').slice(0, 96);
  }

  private liveCacheKey(query: MyQuery, mode: string, path: string, request: DataQueryRequest<MyQuery>): string {
    if (mode !== 'stream') {
      return [
        this.uid,
        request.dashboardUID || '',
        request.panelId || 'panel',
        query.refId || 'A',
        query.executionMode || '',
        query.compatibilityMode || '',
        this.rawTimeRangeKey(request),
        query.queryText || '',
        query.deferredQueryWrapper || '',
        query.panopticonQueryWrapper || '',
        query.panopticonRequestFunction || '',
        query.timeColumn || '',
        query.useTimeColumn ? 'time' : 'notime',
        query.includeKeyColumns ? 'keys' : 'nokeys',
      ].join('|');
    }
    return [
      this.uid,
      path,
      query.queryText || '',
      query.timeColumn || '',
      query.useTimeColumn ? 'time' : 'notime',
      query.includeKeyColumns ? 'keys' : 'nokeys',
    ].join('|');
  }

  private getOrCreateStreamSession(
    live: ReturnType<typeof getGrafanaLiveSrv>,
    cacheKey: string,
    query: MyQuery,
    request: DataQueryRequest<MyQuery>,
    path: string,
    liveID: string,
    maxRows: number
  ): StreamSession {
    const existing = streamSessions.get(cacheKey);
    if (existing) {
      return existing;
    }

    const session: StreamSession = {
      framesByKey: new Map<string, DataFrame>(),
      maxRows,
      response: of({ data: [], state: LoadingState.Streaming, key: liveID }),
      state: LoadingState.Streaming,
    };

    session.response = live!
      .getStream({
        scope: LiveChannelScope.DataSource,
        stream: this.uid,
        path,
        data: {
          ...query,
          refId: query.refId,
          maxDataPoints: request.maxDataPoints,
          intervalMs: request.intervalMs,
          timeRange: {
            from: request.range?.from?.toISOString(),
            to: request.range?.to?.toISOString(),
          },
        },
      } as any)
      .pipe(
        map((event: any) => {
          if (!event?.message) {
            return { data: this.snapshotFrames(session.framesByKey, session.maxRows), state: session.state, key: liveID };
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
            return { data: this.snapshotFrames(session.framesByKey, session.maxRows), state: session.state, key: liveID };
          }

          const frameKey = `${frame.refId || query.refId || 'A'}/${frame.name || 'response'}`;
          session.framesByKey.set(frameKey, this.appendFrame(session.framesByKey.get(frameKey), frame, session.maxRows));
          session.state = LoadingState.Streaming;
          return { data: this.snapshotFrames(session.framesByKey, session.maxRows), state: session.state, key: liveID };
        }),
        takeWhile((response) => response.state !== LoadingState.Done && response.state !== LoadingState.Error, true),
        shareReplay({ bufferSize: 1, refCount: false })
      );

    streamSessions.set(cacheKey, session);
    return session;
  }

  private getOrCreateAsyncSession(cacheKey: string, maxRows: number): AsyncSession {
    const existing = asyncSessions.get(cacheKey);
    if (existing) {
      asyncSessions.delete(cacheKey);
      asyncSessions.set(cacheKey, existing);
      return existing;
    }

    if (asyncSessions.size >= maxAsyncSessions) {
      const oldestKey = asyncSessions.keys().next().value;
      if (oldestKey !== undefined) {
        asyncSessions.delete(oldestKey);
      }
    }

    const session: AsyncSession = {
      framesByKey: new Map<string, DataFrame>(),
      maxRows,
      state: LoadingState.Streaming,
    };
    asyncSessions.set(cacheKey, session);
    return session;
  }

  private snapshotFrames(framesByKey: Map<string, DataFrame>, maxRows: number): DataFrame[] {
    return Array.from(framesByKey.values()).map((frame) => this.cloneFrame(frame, maxRows));
  }

  private cloneFrame(frame: DataFrame, maxRows: number): DataFrame {
    return {
      ...frame,
      fields: frame.fields.map((field) => ({
        ...field,
        values: this.valuesToArray(field.values).slice(-maxRows),
      })),
    };
  }

  private stableHash(value: string): string {
    let hash = 0;
    for (let i = 0; i < value.length; i++) {
      hash = (Math.imul(31, hash) + value.charCodeAt(i)) | 0;
    }
    return Math.abs(hash).toString(36);
  }

  private rawTimeRangeKey(request: DataQueryRequest<MyQuery>): string {
    return `${String(request.rangeRaw?.from || '')}/${String(request.rangeRaw?.to || '')}`;
  }

  private appendFrame(existing: DataFrame | undefined, incoming: DataFrame, maxRows: number): DataFrame {
    if (!existing || !this.sameSchema(existing, incoming)) {
      return this.trimFrame(incoming, maxRows);
    }

    for (let i = 0; i < incoming.fields.length; i++) {
      const oldValues = this.valuesToArray(existing.fields[i].values);
      const newValues = this.valuesToArray(incoming.fields[i].values);
      (incoming.fields[i] as any).values = oldValues.concat(newValues).slice(-maxRows);
    }
    return incoming;
  }

  private trimFrame(frame: DataFrame, maxRows: number): DataFrame {
    for (const field of frame.fields) {
      const values = this.valuesToArray(field.values);
      (field as any).values = values.slice(-maxRows);
    }
    return frame;
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
