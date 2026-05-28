# AsyncQ kdb+ Grafana datasource

AsyncQ is a Grafana 13 backend datasource for kdb+ derived from AquaQ Analytics' community kdb+ backend datasource. The synchronous query path is intentionally kept compatible with the original plugin, while panel queries can opt into helper async, plugin-managed async, legacy gateway async adapters, deferred-wrapper async, or q-driven streaming through Grafana Live.

## Query modes

Sync mode is the default and keeps the upstream request envelope. Variables and alerting use sync mode.

Each datasource instance has a bounded sync IPC connection pool. `Sync Max Connections` defaults to `4`, allowing independent sync panels on the same datasource to run concurrently. Set it to `1` for strict legacy serial behavior or gateways with per-handle state/low connection limits.

Datasource `Query Cache` is enabled by default for new datasource configs and can cache successful sync query results in memory and on local disk for `Cache TTL (s)`. Use it only for read-only gateway calls; explicit disabled settings remain respected. Cache keys include query text after macro expansion, time range, interval, max data points, datasource identity, and Grafana user context. `Cache Time Bucket (s)` defaults to exact time ranges (`0`); set it to a small bucket to reuse results for relative `now` dashboards. `Stale TTL (s)` returns expired cached data immediately while refreshing the cache in the background for the next query. `Cache Key` defaults to `Strict`; use `Shared` only when the q request does not depend on Grafana ref ID. Per-query cache controls can override mode, key mode, TTL, stale TTL, and time bucket. Put `asyncq:cache=off`, `asyncq:cache=bypass`, `asyncq:cache=refresh`, or `asyncq:cache=on` in a q comment to force query-level behavior.

Excel report definitions support `writeMode` at report or binding level. Use `writeMode: "stream"` for large dedicated workbook data sheets; it uses excelize's streaming row writer and replaces the target sheet's cell data. Keep charts and formulas on separate sheets pointing at those ranges. The default `cells` mode preserves the legacy per-cell writer for template sheets that must preserve existing cells. The companion report panel shows generation progress, final duration, and downloads through a one-time backend token so custom filenames are preserved.

Helper Async mode uses Grafana Live and calls q helper functions:

```q
.grafana.asyncq.async.submit requestDict
.grafana.asyncq.async.status jobId
.grafana.asyncq.async.result jobId
.grafana.asyncq.async.cancel jobId
```

The datasource also exposes `POST async/run-and-wait` for MCP clients and migration tooling. It runs finite async modes (`async`, `pluginAsync`, `deferredAsync`, and `legacyAsync`) to completion and returns final frames plus status timeline events. `stream` is not supported by this resource because streams are open Grafana Live subscriptions.

Plugin Async mode uses Grafana Live but does not require q helper functions. The backend opens a dedicated IPC connection, evaluates the query in a goroutine, emits status frames while waiting, and returns the final q result when it arrives.

Deferred Async mode first expands a wrapper containing exactly one `{Query}` placeholder, then runs that expression through Plugin Async. Use this for q gateways that already support deferred responses or wrapper-based submission.

Legacy Async mode targets existing same-port q gateway protocols that already have submit/status/result/cancel functions but use different names or envelopes from the AsyncQ helper. Configure `legacyAsyncSubmit`, `legacyAsyncStatus`, optional `legacyAsyncResult`/`legacyAsyncCancel`, request mode, response paths, and comma-separated status value mappings at datasource or query level. The adapter can submit the full request dict, Panopticon dict, original query text, or compiled query text.

Stream mode uses Grafana Live and calls:

```q
.grafana.asyncq.stream.start requestDict
.grafana.asyncq.stream.stop streamId
```

q code can push rows to a subscribed panel with:

```q
.grafana.asyncq.stream.publish[streamId; ([] time:.z.p+til 3; value:10 20 30)]
.grafana.asyncq.stream.done streamId
```

Streaming query `Max Rows` is a browser-side row cap. The optional `Retention (s)` field adds a trailing time-window cap for time series streams; the plugin applies both limits.

## q helper

Load the development helper from this repository:

```q
\l q/asyncq_grafana.q
```

