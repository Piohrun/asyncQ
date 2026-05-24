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
	ID       string
	Status   string
	Message  string
	Error    string
	Progress float64
	Payload  *kdb.K
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
	model.ExecutionMode = mode
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
		return err
	}
	conn, err := d.newConnection()
	if err != nil {
		return err
	}
	defer conn.Close()

	helperReq := buildHelperRequest(req.PluginContext, query, model, requestID, "")
	submitRes, err := callKdbFunction(conn, asyncSubmitFn, helperReq)
	if err != nil {
		return fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
	}
	status := parseAsyncQStatus(submitRes, requestID)
	jobID := status.ID
	if jobID == "" {
		jobID = requestID
	}
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, statusWithDefault(status.Status, "queued"), jobID, status.Message, status.Error, status.Progress, false); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Duration(model.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_, _ = callKdbFunction(conn, asyncCancelFn, kdb.Atom(kdb.KC, jobID))
			return ctx.Err()
		case <-ticker.C:
			statusRes, err := callKdbFunction(conn, asyncStatusFn, kdb.Atom(kdb.KC, jobID))
			if err != nil {
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), 0, true)
				return nil
			}
			status = parseAsyncQStatus(statusRes, jobID)
			state := strings.ToLower(statusWithDefault(status.Status, "running"))
			if state == "error" {
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			}
			if state == "cancelled" || state == "canceled" {
				_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "cancelled", jobID, status.Message, status.Error, status.Progress, true)
				return nil
			}
			if state == "done" || state == "complete" || state == "completed" {
				result, err := callKdbFunction(conn, asyncResultFn, kdb.Atom(kdb.KC, jobID))
				if err != nil {
					_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), status.Progress, true)
					return nil
				}
				frames, err := parseKdbResponseToFrames(result, model, liveReq.RefID)
				if err != nil {
					_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "error", jobID, "", err.Error(), status.Progress, true)
					return nil
				}
				for _, frame := range frames {
					markFrame(frame, ExecutionModeAsync, "data", jobID, false, false)
					if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
						return err
					}
				}
				return sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, "done", jobID, status.Message, "", 1, true)
			}
			if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeAsync, state, jobID, status.Message, status.Error, status.Progress, false); err != nil {
				return err
			}
		}
	}
}

func (d *KdbDatasource) runKdbPushStream(ctx context.Context, req *backend.RunStreamRequest, sender *backend.StreamSender) error {
	liveReq, query, model, streamID, err := decodeLiveQueryRequest(req.Data, ExecutionModeStream, req.Path)
	if err != nil {
		return err
	}
	conn, err := d.newConnection()
	if err != nil {
		return err
	}
	defer conn.Close()

	helperReq := buildHelperRequest(req.PluginContext, query, model, streamID, streamID)
	startRes, err := callKdbFunction(conn, streamStartFn, helperReq)
	if err != nil {
		return fmt.Errorf("%s: %w", asyncQHelperUnavailable, err)
	}
	status := parseAsyncQStatus(startRes, streamID)
	if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, statusWithDefault(status.Status, "running"), streamID, status.Message, status.Error, status.Progress, false); err != nil {
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
			return nil
		default:
		}

		msg, _, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.DefaultLogger.Info("stream read stopped", "streamID", streamID, "error", err)
			d.stopKdbStream(streamID)
			return nil
		}
		status := parseAsyncQStatus(msg, streamID)
		state := strings.ToLower(statusWithDefault(status.Status, "data"))
		if state == "error" {
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, status.Message, status.Error, status.Progress, true)
			return nil
		}
		if state == "done" || state == "complete" || state == "completed" {
			return sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "done", streamID, status.Message, "", status.Progress, true)
		}
		payload := status.Payload
		if payload == nil && msg != nil && (msg.Type == kdb.XT || msg.Type == kdb.XD) {
			payload = msg
		}
		if payload == nil {
			if err := sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, state, streamID, status.Message, status.Error, status.Progress, false); err != nil {
				return err
			}
			continue
		}
		frames, err := parseKdbResponseToFrames(payload, model, liveReq.RefID)
		if err != nil {
			_ = sendControlFrame(sender, liveReq.RefID, ExecutionModeStream, "error", streamID, "", err.Error(), 0, true)
			return nil
		}
		for _, frame := range frames {
			markFrame(frame, ExecutionModeStream, "data", streamID, false, false)
			if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
				return err
			}
		}
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

func buildHelperRequest(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, requestID string, streamID string) *kdb.K {
	baseKeys, baseValues := buildMasterKdbLists(pCtx, query, model)
	keys := append(append([]string{}, baseKeys.Data.([]string)...),
		"RequestID", "StreamID", "ExecutionMode", "PollIntervalMs", "MaxStreamRows")
	values := append(append([]*kdb.K{}, baseValues.Data.([]*kdb.K)...),
		kdb.Atom(kdb.KC, requestID),
		kdb.Atom(kdb.KC, streamID),
		kdb.Symbol(model.ExecutionMode),
		kdb.Long(int64(model.PollIntervalMs)),
		kdb.Long(int64(model.MaxStreamRows)))
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
		}
		return status
	}
	if v, ok := dictLookup(k, "JobID", "RequestID", "StreamID", "ID"); ok {
		status.ID = kdbString(v)
	}
	if v, ok := dictLookup(k, "Status", "State", "MessageType"); ok {
		status.Status = kdbString(v)
	}
	if v, ok := dictLookup(k, "Message"); ok {
		status.Message = kdbString(v)
	}
	if v, ok := dictLookup(k, "Error"); ok {
		status.Error = kdbString(v)
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
