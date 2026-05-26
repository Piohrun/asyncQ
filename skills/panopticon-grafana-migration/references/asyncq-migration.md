# AsyncQ Migration Reference

## Compatibility Level

AsyncQ is close for Panopticon table-style panels where the q query or function returns primitive table-like data. It is not a Panopticon dashboard importer. It does not automatically translate Panopticon visual layout, actions, client transforms, custom coloring, or dashboard interaction models.

Use a Table panel as the first migration target. Convert to Time series, State timeline, Bar chart, or another Grafana visualization only after the returned data matches.

When a Panopticon dashboard runs one datasource query and several panels only transform or visualize that same result differently, map it to Grafana's Dashboard datasource. Use one AsyncQ source panel for the shared q query; configure dependent panels with datasource `-- Dashboard --` and `Use results from panel` pointing at the source panel. This avoids repeated kdb+ calls and is the closest Grafana equivalent to Panopticon shared result reuse.

## Compatibility Matrix

Verdict legend:

- Direct: paste the query/function call and point AsyncQ at the same q port.
- Config-only: no q or plugin code change, but set wrapper, request function, variables, or panel options.
- Adapter needed: same q port can still be the target, but AsyncQ needs a plugin adapter or a small q shim to speak the existing gateway protocol.
- Visual rewrite: data can be retrieved, but Panopticon visual/interactivity must be rebuilt in Grafana.
- Not portable: no reliable equivalent without changing the source application behavior.

AsyncQ sync execution uses a per-datasource kdb+ IPC connection pool. `syncMaxConnections` defaults to `4`, so direct sync panels can run concurrently against the same datasource instance. Set `syncMaxConnections=1` when validating old Panopticon-serving ports that rely on strict serial requests, per-handle session state, or limited connection capacity.

AsyncQ can also cache successful sync query results in the datasource instance. New datasource configs default to a 60-second memory cache plus a self-contained local disk cache; existing explicit `queryCacheEnabled=false` or `queryCacheDiskEnabled=false` settings remain respected. Keep cache disabled while validating queries with side effects. Cache keys include the compiled query, time range, interval, max data points, datasource identity, and Grafana user context. `queryCacheTimeBucketSeconds` defaults to `0` for exact time ranges; set it to a small bucket such as `5`, `30`, or `60` when relative `now` dashboard reloads should reuse warm results. `queryCacheStaleTTLSeconds` enables stale-while-revalidate: the plugin returns stale cached data immediately and refreshes the cache in the background for the next query. `queryCacheKeyMode=strict` includes Grafana ref ID; `queryCacheKeyMode=shared` omits ref ID and should only be used when the q request function does not depend on ref ID. Per-query controls can override mode, key mode, TTL, stale TTL, and time bucket. Do not enable cache for writeback/action queries or gateway calls with side effects; add a q comment containing `asyncq:cache=off`, `asyncq:cache=bypass`, `asyncq:cache=refresh`, or `asyncq:cache=on` to force query-level behavior.

### Query And Execution

