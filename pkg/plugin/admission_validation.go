package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

const (
	maxDatasourceSettingsJSONBytes = 4 << 20
	maxQueryJSONBytes              = 1 << 20
	maxQueryTextBytes              = 1 << 20
	maxRefIDBytes                  = 64
	maxQueryCount                  = 128
	maxJSONTopLevelMembers         = 256
	maxJSONStructuredValues        = 16384
	maxJSONDepth                   = 32
	maxJSONMemberNameBytes         = 256

	maxHostBytes                = 1024
	maxTimeoutTextBytes         = 32
	maxCodeTextBytes            = 64 << 10
	maxMappingTextBytes         = 4 << 10
	maxFieldNameBytes           = 256
	maxStreamNameBytes          = 256
	maxPathTextBytes            = 4 << 10
	maxTemplateDirsTextBytes    = 64 << 10
	maxExcelReportsJSONBytes    = 2 << 20
	maxQueryTypeBytes           = 128
	maxPluginContextStringBytes = 4 << 10

	maxSyncConnections        = 64
	maxAsyncJobs              = 256
	maxCacheTTLSeconds        = 365 * 24 * 60 * 60
	maxCacheTimeBucketSeconds = 24 * 60 * 60
	maxCacheEntries           = 65536
	maxDiskCacheEntries       = 1000000
	maxDiskCacheBytes         = int64(1) << 40
	maxExcelReportRows        = 1048576
	maxExcelReportFileBytes   = int64(1) << 30
	maxExcelReportTimeoutMs   = 3600000
	maxExcelReportCount       = 128
	maxExcelBindingsPerReport = 128
	maxExcelBindingCount      = 1024

	maxQueryTimeoutMs       = 3600000
	maxQueryKeyBytes        = 256
	minPollIntervalMs       = 100
	maxPollIntervalMs       = 300000
	maxStreamRows           = 1000000
	maxStreamRetentionMs    = 7 * 24 * 60 * 60 * 1000
	maxQueryCacheTTLSeconds = maxCacheTTLSeconds
	maxQueryMaxDataPoints   = int64(10000000)
	maxQueryInterval        = 365 * 24 * time.Hour
	maxQueryCachingTTL      = int64(maxQueryInterval / time.Millisecond)
	maxQueryDatasourceID    = int64(1<<53 - 1)
	maxLivePathBytes        = len("stream/") + 128
	maxLiveIDBytes          = 128
	maxResourcePathBytes    = 256
	maxResourceURLBytes     = 8 << 10
	maxResourceJSONBytes    = 16 << 20
	maxResourceResponseJSON = 32 << 20
	maxAsyncStatusEvents    = 256
)

type jsonValueKind uint8

const (
	jsonStringValue jsonValueKind = iota
	jsonBoolValue
	jsonIntegerValue
	jsonObjectValue
)

type jsonFieldSpec struct {
	kind           jsonValueKind
	maxStringBytes int
	allowNull      bool
}

type admittedDataQuery struct {
	query     backend.DataQuery
	model     QueryModel
	decodeMs  float64
	prepareMs float64
}

type admittedLiveQuery struct {
	liveReq liveQueryRequest
	query   backend.DataQuery
	model   QueryModel
	family  string
	id      string
}

