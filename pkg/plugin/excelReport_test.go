package plugin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/xuri/excelize/v2"
)

func TestExcelReportCatalogParsesConfiguredObject(t *testing.T) {
	ds := &KdbDatasource{ExcelReports: `{"reports":[{"id":"r1","name":"Report","bindings":[{"queryText":"([] a:1 2)","sheet":"Data","cell":"B2"}]}]}`}
	catalog, err := ds.excelReportCatalog()
	if err != nil {
		t.Fatalf("excelReportCatalog returned error: %v", err)
	}
	if len(catalog.Reports) != 1 || catalog.Reports[0].ID != "r1" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
}

func TestExcelReportCatalogRejectsInvalidBinding(t *testing.T) {
	ds := &KdbDatasource{ExcelReports: `{"reports":[{"id":"r1","bindings":[{"queryText":"1+1","sheet":"Data","cell":"not-a-cell"}]}]}`}
	if _, err := ds.excelReportCatalog(); err == nil {
		t.Fatalf("expected invalid cell error")
	}
}

func TestExpandExcelReportParametersKeepsPanopticonMacros(t *testing.T) {
	request := excelReportGenerateRequest{
		Variables: map[string]interface{}{
			"symbols": []interface{}{"AAPL", "MSFT"},
			"book":    "EQ",
		},
	}
	got := expandExcelReportParameters("select from t where time>{TimeWindowStart}, book=\"{book}\", sym in `$\" \" vs \"{symbols: }\"", request, time.Time{}, time.Time{})
	want := "select from t where time>{TimeWindowStart}, book=\"EQ\", sym in `$\" \" vs \"AAPL MSFT\""
	if got != want {
		t.Fatalf("unexpected expansion:\n got: %s\nwant: %s", got, want)
	}
}

func TestDecodeExcelReportGenerateRequestAcceptsFormPayload(t *testing.T) {
	raw := []byte(`payload=%7B%22reportId%22%3A%22demo%22%2C%22fileName%22%3A%22typed%22%7D`)
	var request excelReportGenerateRequest

	if err := decodeExcelReportGenerateRequest(raw, &request); err != nil {
		t.Fatalf("decodeExcelReportGenerateRequest returned error: %v", err)
	}
	if request.ReportID != "demo" || request.FileName != "typed" {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestWriteExcelReportBindingWritesFrame(t *testing.T) {
	workbook := excelize.NewFile()
	header := true
	binding := excelReportBinding{
		Sheet:         "Summary",
		Cell:          "B2",
		ClearRange:    "B2:D10",
		IncludeHeader: &header,
	}
	frame := data.NewFrame("A",
		data.NewField("sym", nil, []string{"AAPL", "MSFT"}),
		data.NewField("price", nil, []float64{101.5, 202.25}),
	)
	if err := writeExcelReportBinding(workbook, binding, []*data.Frame{frame}); err != nil {
		t.Fatalf("writeExcelReportBinding returned error: %v", err)
	}
	assertCell(t, workbook, "Summary", "B2", "sym")
	assertCell(t, workbook, "Summary", "C2", "price")
	assertCell(t, workbook, "Summary", "B3", "AAPL")
	assertCell(t, workbook, "Summary", "C4", "202.25")
}

func TestWriteSubmittedExcelReportBindingWritesFrame(t *testing.T) {
	workbook := excelize.NewFile()
	header := true
	binding := excelReportBinding{
		RefID:         "A",
		Sheet:         "PanelData",
		Cell:          "A1",
		IncludeHeader: &header,
	}
	frame := excelSubmittedFrame{
		RefID: "A",
		Fields: []excelSubmittedField{
			{Name: "sym", Values: []interface{}{"AAPL", "MSFT"}},
			{Name: "price", Values: []interface{}{101.5, 202.25}},
		},
	}
	if err := writeSubmittedExcelReportBinding(workbook, binding, []excelSubmittedFrame{frame}); err != nil {
		t.Fatalf("writeSubmittedExcelReportBinding returned error: %v", err)
	}
	assertCell(t, workbook, "PanelData", "A1", "sym")
	assertCell(t, workbook, "PanelData", "B1", "price")
	assertCell(t, workbook, "PanelData", "A2", "AAPL")
	assertCell(t, workbook, "PanelData", "B3", "202.25")
}

func TestSubmittedFrameMatchesTargetRefID(t *testing.T) {
	frame := excelSubmittedFrame{TargetRefID: "B"}

	if !submittedFrameMatchesBinding(frame, "b") {
		t.Fatalf("expected submitted frame to match target refId")
	}
}

func TestBuildSubmittedExcelReportBindingFallsBackToBindingOrder(t *testing.T) {
	binding := excelReportBinding{RefID: "B", Sheet: "Data", Cell: "A1"}
	request := excelReportGenerateRequest{
		Frames: []excelSubmittedFrame{
			{Fields: []excelSubmittedField{{Name: "first", Values: []interface{}{1}}}},
			{Fields: []excelSubmittedField{{Name: "second", Values: []interface{}{2}}}},
		},
	}

	result, err := buildSubmittedExcelReportBinding(binding, request, 1)
	if err != nil {
		t.Fatalf("buildSubmittedExcelReportBinding returned error: %v", err)
	}
	if len(result.SubmittedFrames) != 1 || result.SubmittedFrames[0].Fields[0].Name != "second" {
		t.Fatalf("expected second submitted frame, got %#v", result.SubmittedFrames)
	}
}

func TestExcelReportFileNameSupportsUserReportTypeAndMinuteToken(t *testing.T) {
	report := excelReportDefinition{
		ID:         "daily-risk",
		Name:       "Daily Risk",
		OutputName: "{userId}_{reportType}_yyyymmddhhmm",
		Metadata:   map[string]interface{}{"reportType": "risk"},
	}
	request := excelReportGenerateRequest{
		User: excelReportUser{ID: 42, Login: "alice"},
	}
	now := time.Date(2026, 5, 26, 17, 53, 44, 0, time.UTC)

	got := excelReportFileName(report, request, now)
	want := "42_risk_202605261753.xlsx"
	if got != want {
		t.Fatalf("unexpected filename: got %q want %q", got, want)
	}
}

func TestExcelReportFileNameAllowsUserOverrideAndSanitizes(t *testing.T) {
	report := excelReportDefinition{ID: "daily-risk", OutputName: "ignored-{timestamp}.xlsx"}
	request := excelReportGenerateRequest{FileName: `../risk:bad`}

	got := excelReportFileName(report, request, time.Date(2026, 5, 26, 17, 53, 44, 0, time.UTC))
	want := ".._risk_bad.xlsx"
	if got != want {
		t.Fatalf("unexpected filename: got %q want %q", got, want)
	}
}

func TestExcelReportTemplatePathRequiresAllowlist(t *testing.T) {
	templatePath := writeTestExcelTemplate(t, t.TempDir())
	ds := &KdbDatasource{}

	if _, err := ds.resolveExcelReportTemplatePath(templatePath); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected allowlist error, got %v", err)
	}
}

func TestExcelReportTemplatePathAllowsConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	templatePath := writeTestExcelTemplate(t, dir)
	ds := &KdbDatasource{ExcelReportTemplateDirs: dir}

	resolved, err := ds.resolveExcelReportTemplatePath(templatePath)
	if err != nil {
		t.Fatalf("resolveExcelReportTemplatePath returned error: %v", err)
	}
	if resolved != templatePath {
		t.Fatalf("unexpected resolved path: got %q want %q", resolved, templatePath)
	}
}

