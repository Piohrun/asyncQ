package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
)

func TestExactDecoderSpecsCoverTaggedModels(t *testing.T) {
	assertSpecsCoverType(t, reflect.TypeOf(KdbDatasource{}), datasourceJSONFieldSpecs, nil)
	assertSpecsCoverType(t, reflect.TypeOf(QueryModel{}), queryJSONFieldSpecs, map[string]struct{}{
		"datasource":      {},
		"datasourceId":    {},
		"intervalMs":      {},
		"key":             {},
		"maxDataPoints":   {},
		"queryCachingTTL": {},
		"refId":           {},
		"hide":            {},
		"queryType":       {},
	})
	for _, standardOnly := range []string{"datasourceId", "queryCachingTTL"} {
		if _, ok := liveQueryJSONFieldSpecs[standardOnly]; ok {
			t.Errorf("live schema unexpectedly admits standard-only field %q", standardOnly)
		}
		if _, ok := asyncRunAndWaitJSONFieldSpecs[standardOnly]; ok {
			t.Errorf("async resource schema unexpectedly admits standard-only field %q", standardOnly)
		}
	}
}

func TestDatasourceSettingsExactObjectAdmission(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "null", raw: `null`},
		{name: "array", raw: `[]`},
		{name: "trailing", raw: `{"host":"localhost","port":5000} true`},
		{name: "malformed", raw: `{"host":"localhost","port":5000`},
		{name: "duplicate known", raw: `{"host":"localhost","host":"other","port":5000}`},
		{name: "duplicate metadata", raw: `{"host":"localhost","port":5000,"access":"proxy","access":"direct"}`},
		{name: "case variant", raw: `{"Host":"localhost","port":5000}`},
		{name: "mixed case TLS alias", raw: `{"host":"localhost","port":5000,"withTls":false}`},
		{name: "unicode folded host alias", raw: `{"hoſt":"localhost","port":5000}`},
		{name: "unicode folded capped field alias", raw: `{"host":"localhost","port":5000,"syncMaxConnectionſ":999}`},
		{name: "known null", raw: `{"host":null,"port":5000}`},
		{name: "wrong string kind", raw: `{"host":17,"port":5000}`},
		{name: "wrong integer kind", raw: `{"host":"localhost","port":"5000"}`},
		{name: "fractional integer", raw: `{"host":"localhost","port":5000.0}`},
		{name: "wrong boolean kind", raw: `{"host":"localhost","port":5000,"enableAsync":1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewKdbDatasource(context.Background(), backend.DataSourceInstanceSettings{JSONData: []byte(test.raw)}); err == nil {
				t.Fatal("invalid datasource settings were accepted")
			}
		})
	}
}

func TestDatasourceSettingsAllowUnknownGrafanaMetadata(t *testing.T) {
	raw := []byte(`{
		"host":"localhost",
		"port":5000,
		"access":"proxy",
		"basicAuth":false,
		"futureGrafanaMetadata":{"nested":[1,true,null]}
	}`)
	instance, err := NewKdbDatasource(context.Background(), backend.DataSourceInstanceSettings{JSONData: raw})
	if err != nil {
		t.Fatalf("forward-compatible metadata was rejected: %v", err)
	}
	ds := instance.(*KdbDatasource)
	t.Cleanup(ds.Dispose)
}

func TestExactJSONScannerBoundsMembersDepthAndUnknownRetention(t *testing.T) {
	members := make([]string, 0, maxJSONTopLevelMembers+1)
	for index := 0; index <= maxJSONTopLevelMembers; index++ {
		members = append(members, fmt.Sprintf("%q:%d", fmt.Sprintf("future%d", index), index))
	}
	tooMany := []byte("{" + strings.Join(members, ",") + "}")
	if _, err := scanTopLevelJSONObject(tooMany, maxDatasourceSettingsJSONBytes, datasourceJSONFieldSpecs, true, "settings"); err == nil {
		t.Fatal("top-level member limit was not enforced")
	}

	deep := "0"
	for range maxJSONDepth + 1 {
		deep = "[" + deep + "]"
	}
	if _, err := scanTopLevelJSONObject([]byte(`{"future":`+deep+`}`), maxDatasourceSettingsJSONBytes, datasourceJSONFieldSpecs, true, "settings"); err == nil {
		t.Fatal("JSON depth limit was not enforced")
	}
	oversizedKey := []byte(`{"` + strings.Repeat("k", maxJSONMemberNameBytes+1) + `":true}`)
	if _, err := scanTopLevelJSONObject(oversizedKey, maxDatasourceSettingsJSONBytes, datasourceJSONFieldSpecs, true, "settings"); err == nil {
		t.Fatal("JSON member-name limit was not enforced")
	}

	fields, err := scanTopLevelJSONObject(
		[]byte(`{"host":"localhost","future":{"large":["metadata"]}}`),
		maxDatasourceSettingsJSONBytes,
		datasourceJSONFieldSpecs,
		true,
		"settings",
	)
	if err != nil {
		t.Fatalf("forward-compatible metadata rejected: %v", err)
	}
	if _, retained := fields["future"]; retained {
		t.Fatal("unknown datasource metadata was retained")
	}
	if _, retained := fields["host"]; !retained {
		t.Fatal("known datasource field was not retained")
	}
}

func TestDatasourceSettingsRejectInvalidEnumsAndResourceLimits(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value interface{}
	}{
		{name: "execution mode empty", field: "executionMode", value: ""},
		{name: "execution mode case", field: "executionMode", value: "SYNC"},
		{name: "compatibility mode", field: "compatibilityMode", value: "native "},
		{name: "legacy request mode", field: "legacyAsyncRequestMode", value: "request"},
		{name: "cache key mode", field: "queryCacheKeyMode", value: "default"},
		{name: "sync zero", field: "syncMaxConnections", value: 0},
		{name: "sync negative", field: "syncMaxConnections", value: -1},
		{name: "sync over cap", field: "syncMaxConnections", value: maxSyncConnections + 1},
		{name: "async zero", field: "asyncMaxJobs", value: 0},
		{name: "async negative", field: "asyncMaxJobs", value: -1},
		{name: "async over cap", field: "asyncMaxJobs", value: maxAsyncJobs + 1},
		{name: "memory TTL zero", field: "queryCacheTTLSeconds", value: 0},
		{name: "memory TTL negative", field: "queryCacheTTLSeconds", value: -1},
		{name: "memory TTL over cap", field: "queryCacheTTLSeconds", value: maxCacheTTLSeconds + 1},
		{name: "stale negative", field: "queryCacheStaleTTLSeconds", value: -1},
		{name: "stale over cap", field: "queryCacheStaleTTLSeconds", value: maxCacheTTLSeconds + 1},
		{name: "bucket negative", field: "queryCacheTimeBucketSeconds", value: -1},
		{name: "bucket over cap", field: "queryCacheTimeBucketSeconds", value: maxCacheTimeBucketSeconds + 1},
		{name: "memory entries zero", field: "queryCacheMaxEntries", value: 0},
		{name: "memory entries over cap", field: "queryCacheMaxEntries", value: maxCacheEntries + 1},
		{name: "disk entries zero", field: "queryCacheDiskMaxEntries", value: 0},
		{name: "disk entries over cap", field: "queryCacheDiskMaxEntries", value: maxDiskCacheEntries + 1},
		{name: "disk bytes zero", field: "queryCacheDiskMaxBytes", value: 0},
		{name: "disk bytes over cap", field: "queryCacheDiskMaxBytes", value: maxDiskCacheBytes + 1},
		{name: "excel rows zero", field: "excelReportMaxRows", value: 0},
		{name: "excel rows over cap", field: "excelReportMaxRows", value: maxExcelReportRows + 1},
		{name: "excel bytes zero", field: "excelReportMaxFileBytes", value: 0},
		{name: "excel bytes over cap", field: "excelReportMaxFileBytes", value: maxExcelReportFileBytes + 1},
		{name: "excel timeout zero", field: "excelReportTimeoutMs", value: 0},
		{name: "excel timeout over cap", field: "excelReportTimeoutMs", value: maxExcelReportTimeoutMs + 1},
		{name: "max int", field: "queryCacheDiskMaxBytes", value: int64(math.MaxInt64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := validDatasourceJSON()
			settings[test.field] = test.value
			if _, err := NewKdbDatasource(context.Background(), datasourceSettings(t, settings, nil)); err == nil {
				t.Fatalf("invalid %s value was accepted", test.field)
			}
		})
	}
}

func TestDatasourceSettingsValidBoundariesAndPresence(t *testing.T) {
	settings := validDatasourceJSON()
	settings["executionMode"] = ExecutionModeStream
	settings["compatibilityMode"] = CompatibilityModePanopticon
	settings["legacyAsyncRequestMode"] = LegacyAsyncRequestModePanopticonDict
	settings["queryCacheKeyMode"] = QueryCacheKeyModeShared
	settings["syncMaxConnections"] = maxSyncConnections
	settings["asyncMaxJobs"] = maxAsyncJobs
	settings["queryCacheEnabled"] = false
	settings["queryCacheTTLSeconds"] = maxCacheTTLSeconds
	settings["queryCacheStaleTTLSeconds"] = 0
	settings["queryCacheTimeBucketSeconds"] = 0
	settings["queryCacheMaxEntries"] = maxCacheEntries
	settings["queryCacheDiskEnabled"] = false
	settings["queryCacheDiskMaxEntries"] = maxDiskCacheEntries
	settings["queryCacheDiskMaxBytes"] = maxDiskCacheBytes
	settings["queryCacheControlEnabled"] = false
	settings["excelReportMaxRows"] = maxExcelReportRows
	settings["excelReportMaxFileBytes"] = maxExcelReportFileBytes
	settings["excelReportTimeoutMs"] = maxExcelReportTimeoutMs

	instance, err := NewKdbDatasource(context.Background(), datasourceSettings(t, settings, nil))
	if err != nil {
		t.Fatalf("valid datasource boundaries were rejected: %v", err)
	}
	ds := instance.(*KdbDatasource)
	t.Cleanup(ds.Dispose)
	if !ds.queryCacheConfigured || ds.QueryCacheEnabled {
		t.Fatalf("explicit queryCacheEnabled=false was not preserved: configured=%v value=%v", ds.queryCacheConfigured, ds.QueryCacheEnabled)
	}
	if !ds.queryCacheDiskConfigured || ds.QueryCacheDiskEnabled {
		t.Fatalf("explicit queryCacheDiskEnabled=false was not preserved: configured=%v value=%v", ds.queryCacheDiskConfigured, ds.QueryCacheDiskEnabled)
	}
	if !ds.queryCacheControlConfigured || ds.QueryCacheControlEnabled {
		t.Fatalf("explicit queryCacheControlEnabled=false was not preserved: configured=%v value=%v", ds.queryCacheControlConfigured, ds.QueryCacheControlEnabled)
	}
	if ds.SyncMaxConnections != maxSyncConnections || ds.AsyncMaxJobs != maxAsyncJobs {
		t.Fatalf("connection/job limits changed: sync=%d async=%d", ds.SyncMaxConnections, ds.AsyncMaxJobs)
	}
}

func TestDatasourceSettingsBoundLargeTextBeforeUse(t *testing.T) {
	tests := []map[string]interface{}{
		{"deferredQueryWrapper": strings.Repeat("x", maxCodeTextBytes+1)},
		{"queryCacheDiskPath": strings.Repeat("x", maxPathTextBytes+1)},
		{"excelReportTemplateDirs": strings.Repeat("x", maxTemplateDirsTextBytes+1)},
		{"excelReports": strings.Repeat("x", maxExcelReportsJSONBytes+1)},
		{"excelReports": `{"reports":[`},
		{"excelReports": fmt.Sprintf(`{"reports":[{"id":"r","maxRows":%d,"bindings":[{"id":"A","sheet":"S","cell":"A1"}]}]}`, maxExcelReportRows+1)},
	}
	for index, extra := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			settings := validDatasourceJSON()
			for key, value := range extra {
				settings[key] = value
			}
			if _, err := NewKdbDatasource(context.Background(), datasourceSettings(t, settings, nil)); err == nil {
				t.Fatal("oversized or invalid datasource text was accepted")
			}
		})
	}

	oversized := append([]byte(`{"host":"localhost","port":5000,"future":"`), []byte(strings.Repeat("x", maxDatasourceSettingsJSONBytes))...)
	oversized = append(oversized, []byte(`"}`)...)
	if _, err := NewKdbDatasource(context.Background(), backend.DataSourceInstanceSettings{JSONData: oversized}); err == nil {
		t.Fatal("oversized datasource JSON was accepted")
	}
}

func TestQueryDataRejectsInvalidRequestShapeBeforeExecution(t *testing.T) {
	validJSON := json.RawMessage(`{"queryText":"1","compatibilityMode":"panopticon"}`)
	tests := []struct {
		name string
		req  *backend.QueryDataRequest
	}{
		{name: "nil request", req: nil},
		{name: "empty RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{{JSON: validJSON}}}},
		{name: "leading whitespace RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{{RefID: " A", JSON: validJSON}}}},
		{name: "trailing whitespace RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{{RefID: "A ", JSON: validJSON}}}},
		{name: "control RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{{RefID: "A\nB", JSON: validJSON}}}},
		{name: "oversized RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{{RefID: strings.Repeat("A", maxRefIDBytes+1), JSON: validJSON}}}},
		{name: "duplicate RefID", req: &backend.QueryDataRequest{Queries: []backend.DataQuery{
			{RefID: "A", JSON: validJSON},
			{RefID: "A", JSON: validJSON},
		}}},
	}
	tooMany := make([]backend.DataQuery, maxQueryCount+1)
	for index := range tooMany {
		tooMany[index] = backend.DataQuery{RefID: fmt.Sprintf("Q%d", index), JSON: validJSON}
	}
	tests = append(tests, struct {
		name string
		req  *backend.QueryDataRequest
	}{name: "too many queries", req: &backend.QueryDataRequest{Queries: tooMany}})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds, calls := admissionTestDatasource()
			response, err := ds.QueryData(context.Background(), test.req)
			if err == nil || !backend.IsPluginError(err) {
				t.Fatalf("request error = %v, want plugin-sourced error", err)
			}
			if response != nil {
				t.Fatalf("request-level rejection returned a response: %#v", response)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("request-level rejection executed %d q calls", got)
			}
		})
	}
}

func TestQueryDataExactQueryJSONAdmission(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "null", raw: `null`},
		{name: "array", raw: `[]`},
		{name: "trailing", raw: `{"queryText":"1"} false`},
		{name: "malformed", raw: `{"queryText":"1"`},
		{name: "duplicate", raw: `{"queryText":"1","queryText":"2"}`},
		{name: "case variant", raw: `{"QueryText":"1"}`},
		{name: "timeout case variant", raw: `{"queryText":"1","timeout":1000}`},
		{name: "unknown typo", raw: `{"queryText":"1","queryTex":"2"}`},
		{name: "null plugin field", raw: `{"queryText":null}`},
		{name: "wrong query kind", raw: `{"queryText":1}`},
		{name: "wrong bool kind", raw: `{"queryText":"1","useTimeColumn":0}`},
		{name: "wrong integer kind", raw: `{"queryText":"1","timeOut":"1000"}`},
		{name: "fractional integer", raw: `{"queryText":"1","timeOut":1000.0}`},
		{name: "string datasource", raw: `{"queryText":"1","datasource":"legacy"}`},
		{name: "wrong hide kind", raw: `{"queryText":"1","hide":0}`},
		{name: "wrong query type kind", raw: `{"queryText":"1","queryType":1}`},
		{name: "mismatched envelope RefID", raw: `{"queryText":"1","refId":"B"}`},
		{name: "mismatched envelope query type", raw: `{"queryText":"1","queryType":"table"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds, calls := admissionTestDatasource()
			req := &backend.QueryDataRequest{Queries: []backend.DataQuery{{RefID: "A", JSON: []byte(test.raw)}}}
			response, err := ds.QueryData(context.Background(), req)
			if err != nil {
				t.Fatalf("query-level rejection returned top-level error: %v", err)
			}
			assertValidationResponses(t, response, "A")
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid query JSON executed %d q calls", got)
			}
		})
	}
}

func TestQueryEnvelopeCommonMetadataAcceptedAcrossSurfaces(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{name: "explore key", fragment: `,"key":"explore-query-A"`},
		{name: "maximum key", fragment: `,"key":"` + strings.Repeat("k", maxQueryKeyBytes) + `"`},
		{name: "null datasource", fragment: `,"datasource":null`},
		{
			name:     "strict datasource ref",
			fragment: `,"datasource":{"type":"asyncq-kdbbackend-datasource","uid":"asyncq-main","apiVersion":"v1"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{"queryText":"1","refId":"A"` + test.fragment + `}`)
			t.Run("standard", func(t *testing.T) {
				ds := &KdbDatasource{}
				admitted, err := ds.admitDataQueries(&backend.QueryDataRequest{
					Queries: []backend.DataQuery{{RefID: "A", JSON: raw}},
				})
				if err != nil {
					t.Fatalf("standard query rejected common envelope metadata: %v", err)
				}
				if len(admitted) != 1 || admitted[0].model.QueryText != "1" {
					t.Fatalf("unexpected standard admission: %#v", admitted)
				}
			})
			t.Run("live", func(t *testing.T) {
				if _, err := (&KdbDatasource{}).admitLiveQueryRequest(
					backend.PluginContext{},
					raw,
					"async/request",
				); err != nil {
					t.Fatalf("live query rejected common envelope metadata: %v", err)
				}
			})
			t.Run("async resource", func(t *testing.T) {
				if _, _, _, _, err := (&KdbDatasource{}).decodeAsyncRunAndWaitRequest(
					backend.PluginContext{},
					raw,
				); err != nil {
					t.Fatalf("async resource rejected common envelope metadata: %v", err)
				}
			})
		})
	}
}

func TestQueryEnvelopeCommonMetadataRemainsStrictAcrossSurfaces(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{name: "key null", fragment: `,"key":null`},
		{name: "key wrong type", fragment: `,"key":1`},
		{name: "key control", fragment: `,"key":"explore\u0000A"`},
		{name: "key oversized", fragment: `,"key":"` + strings.Repeat("k", maxQueryKeyBytes+1) + `"`},
		{name: "key alias", fragment: `,"Key":"explore-A"`},
		{name: "datasource string", fragment: `,"datasource":"legacy"`},
		{name: "datasource duplicate field", fragment: `,"datasource":{"uid":"a","uid":"b"}`},
		{name: "datasource alias", fragment: `,"datasource":{"UID":"a"}`},
		{name: "datasource unknown field", fragment: `,"datasource":{"name":"main"}`},
		{name: "datasource wrong type", fragment: `,"datasource":{"apiVersion":1}`},
		{name: "datasource nested null", fragment: `,"datasource":{"uid":null}`},
		{name: "datasource control", fragment: `,"datasource":{"uid":"a\u0000b"}`},
		{name: "datasource type oversized", fragment: `,"datasource":{"type":"` + strings.Repeat("t", maxFieldNameBytes+1) + `"}`},
		{name: "datasource uid oversized", fragment: `,"datasource":{"uid":"` + strings.Repeat("u", maxFieldNameBytes+1) + `"}`},
		{name: "datasource API version oversized", fragment: `,"datasource":{"apiVersion":"` + strings.Repeat("v", maxFieldNameBytes+1) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{"queryText":"1","refId":"A"` + test.fragment + `}`)
			if _, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
				Queries: []backend.DataQuery{{RefID: "A", JSON: raw}},
			}); err == nil {
				t.Fatal("standard query accepted invalid common envelope metadata")
			}
			if _, err := (&KdbDatasource{}).admitLiveQueryRequest(
				backend.PluginContext{},
				raw,
				"async/request",
			); err == nil {
				t.Fatal("live query accepted invalid common envelope metadata")
			}
			if _, _, _, _, err := (&KdbDatasource{}).decodeAsyncRunAndWaitRequest(
				backend.PluginContext{},
				raw,
			); err == nil {
				t.Fatal("async resource accepted invalid common envelope metadata")
			}
		})
	}
}

func TestQueryEnvelopeRejectsInvalidUTF8KeyAcrossSurfaces(t *testing.T) {
	raw := append([]byte(`{"queryText":"1","refId":"A","key":"`), 0xff)
	raw = append(raw, []byte(`"}`)...)
	if _, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: raw}},
	}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("standard query invalid UTF-8 key error=%v", err)
	}
	if _, err := (&KdbDatasource{}).admitLiveQueryRequest(
		backend.PluginContext{},
		raw,
		"async/request",
	); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("live query invalid UTF-8 key error=%v", err)
	}
	if _, _, _, _, err := (&KdbDatasource{}).decodeAsyncRunAndWaitRequest(
		backend.PluginContext{},
		raw,
	); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("async resource invalid UTF-8 key error=%v", err)
	}
}

