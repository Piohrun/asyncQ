# AsyncQ Migration Reference

## Compatibility Level

AsyncQ is close for Panopticon table-style panels where the q query or function returns primitive table-like data. It is not a Panopticon dashboard importer. It does not automatically translate Panopticon visual layout, actions, client transforms, custom coloring, or dashboard interaction models.

Use a Table panel as the first migration target. Convert to Time series, State timeline, Bar chart, or another Grafana visualization only after the returned data matches.

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

## Mode Selection

| Panopticon source behavior | AsyncQ setting |
| --- | --- |
| Plain q expression/function call | `compatibilityMode="panopticon"`, `executionMode="sync"` first, then `pluginAsync` |
| Long-running direct query | `executionMode="pluginAsync"` |
| q gateway already has async submit/status/result/cancel | `executionMode="async"` |
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

Action parameters and dashboard variables must be mapped to Grafana variables manually.

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

Unsupported or fragile shapes:

- nested dictionaries as cells
- generic lists of non-row objects
- deeply nested list columns
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

For production debugging, prefer adding a q adapter that logs job ID, ref ID, time range, query hash, and result type on the q side without logging sensitive query text.

## Safety Rules

- Do not run destructive q operations during migration validation.
- Avoid `value` on untrusted pasted query text in production gateways; use allowlisted function wrappers where possible.
- Avoid creating symbols from unbounded Grafana variables.
- Keep dashboard timezone UTC unless the environment explicitly handles conversion.
