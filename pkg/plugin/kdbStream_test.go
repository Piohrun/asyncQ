package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
)

func TestAsyncContextErrorPreservesCancellationCause(t *testing.T) {
	cause := errors.New("live request superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	err := asyncContextError(ctx, "helper async", time.Second)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("async cancellation cause was not preserved: %v", err)
	}
}

func TestClassifyAsyncContextAlwaysPrefersParentCancellation(t *testing.T) {
	cause := errors.New("dashboard request superseded")
	parentCtx, cancelParent := context.WithCancelCause(context.Background())
	jobCtx, cancelJob := context.WithTimeout(parentCtx, time.Hour)
	defer cancelJob()
	cancelParent(cause)
	<-jobCtx.Done()

	for iteration := 0; iteration < 2000; iteration++ {
		// Both channels are ready. The select deliberately exercises Go's random
		// ready-case choice before the shared classifier resolves the cause.
		select {
		case <-parentCtx.Done():
		case <-jobCtx.Done():
		}
		outcome := classifyAsyncContext(parentCtx, jobCtx, "plugin async", time.Hour)
		if !outcome.cancelled {
			t.Fatalf("iteration %d misclassified parent cancellation as timeout: %v", iteration, outcome.err)
		}
		if !errors.Is(outcome.err, context.Canceled) || !errors.Is(outcome.err, cause) {
			t.Fatalf("iteration %d did not preserve parent cause: %v", iteration, outcome.err)
		}
	}
}

func TestClassifyAsyncContextDistinguishesJobDeadlineAndCancellation(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		jobCtx, cancelJob := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancelJob()
		<-jobCtx.Done()
		outcome := classifyAsyncContext(context.Background(), jobCtx, "helper async", time.Nanosecond)
		if outcome.cancelled || !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("job deadline misclassified: cancelled=%v error=%v", outcome.cancelled, outcome.err)
		}
	})

	t.Run("custom cancellation cause", func(t *testing.T) {
		cause := errors.New("worker revoked")
		jobCtx, cancelJob := context.WithCancelCause(context.Background())
		cancelJob(cause)
		outcome := classifyAsyncContext(context.Background(), jobCtx, "helper async", time.Second)
		if !outcome.cancelled || !errors.Is(outcome.err, context.Canceled) || !errors.Is(outcome.err, cause) {
			t.Fatalf("job cancellation cause misclassified: cancelled=%v error=%v", outcome.cancelled, outcome.err)
		}
	})
}

func TestReadyAsyncContextDetectsBothReadyBeforeResultOrErrorHandling(t *testing.T) {
	cause := errors.New("request replaced")
	parentCtx, cancelParent := context.WithCancelCause(context.Background())
	jobCtx, cancelJob := context.WithTimeout(parentCtx, time.Hour)
	defer cancelJob()
	cancelParent(cause)
	<-jobCtx.Done()

	for iteration := 0; iteration < 2000; iteration++ {
		outcome, ready := readyAsyncContext(parentCtx, jobCtx, "plugin async", time.Hour)
		if !ready || !outcome.cancelled {
			t.Fatalf("iteration %d did not detect ready cancellation: ready=%v outcome=%#v", iteration, ready, outcome)
		}
		if !errors.Is(outcome.err, context.Canceled) || !errors.Is(outcome.err, cause) {
			t.Fatalf("iteration %d lost cancellation cause: %v", iteration, outcome.err)
		}
	}

	if _, ready := readyAsyncContext(context.Background(), context.Background(), "plugin async", time.Hour); ready {
		t.Fatal("live contexts were reported ready")
	}
}

type laggingAsyncDeadlineContext struct {
	deadline time.Time
}