func TestStandardQueryAdmitsExactGrafanaRuntimeEnvelope(t *testing.T) {
	raw := json.RawMessage(`{
		"queryText":"select from trade",
		"refId":"A",
		"key":"explore-query-A",
		"hide":false,
		"queryType":"table",
		"datasource":{"type":"asyncq-kdbbackend-datasource","uid":"asyncq-main","apiVersion":"v1"},
		"datasourceId":42,
		"intervalMs":1500,
		"maxDataPoints":1234,
		"queryCachingTTL":60000
	}`)
	query := backend.DataQuery{
		RefID:         "A",
		QueryType:     "table",
		MaxDataPoints: 1234,
		Interval:      1500 * time.Millisecond,
		JSON:          raw,
	}
	admitted, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
		Queries: []backend.DataQuery{query},
	})
	if err != nil {
		t.Fatalf("Grafana runtime query envelope was rejected: %v", err)
	}
	if len(admitted) != 1 {
		t.Fatalf("admitted query count=%d want=1", len(admitted))
	}
	if !bytes.Equal(admitted[0].query.JSON, raw) {
		t.Fatal("standard admission did not retain the original SDK query JSON")
	}
	normalized, err := json.Marshal(admitted[0].model)
	if err != nil {
		t.Fatalf("marshal normalized query model: %v", err)
	}
	var modelFields map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &modelFields); err != nil {
		t.Fatalf("decode normalized query model: %v", err)
	}
	for _, envelopeOnly := range []string{
		"key",
		"datasource",
		"datasourceId",
		"intervalMs",
		"maxDataPoints",
		"queryCachingTTL",
	} {
		if _, copied := modelFields[envelopeOnly]; copied {
			t.Errorf("envelope-only field %q was copied into QueryModel", envelopeOnly)
		}
	}

	nullTTL := bytes.Replace(raw, []byte(`60000`), []byte(`null`), 1)
	query.JSON = nullTTL
	if _, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
		Queries: []backend.DataQuery{query},
	}); err != nil {
		t.Fatalf("Grafana runtime null queryCachingTTL was rejected: %v", err)
	}
}

