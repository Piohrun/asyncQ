# AsyncQ kdb+ Grafana datasource

AsyncQ is a Grafana 13 backend datasource for kdb+ derived from AquaQ Analytics' community kdb+ backend datasource. The synchronous query path is intentionally kept compatible with the original plugin, while panel queries can opt into asynchronous execution or q-driven streaming through Grafana Live.

## Compatibility goals

- Existing synchronous panel queries, variables, health checks, and alert queries continue to use the original backend query path.
- Existing query JSON fields such as `queryText`, `timeOut`, `useTimeColumn`, `timeColumn`, and `includeKeyColumns` are preserved.
- Async and stream modes are opt-in per query. New datasource settings can disable either feature, but omitted settings default to enabled for backwards-compatible provisioning.
- The kdb+ request dictionary keeps the original `AQUAQ_KDB_BACKEND_GRAF_DATASOURCE` key so existing q-side handlers can continue to recognise plugin-originated requests.

## Query modes

### Sync

Sync mode is the default and matches the upstream datasource behavior. The backend sends the following synchronous IPC call and expects a flat table or grouped table:

```q
({[x] value x[`Query;`Query]}; requestDict)
```

Variables and Grafana alerting use sync mode.

### Async

Async mode is served through Grafana Live. The backend opens a dedicated kdb+ connection and calls:

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

When the job reaches `done`, `.grafana.asyncq.async.result` must return a flat table or grouped table accepted by the existing parser.

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

## q helper

The helper in [q/asyncq_grafana.q](q/asyncq_grafana.q) provides the protocol functions above:

```q
\l q/asyncq_grafana.q
```

It is useful for development and for documenting the wire contract. Its async implementation evaluates work in-process, so it is not a production worker-pool scheduler. For production async queries, load compatible functions in a gateway that submits work to workers and returns quickly from `async.submit`.

## Security

This datasource preserves the upstream behavior of sending user-entered q text to kdb+. Treat that text as untrusted input unless your environment already has strong controls. For production gateways, prefer allowlisted function calls, `reval` where applicable, `-b`, authenticated IPC, query timeouts, memory limits, and separate worker processes.

## Returned data

Queries must return either:

- a flat table, kdb+ type 98
- a grouped table, kdb+ type 99 where key and value are congruent tables

Columns must have stable scalar types. String columns and grouped table keys are supported by the inherited parser. If `Use Custom Time Column` is enabled, the named column must exist in every returned frame.

## Development

Install dependencies and run checks:

```bash
npm install
npm run typecheck
npm run build
go test ./pkg/...
```

Validate the q helper if a local `q` binary is available:

```bash
printf '\\\\\n' | q q/asyncq_grafana.q -q -T 5 -w 1024 -u 1 -b
```

The frontend build writes `dist/module.js` and copies plugin metadata. Backend binaries can be built with Mage once the target Grafana plugin packaging flow is set up.

## Local demo

A runnable demo is available in [demo/README.md](demo/README.md). It provisions a Grafana 13 dashboard and datasource, and starts a local q process with sync, async, and streaming sample data:

```bash
./scripts/start-demo-local.sh
```
