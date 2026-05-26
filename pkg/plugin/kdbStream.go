package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
)

const (
	asyncSubmitFn = "{[req] .grafana.asyncq.async.submit req}"
	asyncStatusFn = "{[jobId] .grafana.asyncq.async.status jobId}"
	asyncResultFn = "{[jobId] .grafana.asyncq.async.result jobId}"
	asyncCancelFn = "{[jobId] .grafana.asyncq.async.cancel jobId}"
	streamStartFn = "{[req] .grafana.asyncq.stream.start req}"
	streamStopFn  = "{[streamId] .grafana.asyncq.stream.stop streamId}"
)

type liveQueryRequest struct {
	QueryModel
	RefID         string        `json:"refId"`
	MaxDataPoints int64         `json:"maxDataPoints"`
	IntervalMs    int64         `json:"intervalMs"`
	TimeRange     liveTimeRange `json:"timeRange"`
}

type liveTimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type asyncQStatus struct {
	ID         string
	Status     string
	RawStatus  string
	Message    string
	Error      string
	ErrorClass string
	StackTrace string
	Worker     string
	Started    string
	Finished   string
	ResultType string
	Progress   float64
	Payload    *kdb.K
}

func (d *KdbDatasource) SubscribeStream(_ context.Context, req *backend.SubscribeStreamRequest) (*backend.SubscribeStreamResponse, error) {
	if isAsyncPath(req.Path) {
		if d.asyncConfigured && !d.EnableAsync {
			return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, fmt.Errorf("async queries are disabled for this datasource")
		}
		if len(req.Data) == 0 {
			return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, fmt.Errorf("missing stream request data")
		}
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK}, nil
	}
	if isStreamPath(req.Path) {
		if d.streamConfigured && !d.EnableStreaming {
			return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, fmt.Errorf("streaming is disabled for this datasource")
		}
		if len(req.Data) == 0 {
			return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, fmt.Errorf("missing stream request data")
		}
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK}, nil
	}
	return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, fmt.Errorf("unsupported stream path: %s", req.Path)
}

func (d *KdbDatasource) PublishStream(_ context.Context, _ *backend.PublishStreamRequest) (*backend.PublishStreamResponse, error) {
	return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, nil
}

func (d *KdbDatasource) RunStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) error {
	switch {
	case isAsyncPath(req.Path):
		return d.runAsyncQueryStream(ctx, req, sender)
	case isStreamPath(req.Path):
		return d.runKdbPushStream(ctx, req, sender)
	default:
		return fmt.Errorf("unsupported stream path: %s", req.Path)
	}
}

func isAsyncPath(path string) bool {
	return strings.HasPrefix(path, "async/")
}

func isStreamPath(path string) bool {
	return strings.HasPrefix(path, "stream/")
}