func TestStandardQueryRuntimeEnvelopeBoundsAndParity(t *testing.T) {
	valid := `{
		"queryText":"1",
		"refId":"A",
		"datasource":{"type":"asyncq-kdbbackend-datasource","uid":"main","apiVersion":"v1"},
		"datasourceId":42,
		"intervalMs":1000,
		"maxDataPoints":500,
		"queryCachingTTL":60000
	}`
	tests := []struct {
		name          string
		old           string
		replacement   string
		maxDataPoints int64
		interval      time.Duration
	}{
		{name: "max data points mismatch", old: `"maxDataPoints":500`, replacement: `"maxDataPoints":501`, maxDataPoints: 500, interval: time.Second},
		{name: "interval mismatch", old: `"intervalMs":1000`, replacement: `"intervalMs":1001`, maxDataPoints: 500, interval: time.Second},
		{name: "interval sub-millisecond mismatch", old: `"intervalMs":1000`, replacement: `"intervalMs":1000`, maxDataPoints: 500, interval: time.Second + time.Nanosecond},
		{name: "max data points negative", old: `"maxDataPoints":500`, replacement: `"maxDataPoints":-1`, maxDataPoints: 500, interval: time.Second},
		{name: "max data points over bound", old: `"maxDataPoints":500`, replacement: fmt.Sprintf(`"maxDataPoints":%d`, maxQueryMaxDataPoints+1), maxDataPoints: 500, interval: time.Second},
		{name: "max data points fractional", old: `"maxDataPoints":500`, replacement: `"maxDataPoints":500.0`, maxDataPoints: 500, interval: time.Second},
		{name: "max data points null", old: `"maxDataPoints":500`, replacement: `"maxDataPoints":null`, maxDataPoints: 500, interval: time.Second},
		{name: "interval negative", old: `"intervalMs":1000`, replacement: `"intervalMs":-1`, maxDataPoints: 500, interval: time.Second},
		{name: "interval over bound", old: `"intervalMs":1000`, replacement: fmt.Sprintf(`"intervalMs":%d`, maxQueryInterval/time.Millisecond+1), maxDataPoints: 500, interval: time.Second},
		{name: "interval fractional", old: `"intervalMs":1000`, replacement: `"intervalMs":1000.0`, maxDataPoints: 500, interval: time.Second},
		{name: "interval null", old: `"intervalMs":1000`, replacement: `"intervalMs":null`, maxDataPoints: 500, interval: time.Second},
		{name: "datasource id negative", old: `"datasourceId":42`, replacement: `"datasourceId":-1`, maxDataPoints: 500, interval: time.Second},
		{name: "datasource id over bound", old: `"datasourceId":42`, replacement: fmt.Sprintf(`"datasourceId":%d`, maxQueryDatasourceID+1), maxDataPoints: 500, interval: time.Second},
		{name: "datasource id fractional", old: `"datasourceId":42`, replacement: `"datasourceId":42.5`, maxDataPoints: 500, interval: time.Second},
		{name: "datasource id null", old: `"datasourceId":42`, replacement: `"datasourceId":null`, maxDataPoints: 500, interval: time.Second},
		{name: "cache TTL negative", old: `"queryCachingTTL":60000`, replacement: `"queryCachingTTL":-1`, maxDataPoints: 500, interval: time.Second},
		{name: "cache TTL over bound", old: `"queryCachingTTL":60000`, replacement: fmt.Sprintf(`"queryCachingTTL":%d`, maxQueryCachingTTL+1), maxDataPoints: 500, interval: time.Second},
		{name: "cache TTL fractional", old: `"queryCachingTTL":60000`, replacement: `"queryCachingTTL":60000.5`, maxDataPoints: 500, interval: time.Second},
		{name: "cache TTL wrong type", old: `"queryCachingTTL":60000`, replacement: `"queryCachingTTL":"60000"`, maxDataPoints: 500, interval: time.Second},
		{name: "unknown request member", old: `"queryCachingTTL":60000`, replacement: `"queryCachingTTL":60000,"scopedVars":{}`, maxDataPoints: 500, interval: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(valid, test.old, test.replacement, 1))
			_, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
				Queries: []backend.DataQuery{{
					RefID:         "A",
					MaxDataPoints: test.maxDataPoints,
					Interval:      test.interval,
					JSON:          raw,
				}},
			})
			if err == nil {
				t.Fatal("invalid Grafana runtime envelope was accepted")
			}
			if strings.Contains(test.name, "mismatch") && !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("runtime/backend contradiction error=%v", err)
			}
		})
	}

	for _, surface := range []string{"live", "async resource"} {
		for _, field := range []string{`"datasourceId":42`, `"queryCachingTTL":null`} {
			t.Run("standard-only "+field+" rejected by "+surface, func(t *testing.T) {
				prefix := `{"queryText":"1","refId":"A",`
				if surface == "async resource" {
					prefix = `{"queryText":"1","refId":"A","requestId":"request-1",`
				}
				raw := json.RawMessage(prefix + field + `}`)
				if surface == "live" {
					if _, err := (&KdbDatasource{}).admitLiveQueryRequest(backend.PluginContext{}, raw, "async/request"); err == nil {
						t.Fatal("live query accepted a standard-only envelope field")
					}
					return
				}
				if _, _, _, _, err := (&KdbDatasource{}).decodeAsyncRunAndWaitRequest(backend.PluginContext{}, raw); err == nil {
					t.Fatal("async resource accepted a standard-only envelope field")
				}
			})
		}
	}
}

