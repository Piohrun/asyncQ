package plugin

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"strconv"
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

func TestLegacyAsyncDemoFullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live q integration in short mode")
	}
	qPath, err := exec.LookPath("q")
	if err != nil {
		t.Skipf("skipping live q integration because q is not on PATH: %v", err)
	}

	port := freeTestPort(t)
	var output bytes.Buffer
	cmd := exec.Command(qPath, "demo/q/asyncq_demo.q", "-p", strconv.Itoa(port), "-q")
	cmd.Dir = "../.."
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start demo q process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	conn := waitForDemoKdb(t, port, &output)
	defer conn.Close()

	query := backend.DataQuery{
		RefID:         "A",
		MaxDataPoints: 1000,
		Interval:      time.Second,
		TimeRange: backend.TimeRange{
			From: time.Now().Add(-5 * time.Minute),
			To:   time.Now(),
		},
	}
	model := QueryModel{
		QueryText:                  ".demo.asyncq.slowAgg[]",
		ExecutionMode:              ExecutionModeLegacyAsync,
		CompatibilityMode:          CompatibilityModeNative,
		PollIntervalMs:             100,
		Timeout:                    10000,
		LegacyAsyncSubmit:          ".demo.legacy.submit",
		LegacyAsyncStatus:          ".demo.legacy.status",
		LegacyAsyncResult:          ".demo.legacy.result",
		LegacyAsyncCancel:          ".demo.legacy.cancel",
		LegacyAsyncRequestMode:     LegacyAsyncRequestModeRequestDict,
		LegacyAsyncJobIDPath:       "id",
		LegacyAsyncStatusPath:      "state",
		LegacyAsyncProgressPath:    "pct",
		LegacyAsyncMessagePath:     "note",
		LegacyAsyncErrorPath:       "err",
		LegacyAsyncPayloadPath:     "payload",
		LegacyAsyncQueuedValues:    "queued",
		LegacyAsyncRunningValues:   "running",
		LegacyAsyncDoneValues:      "done",
		LegacyAsyncErrorValues:     "error",
		LegacyAsyncCancelledValues: "cancelled",
	}
	adapter := legacyAsyncAdapterFromModel(model)
	req := buildHelperRequest(backend.PluginContext{}, query, model, "legacy-flow-test", "")

	callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	submitRes, err := callKdbFunctionWithContext(callCtx, conn, legacyAsyncCallExpression(adapter.submit, 1), req)
	if err != nil {
		t.Fatalf("legacy submit failed: %v\nq output:\n%s", err, output.String())
	}
	status, err := adapter.parseSubmitResponse(submitRes, "legacy-flow-test")
	if err != nil {
		t.Fatalf("legacy submit parse failed: %v", err)
	}
	if status.ID == "" || status.RawStatus == "" {
		t.Fatalf("submit did not preserve job id/raw status: %#v", status)
	}

	payload := pollDemoLegacyResult(t, conn, adapter, status.ID)
	frames, err := parseKdbResponseToFrames(payload, model, query.RefID)
	if err != nil {
		t.Fatalf("legacy payload frame parse failed: %v", err)
	}
	hasRows := false
	for _, frame := range frames {
		hasRows = hasRows || (len(frame.Fields) > 0 && frame.Fields[0].Len() > 0)
	}
	if len(frames) == 0 || !hasRows {
		t.Fatalf("unexpected parsed frames: %#v", frames)
	}
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve test port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForDemoKdb(t *testing.T, port int, output *bytes.Buffer) *kdb.KDBConn {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := kdb.DialKDBTimeout("127.0.0.1", port, ":", 500*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("demo q process did not accept IPC connections on port %d\nq output:\n%s", port, output.String())
	return nil
}

func pollDemoLegacyResult(t *testing.T, conn *kdb.KDBConn, adapter legacyAsyncAdapter, jobID string) *kdb.K {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		statusRes, err := callKdbFunctionWithContext(callCtx, conn, legacyAsyncCallExpression(adapter.status, 1), kdb.Atom(kdb.KC, jobID))
		cancel()
		if err != nil {
			t.Fatalf("legacy status failed: %v", err)
		}
		status, err := adapter.parseStatusResponse(statusRes, jobID)
		if err != nil {
			t.Fatalf("legacy status parse failed: %v", err)
		}
		if status.RawStatus == "" {
			t.Fatalf("legacy status did not preserve raw status: %#v", status)
		}
		state, _ := adapter.normalizeStatusDetail(status.Status, "running")
		switch state {
		case "done":
			callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			resultRes, err := callKdbFunctionWithContext(callCtx, conn, legacyAsyncCallExpression(adapter.result, 1), kdb.Atom(kdb.KC, jobID))
			cancel()
			if err != nil {
				t.Fatalf("legacy result failed: %v", err)
			}
			payload, err := adapter.extractResultPayload(resultRes)
			if err != nil {
				t.Fatalf("legacy result payload extract failed: %v", err)
			}
			return payload
		case "error":
			t.Fatalf("legacy job returned error status: %#v", status)
		case "cancelled":
			t.Fatalf("legacy job was cancelled: %#v", status)
		}
	}
	t.Fatalf("legacy job %s did not complete before deadline", jobID)
	return nil
}