func pathID(path string) string {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 2 && parts[1] != "" {
		return parts[1]
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func decodeLiveQueryRequest(raw json.RawMessage, mode string, path string) (liveQueryRequest, backend.DataQuery, QueryModel, string, error) {
	var liveReq liveQueryRequest
	if err := json.Unmarshal(raw, &liveReq); err != nil {
		return liveReq, backend.DataQuery{}, QueryModel{}, "", err
	}
	model := liveReq.QueryModel
	normalizeQueryModel(&model)
	if mode == ExecutionModeStream {
		model.ExecutionMode = ExecutionModeStream
	} else if model.ExecutionMode == "" || model.ExecutionMode == ExecutionModeSync || model.ExecutionMode == ExecutionModeStream {
		model.ExecutionMode = ExecutionModeAsync
	}
	if liveReq.RefID == "" {
		liveReq.RefID = "A"
	}

	from, _ := time.Parse(time.RFC3339Nano, liveReq.TimeRange.From)
	to, _ := time.Parse(time.RFC3339Nano, liveReq.TimeRange.To)
	queryJSON, _ := json.Marshal(model)
	query := backend.DataQuery{
		RefID:         liveReq.RefID,
		MaxDataPoints: liveReq.MaxDataPoints,
		Interval:      time.Duration(liveReq.IntervalMs) * time.Millisecond,
		TimeRange: backend.TimeRange{
			From: from,
			To:   to,
		},
		JSON: queryJSON,
	}
	return liveReq, query, model, pathID(path), nil
}

func (d *KdbDatasource) runAsyncQueryStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) error {
	liveReq, query, model, requestID, err := decodeLiveQueryRequest(req.Data, ExecutionModeAsync, req.Path)
	if err != nil {
		d.logDiagnosticError("unable to decode async live query", "path", req.Path, "error", err.Error())
		return err
	}
	d.normalizeAsyncQueryModel(liveReq, &model)
	fields := d.diagnosticQueryFields(req.PluginContext, query, model, requestID)
	fields = append(fields, "path", req.Path)
	d.logDiagnostics("async query received", fields...)
	switch model.ExecutionMode {
	case ExecutionModePluginAsync:
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("plugin async query preparation failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
			return nil
		}
		fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, requestID), "path", req.Path)
		d.logDiagnostics("plugin async query prepared", fields...)
		return d.runPluginManagedAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeDeferredAsync:
		model.OriginalQueryText = model.QueryText
		wrappedQuery, err := applyDeferredQueryWrapper(model.QueryText, model.DeferredQueryWrapper)
		if err != nil {
			d.logDiagnosticError("deferred async wrapper failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
			return nil
		}
		model.QueryText = wrappedQuery
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, requestID), "path", req.Path)
			d.logDiagnosticError("deferred async query preparation failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
			return nil
		}
		fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, requestID), "path", req.Path)
		d.logDiagnostics("deferred async query prepared", fields...)
		return d.runPluginManagedAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeLegacyAsync:
		if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
			d.logDiagnosticError("legacy async query preparation failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
			return nil
		}
		fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, requestID), "path", req.Path)
		d.logDiagnostics("legacy async query prepared", fields...)
		return d.runLegacyAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeAsync, "":
	default:
		err := fmt.Errorf("unsupported async execution mode: %s", model.ExecutionMode)
		d.logDiagnosticError("async query rejected", appendDiagnosticError(fields, err)...)
		return err
	}
	if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
		d.logDiagnosticError("helper async query preparation failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, requestID), "path", req.Path)
	d.logDiagnostics("helper async query prepared", fields...)

	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("helper async connection failed", appendDiagnosticError(fields, err)...)
		return err
	}
	defer conn.Close()

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()

	helperReq := buildHelperRequest(req.PluginContext, query, model, requestID, "")
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, asyncSubmitFn, helperReq)
	if err != nil {
		if jobCtx.Err() != nil {
			err = asyncContextError(jobCtx, "helper async", timeout)
		}
		err = fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
		d.logDiagnosticError("helper async submit failed", appendDiagnosticError(fields, err)...)
		return err
	}
	status := parseAsyncQStatus(submitRes, requestID)
	jobID := status.ID
	if jobID == "" {
		jobID = requestID
	}
	jobFields := append(append([]interface{}{}, fields...), "jobID", jobID)
	submitStatus := status
	submitStatus.Status = statusWithDefault(submitStatus.Status, "queued")
	d.logDiagnostics("helper async submitted", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), submitStatus)...)
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, statusWithDefault(status.Status, "queued"), jobID, status.Message, status.Error, status.Progress, false); err != nil {
		d.logDiagnosticError("helper async status send failed", appendDiagnosticError(jobFields, err)...)
		return err
	}

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	lastState := strings.ToLower(statusWithDefault(status.Status, "queued"))
	finishContext := func() error {
		if ctx.Err() != nil {
			d.bestEffortAsyncCancel(asyncCancelFn, jobID)
			d.logDiagnostics("helper async cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "cancelled", jobID, "", ctx.Err().Error(), 1, true)
			return ctx.Err()
		}
		err := asyncContextError(jobCtx, "helper async", timeout)
		d.bestEffortAsyncCancel(asyncCancelFn, jobID)
		d.logDiagnosticError("helper async timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), 1, true)
		return nil
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
				d.logDiagnosticError("helper async status failed", appendDiagnosticError(jobFields, err)...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), 0, true)
				return nil
			}
			status = parseAsyncQStatus(statusRes, jobID)
			state := strings.ToLower(statusWithDefault(status.Status, "running"))
			if state != lastState {
				status.Status = state
				d.logDiagnostics("helper async status changed", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status)...)
				lastState = state
			}
			if state == "error" {
				err := fmt.Errorf("%s", status.Error)
				d.logDiagnosticError("helper async returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status), err)...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			}
			if state == "cancelled" || state == "canceled" {
				status.Status = state
				d.logDiagnostics("helper async cancelled by q", append(d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status), "durationMs", time.Since(start).Milliseconds())...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "cancelled", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			}
			if state == "done" || state == "complete" || state == "completed" {
				result, err := callKdbFunctionWithContext(jobCtx, conn, asyncResultFn, kdb.Atom(kdb.KC, jobID))
				if err != nil {
					if jobCtx.Err() != nil {
						return finishContext()
					}
					d.logDiagnosticError("helper async result failed", appendDiagnosticError(jobFields, err)...)
					_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), status.Progress, true)
					return nil
				}
				resultFields := appendDiagnosticKdbObject(append([]interface{}{}, jobFields...), "kdbResponse", result)
				frames, err := parseKdbResponseToFrames(result, model, liveReq.RefID)
				if err != nil {
					d.logDiagnosticError("helper async result parse failed", appendDiagnosticError(resultFields, err)...)
					_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), status.Progress, true)
					return nil
				}
				resultFields = appendDiagnosticFrames(resultFields, frames)
				status.Status = "done"
				resultFields = d.appendDiagnosticAsyncStatus(resultFields, status)
				resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
				for _, frame := range frames {
					markFrame(frame, ExecutionModeAsync, "data", jobID, false, false)
					attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
					if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
						d.logDiagnosticError("helper async data send failed", appendDiagnosticError(resultFields, err)...)
						return err
					}
				}
				d.logDiagnostics("helper async completed", resultFields...)
				return sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "done", jobID, status.Message, "", 1, true)
			}
			if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, state, jobID, status.Message, status.Error, status.Progress, false); err != nil {
				d.logDiagnosticError("helper async status send failed", appendDiagnosticError(jobFields, err)...)
				return err
			}
		}
	}
}