func TestStandardQueryRuntimeEnvelopeAcceptsSafeBoundaries(t *testing.T) {
	for _, test := range []struct {
		name          string
		datasourceID  int64
		maxDataPoints int64
		intervalMs    int64
		cacheTTL      string
	}{
		{name: "zero and null", cacheTTL: "null"},
		{
			name:          "maximum",
			datasourceID:  maxQueryDatasourceID,
			maxDataPoints: maxQueryMaxDataPoints,
			intervalMs:    int64(maxQueryInterval / time.Millisecond),
			cacheTTL:      strconv.FormatInt(maxQueryCachingTTL, 10),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(`{
				"queryText":"1",
				"refId":"A",
				"datasource":null,
				"datasourceId":%d,
				"intervalMs":%d,
				"maxDataPoints":%d,
				"queryCachingTTL":%s
			}`, test.datasourceID, test.intervalMs, test.maxDataPoints, test.cacheTTL))
			if _, err := (&KdbDatasource{}).admitDataQueries(&backend.QueryDataRequest{
				Queries: []backend.DataQuery{{
					RefID:         "A",
					MaxDataPoints: test.maxDataPoints,
					Interval:      time.Duration(test.intervalMs) * time.Millisecond,
					JSON:          raw,
				}},
			}); err != nil {
				t.Fatalf("safe runtime boundary was rejected: %v", err)
			}
		})
	}
}

