# Changelog

## 0.1.0 (Unreleased)

- Forked AquaQ kdb+ backend datasource for Grafana 13.
- Preserved the synchronous query, variable, health check, and alerting path.
- Added per-query sync, async, and stream execution modes.
- Added Grafana Live streaming handlers for async status/result delivery and q-pushed streaming data.
- Added `q/asyncq_grafana.q` helper protocol for development and gateway implementations.
- Replaced deprecated Grafana Toolkit frontend build with a minimal webpack/Grafana 13 toolchain.
