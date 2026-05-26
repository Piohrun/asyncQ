package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/xuri/excelize/v2"
)

const excelReportContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

var excelReportParameterPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.-]*)(?::([^{}]*))?\}`)

type excelReportCatalog struct {
	Reports []excelReportDefinition `json:"reports"`
}

type excelReportPublicCatalog struct {
	Reports []excelReportPublicDefinition `json:"reports"`
}

type excelReportPublicDefinition struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	OutputName  string                 `json:"outputName,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type excelReportDefinition struct {
	ID                string                 `json:"id"`
	Name              string                 `json:"name"`
	Description       string                 `json:"description,omitempty"`
	OutputName        string                 `json:"outputName,omitempty"`
	TemplatePath      string                 `json:"templatePath,omitempty"`
	ExecutionMode     string                 `json:"executionMode,omitempty"`
	CompatibilityMode string                 `json:"compatibilityMode,omitempty"`
	QueryCacheMode    string                 `json:"queryCacheMode,omitempty"`
	QueryCacheKeyMode string                 `json:"queryCacheKeyMode,omitempty"`
	Timeout           int                    `json:"timeOut,omitempty"`
	MaxDataPoints     int64                  `json:"maxDataPoints,omitempty"`
	IntervalMs        int64                  `json:"intervalMs,omitempty"`
	Bindings          []excelReportBinding   `json:"bindings"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

type excelReportBinding struct {
	ID                        string `json:"id,omitempty"`
	RefID                     string `json:"refId,omitempty"`
	QueryText                 string `json:"queryText,omitempty"`
	Sheet                     string `json:"sheet"`
	Cell                      string `json:"cell"`
	ClearRange                string `json:"clearRange,omitempty"`
	IncludeHeader             *bool  `json:"includeHeader,omitempty"`
	ExecutionMode             string `json:"executionMode,omitempty"`
	CompatibilityMode         string `json:"compatibilityMode,omitempty"`
	DeferredQueryWrapper      string `json:"deferredQueryWrapper,omitempty"`
	PanopticonQueryWrapper    string `json:"panopticonQueryWrapper,omitempty"`
	PanopticonRequestFunction string `json:"panopticonRequestFunction,omitempty"`
	QueryCacheMode            string `json:"queryCacheMode,omitempty"`
	QueryCacheKeyMode         string `json:"queryCacheKeyMode,omitempty"`
	Timeout                   int    `json:"timeOut,omitempty"`
	MaxDataPoints             int64  `json:"maxDataPoints,omitempty"`
	IntervalMs                int64  `json:"intervalMs,omitempty"`
	UseTimeColumn             *bool  `json:"useTimeColumn,omitempty"`
	TimeColumn                string `json:"timeColumn,omitempty"`
	IncludeKeyColumns         *bool  `json:"includeKeyColumns,omitempty"`
}

type excelReportGenerateRequest struct {
	ReportID  string                 `json:"reportId"`
	FileName  string                 `json:"fileName,omitempty"`
	TimeRange excelReportTimeRange   `json:"timeRange"`
	Variables map[string]interface{} `json:"variables,omitempty"`
	User      excelReportUser        `json:"user,omitempty"`
	Timezone  string                 `json:"timezone,omitempty"`
	Frames    []excelSubmittedFrame  `json:"frames,omitempty"`
}

type excelReportUser struct {
	ID    int64  `json:"id,omitempty"`
	UID   string `json:"uid,omitempty"`
	Login string `json:"login,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

type excelReportTimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type excelReportResourceResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Report  string `json:"report,omitempty"`
	Binding string `json:"binding,omitempty"`
}

type excelReportGenerated struct {
	Body     []byte
	FileName string
}

type excelReportRunResult struct {
	Binding         excelReportBinding
	Frames          []*data.Frame
	SubmittedFrames []excelSubmittedFrame
}

type excelSubmittedFrame struct {
	RefID       string                `json:"refId,omitempty"`
	TargetRefID string                `json:"targetRefId,omitempty"`
	PanelID     interface{}           `json:"panelId,omitempty"`
	Name        string                `json:"name,omitempty"`
	Fields      []excelSubmittedField `json:"fields"`
}

type excelSubmittedField struct {
	Name   string        `json:"name"`
	Values []interface{} `json:"values"`
}

