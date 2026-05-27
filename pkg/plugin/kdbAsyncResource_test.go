package plugin

import (
	"encoding/json"
	"testing"
	"time"

	kdb "github.com/sv/kdbgo"
)

func TestParseAsyncResourceTime(t *testing.T) {
	now := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	fallback := now.Add(-time.Hour)

	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "fallback", raw: "", want: fallback},
		{name: "now", raw: "now", want: now},
		{name: "relative minutes", raw: "now-5m", want: now.Add(-5 * time.Minute)},
		{name: "relative days", raw: "now-2d", want: now.Add(-48 * time.Hour)},
		{name: "unix ms", raw: "1779876000000", want: time.UnixMilli(1779876000000).UTC()},
		{name: "rfc3339", raw: "2026-05-27T09:30:00Z", want: time.Date(2026, 5, 27, 9, 30, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAsyncResourceTime(tc.raw, fallback, now)
			if err != nil {
				t.Fatalf("parseAsyncResourceTime returned error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("unexpected time: got %s want %s", got, tc.want)
			}
		})
	}
}

func TestDecodeAsyncRunAndWaitRequestDefaultsAndNormalizes(t *testing.T) {
	raw := []byte(`{
		"queryText": ".demo.asyncq.slowAgg[]",
		"executionMode": "pluginAsync",
		"compatibilityMode": "panopticon",
		"pollIntervalMs": 250,
		"timeRange": {
			"from": "2026-05-27T09:00:00Z",
			"to": "2026-05-27T10:00:00Z"
		}
	}`)
	ds := &KdbDatasource{}

	liveReq, query, model, requestID, err := ds.decodeAsyncRunAndWaitRequest(raw)
	if err != nil {
		t.Fatalf("decodeAsyncRunAndWaitRequest returned error: %v", err)
	}
	if liveReq.RefID != "A" || query.RefID != "A" {
		t.Fatalf("refId was not defaulted: live=%q query=%q", liveReq.RefID, query.RefID)
	}
	if requestID == "" {
		t.Fatal("requestID was not generated")
	}
	if model.ExecutionMode != ExecutionModePluginAsync || model.CompatibilityMode != CompatibilityModePanopticon {
		t.Fatalf("unexpected model modes: %#v", model)
	}
	if model.Timeout != defaultQueryTimeout || model.PollIntervalMs != 250 {
		t.Fatalf("unexpected model defaults: %#v", model)
	}
	if query.MaxDataPoints != 500 || query.Interval != time.Second {
		t.Fatalf("query defaults were not applied: %#v", query)
	}
	if query.TimeRange.From.Format(time.RFC3339) != "2026-05-27T09:00:00Z" || query.TimeRange.To.Format(time.RFC3339) != "2026-05-27T10:00:00Z" {
		t.Fatalf("unexpected query range: %#v", query.TimeRange)
	}
}

func TestAsyncRunAndWaitResponseFramesMarshalAsGrafanaFrames(t *testing.T) {
	resp := asyncRunAndWaitResponse{
		OK:     true,
		RefID:  "A",
		Status: "done",
	}
	frames, err := parseKdbResponseToFrames(
		kdbTestTable(),
		QueryModel{ExecutionMode: ExecutionModePluginAsync, CompatibilityMode: CompatibilityModeNative},
		"A",
	)
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	resp.Frames = frames

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode marshaled response: %v", err)
	}
	frame := decoded["frames"].([]interface{})[0].(map[string]interface{})
	if _, ok := frame["schema"]; !ok {
		t.Fatalf("frame JSON did not include schema: %s", raw)
	}
	if _, ok := frame["data"]; !ok {
		t.Fatalf("frame JSON did not include data: %s", raw)
	}
}

func kdbTestTable() *kdb.K {
	return kdb.NewTable([]string{"sym", "price"}, []*kdb.K{
		kdb.SymbolV([]string{"AAPL"}),
		kdb.FloatV([]float64{189.5}),
	})
}
