package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
)

type legacyAsyncAdapter struct {
	submit          string
	status          string
	result          string
	cancel          string
	requestMode     string
	jobIDPath       string
	statusPath      string
	progressPath    string
	messagePath     string
	errorPath       string
	payloadPath     string
	queuedValues    map[string]bool
	runningValues   map[string]bool
	doneValues      map[string]bool
	errorValues     map[string]bool
	cancelledValues map[string]bool
}

func normalizeLegacyAsyncDefaults(requestMode, jobIDPath, statusPath, progressPath, messagePath, errorPath, payloadPath, queuedValues, runningValues, doneValues, errorValues, cancelledValues *string) {
	switch strings.TrimSpace(*requestMode) {
	case LegacyAsyncRequestModeQueryText, LegacyAsyncRequestModeCompiledQueryText, LegacyAsyncRequestModePanopticonDict:
	case LegacyAsyncRequestModeRequestDict, "":
		*requestMode = LegacyAsyncRequestModeRequestDict
	default:
		*requestMode = LegacyAsyncRequestModeRequestDict
	}
	defaultString(jobIDPath, defaultLegacyAsyncJobIDPath)
	defaultString(statusPath, defaultLegacyAsyncStatus)
	defaultString(progressPath, defaultLegacyAsyncProgress)
	defaultString(messagePath, defaultLegacyAsyncMessage)
	defaultString(errorPath, defaultLegacyAsyncError)
	defaultString(payloadPath, defaultLegacyAsyncPayload)
	defaultString(queuedValues, "queued,pending")
	defaultString(runningValues, "running,executing")
	defaultString(doneValues, "done,complete,completed")
	defaultString(errorValues, "error,failed")
	defaultString(cancelledValues, "cancelled,canceled")
}

func defaultString(target *string, value string) {
	if strings.TrimSpace(*target) == "" {
		*target = value
	}
}

func legacyAsyncAdapterFromModel(model QueryModel) legacyAsyncAdapter {
	normalizeLegacyAsyncDefaults(
		&model.LegacyAsyncRequestMode,
		&model.LegacyAsyncJobIDPath,
		&model.LegacyAsyncStatusPath,
		&model.LegacyAsyncProgressPath,
		&model.LegacyAsyncMessagePath,
		&model.LegacyAsyncErrorPath,
		&model.LegacyAsyncPayloadPath,
		&model.LegacyAsyncQueuedValues,
		&model.LegacyAsyncRunningValues,
		&model.LegacyAsyncDoneValues,
		&model.LegacyAsyncErrorValues,
		&model.LegacyAsyncCancelledValues,
	)
	return legacyAsyncAdapter{
		submit:          strings.TrimSpace(model.LegacyAsyncSubmit),
		status:          strings.TrimSpace(model.LegacyAsyncStatus),
		result:          strings.TrimSpace(model.LegacyAsyncResult),
		cancel:          strings.TrimSpace(model.LegacyAsyncCancel),
		requestMode:     strings.TrimSpace(model.LegacyAsyncRequestMode),
		jobIDPath:       strings.TrimSpace(model.LegacyAsyncJobIDPath),
		statusPath:      strings.TrimSpace(model.LegacyAsyncStatusPath),
		progressPath:    strings.TrimSpace(model.LegacyAsyncProgressPath),
		messagePath:     strings.TrimSpace(model.LegacyAsyncMessagePath),
		errorPath:       strings.TrimSpace(model.LegacyAsyncErrorPath),
		payloadPath:     strings.TrimSpace(model.LegacyAsyncPayloadPath),
		queuedValues:    legacyValueSet(model.LegacyAsyncQueuedValues),
		runningValues:   legacyValueSet(model.LegacyAsyncRunningValues),
		doneValues:      legacyValueSet(model.LegacyAsyncDoneValues),
		errorValues:     legacyValueSet(model.LegacyAsyncErrorValues),
		cancelledValues: legacyValueSet(model.LegacyAsyncCancelledValues),
	}
}

func legacyValueSet(csv string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(csv, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			out[item] = true
		}
	}
	return out
}