func (d *KdbDatasource) handleExcelReportResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender, path string) error {
	switch path {
	case "report/catalog":
		catalog, err := d.excelReportCatalog()
		if err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportResourceResponse{OK: false, Error: err.Error()})
		}
		return sendResourceJSON(sender, http.StatusOK, publicExcelReportCatalog(catalog))
	case "report/generate":
		var body excelReportGenerateRequest
		if err := decodeExcelReportGenerateRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportResourceResponse{OK: false, Error: err.Error()})
		}
		generated, err := d.generateExcelReport(ctx, req.PluginContext, body)
		if err != nil {
			log.DefaultLogger.Error("excel report generation failed", "datasourceUID", d.instanceUID, "reportID", body.ReportID, "error", err.Error())
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportResourceResponse{OK: false, Report: body.ReportID, Error: err.Error()})
		}
		return sender.Send(&backend.CallResourceResponse{
			Status: http.StatusOK,
			Headers: map[string][]string{
				"content-type":        {excelReportContentType},
				"content-disposition": {excelReportContentDisposition(generated.FileName)},
				"x-asyncq-file-name":  {generated.FileName},
			},
			Body: generated.Body,
		})
	default:
		return sendResourceJSON(sender, http.StatusNotFound, excelReportResourceResponse{OK: false, Error: "unknown report resource path"})
	}
}

func publicExcelReportCatalog(catalog excelReportCatalog) excelReportPublicCatalog {
	public := excelReportPublicCatalog{Reports: make([]excelReportPublicDefinition, 0, len(catalog.Reports))}
	for _, report := range catalog.Reports {
		public.Reports = append(public.Reports, excelReportPublicDefinition{
			ID:          report.ID,
			Name:        report.Name,
			Description: report.Description,
			OutputName:  report.OutputName,
			Metadata:    report.Metadata,
		})
	}
	return public
}

func decodeExcelReportGenerateRequest(raw []byte, target *excelReportGenerateRequest) error {
	if len(raw) == 0 {
		return fmt.Errorf("missing report generation request body")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		values, parseErr := url.ParseQuery(string(raw))
		if parseErr != nil {
			return err
		}
		payload := strings.TrimSpace(values.Get("payload"))
		if payload == "" {
			return err
		}
		if payloadErr := json.Unmarshal([]byte(payload), target); payloadErr != nil {
			return payloadErr
		}
	}
	target.ReportID = strings.TrimSpace(target.ReportID)
	if target.ReportID == "" {
		return fmt.Errorf("reportId is required")
	}
	return nil
}

func (d *KdbDatasource) excelReportCatalog() (excelReportCatalog, error) {
	raw := strings.TrimSpace(d.ExcelReports)
	if raw == "" {
		return defaultExcelReportCatalog(), nil
	}

	var catalog excelReportCatalog
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &catalog.Reports); err != nil {
			return excelReportCatalog{}, fmt.Errorf("unable to parse excelReports array: %w", err)
		}
	} else {
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			return excelReportCatalog{}, fmt.Errorf("unable to parse excelReports object: %w", err)
		}
	}
	if err := validateExcelReportCatalog(catalog); err != nil {
		return excelReportCatalog{}, err
	}
	return catalog, nil
}

func defaultExcelReportCatalog() excelReportCatalog {
	header := true
	return excelReportCatalog{Reports: []excelReportDefinition{
		{
			ID:                "demo-market-report",
			Name:              "Demo market report",
			Description:       "Writes the demo latest prices and latest trades into workbook data sheets.",
			OutputName:        "asyncq-demo-market-report-{timestamp}.xlsx",
			CompatibilityMode: CompatibilityModeNative,
			MaxDataPoints:     10000,
			IntervalMs:        1000,
			Bindings: []excelReportBinding{
				{
					ID:            "last-prices",
					RefID:         "A",
					QueryText:     ".demo.asyncq.lastPrices[]",
					Sheet:         "Summary",
					Cell:          "A1",
					ClearRange:    "A1:Z2000",
					IncludeHeader: &header,
				},
				{
					ID:            "latest-trades",
					RefID:         "B",
					QueryText:     ".demo.asyncq.latest 50",
					Sheet:         "Trades",
					Cell:          "A1",
					ClearRange:    "A1:Z2000",
					IncludeHeader: &header,
				},
			},
		},
	}}
}

