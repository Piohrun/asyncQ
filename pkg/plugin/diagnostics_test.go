package plugin

import (
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestDiagnosticHashDoesNotExposeQueryText(t *testing.T) {
	queryText := "select from trade where sym=`SECRET"
	hash := diagnosticHash(queryText)
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if strings.Contains(hash, "SECRET") || strings.Contains(hash, "select") {
		t.Fatalf("diagnostic hash leaked query text: %q", hash)
	}
	if len(hash) != diagnosticHashLength {
		t.Fatalf("expected %d-character hash, got %d", diagnosticHashLength, len(hash))
	}
}

func TestDiagnosticFieldsOnlyIncludeQueryTextWhenOptedIn(t *testing.T) {
	query := backend.DataQuery{RefID: "A"}
	model := QueryModel{
		QueryText:              "select from trade",
		OriginalQueryText:      ".pano.run[]",
		ExecutionMode:          ExecutionModeSync,
		CompatibilityMode:      CompatibilityModePanopticon,
		PanopticonQueryWrapper: "{Query}",
	}

	fields := (&KdbDatasource{}).diagnosticQueryFields(backend.PluginContext{}, query, model, "request-1")
	if diagnosticFieldsContainKey(fields, "queryText") {
		t.Fatal("queryText was present without explicit opt-in")
	}
	if !diagnosticFieldsContainKey(fields, "queryHash") {
		t.Fatal("queryHash was not present")
	}

	fields = (&KdbDatasource{DiagnosticsEnabled: true, DiagnosticsLogQueryText: true}).diagnosticQueryFields(backend.PluginContext{}, query, model, "request-1")
	if !diagnosticFieldsContainKey(fields, "queryText") {
		t.Fatal("queryText was not present with explicit opt-in")
	}
}

func TestDiagnosticIDPartSanitizesAndBounds(t *testing.T) {
	raw := strings.Repeat("abc#", 80)
	got := diagnosticIDPart(raw)
	if strings.Contains(got, "#") {
		t.Fatalf("expected # to be sanitized: %q", got)
	}
	if len(got) > 128 {
		t.Fatalf("expected ID to be bounded, got %d characters", len(got))
	}
}

func TestDiagnosticAsyncStatusHashesStackTraceByDefault(t *testing.T) {
	status := asyncQStatus{
		Status:     "error",
		Error:      "boom",
		ErrorClass: "q",
		StackTrace: "sensitive q stack trace",
		Worker:     "worker-1",
	}

	fields := (&KdbDatasource{DiagnosticsEnabled: true}).appendDiagnosticAsyncStatus(nil, status)
	if diagnosticFieldsContainKey(fields, "qStackTrace") {
		t.Fatal("stack trace was present without query-text diagnostics")
	}
	if !diagnosticFieldsContainKey(fields, "qStackTraceHash") {
		t.Fatal("stack trace hash was not present")
	}

	fields = (&KdbDatasource{DiagnosticsEnabled: true, DiagnosticsLogQueryText: true}).appendDiagnosticAsyncStatus(nil, status)
	if !diagnosticFieldsContainKey(fields, "qStackTrace") {
		t.Fatal("stack trace was not present with explicit verbose diagnostics")
	}
}

func diagnosticFieldsContainKey(fields []interface{}, key string) bool {
	for i := 0; i < len(fields)-1; i += 2 {
		if fields[i] == key {
			return true
		}
	}
	return false
}
