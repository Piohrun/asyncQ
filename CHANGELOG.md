# Changelog

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
