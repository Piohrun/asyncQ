package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

const (
	defaultExcelReportMaxRows      = 100000
	defaultExcelReportMaxFileBytes = int64(50 * 1024 * 1024)
	defaultExcelReportTimeoutMs    = 60000
	excelReportDownloadTTL         = 5 * time.Minute
)

const (
	excelReportWriteModeRows   = "rows"
	excelReportWriteModeCells  = "cells"
	excelReportWriteModeStream = "stream"
)

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
	MaxRows           int                    `json:"maxRows,omitempty"`
	MaxFileBytes      int64                  `json:"maxFileBytes,omitempty"`
	GenerationTimeout int                    `json:"generationTimeoutMs,omitempty"`
	ExecutionMode     string                 `json:"executionMode,omitempty"`
	CompatibilityMode string                 `json:"compatibilityMode,omitempty"`
	QueryCacheMode    string                 `json:"queryCacheMode,omitempty"`
	QueryCacheKeyMode string                 `json:"queryCacheKeyMode,omitempty"`
	WriteMode         string                 `json:"writeMode,omitempty"`
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
	WriteMode                 string `json:"writeMode,omitempty"`
	Timeout                   int    `json:"timeOut,omitempty"`
	MaxDataPoints             int64  `json:"maxDataPoints,omitempty"`
	MaxRows                   int    `json:"maxRows,omitempty"`
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
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
	Report  string `json:"report,omitempty"`
	Binding string `json:"binding,omitempty"`
}

type excelReportValidationResponse struct {
	OK          bool                         `json:"ok"`
	ReportCount int                          `json:"reportCount"`
	Errors      []excelReportValidationIssue `json:"errors,omitempty"`
	Warnings    []excelReportValidationIssue `json:"warnings,omitempty"`
}

type excelReportValidationIssue struct {
	Report  string `json:"report,omitempty"`
	Binding string `json:"binding,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type excelReportGenerated struct {
	Body     []byte
	FileName string
}

type excelReportDownload struct {
	Body      []byte
	FileName  string
	ExpiresAt time.Time
}

type excelReportGenerateLinkResponse struct {
	OK           bool    `json:"ok"`
	Token        string  `json:"token,omitempty"`
	FileName     string  `json:"fileName,omitempty"`
	GenerationMs float64 `json:"generationMs,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type excelReportRunResult struct {
	Binding         excelReportBinding
	Frames          []*data.Frame
	SubmittedFrames []excelSubmittedFrame
}

type excelReportLimits struct {
	MaxRows      int
	MaxFileBytes int64
	Timeout      time.Duration
}

type excelReportWriteStats struct {
	Rows   int
	Cells  int
	Frames int
}

func (s *excelReportWriteStats) add(other excelReportWriteStats) {
	s.Rows += other.Rows
	s.Cells += other.Cells
	s.Frames += other.Frames
}

type excelReportWorkbookWriter struct {
	workbook      *excelize.File
	streamWriters map[string]*excelReportStreamWriter
	normalSheets  map[string]struct{}
}

type excelReportStreamWriter struct {
	writer  *excelize.StreamWriter
	lastRow int
}

