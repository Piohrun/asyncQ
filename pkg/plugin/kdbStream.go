package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
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

func (d *KdbDatasource) SubscribeStream(ctx context.Context, req *backend.SubscribeStreamRequest) (response *backend.SubscribeStreamResponse, err error) {
	if d == nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, backend.PluginErrorf("live subscription failed: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()
	if req == nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, backend.PluginErrorf("live subscription failed: request is nil")
	}
	d.normalizeDatasourceDefaults()
	admitted, admissionErr := d.admitLiveQueryRequest(req.PluginContext, req.Data, req.Path)
	if admissionErr != nil {
		status := backend.SubscribeStreamStatusPermissionDenied
		if _, _, pathErr := admitLivePath(req.Path); pathErr != nil {
			status = backend.SubscribeStreamStatusNotFound
		}
		return &backend.SubscribeStreamResponse{Status: status}, backend.PluginErrorf("live subscription failed validation: %v", admissionErr)
	}
	if err := d.authorizeLiveAdmission(admitted); err != nil {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, err
	}
	return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK}, nil
}

func (d *KdbDatasource) PublishStream(ctx context.Context, req *backend.PublishStreamRequest) (response *backend.PublishStreamResponse, err error) {
	if d == nil {
		return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, backend.PluginErrorf("live publication failed: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()
	if req == nil {
		return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusNotFound}, backend.PluginErrorf("live publication failed: request is nil")
	}
	d.normalizeDatasourceDefaults()
	if _, _, pathErr := admitLivePath(req.Path); pathErr != nil {
		return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusNotFound}, backend.PluginErrorf("live publication failed validation: %v", pathErr)
	}
	return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, nil
}