func TestQueryDataValidatesQueryTypeExactly(t *testing.T) {
	tests := []struct {
		name      string
		queryType string
		raw       string
		valid     bool
	}{
		{name: "inner nonempty outer empty", raw: `{"queryText":"1","queryType":"table"}`},
		{name: "inner empty outer nonempty", queryType: "table", raw: `{"queryText":"1","queryType":""}`},
		{name: "case mismatch", queryType: "table", raw: `{"queryText":"1","queryType":"Table"}`},
		{name: "Unicode normalization mismatch", queryType: "cafe\u0301", raw: `{"queryText":"1","queryType":"caf\u00e9"}`},
		{name: "outer surrounding whitespace", queryType: " table", raw: `{"queryText":"1"}`},
		{name: "outer control", queryType: "tab\u0001le", raw: `{"queryText":"1"}`},
		{name: "inner control", queryType: "tab\u0001le", raw: `{"queryText":"1","queryType":"tab\u0001le"}`},
		{name: "outer invalid UTF-8", queryType: string([]byte{'t', 0xff}), raw: `{"queryText":"1"}`},
		{name: "oversized", queryType: strings.Repeat("t", maxQueryTypeBytes+1), raw: `{"queryText":"1"}`},
		{name: "exact Unicode", queryType: "tabela-żąć", raw: `{"queryText":"1","compatibilityMode":"panopticon","queryType":"tabela-żąć"}`, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds, calls := admissionTestDatasource()
			response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
				Queries: []backend.DataQuery{{
					RefID:     "A",
					QueryType: test.queryType,
					JSON:      json.RawMessage(test.raw),
				}},
			})
			if err != nil {
				t.Fatalf("query type admission returned top-level error: %v", err)
			}
			if test.valid {
				if result := response.Responses["A"]; result.Error != nil {
					t.Fatalf("valid query type failed admission: %v", result.Error)
				}
				if got := calls.Load(); got != 1 {
					t.Fatalf("valid query type executed %d q calls, want 1", got)
				}
				return
			}
			assertValidationResponses(t, response, "A")
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid query type executed %d q calls", got)
			}
		})
	}
}

