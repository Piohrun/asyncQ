# AsyncQ Master Data Panel

This companion panel shows AsyncQ datasource result freshness, cache state, backend diagnostics, and lightweight query profile timings from `frame.meta.custom.asyncqDiagnostics`. Master-data and diagnostics views scroll internally when the panel is short, and diagnostics mode breaks the total sync query time into prepare, cache/query path, cache lookup, kdb call, payload build, and frame parse timings.

Use it as a source panel when other Grafana panels should reuse the same query result through the `-- Dashboard --` datasource. Use Freshness mode as a compact widget on migrated Panopticon-style dashboards.
