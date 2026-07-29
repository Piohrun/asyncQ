# AsyncQ demo

This demo runs:

- a development q process on IPv4 loopback port `5000` by default
- Grafana 13 on `http://127.0.0.1:3000`
- a provisioned `AsyncQ Demo` datasource
- a provisioned `AsyncQ kdb+ demo` dashboard with sync, helper async, plugin async, legacy async adapter, deferred wrapper, Panopticon compatibility, stream panels, cache diagnostics, and Excel reporting
- pinned Business Suite panel plugins for migration testing: Business Table (`volkovlabs-table-panel`), Business Charts (`volkovlabs-echarts-panel`), and Business Forms (`volkovlabs-form-panel`)

## Start without Docker

From the repository root:

```bash
./scripts/start-demo-local.sh
```

Open:

```text
http://127.0.0.1:3000/d/asyncq-kdb-demo/asyncq-kdb-demo
```

Additional provisioned test dashboards:

```text
http://127.0.0.1:3000/d/asyncq-compat-matrix/asyncq-panopticon-compatibility-matrix
http://127.0.0.1:3000/d/asyncq-pano-compat/asyncq-panopticon-compatibility-tests
http://127.0.0.1:3000/d/asyncq-async-tests/asyncq-async-execution-tests
http://127.0.0.1:3000/d/asyncq-sync-pool/asyncq-sync-connection-pool
http://127.0.0.1:3000/d/asyncq-masterdata-cache/asyncq-master-data-and-cache-controls
http://127.0.0.1:3000/d/asyncq-excel-report/asyncq-excel-reporting
http://127.0.0.1:3000/d/asyncq-business-suite/asyncq-business-suite-smoke
```

Grafana is configured with anonymous Admin access for demo convenience. Anonymous Admin is a privileged, unsafe configuration: both launchers configure Grafana explicitly for `127.0.0.1`, and the Docker path verifies the pinned loopback configuration before accepting health. Do not reverse-proxy this demo to another interface. The explicit login is also the development-only `admin` / `admin`.