func (d *KdbDatasource) normalizeAsyncQueryModel(liveReq liveQueryRequest, model *QueryModel) {
	if liveReq.QueryModel.ExecutionMode == "" {
		model.ExecutionMode = ""
	}
	d.normalizeQueryModel(model)
	if model.ExecutionMode == "" || model.ExecutionMode == ExecutionModeSync || model.ExecutionMode == ExecutionModeStream {
		model.ExecutionMode = ExecutionModeAsync
	}
}

func (d *KdbDatasource) runPluginManagedAsyncQueryStream(ctx context.Context, pCtx backend.PluginContext, liveReq liveQueryRequest, query backend.DataQuery, model QueryModel, requestID string, sender *backend.StreamSender) error {
	fields := d.diagnosticQueryFields(pCtx, query, model, requestID)
	start := time.Now()
	if err := d.acquireAsyncSlot(ctx); err != nil {
		d.logDiagnosticError("plugin async slot unavailable", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	defer d.releaseAsyncSlot()

	d.logDiagnostics("plugin async queued", fields...)
	if err := sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "queued", requestID, "", "", 0, false); err != nil {
		d.logDiagnosticError("plugin async queued status send failed", appendDiagnosticError(fields, err)...)
		return err
	}

	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("plugin async connection failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	defer conn.Close()
	d.logDiagnostics("plugin async started", fields...)

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

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	timeout := asyncTimeoutDuration(model)
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			d.logDiagnostics("plugin async cancelled", append(fields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "cancelled", requestID, "", ctx.Err().Error(), 1, true)
			return nil
		case <-timeoutTimer.C:
			err := fmt.Errorf("%s query timed out after %v", model.ExecutionMode, timeout)
			_ = conn.Close()
			d.logDiagnosticError("plugin async timed out", appendDiagnosticError(append(fields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 1, true)
			return nil
		case err := <-errCh:
			d.logDiagnosticError("plugin async query failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 1, true)
			return nil
		case result := <-resultCh:
			resultFields := appendDiagnosticKdbObject(append([]interface{}{}, fields...), "kdbResponse", result)
			frames, err := parseKdbResponseToFrames(result, model, liveReq.RefID)
			if err != nil {
				d.logDiagnosticError("plugin async result parse failed", appendDiagnosticError(resultFields, err)...)
				_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 1, true)
				return nil
			}
			resultFields = appendDiagnosticFrames(resultFields, frames)
			resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
			for _, frame := range frames {
				markFrame(frame, model.ExecutionMode, "data", requestID, false, false)
				attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
				if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
					d.logDiagnosticError("plugin async data send failed", appendDiagnosticError(resultFields, err)...)
					return err
				}
			}
			d.logDiagnostics("plugin async completed", resultFields...)
			return sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "done", requestID, "", "", 1, true)
		case <-ticker.C:
			if err := sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "running", requestID, "", "", 0.5, false); err != nil {
				_ = conn.Close()
				d.logDiagnosticError("plugin async running status send failed", appendDiagnosticError(fields, err)...)
				return err
			}
		}
	}
}

