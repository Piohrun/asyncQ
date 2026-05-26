# AsyncQ kdb+ Grafana datasource

AsyncQ is a Grafana 13 backend datasource for kdb+ derived from AquaQ Analytics' community kdb+ backend datasource. The synchronous query path is intentionally kept compatible with the original plugin, while panel queries can opt into helper-backed async, plugin-managed async, deferred-wrapper async, or q-driven streaming through Grafana Live.

## Compatibility goals

- Existing synchronous panel queries, variables, health checks, and alert queries continue to use the original backend query path.
- Existing query JSON fields such as `queryText`, `timeOut`, `useTimeColumn`, `timeColumn`, and `includeKeyColumns` are preserved.
- Async and stream modes are opt-in per query unless configured as datasource defaults. New datasource settings can disable either feature, but omitted settings default to enabled for backwards-compatible provisioning.
- The kdb+ request dictionary keeps the original `AQUAQ_KDB_BACKEND_GRAF_DATASOURCE` key so existing q-side handlers can continue to recognise plugin-originated requests.

## Query modes

### Sync

Sync mode is the default and keeps the upstream query envelope. The backend sends the following synchronous IPC call and expects a flat table or grouped table:

```q
({[x] value x[`Query;`Query]}; requestDict)
```

Variables and Grafana alerting use sync mode.

Each datasource instance owns a bounded sync IPC connection pool. `Sync Max Connections` defaults to `4`, so independent sync panels against the same datasource can run concurrently instead of queueing behind one handle. Set it to `1` when a legacy gateway requires strict serial access, per-handle session state, or has a low connection limit.

Sync query result caching is enabled by default for new datasource configs and is intended for read-only gateway calls. Successful sync query results are cached in memory and, by default, on local disk for `Cache TTL (s)` seconds, bounded by the memory and disk cache limits. Explicit `queryCacheEnabled: false` or `queryCacheDiskEnabled: false` settings are still respected. Cache keys include the compiled query text, time range, interval, max data points, execution/compatibility options, datasource identity, and Grafana user context, so user-specific gateway responses are not shared across users. `Cache Time Bucket (s)` defaults to `0` for exact time ranges; set it to a small value such as `5`, `30`, or `60` when relative `now` dashboards should reuse warm results briefly. `Stale TTL (s)` enables stale-while-revalidate: expired cached data is returned immediately while the backend refreshes the cache for the next query. `Cache Key` defaults to `Strict`, which includes Grafana ref ID; `Shared` omits ref ID for panels that safely share one result. Per-query cache controls can override mode, key mode, TTL, stale TTL, and time bucket. Add a q comment containing `asyncq:cache=off`, `asyncq:cache=bypass`, `asyncq:cache=refresh`, or `asyncq:cache=on` to force query-level behavior from copied q text.

### Helper Async

Helper Async mode is served through Grafana Live. The backend opens a dedicated kdb+ connection and calls q helper or gateway functions:

```q
.grafana.asyncq.async.submit requestDict
.grafana.asyncq.async.status jobId
.grafana.asyncq.async.result jobId
.grafana.asyncq.async.cancel jobId
```

The q side should return status dictionaries with these keys:

| Key | Type | Meaning |
| --- | --- | --- |
| `JobID` | char list | Stable async job ID |
| `Status` | char list | `queued`, `running`, `done`, `error`, or `cancelled` |
| `Progress` | float | Optional 0-1 progress value |
| `Error` | char list | Error text for failed jobs |
| `Message` | char list | Optional user-facing status or error message |
| `ErrorClass` | char list | Optional q/gateway error class such as `q`, `timeout`, `permission`, or `worker` |
| `StackTrace` | char list | Optional q-side stack trace; backend diagnostics log only a hash unless raw query text logging is enabled |
| `Worker` | char list | Optional worker or gateway identifier |
| `Started`, `Finished` | timestamp | Optional q-side lifecycle timestamps |
| `ResultType` | char list | Optional result description, for example `type=98;count=5` |

When the job reaches `done`, `.grafana.asyncq.async.result` must return a flat table or grouped table accepted by the existing parser.

### Plugin Async

Plugin Async mode does not require q helper functions. The backend opens a dedicated kdb+ connection, evaluates the same query expression as sync mode in a goroutine, sends Grafana Live status frames while it waits, and returns the final result frame when q replies.

This is useful as the lowest-friction migration path for long-running sync q functions. It is not a durable q-side job scheduler: cancellation is best-effort by closing the IPC connection, progress is synthetic, and work is limited by the datasource `Async Max Jobs` setting.

### Deferred Async

Deferred Async mode uses the plugin-managed async path after first expanding a wrapper expression. The wrapper must contain exactly one `{Query}` placeholder, for example:

```q
.gateway.defer[{Query}]
```

Use this when an existing q gateway can accept a wrapped call and eventually return a deferred response on the same IPC request. The plugin still reports the work through Grafana Live, so the panel does not block the normal synchronous query path.

### Stream

Stream mode is also served through Grafana Live. The backend calls:

