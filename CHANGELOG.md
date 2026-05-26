# Changelog

## 1.0.5

- Added `legacyAsync` execution mode for same-port legacy q gateway async protocols with configurable submit, status, result, cancel, response paths, request mode, and status value mappings.
- Added total async timeout enforcement for helper async and legacy async submit/status/result flows, with best-effort cancel on timeout.
- Added legacy async diagnostics for raw status, normalized status, and status-mapping matches.
- Added live integration coverage that starts the demo q process and exercises the legacy async submit/status/result flow.
- Tidied the legacy async datasource and panel editors by moving advanced response mapping into an expandable section.
- Added demo q functions and a dashboard panel that exercise a legacy-shaped async protocol without calling the `.grafana.asyncq.async.*` helper contract directly.

## 1.0.4

- Added self-contained local disk persistence for successful sync query cache entries, with datasource settings for disk enablement, path, entry cap, and byte budget.
- Changed new datasource defaults to enable sync result caching and disk persistence while preserving explicit disabled settings.
- Added datasource resource endpoints for cache status, entry listing, clear, clear-entry, and clear-expired controls.
- Gated destructive cache-control endpoints behind datasource enablement and Grafana Admin/Editor roles; cache status remains read-only.
- Attached AsyncQ diagnostics to result frames under `frame.meta.custom.asyncqDiagnostics`.
- Added the companion `asyncq-masterdata-panel` plugin for master data previews, freshness widgets, diagnostics, and cache-control buttons.
- Added a demo dashboard showing master-data panel reuse through Grafana's `-- Dashboard --` datasource.

## 1.0.3

- Added per-datasource sync query result caching with TTL, max-entry bounds, diagnostics, and q comment cache directives.
- Added per-query cache controls for mode, key mode, TTL, stale TTL, and time bucket.
- Added stale-while-revalidate support so expired cached results can return immediately while the backend refreshes the cache for the next query.
- Added strict/shared cache key modes; strict includes Grafana ref ID, while shared can reuse cached q results across panels when the q path is ref-ID independent.
- Updated Panopticon migration skill guidance for cache policy, warm dashboard reloads, shared result reuse, and stale-refresh limitations.

## 1.0.2

- Added a bounded reusable synchronous kdb+ IPC connection pool per datasource instance.
- Added `Sync Max Connections` datasource configuration; default is `4`, while `1` restores strict legacy single-handle sync behavior.
- Changed standard `QueryData` handling so independent sync targets in the same Grafana request run concurrently, with the datasource pool enforcing the limit.
- Added sync pool diagnostics for acquire wait, opened/reused connections, active/idle pool state, release/discard action, transport duration, and timeout/error cases.
- Added a local sync pool probe q function and demo dashboard for checking whether waits happen in the plugin pool or inside the target q gateway/process.

## 1.0.1

- Fixed generic/mixed kdb+ list columns (`type 0`) by converting mixed values to string columns instead of producing empty fields.
- Added Panopticon dashboard-parameter compatibility for `{parameter}` and `{parameter:delimiter}` syntax backed by matching Grafana variables.
- Updated Panopticon migration docs and skill guidance for dashboard parameters and copy/paste query compatibility.

## 0.2.0 (Unreleased)

- Added plugin-managed async mode for long-running q queries without q helper functions.
- Added deferred-wrapper async mode with datasource and per-query wrapper configuration.
- Added datasource and query compatibility modes for native AsyncQ, AquaQ, and Panopticon-style result handling.
- Added Panopticon macro expansion, query wrapper mode, and request-function invocation mode.
- Added Panopticon result coercion for keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries.
- Expanded the local demo dashboard with plugin async, deferred wrapper, and Panopticon dictionary panels.

## 0.1.0

- Forked AquaQ kdb+ backend datasource for Grafana 13.
- Preserved the synchronous query, variable, health check, and alerting path.
- Added per-query sync, async, and stream execution modes.
- Added Grafana Live streaming handlers for async status/result delivery and q-pushed streaming data.
- Added `q/asyncq_grafana.q` helper protocol for development and gateway implementations.
- Replaced deprecated Grafana Toolkit frontend build with a minimal webpack/Grafana 13 toolchain.