func (a legacyAsyncAdapter) validate() error {
	if a.submit == "" {
		return fmt.Errorf("legacy async submit function is required")
	}
	if a.status == "" {
		return fmt.Errorf("legacy async status function is required")
	}
	return nil
}

func (a legacyAsyncAdapter) normalizeStatus(raw string, fallback string) string {
	status := strings.ToLower(strings.TrimSpace(raw))
	if status == "" {
		status = fallback
	}
	switch {
	case a.queuedValues[status]:
		return "queued"
	case a.runningValues[status]:
		return "running"
	case a.doneValues[status]:
		return "done"
	case a.errorValues[status]:
		return "error"
	case a.cancelledValues[status]:
		return "cancelled"
	default:
		return "running"
	}
}

func (a legacyAsyncAdapter) buildSubmitArg(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, requestID string) (*kdb.K, error) {
	switch a.requestMode {
	case LegacyAsyncRequestModeQueryText:
		queryText := model.OriginalQueryText
		if queryText == "" {
			queryText = model.QueryText
		}
		return kdb.Atom(kdb.KC, queryText), nil
	case LegacyAsyncRequestModeCompiledQueryText:
		return kdb.Atom(kdb.KC, model.QueryText), nil
	case LegacyAsyncRequestModePanopticonDict:
		return buildPanopticonKdbDict(query, model), nil
	case LegacyAsyncRequestModeRequestDict:
		return buildHelperRequest(pCtx, query, model, requestID, ""), nil
	default:
		return nil, fmt.Errorf("unsupported legacy async request mode: %s", a.requestMode)
	}
}

func legacyAsyncCallExpression(fn string, arity int) string {
	fn = strings.TrimSpace(fn)
	if strings.HasPrefix(fn, "{") {
		return fn
	}
	switch arity {
	case 1:
		return fmt.Sprintf("{[x] %s x}", fn)
	default:
		return fn
	}
}

func (a legacyAsyncAdapter) parseSubmitResponse(k *kdb.K, fallbackID string) (asyncQStatus, error) {
	status := asyncQStatus{ID: fallbackID}
	if k == nil {
		return status, fmt.Errorf("legacy async submit returned nil")
	}
	if k.Type == kdb.KC || k.Type == -kdb.KS {
		status.ID = kdbString(k)
		status.Status = "queued"
		return status, nil
	}
	if id, ok := legacyExtractPath(k, a.jobIDPath); ok {
		status.ID = kdbString(id)
	}
	a.populateStatusFields(&status, k)
	if status.ID == "" {
		return status, fmt.Errorf("legacy async submit response did not contain job id at path %q", a.jobIDPath)
	}
	return status, nil
}

func (a legacyAsyncAdapter) parseStatusResponse(k *kdb.K, fallbackID string) (asyncQStatus, error) {
	status := asyncQStatus{ID: fallbackID}
	if k == nil {
		return status, fmt.Errorf("legacy async status returned nil")
	}
	if k.Type == kdb.KC || k.Type == -kdb.KS {
		status.Status = kdbString(k)
		return status, nil
	}
	if id, ok := legacyExtractPath(k, a.jobIDPath); ok {
		status.ID = kdbString(id)
	}
	a.populateStatusFields(&status, k)
	return status, nil
}

func (a legacyAsyncAdapter) populateStatusFields(status *asyncQStatus, k *kdb.K) {
	if v, ok := legacyExtractPath(k, a.statusPath); ok {
		status.Status = kdbString(v)
	}
	if v, ok := legacyExtractPath(k, a.progressPath); ok {
		status.Progress = kdbFloat(v)
	}
	if v, ok := legacyExtractPath(k, a.messagePath); ok {
		status.Message = kdbString(v)
	}
	if v, ok := legacyExtractPath(k, a.errorPath); ok {
		status.Error = kdbString(v)
	}
	if v, ok := legacyExtractPath(k, a.payloadPath); ok {
		status.Payload = v
	}
}

