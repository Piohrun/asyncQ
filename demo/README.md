# AsyncQ demo

This demo runs:

- a local q process on port `5000`
- Grafana 13 on `http://localhost:3000`
- a provisioned `AsyncQ Demo` datasource
- a provisioned `AsyncQ kdb+ demo` dashboard with sync, helper async, plugin async, legacy async adapter, deferred wrapper, Panopticon compatibility, and stream panels

## Start without Docker

From the repository root:

```bash
./scripts/start-demo-local.sh
```

Open:

```text
http://localhost:3000/d/asyncq-kdb-demo/asyncq-kdb-demo
```

Additional provisioned test dashboards:

```text
http://localhost:3000/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix
http://localhost:3000/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests
http://localhost:3000/d/asyncq-async-tests/asyncq-async-execution-tests
http://localhost:3000/d/asyncq-sync-pool/asyncq-sync-connection-pool
http://localhost:3000/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls
```

Grafana is configured with anonymous admin access for the demo. The explicit login is `admin` / `admin`.

The local starter downloads Grafana OSS `13.0.1` into `demo/runtime/` on first run and keeps all Grafana data, logs, generated provisioning, and plugin symlinks under that ignored runtime directory.

The demo datasource enables safe backend diagnostics by default. Grafana logs include request IDs, ref IDs, query hashes, q worker/result metadata, frame schemas, async job or stream IDs, and errors. Raw query text and q stack trace logging stays disabled.

The provisioned datasource sets `syncMaxConnections: 4`, so multiple sync panels can exercise the per-datasource kdb+ connection pool. Lower this to `1` in `demo/grafana/provisioning/datasources/asyncq.yml` if you want to compare the original serial sync behavior. Query caching and local disk cache are enabled in the demo with a 60-second sync result TTL. For relative `now` ranges, set `queryCacheTimeBucketSeconds: 60` if you want near-identical reloads to share cached results. Set `queryCacheStaleTTLSeconds` to return stale data immediately while the backend refreshes the cache for the next query.

## Start with Docker

Docker is optional. It is useful when you want a fully disposable Grafana container:

```bash
./scripts/start-demo-q.sh
./scripts/start-demo-grafana.sh
```

## What to try

- `Sync latest trades` calls `.demo.asyncq.latest 25`.
- `Async aggregate after queued delay` calls `.demo.asyncq.slowAgg[]` through the async helper functions. The demo q process waits about three seconds before marking the job done.
- `Plugin async aggregate` runs `.demo.asyncq.slowAgg[]` without loading any q async helper functions for that query path.
- `Deferred wrapper aggregate` runs `.demo.asyncq.deferred[{Query}]` around `.demo.asyncq.slowAgg[]` to demonstrate wrapper expansion.
- `Panopticon dict result` enables Panopticon compatibility mode and displays a symbol-keyed q dictionary from `.demo.asyncq.panopticonSummary[]`.
- To try Panopticon request-function mode, set `Compatibility` to `Panopticon`, set `Pano Fn` to `{[req] .demo.asyncq.panopticonRequest req}`, and run any harmless query text such as `1+1`.
- `Streaming tick prices` and `Streaming rows` subscribe through Grafana Live. The q timer publishes five new rows every second to active streams. The tick chart uses a 10-minute `Retention (s)` window plus a row cap.
- `Demo process counters` shows row, stream, and job counts from the q process.
- `AsyncQ Panopticon compatibility tests` exercises macro expansion, dashboard-parameter expansion from Grafana variables, `Pano Wrapper`, `Pano Fn`, scalar/vector/string returns, keyed tables, lists of row dictionaries, sparse row dictionaries, and mixed numeric row values.
- `AsyncQ Panopticon compatibility matrix` maps the migration matrix to demo panels: direct sync, plugin async, wrapper, request function, macros, keyed table, dictionary, row dictionaries, an expected adapter-needed failure, and its table-shaped replacement.
- `AsyncQ async execution tests` compares sync, helper async, plugin async, legacy async adapter, deferred async, streaming, and Panopticon request-function execution. The legacy panel uses `.demo.legacy.submit/status/result/cancel`, which deliberately return `id/state/pct/payload` envelopes instead of the `.grafana.asyncq.async.*` helper contract.
- `AsyncQ sync connection pool` runs four slow sync probes against the same datasource. With the single local q process, q itself may serialize execution; inspect Grafana diagnostics for `syncPoolAcquireWaitMs`, `syncPoolAcquireSource`, `syncPoolActive`, and `syncTransportMs` to distinguish plugin pool wait from target q processing time.
- `AsyncQ master data and cache controls` demonstrates the companion `asyncq-masterdata-panel`: one master data panel runs the AsyncQ query, a freshness widget and table reuse that result through Grafana's `-- Dashboard --` datasource, and cache-control buttons call the datasource cache resources.

For Panopticon dashboards where several panels share one base datasource result, create one AsyncQ source panel or `asyncq-masterdata-panel` and set the dependent panels to Grafana's `-- Dashboard --` datasource with `Use results from panel`. The demo dashboards keep most panels direct so the plugin behavior is visible, but production migrations should use Dashboard datasource sharing for this Panopticon pattern.

If you restart the q process while the dashboard is already open, refresh the browser tab so the async and streaming panels create fresh Grafana Live subscriptions.

## Stop

```bash
./scripts/stop-demo-local.sh
```

For the Docker path, run `docker compose down` from `demo/`, then `./scripts/stop-demo-q.sh`.

## Files

- `demo/q/asyncq_demo.q` - q demo process
- `demo/grafana/provisioning/datasources/asyncq.yml` - datasource provisioning
- `demo/grafana/provisioning/dashboards/json/asyncq-demo.json` - dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-compatibility-matrix.json` - compatibility matrix dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-panopticon-compat.json` - Panopticon compatibility test dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-async-tests.json` - async execution mode test dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-sync-pool.json` - sync pool diagnostics dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-masterdata-cache.json` - master data/cache-control dashboard
- `demo/docker-compose.yml` - Grafana 13 container

## Notes

The q process is intentionally permissive and evaluates demo query text. It is for local development only.
