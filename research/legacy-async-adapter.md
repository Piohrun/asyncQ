# Legacy Async Adapter Design

## Goal

Allow AsyncQ to use existing Panopticon-serving q gateways on the same host/port without requiring `q/asyncq_grafana.q` or other q-side changes, when those gateways already expose a server-side async protocol.

This is for true legacy async protocols: submit a request, receive a job ID, poll status, fetch result, and optionally cancel. It is not needed for blocking legacy calls, which are already covered by `pluginAsync`.

## Non-Goals

- Import Panopticon dashboards or visual settings.
- Emulate Panopticon client UI behavior such as playback, focus windows, brushing, or actions.
- Treat datasource queries as a general writeback/action channel.
- Support arbitrary proprietary envelopes without first documenting their fields.

## Current State

Current AsyncQ modes:

| Mode | q-side requirement | Same-port legacy usefulness |
| --- | --- | --- |
| `sync` | q accepts direct expression/function call | Good for validation and fast queries |
| `pluginAsync` | q accepts direct blocking expression/function call | Good for long-running legacy calls; no q helper needed |
| `deferredAsync` | q accepts wrapper-expanded blocking expression/function call | Good for wrapper-based gateways; not true callback async |
| `legacyAsync` | q exposes configurable submit/status/result/cancel functions with parseable envelopes | Good for unchanged pull-style legacy async gateways |
| `async` | q exposes `.grafana.asyncq.async.submit/status/result/cancel` | Good only when the helper contract is available |
| `stream` | q exposes `.grafana.asyncq.stream.start/stop` and pushes payloads | Good only when the stream helper contract is available |

The implemented gap coverage is a gateway that already has pull-style async functions, but with different names, argument shapes, status values, and result envelopes. Callback/deferred/pushed-result protocols still need protocol-specific adapter work.

## Datasource And Query Configuration

Use `executionMode="legacyAsync"` with adapter fields at datasource or query level.

Datasource-level defaults and query-level overrides use the same shape:

```json
{
  "executionMode": "legacyAsync",
  "legacyAsyncSubmit": ".gw.submit",
  "legacyAsyncStatus": ".gw.status",
  "legacyAsyncResult": ".gw.result",
  "legacyAsyncCancel": ".gw.cancel",
  "legacyAsyncRequestMode": "requestDict",
  "legacyAsyncJobIDPath": "jobId",
  "legacyAsyncStatusPath": "status",
  "legacyAsyncProgressPath": "progress",
  "legacyAsyncMessagePath": "message",
  "legacyAsyncErrorPath": "error",
  "legacyAsyncPayloadPath": "result",
  "legacyAsyncQueuedValues": "queued,pending",
  "legacyAsyncRunningValues": "running,executing",
  "legacyAsyncDoneValues": "done,complete,completed",
  "legacyAsyncErrorValues": "error,failed",
  "legacyAsyncCancelledValues": "cancelled,canceled",
  "pollIntervalMs": 1000
}
```

Panel-level overrides allow changing function names and request mode per target, because some migrated dashboards may call different gateway routes on the same q port.

## Request Modes

| Request mode | Submit call | Use case |
| --- | --- | --- |
| `queryText` | `.gw.submit queryText` | Gateway accepts plain q text |
| `compiledQueryText` | `.gw.submit compiledQueryText` | Gateway expects Panopticon macros already expanded |
| `requestDict` | `.gw.submit requestDict` | Gateway expects metadata such as time window, user, datasource, and query |
| `panopticonDict` | `.gw.submit requestDict\`Panopticon` | Gateway expects only a Panopticon-style context dict |
| `functionArgs` | `.gw.submit[arg0;arg1;...]` | Gateway accepts positional args derived from query/context fields |

Implemented modes: `queryText`, `compiledQueryText`, `requestDict`, and `panopticonDict`. Add `functionArgs` only after a real gateway requires it, because positional mapping needs a schema.

## Call Sequence

1. Build the same request dictionary used by existing AsyncQ helper mode.
2. Apply Panopticon macro expansion and optional wrapper.
3. Call adapter submit function on a dedicated q connection.
4. Extract job ID and initial status from submit response.
5. Emit Grafana Live control frame with queued/running state.
6. Poll status function until terminal state, configured timeout, or panel cancellation.
7. On terminal success, call result function unless the terminal status already includes payload.
8. Parse q result through the existing compatibility parser.
9. On panel cancellation or timeout, call cancel function if configured on a short separate IPC connection, then close the active connection.