func newExcelReportWorkbookWriter(workbook *excelize.File) *excelReportWorkbookWriter {
	return &excelReportWorkbookWriter{
		workbook:      workbook,
		streamWriters: map[string]*excelReportStreamWriter{},
		normalSheets:  map[string]struct{}{},
	}
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
	case "report/validate":
		validation := d.validateExcelReportConfiguration()
		status := http.StatusOK
		if !validation.OK {
			status = http.StatusBadRequest
		}
		return sendResourceJSON(sender, status, validation)
	case "report/generate":
		var body excelReportGenerateRequest
		if err := decodeExcelReportGenerateRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportResourceResponse{OK: false, Error: err.Error()})
		}
		generated, err := d.generateExcelReport(ctx, req.PluginContext, body)
		if err != nil {
			status, code := excelReportErrorStatus(err)
			log.DefaultLogger.Error("excel report generation failed", "datasourceUID", d.instanceUID, "reportID", body.ReportID, "status", status, "code", code, "error", err.Error())
			return sendResourceJSON(sender, status, excelReportResourceResponse{OK: false, Code: code, Report: body.ReportID, Error: err.Error()})
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
	case "report/generate-link":
		var body excelReportGenerateRequest
		if err := decodeExcelReportGenerateRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportGenerateLinkResponse{OK: false, Error: err.Error()})
		}
		started := time.Now()
		generated, err := d.generateExcelReport(ctx, req.PluginContext, body)
		if err != nil {
			status, code := excelReportErrorStatus(err)
			log.DefaultLogger.Error("excel report generation failed", "datasourceUID", d.instanceUID, "reportID", body.ReportID, "status", status, "code", code, "error", err.Error())
			return sendResourceJSON(sender, status, excelReportResourceResponse{OK: false, Code: code, Report: body.ReportID, Error: err.Error()})
		}
		token, err := d.storeExcelReportDownload(generated)
		if err != nil {
			return sendResourceJSON(sender, http.StatusInternalServerError, excelReportGenerateLinkResponse{OK: false, Error: err.Error()})
		}
		return sendResourceJSON(sender, http.StatusOK, excelReportGenerateLinkResponse{
			OK:           true,
			Token:        token,
			FileName:     generated.FileName,
			GenerationMs: diagnosticDurationMs(time.Since(started)),
		})
	case "report/download":
		token := excelReportDownloadToken(req)
		if token == "" {
			return sendResourceJSON(sender, http.StatusBadRequest, excelReportResourceResponse{OK: false, Error: "token is required"})
		}
		generated, ok := d.takeExcelReportDownload(token)
		if !ok {
			return sendResourceJSON(sender, http.StatusNotFound, excelReportResourceResponse{OK: false, Error: "report download token was not found or has expired"})
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

func (d *KdbDatasource) storeExcelReportDownload(generated excelReportGenerated) (string, error) {
	token, err := newExcelReportDownloadToken()
	if err != nil {
		return "", err
	}
	d.excelDownloadsMu.Lock()
	defer d.excelDownloadsMu.Unlock()
	if d.excelDownloads == nil {
		d.excelDownloads = map[string]excelReportDownload{}
	}
	now := time.Now()
	d.cleanupExpiredExcelReportDownloadsLocked(now)
	d.excelDownloads[token] = excelReportDownload{
		Body:      generated.Body,
		FileName:  generated.FileName,
		ExpiresAt: now.Add(excelReportDownloadTTL),
	}
	return token, nil
}

func (d *KdbDatasource) takeExcelReportDownload(token string) (excelReportGenerated, bool) {
	d.excelDownloadsMu.Lock()
	defer d.excelDownloadsMu.Unlock()
	now := time.Now()
	d.cleanupExpiredExcelReportDownloadsLocked(now)
	download, ok := d.excelDownloads[token]
	if !ok || now.After(download.ExpiresAt) {
		delete(d.excelDownloads, token)
		return excelReportGenerated{}, false
	}
	delete(d.excelDownloads, token)
	return excelReportGenerated{Body: download.Body, FileName: download.FileName}, true
}

func (d *KdbDatasource) cleanupExpiredExcelReportDownloadsLocked(now time.Time) {
	for token, download := range d.excelDownloads {
		if now.After(download.ExpiresAt) {
			delete(d.excelDownloads, token)
		}
	}
}

func newExcelReportDownloadToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("unable to create report download token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func excelReportDownloadToken(req *backend.CallResourceRequest) string {
	if req == nil {
		return ""
	}
	if parsed, err := url.Parse(req.URL); err == nil {
		if token := strings.TrimSpace(parsed.Query().Get("token")); token != "" {
			return token
		}
	}
	if strings.Contains(req.Path, "?") {
		if parsed, err := url.Parse(req.Path); err == nil {
			return strings.TrimSpace(parsed.Query().Get("token"))
		}
	}
	return ""
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

func excelReportErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	case strings.Contains(err.Error(), "unknown reportId"):
		return http.StatusNotFound, "unknown-report"
	case strings.Contains(err.Error(), "exceeds maxRows"):
		return http.StatusRequestEntityTooLarge, "row-limit"
	case strings.Contains(err.Error(), "exceeds maxFileBytes"):
		return http.StatusRequestEntityTooLarge, "file-size-limit"
	case strings.Contains(err.Error(), "templatePath"):
		return http.StatusBadRequest, "template-path"
	default:
		return http.StatusBadRequest, "report-generation"
	}
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
			WriteMode:         excelReportWriteModeStream,
			MaxDataPoints:     10000,
			IntervalMs:        1000,
			Bindings: []excelReportBinding{
				{
					ID:            "last-prices",
					RefID:         "A",
					QueryText:     ".demo.asyncq.lastPrices[]",
					Sheet:         "Summary",
					Cell:          "A1",
					IncludeHeader: &header,
				},
				{
					ID:            "latest-trades",
					RefID:         "B",
					QueryText:     ".demo.asyncq.latest 50",
					Sheet:         "Trades",
					Cell:          "A1",
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
		if report.MaxRows < 0 {
			return fmt.Errorf("excel report %q maxRows must be non-negative", id)
		}
		if report.MaxFileBytes < 0 {
			return fmt.Errorf("excel report %q maxFileBytes must be non-negative", id)
		}
		if report.GenerationTimeout < 0 {
			return fmt.Errorf("excel report %q generationTimeoutMs must be non-negative", id)
		}
		if _, err := normalizeExcelReportWriteMode(report.WriteMode); err != nil {
			return fmt.Errorf("excel report %q writeMode: %w", id, err)
		}
		if templatePath := strings.TrimSpace(report.TemplatePath); templatePath != "" && !strings.EqualFold(filepath.Ext(templatePath), ".xlsx") {
			return fmt.Errorf("excel report %q templatePath must point to an .xlsx file", id)
		}
		sheetWriteModes := map[string]string{}
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
			if strings.TrimSpace(binding.ClearRange) != "" {
				if _, _, err := parseExcelRange(binding.ClearRange); err != nil {
					return fmt.Errorf("excel report %q binding %d has invalid clearRange %q: %w", id, j, binding.ClearRange, err)
				}
			}
			if binding.MaxRows < 0 {
				return fmt.Errorf("excel report %q binding %d maxRows must be non-negative", id, j)
			}
			writeMode, err := excelReportBindingWriteMode(report, binding)
			if err != nil {
				return fmt.Errorf("excel report %q binding %d writeMode: %w", id, j, err)
			}
			sheetKey := strings.ToLower(strings.TrimSpace(binding.Sheet))
			if previousMode, ok := sheetWriteModes[sheetKey]; ok && previousMode != writeMode {
				return fmt.Errorf("excel report %q sheet %q mixes writeMode %q and %q; use one writeMode per sheet", id, binding.Sheet, previousMode, writeMode)
			}
			sheetWriteModes[sheetKey] = writeMode
		}
	}
	return nil
}