func validateExcelReportCatalog(catalog excelReportCatalog) error {
	seen := map[string]struct{}{}
	for i, report := range catalog.Reports {
		id := strings.TrimSpace(report.ID)
		if id == "" {
			return fmt.Errorf("excelReports.reports[%d].id is required", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate excel report id %q", id)
		}
		seen[id] = struct{}{}
		if len(report.Bindings) == 0 {
			return fmt.Errorf("excel report %q must define at least one binding", id)
		}
		for j, binding := range report.Bindings {
			if strings.TrimSpace(binding.QueryText) == "" && strings.TrimSpace(binding.RefID) == "" && strings.TrimSpace(binding.ID) == "" {
				return fmt.Errorf("excel report %q binding %d requires queryText, refId, or id", id, j)
			}
			if strings.TrimSpace(binding.Sheet) == "" {
				return fmt.Errorf("excel report %q binding %d requires sheet", id, j)
			}
			if strings.TrimSpace(binding.Cell) == "" {
				return fmt.Errorf("excel report %q binding %d requires cell", id, j)
			}
			if _, _, err := excelize.CellNameToCoordinates(strings.TrimSpace(binding.Cell)); err != nil {
				return fmt.Errorf("excel report %q binding %d has invalid cell %q: %w", id, j, binding.Cell, err)
			}
		}
	}
	return nil
}

func (d *KdbDatasource) generateExcelReport(ctx context.Context, pCtx backend.PluginContext, request excelReportGenerateRequest) (excelReportGenerated, error) {
	catalog, err := d.excelReportCatalog()
	if err != nil {
		return excelReportGenerated{}, err
	}
	report, ok := findExcelReport(catalog, request.ReportID)
	if !ok {
		return excelReportGenerated{}, fmt.Errorf("unknown reportId %q", request.ReportID)
	}
	if strings.TrimSpace(request.TimeRange.From) == "" || strings.TrimSpace(request.TimeRange.To) == "" {
		return excelReportGenerated{}, fmt.Errorf("timeRange.from and timeRange.to are required")
	}
	from, err := parseExcelReportTime(request.TimeRange.From)
	if err != nil {
		return excelReportGenerated{}, fmt.Errorf("invalid timeRange.from: %w", err)
	}
	to, err := parseExcelReportTime(request.TimeRange.To)
	if err != nil {
		return excelReportGenerated{}, fmt.Errorf("invalid timeRange.to: %w", err)
	}
	if !from.Before(to) && !from.Equal(to) {
		return excelReportGenerated{}, fmt.Errorf("timeRange.from must be before or equal to timeRange.to")
	}

	results := make([]excelReportRunResult, 0, len(report.Bindings))
	for index, binding := range report.Bindings {
		if err := ctx.Err(); err != nil {
			return excelReportGenerated{}, err
		}
		var result excelReportRunResult
		var err error
		if strings.TrimSpace(binding.QueryText) == "" {
			result, err = buildSubmittedExcelReportBinding(binding, request, index)
		} else {
			result, err = d.runExcelReportBinding(pCtx, report, binding, request, from, to, index)
		}
		if err != nil {
			return excelReportGenerated{}, fmt.Errorf("binding %q failed: %w", excelReportBindingID(binding), err)
		}
		results = append(results, result)
	}

	workbook, err := openExcelReportWorkbook(report)
	if err != nil {
		return excelReportGenerated{}, err
	}
	defer func() { _ = workbook.Close() }()

	for _, result := range results {
		if err := writeExcelReportBinding(workbook, result.Binding, result.Frames); err != nil {
			return excelReportGenerated{}, err
		}
		if err := writeSubmittedExcelReportBinding(workbook, result.Binding, result.SubmittedFrames); err != nil {
			return excelReportGenerated{}, err
		}
	}
	setExcelReportActiveSheet(workbook, report)
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return excelReportGenerated{}, err
	}
	fileName := excelReportFileName(report, request, time.Now().UTC())
	log.DefaultLogger.Info("excel report generated", "datasourceUID", d.instanceUID, "reportID", report.ID, "requestedFileName", request.FileName, "resolvedFileName", fileName, "frameCount", len(results))
	return excelReportGenerated{
		Body:     buffer.Bytes(),
		FileName: fileName,
	}, nil
}

