package plugin

import (
	"encoding/json"
	"testing"
	"time"

	kdb "github.com/sv/kdbgo"
)

func TestDecodeLiveQueryRequestNormalizesModel(t *testing.T) {
	raw := json.RawMessage(`{
		"refId": "B",
		"queryText": "select from trade",
		"pollIntervalMs": 250,
		"maxStreamRows": 42,
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
	if query.Interval != time.Second {
		t.Fatalf("unexpected query interval: %v", query.Interval)
	}
	if query.TimeRange.From.IsZero() || query.TimeRange.To.IsZero() {
		t.Fatal("time range was not parsed")
	}
}

func TestParseAsyncQStatusFromHelperDict(t *testing.T) {
	statusK := kdb.NewDict(
		kdb.SymbolV([]string{"JobID", "Status", "Progress", "Error"}),
		kdb.NewList(
			kdb.Atom(kdb.KC, "job-7"),
			kdb.Atom(kdb.KC, "running"),
			kdb.Float(0.75),
			kdb.Atom(kdb.KC, ""),
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
}

func TestParseAsyncQStatusUsesFallbackForNil(t *testing.T) {
	status := parseAsyncQStatus(nil, "fallback")
	if status.ID != "fallback" {
		t.Fatalf("unexpected fallback id: %q", status.ID)
	}
}