func (d *KdbDatasource) validateExcelReportConfiguration() excelReportValidationResponse {
	response := excelReportValidationResponse{OK: true}
	catalog, err := d.excelReportCatalog()
	if err != nil {
		response.OK = false
		response.Errors = append(response.Errors, excelReportValidationIssue{
			Field:   "excelReports",
			Message: err.Error(),
		})
		return response
	}
	response.ReportCount = len(catalog.Reports)
	if len(catalog.Reports) == 0 {
		response.Warnings = append(response.Warnings, excelReportValidationIssue{
			Field:   "excelReports",
			Message: "no Excel reports are configured",
		})
	}
	for _, report := range catalog.Reports {
		if strings.TrimSpace(report.TemplatePath) != "" {
			templatePath, err := d.resolveExcelReportTemplatePath(report.TemplatePath)
			if err != nil {
				response.Errors = append(response.Errors, excelReportValidationIssue{
					Report:  report.ID,
					Field:   "templatePath",
					Message: err.Error(),
				})
			} else if workbook, err := excelize.OpenFile(templatePath); err != nil {
				response.Errors = append(response.Errors, excelReportValidationIssue{
					Report:  report.ID,
					Field:   "templatePath",
					Message: fmt.Sprintf("unable to open template workbook: %v", err),
				})
			} else {
				_ = workbook.Close()
			}
		}
		limits := d.excelReportLimits(report)
		if limits.MaxRows <= 0 {
			response.Errors = append(response.Errors, excelReportValidationIssue{
				Report:  report.ID,
				Field:   "maxRows",
				Message: "maxRows must resolve to a positive value",
			})
		}
		if limits.MaxFileBytes <= 0 {
			response.Errors = append(response.Errors, excelReportValidationIssue{
				Report:  report.ID,
				Field:   "maxFileBytes",
				Message: "maxFileBytes must resolve to a positive value",
			})
		}
		if limits.Timeout <= 0 {
			response.Errors = append(response.Errors, excelReportValidationIssue{
				Report:  report.ID,
				Field:   "generationTimeoutMs",
				Message: "generationTimeoutMs must resolve to a positive value",
			})
		}
		for _, binding := range report.Bindings {
			bindingID := excelReportBindingID(binding)
			if maxRows := d.excelReportBindingMaxRows(report, binding); maxRows <= 0 {
				response.Errors = append(response.Errors, excelReportValidationIssue{
					Report:  report.ID,
					Binding: bindingID,
					Field:   "maxRows",
					Message: "binding maxRows must resolve to a positive value",
				})
			}
			if writeMode, err := excelReportBindingWriteMode(report, binding); err == nil && writeMode == excelReportWriteModeStream && strings.TrimSpace(binding.ClearRange) != "" {
				response.Warnings = append(response.Warnings, excelReportValidationIssue{
					Report:  report.ID,
					Binding: bindingID,
					Field:   "clearRange",
					Message: "clearRange is ignored when writeMode is stream because the target sheet cell data is rewritten",
				})
			}
		}
	}
	response.OK = len(response.Errors) == 0
	return response
}

