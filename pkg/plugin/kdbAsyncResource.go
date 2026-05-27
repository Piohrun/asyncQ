package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
)

type asyncRunAndWaitRequest struct {
	QueryModel
	RefID         string        `json:"refId,omitempty"`
	MaxDataPoints int64         `json:"maxDataPoints,omitempty"`
	IntervalMs    int64         `json:"intervalMs,omitempty"`
	TimeRange     liveTimeRange `json:"timeRange,omitempty"`
	RequestID     string        `json:"requestId,omitempty"`
}

type asyncRunAndWaitResponse struct {
	OK                bool                         `json:"ok"`
	Code              string                       `json:"code,omitempty"`
	RefID             string                       `json:"refId,omitempty"`
	RequestID         string                       `json:"requestId,omitempty"`
	JobID             string                       `json:"jobId,omitempty"`
	ExecutionMode     string                       `json:"executionMode,omitempty"`
	CompatibilityMode string                       `json:"compatibilityMode,omitempty"`
	QueryHash         string                       `json:"queryHash,omitempty"`
	Status            string                       `json:"status,omitempty"`
	DurationMs        int64                        `json:"durationMs,omitempty"`
	Statuses          []asyncRunAndWaitStatusEvent `json:"statuses,omitempty"`
	Frames            data.Frames                  `json:"frames,omitempty"`
	Error             string                       `json:"error,omitempty"`
}

type asyncRunAndWaitStatusEvent struct {
	AtMs      int64   `json:"atMs"`
	State     string  `json:"state"`
	RawStatus string  `json:"rawStatus,omitempty"`
	JobID     string  `json:"jobId,omitempty"`
	Message   string  `json:"message,omitempty"`
	Error     string  `json:"error,omitempty"`
	Progress  float64 `json:"progress,omitempty"`
	Final     bool    `json:"final,omitempty"`
}

func (d *KdbDatasource) handleAsyncRunAndWaitResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	liveReq, query, model, requestID, err := d.decodeAsyncRunAndWaitRequest(req.Body)
	if err != nil {
		return sendResourceJSON(sender, http.StatusBadRequest, asyncRunAndWaitResponse{OK: false, Code: "bad-request", Error: err.Error()})
	}
	if model.ExecutionMode == ExecutionModeStream {
		return sendResourceJSON(sender, http.StatusBadRequest, asyncRunAndWaitResponse{
			OK:            false,
			Code:          "unsupported-mode",
			RefID:         liveReq.RefID,
			RequestID:     requestID,
			ExecutionMode: model.ExecutionMode,
			Error:         "stream mode is an open subscription and is not supported by async/run-and-wait; use async/pluginAsync/deferredAsync/legacyAsync",
		})
	}

	fields := d.diagnosticQueryFields(req.PluginContext, query, model, requestID)
	fields = append(fields, "resourcePath", "async/run-and-wait")
	d.logDiagnostics("async run-and-wait received", fields...)

	var resp asyncRunAndWaitResponse
	switch model.ExecutionMode {
	case ExecutionModePluginAsync:
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("plugin async run-and-wait preparation failed", appendDiagnosticError(fields, err)...)
			resp = newAsyncRunAndWaitResponse(liveReq, model, requestID)
			resp.fail("prepare-failed", "error", "", err.Error(), 0, true, time.Now())
			return sendResourceJSON(sender, http.StatusOK, resp)
		}
		resp = d.runPluginManagedAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeDeferredAsync:
		model.OriginalQueryText = model.QueryText
		wrappedQuery, err := applyDeferredQueryWrapper(model.QueryText, model.DeferredQueryWrapper)
		if err != nil {
			d.logDiagnosticError("deferred async run-and-wait wrapper failed", appendDiagnosticError(fields, err)...)
			resp = newAsyncRunAndWaitResponse(liveReq, model, requestID)
			resp.fail("wrapper-failed", "error", "", err.Error(), 0, true, time.Now())
			return sendResourceJSON(sender, http.StatusOK, resp)
		}
		model.QueryText = wrappedQuery
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("deferred async run-and-wait preparation failed", appendDiagnosticError(fields, err)...)
			resp = newAsyncRunAndWaitResponse(liveReq, model, requestID)
			resp.fail("prepare-failed", "error", "", err.Error(), 0, true, time.Now())
			return sendResourceJSON(sender, http.StatusOK, resp)
		}
		resp = d.runPluginManagedAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeLegacyAsync:
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("legacy async run-and-wait preparation failed", appendDiagnosticError(fields, err)...)
			resp = newAsyncRunAndWaitResponse(liveReq, model, requestID)
			resp.fail("prepare-failed", "error", "", err.Error(), 0, true, time.Now())
			return sendResourceJSON(sender, http.StatusOK, resp)
		}
		resp = d.runLegacyAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeAsync, "":
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("helper async run-and-wait preparation failed", appendDiagnosticError(fields, err)...)
			resp = newAsyncRunAndWaitResponse(liveReq, model, requestID)
			resp.fail("prepare-failed", "error", "", err.Error(), 0, true, time.Now())
			return sendResourceJSON(sender, http.StatusOK, resp)
		}
		resp = d.runHelperAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	default:
		return sendResourceJSON(sender, http.StatusBadRequest, asyncRunAndWaitResponse{
			OK:            false,
			Code:          "unsupported-mode",
			RefID:         liveReq.RefID,
			RequestID:     requestID,
			ExecutionMode: model.ExecutionMode,
			Error:         fmt.Sprintf("unsupported async execution mode %q", model.ExecutionMode),
		})
	}

	status := http.StatusOK
	if !resp.OK && resp.Code == "timeout" {
		status = http.StatusGatewayTimeout
	}
	return sendResourceJSON(sender, status, resp)
}