The helper documents the backend contract and supports local testing. Its async implementation evaluates jobs in-process, so production deployments should replace it with gateway/worker-backed functions that return quickly from `async.submit`.

## Data contract

Native and AquaQ compatibility modes accept flat kdb+ tables or grouped tables. Panopticon compatibility mode also accepts keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries and converts them into Grafana frames for table-style panels. Sparse row dictionaries are supported by building the union of row keys and returning nulls for missing cells. Mixed generic-list table columns are converted to string columns.

The request dictionary preserves the upstream `AQUAQ_KDB_BACKEND_GRAF_DATASOURCE` key for compatibility and adds metadata such as `ExecutionMode`, `CompatibilityMode`, `RequestID`, `StreamID`, `PollIntervalMs`, `MaxStreamRows`, and a `Panopticon` dict containing timestamp aliases, text aliases, interval metadata, `RefID`, `OriginalQuery`, and `CompiledQuery`. Common Panopticon aliases such as `TimeWindowStart`, `TimeWindowEnd`, `Snapshot`, `FocusTime`, `IntervalMs`, `MaxDataPoints`, and `RefID` are also copied to the top-level request dictionary.

Panopticon query text and wrappers expand q-literal macros such as `{TimeWindowStart}`, `{TimeWindowEnd}`, `{Snapshot}`, `{FocusTime}`, `$TimeWindowStart`, `{TimeWindowStart:yyyy-MM-dd HH:mm:ss}`, `{IntervalMs}`, `{MaxDataPoints}`, `{RefID}`, and `{UserLogin}`. Panopticon dashboard parameters such as `{symbol}` and `{symbol:,}` are expanded from Grafana variables with matching names in Panopticon mode. `Pano Wrapper` rewrites query text with one `{Query}` placeholder. `Pano Fn` calls a q function or lambda with the full request dictionary instead of evaluating query text directly.

For Panopticon dashboards where several panels share one base query and only apply different transforms or visual options, use one AsyncQ source panel and Grafana's `-- Dashboard --` datasource for dependent panels. Duplicating the same AsyncQ target in every panel sends repeated kdb+ requests.

## Cache And Frame Diagnostics

New datasource configs default to sync query caching enabled with a 60-second TTL and local disk persistence enabled. Existing explicit `queryCacheEnabled: false` or `queryCacheDiskEnabled: false` settings remain respected. Keep cache disabled for writeback/action queries or gateway calls with side effects.

Cache status and controls are exposed as datasource resources:

- `GET cache/status` and `GET cache/entries`
- `POST cache/clear`, `POST cache/clear-entry`, and `POST cache/clear-expired`
- `POST async/run-and-wait` for finite async execution from tooling

Status endpoints are read-only. Clear endpoints require datasource `Cache Controls` to be enabled and a Grafana `Admin` or `Editor` role.

Successful sync, async, and stream result frames include `frame.meta.custom.asyncqDiagnostics`. Sync frames include lightweight profile timings for decode, preparation, cache lookup, kdb call, frame parsing, and frame size. Cache-hit sync frames keep the same row/field/cell profile fields as fresh query frames. The companion `asyncq-masterdata-panel` uses this metadata to show data freshness, cache state, query diagnostics, and profile timings, and can serve as the master source panel for Dashboard datasource reuse. Its diagnostics layout keeps stable metric slots across fresh and cached results, with `n/a` or `-` for unavailable timings.

## Diagnostics

Enable datasource `Diagnostics` to write structured backend logs for sync, async, legacy async, deferred, and stream lifecycles. By default the logs contain request IDs, ref IDs, mode, query hashes, adapter function hashes, kdb+ object shapes, frame schemas, q worker/result metadata, status transitions, durations, profile timings, and errors, but not raw query text. Sync logs also include pool acquire wait, opened/reused connections, active/idle pool state, release/discard action, transport duration, cache disabled/bypass/miss/refresh/store/stale/hit status, and profile fields such as `profileKdbCallMs`, `profileFrameParseMs`, `profileFrameRows`, `profileFrameFields`, and `profileFrameCells`. q stack traces are hashed by default. `Log Query Text` is a separate opt-in switch for trusted debugging sessions only.
