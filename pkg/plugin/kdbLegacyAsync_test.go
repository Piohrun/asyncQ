package plugin

import (
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func TestLegacyAsyncNormalizeDefaults(t *testing.T) {
	model := QueryModel{}
	adapter := legacyAsyncAdapterFromModel(model)

	if adapter.requestMode != LegacyAsyncRequestModeRequestDict {
		t.Fatalf("unexpected request mode: %q", adapter.requestMode)
	}
	if adapter.jobIDPath != defaultLegacyAsyncJobIDPath || adapter.statusPath != defaultLegacyAsyncStatus || adapter.payloadPath != defaultLegacyAsyncPayload {
		t.Fatalf("unexpected default paths: %#v", adapter)
	}
	if !adapter.doneValues["done"] || !adapter.doneValues["complete"] || !adapter.cancelledValues["canceled"] {
		t.Fatalf("default status value sets were not populated: %#v", adapter)
	}
}

func TestKdbDatasourceNormalizeQueryModelCopiesLegacyAsyncDefaults(t *testing.T) {
	ds := KdbDatasource{
		ExecutionMode:               ExecutionModeLegacyAsync,
		LegacyAsyncSubmit:           ".gw.submit",
		LegacyAsyncStatus:           ".gw.status",
		LegacyAsyncResult:           ".gw.result",
		LegacyAsyncRequestMode:      LegacyAsyncRequestModeCompiledQueryText,
		LegacyAsyncJobIDPath:        "id",
		LegacyAsyncStatusPath:       "state",
		LegacyAsyncDoneValues:       "finished",
		queryCacheDefaultEnabled:    true,
		queryCacheDiskDefault:       true,
		queryCacheControlDefault:    true,
		queryCacheConfigured:        true,
		queryCacheDiskConfigured:    true,
		queryCacheControlConfigured: true,
	}
	model := QueryModel{}

	ds.normalizeQueryModel(&model)

	if model.ExecutionMode != ExecutionModeLegacyAsync {
		t.Fatalf("execution mode was not copied: %q", model.ExecutionMode)
	}
	if model.LegacyAsyncSubmit != ".gw.submit" || model.LegacyAsyncStatus != ".gw.status" || model.LegacyAsyncResult != ".gw.result" {
		t.Fatalf("legacy async functions were not copied: %#v", model)
	}
	if model.LegacyAsyncRequestMode != LegacyAsyncRequestModeCompiledQueryText {
		t.Fatalf("request mode was not copied: %q", model.LegacyAsyncRequestMode)
	}
	if model.LegacyAsyncJobIDPath != "id" || model.LegacyAsyncStatusPath != "state" || model.LegacyAsyncDoneValues != "finished" {
		t.Fatalf("legacy async paths/status values were not copied: %#v", model)
	}
}

func TestLegacyAsyncParseSubmitResponseFromDict(t *testing.T) {
	adapter := legacyAsyncAdapterFromModel(QueryModel{
		LegacyAsyncJobIDPath:    "id",
		LegacyAsyncStatusPath:   "state",
		LegacyAsyncProgressPath: "pct",
		LegacyAsyncMessagePath:  "note",
		LegacyAsyncErrorPath:    "err",
	})
	res := kdb.NewDict(
		kdb.SymbolV([]string{"id", "state", "pct", "note", "err"}),
		kdb.NewList(
			kdb.Atom(kdb.KC, "job-1"),
			kdb.Atom(kdb.KC, "pending"),
			kdb.Float(0.25),
			kdb.Atom(kdb.KC, "queued behind worker"),
			kdb.Atom(kdb.KC, ""),
		),
	)

	status, err := adapter.parseSubmitResponse(res, "fallback")
	if err != nil {
		t.Fatalf("parseSubmitResponse returned error: %v", err)
	}
	if status.ID != "job-1" || status.Status != "pending" || status.Progress != 0.25 || status.Message != "queued behind worker" || status.Error != "" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestLegacyAsyncParseStatusResponseFromOneRowTable(t *testing.T) {
	adapter := legacyAsyncAdapterFromModel(QueryModel{
		LegacyAsyncJobIDPath:    "id",
		LegacyAsyncStatusPath:   "state",
		LegacyAsyncProgressPath: "pct",
		LegacyAsyncDoneValues:   "finished",
	})
	res := kdb.NewTable([]string{"id", "state", "pct"}, []*kdb.K{
		kdb.SymbolV([]string{"job-2"}),
		kdb.SymbolV([]string{"finished"}),
		kdb.FloatV([]float64{1}),
	})

	status, err := adapter.parseStatusResponse(res, "fallback")
	if err != nil {
		t.Fatalf("parseStatusResponse returned error: %v", err)
	}
	if status.ID != "job-2" || status.Status != "finished" || status.Progress != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if got := adapter.normalizeStatus(status.Status, "running"); got != "done" {
		t.Fatalf("status was not normalized to done: %q", got)
	}
}

func TestLegacyAsyncExtractNestedPayload(t *testing.T) {
	payload := kdb.NewTable([]string{"sym", "price"}, []*kdb.K{
		kdb.SymbolV([]string{"AAPL"}),
		kdb.FloatV([]float64{189.5}),
	})
	res := kdb.NewDict(
		kdb.SymbolV([]string{"meta", "body"}),
		kdb.NewList(
			kdb.NewDict(kdb.Symbol("state"), kdb.Symbol("done")),
			kdb.NewDict(kdb.Symbol("payload"), payload),
		),
	)
	adapter := legacyAsyncAdapterFromModel(QueryModel{LegacyAsyncPayloadPath: "body.payload"})

	got, err := adapter.extractResultPayload(res)
	if err != nil {
		t.Fatalf("extractResultPayload returned error: %v", err)
	}
	if got != payload {
		t.Fatalf("unexpected payload: %#v", got)
	}
}

func TestLegacyAsyncExtractResultPayloadFallsBackToRawTable(t *testing.T) {
	payload := kdb.NewTable([]string{"sym", "price"}, []*kdb.K{
		kdb.SymbolV([]string{"AAPL"}),
		kdb.FloatV([]float64{189.5}),
	})
	adapter := legacyAsyncAdapterFromModel(QueryModel{})

	got, err := adapter.extractResultPayload(payload)
	if err != nil {
		t.Fatalf("extractResultPayload returned error: %v", err)
	}
	if got != payload {
		t.Fatalf("unexpected payload: %#v", got)
	}
}

func TestLegacyAsyncBuildSubmitArgModes(t *testing.T) {
	query := backend.DataQuery{
		RefID:         "A",
		MaxDataPoints: 500,
		Interval:      time.Second,
		TimeRange: backend.TimeRange{
			From: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
		},
	}
	model := QueryModel{
		QueryText:         ".compiled[]",
		OriginalQueryText: ".original[]",
		ExecutionMode:     ExecutionModeLegacyAsync,
		CompatibilityMode: CompatibilityModePanopticon,
	}

	for _, tc := range []struct {
		name        string
		requestMode string
		wantType    int8
		wantText    string
	}{
		{name: "query text", requestMode: LegacyAsyncRequestModeQueryText, wantType: kdb.KC, wantText: ".original[]"},
		{name: "compiled query text", requestMode: LegacyAsyncRequestModeCompiledQueryText, wantType: kdb.KC, wantText: ".compiled[]"},
		{name: "request dict", requestMode: LegacyAsyncRequestModeRequestDict, wantType: kdb.XD},
		{name: "panopticon dict", requestMode: LegacyAsyncRequestModePanopticonDict, wantType: kdb.XD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := legacyAsyncAdapterFromModel(QueryModel{LegacyAsyncRequestMode: tc.requestMode})
			arg, err := adapter.buildSubmitArg(backend.PluginContext{}, query, model, "req-1")
			if err != nil {
				t.Fatalf("buildSubmitArg returned error: %v", err)
			}
			if arg.Type != tc.wantType {
				t.Fatalf("unexpected arg type: %d", arg.Type)
			}
			if tc.wantText != "" && kdbString(arg) != tc.wantText {
				t.Fatalf("unexpected arg text: %q", kdbString(arg))
			}
		})
	}
}

func TestLegacyAsyncCallExpressionWrapsNamedUnaryFunction(t *testing.T) {
	if got := legacyAsyncCallExpression(".gw.submit", 1); got != "{[x] .gw.submit x}" {
		t.Fatalf("unexpected call expression: %q", got)
	}
	lambda := "{[x] .gw.submit x}"
	if got := legacyAsyncCallExpression(lambda, 1); got != lambda {
		t.Fatalf("lambda should be preserved: %q", got)
	}
}