func findExcelReport(catalog excelReportCatalog, id string) (excelReportDefinition, bool) {
	for _, report := range catalog.Reports {
		if report.ID == id {
			return report, true
		}
	}
	return excelReportDefinition{}, false
}

func parseExcelReportTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), nil
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 timestamp or Unix milliseconds")
}

func (d *KdbDatasource) runExcelReportBinding(pCtx backend.PluginContext, report excelReportDefinition, binding excelReportBinding, request excelReportGenerateRequest, from time.Time, to time.Time, index int) (excelReportRunResult, error) {
	refID := strings.TrimSpace(binding.RefID)
	if refID == "" {
		refID = strings.TrimSpace(binding.ID)
	}
	if refID == "" {
		refID = fmt.Sprintf("R%d", index+1)
	}
	queryText := expandExcelReportParameters(binding.QueryText, request, from, to)
	model := QueryModel{
		QueryText:                 queryText,
		OriginalQueryText:         binding.QueryText,
		Timeout:                   firstPositiveInt(binding.Timeout, report.Timeout),
		ExecutionMode:             firstNonEmpty(binding.ExecutionMode, report.ExecutionMode, ExecutionModeSync),
		CompatibilityMode:         firstNonEmpty(binding.CompatibilityMode, report.CompatibilityMode, d.CompatibilityMode),
		DeferredQueryWrapper:      binding.DeferredQueryWrapper,
		PanopticonQueryWrapper:    expandExcelReportParameters(binding.PanopticonQueryWrapper, request, from, to),
		PanopticonRequestFunction: binding.PanopticonRequestFunction,
		QueryCacheMode:            firstNonEmpty(binding.QueryCacheMode, report.QueryCacheMode),
		QueryCacheKeyMode:         firstNonEmpty(binding.QueryCacheKeyMode, report.QueryCacheKeyMode),
		TimeColumn:                binding.TimeColumn,
	}
	if binding.UseTimeColumn != nil {
		model.UseTimeColumn = *binding.UseTimeColumn
	}
	if binding.IncludeKeyColumns != nil {
		model.IncludeKeyColumns = *binding.IncludeKeyColumns
	}
	d.normalizeQueryModel(&model)
	if model.ExecutionMode != ExecutionModeSync {
		return excelReportRunResult{}, fmt.Errorf("excel report bindings currently require sync execution, got %q", model.ExecutionMode)
	}

	query := backend.DataQuery{
		RefID:         refID,
		QueryType:     "excel-report",
		MaxDataPoints: firstPositiveInt64(binding.MaxDataPoints, report.MaxDataPoints, 10000),
		Interval:      time.Duration(firstPositiveInt64(binding.IntervalMs, report.IntervalMs, 1000)) * time.Millisecond,
		TimeRange: backend.TimeRange{
			From: from,
			To:   to,
		},
	}
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return excelReportRunResult{}, err
	}
	query.JSON = modelJSON
	if err := prepareQueryForExecution(pCtx, query, &model); err != nil {
		return excelReportRunResult{}, err
	}
	fields := d.diagnosticQueryFields(pCtx, query, model, fmt.Sprintf("excel-report-%s-%s", report.ID, refID))
	fields = append(fields, "excelReportID", report.ID, "excelBindingID", binding.ID, "excelSheet", binding.Sheet, "excelCell", binding.Cell)
	result, err := d.runSyncQueryWithCache(pCtx, query, model, fields)
	if err != nil {
		return excelReportRunResult{}, err
	}
	binding.RefID = refID
	return excelReportRunResult{Binding: binding, Frames: result.frames}, nil
}

func buildSubmittedExcelReportBinding(binding excelReportBinding, request excelReportGenerateRequest, bindingIndex int) (excelReportRunResult, error) {
	bindingID := excelReportBindingID(binding)
	frames := make([]excelSubmittedFrame, 0)
	for _, frame := range request.Frames {
		if submittedFrameMatchesBinding(frame, bindingID) {
			frames = append(frames, frame)
		}
	}
	if len(frames) == 0 && bindingIndex >= 0 && bindingIndex < len(request.Frames) {
		frame := request.Frames[bindingIndex]
		if len(frame.Fields) > 0 {
			frames = append(frames, frame)
		}
	}
	if len(frames) == 0 {
		return excelReportRunResult{}, fmt.Errorf("no submitted frames matched refId/id %q and no frame was available at binding index %d; submitted frame count: %d", bindingID, bindingIndex, len(request.Frames))
	}
	if strings.TrimSpace(binding.RefID) == "" {
		binding.RefID = bindingID
	}
	return excelReportRunResult{Binding: binding, SubmittedFrames: frames}, nil
}