var datasourceJSONFieldSpecs = map[string]jsonFieldSpec{
	"host":                        {kind: jsonStringValue, maxStringBytes: maxHostBytes},
	"port":                        {kind: jsonIntegerValue},
	"timeout":                     {kind: jsonStringValue, maxStringBytes: maxTimeoutTextBytes},
	"withTLS":                     {kind: jsonBoolValue},
	"skipVerifyTLS":               {kind: jsonBoolValue},
	"withCACert":                  {kind: jsonBoolValue},
	"enableAsync":                 {kind: jsonBoolValue},
	"enableStreaming":             {kind: jsonBoolValue},
	"executionMode":               {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"compatibilityMode":           {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"deferredQueryWrapper":        {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"panopticonQueryWrapper":      {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"panopticonRequestFunction":   {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncSubmit":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncStatus":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncResult":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncCancel":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncRequestMode":      {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"legacyAsyncJobIDPath":        {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncStatusPath":       {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncProgressPath":     {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncMessagePath":      {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncErrorPath":        {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncPayloadPath":      {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncQueuedValues":     {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncRunningValues":    {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncDoneValues":       {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncErrorValues":      {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncCancelledValues":  {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"asyncMaxJobs":                {kind: jsonIntegerValue},
	"syncMaxConnections":          {kind: jsonIntegerValue},
	"queryCacheEnabled":           {kind: jsonBoolValue},
	"queryCacheTTLSeconds":        {kind: jsonIntegerValue},
	"queryCacheMaxEntries":        {kind: jsonIntegerValue},
	"queryCacheTimeBucketSeconds": {kind: jsonIntegerValue},
	"queryCacheStaleTTLSeconds":   {kind: jsonIntegerValue},
	"queryCacheKeyMode":           {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"queryCacheDiskEnabled":       {kind: jsonBoolValue},
	"queryCacheDiskPath":          {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"queryCacheDiskMaxBytes":      {kind: jsonIntegerValue},
	"queryCacheDiskMaxEntries":    {kind: jsonIntegerValue},
	"queryCacheControlEnabled":    {kind: jsonBoolValue},
	"diagnosticsEnabled":          {kind: jsonBoolValue},
	"diagnosticsLogQueryText":     {kind: jsonBoolValue},
	"excelReports":                {kind: jsonStringValue, maxStringBytes: maxExcelReportsJSONBytes},
	"excelReportTemplateDirs":     {kind: jsonStringValue, maxStringBytes: maxTemplateDirsTextBytes},
	"excelReportMaxRows":          {kind: jsonIntegerValue},
	"excelReportMaxFileBytes":     {kind: jsonIntegerValue},
	"excelReportTimeoutMs":        {kind: jsonIntegerValue},
}

var (
	qTimestampEpoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	minQTimestamp   = qTimestampEpoch.Add(-time.Duration(math.MaxInt64 - 1))
	maxQTimestamp   = qTimestampEpoch.Add(time.Duration(math.MaxInt64 - 1))
)

var commonQueryJSONFieldSpecs = map[string]jsonFieldSpec{
	"queryText":                   {kind: jsonStringValue, maxStringBytes: maxQueryTextBytes},
	"timeOut":                     {kind: jsonIntegerValue},
	"useTimeColumn":               {kind: jsonBoolValue},
	"timeColumn":                  {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"includeKeyColumns":           {kind: jsonBoolValue},
	"executionMode":               {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"compatibilityMode":           {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"deferredQueryWrapper":        {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"panopticonQueryWrapper":      {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"panopticonRequestFunction":   {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncSubmit":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncStatus":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncResult":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncCancel":           {kind: jsonStringValue, maxStringBytes: maxCodeTextBytes},
	"legacyAsyncRequestMode":      {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"legacyAsyncJobIDPath":        {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncStatusPath":       {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncProgressPath":     {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncMessagePath":      {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncErrorPath":        {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncPayloadPath":      {kind: jsonStringValue, maxStringBytes: maxPathTextBytes},
	"legacyAsyncQueuedValues":     {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncRunningValues":    {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncDoneValues":       {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncErrorValues":      {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"legacyAsyncCancelledValues":  {kind: jsonStringValue, maxStringBytes: maxMappingTextBytes},
	"streamName":                  {kind: jsonStringValue, maxStringBytes: maxStreamNameBytes},
	"pollIntervalMs":              {kind: jsonIntegerValue},
	"maxStreamRows":               {kind: jsonIntegerValue},
	"streamRetentionMs":           {kind: jsonIntegerValue},
	"queryCacheMode":              {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"queryCacheKeyMode":           {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"queryCacheTTLSeconds":        {kind: jsonIntegerValue},
	"queryCacheStaleTTLSeconds":   {kind: jsonIntegerValue},
	"queryCacheTimeBucketSeconds": {kind: jsonIntegerValue},

	// Grafana query-envelope metadata proven by the frontend model and demo
	// dashboard fixtures. These values are validated but decoded by the SDK.
	"datasource": {kind: jsonObjectValue, allowNull: true},
	"key":        {kind: jsonStringValue, maxStringBytes: maxQueryKeyBytes},
	"refId":      {kind: jsonStringValue, maxStringBytes: maxRefIDBytes},
	"hide":       {kind: jsonBoolValue},
	"queryType":  {kind: jsonStringValue, maxStringBytes: maxQueryTypeBytes},
}

var queryJSONFieldSpecs = func() map[string]jsonFieldSpec {
	specs := make(map[string]jsonFieldSpec, len(commonQueryJSONFieldSpecs)+4)
	for key, spec := range commonQueryJSONFieldSpecs {
		specs[key] = spec
	}
	// DataSourceWithBackend adds exactly these target-envelope members before
	// posting to /api/ds/query. They stay out of QueryModel.
	specs["datasourceId"] = jsonFieldSpec{kind: jsonIntegerValue}
	specs["intervalMs"] = jsonFieldSpec{kind: jsonIntegerValue}
	specs["maxDataPoints"] = jsonFieldSpec{kind: jsonIntegerValue}
	specs["queryCachingTTL"] = jsonFieldSpec{kind: jsonIntegerValue, allowNull: true}
	return specs
}()

var liveQueryJSONFieldSpecs = func() map[string]jsonFieldSpec {
	specs := make(map[string]jsonFieldSpec, len(commonQueryJSONFieldSpecs)+3)
	for key, spec := range commonQueryJSONFieldSpecs {
		specs[key] = spec
	}
	specs["maxDataPoints"] = jsonFieldSpec{kind: jsonIntegerValue}
	specs["intervalMs"] = jsonFieldSpec{kind: jsonIntegerValue}
	specs["timeRange"] = jsonFieldSpec{kind: jsonObjectValue}
	return specs
}()

var asyncRunAndWaitJSONFieldSpecs = func() map[string]jsonFieldSpec {
	specs := make(map[string]jsonFieldSpec, len(liveQueryJSONFieldSpecs)+1)
	for key, spec := range liveQueryJSONFieldSpecs {
		specs[key] = spec
	}
	specs["requestId"] = jsonFieldSpec{kind: jsonStringValue, maxStringBytes: maxLiveIDBytes}
	return specs
}()

var liveTimeRangeJSONFieldSpecs = map[string]jsonFieldSpec{
	"from": {kind: jsonStringValue, maxStringBytes: maxTimeoutTextBytes * 2},
	"to":   {kind: jsonStringValue, maxStringBytes: maxTimeoutTextBytes * 2},
}

var queryDatasourceJSONFieldSpecs = map[string]jsonFieldSpec{
	"type":       {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"uid":        {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
	"apiVersion": {kind: jsonStringValue, maxStringBytes: maxFieldNameBytes},
}

func decodeDatasourceSettings(raw []byte, target *KdbDatasource) (map[string]json.RawMessage, error) {
	fields, err := scanTopLevelJSONObject(raw, maxDatasourceSettingsJSONBytes, datasourceJSONFieldSpecs, true, "datasource settings")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("datasource settings could not be decoded")
	}
	if err := validateDatasourceConfiguration(target, fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func scanTopLevelJSONObject(raw []byte, maxBytes int, specs map[string]jsonFieldSpec, allowUnknown bool, label string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", label, maxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%s must contain valid UTF-8", label)
	}
	if err := validateBoundedJSONStructure(raw, label); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%s is malformed JSON", label)
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("%s must be a non-null JSON object", label)
	}

	fields := make(map[string]json.RawMessage, len(specs))
	seen := make(map[string]struct{}, min(len(specs), maxJSONTopLevelMembers))
	memberCount := 0
	for decoder.More() {
		memberCount++
		if memberCount > maxJSONTopLevelMembers {
			return nil, fmt.Errorf("%s contains more than %d top-level members", label, maxJSONTopLevelMembers)
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s is malformed JSON", label)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s contains an invalid object key", label)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s contains a duplicate top-level key", label)
		}
		seen[key] = struct{}{}

		spec, known := specs[key]
		if !known {
			// Alias work is bounded by the fixed schema size and the hard
			// top-level member cap. EqualFold intentionally catches Unicode
			// simple-fold aliases such as the long-s spelling of "host".
			for canonical := range specs {
				if strings.EqualFold(key, canonical) {
					return nil, fmt.Errorf("%s field %q must use its canonical spelling", label, canonical)
				}
			}
		}
		if !known && !allowUnknown {
			return nil, fmt.Errorf("%s contains an unknown field", label)
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s is malformed JSON", label)
		}
		if known {
			if err := validateJSONFieldValue(value, spec, key, label); err != nil {
				return nil, err
			}
			fields[key] = append(json.RawMessage(nil), value...)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%s is malformed JSON", label)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must not contain trailing JSON", label)
	}
	return fields, nil
}

type jsonStructureBudget struct {
	values int
}

func validateBoundedJSONStructure(raw []byte, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	budget := &jsonStructureBudget{}
	if err := consumeBoundedJSONValue(decoder, 1, budget); err != nil {
		return fmt.Errorf("%s %w", label, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s must not contain trailing JSON", label)
	}
	return nil
}

func consumeBoundedJSONValue(decoder *json.Decoder, depth int, budget *jsonStructureBudget) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("exceeds the maximum JSON depth of %d", maxJSONDepth)
	}
	budget.values++
	if budget.values > maxJSONStructuredValues {
		return fmt.Errorf("contains more than %d structured values", maxJSONStructuredValues)
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("is malformed JSON")
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			budget.values++
			if budget.values > maxJSONStructuredValues {
				return fmt.Errorf("contains more than %d structured values", maxJSONStructuredValues)
			}
			key, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("is malformed JSON")
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("contains an invalid object key")
			}
			if len(name) == 0 || len(name) > maxJSONMemberNameBytes {
				return fmt.Errorf("contains an empty or oversized object key")
			}
			if err := consumeBoundedJSONValue(decoder, depth+1, budget); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("is malformed JSON")
		}
	case '[':
		for decoder.More() {
			if err := consumeBoundedJSONValue(decoder, depth+1, budget); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("is malformed JSON")
		}
	default:
		return fmt.Errorf("is malformed JSON")
	}
	return nil
}

func validateJSONFieldValue(raw json.RawMessage, spec jsonFieldSpec, key string, label string) error {
	value := bytes.TrimSpace(raw)
	if bytes.Equal(value, []byte("null")) {
		if spec.allowNull {
			return nil
		}
		return fmt.Errorf("%s field %q must not be null", label, key)
	}
	switch spec.kind {
	case jsonStringValue:
		if len(value) < 2 || value[0] != '"' {
			return fmt.Errorf("%s field %q must be a string", label, key)
		}
		// A JSON escape can consume six encoded bytes per decoded byte. This
		// gate bounds work before allocating the decoded string.
		if spec.maxStringBytes > 0 && len(value) > spec.maxStringBytes*6+2 {
			return fmt.Errorf("%s field %q exceeds the %d-byte limit", label, key, spec.maxStringBytes)
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return fmt.Errorf("%s field %q must be a valid string", label, key)
		}
		if spec.maxStringBytes > 0 && len(decoded) > spec.maxStringBytes {
			return fmt.Errorf("%s field %q exceeds the %d-byte limit", label, key, spec.maxStringBytes)
		}
	case jsonBoolValue:
		if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
			return fmt.Errorf("%s field %q must be a boolean", label, key)
		}
	case jsonIntegerValue:
		if _, err := strconv.ParseInt(string(value), 10, 64); err != nil {
			return fmt.Errorf("%s field %q must be an integer", label, key)
		}
	case jsonObjectValue:
		if len(value) == 0 || value[0] != '{' {
			return fmt.Errorf("%s field %q must be an object", label, key)
		}
	default:
		return fmt.Errorf("%s field %q has no validator", label, key)
	}
	return nil
}

func validateDatasourceConfiguration(ds *KdbDatasource, fields map[string]json.RawMessage) error {
	if ds == nil {
		return fmt.Errorf("datasource settings target is nil")
	}
	if err := validateConfiguredEnum(fields, "executionMode", ds.ExecutionMode,
		ExecutionModeSync, ExecutionModeAsync, ExecutionModePluginAsync, ExecutionModeDeferredAsync, ExecutionModeLegacyAsync, ExecutionModeStream); err != nil {
		return err
	}
	if err := validateConfiguredEnum(fields, "compatibilityMode", ds.CompatibilityMode,
		CompatibilityModeNative, CompatibilityModeAquaQ, CompatibilityModePanopticon); err != nil {
		return err
	}
	if err := validateConfiguredEnum(fields, "legacyAsyncRequestMode", ds.LegacyAsyncRequestMode,
		LegacyAsyncRequestModeQueryText, LegacyAsyncRequestModeCompiledQueryText, LegacyAsyncRequestModeRequestDict, LegacyAsyncRequestModePanopticonDict); err != nil {
		return err
	}
	if err := validateConfiguredEnum(fields, "queryCacheKeyMode", ds.QueryCacheKeyMode,
		QueryCacheKeyModeStrict, QueryCacheKeyModeShared); err != nil {
		return err
	}

	for _, check := range []struct {
		key      string
		value    int64
		min, max int64
	}{
		{key: "port", value: int64(ds.Port), min: 1, max: 65535},
		{key: "syncMaxConnections", value: int64(ds.SyncMaxConnections), min: 1, max: maxSyncConnections},
		{key: "asyncMaxJobs", value: int64(ds.AsyncMaxJobs), min: 1, max: maxAsyncJobs},
		{key: "queryCacheTTLSeconds", value: int64(ds.QueryCacheTTLSeconds), min: 1, max: maxCacheTTLSeconds},
		{key: "queryCacheStaleTTLSeconds", value: int64(ds.QueryCacheStaleTTLSeconds), min: 0, max: maxCacheTTLSeconds},
		{key: "queryCacheTimeBucketSeconds", value: int64(ds.QueryCacheTimeBucketSeconds), min: 0, max: maxCacheTimeBucketSeconds},
		{key: "queryCacheMaxEntries", value: int64(ds.QueryCacheMaxEntries), min: 1, max: maxCacheEntries},
		{key: "queryCacheDiskMaxEntries", value: int64(ds.QueryCacheDiskMaxEntries), min: 1, max: maxDiskCacheEntries},
		{key: "queryCacheDiskMaxBytes", value: ds.QueryCacheDiskMaxBytes, min: 1, max: maxDiskCacheBytes},
		{key: "excelReportMaxRows", value: int64(ds.ExcelReportMaxRows), min: 1, max: maxExcelReportRows},
		{key: "excelReportMaxFileBytes", value: ds.ExcelReportMaxFileBytes, min: 1, max: maxExcelReportFileBytes},
		{key: "excelReportTimeoutMs", value: int64(ds.ExcelReportTimeoutMs), min: 1, max: maxExcelReportTimeoutMs},
	} {
		if _, configured := fields[check.key]; configured && (check.value < check.min || check.value > check.max) {
			return fmt.Errorf("datasource setting %q must be between %d and %d", check.key, check.min, check.max)
		}
	}

	for _, value := range []string{
		ds.Host,
		ds.Timeout,
		ds.ExecutionMode,
		ds.CompatibilityMode,
		ds.DeferredQueryWrapper,
		ds.PanopticonQueryWrapper,
		ds.PanopticonRequestFunction,
		ds.LegacyAsyncSubmit,
		ds.LegacyAsyncStatus,
		ds.LegacyAsyncResult,
		ds.LegacyAsyncCancel,
		ds.LegacyAsyncRequestMode,
		ds.LegacyAsyncJobIDPath,
		ds.LegacyAsyncStatusPath,
		ds.LegacyAsyncProgressPath,
		ds.LegacyAsyncMessagePath,
		ds.LegacyAsyncErrorPath,
		ds.LegacyAsyncPayloadPath,
		ds.LegacyAsyncQueuedValues,
		ds.LegacyAsyncRunningValues,
		ds.LegacyAsyncDoneValues,
		ds.LegacyAsyncErrorValues,
		ds.LegacyAsyncCancelledValues,
		ds.QueryCacheKeyMode,
		ds.QueryCacheDiskPath,
		ds.ExcelReportTemplateDirs,
		ds.ExcelReports,
	} {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("datasource settings must not contain NUL bytes")
		}
	}
	if wrapper := strings.TrimSpace(ds.DeferredQueryWrapper); wrapper != "" && strings.Count(wrapper, "{Query}") != 1 {
		return fmt.Errorf("datasource setting %q must contain exactly one {Query} placeholder", "deferredQueryWrapper")
	}
	if wrapper := strings.TrimSpace(ds.PanopticonQueryWrapper); wrapper != "" && strings.Count(wrapper, "{Query}") != 1 {
		return fmt.Errorf("datasource setting %q must contain exactly one {Query} placeholder", "panopticonQueryWrapper")
	}

	if strings.TrimSpace(ds.ExcelReports) != "" {
		catalog, err := ds.excelReportCatalog()
		if err != nil {
			return fmt.Errorf("datasource setting %q is invalid: %w", "excelReports", err)
		}
		if err := validateExcelReportCatalogLimits(catalog); err != nil {
			return fmt.Errorf("datasource setting %q is invalid: %w", "excelReports", err)
		}
	}
	return nil
}

func validateConfiguredEnum(fields map[string]json.RawMessage, key string, value string, allowed ...string) error {
	if _, configured := fields[key]; !configured {
		return nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("datasource setting %q has an invalid value", key)
}

func validateExcelReportCatalogLimits(catalog excelReportCatalog) error {
	if len(catalog.Reports) > maxExcelReportCount {
		return fmt.Errorf("report count exceeds %d", maxExcelReportCount)
	}
	totalBindings := 0
	for reportIndex, report := range catalog.Reports {
		if len(report.Bindings) > maxExcelBindingsPerReport {
			return fmt.Errorf("report %d binding count exceeds %d", reportIndex, maxExcelBindingsPerReport)
		}
		totalBindings += len(report.Bindings)
		if totalBindings > maxExcelBindingCount {
			return fmt.Errorf("total binding count exceeds %d", maxExcelBindingCount)
		}
		if err := validateOptionalResourceLimit("maxRows", int64(report.MaxRows), maxExcelReportRows); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if err := validateOptionalResourceLimit("maxFileBytes", report.MaxFileBytes, maxExcelReportFileBytes); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if err := validateOptionalResourceLimit("generationTimeoutMs", int64(report.GenerationTimeout), maxExcelReportTimeoutMs); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if err := validateOptionalResourceLimit("timeOut", int64(report.Timeout), maxQueryTimeoutMs); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if err := validateOptionalResourceLimit("maxDataPoints", report.MaxDataPoints, maxQueryMaxDataPoints); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if err := validateOptionalResourceLimit("intervalMs", report.IntervalMs, int64(maxQueryInterval/time.Millisecond)); err != nil {
			return fmt.Errorf("report %d: %w", reportIndex, err)
		}
		if report.ExecutionMode != "" && report.ExecutionMode != ExecutionModeSync {
			return fmt.Errorf("report %d executionMode must be %q", reportIndex, ExecutionModeSync)
		}
		if report.CompatibilityMode != "" && !isCompatibilityMode(report.CompatibilityMode) {
			return fmt.Errorf("report %d compatibilityMode is invalid", reportIndex)
		}
		if report.QueryCacheMode != "" && !isQueryCacheMode(report.QueryCacheMode) {
			return fmt.Errorf("report %d queryCacheMode is invalid", reportIndex)
		}
		if report.QueryCacheKeyMode != "" && !isQueryCacheKeyMode(report.QueryCacheKeyMode, true) {
			return fmt.Errorf("report %d queryCacheKeyMode is invalid", reportIndex)
		}
		if len(report.TemplatePath) > maxPathTextBytes {
			return fmt.Errorf("report %d templatePath exceeds the %d-byte limit", reportIndex, maxPathTextBytes)
		}
		for bindingIndex, binding := range report.Bindings {
			if err := validateOptionalResourceLimit("maxRows", int64(binding.MaxRows), maxExcelReportRows); err != nil {
				return fmt.Errorf("report %d binding %d: %w", reportIndex, bindingIndex, err)
			}
			if err := validateOptionalResourceLimit("timeOut", int64(binding.Timeout), maxQueryTimeoutMs); err != nil {
				return fmt.Errorf("report %d binding %d: %w", reportIndex, bindingIndex, err)
			}
			if err := validateOptionalResourceLimit("maxDataPoints", binding.MaxDataPoints, maxQueryMaxDataPoints); err != nil {
				return fmt.Errorf("report %d binding %d: %w", reportIndex, bindingIndex, err)
			}
			if err := validateOptionalResourceLimit("intervalMs", binding.IntervalMs, int64(maxQueryInterval/time.Millisecond)); err != nil {
				return fmt.Errorf("report %d binding %d: %w", reportIndex, bindingIndex, err)
			}
			if len(binding.QueryText) > maxQueryTextBytes {
				return fmt.Errorf("report %d binding %d queryText exceeds the %d-byte limit", reportIndex, bindingIndex, maxQueryTextBytes)
			}
			if len(binding.DeferredQueryWrapper) > maxCodeTextBytes ||
				len(binding.PanopticonQueryWrapper) > maxCodeTextBytes ||
				len(binding.PanopticonRequestFunction) > maxCodeTextBytes {
				return fmt.Errorf("report %d binding %d code text exceeds the %d-byte limit", reportIndex, bindingIndex, maxCodeTextBytes)
			}
			if binding.ExecutionMode != "" && binding.ExecutionMode != ExecutionModeSync {
				return fmt.Errorf("report %d binding %d executionMode must be %q", reportIndex, bindingIndex, ExecutionModeSync)
			}
			if binding.CompatibilityMode != "" && !isCompatibilityMode(binding.CompatibilityMode) {
				return fmt.Errorf("report %d binding %d compatibilityMode is invalid", reportIndex, bindingIndex)
			}
			if binding.QueryCacheMode != "" && !isQueryCacheMode(binding.QueryCacheMode) {
				return fmt.Errorf("report %d binding %d queryCacheMode is invalid", reportIndex, bindingIndex)
			}
			if binding.QueryCacheKeyMode != "" && !isQueryCacheKeyMode(binding.QueryCacheKeyMode, true) {
				return fmt.Errorf("report %d binding %d queryCacheKeyMode is invalid", reportIndex, bindingIndex)
			}
		}
	}
	return nil
}

func validateOptionalResourceLimit(name string, value int64, maximum int64) error {
	if value < 0 || value > maximum {
		return fmt.Errorf("%s must be zero or between 1 and %d", name, maximum)
	}
	return nil
}

func validateQueryRequestShape(req *backend.QueryDataRequest) error {
	if req == nil {
		return backend.PluginErrorf("invalid query request: request is nil")
	}
	if len(req.Queries) > maxQueryCount {
		return backend.PluginErrorf("invalid query request: query count exceeds %d", maxQueryCount)
	}
	seen := make(map[string]struct{}, len(req.Queries))
	for index, query := range req.Queries {
		if err := validateRefID(query.RefID); err != nil {
			return backend.PluginErrorf("invalid query request: RefID at index %d is invalid: %v", index, err)
		}
		if _, duplicate := seen[query.RefID]; duplicate {
			return backend.PluginErrorf("invalid query request: duplicate RefID at index %d", index)
		}
		seen[query.RefID] = struct{}{}
	}
	return nil
}

func validateRefID(refID string) error {
	if refID == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(refID) > maxRefIDBytes {
		return fmt.Errorf("exceeds the %d-byte limit", maxRefIDBytes)
	}
	if !utf8.ValidString(refID) {
		return fmt.Errorf("must contain valid UTF-8")
	}
	if strings.TrimSpace(refID) != refID {
		return fmt.Errorf("must not contain surrounding whitespace")
	}
	for _, r := range refID {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func (d *KdbDatasource) admitDataQueries(req *backend.QueryDataRequest) ([]admittedDataQuery, error) {
	admitted := make([]admittedDataQuery, 0, len(req.Queries))
	for index, query := range req.Queries {
		decodeStart := time.Now()
		model, fields, err := decodeExactQueryModel(query.JSON)
		decodeMs := diagnosticDurationMs(time.Since(decodeStart))
		if err != nil {
			return nil, fmt.Errorf("query %d failed admission: %w", index, err)
		}
		prepareStart := time.Now()
		if err := d.normalizeAndValidateQueryModel(req.PluginContext, query, &model, fields); err != nil {
			return nil, fmt.Errorf("query %d failed admission: %w", index, err)
		}
		prepareMs := diagnosticDurationMs(time.Since(prepareStart))
		admitted = append(admitted, admittedDataQuery{query: query, model: model, decodeMs: decodeMs, prepareMs: prepareMs})
	}
	return admitted, nil
}

func decodeExactQueryModel(raw []byte) (QueryModel, map[string]json.RawMessage, error) {
	fields, err := scanTopLevelJSONObject(raw, maxQueryJSONBytes, queryJSONFieldSpecs, false, "query JSON")
	if err != nil {
		return QueryModel{}, nil, err
	}
	if err := validateQueryEnvelopeObjects(fields, false, "query JSON"); err != nil {
		return QueryModel{}, nil, err
	}
	var model QueryModel
	if err := json.Unmarshal(raw, &model); err != nil {
		return QueryModel{}, nil, fmt.Errorf("query JSON could not be decoded")
	}
	return model, fields, nil
}

func validateQueryEnvelopeObjects(fields map[string]json.RawMessage, requireTimeRange bool, label string) error {
	if err := validateQueryEnvelopeText(fields, "key", label); err != nil {
		return err
	}
	if raw, ok := fields["datasource"]; ok {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			datasourceFields, err := scanTopLevelJSONObject(raw, maxQueryJSONBytes, queryDatasourceJSONFieldSpecs, false, label+" datasource")
			if err != nil {
				return err
			}
			for _, key := range []string{"type", "uid", "apiVersion"} {
				if err := validateQueryEnvelopeText(datasourceFields, key, label+" datasource"); err != nil {
					return err
				}
			}
		}
	}
	rawTimeRange, hasTimeRange := fields["timeRange"]
	if !hasTimeRange {
		if requireTimeRange {
			return fmt.Errorf("%s field %q is required", label, "timeRange")
		}
		return nil
	}
	timeFields, err := scanTopLevelJSONObject(rawTimeRange, maxQueryJSONBytes, liveTimeRangeJSONFieldSpecs, false, label+" timeRange")
	if err != nil {
		return err
	}
	if _, ok := timeFields["from"]; !ok {
		return fmt.Errorf("%s timeRange field %q is required", label, "from")
	}
	if _, ok := timeFields["to"]; !ok {
		return fmt.Errorf("%s timeRange field %q is required", label, "to")
	}
	return nil
}

func validateQueryEnvelopeText(fields map[string]json.RawMessage, key string, label string) error {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s field %q could not be decoded", label, key)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s field %q must contain valid UTF-8", label, key)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s field %q must not contain control characters", label, key)
		}
	}
	return nil
}

func admitLivePath(path string) (family string, id string, err error) {
	if len(path) == 0 || len(path) > maxLivePathBytes || !utf8.ValidString(path) {
		return "", "", fmt.Errorf("live path is invalid or exceeds the %d-byte limit", maxLivePathBytes)
	}
	separator := strings.IndexByte(path, '/')
	if separator <= 0 || strings.IndexByte(path[separator+1:], '/') >= 0 {
		return "", "", fmt.Errorf("live path must be exactly async/<id> or stream/<id>")
	}
	family, id = path[:separator], path[separator+1:]
	if family != ExecutionModeAsync && family != ExecutionModeStream {
		return "", "", fmt.Errorf("live path must be exactly async/<id> or stream/<id>")
	}
	if err := validateExternalID(id, "live path ID"); err != nil {
		return "", "", err
	}
	return family, id, nil
}

func validateExternalID(value string, label string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%s must not be a dot path segment", label)
	}
	if len(value) > maxLiveIDBytes {
		return fmt.Errorf("%s exceeds the %d-byte limit", label, maxLiveIDBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must contain valid UTF-8", label)
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		if (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '~' || c == '-' {
			continue
		}
		return fmt.Errorf("%s must use only ASCII letters, digits, '.', '_', '~', or '-'", label)
	}
	return nil
}

func (d *KdbDatasource) admitLiveQueryRequest(pCtx backend.PluginContext, raw json.RawMessage, path string) (admittedLiveQuery, error) {
	family, id, err := admitLivePath(path)
	if err != nil {
		return admittedLiveQuery{}, err
	}
	fields, err := scanTopLevelJSONObject(raw, maxQueryJSONBytes, liveQueryJSONFieldSpecs, false, "live query JSON")
	if err != nil {
		return admittedLiveQuery{}, err
	}
	if err := validateQueryEnvelopeObjects(fields, false, "live query JSON"); err != nil {
		return admittedLiveQuery{}, err
	}
	if _, ok := fields["refId"]; !ok {
		return admittedLiveQuery{}, fmt.Errorf("live query JSON field %q is required", "refId")
	}
	if _, ok := fields["queryText"]; !ok {
		return admittedLiveQuery{}, fmt.Errorf("live query JSON field %q is required", "queryText")
	}

	var liveReq liveQueryRequest
	if err := json.Unmarshal(raw, &liveReq); err != nil {
		return admittedLiveQuery{}, fmt.Errorf("live query JSON could not be decoded")
	}
	if err := validateRefID(liveReq.RefID); err != nil {
		return admittedLiveQuery{}, fmt.Errorf("live query RefID is invalid: %w", err)
	}
	from, to, err := parseLiveTimeRange(liveReq.TimeRange, fields)
	if err != nil {
		return admittedLiveQuery{}, err
	}
	queryType, err := decodedOptionalString(fields, "queryType")
	if err != nil {
		return admittedLiveQuery{}, err
	}
	model := liveReq.QueryModel
	query := backend.DataQuery{
		RefID:         liveReq.RefID,
		QueryType:     queryType,
		MaxDataPoints: liveReq.MaxDataPoints,
		TimeRange:     backend.TimeRange{From: from, To: to},
	}
	if liveReq.IntervalMs < 0 || liveReq.IntervalMs > int64(maxQueryInterval/time.Millisecond) {
		return admittedLiveQuery{}, fmt.Errorf("intervalMs must be between 0 and %d", maxQueryInterval/time.Millisecond)
	}
	query.Interval = time.Duration(liveReq.IntervalMs) * time.Millisecond
	if err := validateQueryEnvelopeAndBounds(query, &model, fields); err != nil {
		return admittedLiveQuery{}, err
	}
	if err := d.normalizeAndPrepareAsyncModel(pCtx, query, &model, fields, family); err != nil {
		return admittedLiveQuery{}, err
	}
	queryJSON, err := json.Marshal(model)
	if err != nil {
		return admittedLiveQuery{}, fmt.Errorf("normalized live query could not be encoded")
	}
	if len(queryJSON) > maxQueryJSONBytes {
		return admittedLiveQuery{}, fmt.Errorf("normalized live query exceeds the %d-byte limit", maxQueryJSONBytes)
	}
	query.JSON = queryJSON
	return admittedLiveQuery{
		liveReq: liveReq,
		query:   query,
		model:   model,
		family:  family,
		id:      id,
	}, nil
}

func decodedOptionalString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("field %q could not be decoded", key)
	}
	return value, nil
}

func parseLiveTimeRange(value liveTimeRange, fields map[string]json.RawMessage) (time.Time, time.Time, error) {
	if _, present := fields["timeRange"]; !present {
		return time.Time{}, time.Time{}, nil
	}
	from, err := time.Parse(time.RFC3339Nano, value.From)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("timeRange.from must be an RFC3339 timestamp")
	}
	to, err := time.Parse(time.RFC3339Nano, value.To)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("timeRange.to must be an RFC3339 timestamp")
	}
	from, to = from.UTC(), to.UTC()
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("timeRange.from must be before or equal to timeRange.to")
	}
	if from.Before(minQTimestamp) || to.After(maxQTimestamp) {
		return time.Time{}, time.Time{}, fmt.Errorf("timeRange is outside the finite q timestamp range")
	}
	return from, to, nil
}

func validateQueryEnvelopeAndBounds(query backend.DataQuery, model *QueryModel, fields map[string]json.RawMessage) error {
	if model == nil {
		return fmt.Errorf("query model is nil")
	}
	if rawRefID, ok := fields["refId"]; ok {
		var envelopeRefID string
		if err := json.Unmarshal(rawRefID, &envelopeRefID); err != nil {
			return fmt.Errorf("query JSON field %q could not be decoded", "refId")
		}
		if err := validateRefID(envelopeRefID); err != nil {
			return fmt.Errorf("query JSON field %q is invalid: %v", "refId", err)
		}
		if envelopeRefID != query.RefID {
			return fmt.Errorf("query JSON field %q does not match the request RefID", "refId")
		}
	}
	if err := validateQueryType(query.QueryType); err != nil {
		return fmt.Errorf("query type is invalid: %w", err)
	}
	if rawQueryType, ok := fields["queryType"]; ok {
		var envelopeQueryType string
		if err := json.Unmarshal(rawQueryType, &envelopeQueryType); err != nil {
			return fmt.Errorf("query JSON field %q could not be decoded", "queryType")
		}
		if err := validateQueryType(envelopeQueryType); err != nil {
			return fmt.Errorf("query JSON field %q is invalid: %w", "queryType", err)
		}
		if envelopeQueryType != query.QueryType {
			return fmt.Errorf("query JSON field %q does not match the request query type", "queryType")
		}
	}
	if query.MaxDataPoints < 0 || query.MaxDataPoints > maxQueryMaxDataPoints {
		return fmt.Errorf("maxDataPoints must be between 0 and %d", maxQueryMaxDataPoints)
	}
	if query.Interval < 0 || query.Interval > maxQueryInterval {
		return fmt.Errorf("interval must be between 0 and %s", maxQueryInterval)
	}
	if raw, ok := fields["maxDataPoints"]; ok {
		envelopeMaxDataPoints, err := decodeJSONInteger(raw, "maxDataPoints")
		if err != nil {
			return err
		}
		if envelopeMaxDataPoints < 0 || envelopeMaxDataPoints > maxQueryMaxDataPoints {
			return fmt.Errorf("query JSON field %q must be between 0 and %d", "maxDataPoints", maxQueryMaxDataPoints)
		}
		if envelopeMaxDataPoints != query.MaxDataPoints {
			return fmt.Errorf("query JSON field %q does not match the request max data points", "maxDataPoints")
		}
	}
	if raw, ok := fields["intervalMs"]; ok {
		envelopeIntervalMs, err := decodeJSONInteger(raw, "intervalMs")
		if err != nil {
			return err
		}
		maximumIntervalMs := int64(maxQueryInterval / time.Millisecond)
		if envelopeIntervalMs < 0 || envelopeIntervalMs > maximumIntervalMs {
			return fmt.Errorf("query JSON field %q must be between 0 and %d", "intervalMs", maximumIntervalMs)
		}
		if query.Interval%time.Millisecond != 0 || envelopeIntervalMs != query.Interval.Milliseconds() {
			return fmt.Errorf("query JSON field %q does not match the request interval", "intervalMs")
		}
	}
	if raw, ok := fields["datasourceId"]; ok {
		datasourceID, err := decodeJSONInteger(raw, "datasourceId")
		if err != nil {
			return err
		}
		if datasourceID < 0 || datasourceID > maxQueryDatasourceID {
			return fmt.Errorf("datasourceId must be between 0 and %d", maxQueryDatasourceID)
		}
	}
	if raw, ok := fields["queryCachingTTL"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		queryCachingTTL, err := decodeJSONInteger(raw, "queryCachingTTL")
		if err != nil {
			return err
		}
		if queryCachingTTL < 0 || queryCachingTTL > maxQueryCachingTTL {
			return fmt.Errorf("queryCachingTTL must be null or between 0 and %d milliseconds", maxQueryCachingTTL)
		}
	}
	from, to := query.TimeRange.From, query.TimeRange.To
	if from.IsZero() != to.IsZero() {
		return fmt.Errorf("time range must provide both from and to")
	}
	if !from.IsZero() && from.After(to) {
		return fmt.Errorf("time range from must be before or equal to to")
	}
	if !from.IsZero() && (from.Before(minQTimestamp) || to.After(maxQTimestamp)) {
		return fmt.Errorf("time range is outside the finite q timestamp range")
	}
	return validateExplicitQueryFields(*model, fields)
}

func decodeJSONInteger(raw json.RawMessage, key string) (int64, error) {
	value, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("query JSON field %q must be an integer", key)
	}
	return value, nil
}

func (d *KdbDatasource) normalizeAndPrepareAsyncModel(pCtx backend.PluginContext, query backend.DataQuery, model *QueryModel, fields map[string]json.RawMessage, family string) error {
	_, explicitMode := fields["executionMode"]
	d.normalizeQueryModel(model)
	switch family {
	case ExecutionModeStream:
		if explicitMode && model.ExecutionMode != ExecutionModeStream {
			return fmt.Errorf("executionMode contradicts the stream live path")
		}
		model.ExecutionMode = ExecutionModeStream
	case ExecutionModeAsync:
		if explicitMode {
			if !isAsyncExecutionMode(model.ExecutionMode) {
				return fmt.Errorf("executionMode contradicts the async live path")
			}
		} else if !isAsyncExecutionMode(model.ExecutionMode) {
			model.ExecutionMode = ExecutionModeAsync
		}
	default:
		return fmt.Errorf("unsupported live path family")
	}
	if err := validateNormalizedQueryModel(*model); err != nil {
		return err
	}
	if err := validatePluginContextExpansionInputs(pCtx); err != nil {
		return err
	}
	if model.ExecutionMode == ExecutionModeDeferredAsync {
		model.OriginalQueryText = model.QueryText
		wrapped, err := applyDeferredQueryWrapper(model.QueryText, model.DeferredQueryWrapper)
		if err != nil {
			return err
		}
		if len(wrapped) > maxQueryTextBytes {
			return fmt.Errorf("prepared queryText exceeds the %d-byte limit", maxQueryTextBytes)
		}
		model.QueryText = wrapped
	}
	if model.CompatibilityMode == CompatibilityModePanopticon {
		if err := validatePanopticonExpansionBudget(pCtx, query, *model); err != nil {
			return err
		}
	}
	if err := prepareQueryForExecution(pCtx, query, model); err != nil {
		return err
	}
	if strings.TrimSpace(model.QueryText) == "" {
		return fmt.Errorf("queryText must not be blank")
	}
	if len(model.QueryText) > maxQueryTextBytes {
		return fmt.Errorf("prepared queryText exceeds the %d-byte limit", maxQueryTextBytes)
	}
	return nil
}

func isAsyncExecutionMode(value string) bool {
	switch value {
	case ExecutionModeAsync, ExecutionModePluginAsync, ExecutionModeDeferredAsync, ExecutionModeLegacyAsync:
		return true
	default:
		return false
	}
}

func (d *KdbDatasource) normalizeAndValidateQueryModel(pCtx backend.PluginContext, query backend.DataQuery, model *QueryModel, fields map[string]json.RawMessage) error {
	if err := validateQueryEnvelopeAndBounds(query, model, fields); err != nil {
		return err
	}
	d.normalizeQueryModel(model)
	if err := validateNormalizedQueryModel(*model); err != nil {
		return err
	}
	if model.ExecutionMode != ExecutionModeSync {
		return fmt.Errorf("executionMode %q is not valid on the standard query endpoint", model.ExecutionMode)
	}
	if err := validatePluginContextExpansionInputs(pCtx); err != nil {
		return err
	}
	if model.CompatibilityMode == CompatibilityModePanopticon {
		if err := validatePanopticonExpansionBudget(pCtx, query, *model); err != nil {
			return err
		}
	}
	if err := prepareQueryForExecution(pCtx, query, model); err != nil {
		return err
	}
	if strings.TrimSpace(model.QueryText) == "" {
		return fmt.Errorf("queryText must not be blank")
	}
	if len(model.QueryText) > maxQueryTextBytes {
		return fmt.Errorf("prepared queryText exceeds the %d-byte limit", maxQueryTextBytes)
	}
	return nil
}

func validateQueryType(queryType string) error {
	if len(queryType) > maxQueryTypeBytes {
		return fmt.Errorf("exceeds the %d-byte limit", maxQueryTypeBytes)
	}
	if !utf8.ValidString(queryType) {
		return fmt.Errorf("must contain valid UTF-8")
	}
	if strings.TrimSpace(queryType) != queryType {
		return fmt.Errorf("must not contain surrounding whitespace")
	}
	for _, r := range queryType {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func validateExplicitQueryFields(model QueryModel, fields map[string]json.RawMessage) error {
	if _, ok := fields["timeOut"]; ok && (model.Timeout < 1 || model.Timeout > maxQueryTimeoutMs) {
		return fmt.Errorf("timeOut must be between 1 and %d milliseconds", maxQueryTimeoutMs)
	}
	if _, ok := fields["pollIntervalMs"]; ok && (model.PollIntervalMs < minPollIntervalMs || model.PollIntervalMs > maxPollIntervalMs) {
		return fmt.Errorf("pollIntervalMs must be between %d and %d milliseconds", minPollIntervalMs, maxPollIntervalMs)
	}
	if _, ok := fields["maxStreamRows"]; ok && (model.MaxStreamRows < 1 || model.MaxStreamRows > maxStreamRows) {
		return fmt.Errorf("maxStreamRows must be between 1 and %d", maxStreamRows)
	}
	if _, ok := fields["streamRetentionMs"]; ok && (model.StreamRetentionMs < 0 || model.StreamRetentionMs > maxStreamRetentionMs) {
		return fmt.Errorf("streamRetentionMs must be between 0 and %d", maxStreamRetentionMs)
	}
	if _, ok := fields["executionMode"]; ok && !isExecutionMode(model.ExecutionMode) {
		return fmt.Errorf("executionMode has an invalid value")
	}
	if _, ok := fields["compatibilityMode"]; ok && !isCompatibilityMode(model.CompatibilityMode) {
		return fmt.Errorf("compatibilityMode has an invalid value")
	}
	if _, ok := fields["legacyAsyncRequestMode"]; ok && !isLegacyAsyncRequestMode(model.LegacyAsyncRequestMode) {
		return fmt.Errorf("legacyAsyncRequestMode has an invalid value")
	}
	if _, ok := fields["queryCacheMode"]; ok && !isQueryCacheMode(model.QueryCacheMode) {
		return fmt.Errorf("queryCacheMode has an invalid value")
	}
	if _, ok := fields["queryCacheKeyMode"]; ok && !isQueryCacheKeyMode(model.QueryCacheKeyMode, true) {
		return fmt.Errorf("queryCacheKeyMode has an invalid value")
	}
	if model.QueryCacheTTLSeconds != nil && (*model.QueryCacheTTLSeconds < 1 || *model.QueryCacheTTLSeconds > maxQueryCacheTTLSeconds) {
		return fmt.Errorf("queryCacheTTLSeconds must be between 1 and %d", maxQueryCacheTTLSeconds)
	}
	if model.QueryCacheStaleTTLSeconds != nil && (*model.QueryCacheStaleTTLSeconds < 0 || *model.QueryCacheStaleTTLSeconds > maxQueryCacheTTLSeconds) {
		return fmt.Errorf("queryCacheStaleTTLSeconds must be between 0 and %d", maxQueryCacheTTLSeconds)
	}
	if model.QueryCacheTimeBucketSeconds != nil && (*model.QueryCacheTimeBucketSeconds < 0 || *model.QueryCacheTimeBucketSeconds > maxCacheTimeBucketSeconds) {
		return fmt.Errorf("queryCacheTimeBucketSeconds must be between 0 and %d", maxCacheTimeBucketSeconds)
	}
	for _, wrapper := range []struct {
		field string
		value string
	}{
		{field: "deferredQueryWrapper", value: model.DeferredQueryWrapper},
		{field: "panopticonQueryWrapper", value: model.PanopticonQueryWrapper},
	} {
		if _, configured := fields[wrapper.field]; configured &&
			strings.TrimSpace(wrapper.value) != "" &&
			strings.Count(wrapper.value, "{Query}") != 1 {
			return fmt.Errorf("%s must contain exactly one {Query} placeholder", wrapper.field)
		}
	}
	return nil
}

func validateNormalizedQueryModel(model QueryModel) error {
	if !isExecutionMode(model.ExecutionMode) {
		return fmt.Errorf("normalized executionMode has an invalid value")
	}
	if !isCompatibilityMode(model.CompatibilityMode) {
		return fmt.Errorf("normalized compatibilityMode has an invalid value")
	}
	if !isLegacyAsyncRequestMode(model.LegacyAsyncRequestMode) {
		return fmt.Errorf("normalized legacyAsyncRequestMode has an invalid value")
	}
	if model.Timeout < 1 || model.Timeout > maxQueryTimeoutMs {
		return fmt.Errorf("normalized timeOut must be between 1 and %d milliseconds", maxQueryTimeoutMs)
	}
	if model.PollIntervalMs < minPollIntervalMs || model.PollIntervalMs > maxPollIntervalMs {
		return fmt.Errorf("normalized pollIntervalMs must be between %d and %d milliseconds", minPollIntervalMs, maxPollIntervalMs)
	}
	if model.MaxStreamRows < 1 || model.MaxStreamRows > maxStreamRows {
		return fmt.Errorf("normalized maxStreamRows must be between 1 and %d", maxStreamRows)
	}
	if model.StreamRetentionMs < 0 || model.StreamRetentionMs > maxStreamRetentionMs {
		return fmt.Errorf("normalized streamRetentionMs must be between 0 and %d", maxStreamRetentionMs)
	}
	if strings.TrimSpace(model.QueryText) == "" {
		return fmt.Errorf("queryText must not be blank")
	}
	if len(model.QueryText) > maxQueryTextBytes || strings.IndexByte(model.QueryText, 0) >= 0 {
		return fmt.Errorf("queryText is invalid or exceeds the %d-byte limit", maxQueryTextBytes)
	}
	for _, text := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "timeColumn", value: model.TimeColumn, limit: maxFieldNameBytes},
		{name: "streamName", value: model.StreamName, limit: maxStreamNameBytes},
		{name: "deferredQueryWrapper", value: model.DeferredQueryWrapper, limit: maxCodeTextBytes},
		{name: "panopticonQueryWrapper", value: model.PanopticonQueryWrapper, limit: maxCodeTextBytes},
		{name: "panopticonRequestFunction", value: model.PanopticonRequestFunction, limit: maxCodeTextBytes},
		{name: "legacyAsyncSubmit", value: model.LegacyAsyncSubmit, limit: maxCodeTextBytes},
		{name: "legacyAsyncStatus", value: model.LegacyAsyncStatus, limit: maxCodeTextBytes},
		{name: "legacyAsyncResult", value: model.LegacyAsyncResult, limit: maxCodeTextBytes},
		{name: "legacyAsyncCancel", value: model.LegacyAsyncCancel, limit: maxCodeTextBytes},
		{name: "legacyAsyncJobIDPath", value: model.LegacyAsyncJobIDPath, limit: maxPathTextBytes},
		{name: "legacyAsyncStatusPath", value: model.LegacyAsyncStatusPath, limit: maxPathTextBytes},
		{name: "legacyAsyncProgressPath", value: model.LegacyAsyncProgressPath, limit: maxPathTextBytes},
		{name: "legacyAsyncMessagePath", value: model.LegacyAsyncMessagePath, limit: maxPathTextBytes},
		{name: "legacyAsyncErrorPath", value: model.LegacyAsyncErrorPath, limit: maxPathTextBytes},
		{name: "legacyAsyncPayloadPath", value: model.LegacyAsyncPayloadPath, limit: maxPathTextBytes},
		{name: "legacyAsyncQueuedValues", value: model.LegacyAsyncQueuedValues, limit: maxMappingTextBytes},
		{name: "legacyAsyncRunningValues", value: model.LegacyAsyncRunningValues, limit: maxMappingTextBytes},
		{name: "legacyAsyncDoneValues", value: model.LegacyAsyncDoneValues, limit: maxMappingTextBytes},
		{name: "legacyAsyncErrorValues", value: model.LegacyAsyncErrorValues, limit: maxMappingTextBytes},
		{name: "legacyAsyncCancelledValues", value: model.LegacyAsyncCancelledValues, limit: maxMappingTextBytes},
	} {
		if len(text.value) > text.limit || strings.IndexByte(text.value, 0) >= 0 {
			return fmt.Errorf("%s is invalid or exceeds the %d-byte limit", text.name, text.limit)
		}
	}
	if model.UseTimeColumn && strings.TrimSpace(model.TimeColumn) == "" {
		return fmt.Errorf("timeColumn is required when useTimeColumn is true")
	}
	return nil
}

func isExecutionMode(value string) bool {
	switch value {
	case ExecutionModeSync, ExecutionModeAsync, ExecutionModePluginAsync, ExecutionModeDeferredAsync, ExecutionModeLegacyAsync, ExecutionModeStream:
		return true
	default:
		return false
	}
}

func isCompatibilityMode(value string) bool {
	switch value {
	case CompatibilityModeNative, CompatibilityModeAquaQ, CompatibilityModePanopticon:
		return true
	default:
		return false
	}
}

func isLegacyAsyncRequestMode(value string) bool {
	switch value {
	case LegacyAsyncRequestModeQueryText, LegacyAsyncRequestModeCompiledQueryText, LegacyAsyncRequestModeRequestDict, LegacyAsyncRequestModePanopticonDict:
		return true
	default:
		return false
	}
}

func isQueryCacheMode(value string) bool {
	switch value {
	case QueryCacheModeDefault, QueryCacheModeEnabled, QueryCacheModeDisabled, QueryCacheModeBypass, QueryCacheModeRefresh:
		return true
	default:
		return false
	}
}

func isQueryCacheKeyMode(value string, allowDefault bool) bool {
	switch value {
	case QueryCacheKeyModeStrict, QueryCacheKeyModeShared:
		return true
	case QueryCacheModeDefault:
		return allowDefault
	default:
		return false
	}
}

func validatePluginContextExpansionInputs(pCtx backend.PluginContext) error {
	values := []string{}
	if pCtx.User != nil {
		values = append(values, pCtx.User.Name, pCtx.User.Login, pCtx.User.Email, pCtx.User.Role)
	}
	if pCtx.DataSourceInstanceSettings != nil {
		values = append(values,
			pCtx.DataSourceInstanceSettings.Name,
			pCtx.DataSourceInstanceSettings.UID,
			pCtx.DataSourceInstanceSettings.URL,
			pCtx.DataSourceInstanceSettings.User,
		)
	}
	for _, value := range values {
		if len(value) > maxPluginContextStringBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("plugin context contains an invalid or oversized string")
		}
	}
	return nil
}

func validatePanopticonExpansionBudget(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel) error {
	if err := validateMacroInputBudget(model.QueryText, panopticonMacroPairs(pCtx, query), maxQueryTextBytes); err != nil {
		return fmt.Errorf("queryText macro expansion exceeds the %d-byte limit", maxQueryTextBytes)
	}
	wrapper := model.PanopticonQueryWrapper
	if strings.TrimSpace(wrapper) == "" {
		return nil
	}
	if strings.Count(wrapper, "{Query}") != 1 {
		return fmt.Errorf("Panopticon query wrapper must contain exactly one {Query} placeholder")
	}
	if err := validateMacroInputBudget(wrapper, panopticonMacroPairs(pCtx, query), maxQueryTextBytes); err != nil {
		return fmt.Errorf("Panopticon query wrapper expansion exceeds the %d-byte limit", maxQueryTextBytes)
	}
	expandedQueryBudget := estimatedMacroExpansionBytes(model.QueryText, panopticonMacroPairs(pCtx, query), maxQueryTextBytes)
	expandedWrapperBudget := estimatedMacroExpansionBytes(wrapper, panopticonMacroPairs(pCtx, query), maxQueryTextBytes)
	if expandedQueryBudget < 0 || expandedWrapperBudget < 0 ||
		expandedWrapperBudget-len("{Query}") > maxQueryTextBytes-expandedQueryBudget {
		return fmt.Errorf("prepared queryText exceeds the %d-byte limit", maxQueryTextBytes)
	}
	return nil
}

func validateMacroInputBudget(input string, replacements []string, maximum int) error {
	if estimatedMacroExpansionBytes(input, replacements, maximum) < 0 {
		return fmt.Errorf("macro expansion limit exceeded")
	}
	return nil
}

func estimatedMacroExpansionBytes(input string, replacements []string, maximum int) int {
	size := len(input)
	if size > maximum {
		return -1
	}
	for index := 0; index+1 < len(replacements); index += 2 {
		token, replacement := replacements[index], replacements[index+1]
		if len(replacement) <= len(token) {
			continue
		}
		count := strings.Count(input, token)
		growth := len(replacement) - len(token)
		if count > 0 && (growth > maximum || count > (maximum-size)/growth) {
			return -1
		}
		size += count * growth
	}
	return size
}

func queryAdmissionResponse(req *backend.QueryDataRequest, admissionErr error) *backend.QueryDataResponse {
	response := backend.NewQueryDataResponse()
	message := "query request failed validation"
	if admissionErr != nil {
		message += ": " + admissionErr.Error()
	}
	for _, query := range req.Queries {
		response.Responses[query.RefID] = backend.ErrDataResponseWithSource(
			backend.StatusValidationFailed,
			backend.ErrorSourcePlugin,
			message,
		)
	}
	return response
}
