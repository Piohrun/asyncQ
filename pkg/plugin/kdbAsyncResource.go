package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
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
	if d == nil {
		return backend.PluginErrorf("async resource failed: datasource is nil")
	}
	if req == nil {
		return backend.PluginErrorf("async resource failed: request is nil")
	}
	if resourceSenderIsNil(sender) {
		return backend.PluginErrorf("async resource failed: sender is nil")
	}
	liveReq, query, model, requestID, err := d.decodeAsyncRunAndWaitRequest(req.PluginContext, req.Body)
	if err != nil {
		return sendResourceJSON(sender, http.StatusBadRequest, asyncRunAndWaitResponse{
			OK:    false,
			Code:  "bad-request",
			Error: "async request failed validation: " + err.Error(),
		})
	}
	if d.asyncConfigured && !d.EnableAsync {
		return sendResourceJSON(sender, http.StatusForbidden, asyncRunAndWaitResponse{
			OK:    false,
			Code:  "forbidden",
			Error: "async queries are disabled for this datasource",
		})
	}

	fields := d.diagnosticQueryFields(req.PluginContext, query, model, requestID)
	fields = append(fields, "resourcePath", "async/run-and-wait")
	d.logDiagnostics("async run-and-wait received", fields...)

	var resp asyncRunAndWaitResponse
	switch model.ExecutionMode {
	case ExecutionModePluginAsync:
		resp = d.runPluginManagedAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeDeferredAsync:
		resp = d.runPluginManagedAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeLegacyAsync:
		resp = d.runLegacyAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	case ExecutionModeAsync:
		resp = d.runHelperAsyncQueryWait(ctx, req.PluginContext, liveReq, query, model, requestID)
	default:
		return sendResourceJSON(sender, http.StatusBadRequest, asyncRunAndWaitResponse{
			OK:            false,
			Code:          "unsupported-mode",
			RefID:         liveReq.RefID,
			RequestID:     requestID,
			ExecutionMode: model.ExecutionMode,
			Error:         "unsupported async execution mode",
		})
	}

	status := http.StatusOK
	if !resp.OK && resp.Code == "timeout" {
		status = http.StatusGatewayTimeout
	}
	return sendResourceJSON(sender, status, resp)
}

func (d *KdbDatasource) decodeAsyncRunAndWaitRequest(pCtx backend.PluginContext, raw []byte) (liveQueryRequest, backend.DataQuery, QueryModel, string, error) {
	fields, err := scanTopLevelJSONObject(raw, maxQueryJSONBytes, asyncRunAndWaitJSONFieldSpecs, false, "async request JSON")
	if err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	if err := validateQueryEnvelopeObjects(fields, false, "async request JSON"); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	if _, ok := fields["queryText"]; !ok {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("field %q is required", "queryText")
	}
	var body asyncRunAndWaitRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("async request JSON could not be decoded")
	}
	if _, present := fields["refId"]; !present {
		body.RefID = "A"
	}
	if err := validateRefID(body.RefID); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("refId is invalid: %w", err)
	}
	if _, present := fields["maxDataPoints"]; !present {
		body.MaxDataPoints = 500
	}
	if _, present := fields["intervalMs"]; !present {
		body.IntervalMs = 1000
	}
	if body.IntervalMs < 0 || body.IntervalMs > int64(maxQueryInterval/time.Millisecond) {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("intervalMs must be between 0 and %d", maxQueryInterval/time.Millisecond)
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
	if from.Before(minQTimestamp) || to.After(maxQTimestamp) {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("timeRange is outside the finite q timestamp range")
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
	queryType, err := decodedOptionalString(fields, "queryType")
	if err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	query := backend.DataQuery{
		RefID:         body.RefID,
		QueryType:     queryType,
		MaxDataPoints: body.MaxDataPoints,
		Interval:      time.Duration(body.IntervalMs) * time.Millisecond,
		TimeRange: backend.TimeRange{
			From: from,
			To:   to,
		},
	}
	if err := validateQueryEnvelopeAndBounds(query, &model, fields); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	if err := d.normalizeAndPrepareAsyncModel(pCtx, query, &model, fields, ExecutionModeAsync); err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
	}
	requestID := body.RequestID
	if _, present := fields["requestId"]; present {
		if err := validateExternalID(requestID, "requestId"); err != nil {
			return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", err
		}
	} else {
		requestID, err = newAsyncRequestID(rand.Reader)
		if err != nil {
			return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("requestId could not be generated")
		}
	}
	queryJSON, err := json.Marshal(model)
	if err != nil {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("normalized async request could not be encoded")
	}
	if len(queryJSON) > maxQueryJSONBytes {
		return liveQueryRequest{}, backend.DataQuery{}, QueryModel{}, "", fmt.Errorf("normalized async request exceeds the %d-byte limit", maxQueryJSONBytes)
	}
	query.JSON = queryJSON
	return liveReq, query, model, requestID, nil
}