```q
.grafana.asyncq.stream.start requestDict
.grafana.asyncq.stream.stop streamId
```

The reference helper stores the current IPC handle. q code can then push data to Grafana with:

```q
.grafana.asyncq.stream.publish[streamId; ([] time:.z.p+til 3; value:10 20 30)]
.grafana.asyncq.stream.done streamId
.grafana.asyncq.stream.error[streamId; "message"]
```

Each published payload must be a flat table or grouped table. The frontend appends streaming rows in memory up to the query's `Max Rows` setting.

Streaming panels also support an optional `Retention (s)` query setting. When set, the browser keeps only rows inside that trailing time window, still bounded by `Max Rows` as a safety cap. Leave it blank or `0` to retain by row count only.

## q helper

The helper in [q/asyncq_grafana.q](q/asyncq_grafana.q) provides the protocol functions above:

```q
\l q/asyncq_grafana.q
```

It is useful for development and for documenting the wire contract. Its async implementation evaluates work in-process, so it is not a production worker-pool scheduler. For production async queries, load compatible functions in a gateway that submits work to workers and returns quickly from `async.submit`.

## Compatibility modes

### Native AsyncQ

Native mode preserves the upstream table contract: flat kdb+ tables and grouped tables become Grafana data frames.

### AquaQ

AquaQ mode keeps the original request envelope and parser behavior while exposing the selected compatibility mode to q in the request dictionary. It is intended for gateways that want to branch on mode without changing existing table result handling.

### Panopticon

Panopticon mode broadens result coercion for table-style panels. In addition to flat tables, it accepts keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries and converts them into Grafana frames. Row dictionaries can arrive with reordered or missing keys; the parser builds the union of columns and leaves missing cells null. The request dictionary also includes:

| Key | Type | Meaning |
| --- | --- | --- |
| `Panopticon` | dict | Contains timestamp aliases such as `TimeWindowStart`, `TimeWindowEnd`, `Snapshot`, `FocusTime`, `Start`, `End`, `From`, and `To`, plus text aliases, `Interval`, `IntervalNs`, `IntervalMs`, `MaxDataPoints`, `RefID`, `OriginalQuery`, and `CompiledQuery` |
| top-level aliases | mixed | `TimeWindowStart`, `TimeWindowEnd`, `Snapshot`, `FocusTime`, `IntervalMs`, `MaxDataPoints`, and `RefID` are also copied to the top-level request dict for request-function compatibility |

Panopticon query text and Panopticon wrappers expand these q-literal macros:

| Macro | Example output |
| --- | --- |
| `{TimeWindowStart}`, `{TimeWindowEnd}`, `{Snapshot}`, `{FocusTime}` | `2026.05.24D10:00:00.000000000` |
| `$TimeWindowStart`, `$TimeWindowEnd`, `$Snapshot`, `$FocusTime` | `2026.05.24D10:00:00.000000000` |
| `{TimeWindowStartText}`, `{TimeWindowEndText}`, `{SnapshotText}`, `{FocusTimeText}` | `"2026-05-24T10:00:00Z"` |
| `{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}` | `"2026-05-24 10:00:00.000"` |
| `{Interval}`, `{IntervalNs}`, `{IntervalMs}`, `{MaxDataPoints}`, `{OrgID}` | `5000j` |
| `{RefID}`, `{UserName}`, `{UserLogin}`, `{UserEmail}`, `{DatasourceName}`, `{DatasourceUID}` | `"A"` |

Panopticon dashboard parameters are also expanded in Panopticon mode when a Grafana variable with the same name exists. For example, a Grafana variable named `symbol` lets a pasted query keep `{symbol}` or `{symbol:,}` syntax. Multi-value parameters use the delimiter after `:`, defaulting to `,` when no delimiter is provided. Values are inserted as raw text, matching Panopticon-style query substitution, so keep any required q quoting in the pasted query.

If a Panopticon dashboard runs one base datasource query and several panels only transform or visualize that same result differently, model that in Grafana with the built-in Dashboard datasource rather than duplicating the AsyncQ query in every panel. Create one AsyncQ source panel with the shared q query, then set dependent panels to datasource `-- Dashboard --` and select the source panel in `Use results from panel`. This makes Grafana issue one kdb+ query and reuse the returned frames across panels. Grafana documents this workflow as [sharing query results with another panel](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/query-transform-data/share-query/).

Two optional Panopticon invocation controls are available at datasource and query level:

- `Pano Wrapper` rewrites the query expression before execution. It must contain exactly one `{Query}` placeholder, for example `.pano.run[{Query};{TimeWindowStart};{TimeWindowEnd}]`.
- `Pano Fn` is a q function or lambda that accepts the full request dictionary. When set, the backend calls it instead of directly evaluating query text, for example `{[req] .pano.run req}`.

This does not make Grafana a byte-for-byte Panopticon runtime. Grafana variables still need to be created, and panel configuration, callbacks, and client-side Panopticon behavior still need deliberate mapping, but simple function-driven table panels are much closer to copy/paste compatible.

