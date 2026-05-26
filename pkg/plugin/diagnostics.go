package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
)

const diagnosticHashLength = 16

func (d *KdbDatasource) diagnosticsEnabled() bool {
	return d != nil && d.DiagnosticsEnabled
}

func (d *KdbDatasource) logDiagnostics(message string, fields ...interface{}) {
	if !d.diagnosticsEnabled() {
		return
	}
	log.DefaultLogger.Info("AsyncQ diagnostics: "+message, fields...)
}

func (d *KdbDatasource) logDiagnosticError(message string, fields ...interface{}) {
	log.DefaultLogger.Error("AsyncQ: "+message, fields...)
}

func (d *KdbDatasource) diagnosticQueryFields(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, requestID string) []interface{} {
	originalQuery := model.OriginalQueryText
	if originalQuery == "" {
		originalQuery = model.QueryText
	}
	syncMaxConnections := 0
	if d != nil {
		syncMaxConnections = d.SyncMaxConnections
	}
	fields := []interface{}{
		"requestID", requestID,
		"refID", query.RefID,
		"queryType", query.QueryType,
		"executionMode", model.ExecutionMode,
		"compatibilityMode", model.CompatibilityMode,
		"queryHash", diagnosticHash(model.QueryText),
		"originalQueryHash", diagnosticHash(originalQuery),
		"queryLen", len(model.QueryText),
		"originalQueryLen", len(originalQuery),
		"timeoutMs", model.Timeout,
		"syncMaxConnections", syncMaxConnections,
		"pollIntervalMs", model.PollIntervalMs,
		"maxStreamRows", model.MaxStreamRows,
		"streamRetentionMs", model.StreamRetentionMs,
		"maxDataPoints", query.MaxDataPoints,
		"intervalMs", int64(query.Interval / time.Millisecond),
		"timeFrom", diagnosticTime(query.TimeRange.From),
		"timeTo", diagnosticTime(query.TimeRange.To),
		"useTimeColumn", model.UseTimeColumn,
		"timeColumn", model.TimeColumn,
		"includeKeyColumns", model.IncludeKeyColumns,
		"deferredWrapperHash", diagnosticHash(model.DeferredQueryWrapper),
		"panopticonWrapperHash", diagnosticHash(model.PanopticonQueryWrapper),
		"panopticonRequestFunctionHash", diagnosticHash(model.PanopticonRequestFunction),
	}
	if pCtx.OrgID != 0 {
		fields = append(fields, "orgID", pCtx.OrgID)
	}
	if pCtx.PluginID != "" {
		fields = append(fields, "pluginID", pCtx.PluginID)
	}
	if pCtx.PluginVersion != "" {
		fields = append(fields, "pluginVersion", pCtx.PluginVersion)
	}
	if pCtx.DataSourceInstanceSettings != nil {
		fields = append(fields,
			"datasourceUID", pCtx.DataSourceInstanceSettings.UID,
			"datasourceName", pCtx.DataSourceInstanceSettings.Name,
		)
	}
	if d != nil && d.DiagnosticsEnabled && d.DiagnosticsLogQueryText {
		fields = append(fields,
			"queryText", model.QueryText,
			"originalQueryText", originalQuery,
			"deferredWrapper", model.DeferredQueryWrapper,
			"panopticonWrapper", model.PanopticonQueryWrapper,
			"panopticonRequestFunction", model.PanopticonRequestFunction,
		)
	}
	return fields
}

func appendDiagnosticError(fields []interface{}, err error) []interface{} {
	if err == nil {
		return fields
	}
	return append(fields, "error", err.Error())
}

func appendDiagnosticKdbObject(fields []interface{}, name string, obj *kdb.K) []interface{} {
	return append(fields, name, describeKdbObject(obj))
}

func appendDiagnosticFrames(fields []interface{}, frames []*data.Frame) []interface{} {
	return append(fields, "frameCount", len(frames), "frameSchemas", diagnosticFrameSchemas(frames))
}

func (d *KdbDatasource) appendDiagnosticAsyncStatus(fields []interface{}, status asyncQStatus) []interface{} {
	fields = append(fields,
		"status", status.Status,
		"progress", status.Progress,
		"message", status.Message,
		"errorText", status.Error,
		"errorClass", status.ErrorClass,
		"worker", status.Worker,
		"started", status.Started,
		"finished", status.Finished,
		"resultType", status.ResultType,
	)
	if status.StackTrace != "" {
		fields = append(fields,
			"qStackTraceHash", diagnosticHash(status.StackTrace),
			"qStackTraceLen", len(status.StackTrace),
		)
		if d != nil && d.DiagnosticsEnabled && d.DiagnosticsLogQueryText {
			fields = append(fields, "qStackTrace", status.StackTrace)
		}
	}
	return fields
}

func diagnosticHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if len(encoded) <= diagnosticHashLength {
		return encoded
	}
	return encoded[:diagnosticHashLength]
}

func diagnosticFrameSchemas(frames []*data.Frame) []string {
	schemas := make([]string, 0, len(frames))
	for _, frame := range frames {
		if frame == nil {
			schemas = append(schemas, "nil")
			continue
		}
		schemas = append(schemas, frameSchema(frame))
	}
	return schemas
}

func diagnosticTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func syncDiagnosticRequestID(req *backend.QueryDataRequest, query backend.DataQuery, index int) string {
	base := ""
	if req != nil {
		for _, key := range []string{"X-Request-Id", "X-Request-ID", "X-Grafana-Request-Id", "X-Grafana-Request-ID"} {
			if value := req.GetHTTPHeader(key); strings.TrimSpace(value) != "" {
				base = value
				break
			}
			if value := req.Headers[key]; strings.TrimSpace(value) != "" {
				base = value
				break
			}
		}
	}
	refID := query.RefID
	if refID == "" {
		refID = fmt.Sprintf("query-%d", index)
	}
	if base == "" {
		return fmt.Sprintf("sync-%s-%d", diagnosticIDPart(refID), time.Now().UnixNano())
	}
	return fmt.Sprintf("%s/%s", diagnosticIDPart(base), diagnosticIDPart(refID))
}

func diagnosticIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.' || r == '/' || r == '=':
			return r
		default:
			return '-'
		}
	}, value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}