func asyncTimeoutDuration(model QueryModel) time.Duration {
	timeoutMs := model.Timeout
	if timeoutMs < 1 {
		timeoutMs = defaultQueryTimeout
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

func (d *KdbDatasource) bestEffortAsyncCancel(fn string, jobID string) {
	fn = strings.TrimSpace(fn)
	if fn == "" || jobID == "" {
		return
	}
	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnostics("async cancel connection failed", "functionHash", diagnosticHash(fn), "jobID", jobID, "error", err.Error())
		return
	}
	defer conn.Close()

	cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := callKdbFunctionWithContext(cancelCtx, conn, fn, kdb.Atom(kdb.KC, jobID)); err != nil {
		d.logDiagnostics("async cancel failed", "functionHash", diagnosticHash(fn), "jobID", jobID, "error", err.Error())
	}
}

func (d *KdbDatasource) acquireAsyncSlot(ctx context.Context) error {
	d.normalizeDatasourceDefaults()
	select {
	case d.asyncJobs <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("async job limit reached (%d)", d.AsyncMaxJobs)
	}
}

func (d *KdbDatasource) releaseAsyncSlot() {
	select {
	case <-d.asyncJobs:
	default:
	}
}

func (d *KdbDatasource) runKdbPushStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) error {
	liveReq, query, model, streamID, err := decodeLiveQueryRequest(req.Data, ExecutionModeStream, req.Path)
	if err != nil {
		d.logDiagnosticError("unable to decode stream query", "path", req.Path, "error", err.Error())
		return err
	}
	d.normalizeQueryModel(&model)
	model.ExecutionMode = ExecutionModeStream
	fields := d.diagnosticQueryFields(req.PluginContext, query, model, streamID)
	fields = append(fields, "path", req.Path, "streamID", streamID)
	d.logDiagnostics("stream query received", fields...)
	if err := prepareQueryForExecution(req.PluginContext, query, &model); err != nil {
		d.logDiagnosticError("stream query preparation failed", appendDiagnosticError(fields, err)...)
		return err
	}
	fields = append(d.diagnosticQueryFields(req.PluginContext, query, model, streamID), "path", req.Path, "streamID", streamID)
	d.logDiagnostics("stream query prepared", fields...)
	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("stream connection failed", appendDiagnosticError(fields, err)...)
		return err
	}
	defer conn.Close()

	helperReq := buildHelperRequest(req.PluginContext, query, model, streamID, streamID)
	startRes, err := callKdbFunction(conn, streamStartFn, helperReq)
	if err != nil {
		err = fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
		d.logDiagnosticError("stream start failed", appendDiagnosticError(fields, err)...)
		return err
	}
	status := parseAsyncQStatus(startRes, streamID)
	status.Status = statusWithDefault(status.Status, "running")
	d.logDiagnostics("stream started", d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status)...)
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, statusWithDefault(status.Status, "running"), streamID, status.Message, status.Error, status.Progress, false); err != nil {
		d.logDiagnosticError("stream start status send failed", appendDiagnosticError(fields, err)...)
		return err
	}
	stopOnContextCancel := make(chan struct{})
	defer close(stopOnContextCancel)
	go func() {
		select {
		case <-ctx.Done():
			d.stopKdbStream(streamID)
			_ = conn.Close()
		case <-stopOnContextCancel:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			d.stopKdbStream(streamID)
			d.logDiagnostics("stream cancelled", append(fields, "error", ctx.Err().Error())...)
			return nil
		default:
		}

		msg, _, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.logDiagnosticError("stream read stopped", appendDiagnosticError(fields, err)...)
			d.stopKdbStream(streamID)
			return nil
		}
		status := parseAsyncQStatus(msg, streamID)
		state := strings.ToLower(statusWithDefault(status.Status, "data"))
		if state == "error" {
			err := fmt.Errorf("%s", status.Error)
			status.Status = state
			d.logDiagnosticError("stream returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status), err)...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, status.Message, status.Error, status.Progress, true)
			return nil
		}
		if state == "done" || state == "complete" || state == "completed" {
			status.Status = state
			d.logDiagnostics("stream completed", d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status)...)
			return sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "done", streamID, status.Message, "", status.Progress, true)
		}
		payload := status.Payload
		if payload == nil && msg != nil && (msg.Type == kdb.XT || msg.Type == kdb.XD) {
			payload = msg
		}
		if payload == nil {
			status.Status = state
			d.logDiagnostics("stream status received", d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status)...)
			if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, state, streamID, status.Message, status.Error, status.Progress, false); err != nil {
				d.logDiagnosticError("stream status send failed", appendDiagnosticError(fields, err)...)
				return err
			}
			continue
		}
		payloadFields := appendDiagnosticKdbObject(append([]interface{}{}, fields...), "kdbResponse", payload)
		frames, err := parseKdbResponseToFrames(payload, model, liveReq.RefID)
		if err != nil {
			d.logDiagnosticError("stream payload parse failed", appendDiagnosticError(payloadFields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, "", err.Error(), 0, true)
			return nil
		}
		payloadFields = appendDiagnosticFrames(payloadFields, frames)
		for _, frame := range frames {
			markFrame(frame, ExecutionModeStream, "data", streamID, false, false)
			attachAsyncQDiagnostics([]*data.Frame{frame}, payloadFields)
			if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
				d.logDiagnosticError("stream data send failed", appendDiagnosticError(payloadFields, err)...)
				return err
			}
		}
		d.logDiagnostics("stream payload sent", payloadFields...)
	}
}