func (d *KdbDatasource) generateExcelReport(ctx context.Context, pCtx backend.PluginContext, request excelReportGenerateRequest) (excelReportGenerated, error) {
	totalStarted := time.Now()
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
	limits := d.excelReportLimits(report)
	if limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limits.Timeout)
		defer cancel()
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

	queryStarted := time.Now()
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
		if err := d.validateExcelReportResultRows(report, binding, result); err != nil {
			return excelReportGenerated{}, err
		}
		results = append(results, result)
	}
	queryDuration := time.Since(queryStarted)

	openStarted := time.Now()
	workbook, err := d.openExcelReportWorkbook(report)
	if err != nil {
		return excelReportGenerated{}, err
	}
	defer func() { _ = workbook.Close() }()
	openDuration := time.Since(openStarted)

	writeStarted := time.Now()
	workbookWriter := newExcelReportWorkbookWriter(workbook)
	var writeStats excelReportWriteStats
	for _, result := range results {
		if err := ctx.Err(); err != nil {
			return excelReportGenerated{}, err
		}
		stats, err := workbookWriter.writeExcelReportBinding(report, result.Binding, result.Frames)
		if err != nil {
			return excelReportGenerated{}, err
		}
		writeStats.add(stats)
		stats, err = workbookWriter.writeSubmittedExcelReportBinding(report, result.Binding, result.SubmittedFrames)
		if err != nil {
			return excelReportGenerated{}, err
		}
		writeStats.add(stats)
	}
	if err := workbookWriter.flush(); err != nil {
		return excelReportGenerated{}, err
	}
	writeDuration := time.Since(writeStarted)
	setExcelReportActiveSheet(workbook, report)
	serializeStarted := time.Now()
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return excelReportGenerated{}, err
	}
	serializeDuration := time.Since(serializeStarted)
	if maxBytes := limits.MaxFileBytes; maxBytes > 0 && int64(buffer.Len()) > maxBytes {
		return excelReportGenerated{}, fmt.Errorf("generated workbook size %d bytes exceeds maxFileBytes %d for report %q", buffer.Len(), maxBytes, report.ID)
	}
	fileName := excelReportFileName(report, request, time.Now().UTC())
	log.DefaultLogger.Info(
		"excel report generated",
		"datasourceUID", d.instanceUID,
		"reportID", report.ID,
		"requestedFileName", request.FileName,
		"resolvedFileName", fileName,
		"frameCount", len(results),
		"writtenFrames", writeStats.Frames,
		"writtenRows", writeStats.Rows,
		"writtenCells", writeStats.Cells,
		"bytes", buffer.Len(),
		"maxFileBytes", limits.MaxFileBytes,
		"maxRows", limits.MaxRows,
		"queryMs", diagnosticDurationMs(queryDuration),
		"openWorkbookMs", diagnosticDurationMs(openDuration),
		"writeSheetsMs", diagnosticDurationMs(writeDuration),
		"serializeWorkbookMs", diagnosticDurationMs(serializeDuration),
		"totalMs", diagnosticDurationMs(time.Since(totalStarted)),
	)
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

func (d *KdbDatasource) excelReportLimits(report excelReportDefinition) excelReportLimits {
	maxRows := firstPositiveInt(report.MaxRows, d.ExcelReportMaxRows, defaultExcelReportMaxRows)
	maxFileBytes := firstPositiveInt64(report.MaxFileBytes, d.ExcelReportMaxFileBytes, defaultExcelReportMaxFileBytes)
	timeoutMs := firstPositiveInt(report.GenerationTimeout, d.ExcelReportTimeoutMs, defaultExcelReportTimeoutMs)
	return excelReportLimits{
		MaxRows:      maxRows,
		MaxFileBytes: maxFileBytes,
		Timeout:      time.Duration(timeoutMs) * time.Millisecond,
	}
}

