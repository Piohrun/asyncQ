# Initial source checkout notes

Checked out source trees:

- Grafana: `upstream/grafana-13`
  - Tag: `v13.0.1+security-01`
  - Commit: `9bbe672d13753e132db266e1f47dcaf362a76e81`
  - Clone command used: `git clone --depth 1 --branch 'v13.0.1+security-01' --filter=blob:none https://github.com/grafana/grafana.git upstream/grafana-13`
- AquaQ kdb+ backend datasource: `upstream/aquaq-kdb-backend-datasource-1.0.0`
  - Tag: `v1.0.0`
  - Commit: `1b84f1c73d9dc2d785022b6ce39e10dccfe97e60`
  - Clone command used: `git clone --depth 1 --branch v1.0.0 https://github.com/AquaQAnalytics/grafana-kdb-backend-datasource.git upstream/aquaq-kdb-backend-datasource-1.0.0`

Initial observations:

- AquaQ v1.0.0 uses `github.com/grafana/grafana-plugin-sdk-go v0.114.0`; Grafana 13 uses `v0.291.0`.
- The plugin backend implements `backend.QueryDataHandler` and `backend.CheckHealthHandler`, but not `backend.StreamHandler`.
- All panel queries flow through `QueryData`, then `RunKdbQuerySync`, then `WriteConnection(kdb.SYNC, ...)`.
- The plugin serializes kdb requests through a single `syncQueue` and reads responses through one `rawReadChan`.
- Grafana 13 has first-class datasource streaming through Grafana Live, implemented with `SubscribeStream`, `RunStream`, and `PublishStream`.
- Existing Grafana examples to study:
  - `upstream/grafana-13/pkg/tsdb/grafana-testdata-datasource/stream_handler.go`
  - `upstream/grafana-13/pkg/tsdb/loki/streaming.go`
  - `upstream/grafana-13/public/app/plugins/datasource/tempo/streaming.ts`

Early direction:

- Reusing the AquaQ plugin is plausible for synchronous query compatibility, configuration UI, TLS setup, and table-to-data-frame parsing.
- Async and streaming should likely be added as new paths rather than forced through the current sync queue.
- A serious prototype should upgrade the Grafana plugin SDK, implement `backend.StreamHandler`, add frontend `getGrafanaLiveSrv().getStream(...)` support, and create a separate kdb IPC loop capable of handling async messages and stream cancellation.

Environment note:

- `go` is not currently installed or not on `PATH`, so this environment can inspect source but cannot build or run Go tests yet.
