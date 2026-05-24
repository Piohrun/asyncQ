package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestPrepareQueryForExecutionExpandsPanopticonMacrosAndWrapper(t *testing.T) {
	from := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 24, 10, 5, 0, 0, time.UTC)
	query := backend.DataQuery{
		RefID:         "A",
		MaxDataPoints: 123,
		Interval:      5 * time.Second,
		TimeRange:     backend.TimeRange{From: from, To: to},
	}
	pCtx := backend.PluginContext{
		OrgID: 7,
		User:  &backend.User{Login: "greg"},
	}
	model := QueryModel{
		CompatibilityMode:      CompatibilityModePanopticon,
		QueryText:              "select from trade where time within ({TimeWindowStart};{TimeWindowEnd})",
		PanopticonQueryWrapper: ".pano.run[{Query};{IntervalMs};{MaxDataPoints};{RefID};{UserLogin};{OrgID}]",
	}

	if err := prepareQueryForExecution(pCtx, query, &model); err != nil {
		t.Fatalf("prepareQueryForExecution returned error: %v", err)
	}

	for _, want := range []string{
		".pano.run[select from trade where time within (2026.05.24D10:00:00.000000000;2026.05.24D10:05:00.000000000);5000j;123j;\"A\";\"greg\";7j]",
	} {
		if model.QueryText != want {
			t.Fatalf("unexpected compiled query:\n got: %s\nwant: %s", model.QueryText, want)
		}
	}
	if model.OriginalQueryText == "" || model.OriginalQueryText == model.QueryText {
		t.Fatalf("original query was not preserved: %q", model.OriginalQueryText)
	}
}

func TestExpandPanopticonMacrosSupportsDollarAndFormattedTimeParameters(t *testing.T) {
	from := time.Date(2026, 5, 24, 10, 0, 1, 123456789, time.UTC)
	to := time.Date(2026, 5, 24, 10, 5, 2, 987654321, time.UTC)
	query := backend.DataQuery{
		TimeRange: backend.TimeRange{From: from, To: to},
	}

	got := expandPanopticonMacros(
		"start:$TimeWindowStart end:{TimeWindowEnd:yyyy-MM-dd HH:mm:ss.SSS} focus:{FocusTime}",
		backend.PluginContext{},
		query,
	)

	for _, want := range []string{
		"start:2026.05.24D10:00:01.123456789",
		"end:\"2026-05-24 10:05:02.987\"",
		"focus:2026.05.24D10:05:02.987654321",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded macro %q missing %q", got, want)
		}
	}
}

func TestPrepareQueryForExecutionRequiresSingleWrapperQueryPlaceholder(t *testing.T) {
	model := QueryModel{
		CompatibilityMode:      CompatibilityModePanopticon,
		QueryText:              "1+1",
		PanopticonQueryWrapper: ".pano.run[{Query};{Query}]",
	}

	err := prepareQueryForExecution(backend.PluginContext{}, backend.DataQuery{}, &model)
	if err == nil || !strings.Contains(err.Error(), "exactly one {Query}") {
		t.Fatalf("expected wrapper placeholder error, got %v", err)
	}
}

func TestQueryExecutionFunctionUsesPanopticonRequestFunction(t *testing.T) {
	model := QueryModel{
		CompatibilityMode:         CompatibilityModePanopticon,
		PanopticonRequestFunction: "{[req] .pano.run req}",
	}

	got := queryExecutionFunction(model)
	if got != "{[req] .pano.run req}" {
		t.Fatalf("unexpected execution function: %q", got)
	}
}

func TestBuildQueryKdbDictIncludesPanopticonMetadata(t *testing.T) {
	query := backend.DataQuery{RefID: "A"}
	model := QueryModel{
		QueryText:                 ".compiled[]",
		OriginalQueryText:         ".original[]",
		PanopticonQueryWrapper:    ".wrap[{Query}]",
		PanopticonRequestFunction: "{[req] .pano.run req}",
	}

	req := buildQueryKdbDict(query, model)

	if got, ok := dictLookup(req, "OriginalQuery"); !ok || kdbString(got) != ".original[]" {
		t.Fatalf("unexpected OriginalQuery: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(req, "CompiledQuery"); !ok || kdbString(got) != ".compiled[]" {
		t.Fatalf("unexpected CompiledQuery: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(req, "PanopticonRequestFunction"); !ok || kdbString(got) != "{[req] .pano.run req}" {
		t.Fatalf("unexpected PanopticonRequestFunction: %#v present=%v", got, ok)
	}
}

func TestBuildPanopticonKdbDictIncludesContextAliases(t *testing.T) {
	from := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 24, 10, 5, 0, 0, time.UTC)
	query := backend.DataQuery{
		RefID:         "A",
		MaxDataPoints: 123,
		Interval:      5 * time.Second,
		TimeRange:     backend.TimeRange{From: from, To: to},
	}
	model := QueryModel{QueryText: ".compiled[]", OriginalQueryText: ".original[]"}

	pano := buildPanopticonKdbDict(query, model)

	if got, ok := dictLookup(pano, "RefID"); !ok || kdbString(got) != "A" {
		t.Fatalf("unexpected RefID: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(pano, "Query"); !ok || kdbString(got) != ".compiled[]" {
		t.Fatalf("unexpected Query: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(pano, "OriginalQuery"); !ok || kdbString(got) != ".original[]" {
		t.Fatalf("unexpected OriginalQuery: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(pano, "FocusTime"); !ok || got.Data.(time.Time) != to {
		t.Fatalf("unexpected FocusTime: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(pano, "TimeWindowStartText"); !ok || kdbString(got) != "2026-05-24T10:00:00Z" {
		t.Fatalf("unexpected TimeWindowStartText: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(pano, "IntervalNs"); !ok || got.Data.(int64) != int64(5*time.Second) {
		t.Fatalf("unexpected IntervalNs: %#v present=%v", got, ok)
	}
}

func TestBuildDirectQueryRequestIncludesTopLevelPanopticonAliases(t *testing.T) {
	from := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 24, 10, 5, 0, 0, time.UTC)
	query := backend.DataQuery{
		RefID:         "A",
		MaxDataPoints: 123,
		Interval:      5 * time.Second,
		TimeRange:     backend.TimeRange{From: from, To: to},
	}

	req := buildDirectQueryRequest(backend.PluginContext{}, query, QueryModel{CompatibilityMode: CompatibilityModePanopticon})

	if got, ok := dictLookup(req, "TimeWindowStart"); !ok || got.Data.(time.Time) != from {
		t.Fatalf("unexpected top-level TimeWindowStart: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(req, "FocusTime"); !ok || got.Data.(time.Time) != to {
		t.Fatalf("unexpected top-level FocusTime: %#v present=%v", got, ok)
	}
	if got, ok := dictLookup(req, "IntervalMs"); !ok || got.Data.(int64) != 5000 {
		t.Fatalf("unexpected top-level IntervalMs: %#v present=%v", got, ok)
	}
}