func TestQueryDataRejectsOversizedJSONAndText(t *testing.T) {
	ds, calls := admissionTestDatasource()
	oversizedJSON := json.RawMessage(strings.Repeat(" ", maxQueryJSONBytes+1))
	response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: oversizedJSON}},
	})
	if err != nil {
		t.Fatalf("oversized JSON returned top-level error: %v", err)
	}
	assertValidationResponses(t, response, "A")
	if calls.Load() != 0 {
		t.Fatal("oversized JSON reached q")
	}

	model := QueryModel{
		QueryText:              strings.Repeat("q", maxQueryTextBytes+1),
		ExecutionMode:          ExecutionModeSync,
		CompatibilityMode:      CompatibilityModeNative,
		LegacyAsyncRequestMode: LegacyAsyncRequestModeRequestDict,
		Timeout:                defaultQueryTimeout,
		PollIntervalMs:         defaultPollIntervalMs,
		MaxStreamRows:          defaultMaxStreamRows,
	}
	if err := validateNormalizedQueryModel(model); err == nil {
		t.Fatal("oversized normalized queryText was accepted")
	}
}

func TestQueryDataRejectsInvalidNormalizedSemantics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "blank query", raw: `{"queryText":" \n\t"}`},
		{name: "timeout zero", raw: `{"queryText":"1","timeOut":0}`},
		{name: "timeout negative", raw: `{"queryText":"1","timeOut":-1}`},
		{name: "timeout over cap", raw: fmt.Sprintf(`{"queryText":"1","timeOut":%d}`, maxQueryTimeoutMs+1)},
		{name: "timeout max int", raw: fmt.Sprintf(`{"queryText":"1","timeOut":%d}`, int64(math.MaxInt64))},
		{name: "poll zero", raw: `{"queryText":"1","pollIntervalMs":0}`},
		{name: "poll below safe minimum", raw: fmt.Sprintf(`{"queryText":"1","pollIntervalMs":%d}`, minPollIntervalMs-1)},
		{name: "poll over cap", raw: fmt.Sprintf(`{"queryText":"1","pollIntervalMs":%d}`, maxPollIntervalMs+1)},
		{name: "stream rows zero", raw: `{"queryText":"1","maxStreamRows":0}`},
		{name: "stream rows negative", raw: `{"queryText":"1","maxStreamRows":-1}`},
		{name: "stream rows over cap", raw: fmt.Sprintf(`{"queryText":"1","maxStreamRows":%d}`, maxStreamRows+1)},
		{name: "retention negative", raw: `{"queryText":"1","streamRetentionMs":-1}`},
		{name: "retention over cap", raw: fmt.Sprintf(`{"queryText":"1","streamRetentionMs":%d}`, maxStreamRetentionMs+1)},
		{name: "execution enum", raw: `{"queryText":"1","executionMode":"SYNC"}`},
		{name: "wrong endpoint", raw: `{"queryText":"1","executionMode":"async"}`},
		{name: "compatibility enum", raw: `{"queryText":"1","compatibilityMode":"Native"}`},
		{name: "legacy request enum", raw: `{"queryText":"1","legacyAsyncRequestMode":"request"}`},
		{name: "cache mode enum", raw: `{"queryText":"1","queryCacheMode":"cached"}`},
		{name: "cache key enum", raw: `{"queryText":"1","queryCacheKeyMode":"strict "}`},
		{name: "cache TTL zero", raw: `{"queryText":"1","queryCacheTTLSeconds":0}`},
		{name: "cache TTL negative", raw: `{"queryText":"1","queryCacheTTLSeconds":-1}`},
		{name: "cache TTL over cap", raw: fmt.Sprintf(`{"queryText":"1","queryCacheTTLSeconds":%d}`, maxQueryCacheTTLSeconds+1)},
		{name: "stale negative", raw: `{"queryText":"1","queryCacheStaleTTLSeconds":-1}`},
		{name: "stale over cap", raw: fmt.Sprintf(`{"queryText":"1","queryCacheStaleTTLSeconds":%d}`, maxQueryCacheTTLSeconds+1)},
		{name: "bucket negative", raw: `{"queryText":"1","queryCacheTimeBucketSeconds":-1}`},
		{name: "bucket over cap", raw: fmt.Sprintf(`{"queryText":"1","queryCacheTimeBucketSeconds":%d}`, maxCacheTimeBucketSeconds+1)},
		{name: "missing time column", raw: `{"queryText":"1","useTimeColumn":true,"timeColumn":" "}`},
		{name: "bad Panopticon wrapper", raw: `{"queryText":"1","compatibilityMode":"panopticon","panopticonQueryWrapper":".pano.run[]"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds, calls := admissionTestDatasource()
			response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
				Queries: []backend.DataQuery{{RefID: "A", JSON: []byte(test.raw)}},
			})
			if err != nil {
				t.Fatalf("query validation returned top-level error: %v", err)
			}
			assertValidationResponses(t, response, "A")
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid semantics executed %d q calls", got)
			}
		})
	}
}

func TestQueryDataRejectsInvalidSDKQueryBounds(t *testing.T) {
	now := time.Now().UTC()
	validJSON := json.RawMessage(`{"queryText":"1","compatibilityMode":"panopticon"}`)
	tests := []struct {
		name      string
		configure func(*backend.DataQuery)
	}{
		{name: "negative max data points", configure: func(q *backend.DataQuery) { q.MaxDataPoints = -1 }},
		{name: "max data points over cap", configure: func(q *backend.DataQuery) { q.MaxDataPoints = maxQueryMaxDataPoints + 1 }},
		{name: "negative interval", configure: func(q *backend.DataQuery) { q.Interval = -1 }},
		{name: "interval over cap", configure: func(q *backend.DataQuery) { q.Interval = maxQueryInterval + 1 }},
		{name: "partial time range", configure: func(q *backend.DataQuery) { q.TimeRange.From = now }},
		{name: "reversed time range", configure: func(q *backend.DataQuery) {
			q.TimeRange.From = now
			q.TimeRange.To = now.Add(-time.Second)
		}},
		{name: "time range below q timestamp minimum", configure: func(q *backend.DataQuery) {
			q.TimeRange.From = minQTimestamp.Add(-time.Nanosecond)
			q.TimeRange.To = now
		}},
		{name: "time range above q timestamp maximum", configure: func(q *backend.DataQuery) {
			q.TimeRange.From = now
			q.TimeRange.To = maxQTimestamp.Add(time.Nanosecond)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds, calls := admissionTestDatasource()
			query := backend.DataQuery{RefID: "A", JSON: validJSON}
			test.configure(&query)
			response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{Queries: []backend.DataQuery{query}})
			if err != nil {
				t.Fatalf("query validation returned top-level error: %v", err)
			}
			assertValidationResponses(t, response, "A")
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid SDK query bounds executed %d q calls", got)
			}
		})
	}
}

func TestQueryDataAdmissionIsAllOrNothing(t *testing.T) {
	ds, calls := admissionTestDatasource()
	response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{
			{RefID: "A", JSON: json.RawMessage(`{"queryText":"1","compatibilityMode":"panopticon"}`)},
			{RefID: "B", JSON: json.RawMessage(`{"queryText":" ","compatibilityMode":"panopticon"}`)},
		},
	})
	if err != nil {
		t.Fatalf("all-or-nothing rejection returned top-level error: %v", err)
	}
	assertValidationResponses(t, response, "A", "B")
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid sibling allowed %d q calls", got)
	}
}

func TestQueryDataAcceptsGrafanaEnvelopeDefaultsAndBoundaries(t *testing.T) {
	ds, calls := admissionTestDatasource()
	now := time.Now().UTC()
	raw := fmt.Sprintf(`{
		"queryText":"1",
		"timeOut":%d,
		"executionMode":"sync",
		"compatibilityMode":"panopticon",
		"legacyAsyncRequestMode":"requestDict",
		"pollIntervalMs":%d,
		"maxStreamRows":%d,
		"streamRetentionMs":%d,
		"queryCacheMode":"default",
		"queryCacheKeyMode":"default",
		"queryCacheTTLSeconds":%d,
		"queryCacheStaleTTLSeconds":0,
		"queryCacheTimeBucketSeconds":0,
		"datasource":{"type":"greg-kdb-datasource","uid":"test"},
		"refId":"A",
		"hide":false,
		"queryType":"table"
	}`, maxQueryTimeoutMs, minPollIntervalMs, maxStreamRows, maxStreamRetentionMs, maxQueryCacheTTLSeconds)
	response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:         "A",
			QueryType:     "table",
			MaxDataPoints: maxQueryMaxDataPoints,
			Interval:      maxQueryInterval,
			TimeRange:     backend.TimeRange{From: now, To: now},
			JSON:          json.RawMessage(raw),
		}},
	})
	if err != nil {
		t.Fatalf("valid boundary query returned top-level error: %v", err)
	}
	if result := response.Responses["A"]; result.Error != nil {
		t.Fatalf("valid boundary query failed: %v", result.Error)
	}
	if calls.Load() != 1 {
		t.Fatalf("valid boundary query executed %d q calls, want 1", calls.Load())
	}

	var observedTimeout atomic.Int64
	ds.RunKdbQuerySync = func(_ context.Context, _ *kdb.K, timeout time.Duration, _ ...interface{}) (*kdb.K, error) {
		observedTimeout.Store(timeout.Milliseconds())
		return kdb.Long(2), nil
	}
	defaultResponse, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "D", JSON: json.RawMessage(`{"queryText":"1","compatibilityMode":"panopticon"}`)}},
	})
	if err != nil || defaultResponse.Responses["D"].Error != nil {
		t.Fatalf("omitted defaults were rejected: response=%#v error=%v", defaultResponse, err)
	}
	if observedTimeout.Load() != defaultQueryTimeout {
		t.Fatalf("omitted timeout resolved to %dms, want %dms", observedTimeout.Load(), defaultQueryTimeout)
	}
}

func TestCheckHealthRejectsMalformedLongShapesWithoutPanic(t *testing.T) {
	tests := []struct {
		name     string
		response *kdb.K
	}{
		{name: "nil response", response: nil},
		{name: "nil long data", response: &kdb.K{Type: -kdb.KJ, Data: nil}},
		{name: "wrong long data type", response: &kdb.K{Type: -kdb.KJ, Data: "2"}},
		{name: "wrong integer width", response: &kdb.K{Type: -kdb.KJ, Data: int32(2)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := &KdbDatasource{}
			ds.setupKdbConnectionHandlers()
			ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
				return test.response, nil
			}
			result, err := ds.CheckHealth(context.Background(), nil)
			if err != nil {
				t.Fatalf("malformed health response returned handler error: %v", err)
			}
			if result == nil || result.Status != backend.HealthStatusError {
				t.Fatalf("malformed health response result = %#v", result)
			}
		})
	}
}

func TestCheckHealthRejectsNilDatasourceWithoutPanic(t *testing.T) {
	var ds *KdbDatasource
	result, err := ds.CheckHealth(context.Background(), nil)
	if err == nil || !backend.IsPluginError(err) {
		t.Fatalf("nil datasource error = %v, want plugin-sourced error", err)
	}
	if result == nil || result.Status != backend.HealthStatusError {
		t.Fatalf("nil datasource result = %#v, want health error", result)
	}
	if result.Message != healthDatasourceUnavailableMessage {
		t.Fatalf("nil datasource message = %q, want %q", result.Message, healthDatasourceUnavailableMessage)
	}
}

func TestCheckHealthRejectsInvalidPluginContextBeforeTransport(t *testing.T) {
	tests := []struct {
		name    string
		context backend.PluginContext
	}{
		{
			name: "oversized user name",
			context: backend.PluginContext{
				User: &backend.User{Name: strings.Repeat("u", maxPluginContextStringBytes+1)},
			},
		},
		{
			name: "control in datasource UID",
			context: backend.PluginContext{
				DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{UID: "uid\x00suffix"},
			},
		},
		{
			name: "invalid UTF-8 datasource URL",
			context: backend.PluginContext{
				DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{URL: string([]byte{'h', 0xff})},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			ds := &KdbDatasource{}
			ds.setupKdbConnectionHandlers()
			ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
				calls.Add(1)
				return kdb.Long(2), nil
			}
			result, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{PluginContext: test.context})
			if err != nil {
				t.Fatalf("invalid context returned handler error: %v", err)
			}
			if result == nil || result.Status != backend.HealthStatusError {
				t.Fatalf("invalid context result = %#v, want health error", result)
			}
			if result.Message != healthRequestValidationMessage {
				t.Fatalf("invalid context message = %q, want %q", result.Message, healthRequestValidationMessage)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid context performed %d transport calls", got)
			}
		})
	}
}

func FuzzDecodeExactQueryModelDoesNotPanic(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"queryText":"1"}`),
		[]byte(`{"queryText":"1","queryText":"2"}`),
		[]byte(`{"QueryText":"1"}`),
		[]byte(`null`),
		[]byte(`{`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxQueryJSONBytes+1 {
			return
		}
		_, _, _ = decodeExactQueryModel(raw)
	})
}