| Panopticon behavior | AsyncQ status | AsyncQ configuration | Notes and limits |
| --- | --- | --- | --- |
| Plain q expression or function call over sync IPC | Direct | `compatibilityMode="panopticon"`, `executionMode="sync"` for validation; tune `syncMaxConnections` per gateway | Highest-confidence copy/paste case when the result shape is supported. Set `syncMaxConnections=1` if the legacy port must serialize requests. |
| Long-running blocking query over the same q port | Direct | Validate with `sync`, then use `executionMode="pluginAsync"` | Keeps Grafana responsive. It does not make the q gateway itself non-blocking; cancellation is best-effort by closing the plugin-owned IPC connection. |
| Existing gateway accepts a q expression wrapped in a known call | Config-only | Set `panopticonQueryWrapper` with exactly one `{Query}` | Example: `.gateway.run[{Query};{TimeWindowStart};{TimeWindowEnd}]`. |
| Existing panel passes a full request object into a q function | Config-only if the function can accept AsyncQ's request dict; otherwise adapter needed | Set `panopticonRequestFunction` | AsyncQ passes a request dictionary with `Query`, `Panopticon`, top-level time aliases, datasource, user, and execution metadata. Proprietary envelopes need mapping. |
| Panopticon source uses positional function args | Config-only or adapter needed | Prefer query text like `.fn[arg1;arg2]`; use wrapper/request function if args come from time range or variables | Works when args are expressible as q literals after macro/variable expansion. |
| Multiple panels share one base query/result | Config-only | One AsyncQ source panel or `asyncq-masterdata-panel`, dependent panels use Grafana datasource `-- Dashboard --` and `Use results from panel` | Do not duplicate the same AsyncQ target in each dependent panel; duplicate targets produce repeated kdb+ requests. The companion master-data panel also exposes freshness and cache controls. |
| Dashboard reopens should use warm server-side results | Config-only if stale data is acceptable | Use datasource `queryCacheEnabled`, `queryCacheDiskEnabled`, `queryCacheTTLSeconds`, optionally set `queryCacheStaleTTLSeconds` and `queryCacheTimeBucketSeconds`, and keep Dashboard datasource sharing for dependent panels | This approximates Panopticon query result cache. Cache only successful sync results and is local to the Grafana server/datasource instance. Disk cache persists across plugin restarts; stale-while-revalidate makes reopen feel instant, but the refreshed result appears on the next Grafana query/refresh unless the panel uses a live path. |
| Duplicated panels should share the same cached q result | Config-only if the q path ignores ref ID | Set datasource or query `queryCacheKeyMode=shared`; prefer Dashboard datasource when a panel explicitly derives from a source panel | Shared cache mode omits ref ID from the cache key. Do not use it when a Panopticon request function branches on panel/ref ID. |
| Panopticon server-side async submit/status/result/cancel | Config-only when the gateway exposes callable functions and parseable envelopes; otherwise adapter needed | Use `executionMode="legacyAsync"` and configure submit/status/result/cancel functions, request mode, response paths, and status value mappings. Use `executionMode="async"` only for the `.grafana.asyncq.async.*` helper contract | Discover job ID field, status values, result function, error fields, expiry, and cancel semantics before configuring. Callback/deferred protocols still need a specific adapter. |
| Deferred response or callback over IPC handle | Adapter needed | Current `deferredAsync` only wraps a query then runs Plugin Async | True q `neg` callback/deferred protocols need a plugin adapter that registers the callback handle and translates returned messages. |
| Gateway only accepts serialized/proprietary Panopticon envelopes | Adapter needed | Implement envelope builder in plugin or q shim | Do not claim copy/paste until the envelope schema is known. |
| Streaming subscription/push panel | Adapter needed | `executionMode="stream"` requires `.grafana.asyncq.stream.start/stop` or equivalent adapter | Panopticon stream definitions do not copy directly unless their protocol is implemented. |
| Template variable query | Direct for sync one-column outputs | Variables use sync query execution | Async/live modes are not used for Grafana variables. Return a simple vector or single-column table. |
| Alert query | Direct only for sync-compatible query paths | Use sync-compatible settings | Grafana alerting does not use the panel live-stream path. Avoid async/stream-only assumptions. |

### Parameters And Context