func TestExcelReportGenerationRejectsSubmittedRowsOverLimit(t *testing.T) {
	ds := &KdbDatasource{ExcelReports: `{"reports":[{"id":"r1","maxRows":1,"bindings":[{"id":"A","sheet":"Data","cell":"A1"}]}]}`}
	request := excelReportGenerateRequest{
		ReportID:  "r1",
		TimeRange: excelReportTimeRange{From: "2026-05-26T10:00:00Z", To: "2026-05-26T10:01:00Z"},
		Frames: []excelSubmittedFrame{
			{
				RefID:  "A",
				Fields: []excelSubmittedField{{Name: "sym", Values: []interface{}{"AAPL", "MSFT"}}},
			},
		},
	}

	_, err := ds.generateExcelReport(context.Background(), backend.PluginContext{}, request)
	if err == nil || !strings.Contains(err.Error(), "exceeds maxRows") {
		t.Fatalf("expected maxRows error, got %v", err)
	}
}

func TestExcelReportValidateResourceReportsTemplateIssues(t *testing.T) {
	ds := &KdbDatasource{
		ExcelReports: `{"reports":[{"id":"r1","templatePath":"/tmp/not-allowlisted.xlsx","bindings":[{"id":"A","sheet":"Data","cell":"A1"}]}]}`,
	}

	var resourceResp *backend.CallResourceResponse
	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{
		Path: "report/validate",
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		resourceResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}
	if resourceResp == nil || resourceResp.Status != 400 {
		t.Fatalf("expected validation failure status, got %#v", resourceResp)
	}
	var validation excelReportValidationResponse
	if err := json.Unmarshal(resourceResp.Body, &validation); err != nil {
		t.Fatalf("unable to decode validation response: %v", err)
	}
	if validation.OK || len(validation.Errors) == 0 {
		t.Fatalf("expected validation errors, got %#v", validation)
	}
}

func writeTestExcelTemplate(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "template.xlsx")
	workbook := excelize.NewFile()
	if err := workbook.SaveAs(path); err != nil {
		t.Fatalf("SaveAs returned error: %v", err)
	}
	if err := workbook.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	return path
}

func assertCell(t *testing.T, workbook *excelize.File, sheet string, cell string, want string) {
	t.Helper()
	got, err := workbook.GetCellValue(sheet, cell)
	if err != nil {
		t.Fatalf("GetCellValue(%s!%s) returned error: %v", sheet, cell, err)
	}
	if got != want {
		t.Fatalf("unexpected cell %s!%s: got %q want %q", sheet, cell, got, want)
	}
}