func (c laggingAsyncDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (laggingAsyncDeadlineContext) Done() <-chan struct{}         { return nil }
func (laggingAsyncDeadlineContext) Err() error                    { return nil }
func (laggingAsyncDeadlineContext) Value(interface{}) interface{} { return nil }

func TestReadyAsyncContextClassifiesNormalizedOperationContextErrors(t *testing.T) {
	lagging := laggingAsyncDeadlineContext{deadline: time.Now().Add(-time.Millisecond)}
	timeoutErr := fmt.Errorf("transport deadline: %w", context.DeadlineExceeded)
	outcome, ready := readyAsyncContext(context.Background(), lagging, "helper async", time.Second, timeoutErr)
	if !ready || outcome.cancelled || !errors.Is(outcome.err, context.DeadlineExceeded) {
		t.Fatalf("normalized deadline was not classified as timeout: ready=%v outcome=%#v", ready, outcome)
	}

	cause := errors.New("worker revoked")
	cancelErr := errors.Join(context.Canceled, cause)
	outcome, ready = readyAsyncContext(context.Background(), context.Background(), "plugin async", time.Second, cancelErr)
	if !ready || !outcome.cancelled || !errors.Is(outcome.err, context.Canceled) || !errors.Is(outcome.err, cause) {
		t.Fatalf("normalized cancellation was not classified with its cause: ready=%v outcome=%#v", ready, outcome)
	}
}

func TestStreamPushRequiresAsyncMessageType(t *testing.T) {
	if err := validateStreamPushMessageType(kdb.ASYNC); err != nil {
		t.Fatalf("ASYNC push rejected: %v", err)
	}
	for _, messageType := range []kdb.ReqType{kdb.SYNC, kdb.RESPONSE} {
		err := validateStreamPushMessageType(messageType)
		if !errors.Is(err, kdb.ErrBadMsg) {
			t.Fatalf("message type %d error = %v, want ErrBadMsg", messageType, err)
		}
	}
}

func TestOnceAsyncStreamStopRunsExactlyOnceConcurrently(t *testing.T) {
	var calls atomic.Int32
	stop := onceAsyncStreamStop(func() {
		calls.Add(1)
	})
	var wait sync.WaitGroup
	for iteration := 0; iteration < 100; iteration++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			stop()
		}()
	}
	wait.Wait()
	stop()
	if got := calls.Load(); got != 1 {
		t.Fatalf("stream stop calls = %d, want 1", got)
	}
}

func TestDecodeLiveQueryRequestNormalizesModel(t *testing.T) {
	raw := json.RawMessage(`{
		"refId": "B",
		"queryText": "select from trade",
		"pollIntervalMs": 250,
		"maxStreamRows": 42,
		"streamRetentionMs": 60000,
		"maxDataPoints": 500,
		"intervalMs": 1000,
		"timeRange": {
			"from": "2026-05-24T10:00:00Z",
			"to": "2026-05-24T11:00:00Z"
		}
	}`)

	liveReq, query, model, id, err := decodeLiveQueryRequest(raw, ExecutionModeAsync, "async/job-123")
	if err != nil {
		t.Fatalf("decodeLiveQueryRequest returned error: %v", err)
	}

	if liveReq.RefID != "B" || query.RefID != "B" {
		t.Fatalf("unexpected refID: live=%q query=%q", liveReq.RefID, query.RefID)
	}
	if id != "job-123" {
		t.Fatalf("unexpected path id: %q", id)
	}
	if model.ExecutionMode != ExecutionModeAsync {
		t.Fatalf("unexpected execution mode: %q", model.ExecutionMode)
	}
	if model.Timeout != defaultQueryTimeout {
		t.Fatalf("timeout was not defaulted: %d", model.Timeout)
	}
	if model.PollIntervalMs != 250 {
		t.Fatalf("poll interval not preserved: %d", model.PollIntervalMs)
	}
	if model.MaxStreamRows != 42 {
		t.Fatalf("max rows not preserved: %d", model.MaxStreamRows)
	}
	if model.StreamRetentionMs != 60000 {
		t.Fatalf("stream retention not preserved: %d", model.StreamRetentionMs)
	}
	if query.Interval != time.Second {
		t.Fatalf("unexpected query interval: %v", query.Interval)
	}
	if query.TimeRange.From.IsZero() || query.TimeRange.To.IsZero() {
		t.Fatal("time range was not parsed")
	}
}

func TestDecodeLiveQueryRequestPreservesAsyncStrategy(t *testing.T) {
	raw := json.RawMessage(`{
		"refId": "A",
		"queryText": "select from trade",
		"executionMode": "pluginAsync"
	}`)

	_, _, model, _, err := decodeLiveQueryRequest(raw, ExecutionModeAsync, "async/job-1")
	if err != nil {
		t.Fatalf("decodeLiveQueryRequest returned error: %v", err)
	}
	if model.ExecutionMode != ExecutionModePluginAsync {
		t.Fatalf("execution mode was not preserved: %q", model.ExecutionMode)
	}
}