| Panopticon feature | AsyncQ status | Mapping | Notes and limits |
| --- | --- | --- | --- |
| `{TimeWindowStart}`, `$TimeWindowStart` | Direct | q timestamp literal from Grafana range start | UTC timestamp literal. |
| `{TimeWindowEnd}`, `$TimeWindowEnd` | Direct | q timestamp literal from Grafana range end | UTC timestamp literal. |
| `{Snapshot}`, `{FocusTime}`, dollar forms | Config-only | Currently mapped to Grafana range end | Good for many table panels; not a full Panopticon playback/focus model. |
| `{Start}`, `{End}`, `{From}`, `{To}` | Direct | q timestamp literals | Aliases for Grafana range start/end. |
| Formatted time macros | Direct | q string literal, e.g. `{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}` | Format support covers common Java-style date tokens; validate unusual formats. |
| `{Interval}`, `{IntervalNs}`, `{IntervalMs}` | Direct | q long | Derived from Grafana query interval. |
| `{MaxDataPoints}`, `{RefID}`, `{OrgID}`, user/datasource macros | Direct | q long or q string | Available in query text, wrapper, and request dict. |
| Panopticon dashboard/action parameters | Config-only | Create Grafana variables with matching names; AsyncQ expands `{parameter}` and `{parameter:delimiter}` in Panopticon mode | Values are inserted as raw text. Multi-select quoting and symbol-list semantics must still match the q query. |
| Cascading/filter variables | Config-only or Visual rewrite | Use Grafana variables backed by sync q queries | Panopticon-specific filter UX must be rebuilt with Grafana variable controls. |
| Panopticon session, entitlement, workbook state | Adapter needed | Reproduce expected fields in `panopticonRequestFunction` or plugin adapter | Same credentials may not be enough if gateway expects Panopticon session IDs or entitlements. |
| Client-side calculated parameters | Visual rewrite or q adapter | Move calculation into q, Grafana transform, or variable expression | Do not assume Panopticon client transforms exist in Grafana. |

### Result Shapes

| q result from migrated query | AsyncQ status | Grafana frame behavior | Notes and limits |
| --- | --- | --- | --- |
| Flat table | Direct | One frame with table columns | Best target shape. |
| Keyed table | Direct in Panopticon compatibility | Key and value columns flattened into one frame | Duplicate column names are disambiguated. |
| Grouped table | Direct in native/AquaQ; usable in Panopticon if parsed as keyed table | Frames or flattened frame depending mode | Validate field names before changing visualization. |
| Symbol-keyed primitive dictionary | Direct | One-row frame with keys as columns | Good for summary panels. |
| Single key mapped to vector | Direct | One-column frame with vector rows | Useful for simple variable or value lists. |
| Atom | Direct | One `value` column, one row | Good for Stat/Table panels. |
| Primitive vector | Direct | One `value` column, many rows | Good for simple lists. |
| Char vector | Direct | One `value` string row | Treated as a single string, not one row per char. |
| List of row dictionaries | Direct | Union of keys becomes columns | Missing cells become null. Mixed numeric values widen to float. |
| Sparse or reordered row dictionaries | Direct | Stable union of keys | Validate null handling in Grafana field display. |
| Generic list column with mixed scalar/string values | Direct | String column | Useful for Panopticon table columns with q type `0`; nested values are stringified. |
| Nested dictionaries/lists as cell values | Adapter needed | Return an explicit flat table from q | Grafana frames need primitive-ish column values. |
| Generic list result of arbitrary non-row objects | Direct but adapter recommended | One stringified `value` column | Copy/paste may render, but explicit table/list-of-dicts output is easier to reason about. |
| Panopticon-only client transform output | Visual rewrite or q adapter | Recreate transform in q or Grafana | The plugin only sees q result data, not Panopticon client-side state. |

### Visuals And Interactions

| Panopticon panel feature | AsyncQ/Grafana status | Migration approach | Notes and limits |
| --- | --- | --- | --- |
| Basic table/grid | Direct after data validation | Grafana Table panel | Field order, widths, sorting, and formatting are manual Grafana settings. |
| Time-series line/area chart | Config-only | Return a real time column; enable `useTimeColumn` if needed | Grafana expects temporal field plus numeric value fields. |
| Bar, stat, gauge, state timeline | Config-only | Use matching Grafana visualization after table validation | Field overrides usually replace Panopticon visual settings. |
| Conditional coloring/thresholds | Visual rewrite | Grafana thresholds/value mappings/field overrides | Data can copy; styling rules need translation. |
| Multiple panels derived from one shared result set | Config-only | Source panel queries AsyncQ; dependent panels use Dashboard datasource plus transformations | Reduces q load and matches Panopticon shared-query behavior better than duplicated panel queries. |
| Panopticon heatmaps, order books, custom finance widgets | Visual rewrite or custom panel | Start with table-compatible data, then map to native Grafana/custom plugin | Query may be portable; exact visual behavior usually is not. |
| Drilldowns and navigation | Visual rewrite | Grafana data links/dashboard links | URL and variable mapping must be rebuilt. |
| Panopticon actions/writebacks | Not portable through datasource alone | Build explicit Grafana app/plugin or secured backend endpoint | Datasource queries should not be treated as a generic writeback/action channel. |
| Cross-panel brushing, focus/playback behavior | Visual rewrite | Approximate with Grafana variables/time range where possible | Grafana interaction model is different. |
| Full dashboard layout/theme import | Visual rewrite | Rebuild dashboard JSON manually or with a future importer | AsyncQ only handles datasource/query compatibility. |

