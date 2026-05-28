package plugin

import (
	"bytes"
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

func TestExcelReportCatalogRejectsMixedWriteModesOnSheet(t *testing.T) {
	ds := &KdbDatasource{ExcelReports: `{"reports":[{"id":"r1","bindings":[{"id":"A","sheet":"Data","cell":"A1","writeMode":"stream"},{"id":"B","sheet":"Data","cell":"A10","writeMode":"rows"}]}]}`}
	if _, err := ds.excelReportCatalog(); err == nil || !strings.Contains(err.Error(), "mixes writeMode") {
		t.Fatalf("expected mixed writeMode error, got %v", err)
	}
}

func TestExcelReportValidateWarnsStreamClearRangeIgnored(t *testing.T) {
	ds := &KdbDatasource{ExcelReports: `{"reports":[{"id":"r1","writeMode":"stream","bindings":[{"id":"A","sheet":"Data","cell":"A1","clearRange":"A1:Z5000"}]}]}`}

	validation := ds.validateExcelReportConfiguration()
	if !validation.OK {
		t.Fatalf("expected valid configuration, got %#v", validation)
	}
	if len(validation.Warnings) != 1 || !strings.Contains(validation.Warnings[0].Message, "clearRange is ignored") {
		t.Fatalf("expected clearRange warning, got %#v", validation.Warnings)
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
	stats, err := writeExcelReportBinding(workbook, excelReportDefinition{}, binding, []*data.Frame{frame})
	if err != nil {
		t.Fatalf("writeExcelReportBinding returned error: %v", err)
	}
	if stats.Rows != 3 || stats.Cells != 6 || stats.Frames != 1 {
		t.Fatalf("unexpected write stats: %#v", stats)
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
	stats, err := writeSubmittedExcelReportBinding(workbook, excelReportDefinition{}, binding, []excelSubmittedFrame{frame})
	if err != nil {
		t.Fatalf("writeSubmittedExcelReportBinding returned error: %v", err)
	}
	if stats.Rows != 3 || stats.Cells != 6 || stats.Frames != 1 {
		t.Fatalf("unexpected write stats: %#v", stats)
	}
	assertCell(t, workbook, "PanelData", "A1", "sym")
	assertCell(t, workbook, "PanelData", "B1", "price")
	assertCell(t, workbook, "PanelData", "A2", "AAPL")
	assertCell(t, workbook, "PanelData", "B3", "202.25")
}

func TestWriteExcelReportBindingStreamModeWritesFrame(t *testing.T) {
	workbook := excelize.NewFile()
	header := true
	report := excelReportDefinition{WriteMode: excelReportWriteModeStream}
	binding := excelReportBinding{
		Sheet:         "Summary",
		Cell:          "B2",
		IncludeHeader: &header,
	}
	frame := data.NewFrame("A",
		data.NewField("sym", nil, []string{"AAPL", "MSFT"}),
		data.NewField("price", nil, []float64{101.5, 202.25}),
	)
	stats, err := writeExcelReportBinding(workbook, report, binding, []*data.Frame{frame})
	if err != nil {
		t.Fatalf("writeExcelReportBinding returned error: %v", err)
	}
	if stats.Rows != 3 || stats.Cells != 6 || stats.Frames != 1 {
		t.Fatalf("unexpected write stats: %#v", stats)
	}

	reopened := reopenWorkbook(t, workbook)
	assertCell(t, reopened, "Summary", "B2", "sym")
	assertCell(t, reopened, "Summary", "C2", "price")
	assertCell(t, reopened, "Summary", "B3", "AAPL")
	assertCell(t, reopened, "Summary", "C4", "202.25")
}

func TestExcelReportStreamModeRejectsMixedSheetWrites(t *testing.T) {
	workbook := excelize.NewFile()
	writer := newExcelReportWorkbookWriter(workbook)
	header := true
	streamBinding := excelReportBinding{
		ID:            "stream",
		Sheet:         "Data",
		Cell:          "A1",
		IncludeHeader: &header,
		WriteMode:     excelReportWriteModeStream,
	}
	normalBinding := excelReportBinding{
		ID:            "normal",
		Sheet:         "Data",
		Cell:          "A10",
		IncludeHeader: &header,
		WriteMode:     excelReportWriteModeRows,
	}
	frame := data.NewFrame("A", data.NewField("sym", nil, []string{"AAPL"}))
	if _, err := writer.writeExcelReportBinding(excelReportDefinition{}, streamBinding, []*data.Frame{frame}); err != nil {
		t.Fatalf("stream write returned error: %v", err)
	}
	if _, err := writer.writeExcelReportBinding(excelReportDefinition{}, normalBinding, []*data.Frame{frame}); err == nil || !strings.Contains(err.Error(), "mixes normal Excel writes") {
		t.Fatalf("expected mixed writeMode error, got %v", err)
	}
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

func TestExcelReportGenerateLinkResourceDownloadsWorkbook(t *testing.T) {
	ds := &KdbDatasource{
		ExcelReports: `{"reports":[{"id":"r1","name":"Report","bindings":[{"id":"A","sheet":"Data","cell":"A1"}]}]}`,
	}
	request := `{
		"reportId":"r1",
		"fileName":"custom-report",
		"timeRange":{"from":"2026-05-26T10:00:00Z","to":"2026-05-26T10:01:00Z"},
		"frames":[{"refId":"A","fields":[{"name":"sym","values":["AAPL","MSFT"]},{"name":"price","values":[100.5,101.25]}]}]
	}`

	var linkResp *backend.CallResourceResponse
	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{
		Path: "report/generate-link",
		Body: []byte(request),
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		linkResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource generate-link returned error: %v", err)
	}
	if linkResp == nil || linkResp.Status != 200 {
		t.Fatalf("expected generate-link success, got %#v", linkResp)
	}
	var link excelReportGenerateLinkResponse
	if err := json.Unmarshal(linkResp.Body, &link); err != nil {
		t.Fatalf("unable to decode generate-link response: %v", err)
	}
	if !link.OK || link.Token == "" || link.FileName != "custom-report.xlsx" || link.GenerationMs <= 0 {
		t.Fatalf("unexpected generate-link response: %#v", link)
	}

	var downloadResp *backend.CallResourceResponse
	err = ds.CallResource(context.Background(), &backend.CallResourceRequest{
		Path: "report/download",
		URL:  "http://localhost/report/download?token=" + link.Token,
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		downloadResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource download returned error: %v", err)
	}
	if downloadResp == nil || downloadResp.Status != 200 {
		t.Fatalf("expected download success, got %#v", downloadResp)
	}
	if got := downloadResp.Headers["x-asyncq-file-name"][0]; got != "custom-report.xlsx" {
		t.Fatalf("unexpected x-asyncq-file-name header: %q", got)
	}
	if got := downloadResp.Headers["content-disposition"][0]; !strings.Contains(got, "custom-report.xlsx") {
		t.Fatalf("unexpected content-disposition header: %q", got)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(downloadResp.Body))
	if err != nil {
		t.Fatalf("downloaded workbook did not open: %v", err)
	}
	defer func() { _ = workbook.Close() }()
	rows, err := workbook.GetRows("Data")
	if err != nil {
		t.Fatalf("unable to read Data sheet: %v", err)
	}
	if len(rows) != 3 || rows[0][0] != "sym" || rows[1][0] != "AAPL" || rows[2][0] != "MSFT" {
		t.Fatalf("unexpected workbook rows: %#v", rows)
	}
}

func BenchmarkWriteExcelReportBindingRows5000x8(b *testing.B) {
	benchmarkWriteExcelReportBinding(b, excelReportWriteModeRows)
}

func BenchmarkWriteExcelReportBindingCells5000x8(b *testing.B) {
	benchmarkWriteExcelReportBinding(b, excelReportWriteModeCells)
}

func BenchmarkWriteExcelReportBindingStream5000x8(b *testing.B) {
	benchmarkWriteExcelReportBinding(b, excelReportWriteModeStream)
}

func benchmarkWriteExcelReportBinding(b *testing.B, writeMode string) {
	frame := benchmarkExcelFrame(5000)
	header := true
	report := excelReportDefinition{WriteMode: writeMode}
	binding := excelReportBinding{
		Sheet:         "Data",
		Cell:          "A1",
		IncludeHeader: &header,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		workbook := excelize.NewFile()
		if _, err := writeExcelReportBinding(workbook, report, binding, []*data.Frame{frame}); err != nil {
			b.Fatalf("writeExcelReportBinding returned error: %v", err)
		}
		if _, err := workbook.WriteToBuffer(); err != nil {
			b.Fatalf("WriteToBuffer returned error: %v", err)
		}
		_ = workbook.Close()
	}
}

func benchmarkExcelFrame(rows int) *data.Frame {
	syms := []string{"AAPL", "MSFT", "GOOG", "AMZN"}
	sym := make([]string, rows)
	book := make([]string, rows)
	price := make([]float64, rows)
	size := make([]int64, rows)
	volume := make([]int64, rows)
	venue := make([]string, rows)
	side := make([]string, rows)
	ts := make([]time.Time, rows)
	base := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < rows; i++ {
		sym[i] = syms[i%len(syms)]
		book[i] = "EQ"
		price[i] = 100 + float64(i%1000)/10
		size[i] = int64(100 + i%500)
		volume[i] = int64(i * 10)
		venue[i] = "XNYS"
		if i%2 == 0 {
			side[i] = "B"
		} else {
			side[i] = "S"
		}
		ts[i] = base.Add(time.Duration(i) * time.Millisecond)
	}
	return data.NewFrame("benchmark",
		data.NewField("time", nil, ts),
		data.NewField("sym", nil, sym),
		data.NewField("book", nil, book),
		data.NewField("price", nil, price),
		data.NewField("size", nil, size),
		data.NewField("volume", nil, volume),
		data.NewField("venue", nil, venue),
		data.NewField("side", nil, side),
	)
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

func reopenWorkbook(t *testing.T, workbook *excelize.File) *excelize.File {
	t.Helper()
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer returned error: %v", err)
	}
	reopened, err := excelize.OpenReader(bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader returned error: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
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