func TestNormalizeAsyncQueryModelUsesDatasourceDefault(t *testing.T) {
	liveReq := liveQueryRequest{QueryModel: QueryModel{}}
	model := QueryModel{ExecutionMode: ExecutionModeAsync}
	d := KdbDatasource{
		ExecutionMode:     ExecutionModePluginAsync,
		CompatibilityMode: CompatibilityModePanopticon,
	}

	d.normalizeAsyncQueryModel(liveReq, &model)

	if model.ExecutionMode != ExecutionModePluginAsync {
		t.Fatalf("execution mode did not use datasource default: %q", model.ExecutionMode)
	}
	if model.CompatibilityMode != CompatibilityModePanopticon {
		t.Fatalf("compatibility mode did not use datasource default: %q", model.CompatibilityMode)
	}
}

func TestNormalizeAsyncQueryModelFallsBackToHelperAsyncForSyncDefault(t *testing.T) {
	liveReq := liveQueryRequest{QueryModel: QueryModel{}}
	model := QueryModel{ExecutionMode: ExecutionModeAsync}
	d := KdbDatasource{ExecutionMode: ExecutionModeSync}

	d.normalizeAsyncQueryModel(liveReq, &model)

	if model.ExecutionMode != ExecutionModeAsync {
		t.Fatalf("execution mode should fall back to helper async on async path: %q", model.ExecutionMode)
	}
}

func TestApplyDeferredQueryWrapper(t *testing.T) {
	got, err := applyDeferredQueryWrapper(".slow.query[]", ".gw.defer[{Query}]")
	if err != nil {
		t.Fatalf("applyDeferredQueryWrapper returned error: %v", err)
	}
	if got != ".gw.defer[.slow.query[]]" {
		t.Fatalf("unexpected wrapped query: %q", got)
	}
}

func TestApplyDeferredQueryWrapperRequiresOnePlaceholder(t *testing.T) {
	for _, wrapper := range []string{"", ".gw.defer[]", "{Query};{Query}"} {
		if _, err := applyDeferredQueryWrapper("1+1", wrapper); err == nil {
			t.Fatalf("expected wrapper %q to fail", wrapper)
		}
	}
}

func TestBuildHelperRequestUsesUniqueKeys(t *testing.T) {
	req := buildHelperRequest(
		backend.PluginContext{},
		backend.DataQuery{},
		QueryModel{ExecutionMode: ExecutionModeAsync, CompatibilityMode: CompatibilityModeNative},
		"job-1",
		"stream-1",
	)
	dict := req.Data.(kdb.Dict)
	seen := map[string]bool{}
	for _, key := range dict.Key.Data.([]string) {
		if seen[key] {
			t.Fatalf("duplicate helper request key: %s", key)
		}
		seen[key] = true
	}
	for _, key := range []string{"ExecutionMode", "CompatibilityMode", "Panopticon", "RequestID", "StreamID", "PollIntervalMs", "MaxStreamRows", "StreamRetentionMs"} {
		if !seen[key] {
			t.Fatalf("missing helper request key: %s", key)
		}
	}
}

func TestParseAsyncQStatusFromHelperDict(t *testing.T) {
	statusK := kdb.NewDict(
		kdb.SymbolV([]string{"JobID", "Status", "Progress", "Error", "Message", "ErrorClass", "StackTrace", "Worker", "ResultType"}),
		kdb.NewList(
			kdb.Atom(kdb.KC, "job-7"),
			kdb.Atom(kdb.KC, "running"),
			kdb.Float(0.75),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, "still running"),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, "worker-1"),
			kdb.Atom(kdb.KC, "type=98;count=2"),
		),
	)

	status := parseAsyncQStatus(statusK, "fallback")
	if status.ID != "job-7" {
		t.Fatalf("unexpected id: %q", status.ID)
	}
	if status.Status != "running" {
		t.Fatalf("unexpected status: %q", status.Status)
	}
	if status.Progress != 0.75 {
		t.Fatalf("unexpected progress: %f", status.Progress)
	}
	if status.Error != "" {
		t.Fatalf("unexpected error: %q", status.Error)
	}
	if status.Message != "still running" || status.Worker != "worker-1" || status.ResultType != "type=98;count=2" {
		t.Fatalf("unexpected diagnostic fields: %#v", status)
	}
}

func TestParseAsyncQStatusUsesFallbackForNil(t *testing.T) {
	status := parseAsyncQStatus(nil, "fallback")
	if status.ID != "fallback" {
		t.Fatalf("unexpected fallback id: %q", status.ID)
	}
}