func (d *KdbDatasource) stopKdbStream(streamID string) {
	conn, err := d.newConnection()
	if err != nil {
		log.DefaultLogger.Info("unable to open stream stop connection", "streamID", streamID, "error", err)
		return
	}
	defer conn.Close()
	if _, err := callKdbFunction(conn, streamStopFn, kdb.Atom(kdb.KC, streamID)); err != nil {
		log.DefaultLogger.Info("unable to stop kdb+ stream", "streamID", streamID, "error", err)
	}
}

func callKdbFunction(conn *kdb.KDBConn, fn string, args ...*kdb.K) (*kdb.K, error) {
	res, err := conn.Call(fn, args...)
	if err != nil {
		return nil, err
	}
	if res != nil && res.Type == kdb.KERR {
		return nil, fmt.Errorf("kdb+ error: %v", res.Data)
	}
	return res, nil
}

func callKdbFunctionWithContext(ctx context.Context, conn *kdb.KDBConn, fn string, args ...*kdb.K) (*kdb.K, error) {
	type callResult struct {
		res *kdb.K
		err error
	}
	done := make(chan callResult, 1)
	go func() {
		res, err := callKdbFunction(conn, fn, args...)
		done <- callResult{res: res, err: err}
	}()

	select {
	case result := <-done:
		return result.res, result.err
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	}
}

func asyncContextError(ctx context.Context, mode string, timeout time.Duration) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s query timed out after %v", mode, timeout)
	}
	return ctx.Err()
}

func buildHelperRequest(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, requestID string, streamID string) *kdb.K {
	baseKeys, baseValues := buildMasterKdbLists(pCtx, query, model)
	keys := append(append([]string{}, baseKeys.Data.([]string)...),
		"RequestID", "StreamID", "PollIntervalMs", "MaxStreamRows", "StreamRetentionMs")
	values := append(append([]*kdb.K{}, baseValues.Data.([]*kdb.K)...),
		kdb.Atom(kdb.KC, requestID),
		kdb.Atom(kdb.KC, streamID),
		kdb.Long(int64(model.PollIntervalMs)),
		kdb.Long(int64(model.MaxStreamRows)),
		kdb.Long(int64(model.StreamRetentionMs)))
	return kdb.NewDict(kdb.SymbolV(keys), kdb.NewList(values...))
}