func (d *KdbDatasource) decodeAsyncRunAndWaitRequest(raw []byte) (liveQueryRequest, backend.DataQuery, QueryModel, string, error) {
	var body asyncRunAndWaitRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	if strings.TrimSpace(body.QueryText) == "" {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("queryText is required")
	}
	if body.RefID == "" {
		body.RefID = "A"
	}
	if body.MaxDataPoints < 1 {
		body.MaxDataPoints = 500
	}
	if body.IntervalMs < 1 {
		body.IntervalMs = 1000
	}

	now := time.Now().UTC()
	from, err := parseAsyncResourceTime(body.TimeRange.From, now.Add(-time.Hour), now)
	if err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("parse timeRange.from: %w", err)
	}
	to, err := parseAsyncResourceTime(body.TimeRange.To, now, now)
	if err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("parse timeRange.to: %w", err)
	}
	if !from.Before(to) {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("timeRange.from must be before timeRange.to")
	}

	liveReq := liveQueryRequest{
		QueryModel:    body.QueryModel,
		RefID:         body.RefID,
		MaxDataPoints: body.MaxDataPoints,
		IntervalMs:    body.IntervalMs,
		TimeRange: liveTimeRange{
			From: from.Format(time.RFC3339Nano),
			To:   to.Format(time.RFC3339Nano),
		},
	}
	model := body.QueryModel
	normalizeQueryModel(&model)
	d.normalizeAsyncQueryModel(liveReq, &model)
	requestID := strings.TrimSpace(body.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("async-wait-%d", time.Now().UnixNano())
	}
	queryJSON, _ := json.Marshal(model)
	query := backend.DataQuery{
		RefID:         body.RefID,
		MaxDataPoints: body.MaxDataPoints,
		Interval:      time.Duration(body.IntervalMs) * time.Millisecond,
		TimeRange: backend.TimeRange{
			From: from,
			To:   to,
		},
		JSON: queryJSON,
	}
	return liveReq, query, model, requestID, nil
}