func (d *KdbDatasource) RunStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) (err error) {
	if d == nil {
		return backend.PluginErrorf("live stream failed: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()
	if req == nil {
		return backend.PluginErrorf("live stream failed: request is nil")
	}
	if sender == nil {
		return backend.PluginErrorf("live stream failed: sender is nil")
	}
	d.normalizeDatasourceDefaults()
	admitted, err := d.admitLiveQueryRequest(req.PluginContext, req.Data, req.Path)
	if err != nil {
		return backend.PluginErrorf("live stream failed validation: %v", err)
	}
	if err := d.authorizeLiveAdmission(admitted); err != nil {
		return err
	}
	switch admitted.family {
	case ExecutionModeAsync:
		return d.runAsyncQueryStream(ctx, req, sender, admitted)
	case ExecutionModeStream:
		return d.runKdbPushStream(ctx, req, sender, admitted)
	default:
		return backend.PluginErrorf("live stream failed validation: unsupported path family")
	}
}

func (d *KdbDatasource) authorizeLiveAdmission(admitted admittedLiveQuery) error {
	switch admitted.family {
	case ExecutionModeAsync:
		if d.asyncConfigured && !d.EnableAsync {
			return backend.PluginErrorf("async queries are disabled for this datasource")
		}
	case ExecutionModeStream:
		if d.streamConfigured && !d.EnableStreaming {
			return backend.PluginErrorf("streaming is disabled for this datasource")
		}
	default:
		return backend.PluginErrorf("live stream failed validation: unsupported path family")
	}
	return nil
}

func (d *KdbDatasource) runAsyncQueryStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender, admitted admittedLiveQuery) error {
	liveReq, query, model, requestID := admitted.liveReq, admitted.query, admitted.model, admitted.id
	fields := d.diagnosticQueryFields(req.PluginContext, query, model, requestID)
	fields = append(fields, "path", req.Path)
	d.logDiagnostics("async query received", fields...)
	switch model.ExecutionMode {
	case ExecutionModePluginAsync:
		return d.runPluginManagedAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeDeferredAsync:
		return d.runPluginManagedAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeLegacyAsync:
		return d.runLegacyAsyncQueryStream(ctx, req.PluginContext, liveReq, query, model, requestID, sender)
	case ExecutionModeAsync:
	default:
		err := fmt.Errorf("unsupported async execution mode")
		d.logDiagnosticError("async query rejected", appendDiagnosticError(fields, err)...)
		return err
	}

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()
	conn, err := d.newConnection(jobCtx)
	if err != nil {
		d.logDiagnosticError("helper async connection failed", appendDiagnosticError(fields, err)...)
		return err
	}
	defer conn.Close()

	helperReq := buildHelperRequest(req.PluginContext, query, model, requestID, "")
	submitRes, err := callKdbFunctionWithContext(jobCtx, conn, asyncSubmitFn, helperReq)
	if err != nil {
		if outcome, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
			err = outcome.err
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
	finishContext := func(operationErrors ...error) error {
		outcome := classifyAsyncContext(ctx, jobCtx, "helper async", timeout, operationErrors...)
		if outcome.cancelled {
			d.bestEffortAsyncCancel(ctx, asyncCancelFn, jobID)
			d.logDiagnostics("helper async cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", outcome.err.Error())...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "cancelled", jobID, "", outcome.err.Error(), 1, true)
			return outcome.err
		}
		d.bestEffortAsyncCancel(ctx, asyncCancelFn, jobID)
		d.logDiagnosticError("helper async timed out", appendDiagnosticError(append(jobFields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), outcome.err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", outcome.err.Error(), 1, true)
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
				if _, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
					return finishContext(err)
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
					if _, ready := readyAsyncContext(ctx, jobCtx, "helper async", timeout, err); ready {
						return finishContext(err)
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

	timeout := asyncTimeoutDuration(model)
	jobCtx, cancelJob := context.WithTimeout(ctx, timeout)
	defer cancelJob()
	conn, err := d.newConnection(jobCtx)
	if err != nil {
		d.logDiagnosticError("plugin async connection failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	defer conn.Close()
	d.logDiagnostics("plugin async started", fields...)

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

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	finishContext := func(operationErrors ...error) error {
		outcome := classifyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout, operationErrors...)
		_ = conn.Close()
		if outcome.cancelled {
			d.logDiagnostics("plugin async cancelled", append(fields, "durationMs", time.Since(start).Milliseconds(), "error", outcome.err.Error())...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "cancelled", requestID, "", outcome.err.Error(), 1, true)
			return nil
		}
		d.logDiagnosticError("plugin async timed out", appendDiagnosticError(append(fields, "durationMs", time.Since(start).Milliseconds(), "timeoutMs", timeout.Milliseconds()), outcome.err)...)
		_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", outcome.err.Error(), 1, true)
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return finishContext()
		case <-jobCtx.Done():
			return finishContext()
		case err := <-errCh:
			if _, ready := readyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout, err); ready {
				return finishContext(err)
			}
			d.logDiagnosticError("plugin async query failed", appendDiagnosticError(fields, err)...)
			_ = sendControlFrame(sender, liveReq.RefID, model.ExecutionMode, "error", requestID, "", err.Error(), 1, true)
			return nil
		case result := <-resultCh:
			if _, ready := readyAsyncContext(ctx, jobCtx, model.ExecutionMode, timeout); ready {
				return finishContext()
			}
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

func (d *KdbDatasource) bestEffortAsyncCancel(ctx context.Context, fn string, jobID string) {
	fn = strings.TrimSpace(fn)
	if fn == "" || jobID == "" {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeSyncQueryContext(ctx)), 2*time.Second)
	defer cancel()
	conn, err := d.newCleanupConnection(cancelCtx)
	if err != nil {
		d.logDiagnostics("async cancel connection failed", "functionHash", diagnosticHash(fn), "jobID", jobID, "error", err.Error())
		return
	}
	defer conn.Close()

	if _, err := callKdbFunctionWithContext(cancelCtx, conn, fn, kdb.Atom(kdb.KC, jobID)); err != nil {
		d.logDiagnostics("async cancel failed", "functionHash", diagnosticHash(fn), "jobID", jobID, "error", err.Error())
	}
}

func (d *KdbDatasource) acquireAsyncSlot(ctx context.Context) error {
	ctx = normalizeSyncQueryContext(ctx)
	if !d.hasActiveLifecycleLease(ctx) {
		return fmt.Errorf("async slot acquisition requires an admitted datasource operation: %w", ErrDatasourceDisposed)
	}

	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	d.initializeLifecycleLocked()
	if d.lifecycleStopping {
		return ErrDatasourceDisposed
	}
	if err := syncQueryContextError(ctx, "async slot acquisition interrupted"); err != nil {
		return err
	}
	d.normalizeDatasourceDefaults()
	select {
	case <-ctx.Done():
		return syncQueryContextError(ctx, "async slot acquisition interrupted")
	case d.asyncJobs <- struct{}{}:
		if err := syncQueryContextError(ctx, "async slot acquisition interrupted"); err != nil {
			<-d.asyncJobs
			return err
		}
		return nil
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

func (d *KdbDatasource) runKdbPushStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender, admitted admittedLiveQuery) error {
	liveReq, query, model, streamID := admitted.liveReq, admitted.query, admitted.model, admitted.id
	fields := d.diagnosticQueryFields(req.PluginContext, query, model, streamID)
	fields = append(fields, "path", req.Path, "streamID", streamID)
	d.logDiagnostics("stream query received", fields...)
	startCtx, cancelStart := context.WithTimeout(ctx, asyncTimeoutDuration(model))
	conn, err := d.newConnection(startCtx)
	if err != nil {
		cancelStart()
		d.logDiagnosticError("stream connection failed", appendDiagnosticError(fields, err)...)
		return err
	}
	defer conn.Close()

	helperReq := buildHelperRequest(req.PluginContext, query, model, streamID, streamID)
	startRes, err := callKdbFunctionWithContext(startCtx, conn, streamStartFn, helperReq)
	cancelStart()
	if err != nil {
		err = fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
		d.logDiagnosticError("stream start failed", appendDiagnosticError(fields, err)...)
		return err
	}
	status := parseAsyncQStatus(startRes, streamID)
	status.Status = statusWithDefault(status.Status, "running")
	d.logDiagnostics("stream started", d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status)...)
	stopStream := onceAsyncStreamStop(func() {
		d.stopKdbStream(ctx, streamID)
	})
	remoteTerminated := false
	defer func() {
		if !remoteTerminated {
			stopStream()
		}
	}()
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, statusWithDefault(status.Status, "running"), streamID, status.Message, status.Error, status.Progress, false); err != nil {
		d.logDiagnosticError("stream start status send failed", appendDiagnosticError(fields, err)...)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			stopStream()
			d.logDiagnostics("stream cancelled", append(fields, "error", ctx.Err().Error())...)
			return nil
		default:
		}

		msg, messageType, err := conn.ReadMessageContext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				stopStream()
				return nil
			}
			d.logDiagnosticError("stream read stopped", appendDiagnosticError(fields, err)...)
			stopStream()
			return nil
		}
		if err := validateStreamPushMessageType(messageType); err != nil {
			d.logDiagnosticError("stream read rejected", appendDiagnosticError(fields, err)...)
			stopStream()
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, "", err.Error(), 0, true)
			return err
		}
		status := parseAsyncQStatus(msg, streamID)
		state := strings.ToLower(statusWithDefault(status.Status, "data"))
		if state == "error" {
			remoteTerminated = true
			err := fmt.Errorf("%s", status.Error)
			status.Status = state
			d.logDiagnosticError("stream returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, fields...), status), err)...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, status.Message, status.Error, status.Progress, true)
			return nil
		}
		if state == "done" || state == "complete" || state == "completed" {
			remoteTerminated = true
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

func validateStreamPushMessageType(messageType kdb.ReqType) error {
	if messageType != kdb.ASYNC {
		return fmt.Errorf("%w: kdb+ stream push received request type %d instead of ASYNC", kdb.ErrBadMsg, messageType)
	}
	return nil
}

func (d *KdbDatasource) stopKdbStream(ctx context.Context, streamID string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeSyncQueryContext(ctx)), 2*time.Second)
	defer cancel()
	conn, err := d.newCleanupConnection(stopCtx)
	if err != nil {
		log.DefaultLogger.Info("unable to open stream stop connection", "streamID", streamID, "error", err)
		return
	}
	defer conn.Close()
	if _, err := callKdbFunctionWithContext(stopCtx, conn, streamStopFn, kdb.Atom(kdb.KC, streamID)); err != nil {
		log.DefaultLogger.Info("unable to stop kdb+ stream", "streamID", streamID, "error", err)
	}
}

func callKdbFunctionWithContext(ctx context.Context, conn *kdb.KDBConn, fn string, args ...*kdb.K) (*kdb.K, error) {
	res, err := conn.CallContext(ctx, fn, args...)
	if err != nil {
		return nil, err
	}
	if res != nil && res.Type == kdb.KERR {
		return nil, fmt.Errorf("kdb+ error: %v", res.Data)
	}
	return res, nil
}

func asyncContextError(ctx context.Context, mode string, timeout time.Duration) error {
	err := syncQueryContextError(ctx, mode+" query interrupted")
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s query timed out after %v: %w", mode, timeout, err)
	}
	return err
}

func readyAsyncContext(parentCtx context.Context, jobCtx context.Context, mode string, timeout time.Duration, operationErrors ...error) (asyncContextOutcome, bool) {
	parentCtx = normalizeSyncQueryContext(parentCtx)
	jobCtx = normalizeSyncQueryContext(jobCtx)
	var operationErr error
	if len(operationErrors) > 0 {
		operationErr = operationErrors[0]
	}
	if parentCtx.Err() == nil &&
		jobCtx.Err() == nil &&
		!errors.Is(operationErr, context.Canceled) &&
		!errors.Is(operationErr, context.DeadlineExceeded) {
		return asyncContextOutcome{}, false
	}
	return classifyAsyncContext(parentCtx, jobCtx, mode, timeout, operationErr), true
}

type asyncContextOutcome struct {
	cancelled bool
	err       error
}

func onceAsyncStreamStop(stop func()) func() {
	if stop == nil {
		return func() {}
	}
	return sync.OnceFunc(stop)
}

func classifyAsyncContext(parentCtx context.Context, jobCtx context.Context, mode string, timeout time.Duration, operationErrors ...error) asyncContextOutcome {
	if err := syncQueryContextError(parentCtx, mode+" query cancelled"); err != nil {
		return asyncContextOutcome{cancelled: true, err: err}
	}
	if err := asyncContextError(jobCtx, mode, timeout); err != nil {
		return asyncContextOutcome{
			cancelled: !errors.Is(err, context.DeadlineExceeded),
			err:       err,
		}
	}
	var operationErr error
	if len(operationErrors) > 0 {
		operationErr = operationErrors[0]
	}
	if errors.Is(operationErr, context.DeadlineExceeded) {
		return asyncContextOutcome{
			cancelled: false,
			err:       fmt.Errorf("%s query timed out after %v: %w", mode, timeout, operationErr),
		}
	}
	if errors.Is(operationErr, context.Canceled) {
		return asyncContextOutcome{
			cancelled: true,
			err:       fmt.Errorf("%s query interrupted: %w", mode, operationErr),
		}
	}
	return asyncContextOutcome{
		cancelled: true,
		err:       fmt.Errorf("%s query interrupted without a context cause: %w", mode, context.Canceled),
	}
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
	if sender == nil {
		return backend.PluginErrorf("live stream response sender is nil")
	}
	mode = boundedStatusText(mode, 32)
	state = boundedStatusText(state, 32)
	id = boundedStatusText(id, maxLiveIDBytes)
	message = boundedStatusText(message, 1024)
	errText = boundedStatusText(errText, 2048)
	progress = boundedStatusProgress(progress)
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
	if frame == nil {
		return
	}
	frame.Meta = &data.FrameMeta{
		Custom: map[string]interface{}{
			"asyncqMode":     boundedStatusText(mode, 32),
			"asyncqState":    boundedStatusText(state, 32),
			"asyncqID":       boundedStatusText(id, maxLiveIDBytes),
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