func admissionTestDatasource() (*KdbDatasource, *atomic.Int64) {
	var calls atomic.Int64
	ds := &KdbDatasource{
		QueryCacheEnabled:    false,
		queryCacheConfigured: true,
	}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		calls.Add(1)
		return kdb.Long(2), nil
	}
	return ds, &calls
}

func assertValidationResponses(t *testing.T, response *backend.QueryDataResponse, refIDs ...string) {
	t.Helper()
	if response == nil {
		t.Fatal("validation rejection returned nil response")
	}
	if len(response.Responses) != len(refIDs) {
		t.Fatalf("validation response count = %d, want %d", len(response.Responses), len(refIDs))
	}
	for _, refID := range refIDs {
		result, ok := response.Responses[refID]
		if !ok {
			t.Fatalf("missing validation response for %q", refID)
		}
		if result.Error == nil {
			t.Fatalf("validation response %q has no error", refID)
		}
		if result.Status != backend.StatusValidationFailed {
			t.Fatalf("validation response %q status = %v, want %v", refID, result.Status, backend.StatusValidationFailed)
		}
		if result.ErrorSource != backend.ErrorSourcePlugin {
			t.Fatalf("validation response %q source = %v, want %v", refID, result.ErrorSource, backend.ErrorSourcePlugin)
		}
	}
}

func assertSpecsCoverType(t *testing.T, model reflect.Type, specs map[string]jsonFieldSpec, extra map[string]struct{}) {
	t.Helper()
	expected := make(map[string]struct{}, model.NumField()+len(extra))
	for index := 0; index < model.NumField(); index++ {
		tag := model.Field(index).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		expected[name] = struct{}{}
	}
	for name := range extra {
		expected[name] = struct{}{}
	}
	for name := range expected {
		if _, ok := specs[name]; !ok {
			t.Errorf("%s JSON field %q has no exact-decoder spec", model.Name(), name)
		}
	}
	for name := range specs {
		if _, ok := expected[name]; !ok {
			t.Errorf("exact-decoder spec %q does not correspond to %s or an approved envelope field", name, model.Name())
		}
	}
}
