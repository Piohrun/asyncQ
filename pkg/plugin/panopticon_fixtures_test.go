package plugin

import (
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func TestPanopticonCompatibilityReturnFixtures(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		response   *kdb.K
		wantFields []string
		wantRows   int
	}{
		{
			name: "function table",
			response: kdb.NewTable(
				[]string{"time", "sym", "price", "active"},
				[]*kdb.K{
					kdb.Atom(kdb.KP, []time.Time{now, now.Add(time.Second)}),
					kdb.SymbolV([]string{"AAPL", "MSFT"}),
					kdb.FloatV([]float64{189.5, 421.25}),
					&kdb.K{kdb.KB, kdb.NONE, []bool{true, false}},
				},
			),
			wantFields: []string{"time", "sym", "price", "active"},
			wantRows:   2,
		},
		{
			name: "keyed table duplicate columns",
			response: kdb.NewDict(
				kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"AAPL", "MSFT"})}),
				kdb.NewTable([]string{"sym", "price"}, []*kdb.K{kdb.SymbolV([]string{"XNYS", "XNAS"}), kdb.FloatV([]float64{189.5, 421.25})}),
			),
			wantFields: []string{"sym", "sym_2", "price"},
			wantRows:   2,
		},
		{
			name: "row dictionaries with time and missing values",
			response: kdb.NewList(
				kdb.NewDict(kdb.SymbolV([]string{"time", "sym", "price"}), kdb.NewList(kdb.Atom(-kdb.KP, now), kdb.Symbol("AAPL"), kdb.Float(189.5))),
				kdb.NewDict(kdb.SymbolV([]string{"time", "sym", "venue"}), kdb.NewList(kdb.Atom(-kdb.KP, now.Add(time.Second)), kdb.Symbol("MSFT"), kdb.Atom(kdb.KC, "XNAS"))),
			),
			wantFields: []string{"time", "sym", "price", "venue"},
			wantRows:   2,
		},
		{
			name:       "summary dictionary",
			response:   kdb.NewDict(kdb.SymbolV([]string{"rows", "state"}), kdb.NewList(kdb.Long(42), kdb.Atom(kdb.KC, "ready"))),
			wantFields: []string{"rows", "state"},
			wantRows:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames, err := parseKdbResponseToFrames(tt.response, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
			if err != nil {
				t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
			}
			frame := onlyFrame(t, frames)
			assertFieldNames(t, frame, tt.wantFields)
			if frame.Fields[0].Len() != tt.wantRows {
				t.Fatalf("unexpected row count: got %d want %d", frame.Fields[0].Len(), tt.wantRows)
			}
		})
	}
}

func TestPanopticonRequestFixtureContainsMigrationContext(t *testing.T) {
	from := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 24, 10, 5, 0, 0, time.UTC)
	query := backend.DataQuery{
		RefID:         "P",
		MaxDataPoints: 250,
		Interval:      30 * time.Second,
		TimeRange:     backend.TimeRange{From: from, To: to},
	}
	model := QueryModel{
		QueryText:                 ".compiled[]",
		OriginalQueryText:         ".original[{TimeWindowStart}]",
		CompatibilityMode:         CompatibilityModePanopticon,
		PanopticonQueryWrapper:    ".wrap[{Query}]",
		PanopticonRequestFunction: "{[req] .pano.run req}",
		StreamRetentionMs:         600000,
	}

	req := buildHelperRequest(backend.PluginContext{OrgID: 42}, query, model, "job-1", "stream-1")

	for _, key := range []string{
		"AQUAQ_KDB_BACKEND_GRAF_DATASOURCE",
		"ExecutionMode",
		"CompatibilityMode",
		"Panopticon",
		"TimeWindowStart",
		"TimeWindowEnd",
		"FocusTime",
		"IntervalMs",
		"MaxDataPoints",
		"RefID",
		"RequestID",
		"StreamID",
		"StreamRetentionMs",
	} {
		if _, ok := dictLookup(req, key); !ok {
			t.Fatalf("helper request missing %s", key)
		}
	}
	if got, ok := dictLookup(req, "StreamRetentionMs"); !ok || got.Data.(int64) != 600000 {
		t.Fatalf("unexpected StreamRetentionMs: %#v present=%v", got, ok)
	}
	pano, ok := dictLookup(req, "Panopticon")
	if !ok {
		t.Fatal("missing Panopticon dict")
	}
	if got, ok := dictLookup(pano, "OriginalQuery"); !ok || kdbString(got) != ".original[{TimeWindowStart}]" {
		t.Fatalf("unexpected Panopticon OriginalQuery: %#v present=%v", got, ok)
	}
}