func submittedFrameMatchesBinding(frame excelSubmittedFrame, bindingID string) bool {
	if bindingID == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(frame.RefID), bindingID) ||
		strings.EqualFold(strings.TrimSpace(frame.TargetRefID), bindingID) ||
		strings.EqualFold(strings.TrimSpace(frame.Name), bindingID)
}

func excelReportBindingID(binding excelReportBinding) string {
	if strings.TrimSpace(binding.RefID) != "" {
		return strings.TrimSpace(binding.RefID)
	}
	if strings.TrimSpace(binding.ID) != "" {
		return strings.TrimSpace(binding.ID)
	}
	return "report-binding"
}

func expandExcelReportParameters(input string, request excelReportGenerateRequest, from time.Time, to time.Time) string {
	if strings.TrimSpace(input) == "" {
		return input
	}
	return excelReportParameterPattern.ReplaceAllStringFunc(input, func(token string) string {
		matches := excelReportParameterPattern.FindStringSubmatch(token)
		if len(matches) < 2 {
			return token
		}
		name := matches[1]
		if isPanopticonTimeMacroName(name) || isExcelReportBuiltInMacro(name) {
			return token
		}
		value, ok := lookupExcelReportVariable(request.Variables, name)
		if !ok {
			return token
		}
		delimiter := ","
		if len(matches) > 2 && matches[2] != "" {
			delimiter = matches[2]
		}
		return excelReportVariableValue(value, delimiter)
	})
}

func isExcelReportBuiltInMacro(name string) bool {
	switch name {
	case "ReportID", "ReportName", "timestamp":
		return true
	default:
		return false
	}
}

func isPanopticonTimeMacroName(name string) bool {
	switch name {
	case "TimeWindowStart", "TimeWindowEnd", "Snapshot", "FocusTime", "Start", "End", "From", "To",
		"TimeWindowStartText", "TimeWindowEndText", "SnapshotText", "FocusTimeText",
		"Interval", "IntervalNs", "IntervalMs", "MaxDataPoints", "RefID", "OrgID",
		"UserName", "UserLogin", "UserEmail", "DatasourceName", "DatasourceUID", "Query":
		return true
	default:
		return false
	}
}

func lookupExcelReportVariable(variables map[string]interface{}, name string) (interface{}, bool) {
	if variables == nil {
		return nil, false
	}
	if value, ok := variables[name]; ok {
		return value, true
	}
	for key, value := range variables {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func excelReportVariableValue(value interface{}, delimiter string) string {
	switch v := value.(type) {
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, excelReportSingleVariableValue(item))
		}
		return strings.Join(parts, delimiter)
	case []string:
		return strings.Join(v, delimiter)
	default:
		return excelReportSingleVariableValue(v)
	}
}

func excelReportSingleVariableValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func openExcelReportWorkbook(report excelReportDefinition) (*excelize.File, error) {
	templatePath := strings.TrimSpace(report.TemplatePath)
	if templatePath == "" {
		return excelize.NewFile(), nil
	}
	expandedPath := os.ExpandEnv(templatePath)
	if !filepath.IsAbs(expandedPath) {
		expandedPath = filepath.Clean(expandedPath)
	}
	workbook, err := excelize.OpenFile(expandedPath)
	if err != nil {
		return nil, fmt.Errorf("unable to open report template %q: %w", templatePath, err)
	}
	return workbook, nil
}

func writeExcelReportBinding(workbook *excelize.File, binding excelReportBinding, frames []*data.Frame) error {
	if len(frames) == 0 {
		return nil
	}
	sheet := strings.TrimSpace(binding.Sheet)
	cell := strings.TrimSpace(binding.Cell)
	if sheet == "" || cell == "" {
		return fmt.Errorf("report binding requires sheet and cell")
	}
	if err := ensureExcelReportSheet(workbook, sheet); err != nil {
		return err
	}
	if strings.TrimSpace(binding.ClearRange) != "" {
		if err := clearExcelReportRange(workbook, sheet, binding.ClearRange); err != nil {
			return err
		}
	}
	col, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return err
	}
	includeHeader := true
	if binding.IncludeHeader != nil {
		includeHeader = *binding.IncludeHeader
	}
	cursor := row
	for frameIndex, frame := range frames {
		if frame == nil || len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, err := writeExcelReportFrame(workbook, sheet, col, cursor, frame, includeHeader)
		if err != nil {
			return err
		}
		cursor += written
	}
	return nil
}