## Submit Response Shapes

Supported first-pass submit response shapes:

| q response | Adapter extraction |
| --- | --- |
| char vector or symbol | Treat as job ID |
| dictionary | Extract `jobIdPath`, `statusPath`, optional message/error/progress |
| table with one row | Extract configured columns |

Avoid parsing nested or opaque serialized submit responses until a real gateway requires it.

## Status Response Shapes

Supported first-pass status response shapes:

| q response | Adapter extraction |
| --- | --- |
| char vector or symbol | Treat as status string |
| dictionary | Extract configured status/message/error/progress/payload |
| table with one row | Extract configured columns |

Status normalization maps configured legacy values to AsyncQ states:

| AsyncQ state | Meaning |
| --- | --- |
| `queued` | Accepted but not running |
| `running` | Still executing |
| `done` | Result is available |
| `error` | Terminal failure |
| `cancelled` | Terminal cancellation |

Unknown status values should be treated as `running` but logged with diagnostics. Diagnostics preserve both `rawStatus` and `normalizedStatus`, plus whether the raw value matched a configured mapping.

## Result Response Shapes

The adapter should return the raw q payload after optional `payloadPath` extraction. Existing `compatibilityMode` then controls frame parsing:

- `panopticon` for migrated panels and flexible table-like returns.
- `native` or `aquaq` for upstream plugin compatibility.

If a gateway returns a result envelope such as `` `status`result`meta!(...) ``, configure `payloadPath="result"`.

## Diagnostics

Log these fields without raw query text unless `Log Query Text` is enabled:

- adapter name or mode
- submit/status/result/cancel function hashes or names
- request ID and job ID
- normalized status, raw status, and status-mapping match flag
- poll count and duration
- timeout duration and whether cancellation was attempted
- configured response paths used
- q object shape for submit/status/result responses
- parse errors with compatibility mode and result object shape

The most useful failure messages should say whether the problem is:

- submit function unavailable
- job ID missing
- status path missing
- unknown terminal state
- result path missing
- q-side error
- unsupported result shape

## Security

Legacy async config is powerful because it points the plugin at q functions by name. For production:

- Prefer allowlisted q functions over arbitrary query text.
- Keep raw query text logging disabled by default.
- Treat `functionArgs` mappings as trusted configuration only.
- Avoid creating symbols from Grafana variable text.
- Keep cancellation best-effort and do not assume it stops remote worker execution unless the gateway contract says so.

## Implementation Status

Implemented:

1. Go structs for datasource and query-level legacy async adapter config.
2. Query editor and datasource editor fields for `legacyAsync`.
3. Response extraction helpers for dictionaries, char/symbol atoms, nested paths, and one-row table responses.
4. `legacyAsync` RunStream path using the same Grafana Live control/data frame flow as helper async.
5. Unit tests for defaults, path extraction, result payload extraction, request modes, and status normalization.
6. Demo q process functions with one legacy-shaped async protocol that does not use `.grafana.asyncq.*` as the exposed protocol.
7. Demo dashboard panel comparing helper async and legacy async adapter behavior.
8. Total async timeout handling for submit, polling, result fetch, and best-effort cancel.
9. Live integration coverage that starts the demo q process, exercises submit/status/result, and parses the returned frame.

Still open:

- Positional function-argument mapping.
- Callback/deferred result delivery where the gateway stores a client handle and pushes later.
- Proprietary serialized Panopticon envelopes.

## Open Questions For Real Gateway Discovery

- Did Panopticon submit raw query text, a function name, a request dictionary, positional args, or a serialized object?
- Does submit return a job ID directly, or an envelope?
- Are statuses pull-based, deferred by callback, or pushed over a stored handle?
- Is result fetched by job ID or included in terminal status?
- How are q errors represented?
- Does cancellation exist, and does it stop the worker or only mark the job abandoned?
- Are user/session/entitlement fields required in the request?
- Does the gateway expect local time, UTC timestamps, dates, or formatted strings?