func parseAsyncResourceTime(raw string, fallback time.Time, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	if raw == "now" {
		return now, nil
	}
	if strings.HasPrefix(raw, "now-") {
		d, err := parseAsyncResourceRelativeDuration(strings.TrimPrefix(raw, "now-"))
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(-d), nil
	}
	if strings.HasPrefix(raw, "now+") {
		d, err := parseAsyncResourceRelativeDuration(strings.TrimPrefix(raw, "now+"))
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(d), nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if len(raw) > 10 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time value %q; use RFC3339, Unix seconds/milliseconds, now, or now-<duration>", raw)
}

func parseAsyncResourceRelativeDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, fmt.Errorf("missing relative duration")
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.ParseInt(strings.TrimSuffix(raw, "d"), 10, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if strings.HasSuffix(raw, "w") {
		n, err := strconv.ParseInt(strings.TrimSuffix(raw, "w"), 10, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func newAsyncRunAndWaitResponse(liveReq liveQueryRequest, model QueryModel, requestID string) asyncRunAndWaitResponse {
	return asyncRunAndWaitResponse{
		OK:                false,
		RefID:             liveReq.RefID,
		RequestID:         requestID,
		ExecutionMode:     model.ExecutionMode,
		CompatibilityMode: model.CompatibilityMode,
		QueryHash:         diagnosticHash(model.QueryText),
		Status:            "queued",
	}
}

func (r *asyncRunAndWaitResponse) addStatus(start time.Time, state string, status asyncQStatus, final bool) {
	jobID := status.ID
	if jobID == "" {
		jobID = r.RequestID
	}
	r.JobID = jobID
	r.Status = state
	r.Statuses = append(r.Statuses, asyncRunAndWaitStatusEvent{
		AtMs:      time.Since(start).Milliseconds(),
		State:     state,
		RawStatus: status.RawStatus,
		JobID:     jobID,
		Message:   status.Message,
		Error:     status.Error,
		Progress:  status.Progress,
		Final:     final,
	})
}

func (r *asyncRunAndWaitResponse) fail(code string, state string, jobID string, message string, progress float64, final bool, start time.Time) asyncRunAndWaitResponse {
	r.OK = false
	r.Code = code
	r.Status = state
	r.Error = message
	r.DurationMs = time.Since(start).Milliseconds()
	status := asyncQStatus{ID: jobID, Status: state, Error: message, Progress: progress}
	r.addStatus(start, state, status, final)
	return *r
}

func (r *asyncRunAndWaitResponse) complete(start time.Time, frames data.Frames, status asyncQStatus) asyncRunAndWaitResponse {
	r.OK = true
	r.Code = ""
	r.Error = ""
	r.Frames = frames
	r.DurationMs = time.Since(start).Milliseconds()
	r.addStatus(start, "done", status, true)
	return *r
}

func (d *KdbDatasource) runPluginManagedAsyncQueryWait(ctx context.Context, pCtx backend.PluginContext, liveReq liveQueryRequest, query backend.DataQuery, model QueryModel, requestID string) asyncRunAndWaitResponse {
	start := time.Now()
	resp := newAsyncRunAndWaitResponse(liveReq, model, requestID)
	fields := d.diagnosticQueryFields(pCtx, query, model, requestID)

	if err := d.acquireAsyncSlot(ctx); err != nil {
		d.logDiagnosticError("plugin async run-and-wait slot unavailable", appendDiagnosticError(fields, err)...)
		return resp.fail("async-slot-unavailable", "error", requestID, err.Error(), 0, true, start)
	}
	defer d.releaseAsyncSlot()

	resp.addStatus(start, "queued", asyncQStatus{ID: requestID, Status: "queued"}, false)
	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("plugin async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()
	resp.addStatus(start, "running", asyncQStatus{ID: requestID, Status: "running", Progress: 0.5}, false)

	resultCh := make(chan *kdb.K, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := callKdbFunction(conn, queryExecutionFunction(model), buildDirectQueryRequest(pCtx, query, model))
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	timeout := asyncTimeoutDuration(model)
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		d.logDiagnostics("plugin async run-and-wait cancelled", append(fields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
		return resp.fail("cancelled", "cancelled", requestID, ctx.Err().Error(), 1, true, start)
	case <-timeoutTimer.C:
		_ = conn.Close()
		err := fmt.Errorf("%s query timed out after %v", model.ExecutionMode, timeout)
		d.logDiagnosticError("plugin async run-and-wait timed out", appendDiagnosticError(append(fields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), err)...)
		return resp.fail("timeout", "error", requestID, err.Error(), 1, true, start)
	case err := <-errCh:
		d.logDiagnosticError("plugin async run-and-wait query failed", appendDiagnosticError(fields, err)...)
		return resp.fail("query-failed", "error", requestID, err.Error(), 1, true, start)
	case result := <-resultCh:
		resultFields := appendDiagnosticKdbObject(append([]interface{}{}, fields...), "kdbResponse", result)
		frames, err := parseKdbResponseToFrames(result, model, liveReq.RefID)
		if err != nil {
			d.logDiagnosticError("plugin async run-and-wait result parse failed", appendDiagnosticError(resultFields, err)...)
			return resp.fail("parse-failed", "error", requestID, err.Error(), 1, true, start)
		}
		resultFields = appendDiagnosticFrames(resultFields, frames)
		resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
		for _, frame := range frames {
			markFrame(frame, model.ExecutionMode, "data", requestID, false, false)
			attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
		}
		d.logDiagnostics("plugin async run-and-wait completed", resultFields...)
		return resp.complete(start, frames, asyncQStatus{ID: requestID, Status: "done", Progress: 1})
	}
}

func (d *KdbDatasource) runHelperAsyncQueryWait(ctx context.Context, pCtx backend.PluginContext, liveReq liveQueryRequest, query backend.DataQuery, model QueryModel, requestID string) asyncRunAndWaitResponse {
	start := time.Now()
	resp := newAsyncRunAndWaitResponse(liveReq, model, requestID)
	fields := d.diagnosticQueryFields(pCtx, query, model, requestID)

	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("helper async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()

	helperReq := buildHelperRequest(pCtx, query, model, requestID, "")
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, asyncSubmitFn, helperReq)
	if err != nil {
		if jobCtx.Err() != nil {
			err = asyncContextError(jobCtx, "helper async", timeout)
		}
		err = fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
		d.logDiagnosticError("helper async run-and-wait submit failed", appendDiagnosticError(fields, err)...)
		return resp.fail("submit-failed", "error", requestID, err.Error(), 0, true, start)
	}
	status := parseAsyncQStatus(submitRes, requestID)
	jobID := status.ID
	if jobID == "" {
		jobID = requestID
	}
	status.Status = statusWithDefault(status.Status, "queued")
	resp.addStatus(start, status.Status, status, false)
	jobFields := append(append([]interface{}{}, fields...), "jobID", jobID)
	d.logDiagnostics("helper async run-and-wait submitted", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status)...)

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	lastState := strings.ToLower(statusWithDefault(status.Status, "queued"))
	finishContext := func() asyncRunAndWaitResponse {
		if ctx.Err() != nil {
			d.bestEffortAsyncCancel(asyncCancelFn, jobID)
			d.logDiagnostics("helper async run-and-wait cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
			return resp.fail("cancelled", "cancelled", jobID, ctx.Err().Error(), 1, true, start)
		}
		err := asyncContextError(jobCtx, "helper async", timeout)
		d.bestEffortAsyncCancel(asyncCancelFn, jobID)
		d.logDiagnosticError("helper async run-and-wait timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), err)...)
		return resp.fail("timeout", "error", jobID, err.Error(), 1, true, start)
	}

	for {
		select {
		case <-ctx.Done():
			return finishContext()
		case <-jobCtx.Done():
			return finishContext()
		case <-ticker.C:
			statusRes, err := callKdbFunctionWithContext(jobCtx, conn, asyncStatusFn, kdb.Atom(kdb.KC, jobID))
			if err != nil {
				if jobCtx.Err() != nil {
					return finishContext()
				}
				d.logDiagnosticError("helper async run-and-wait status failed", appendDiagnosticError(jobFields, err)...)
				return resp.fail("status-failed", "error", jobID, err.Error(), 0, true, start)
			}
			status = parseAsyncQStatus(statusRes, jobID)
			state := strings.ToLower(statusWithDefault(status.Status, "running"))
			if state != lastState {
				status.Status = state
				d.logDiagnostics("helper async run-and-wait status changed", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status)...)
				lastState = state
			}
			resp.addStatus(start, state, status, false)
			switch state {
			case "error":
				err := fmt.Errorf("%s", status.Error)
				d.logDiagnosticError("helper async run-and-wait returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status), err)...)
				return resp.fail("query-failed", "error", jobID, status.Error, status.Progress, true, start)
			case "cancelled", "canceled":
				d.logDiagnostics("helper async run-and-wait cancelled by q", append(d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status), "durationMs", time.Since(start).Milliseconds())...)
				return resp.fail("cancelled", "cancelled", jobID, status.Error, status.Progress, true, start)
			case "done", "complete", "completed":
				result, err := callKdbFunctionWithContext(jobCtx, conn, asyncResultFn, kdb.Atom(kdb.KC, jobID))
				if err != nil {
					if jobCtx.Err() != nil {
						return finishContext()
					}
					d.logDiagnosticError("helper async run-and-wait result failed", appendDiagnosticError(jobFields, err)...)
					return resp.fail("result-failed", "error", jobID, err.Error(), status.Progress, true, start)
				}
				resultFields := appendDiagnosticKdbObject(append([]interface{}{}, jobFields...), "kdbResponse", result)
				frames, err := parseKdbResponseToFrames(result, model, liveReq.RefID)
				if err != nil {
					d.logDiagnosticError("helper async run-and-wait result parse failed", appendDiagnosticError(resultFields, err)...)
					return resp.fail("parse-failed", "error", jobID, err.Error(), status.Progress, true, start)
				}
				status.Status = "done"
				status.Progress = 1
				resultFields = appendDiagnosticFrames(resultFields, frames)
				resultFields = d.appendDiagnosticAsyncStatus(resultFields, status)
				resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
				for _, frame := range frames {
					markFrame(frame, ExecutionModeAsync, "data", jobID, false, false)
					attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
				}
				d.logDiagnostics("helper async run-and-wait completed", resultFields...)
				return resp.complete(start, frames, status)
			}
		}
	}
}

func (d *KdbDatasource) runLegacyAsyncQueryWait(ctx context.Context, pCtx backend.PluginContext, liveReq liveQueryRequest, query backend.DataQuery, model QueryModel, requestID string) asyncRunAndWaitResponse {
	start := time.Now()
	resp := newAsyncRunAndWaitResponse(liveReq, model, requestID)
	adapter := legacyAsyncAdapterFromModel(model)
	fields := d.diagnosticQueryFields(pCtx, query, model, requestID)
	fields = append(fields,
		"legacyAsyncRequestMode", adapter.requestMode,
		"legacyAsyncSubmitHash", diagnosticHash(adapter.submit),
		"legacyAsyncStatusHash", diagnosticHash(adapter.status),
		"legacyAsyncResultHash", diagnosticHash(adapter.result),
		"legacyAsyncCancelHash", diagnosticHash(adapter.cancel),
		"legacyAsyncJobIDPath", adapter.jobIDPath,
		"legacyAsyncStatusPath", adapter.statusPath,
		"legacyAsyncPayloadPath", adapter.payloadPath,
	)
	if err := adapter.validate(); err != nil {
		d.logDiagnosticError("legacy async run-and-wait configuration invalid", appendDiagnosticError(fields, err)...)
		return resp.fail("configuration-invalid", "error", requestID, err.Error(), 0, true, start)
	}
	if err := d.acquireAsyncSlot(ctx); err != nil {
		d.logDiagnosticError("legacy async run-and-wait slot unavailable", appendDiagnosticError(fields, err)...)
		return resp.fail("async-slot-unavailable", "error", requestID, err.Error(), 0, true, start)
	}
	defer d.releaseAsyncSlot()

	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("legacy async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()

	submitArg, err := adapter.buildSubmitArg(pCtx, query, model, requestID)
	if err != nil {
		d.logDiagnosticError("legacy async run-and-wait request build failed", appendDiagnosticError(fields, err)...)
		return resp.fail("request-build-failed", "error", requestID, err.Error(), 0, true, start)
	}
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, legacyAsyncCallExpression(adapter.submit, 1), submitArg)
	if err != nil {
		if jobCtx.Err() != nil {
			err = asyncContextError(jobCtx, "legacy async", timeout)
		}
		d.logDiagnosticError("legacy async run-and-wait submit failed", appendDiagnosticError(fields, err)...)
		return resp.fail("submit-failed", "error", requestID, err.Error(), 0, true, start)
	}
	submitFields := appendDiagnosticKdbObject(append([]interface{}{}, fields...), "legacyAsyncSubmitResponse", submitRes)
	status, err := adapter.parseSubmitResponse(submitRes, requestID)
	if err != nil {
		d.logDiagnosticError("legacy async run-and-wait submit parse failed", appendDiagnosticError(submitFields, err)...)
		return resp.fail("submit-parse-failed", "error", requestID, err.Error(), 0, true, start)
	}
	jobID := status.ID
	rawStatus := status.Status
	state, mapped := adapter.normalizeStatusDetail(rawStatus, "queued")
	status.RawStatus = rawStatus
	status.Status = state
	resp.addStatus(start, state, status, false)
	jobFields := append(submitFields, "jobID", jobID, "legacyAsyncRawStatus", rawStatus, "legacyAsyncNormalizedStatus", state, "legacyAsyncStatusMapped", mapped)
	d.logDiagnostics("legacy async run-and-wait submitted", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status)...)

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	lastState := state
	cancelFn := ""
	if adapter.cancel != "" {
		cancelFn = legacyAsyncCallExpression(adapter.cancel, 1)
	}
	finishContext := func() asyncRunAndWaitResponse {
		if ctx.Err() != nil {
			d.bestEffortAsyncCancel(cancelFn, jobID)
			d.logDiagnostics("legacy async run-and-wait cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
			return resp.fail("cancelled", "cancelled", jobID, ctx.Err().Error(), 1, true, start)
		}
		err := asyncContextError(jobCtx, "legacy async", timeout)
		d.bestEffortAsyncCancel(cancelFn, jobID)
		d.logDiagnosticError("legacy async run-and-wait timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), err)...)
		return resp.fail("timeout", "error", jobID, err.Error(), 1, true, start)
	}

	for {
		select {
		case <-ctx.Done():
			return finishContext()
		case <-jobCtx.Done():
			return finishContext()
		case <-ticker.C:
			statusRes, err := callKdbFunctionWithContext(jobCtx, conn, legacyAsyncCallExpression(adapter.status, 1), kdb.Atom(kdb.KC, jobID))
			if err != nil {
				if jobCtx.Err() != nil {
					return finishContext()
				}
				d.logDiagnosticError("legacy async run-and-wait status failed", appendDiagnosticError(jobFields, err)...)
				return resp.fail("status-failed", "error", jobID, err.Error(), 0, true, start)
			}
			statusFields := appendDiagnosticKdbObject(append([]interface{}{}, jobFields...), "legacyAsyncStatusResponse", statusRes)
			status, err = adapter.parseStatusResponse(statusRes, jobID)
			if err != nil {
				d.logDiagnosticError("legacy async run-and-wait status parse failed", appendDiagnosticError(statusFields, err)...)
				return resp.fail("status-parse-failed", "error", jobID, err.Error(), 0, true, start)
			}
			rawStatus := status.Status
			state, mapped := adapter.normalizeStatusDetail(rawStatus, "running")
			status.RawStatus = rawStatus
			status.Status = state
			statusFields = append(statusFields, "legacyAsyncRawStatus", rawStatus, "legacyAsyncNormalizedStatus", state, "legacyAsyncStatusMapped", mapped)
			if state != lastState {
				d.logDiagnostics("legacy async run-and-wait status changed", d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status)...)
				lastState = state
			}
			resp.addStatus(start, state, status, false)
			switch state {
			case "error":
				err := fmt.Errorf("%s", status.Error)
				d.logDiagnosticError("legacy async run-and-wait returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status), err)...)
				return resp.fail("query-failed", "error", jobID, status.Error, status.Progress, true, start)
			case "cancelled":
				d.logDiagnostics("legacy async run-and-wait cancelled by q", append(d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status), "durationMs", time.Since(start).Milliseconds())...)
				return resp.fail("cancelled", "cancelled", jobID, status.Error, status.Progress, true, start)
			case "done":
				payload := status.Payload
				if payload == nil {
					if adapter.result == "" {
						err := fmt.Errorf("legacy async reached done state but no payload was present and no result function is configured")
						d.logDiagnosticError("legacy async run-and-wait result unavailable", appendDiagnosticError(statusFields, err)...)
						return resp.fail("result-unavailable", "error", jobID, err.Error(), status.Progress, true, start)
					}
					resultRes, err := callKdbFunctionWithContext(jobCtx, conn, legacyAsyncCallExpression(adapter.result, 1), kdb.Atom(kdb.KC, jobID))
					if err != nil {
						if jobCtx.Err() != nil {
							return finishContext()
						}
						d.logDiagnosticError("legacy async run-and-wait result failed", appendDiagnosticError(statusFields, err)...)
						return resp.fail("result-failed", "error", jobID, err.Error(), status.Progress, true, start)
					}
					statusFields = appendDiagnosticKdbObject(statusFields, "legacyAsyncResultResponse", resultRes)
					payload, err = adapter.extractResultPayload(resultRes)
					if err != nil {
						d.logDiagnosticError("legacy async run-and-wait result parse failed", appendDiagnosticError(statusFields, err)...)
						return resp.fail("result-parse-failed", "error", jobID, err.Error(), status.Progress, true, start)
					}
				}
				resultFields := appendDiagnosticKdbObject(append([]interface{}{}, statusFields...), "kdbResponse", payload)
				frames, err := parseKdbResponseToFrames(payload, model, liveReq.RefID)
				if err != nil {
					d.logDiagnosticError("legacy async run-and-wait frame parse failed", appendDiagnosticError(resultFields, err)...)
					return resp.fail("parse-failed", "error", jobID, err.Error(), status.Progress, true, start)
				}
				resultFields = appendDiagnosticFrames(resultFields, frames)
				resultFields = d.appendDiagnosticAsyncStatus(resultFields, status)
				resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
				for _, frame := range frames {
					markFrame(frame, ExecutionModeLegacyAsync, "data", jobID, false, false)
					attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
				}
				d.logDiagnostics("legacy async run-and-wait completed", resultFields...)
				status.Status = "done"
				status.Progress = 1
				return resp.complete(start, frames, status)
			}
		}
	}
}