func writeSubmittedExcelReportBinding(workbook *excelize.File, binding excelReportBinding, frames []excelSubmittedFrame) error {
	if len(frames) == 0 {
		return nil
	}
	sheet := strings.TrimSpace(binding.Sheet)
	cell := strings.TrimSpace(binding.Cell)
	if sheet == "" || cell == "" {
		return fmt.Errorf("report binding requires sheet and cell")
	}
	if err := ensureExcelReportSheet(workbook, sheet); err != nil {
		return err
	}
	if strings.TrimSpace(binding.ClearRange) != "" {
		if err := clearExcelReportRange(workbook, sheet, binding.ClearRange); err != nil {
			return err
		}
	}
	col, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return err
	}
	includeHeader := true
	if binding.IncludeHeader != nil {
		includeHeader = *binding.IncludeHeader
	}
	cursor := row
	for frameIndex, frame := range frames {
		if len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, err := writeSubmittedExcelReportFrame(workbook, sheet, col, cursor, frame, includeHeader)
		if err != nil {
			return err
		}
		cursor += written
	}
	return nil
}

func ensureExcelReportSheet(workbook *excelize.File, sheet string) error {
	if sheet == "" {
		return fmt.Errorf("sheet name is required")
	}
	if idx, err := workbook.GetSheetIndex(sheet); err == nil && idx >= 0 {
		return nil
	}
	if idx, err := workbook.NewSheet(sheet); err != nil {
		return err
	} else if idx >= 0 {
		return nil
	}
	return nil
}

func clearExcelReportRange(workbook *excelize.File, sheet string, rangeRef string) error {
	start, end, err := parseExcelRange(rangeRef)
	if err != nil {
		return err
	}
	startCol, startRow, err := excelize.CellNameToCoordinates(start)
	if err != nil {
		return err
	}
	endCol, endRow, err := excelize.CellNameToCoordinates(end)
	if err != nil {
		return err
	}
	if startCol > endCol {
		startCol, endCol = endCol, startCol
	}
	if startRow > endRow {
		startRow, endRow = endRow, startRow
	}
	for row := startRow; row <= endRow; row++ {
		for col := startCol; col <= endCol; col++ {
			cell, err := excelize.CoordinatesToCellName(col, row)
			if err != nil {
				return err
			}
			if err := workbook.SetCellValue(sheet, cell, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseExcelRange(rangeRef string) (string, string, error) {
	cleaned := strings.TrimSpace(rangeRef)
	if strings.Contains(cleaned, "!") {
		parts := strings.Split(cleaned, "!")
		cleaned = parts[len(parts)-1]
	}
	cleaned = strings.ReplaceAll(cleaned, "$", "")
	parts := strings.Split(cleaned, ":")
	if len(parts) == 1 {
		return parts[0], parts[0], nil
	}
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid Excel range %q", rangeRef)
	}
	if strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid Excel range %q", rangeRef)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func writeExcelReportFrame(workbook *excelize.File, sheet string, startCol int, startRow int, frame *data.Frame, includeHeader bool) (int, error) {
	row := startRow
	if includeHeader {
		for colOffset, field := range frame.Fields {
			cell, err := excelize.CoordinatesToCellName(startCol+colOffset, row)
			if err != nil {
				return 0, err
			}
			if err := workbook.SetCellValue(sheet, cell, field.Name); err != nil {
				return 0, err
			}
		}
		row++
	}
	length := frame.Rows()
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			cell, err := excelize.CoordinatesToCellName(startCol+colOffset, row)
			if err != nil {
				return 0, err
			}
			if err := workbook.SetCellValue(sheet, cell, excelReportCellValue(field, frameRow)); err != nil {
				return 0, err
			}
		}
		row++
	}
	return row - startRow, nil
}

func writeSubmittedExcelReportFrame(workbook *excelize.File, sheet string, startCol int, startRow int, frame excelSubmittedFrame, includeHeader bool) (int, error) {
	row := startRow
	if includeHeader {
		for colOffset, field := range frame.Fields {
			cell, err := excelize.CoordinatesToCellName(startCol+colOffset, row)
			if err != nil {
				return 0, err
			}
			if err := workbook.SetCellValue(sheet, cell, field.Name); err != nil {
				return 0, err
			}
		}
		row++
	}
	length := submittedFrameRows(frame)
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			cell, err := excelize.CoordinatesToCellName(startCol+colOffset, row)
			if err != nil {
				return 0, err
			}
			if err := workbook.SetCellValue(sheet, cell, excelSubmittedCellValue(field, frameRow)); err != nil {
				return 0, err
			}
		}
		row++
	}
	return row - startRow, nil
}

