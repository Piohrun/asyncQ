package plugin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

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
