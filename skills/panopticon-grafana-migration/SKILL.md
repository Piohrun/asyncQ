---
name: panopticon-grafana-migration
description: Use when migrating Altair Panopticon dashboards or panels backed by kdb+/q into Grafana panels using the AsyncQ kdb+ datasource plugin. Applies to copy/pasting Panopticon q queries/functions, translating time-window parameters, choosing AsyncQ execution and compatibility modes, creating Grafana panel JSON, debugging migration failures, and preserving table-style behavior.
---

# Panopticon To AsyncQ Grafana Migration

Use this skill to migrate Panopticon kdb+/q-backed panels into Grafana panels using the AsyncQ datasource plugin in this repository.

## First Read

For detailed mappings and examples, read [references/asyncq-migration.md](references/asyncq-migration.md). For any non-trivial q edit or q diagnosis, also use the q-kdb skill.

## Migration Workflow

1. Identify the Panopticon panel type and data path.
   - Extract the q query text or function call.
   - Note whether Panopticon uses direct query, pass-to-function, deferred query, time-window parameters, action parameters, streaming, or post-query transforms.
   - Record expected result shape: table, keyed table, dictionary, scalar, vector, or list of row dictionaries.

2. Choose AsyncQ settings.
   - Use `compatibilityMode: "panopticon"` for migrated panels unless deliberately preserving AquaQ/native behavior.
   - Use `executionMode: "sync"` for quick validation.
   - Use `executionMode: "pluginAsync"` when the query can run directly but should not block Grafana.
   - Use `executionMode: "async"` only when the target q process exposes `.grafana.asyncq.async.submit/status/result/cancel`.
   - Use `panopticonRequestFunction` when the Panopticon panel used pass-to-function or the q side expects a full request dictionary.
   - Use `panopticonQueryWrapper` when the original query must be wrapped around `{Query}`.

3. Translate parameters conservatively.
   - Keep Panopticon time parameters in the pasted query where possible: `{TimeWindowStart}`, `{TimeWindowEnd}`, `{Snapshot}`, `{FocusTime}`, `$TimeWindowStart`, `$TimeWindowEnd`, `$Snapshot`, `$FocusTime`.
   - Formatted parameters like `{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}` are supported and expand to q string literals.
   - Map Panopticon action parameters to Grafana variables manually. Do not invent variable values.

4. Build the Grafana panel.
   - Start with a Table panel for migration validation, even if the final visual will be different.
   - Set `useTimeColumn: false` unless the returned frame has a real time column and the target visualization needs it.
   - Once data matches, change the visualization type and field overrides.

5. Validate by result shape.
   - Panopticon compatibility accepts flat tables, keyed tables, symbol-keyed dictionaries, atoms, vectors, and lists of row dictionaries.
   - Sparse row dictionaries are allowed; missing cells become nulls.
   - Mixed numeric row-dictionary values widen to float columns.
   - If a result uses nested lists, custom objects, or Panopticon client transforms, add an explicit q adapter that returns a table-like shape.

6. Debug failures from the outside in.
   - Confirm Grafana can connect to kdb+ with datasource health.
   - Run the panel as `sync` first.
   - Check Grafana backend logs for connection, wrapper, parse, and result-shape errors.
   - For async, check job ID/status/result/cancel behavior in q.
   - If the q process returns an error, reproduce the compiled query or request-function call in q with the same time range.

## Output Format

When asked to migrate a panel, return:

- A short migration assessment: likely compatible, needs adapter, or not directly portable.
- The target AsyncQ query settings.
- The Grafana panel target JSON or exact fields to set in the query editor.
- Any q adapter function needed.
- Validation steps and known gaps.

Avoid claiming full dashboard copy/paste compatibility. This plugin targets query/data compatibility; Grafana visual configuration still needs translation.