### Master Data And Cache Controls

The companion panel plugin `asyncq-masterdata-panel` is useful during Panopticon migrations:

- Master data mode runs the shared AsyncQ query and shows a compact data preview plus diagnostics.
- Freshness mode is a widget-style panel for main dashboard tabs. It reports the newest timestamp in the returned frame and cache status.
- Diagnostics mode shows selected `frame.meta.custom.asyncqDiagnostics` fields.
- Cache-control buttons call AsyncQ datasource resources: `cache/status`, `cache/clear`, `cache/clear-entry`, and `cache/clear-expired`.
- Cache status is read-only; clear actions require datasource `queryCacheControlEnabled` and a Grafana `Admin` or `Editor` role.

Use it when a Panopticon panel acted as the hidden or visible source query for several visual panels. Configure dependent panels with datasource `-- Dashboard --`, `panelId` set to the master-data panel ID, and their own Grafana transformations/field overrides.

### Decision Rules

1. If the query is plain q, wrapper-based, or request-function-based and returns a supported shape, call it Direct or Config-only.
2. If multiple panels share the same underlying Panopticon result, use one AsyncQ source panel and Dashboard datasource dependents before considering plugin-side caching or q changes.
3. If the q port expects an async/deferred/streaming/session envelope that AsyncQ does not currently generate, call it Adapter needed and document the exact protocol fields.
4. If the data can be retrieved but the Panopticon value is mostly visual styling, interaction, or client transform behavior, call it Visual rewrite.
5. If the feature performs writeback/action side effects through Panopticon-only infrastructure, call it Not portable through the datasource alone.

## Same-Port Legacy Gateway Migration

When the goal is to point Grafana at the same q ports Panopticon used, optimize for no q-side changes first.

Feasible without modifying the gateway/RDB:

- Plain sync q expressions or function calls.
- Blocking gateway calls wrapped by AsyncQ `pluginAsync`, which keeps Grafana responsive while the existing q call runs on a dedicated IPC connection.
- Panopticon-style time macros expanded by the plugin before submission.
- Shared base-query panels using Grafana's Dashboard datasource to reuse one AsyncQ source panel result.
- Result parsing for flat tables, keyed tables, primitive dictionaries, atoms, vectors, char vectors, and lists of row dictionaries.
- Request-dictionary invocation when the existing gateway already accepts a compatible function/lambda call through `panopticonRequestFunction`.

Not automatically feasible without discovering and reproducing the existing client protocol:

- True server-side async where Panopticon submits a job, receives an ID, polls status, and fetches results can use `legacyAsync` if the gateway exposes callable submit/status/result/cancel functions and parseable envelopes. Deferred/callback-only protocols still need a specific adapter.
- Push streaming or subscriptions unless the gateway exposes a known protocol the plugin can speak.
- Session state, entitlements, callback handles, or request envelopes that are specific to Panopticon.
- Gateways that only accept a proprietary Panopticon envelope rather than plain q text or a documented request dict.

Discovery workflow for source code and q ports:

1. Inspect gateway source for `.z.pg`, `.z.ps`, dispatch tables, auth/session checks, request parsing, logging, job tables, handles, `neg` async sends, timers, and Panopticon-named functions.
2. Identify whether Panopticon sent raw query text, a function call, positional args, a request dictionary, a serialized object, or a deferred/callback subscription.
3. Use read-only IPC probes against development ports only. Start with health checks and harmless expressions such as `1+1`, then safe metadata calls. Do not run mutating or broad data scans.
4. Match the closest current AsyncQ mode:
   - raw blocking call -> `sync` for validation, then `pluginAsync`
   - existing full request dict function -> `panopticonRequestFunction`
   - wrapper around query -> `panopticonQueryWrapper`
   - `.grafana.asyncq.*` helper functions -> `async` or `stream`
   - existing submit/status/result/cancel functions with envelopes -> `legacyAsync`
5. If the gateway has a different async protocol, configure the legacy adapter when possible: submit function, status function, result function, cancel function, request mode, job ID path, status path, progress/message/error paths, payload path, and status value mappings. If the protocol uses callback handles, proprietary serialization, or side-channel delivery, document the missing adapter contract before patching.

Do not claim the plugin can use arbitrary legacy async protocols by configuration. `legacyAsync` covers pull-style submit/status/result/cancel protocols; callback/deferred/push-specific protocols require additional adapter work once the unchanged gateway contract is understood.

For configurable adapter details, see `research/legacy-async-adapter.md` in this repository.

## Query Target Template

```json
{
  "datasource": {
    "type": "asyncq-kdbbackend-datasource",
    "uid": "<datasource-uid>"
  },
  "refId": "A",
  "queryText": "<pasted q query or function call>",
  "executionMode": "pluginAsync",
  "compatibilityMode": "panopticon",
  "timeOut": 10000,
  "useTimeColumn": false,
  "timeColumn": "time",
  "includeKeyColumns": false,
  "pollIntervalMs": 500,
  "maxStreamRows": 1000
}
```

Use `sync` while validating. Switch to `pluginAsync` when the query is correct and may be slow.

For an unchanged q gateway that already exposes pull-style async functions, use the legacy adapter shape:

```json
{
  "executionMode": "legacyAsync",
  "compatibilityMode": "panopticon",
  "queryText": "<pasted q query or function call>",
  "legacyAsyncSubmit": ".gw.submit",
  "legacyAsyncStatus": ".gw.status",
  "legacyAsyncResult": ".gw.result",
  "legacyAsyncCancel": ".gw.cancel",
  "legacyAsyncRequestMode": "requestDict",
  "legacyAsyncJobIDPath": "jobId",
  "legacyAsyncStatusPath": "status",
  "legacyAsyncProgressPath": "progress",
  "legacyAsyncMessagePath": "message",
  "legacyAsyncErrorPath": "error",
  "legacyAsyncPayloadPath": "result",
  "legacyAsyncQueuedValues": "queued,pending",
  "legacyAsyncRunningValues": "running,executing",
  "legacyAsyncDoneValues": "done,complete,completed",
  "legacyAsyncErrorValues": "error,failed",
  "legacyAsyncCancelledValues": "cancelled,canceled"
}
```

Set `legacyAsyncRequestMode` to `compiledQueryText` if the gateway expects a q string after Panopticon macro expansion, `queryText` if it expects the original pasted text, or `panopticonDict` if it expects only Panopticon-style context fields. The result function can return either a raw table/keyed table accepted by the current compatibility mode or an envelope containing `legacyAsyncPayloadPath`.

## Shared Result Panels

Use Grafana's Dashboard datasource when a Panopticon dashboard used one shared datasource result for several panels:

1. Create a source panel that uses AsyncQ and contains the shared q query. Prefer `asyncq-masterdata-panel` when freshness or cache controls are useful.
2. Validate the source panel as a Table panel first.
3. Create dependent panels with datasource `-- Dashboard --`.
4. In each dependent panel, set `Use results from panel` to the source panel.
5. Apply panel-specific Grafana transformations, field overrides, filters, thresholds, and visualization settings.

Do not paste the same expensive AsyncQ query into every dependent panel unless you intentionally want separate q requests. Direct duplicate AsyncQ targets are independent Grafana queries: sync targets run through the datasource's bounded sync IPC pool, and `pluginAsync` targets use separate plugin-managed IPC calls rather than a shared result.