func submittedFrameRows(frame excelSubmittedFrame) int {
	length := 0
	for _, field := range frame.Fields {
		if len(field.Values) > length {
			length = len(field.Values)
		}
	}
	return length
}

func excelSubmittedCellValue(field excelSubmittedField, row int) interface{} {
	if row < 0 || row >= len(field.Values) {
		return nil
	}
	value := field.Values[row]
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return parsed
		}
		return v
	case []byte:
		return string(v)
	case json.RawMessage:
		return string(v)
	default:
		return v
	}
}

func excelReportCellValue(field *data.Field, row int) interface{} {
	if field == nil || row < 0 || row >= field.Len() || field.NilAt(row) {
		return nil
	}
	value := field.At(row)
	switch v := value.(type) {
	case []byte:
		return string(v)
	case json.RawMessage:
		return string(v)
	default:
		return v
	}
}

func setExcelReportActiveSheet(workbook *excelize.File, report excelReportDefinition) {
	for _, binding := range report.Bindings {
		sheet := strings.TrimSpace(binding.Sheet)
		if sheet == "" {
			continue
		}
		if idx, err := workbook.GetSheetIndex(sheet); err == nil && idx >= 0 {
			workbook.SetActiveSheet(idx)
			return
		}
	}
}

func excelReportFileName(report excelReportDefinition, request excelReportGenerateRequest, now time.Time) string {
	name := firstNonEmpty(request.FileName, report.OutputName, report.ID+"-{timestamp}.xlsx")
	name = renderExcelReportFileNameTemplate(name, report, request, now)
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name += ".xlsx"
	}
	return sanitizeExcelReportFileName(name)
}

func renderExcelReportFileNameTemplate(name string, report excelReportDefinition, request excelReportGenerateRequest, now time.Time) string {
	userID := ""
	if request.User.ID > 0 {
		userID = strconv.FormatInt(request.User.ID, 10)
	}
	replacements := map[string]string{
		"{reportId}":            report.ID,
		"{ReportID}":            report.ID,
		"{reportName}":          report.Name,
		"{ReportName}":          report.Name,
		"{reportType}":          excelReportType(report),
		"{ReportType}":          excelReportType(report),
		"{userId}":              userID,
		"{UserID}":              userID,
		"{userUid}":             request.User.UID,
		"{UserUID}":             request.User.UID,
		"{login}":               request.User.Login,
		"{userLogin}":           request.User.Login,
		"{email}":               request.User.Email,
		"{userEmail}":           request.User.Email,
		"{userName}":            request.User.Name,
		"{timestamp}":           now.Format("20060102-150405"),
		"{yyyymmdd}":            now.Format("20060102"),
		"{yyyymmddhhmm}":        now.Format("200601021504"),
		"yyyymmddhhmm":          now.Format("200601021504"),
		"{yyyymmddhhmmss}":      now.Format("20060102150405"),
		"yyyymmddhhmmss":        now.Format("20060102150405"),
		"{TimeWindowStartText}": request.TimeRange.From,
		"{TimeWindowEndText}":   request.TimeRange.To,
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name = strings.ReplaceAll(name, key, replacements[key])
	}
	return name
}

func excelReportType(report excelReportDefinition) string {
	if value, ok := report.Metadata["reportType"]; ok {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return report.ID
}

func sanitizeExcelReportFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "asyncq-report.xlsx"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_")
	name = replacer.Replace(name)
	return name
}

func excelReportContentDisposition(fileName string) string {
	quoted := strings.ReplaceAll(fileName, `"`, `_`)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, quoted, url.PathEscape(fileName))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