func parseAsyncResourceTime(raw string, fallback time.Time, now time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if len(raw) > maxTimeoutTextBytes*2 || strings.TrimSpace(raw) != raw {
		return time.Time{}, fmt.Errorf("time value is invalid or too long")
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
		digits := raw
		if strings.HasPrefix(digits, "-") {
			digits = digits[1:]
		}
		var parsed time.Time
		if len(digits) > 10 {
			parsed = time.UnixMilli(n).UTC()
		} else {
			parsed = time.Unix(n, 0).UTC()
		}
		if parsed.Before(minQTimestamp) || parsed.After(maxQTimestamp) {
			return time.Time{}, fmt.Errorf("time value is outside the finite q timestamp range")
		}
		return parsed, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		t = t.UTC()
		if t.Before(minQTimestamp) || t.After(maxQTimestamp) {
			return time.Time{}, fmt.Errorf("time value is outside the finite q timestamp range")
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported time value; use RFC3339, Unix seconds/milliseconds, now, now-<duration>, or now+<duration>")
}

func parseAsyncResourceRelativeDuration(raw string) (time.Duration, error) {
	if raw == "" || len(raw) > maxTimeoutTextBytes || strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
		return 0, fmt.Errorf("invalid relative duration")
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.ParseUint(strings.TrimSuffix(raw, "d"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid relative duration")
		}
		return checkedRelativeDuration(n, 24*time.Hour)
	}
	if strings.HasSuffix(raw, "w") {
		n, err := strconv.ParseUint(strings.TrimSuffix(raw, "w"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid relative duration")
		}
		return checkedRelativeDuration(n, 7*24*time.Hour)
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 || duration > maxQueryInterval {
		return 0, fmt.Errorf("invalid or oversized relative duration")
	}
	return duration, nil
}

func checkedRelativeDuration(value uint64, unit time.Duration) (time.Duration, error) {
	maximum := uint64(maxQueryInterval / unit)
	if value > maximum || value > uint64(math.MaxInt64/int64(unit)) {
		return 0, fmt.Errorf("relative duration exceeds the supported range")
	}
	return time.Duration(value) * unit, nil
}

func newAsyncRequestID(reader io.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("random source is unavailable")
	}
	var random [16]byte
	if _, err := io.ReadFull(reader, random[:]); err != nil {
		return "", err
	}
	return "async-wait-" + hex.EncodeToString(random[:]), nil
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
	state = boundedStatusText(state, 32)
	jobID := boundedStatusText(status.ID, maxLiveIDBytes)
	if jobID == "" {
		jobID = boundedStatusText(r.RequestID, maxLiveIDBytes)
	}
	r.JobID = jobID
	r.Status = state
	event := asyncRunAndWaitStatusEvent{
		AtMs:      time.Since(start).Milliseconds(),
		State:     state,
		RawStatus: boundedStatusText(status.RawStatus, 256),
		JobID:     jobID,
		Message:   boundedStatusText(status.Message, 1024),
		Error:     boundedStatusText(status.Error, 2048),
		Progress:  boundedStatusProgress(status.Progress),
		Final:     final,
	}
	if count := len(r.Statuses); count > 0 && r.Statuses[count-1].State == event.State && !r.Statuses[count-1].Final {
		r.Statuses[count-1] = event
		return
	}
	if len(r.Statuses) < maxAsyncStatusEvents {
		r.Statuses = append(r.Statuses, event)
		return
	}
	// Keep the first observation and the most recent bounded transition
	// history. A terminal event always replaces the oldest non-first event.
	copy(r.Statuses[1:], r.Statuses[2:])
	r.Statuses[len(r.Statuses)-1] = event
}

func (r *asyncRunAndWaitResponse) fail(code string, state string, jobID string, message string, progress float64, final bool, start time.Time) asyncRunAndWaitResponse {
	r.OK = false
	r.Code = boundedStatusText(code, 64)
	r.Status = boundedStatusText(state, 32)
	r.Error = boundedStatusText(message, 2048)
	r.DurationMs = time.Since(start).Milliseconds()
	status := asyncQStatus{ID: jobID, Status: state, Error: message, Progress: progress}
	r.addStatus(start, state, status, final)
	return *r
}

func boundedStatusText(value string, maximum int) string {
	if maximum <= 0 || value == "" {
		return ""
	}
	if len(value) > maximum {
		value = value[:maximum]
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) > maximum {
		value = value[:maximum]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func boundedStatusProgress(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(1, value))
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
	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()
	conn, err := d.newConnection(jobCtx)
	if err != nil {
		d.logDiagnosticError("plugin async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()
	resp.addStatus(start, "running", asyncQStatus{ID: requestID, Status: "running", Progress: 0.5}, false)

	resultCh := make(chan *kdb.K, 1)
	errCh := make(chan error, 1)
	var queryWorker sync.WaitGroup
	queryWorker.Add(1)
	go func() {
		defer queryWorker.Done()
		result, err := callKdbFunctionWithContext(jobCtx, conn, queryExecutionFunction(model), buildDirectQueryRequest(pCtx, query, model))
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	defer func() {
		cancelJob()
		_ = conn.Close()
		queryWorker.Wait()
	}()
	finishContext := func(operationErrors ...error) asyncRunAndWaitResponse {
		outcome := classifyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout, operationErrors...)
		_ = conn.Close()
		if outcome.cancelled {
			d.logDiagnostics("plugin async run-and-wait cancelled", append(fields, "durationMs", time.Since(start).Milliseconds(), "error", outcome.err.Error())...)
			return resp.fail("cancelled", "cancelled", requestID, outcome.err.Error(), 1, true, start)
		}
		d.logDiagnosticError("plugin async run-and-wait timed out", appendDiagnosticError(append(fields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), outcome.err)...)
		return resp.fail("timeout", "error", requestID, outcome.err.Error(), 1, true, start)
	}

	select {
	case <-ctx.Done():
		return finishContext()
	case <-jobCtx.Done():
		return finishContext()
	case err := <-errCh:
		if _, ready := readyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout, err); ready {
			return finishContext(err)
		}
		d.logDiagnosticError("plugin async run-and-wait query failed", appendDiagnosticError(fields, err)...)
		return resp.fail("query-failed", "error", requestID, err.Error(), 1, true, start)
	case result := <-resultCh:
		if _, ready := readyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout); ready {
			return finishContext()
		}
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

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()
	conn, err := d.newConnection(jobCtx)
	if err != nil {
		d.logDiagnosticError("helper async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()

	helperReq := buildHelperRequest(pCtx, query, model, requestID, "")
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, asyncSubmitFn, helperReq)
	if err != nil {
		if outcome, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
			err = outcome.err
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
	finishContext := func(operationErrors ...error) asyncRunAndWaitResponse {
		outcome := classifyAsyncContext(ctx, jobCtx, "helper async", timeout, operationErrors...)
		if outcome.cancelled {
			d.bestEffortAsyncCancel(ctx, asyncCancelFn, jobID)
			d.logDiagnostics("helper async run-and-wait cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", outcome.err.Error())...)
			return resp.fail("cancelled", "cancelled", jobID, outcome.err.Error(), 1, true, start)
		}
		d.bestEffortAsyncCancel(ctx, asyncCancelFn, jobID)
		d.logDiagnosticError("helper async run-and-wait timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), outcome.err)...)
		return resp.fail("timeout", "error", jobID, outcome.err.Error(), 1, true, start)
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
				if _, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
					return finishContext(err)
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
					if _, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
						return finishContext(err)
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

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()
	conn, err := d.newConnection(jobCtx)
	if err != nil {
		d.logDiagnosticError("legacy async run-and-wait connection failed", appendDiagnosticError(fields, err)...)
		return resp.fail("connection-failed", "error", requestID, err.Error(), 0, true, start)
	}
	defer conn.Close()

	submitArg, err := adapter.buildSubmitArg(pCtx, query, model, requestID)
	if err != nil {
		d.logDiagnosticError("legacy async run-and-wait request build failed", appendDiagnosticError(fields, err)...)
		return resp.fail("request-build-failed", "error", requestID, err.Error(), 0, true, start)
	}
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, legacyAsyncCallExpression(adapter.submit, 1), submitArg)
	if err != nil {
		if outcome, ready := readyAsyncContext(ctx, jobCtx, "legacy async", timeout, err); ready {
			err = outcome.err
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
	finishContext := func(operationErrors ...error) asyncRunAndWaitResponse {
		outcome := classifyAsyncContext(ctx, jobCtx, "legacy async", timeout, operationErrors...)
		if outcome.cancelled {
			d.bestEffortAsyncCancel(ctx, cancelFn, jobID)
			d.logDiagnostics("legacy async run-and-wait cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", outcome.err.Error())...)
			return resp.fail("cancelled", "cancelled", jobID, outcome.err.Error(), 1, true, start)
		}
		d.bestEffortAsyncCancel(ctx, cancelFn, jobID)
		d.logDiagnosticError("legacy async run-and-wait timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), outcome.err)...)
		return resp.fail("timeout", "error", jobID, outcome.err.Error(), 1, true, start)
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
				if _, ready := readyAsyncContext(ctx, jobCtx, "legacy async", timeout, err); ready {
					return finishContext(err)
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
						if _, ready := readyAsyncContext(ctx, jobCtx, "legacy async", timeout, err); ready {
							return finishContext(err)
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
