---
name: panopticon-grafana-migration
description: Use when migrating Altair Panopticon dashboards or panels backed by kdb+/q into Grafana panels using the AsyncQ kdb+ datasource plugin. Applies to same-port/no-q-change migration against existing Panopticon-serving gateways/RDBs, discovering legacy q gateway request and async protocols from source code or IPC probes, copy/pasting Panopticon q queries/functions, translating time-window parameters, choosing AsyncQ execution and compatibility modes, creating Grafana panel JSON, debugging migration failures, and preserving table-style behavior.
---

# Panopticon To AsyncQ Grafana Migration

Use this skill to migrate Panopticon kdb+/q-backed panels into Grafana panels using the AsyncQ datasource plugin in this repository.

## First Read

For the compatibility matrix, detailed mappings, and examples, read [references/asyncq-migration.md](references/asyncq-migration.md). For any non-trivial q edit or q diagnosis, also use the q-kdb skill.

## Migration Workflow

1. Establish the migration constraint.
   - Default to same-port/no-q-change migration when the user wants Grafana pointed at existing Panopticon-serving gateways or RDBs.
   - Do not require loading `q/asyncq_grafana.q` unless the user explicitly accepts q-side changes.
   - If the gateway contract cannot be spoken by current AsyncQ modes, report the needed plugin adapter rather than inventing q changes.
   - Before claiming copy/paste compatibility, classify the panel against the compatibility matrix in the reference.

2. Identify the Panopticon panel type and data path.
   - Extract the q query text or function call.
   - Note whether Panopticon uses direct query, pass-to-function, deferred query, time-window parameters, action parameters, streaming, or post-query transforms.
   - Identify whether multiple Panopticon panels share the same underlying datasource query/result and only differ by client-side transforms, filtering, aggregation, or visualization.
   - Record expected result shape: table, keyed table, dictionary, scalar, vector, or list of row dictionaries.

3. Discover the existing q gateway contract when source code or q ports are available.
   - Inspect `.z.pg`, `.z.ps`, gateway dispatch functions, auth/session logic, request dictionary parsing, async submit/status/result/cancel functions, callback/deferred response handling, and Panopticon-specific envelopes.
   - Use read-only IPC probes only. Start with health checks and harmless expressions/functions; do not run mutating queries.
   - Preserve the same host, port, credentials, and invocation shape that Panopticon used where possible.

4. Choose AsyncQ settings.
   - Use `compatibilityMode: "panopticon"` for migrated panels unless deliberately preserving AquaQ/native behavior.
   - Use `executionMode: "sync"` for quick validation.
   - Use `executionMode: "pluginAsync"` when the gateway only supports blocking sync IPC but the query should not block Grafana. This works with legacy gateways without q-side helper functions.
   - Use `executionMode: "async"` only when the target q process exposes `.grafana.asyncq.async.submit/status/result/cancel`.
   - For shared Panopticon base-query results, create one AsyncQ source panel and point dependent panels at Grafana's `-- Dashboard --` datasource with `Use results from panel`; do not duplicate the same AsyncQ query in every dependent panel.
   - Use `panopticonRequestFunction` when the Panopticon panel used pass-to-function or the q side expects a full request dictionary.
   - Use `panopticonQueryWrapper` when the original query must be wrapped around `{Query}`.
   - For a legacy async protocol with different function names or fields, identify the submit/status/result/cancel mapping and state whether AsyncQ needs a custom adapter patch.

5. Translate parameters conservatively.
   - Keep Panopticon time parameters in the pasted query where possible: `{TimeWindowStart}`, `{TimeWindowEnd}`, `{Snapshot}`, `{FocusTime}`, `$TimeWindowStart`, `$TimeWindowEnd`, `$Snapshot`, `$FocusTime`.
   - Formatted parameters like `{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}` are supported and expand to q string literals.
   - Create Grafana variables with the same names as Panopticon dashboard/action parameters where possible. AsyncQ Panopticon mode expands `{parameter}` and `{parameter:delimiter}` from matching Grafana variables in query text and `panopticonQueryWrapper`.
   - Do not invent variable values. Values are inserted as raw text, so preserve the q quoting/backtick syntax the original query expects.

6. Build the Grafana panel.
   - Start with a Table panel for migration validation, even if the final visual will be different.
   - When several panels share one Panopticon result set, build a source panel with the AsyncQ query first, then build dependent panels using the Dashboard datasource and Grafana transformations/field overrides.
   - Set `useTimeColumn: false` unless the returned frame has a real time column and the target visualization needs it.
   - Once data matches, change the visualization type and field overrides.

7. Validate by result shape.
   - Panopticon compatibility accepts flat tables, keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries.
   - Sparse row dictionaries are allowed; missing cells become nulls.
   - Mixed numeric row-dictionary values widen to float columns.
   - If a result uses nested lists, custom objects, or Panopticon client transforms, add an explicit q adapter that returns a table-like shape.

8. State compatibility explicitly.
   - Use one verdict: Direct, Config-only, Adapter needed, Visual rewrite, or Not portable.
   - Explain the limiting feature in one sentence: execution protocol, request envelope, result shape, parameters, visualization, interaction, auth/session, or streaming.
   - Never claim 100% dashboard copy/paste compatibility. Query/function portability can be high; visual behavior and dashboard interactions still need Grafana translation.

9. Debug failures from the outside in.
   - Confirm Grafana can connect to kdb+ with datasource health.
   - Run the panel as `sync` first.
   - Check Grafana backend logs for connection, wrapper, parse, and result-shape errors.
   - For async, check job ID/status/result/cancel behavior in q.
   - If the q process returns an error, reproduce the compiled query or request-function call in q with the same time range.

## Output Format

When asked to migrate a panel, return:

- A short migration assessment: likely compatible, needs adapter, or not directly portable.
- A compatibility-matrix verdict and the exact reason for any gap.
- The target AsyncQ query settings.
- The Dashboard datasource mapping for any panels that should reuse another panel's result instead of querying AsyncQ directly.
- The Grafana panel target JSON or exact fields to set in the query editor.
- Any q adapter function needed.
- Any AsyncQ plugin adapter/config gap needed to speak an unchanged legacy gateway protocol.
- Validation steps and known gaps.

Avoid claiming full dashboard copy/paste compatibility. This plugin targets query/data compatibility; Grafana visual configuration still needs translation.
