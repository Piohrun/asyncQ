# Changelog

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
