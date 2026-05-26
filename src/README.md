# AsyncQ kdb+ Grafana datasource

AsyncQ is a Grafana 13 backend datasource for kdb+ derived from AquaQ Analytics' community kdb+ backend datasource. The synchronous query path is intentionally kept compatible with the original plugin, while panel queries can opt into helper async, plugin-managed async, deferred-wrapper async, or q-driven streaming through Grafana Live.

## Query modes

Sync mode is the default and keeps the upstream request envelope. Variables and alerting use sync mode.

Each datasource instance has a bounded sync IPC connection pool. `Sync Max Connections` defaults to `4`, allowing independent sync panels on the same datasource to run concurrently. Set it to `1` for strict legacy serial behavior or gateways with per-handle state/low connection limits.

Helper Async mode uses Grafana Live and calls q helper functions:

```q
.grafana.asyncq.async.submit requestDict
.grafana.asyncq.async.status jobId
.grafana.asyncq.async.result jobId
.grafana.asyncq.async.cancel jobId
```

Plugin Async mode uses Grafana Live but does not require q helper functions. The backend opens a dedicated IPC connection, evaluates the query in a goroutine, emits status frames while waiting, and returns the final q result when it arrives.

Deferred Async mode first expands a wrapper containing exactly one `{Query}` placeholder, then runs that expression through Plugin Async. Use this for q gateways that already support deferred responses or wrapper-based submission.

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

## Diagnostics

Enable datasource `Diagnostics` to write structured backend logs for sync, async, deferred, and stream lifecycles. By default the logs contain request IDs, ref IDs, mode, query hashes, kdb+ object shapes, frame schemas, q worker/result metadata, status transitions, durations, and errors, but not raw query text. q stack traces are hashed by default. `Log Query Text` is a separate opt-in switch for trusted debugging sessions only.