## LLM migration skill

This repo includes a Codex-style skill for LLM-assisted Panopticon migrations:

- [skills/panopticon-grafana-migration/SKILL.md](skills/panopticon-grafana-migration/SKILL.md)
- [skills/panopticon-grafana-migration/references/asyncq-migration.md](skills/panopticon-grafana-migration/references/asyncq-migration.md)

Use it as the instruction bundle when asking an LLM to translate Panopticon q-backed table panels into Grafana panels that use this datasource.

The current compatibility matrix is captured in the skill reference and mirrored by the local demo dashboard:

```text
http://localhost:3000/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix
```

The proposed design for configurable same-port legacy async adapters lives in [research/legacy-async-adapter.md](research/legacy-async-adapter.md). That design is intentionally separate from the current runtime until real gateway protocols can be compared against it.

## Diagnostics

Datasource config includes two diagnostic switches:

- `Diagnostics` writes structured backend logs for query receipt, preparation, q execution, result parsing, frame send, cancellation, and completion across sync, helper async, plugin async, deferred async, and stream paths.
- `Log Query Text` additionally writes raw query and wrapper text to backend logs. It is disabled by default because q text can contain sensitive table names, filters, identifiers, or credentials.

Safe diagnostics logs include request IDs, Grafana ref IDs, execution and compatibility modes, time-range metadata, query and wrapper SHA-256 hashes, kdb+ object descriptions, Grafana frame schemas, durations, job IDs, stream IDs, q worker IDs, q result metadata, status changes, and errors. Sync diagnostics also include pool acquire wait, opened/reused connections, active/idle pool state, release/discard action, transport duration, query-cache status (`disabled`, `bypassed`, `miss`, `refresh`, `stored`, `stale`, or `hit`), and cache storage (`memory`, `disk`, or `memory+disk`). These fields help distinguish plugin-side pool saturation, q-side gateway serialization, and warm-cache reuse. The same diagnostics are attached to returned frames under `frame.meta.custom.asyncqDiagnostics`, so panels can display freshness/cache state without scraping logs. q stack traces are hashed by default and logged verbatim only with `Log Query Text`. The local demo provisions `diagnosticsEnabled: true` and `diagnosticsLogQueryText: false` so you can inspect behavior without exposing raw q text.

## Cache And Master Data Panel

Sync query caching has an in-memory layer and an optional self-contained local disk layer. New datasource configs default to cache enabled, 60-second TTL, 128 memory entries, disk cache enabled, 10,000 disk entries, and a 1 GiB disk budget. Existing explicit `queryCacheEnabled: false` or `queryCacheDiskEnabled: false` settings remain respected. Disable cache for writeback/action queries or gateway calls with side effects.

The backend exposes datasource resources for cache status and control:

- `GET cache/status` and `GET cache/entries`
- `POST cache/clear`, `POST cache/clear-entry`, and `POST cache/clear-expired`

Cache status is read-only for any user who can query the datasource. Cache clear actions require `Cache Controls` to be enabled on the datasource and a Grafana `Admin` or `Editor` role.

This repo also includes a companion panel plugin, `asyncq-masterdata-panel`. Use it as a master data panel that runs one AsyncQ query, exposes freshness/cache diagnostics, and can be referenced by dependent panels through Grafana's `-- Dashboard --` datasource. Freshness mode is a small widget-style view for main dashboard tabs.

## Security

This datasource preserves the upstream behavior of sending user-entered q text to kdb+. Treat that text as untrusted input unless your environment already has strong controls. For production gateways, prefer allowlisted function calls, `reval` where applicable, `-b`, authenticated IPC, query timeouts, memory limits, and separate worker processes.

## Returned data

In native and AquaQ compatibility modes, queries must return either:

- a flat table, kdb+ type 98
- a grouped table, kdb+ type 99 where key and value are congruent tables

Panopticon compatibility mode also accepts keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries. Columns should have stable scalar types where possible; mixed numeric row-dictionary values are widened to float columns, sparse row dictionaries produce nullable columns, and mixed generic-list columns are converted to strings. String columns and grouped table keys are supported by the inherited parser. If `Use Custom Time Column` is enabled, the named column must exist in every returned frame.

## Development

Install dependencies and run checks:

```bash
npm install
npm test
npm run build:all
go test ./pkg/...
```

Validate the q helper if a local `q` binary is available:

```bash
printf '\\\\\n' | q q/asyncq_grafana.q -q -T 5 -w 1024 -u 1 -b
```

The datasource frontend build writes `dist/module.js` and copies datasource metadata. The companion panel build writes `dist-panel/asyncq-masterdata-panel/module.js`.

## Local demo

A runnable demo is available in [demo/README.md](demo/README.md). It provisions a Grafana 13 dashboard and datasource, and starts a local q process with sync, async, and streaming sample data:

```bash
./scripts/start-demo-local.sh
```

The sync pool probe dashboard is provisioned at:

```text
http://localhost:3000/d/asyncq-sync-pool/asyncq-sync-connection-pool
```
