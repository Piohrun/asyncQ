# AsyncQ kdb+ Grafana datasource

AsyncQ is a Grafana 13 backend datasource for kdb+ derived from AquaQ Analytics' community kdb+ backend datasource. The synchronous query path is intentionally kept compatible with the original plugin, while panel queries can opt into asynchronous execution or q-driven streaming through Grafana Live.

## Query modes

Sync mode is the default and matches the upstream datasource behavior. Variables and alerting use sync mode.

Async mode uses Grafana Live and calls q helper functions:

```q
.grafana.asyncq.async.submit requestDict
.grafana.asyncq.async.status jobId
.grafana.asyncq.async.result jobId
.grafana.asyncq.async.cancel jobId
```

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

## q helper

Load the development helper from this repository:

```q
\l q/asyncq_grafana.q
```

The helper documents the backend contract and supports local testing. Its async implementation evaluates jobs in-process, so production deployments should replace it with gateway/worker-backed functions that return quickly from `async.submit`.

## Data contract

Queries must return either a flat kdb+ table or a grouped table. The request dictionary preserves the upstream `AQUAQ_KDB_BACKEND_GRAF_DATASOURCE` key for compatibility and adds async/stream metadata such as `RequestID`, `StreamID`, `ExecutionMode`, `PollIntervalMs`, and `MaxStreamRows`.