func parseAsyncQStatus(k *kdb.K, fallbackID string) asyncQStatus {
	status := asyncQStatus{ID: fallbackID}
	if k == nil {
		return status
	}
	if k.Type != kdb.XD {
		if k.Type == kdb.KC || k.Type == -kdb.KS {
			status.ID = kdbString(k)
			status.Status = "queued"
			status.RawStatus = "queued"
		}
		return status
	}
	if v, ok := dictLookup(k, "JobID", "RequestID", "StreamID", "ID"); ok {
		status.ID = kdbString(v)
	}
	if v, ok := dictLookup(k, "Status", "State", "MessageType"); ok {
		status.Status = kdbString(v)
		status.RawStatus = status.Status
	}
	if v, ok := dictLookup(k, "Message"); ok {
		status.Message = kdbString(v)
	}
	if v, ok := dictLookup(k, "Error"); ok {
		status.Error = kdbString(v)
	}
	if v, ok := dictLookup(k, "ErrorClass", "Class"); ok {
		status.ErrorClass = kdbString(v)
	}
	if v, ok := dictLookup(k, "StackTrace", "Backtrace", "Trace"); ok {
		status.StackTrace = kdbString(v)
	}
	if v, ok := dictLookup(k, "Worker", "WorkerID", "WorkerId"); ok {
		status.Worker = kdbString(v)
	}
	if v, ok := dictLookup(k, "Started", "StartTime"); ok {
		status.Started = kdbString(v)
	}
	if v, ok := dictLookup(k, "Finished", "FinishTime", "EndTime"); ok {
		status.Finished = kdbString(v)
	}
	if v, ok := dictLookup(k, "ResultType", "PayloadType"); ok {
		status.ResultType = kdbString(v)
	}
	if v, ok := dictLookup(k, "Progress"); ok {
		status.Progress = kdbFloat(v)
	}
	if v, ok := dictLookup(k, "Payload", "Data", "Result"); ok {
		status.Payload = v
	}
	return status
}

func dictLookup(k *kdb.K, names ...string) (*kdb.K, bool) {
	if k == nil || k.Type != kdb.XD {
		return nil, false
	}
	dict := k.Data.(kdb.Dict)
	keys, ok := dict.Key.Data.([]string)
	if !ok {
		return nil, false
	}
	values, ok := dict.Value.Data.([]*kdb.K)
	if !ok {
		return nil, false
	}
	for i, key := range keys {
		for _, name := range names {
			if key == name && i < len(values) {
				return values[i], true
			}
		}
	}
	return nil, false
}

func kdbString(k *kdb.K) string {
	if k == nil {
		return ""
	}
	switch k.Type {
	case -kdb.KS:
		return k.Data.(string)
	case kdb.KC:
		return k.Data.(string)
	case -kdb.KC:
		return string(k.Data.(byte))
	case kdb.KS:
		return strings.Join(k.Data.([]string), ",")
	default:
		return fmt.Sprint(k.Data)
	}
}

func kdbFloat(k *kdb.K) float64 {
	if k == nil {
		return 0
	}
	switch k.Type {
	case -kdb.KF:
		return k.Data.(float64)
	case -kdb.KE:
		return float64(k.Data.(float32))
	case -kdb.KJ:
		return float64(k.Data.(int64))
	case -kdb.KI:
		return float64(k.Data.(int32))
	default:
		return 0
	}
}

func statusWithDefault(status string, fallback string) string {
	if status == "" {
		return fallback
	}
	return strings.ToLower(status)
}

func sendControlFrame(sender *backend.StreamSender, refID string, mode string, state string, id string, message string, errText string, progress float64, terminal bool) error {
	frame := data.NewFrame("asyncq-status",
		data.NewField("time", nil, []time.Time{time.Now()}),
		data.NewField("state", nil, []string{state}),
		data.NewField("message", nil, []string{message}),
		data.NewField("error", nil, []string{errText}),
		data.NewField("progress", nil, []float64{progress}),
	)
	frame.RefID = refID
	markFrame(frame, mode, state, id, terminal, true)
	return sender.SendFrame(frame, data.IncludeAll)
}

func markFrame(frame *data.Frame, mode string, state string, id string, terminal bool, control bool) {
	frame.Meta = &data.FrameMeta{
		Custom: map[string]interface{}{
			"asyncqMode":     mode,
			"asyncqState":    state,
			"asyncqID":       id,
			"asyncqTerminal": terminal,
			"asyncqControl":  control,
		},
	}
}

func frameSchema(frame *data.Frame) string {
	parts := make([]string, 0, len(frame.Fields)+1)
	parts = append(parts, frame.Name)
	for _, field := range frame.Fields {
		parts = append(parts, fmt.Sprintf("%s:%s", field.Name, field.Type()))
	}
	return strings.Join(parts, "|")
}