func (d *KdbDatasource) excelReportBindingMaxRows(report excelReportDefinition, binding excelReportBinding) int {
	return firstPositiveInt(binding.MaxRows, report.MaxRows, d.ExcelReportMaxRows, defaultExcelReportMaxRows)
}

func (d *KdbDatasource) validateExcelReportResultRows(report excelReportDefinition, binding excelReportBinding, result excelReportRunResult) error {
	maxRows := d.excelReportBindingMaxRows(report, binding)
	if maxRows <= 0 {
		return fmt.Errorf("binding %q maxRows must resolve to a positive value", excelReportBindingID(binding))
	}
	rowCount := excelReportRunResultRows(result)
	if rowCount > maxRows {
		return fmt.Errorf("binding %q row count %d exceeds maxRows %d for report %q", excelReportBindingID(binding), rowCount, maxRows, report.ID)
	}
	return nil
}

func excelReportRunResultRows(result excelReportRunResult) int {
	rows := 0
	for _, frame := range result.Frames {
		if frame != nil {
			rows += frame.Rows()
		}
	}
	for _, frame := range result.SubmittedFrames {
		rows += submittedFrameRows(frame)
	}
	return rows
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
	defaultMaxDataPoints := int64(d.excelReportBindingMaxRows(report, binding))

	query := backend.DataQuery{
		RefID:         refID,
		QueryType:     "excel-report",
		MaxDataPoints: firstPositiveInt64(binding.MaxDataPoints, report.MaxDataPoints, defaultMaxDataPoints),
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

func (d *KdbDatasource) openExcelReportWorkbook(report excelReportDefinition) (*excelize.File, error) {
	templatePath := strings.TrimSpace(report.TemplatePath)
	if templatePath == "" {
		return excelize.NewFile(), nil
	}
	expandedPath, err := d.resolveExcelReportTemplatePath(templatePath)
	if err != nil {
		return nil, err
	}
	workbook, err := excelize.OpenFile(expandedPath)
	if err != nil {
		return nil, fmt.Errorf("unable to open report template %q: %w", templatePath, err)
	}
	return workbook, nil
}

func (d *KdbDatasource) resolveExcelReportTemplatePath(templatePath string) (string, error) {
	templatePath = strings.TrimSpace(templatePath)
	if templatePath == "" {
		return "", fmt.Errorf("templatePath is empty")
	}
	if !strings.EqualFold(filepath.Ext(templatePath), ".xlsx") {
		return "", fmt.Errorf("templatePath %q must point to an .xlsx file", templatePath)
	}
	expandedPath := os.ExpandEnv(templatePath)
	if !filepath.IsAbs(expandedPath) {
		return "", fmt.Errorf("templatePath %q must be absolute after environment expansion", templatePath)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(expandedPath))
	if err != nil {
		return "", fmt.Errorf("unable to resolve templatePath %q: %w", templatePath, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("unable to stat templatePath %q: %w", templatePath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("templatePath %q points to a directory, expected .xlsx file", templatePath)
	}
	allowedDirs, err := d.resolvedExcelReportTemplateDirs()
	if err != nil {
		return "", err
	}
	if len(allowedDirs) == 0 {
		return "", fmt.Errorf("templatePath %q requires excelReportTemplateDirs or ASYNCQ_EXCEL_TEMPLATE_DIRS allowlist", templatePath)
	}
	for _, dir := range allowedDirs {
		if pathWithinDir(target, dir) {
			return target, nil
		}
	}
	return "", fmt.Errorf("templatePath %q resolves outside configured template directories", templatePath)
}

func (d *KdbDatasource) resolvedExcelReportTemplateDirs() ([]string, error) {
	raw := firstNonEmpty(d.ExcelReportTemplateDirs, os.Getenv("ASYNCQ_EXCEL_TEMPLATE_DIRS"), os.Getenv("ASYNCQ_EXCEL_TEMPLATE_DIR"))
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	dirs := make([]string, 0, len(parts))
	for _, part := range parts {
		dir := os.ExpandEnv(strings.TrimSpace(part))
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("excelReportTemplateDirs entry %q must be absolute after environment expansion", part)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(dir))
		if err != nil {
			return nil, fmt.Errorf("unable to resolve excelReportTemplateDirs entry %q: %w", part, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("unable to stat excelReportTemplateDirs entry %q: %w", part, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("excelReportTemplateDirs entry %q is not a directory", part)
		}
		dirs = append(dirs, resolved)
	}
	return dirs, nil
}

func pathWithinDir(path string, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func writeExcelReportBinding(workbook *excelize.File, report excelReportDefinition, binding excelReportBinding, frames []*data.Frame) (excelReportWriteStats, error) {
	writer := newExcelReportWorkbookWriter(workbook)
	stats, err := writer.writeExcelReportBinding(report, binding, frames)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	if err := writer.flush(); err != nil {
		return excelReportWriteStats{}, err
	}
	return stats, nil
}

func (w *excelReportWorkbookWriter) writeExcelReportBinding(report excelReportDefinition, binding excelReportBinding, frames []*data.Frame) (excelReportWriteStats, error) {
	if len(frames) == 0 {
		return excelReportWriteStats{}, nil
	}
	sheet := strings.TrimSpace(binding.Sheet)
	cell := strings.TrimSpace(binding.Cell)
	if sheet == "" || cell == "" {
		return excelReportWriteStats{}, fmt.Errorf("report binding requires sheet and cell")
	}
	if err := ensureExcelReportSheet(w.workbook, sheet); err != nil {
		return excelReportWriteStats{}, err
	}
	col, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	includeHeader := true
	if binding.IncludeHeader != nil {
		includeHeader = *binding.IncludeHeader
	}
	writeMode, err := excelReportBindingWriteMode(report, binding)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	if writeMode == excelReportWriteModeStream {
		return w.writeExcelReportFramesStream(sheet, col, row, frames, includeHeader)
	}
	if err := w.markNormalSheet(sheet); err != nil {
		return excelReportWriteStats{}, err
	}
	if strings.TrimSpace(binding.ClearRange) != "" {
		if err := clearExcelReportRange(w.workbook, sheet, binding.ClearRange); err != nil {
			return excelReportWriteStats{}, err
		}
	}
	cursor := row
	var stats excelReportWriteStats
	for frameIndex, frame := range frames {
		if frame == nil || len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, frameStats, err := writeExcelReportFrame(w.workbook, sheet, col, cursor, frame, includeHeader, writeMode)
		if err != nil {
			return excelReportWriteStats{}, err
		}
		stats.add(frameStats)
		cursor += written
	}
	return stats, nil
}

func writeSubmittedExcelReportBinding(workbook *excelize.File, report excelReportDefinition, binding excelReportBinding, frames []excelSubmittedFrame) (excelReportWriteStats, error) {
	writer := newExcelReportWorkbookWriter(workbook)
	stats, err := writer.writeSubmittedExcelReportBinding(report, binding, frames)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	if err := writer.flush(); err != nil {
		return excelReportWriteStats{}, err
	}
	return stats, nil
}

func (w *excelReportWorkbookWriter) writeSubmittedExcelReportBinding(report excelReportDefinition, binding excelReportBinding, frames []excelSubmittedFrame) (excelReportWriteStats, error) {
	if len(frames) == 0 {
		return excelReportWriteStats{}, nil
	}
	sheet := strings.TrimSpace(binding.Sheet)
	cell := strings.TrimSpace(binding.Cell)
	if sheet == "" || cell == "" {
		return excelReportWriteStats{}, fmt.Errorf("report binding requires sheet and cell")
	}
	if err := ensureExcelReportSheet(w.workbook, sheet); err != nil {
		return excelReportWriteStats{}, err
	}
	col, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	includeHeader := true
	if binding.IncludeHeader != nil {
		includeHeader = *binding.IncludeHeader
	}
	writeMode, err := excelReportBindingWriteMode(report, binding)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	if writeMode == excelReportWriteModeStream {
		return w.writeSubmittedExcelReportFramesStream(sheet, col, row, frames, includeHeader)
	}
	if err := w.markNormalSheet(sheet); err != nil {
		return excelReportWriteStats{}, err
	}
	if strings.TrimSpace(binding.ClearRange) != "" {
		if err := clearExcelReportRange(w.workbook, sheet, binding.ClearRange); err != nil {
			return excelReportWriteStats{}, err
		}
	}
	cursor := row
	var stats excelReportWriteStats
	for frameIndex, frame := range frames {
		if len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, frameStats, err := writeSubmittedExcelReportFrame(w.workbook, sheet, col, cursor, frame, includeHeader, writeMode)
		if err != nil {
			return excelReportWriteStats{}, err
		}
		stats.add(frameStats)
		cursor += written
	}
	return stats, nil
}

func (w *excelReportWorkbookWriter) streamWriter(sheet string) (*excelReportStreamWriter, error) {
	if _, ok := w.normalSheets[sheet]; ok {
		return nil, fmt.Errorf("sheet %q mixes stream writeMode with normal Excel writes; use one writeMode per sheet", sheet)
	}
	if writer, ok := w.streamWriters[sheet]; ok {
		return writer, nil
	}
	stream, err := w.workbook.NewStreamWriter(sheet)
	if err != nil {
		return nil, err
	}
	writer := &excelReportStreamWriter{writer: stream}
	w.streamWriters[sheet] = writer
	return writer, nil
}

func (w *excelReportWorkbookWriter) markNormalSheet(sheet string) error {
	if _, ok := w.streamWriters[sheet]; ok {
		return fmt.Errorf("sheet %q mixes normal Excel writes with stream writeMode; use one writeMode per sheet", sheet)
	}
	w.normalSheets[sheet] = struct{}{}
	return nil
}

func (w *excelReportWorkbookWriter) flush() error {
	sheets := make([]string, 0, len(w.streamWriters))
	for sheet := range w.streamWriters {
		sheets = append(sheets, sheet)
	}
	sort.Strings(sheets)
	for _, sheet := range sheets {
		if err := w.streamWriters[sheet].writer.Flush(); err != nil {
			return fmt.Errorf("flush stream writer for sheet %q: %w", sheet, err)
		}
	}
	return nil
}

func excelReportBindingWriteMode(report excelReportDefinition, binding excelReportBinding) (string, error) {
	return normalizeExcelReportWriteMode(firstNonEmpty(binding.WriteMode, report.WriteMode))
}

func normalizeExcelReportWriteMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto", "cell", "cells", "legacy":
		return excelReportWriteModeCells, nil
	case "row", "rows", "bulk":
		return excelReportWriteModeRows, nil
	case "stream", "streaming":
		return excelReportWriteModeStream, nil
	default:
		return "", fmt.Errorf("unsupported value %q; use rows, cells, or stream", mode)
	}
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
	width := endCol - startCol + 1
	emptyRow := make([]interface{}, width)
	for row := startRow; row <= endRow; row++ {
		cell, err := excelize.CoordinatesToCellName(startCol, row)
		if err != nil {
			return err
		}
		if err := workbook.SetSheetRow(sheet, cell, &emptyRow); err != nil {
			return err
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

func setExcelReportRow(workbook *excelize.File, sheet string, startCol int, row int, values []interface{}, writeMode string) error {
	cell, err := excelize.CoordinatesToCellName(startCol, row)
	if err != nil {
		return err
	}
	switch writeMode {
	case excelReportWriteModeCells:
		for colOffset, value := range values {
			cell, err := excelize.CoordinatesToCellName(startCol+colOffset, row)
			if err != nil {
				return err
			}
			if err := workbook.SetCellValue(sheet, cell, value); err != nil {
				return err
			}
		}
		return nil
	default:
		return workbook.SetSheetRow(sheet, cell, &values)
	}
}

func (w *excelReportWorkbookWriter) writeExcelReportFramesStream(workbookSheet string, startCol int, startRow int, frames []*data.Frame, includeHeader bool) (excelReportWriteStats, error) {
	stream, err := w.streamWriter(workbookSheet)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	cursor := startRow
	var stats excelReportWriteStats
	for frameIndex, frame := range frames {
		if frame == nil || len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, frameStats, err := writeExcelReportFrameStream(stream, startCol, cursor, frame, includeHeader)
		if err != nil {
			return excelReportWriteStats{}, err
		}
		stats.add(frameStats)
		cursor += written
	}
	return stats, nil
}

func (w *excelReportWorkbookWriter) writeSubmittedExcelReportFramesStream(workbookSheet string, startCol int, startRow int, frames []excelSubmittedFrame, includeHeader bool) (excelReportWriteStats, error) {
	stream, err := w.streamWriter(workbookSheet)
	if err != nil {
		return excelReportWriteStats{}, err
	}
	cursor := startRow
	var stats excelReportWriteStats
	for frameIndex, frame := range frames {
		if len(frame.Fields) == 0 {
			continue
		}
		if frameIndex > 0 {
			cursor++
		}
		written, frameStats, err := writeSubmittedExcelReportFrameStream(stream, startCol, cursor, frame, includeHeader)
		if err != nil {
			return excelReportWriteStats{}, err
		}
		stats.add(frameStats)
		cursor += written
	}
	return stats, nil
}

func writeExcelReportFrameStream(stream *excelReportStreamWriter, startCol int, startRow int, frame *data.Frame, includeHeader bool) (int, excelReportWriteStats, error) {
	row := startRow
	stats := excelReportWriteStats{Frames: 1}
	if includeHeader {
		headers := make([]interface{}, len(frame.Fields))
		for colOffset, field := range frame.Fields {
			headers[colOffset] = field.Name
		}
		if err := stream.setRow(startCol, row, headers); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(headers)
		row++
	}
	length := frame.Rows()
	values := make([]interface{}, len(frame.Fields))
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			values[colOffset] = excelReportCellValue(field, frameRow)
		}
		if err := stream.setRow(startCol, row, values); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(values)
		row++
	}
	return row - startRow, stats, nil
}

func writeSubmittedExcelReportFrameStream(stream *excelReportStreamWriter, startCol int, startRow int, frame excelSubmittedFrame, includeHeader bool) (int, excelReportWriteStats, error) {
	row := startRow
	stats := excelReportWriteStats{Frames: 1}
	if includeHeader {
		headers := make([]interface{}, len(frame.Fields))
		for colOffset, field := range frame.Fields {
			headers[colOffset] = field.Name
		}
		if err := stream.setRow(startCol, row, headers); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(headers)
		row++
	}
	length := submittedFrameRows(frame)
	values := make([]interface{}, len(frame.Fields))
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			values[colOffset] = excelSubmittedCellValue(field, frameRow)
		}
		if err := stream.setRow(startCol, row, values); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(values)
		row++
	}
	return row - startRow, stats, nil
}

func (s *excelReportStreamWriter) setRow(startCol int, row int, values []interface{}) error {
	if row <= s.lastRow {
		return fmt.Errorf("stream writeMode requires ascending row order; attempted row %d after row %d", row, s.lastRow)
	}
	cell, err := excelize.CoordinatesToCellName(startCol, row)
	if err != nil {
		return err
	}
	if err := s.writer.SetRow(cell, values); err != nil {
		return err
	}
	s.lastRow = row
	return nil
}

func writeExcelReportFrame(workbook *excelize.File, sheet string, startCol int, startRow int, frame *data.Frame, includeHeader bool, writeMode string) (int, excelReportWriteStats, error) {
	row := startRow
	stats := excelReportWriteStats{Frames: 1}
	if includeHeader {
		headers := make([]interface{}, len(frame.Fields))
		for colOffset, field := range frame.Fields {
			headers[colOffset] = field.Name
		}
		if err := setExcelReportRow(workbook, sheet, startCol, row, headers, writeMode); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(headers)
		row++
	}
	length := frame.Rows()
	values := make([]interface{}, len(frame.Fields))
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			values[colOffset] = excelReportCellValue(field, frameRow)
		}
		if err := setExcelReportRow(workbook, sheet, startCol, row, values, writeMode); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(values)
		row++
	}
	return row - startRow, stats, nil
}

func writeSubmittedExcelReportFrame(workbook *excelize.File, sheet string, startCol int, startRow int, frame excelSubmittedFrame, includeHeader bool, writeMode string) (int, excelReportWriteStats, error) {
	row := startRow
	stats := excelReportWriteStats{Frames: 1}
	if includeHeader {
		headers := make([]interface{}, len(frame.Fields))
		for colOffset, field := range frame.Fields {
			headers[colOffset] = field.Name
		}
		if err := setExcelReportRow(workbook, sheet, startCol, row, headers, writeMode); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(headers)
		row++
	}
	length := submittedFrameRows(frame)
	values := make([]interface{}, len(frame.Fields))
	for frameRow := 0; frameRow < length; frameRow++ {
		for colOffset, field := range frame.Fields {
			values[colOffset] = excelSubmittedCellValue(field, frameRow)
		}
		if err := setExcelReportRow(workbook, sheet, startCol, row, values, writeMode); err != nil {
			return 0, excelReportWriteStats{}, err
		}
		stats.Rows++
		stats.Cells += len(values)
		row++
	}
	return row - startRow, stats, nil
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