Dashboard datasource target JSON shape:

```json
{
  "datasource": {
    "type": "datasource",
    "uid": "-- Dashboard --"
  },
  "panelId": 1,
  "refId": "A"
}
```

## Mode Selection

| Panopticon source behavior | AsyncQ setting |
| --- | --- |
| Plain q expression/function call | `compatibilityMode="panopticon"`, `executionMode="sync"` first, then `pluginAsync` |
| Long-running direct query | `executionMode="pluginAsync"` |
| Shared result reused by several panels | One AsyncQ or `asyncq-masterdata-panel` source panel; dependent panels use datasource `-- Dashboard --` |
| q gateway already has AsyncQ helper functions | `executionMode="async"` |
| q gateway already has non-AsyncQ submit/status/result/cancel functions | `executionMode="legacyAsync"` with configured function names, request mode, response paths, and status mappings |
| Query must be wrapped before evaluation | Set `panopticonQueryWrapper`, exactly one `{Query}` |
| Pass-to-function panel | Set `panopticonRequestFunction` to a q function/lambda accepting `req` |
| True push stream | Requires AsyncQ streaming helper or a q-side adapter; do not assume Panopticon stream definitions copy directly |

## Supported Panopticon Parameters

These expand in `queryText` and `panopticonQueryWrapper`:

| Parameter | Expansion |
| --- | --- |
| `{TimeWindowStart}`, `$TimeWindowStart` | q timestamp literal |
| `{TimeWindowEnd}`, `$TimeWindowEnd` | q timestamp literal |
| `{Snapshot}`, `$Snapshot` | q timestamp literal, currently Grafana range end |
| `{FocusTime}`, `$FocusTime` | q timestamp literal, currently Grafana range end |
| `{Start}`, `{From}` | q timestamp literal, Grafana range start |
| `{End}`, `{To}` | q timestamp literal, Grafana range end |
| `{TimeWindowStartText}` and related `Text` forms | q string literal, RFC3339Nano |
| `{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}` | q string literal using the requested date format |
| `{Interval}`, `{IntervalNs}` | q long, nanoseconds |
| `{IntervalMs}` | q long, milliseconds |
| `{MaxDataPoints}` | q long |
| `{RefID}` | q string |
| `{OrgID}` | q long |
| `{UserName}`, `{UserLogin}`, `{UserEmail}` | q strings |
| `{DatasourceName}`, `{DatasourceUID}` | q strings |

Dashboard/action parameters can be kept in Panopticon curly-brace form when a Grafana variable with the same name exists:

| Parameter | Expansion |
| --- | --- |
| `{symbol}` | Raw Grafana variable value for `symbol` |
| `{symbol:,}` | Multi-value `symbol` joined with `,` |
| `{symbol: }` | Multi-value `symbol` joined with a space |

Values are inserted as raw text, mirroring Panopticon-style query substitution. Keep q quoting in the query, for example ``sym=`{symbol}`` for a single symbol or ``sym in `$" " vs "{symbols: }"`` for a multi-symbol variable joined by spaces. Grafana variables must still be created with the correct values; the plugin does not infer dashboard parameter definitions from a Panopticon workbook.

## Request Function Shape

When `panopticonRequestFunction` is set, AsyncQ calls that function with the full request dictionary. Common fields:

- `req\`Query` is a dict with `RefID`, `Query`, `OriginalQuery`, `CompiledQuery`, `MaxDataPoints`, `Interval`, `TimeRange`, `PanopticonQueryWrapper`, and `PanopticonRequestFunction`.
- `req\`Panopticon` is a dict with time aliases, text aliases, interval metadata, `RefID`, `Query`, `OriginalQuery`, and `CompiledQuery`.
- Top-level aliases also exist: `req\`TimeWindowStart`, `req\`TimeWindowEnd`, `req\`Snapshot`, `req\`FocusTime`, `req\`IntervalMs`, `req\`MaxDataPoints`, `req\`RefID`.

Example:

```q
.migrate.panel:{[req]
  qd:req`Query;
  p:req`Panopticon;
  / Keep this function pure where possible.
  select from trade where time within (p`TimeWindowStart;p`TimeWindowEnd), sym=`AAPL
  }
```

Configure:

```json
{
  "compatibilityMode": "panopticon",
  "executionMode": "pluginAsync",
  "queryText": "1+1",
  "panopticonRequestFunction": ".migrate.panel"
}
```

## Result Shape Mapping

| q result | Grafana frame behavior |
| --- | --- |
| Flat table | One frame with table columns |
| Keyed table | Key and value columns flattened into one frame |
| Primitive dictionary | One frame with dictionary keys as columns |
| Atom | One `value` column, one row |
| Vector | One `value` column, many rows |
| Char vector | One `value` string row |
| List of row dictionaries | Union of row keys becomes columns |
| Sparse row dictionaries | Missing cells are null |
| Mixed numeric row values | Values widen to float |
| Mixed generic-list table columns | Values become strings |

Unsupported or fragile shapes:

- nested dictionaries as cells
- generic lists of non-row objects, unless stringified display is acceptable
- deeply nested list columns, unless stringified display is acceptable
- client-side Panopticon transforms that do not exist in q output
- visual-only Panopticon state

Return an explicit table from q for these cases.

## Troubleshooting

Current logging is enough for common failures but not yet a full migration observability layer.

Useful Grafana backend log signals:

- `Opening connection to kdb+`, `Dialled kdb+ successfully`, `Error establishing kdb connection`
- `Partial data response error`
- `returned unsupported kdb+ object (...)`
- `unable to parse Panopticon ...`
- `temporal column override ... is not present`
- `Panopticon query wrapper must contain exactly one {Query}`
- `asyncQ helper unavailable`
- `async job limit reached`

Common fixes:

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| No data, connection refused | q process not reachable | Check host/port, firewall, q `-p`, datasource credentials |
| Query works in q but not Grafana | Missing macro or request wrapper | Use Panopticon compatibility and inspect `OriginalQuery`/`CompiledQuery` |
| Table panel says no data | Async first load pending or result shape unsupported | Try `sync`; return a flat table adapter |
| Missing time column error | `useTimeColumn=true` but returned frame lacks that column | Disable custom time column or return a real time column |
| Helper async never completes | q helper contract mismatch | Test `.grafana.asyncq.async.submit/status/result/cancel` directly |
| Request function fails | Expects different request envelope | Use top-level aliases or `req\`Panopticon`; add a small q shim |
| Panels still resolve one by one after raising `syncMaxConnections` | Target q gateway/process serializes requests or datasource config was not applied | Inspect diagnostics: high `syncPoolAcquireWaitMs` means plugin pool saturation; low acquire wait with high `syncTransportMs` means the target q side is taking/serializing the work |
| Reopened dashboard still hits kdb+ | Query cache disabled, cache TTL expired, exact relative `now` timestamps changed, query key changed, ref ID differs under strict key mode, or query contains cache bypass marker | Enable `queryCacheEnabled`, increase `queryCacheTTLSeconds`, set `queryCacheTimeBucketSeconds` for relative ranges, use `queryCacheKeyMode=shared` only for ref-ID-independent queries, keep variables stable, and inspect `queryCacheStatus` diagnostics |
| Reopened dashboard shows old data once, then fresh data on next refresh | Stale-while-revalidate is active | This is expected for standard Grafana query panels. The plugin can refresh its backend cache after returning stale data, but Grafana will not receive the refreshed frame until another panel query unless using a live/stream path. |

For production debugging, prefer adding a q adapter that logs job ID, ref ID, time range, query hash, and result type on the q side without logging sensitive query text.

## Safety Rules

- Do not run destructive q operations during migration validation.
- Avoid `value` on untrusted pasted query text in production gateways; use allowlisted function wrappers where possible.
- Avoid creating symbols from unbounded Grafana variables.
- Keep dashboard timezone UTC unless the environment explicitly handles conversion.