The local starter downloads Grafana OSS `13.1.1` into `demo/runtime/` on first run and keeps all Grafana data, logs, generated provisioning, and plugin symlinks under that ignored runtime directory. Its plugin build has a ten-minute outer deadline; timeout or interruption terminates the verified build process group, including resistant descendants, before prior runtime state is left in place. Build children receive a private `HOME` and an allowlisted environment: shell, Node/npm, Go, and Git startup or executable hooks are removed, while the validated `PATH`, locale, proxy/CA settings, and explicit cache paths needed by the repository build are retained. PATH entries must be absolute canonical paths without control characters or empty components; ordinary spaces inside an absolute entry are preserved. Unsafe values are rejected rather than rewritten. npm's user and global configuration point at two distinct, empty, launcher-created `0600` files inside that private build home. Before npm, webpack, or Go can write, each exact build-output root is anchored below the physical repository root and recursively checked without following links; symlinks, special files, and multiply linked regular files are rejected. The trees are checked again between the frontend and backend builds and after completion. Published plugin directories are normalized to `0755`, ordinary assets to `0644`, and only the exact current backend executable to `0755`; local runtime configuration, logs, process state, and child homes remain private.
The pinned Linux `amd64`, `arm64`, and `armv7` archives use exact official URLs and SHA-256 values published on [Grafana Labs' `13.1.1` download pages](https://grafana.com/grafana/download/13.1.1?edition=oss). You may select one of those architectures with `GRAFANA_ARCH`; the launcher uses its built-in trusted digest and rejects a conflicting `GRAFANA_SHA256`. A custom `GRAFANA_VERSION` requires an explicit `GRAFANA_SHA256`. Both new downloads and cached archives are restricted to 512 MiB and verified before inspection. Archive entry and metadata listings are capped at 64 MiB while they are produced, and listing and extraction have deadlines; links, special files, unsafe names, more than 25,000 members, members over 600 MiB, and aggregate expansion over 2 GiB are rejected before extraction. Inherited tar/gzip options are cleared. After a verified extraction, the launcher records the trusted archive digest and a bounded canonical fingerprint of the install's names, entry types, sizes, and file contents in a strict `0600` single-link state file. Because `demo/runtime/` is restricted to the current user, a warm start can verify the archive and recompute that fingerprint without extracting the archive again. A missing, malformed, linked, or mismatched state—or any detected install-tree change—forces one verified extraction and transactional replacement. A failed replacement restores the previous install; if restoration also fails, the previous tree remains in a reported recovery directory instead of being deleted. This state detects corruption and accidental edits within the launcher's private-runtime trust boundary; it is not a claim of protection against concurrent mutation by the same user.
It also installs pinned Business Suite panel plugins into `demo/runtime/plugins` by default. When run directly, the installer verifies the archive checksum and the complete install fingerprint. During the normal local launch, it accepts the fingerprint just verified by its still-live parent and checks the strict state cheaply, avoiding a second full-tree hash in the same launch. Grafana CLI signature verification remains enabled, each CLI install is bounded to five minutes, and the installed plugin ID and version are verified from `plugin.json`. Both the CLI and server run with clean environments, private `HOME`/temporary directories, exact launcher-owned config paths, and only the documented Grafana/AsyncQ settings plus safe network trust settings. Set `ASYNCQ_DEMO_INSTALL_BUSINESS_PLUGINS=0` before running `./scripts/start-demo-local.sh` to skip those downloads.

The launchers restrict their existing runtime/log directories to the current user, reject unsafe linked output paths, and record process identity only after readiness. q must own only an IPv4 `127.0.0.1` listener on its configured port within 15 seconds. Within 60 seconds Grafana's exact recorded process must own an IPv4 `127.0.0.1` listener and return bounded, structurally valid `/api/health` JSON with the expected version. Stop scripts require both the recorded PID and process-start identity before signalling a live process. If `start-demo-local.sh` starts a new q process and Grafana then fails or the orchestration is interrupted, it rolls back only that newly started q process; a verified q process that was already running is preserved.

`GRAFANA_PORT` and `ASYNCQ_DEMO_Q_PORT` accept only canonical decimal integers from `1` through `65535`: values with signs, whitespace, leading zeroes, or shell syntax are rejected. The same validated `ASYNCQ_DEMO_Q_PORT` is recorded in Grafana's environment and rendered into its loopback datasource provisioning, so a custom port remains consistent end to end. For example:

```bash
ASYNCQ_DEMO_Q_PORT=5001 ./scripts/start-demo-local.sh
```

`GRAFANA_VERSION`, `GRAFANA_ARCH`, and `GRAFANA_SHA256` are also validated before they can affect a path or download.

The q launcher passes `-p 127.0.0.1:PORT`, verifies that the exact q child owns only that IPv4-loopback listener, applies a 30-second query timeout (`-T 30`), a 1024 MB workspace cap (`-w 1024`), and uses `-u 1`. It starts q with a trusted empty `QINIT` inside a private session and removes inherited q/PyKX startup hooks; only canonicalized existing `QHOME` and `QLIC` directories are retained when supplied. These are defense-in-depth limits, not a sandbox. Block-writes mode (`-b`) is intentionally not used because it would block the demo's remote Helper Async and stream state mutations. The demo q process evaluates query text and must run only on a trusted local development machine, never an exposed or production host.

The demo datasource enables safe backend diagnostics by default. Grafana logs include request IDs, ref IDs, query hashes, q worker/result metadata, frame schemas, async job or stream IDs, and errors. Raw query text and q stack trace logging stays disabled.

The provisioned datasource sets `syncMaxConnections: 4`, so multiple sync panels can exercise the per-datasource kdb+ connection pool. Lower this to `1` in `demo/grafana/provisioning/datasources/asyncq.yml` if you want to compare the original serial sync behavior. Query caching and local disk cache are enabled in the demo with a 60-second sync result TTL. For relative `now` ranges, set `queryCacheTimeBucketSeconds: 60` if you want near-identical reloads to share cached results. Set `queryCacheStaleTTLSeconds` to return stale data immediately while the backend refreshes the cache for the next query.

The Excel report demo allowlists `demo/templates` through `excelReportTemplateDirs` and uses default safeguards for row count, generated workbook size, and generation timeout.

## Start with Docker

Docker is optional. This path targets Linux Docker Engine because it uses host networking to share the host's loopback namespace safely with q:

```bash
./scripts/start-demo-q.sh
./scripts/start-demo-grafana.sh
```

For a custom q port, pass the same value to both launchers:

```bash
ASYNCQ_DEMO_Q_PORT=5001 ./scripts/start-demo-q.sh
ASYNCQ_DEMO_Q_PORT=5001 ./scripts/start-demo-grafana.sh
```

The Compose service pins `grafana/grafana:13.1.1` to its [official Docker Hub multi-platform image digest](https://hub.docker.com/layers/grafana/grafana/13.1.1), uses `network_mode: host`, and configures Grafana itself for `127.0.0.1:3000`; it does not publish a container port. The launcher verifies the exact recorded q runner and child on the selected loopback port, generates a port-specific loopback datasource under `demo/runtime/`, and requires q connectivity before accepting Grafana health.

Every Docker call is frozen to one validated local `unix:///` Engine endpoint and a private empty Docker CLI configuration; remote/TCP and non-default context redirection are rejected, and inherited TLS/plugin redirect variables are not forwarded. Every Compose call starts from a clean environment, sets only the disabled implicit env-file switch plus the validated ownership token and q port, and pins the repository's non-linked `demo/docker-compose.yml`, `demo/` project directory, and fixed `asyncq-demo` project name. Thus arbitrary inherited `COMPOSE_*` values—including orphan-removal behavior—`.env`, and implicit override files cannot redirect or expand the operation. Before Docker is invoked, all plugin bind trees plus the static template and provisioning trees receive the same recursive no-link validation; their nonsecret directories/files are published as `0755`/`0644`. The port-specific generated datasource is atomically rendered, structurally compared when it already exists, and published as a single-link `0644` file, while its `demo/runtime/` parent and all Docker/build command homes and configuration stay `0700`/`0600`. Reuse requires the exact service container ID, project/service labels, current Compose config hash evaluated with the container's validated ownership label and q port, pinned image reference and image ID, host network mode, loopback server environment, running state, and bounded structural health response. Health alone is insufficient: bounded anonymous-Admin requests on `127.0.0.1` must also return datasource UID `asyncq-demo` with type `asyncq-kdbbackend-datasource` and register the datasource, master-data panel, and Excel-report panel plugin IDs. No admin credential is sent by this check. A matching service is reused unchanged; drifted, incompletely provisioned, or unhealthy existing state is preserved and rejected. New containers receive a unique ownership label, and failure or interruption removes only the exact container ID that still bears that token.

## What to try

- `Sync latest trades` calls `.demo.asyncq.latest 25`.
- `Async aggregate after queued delay` calls `.demo.asyncq.slowAgg[]` through the async helper functions. The demo q process waits about three seconds before marking the job done.
- `Plugin async aggregate` runs `.demo.asyncq.slowAgg[]` without loading any q async helper functions for that query path.
- `Deferred wrapper aggregate` runs `.demo.asyncq.deferred[{Query}]` around `.demo.asyncq.slowAgg[]` to demonstrate wrapper expansion.
- `Panopticon dict result` enables Panopticon compatibility mode and displays a symbol-keyed q dictionary from `.demo.asyncq.panopticonSummary[]`.
- To try Panopticon request-function mode, set `Compatibility` to `Panopticon`, set `Pano Fn` to the trusted fully qualified name `.demo.asyncq.panopticonRequest`, and run any harmless query text such as `1+1`. The demo registers that name in `.grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS`; arbitrary function expressions and unregistered names are rejected.
- `Streaming tick prices` and `Streaming rows` subscribe through Grafana Live. The q timer publishes five new rows every second to active streams. The tick chart uses a 10-minute `Retention (s)` window plus a row cap.
- `Demo process counters` shows row, stream, and job counts from the q process.
- `AsyncQ Panopticon compatibility tests` exercises macro expansion, dashboard-parameter expansion from Grafana variables, `Pano Wrapper`, `Pano Fn`, scalar/vector/string returns, keyed tables, lists of row dictionaries, sparse row dictionaries, and mixed numeric row values.
- `AsyncQ Panopticon compatibility matrix` maps the migration matrix to demo panels: direct sync, plugin async, wrapper, request function, macros, keyed table, dictionary, row dictionaries, an expected adapter-needed failure, and its table-shaped replacement.
- `AsyncQ async execution tests` compares sync, helper async, plugin async, legacy async adapter, deferred async, streaming, and Panopticon request-function execution. The legacy panel uses `.demo.legacy.submit/status/result/cancel`, which deliberately return `id/state/pct/payload` envelopes instead of the `.grafana.asyncq.async.*` helper contract.
- `AsyncQ sync connection pool` runs four slow sync probes against the same datasource. With the single local q process, q itself may serialize execution; inspect Grafana diagnostics for `syncPoolAcquireWaitMs`, `syncPoolAcquireSource`, `syncPoolActive`, and `syncTransportMs` to distinguish plugin pool wait from target q processing time.
- `AsyncQ master data and cache controls` demonstrates the companion `asyncq-masterdata-panel`: one master data panel runs the AsyncQ query, a freshness widget and table reuse that result through Grafana's `-- Dashboard --` datasource, and cache-control buttons call the datasource cache resources.
- `AsyncQ Excel reporting` demonstrates the companion `asyncq-excel-report-panel` with large query-backed reports. Change the `Report rows` variable, optionally edit the generated workbook filename, then compare the three report buttons: `cells`, `rows`, and `stream`.
  The report buttons show generation progress, elapsed time, and a final "report generated" message. The source panels show the same deterministic q data that the report definitions query directly. The generated workbooks use [demo/templates/asyncq-demo-report-template.xlsx](templates/asyncq-demo-report-template.xlsx), and backend logs include `writeSheetsMs`, `serializeWorkbookMs`, `writtenRows`, and `writtenCells` for each generated workbook.
- `AsyncQ Business Suite smoke` verifies the installed Business Table, Business Charts, and Business Forms panels against the AsyncQ demo datasource.

For Panopticon dashboards where several panels share one base datasource result, create one AsyncQ source panel or `asyncq-masterdata-panel` and set the dependent panels to Grafana's `-- Dashboard --` datasource with `Use results from panel`. The demo dashboards keep most panels direct so the plugin behavior is visible, but production migrations should use Dashboard datasource sharing for this Panopticon pattern.

If you restart the q process while the dashboard is already open, refresh the browser tab so the async and streaming panels create fresh Grafana Live subscriptions.

## E2E test

The Excel reporting path has a local Playwright test that starts/reuses the demo, clicks the stream report download button, and inspects the generated workbook:

```bash
npm run test:e2e:install
npm run test:e2e:demo
```

## Stop

```bash
./scripts/stop-demo-local.sh
```

For the Docker path, run the same pinned Compose target from the repository root, then stop q:

```bash
docker compose --file demo/docker-compose.yml --project-directory demo --project-name asyncq-demo down
./scripts/stop-demo-q.sh
```

## Files

- `demo/q/asyncq_demo.q` - q demo process
- `demo/grafana/provisioning/datasources/asyncq.yml` - datasource provisioning
- `demo/grafana/provisioning/dashboards/json/asyncq-demo.json` - dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-compatibility-matrix.json` - compatibility matrix dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-panopticon-compat.json` - Panopticon compatibility test dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-async-tests.json` - async execution mode test dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-sync-pool.json` - sync pool diagnostics dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-masterdata-cache.json` - master data/cache-control dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-excel-report.json` - Excel report dashboard
- `demo/grafana/provisioning/dashboards/json/asyncq-business-suite.json` - Business Suite panel smoke dashboard
- `demo/templates/asyncq-demo-report-template.xlsx` - sample workbook template for Excel report tests
- `demo/docker-compose.yml` - Grafana 13 container
- `scripts/install-demo-business-plugins.sh` - pinned Business Suite plugin installer for the local demo

## Notes

The q process is intentionally permissive and evaluates demo query text, but the launcher restricts its TCP listener to IPv4 loopback. This setup remains for trusted local development only.