func (a legacyAsyncAdapter) extractResultPayload(k *kdb.K) (*kdb.K, error) {
	if k == nil {
		return nil, fmt.Errorf("legacy async result returned nil")
	}
	if a.payloadPath == "" {
		return k, nil
	}
	if payload, ok := legacyExtractPath(k, a.payloadPath); ok {
		return payload, nil
	}
	if k.Type == kdb.XD {
		return nil, fmt.Errorf("legacy async result response did not contain payload at path %q", a.payloadPath)
	}
	return k, nil
}

func legacyExtractPath(k *kdb.K, path string) (*kdb.K, bool) {
	path = strings.TrimSpace(path)
	if k == nil || path == "" {
		return k, k != nil
	}
	current := k
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		next, ok := legacyExtractField(current, part)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func legacyExtractField(k *kdb.K, name string) (*kdb.K, bool) {
	if k == nil {
		return nil, false
	}
	switch k.Type {
	case kdb.XD:
		d := k.Data.(kdb.Dict)
		names, err := dictColumnNames(d.Key)
		if err != nil {
			return nil, false
		}
		values, err := dictValues(d.Value, len(names))
		if err != nil {
			return nil, false
		}
		for i, key := range names {
			if strings.EqualFold(key, name) && i < len(values) {
				return values[i], true
			}
		}
	case kdb.XT:
		t := k.Data.(kdb.Table)
		for i, column := range t.Columns {
			if strings.EqualFold(column, name) && i < len(t.Data) && t.Data[i].Len() > 0 {
				cell, ok := correctedIndex(t.Data[i], 0).(*kdb.K)
				return cell, ok
			}
		}
	}
	return nil, false
}

func (d *KdbDatasource) runLegacyAsyncQueryStream(ctx context.Context, pCtx backend.PluginContext, liveReq liveQueryRequest, query backend.DataQuery, model QueryModel, requestID string, sender *backend.StreamSender) error {
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
	if d.DiagnosticsEnabled && d.DiagnosticsLogQueryText {
		fields = append(fields,
			"legacyAsyncSubmit", adapter.submit,
			"legacyAsyncStatus", adapter.status,
			"legacyAsyncResult", adapter.result,
			"legacyAsyncCancel", adapter.cancel,
		)
	}
	start := time.Now()
	if err := adapter.validate(); err != nil {
		d.logDiagnosticError("legacy async configuration invalid", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	if err := d.acquireAsyncSlot(ctx); err != nil {
		d.logDiagnosticError("legacy async slot unavailable", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	defer d.releaseAsyncSlot()

	conn, err := d.newConnection()
	if err != nil {
		d.logDiagnosticError("legacy async connection failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	defer conn.Close()

	submitArg, err := adapter.buildSubmitArg(pCtx, query, model, requestID)
	if err != nil {
		d.logDiagnosticError("legacy async request build failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	submitRes, err := callKdbFunction(conn, legacyAsyncCallExpression(adapter.submit, 1), submitArg)
	if err != nil {
		d.logDiagnosticError("legacy async submit failed", appendDiagnosticError(fields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	submitFields := appendDiagnosticKdbObject(append([]interface{}{}, fields...), "legacyAsyncSubmitResponse", submitRes)
	status, err := adapter.parseSubmitResponse(submitRes, requestID)
	if err != nil {
		d.logDiagnosticError("legacy async submit parse failed", appendDiagnosticError(submitFields, err)...)
		_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", requestID, "", err.Error(), 0, true)
		return nil
	}
	jobID := status.ID
	jobFields := append(submitFields, "jobID", jobID)
	state := adapter.normalizeStatus(status.Status, "queued")
	status.Status = state
	d.logDiagnostics("legacy async submitted", d.appendDiagnosticAsyncStatus(append([]interface{}{}, jobFields...), status)...)
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, state, jobID, status.Message, status.Error, status.Progress, false); err != nil {
		d.logDiagnosticError("legacy async status send failed", appendDiagnosticError(jobFields, err)...)
		return err
	}

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	lastState := state

	for {
		select {
		case <-ctx.Done():
			if adapter.cancel != "" {
				_, _ = callKdbFunction(conn, legacyAsyncCallExpression(adapter.cancel, 1), kdb.Atom(kdb.KC, jobID))
			}
			d.logDiagnostics("legacy async cancelled", append(jobFields, "durationMs", time.Since(start).Milliseconds(), "error", ctx.Err().Error())...)
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "cancelled", jobID, "", ctx.Err().Error(), 1, true)
			return nil
		case <-ticker.C:
			statusRes, err := callKdbFunction(conn, legacyAsyncCallExpression(adapter.status, 1), kdb.Atom(kdb.KC, jobID))
			if err != nil {
				d.logDiagnosticError("legacy async status failed", appendDiagnosticError(jobFields, err)...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), 0, true)
				return nil
			}
			statusFields := appendDiagnosticKdbObject(append([]interface{}{}, jobFields...), "legacyAsyncStatusResponse", statusRes)
			status, err = adapter.parseStatusResponse(statusRes, jobID)
			if err != nil {
				d.logDiagnosticError("legacy async status parse failed", appendDiagnosticError(statusFields, err)...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), 0, true)
				return nil
			}
			state = adapter.normalizeStatus(status.Status, "running")
			status.Status = state
			if state != lastState {
				d.logDiagnostics("legacy async status changed", d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status)...)
				lastState = state
			}
			switch state {
			case "error":
				err := fmt.Errorf("%s", status.Error)
				d.logDiagnosticError("legacy async returned error", appendDiagnosticError(d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status), err)...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			case "cancelled":
				d.logDiagnostics("legacy async cancelled by q", append(d.appendDiagnosticAsyncStatus(append([]interface{}{}, statusFields...), status), "durationMs", time.Since(start).Milliseconds())...)
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "cancelled", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			case "done":
				payload := status.Payload
				if payload == nil {
					if adapter.result == "" {
						err := fmt.Errorf("legacy async reached done state but no payload was present and no result function is configured")
						d.logDiagnosticError("legacy async result unavailable", appendDiagnosticError(statusFields, err)...)
						_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), status.Progress, true)
						return nil
					}
					resultRes, err := callKdbFunction(conn, legacyAsyncCallExpression(adapter.result, 1), kdb.Atom(kdb.KC, jobID))
					if err != nil {
						d.logDiagnosticError("legacy async result failed", appendDiagnosticError(statusFields, err)...)
						_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), status.Progress, true)
						return nil
					}
					statusFields = appendDiagnosticKdbObject(statusFields, "legacyAsyncResultResponse", resultRes)
					payload, err = adapter.extractResultPayload(resultRes)
					if err != nil {
						d.logDiagnosticError("legacy async result parse failed", appendDiagnosticError(statusFields, err)...)
						_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), status.Progress, true)
						return nil
					}
				}
				resultFields := appendDiagnosticKdbObject(append([]interface{}{}, statusFields...), "kdbResponse", payload)
				frames, err := parseKdbResponseToFrames(payload, model, liveReq.RefID)
				if err != nil {
					d.logDiagnosticError("legacy async result frame parse failed", appendDiagnosticError(resultFields, err)...)
					_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "error", jobID, "", err.Error(), status.Progress, true)
					return nil
				}
				resultFields = appendDiagnosticFrames(resultFields, frames)
				resultFields = d.appendDiagnosticAsyncStatus(resultFields, status)
				resultFields = append(resultFields, "durationMs", time.Since(start).Milliseconds())
				for _, frame := range frames {
					markFrame(frame, ExecutionModeLegacyAsync, "data", jobID, false, false)
					attachAsyncQDiagnostics([]*data.Frame{frame}, resultFields)
					if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
						d.logDiagnosticError("legacy async data send failed", appendDiagnosticError(resultFields, err)...)
						return err
					}
				}
				d.logDiagnostics("legacy async completed", resultFields...)
				return sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, "done", jobID, status.Message, "", 1, true)
			default:
				if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeLegacyAsync, state, jobID, status.Message, status.Error, status.Progress, false); err != nil {
					d.logDiagnosticError("legacy async status send failed", appendDiagnosticError(statusFields, err)...)
					return err
				}
			}
		}
	}
}
