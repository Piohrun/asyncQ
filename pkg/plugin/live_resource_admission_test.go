package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type discardStreamPacketSender struct{}

func (discardStreamPacketSender) Send(*backend.StreamPacket) error { return nil }

type resourceJSONHiddenEmbedded struct {
	Payload string `json:"payload"`
}

type ResourceJSONExportedEmbedded struct {
	Payload string `json:"payload"`
}

type resourceJSONPointerOnlyEmbedded struct {
	called *bool
}

func (value *resourceJSONPointerOnlyEmbedded) MarshalJSON() ([]byte, error) {
	*value.called = true
	return json.Marshal("pointer-only")
}

type resourceJSONSafeByte byte
type resourceJSONSafeBytes []resourceJSONSafeByte

type resourceJSONCustomByte byte

var resourceJSONCustomByteCalls atomic.Int64

func (resourceJSONCustomByte) MarshalJSON() ([]byte, error) {
	resourceJSONCustomByteCalls.Add(1)
	return json.Marshal(strings.Repeat("x", maxResourceResponseJSON+1))
}

type resourceJSONPointerCustomByte byte

var resourceJSONPointerCustomByteCalls atomic.Int64

func (*resourceJSONPointerCustomByte) MarshalJSON() ([]byte, error) {
	resourceJSONPointerCustomByteCalls.Add(1)
	return json.Marshal(strings.Repeat("x", maxResourceResponseJSON+1))
}

type resourceJSONTextByte byte

var resourceJSONTextByteCalls atomic.Int64

func (resourceJSONTextByte) MarshalText() ([]byte, error) {
	resourceJSONTextByteCalls.Add(1)
	return bytes.Repeat([]byte{'x'}, maxResourceResponseJSON+1), nil
}

type resourceJSONPointerTextByte byte

var resourceJSONPointerTextByteCalls atomic.Int64

func (*resourceJSONPointerTextByte) MarshalText() ([]byte, error) {
	resourceJSONPointerTextByteCalls.Add(1)
	return bytes.Repeat([]byte{'x'}, maxResourceResponseJSON+1), nil
}

type resourceJSONTextMapKey int

var resourceJSONTextMapKeyCalls atomic.Int64

func (resourceJSONTextMapKey) MarshalText() ([]byte, error) {
	resourceJSONTextMapKeyCalls.Add(1)
	return bytes.Repeat([]byte{'k'}, maxResourceResponseJSON+1), nil
}

type resourceJSONStringMapKey string

var resourceJSONStringMapKeyCalls atomic.Int64

func (resourceJSONStringMapKey) MarshalText() ([]byte, error) {
	resourceJSONStringMapKeyCalls.Add(1)
	return bytes.Repeat([]byte{'k'}, maxResourceResponseJSON+1), nil
}

type resourceJSONPointerTextMapKey int

var resourceJSONPointerTextMapKeyCalls atomic.Int64

func (*resourceJSONPointerTextMapKey) MarshalText() ([]byte, error) {
	resourceJSONPointerTextMapKeyCalls.Add(1)
	return bytes.Repeat([]byte{'k'}, maxResourceResponseJSON+1), nil
}

func resourceJSONPointer[T any](value T) *T {
	return &value
}

func validLiveRequestJSON(mode string) json.RawMessage {
	execution := ""
	if mode != "" {
		execution = `,"executionMode":` + strconvQuote(mode)
	}
	return json.RawMessage(`{
		"queryText":"select from trade",
		"refId":"A",
		"key":"dashboard-panel-A",
		"hide":false,
		"queryType":"table",
		"datasource":{"type":"asyncq-kdbbackend-datasource","uid":"asyncq-main","apiVersion":"v1"},
		"maxDataPoints":500,
			"intervalMs":1000,
			"pollIntervalMs":250,
		"maxStreamRows":1000,
		"streamRetentionMs":0,
		"timeRange":{"from":"2026-07-28T10:00:00Z","to":"2026-07-28T11:00:00Z"}` + execution + `
	}`)
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestLiveHandlersAreNilSafe(t *testing.T) {
	var nilDatasource *KdbDatasource
	if response, err := nilDatasource.SubscribeStream(nil, nil); err == nil || response == nil || response.Status != backend.SubscribeStreamStatusPermissionDenied {
		t.Fatalf("nil datasource SubscribeStream response=%#v error=%v", response, err)
	}
	if response, err := nilDatasource.PublishStream(nil, nil); err == nil || response == nil || response.Status != backend.PublishStreamStatusPermissionDenied {
		t.Fatalf("nil datasource PublishStream response=%#v error=%v", response, err)
	}
	if err := nilDatasource.RunStream(nil, nil, nil); err == nil {
		t.Fatal("nil datasource RunStream succeeded")
	}
	if err := nilDatasource.CallResource(nil, nil, nil); err == nil {
		t.Fatal("nil datasource CallResource succeeded")
	}

	ds := &KdbDatasource{}
	if response, err := ds.SubscribeStream(nil, nil); err == nil || response.Status != backend.SubscribeStreamStatusNotFound {
		t.Fatalf("nil request SubscribeStream response=%#v error=%v", response, err)
	}
	if response, err := ds.PublishStream(nil, nil); err == nil || response.Status != backend.PublishStreamStatusNotFound {
		t.Fatalf("nil request PublishStream response=%#v error=%v", response, err)
	}
	if err := ds.RunStream(nil, nil, backend.NewStreamSender(discardStreamPacketSender{})); err == nil {
		t.Fatal("nil request RunStream succeeded")
	}
	if err := ds.RunStream(nil, &backend.RunStreamRequest{}, nil); err == nil {
		t.Fatal("nil sender RunStream succeeded")
	}
	if err := ds.CallResource(nil, nil, backend.CallResourceResponseSenderFunc(func(*backend.CallResourceResponse) error { return nil })); err == nil {
		t.Fatal("nil request CallResource succeeded")
	}
	if err := ds.CallResource(nil, &backend.CallResourceRequest{}, nil); err == nil {
		t.Fatal("nil sender CallResource succeeded")
	}
	var typedNil backend.CallResourceResponseSenderFunc
	if err := ds.CallResource(nil, &backend.CallResourceRequest{}, typedNil); err == nil {
		t.Fatal("typed nil sender CallResource succeeded")
	}
}

func TestLivePathAdmissionIsCanonicalAndBounded(t *testing.T) {
	validID := strings.Repeat("a", maxLiveIDBytes)
	for _, path := range []string{"async/" + validID, "stream/A._~-09"} {
		if _, _, err := admitLivePath(path); err != nil {
			t.Fatalf("valid path %q rejected: %v", path, err)
		}
	}
	for _, path := range []string{
		"",
		"async",
		"async/",
		"/async/A",
		" async/A",
		"async/A ",
		"async/A/B",
		"async/.",
		"async/..",
		"async/A%2FB",
		"async/A?x=1",
		"async/A#fragment",
		"ASYNC/A",
		"other/A",
		"stream/" + strings.Repeat("a", maxLiveIDBytes+1),
		string([]byte{'a', 's', 'y', 'n', 'c', '/', 0xff}),
	} {
		if _, _, err := admitLivePath(path); err == nil {
			t.Fatalf("invalid path %q was accepted", path)
		}
	}
}

func TestLiveAdmissionRejectsExactJSONViolations(t *testing.T) {
	ds := &KdbDatasource{}
	tests := []string{
		``,
		`null`,
		`[]`,
		`{"queryText":"1","refId":"A"} true`,
		`{"queryText":"1","queryText":"2","refId":"A"}`,
		`{"queryText":"1","refId":"A","RefId":"A"}`,
		`{"queryText":"1","refId":"A","unknown":true}`,
		`{"queryText":null,"refId":"A"}`,
		`{"queryText":"1","refId":null}`,
		`{"queryText":"1","refId":"A","timeRange":null}`,
		`{"queryText":"1","refId":"A","timeRange":{}}`,
		`{"queryText":"1","refId":"A","timeRange":{"from":"2026-01-01T00:00:00Z"}}`,
		`{"queryText":"1","refId":"A","timeRange":{"from":"2026-01-01T00:00:00Z","to":"2026-01-02T00:00:00Z","extra":1}}`,
		`{"queryText":"1","refId":"A","datasource":{"type":"x","type":"y"}}`,
		`{"queryText":"1","refId":"A","datasource":{"Type":"x"}}`,
		`{"queryText":"1","refId":"A","datasource":{"future":"x"}}`,
	}
	for index, raw := range tests {
		if _, err := ds.admitLiveQueryRequest(backend.PluginContext{}, json.RawMessage(raw), "async/id"); err == nil {
			t.Fatalf("invalid live JSON %d was accepted: %s", index, raw)
		}
	}
}

func TestLiveAdmissionModeResolutionAndPathParity(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		explicitMode   string
		datasourceMode string
		wantMode       string
		valid          bool
	}{
		{name: "stream omitted", path: "stream/id", wantMode: ExecutionModeStream, valid: true},
		{name: "stream explicit", path: "stream/id", explicitMode: ExecutionModeStream, wantMode: ExecutionModeStream, valid: true},
		{name: "stream contradicts async", path: "stream/id", explicitMode: ExecutionModeAsync},
		{name: "async helper explicit", path: "async/id", explicitMode: ExecutionModeAsync, wantMode: ExecutionModeAsync, valid: true},
		{name: "async plugin", path: "async/id", explicitMode: ExecutionModePluginAsync, wantMode: ExecutionModePluginAsync, valid: true},
		{name: "async deferred", path: "async/id", explicitMode: ExecutionModeDeferredAsync},
		{name: "async legacy", path: "async/id", explicitMode: ExecutionModeLegacyAsync, wantMode: ExecutionModeLegacyAsync, valid: true},
		{name: "async contradicts sync", path: "async/id", explicitMode: ExecutionModeSync},
		{name: "async contradicts stream", path: "async/id", explicitMode: ExecutionModeStream},
		{name: "async uses datasource strategy", path: "async/id", datasourceMode: ExecutionModePluginAsync, wantMode: ExecutionModePluginAsync, valid: true},
		{name: "async falls back from datasource sync", path: "async/id", datasourceMode: ExecutionModeSync, wantMode: ExecutionModeAsync, valid: true},
		{name: "async falls back from datasource stream", path: "async/id", datasourceMode: ExecutionModeStream, wantMode: ExecutionModeAsync, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := &KdbDatasource{ExecutionMode: test.datasourceMode}
			raw := validLiveRequestJSON(test.explicitMode)
			if test.explicitMode == ExecutionModeDeferredAsync {
				raw = json.RawMessage(strings.Replace(string(raw), `"executionMode":"deferredAsync"`, `"executionMode":"deferredAsync","deferredQueryWrapper":"defer[{Query}]"`, 1))
				test.valid = true
				test.wantMode = ExecutionModeDeferredAsync
			}
			admitted, err := ds.admitLiveQueryRequest(backend.PluginContext{}, raw, test.path)
			if !test.valid {
				if err == nil {
					t.Fatalf("contradictory mode was accepted: %#v", admitted)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid mode rejected: %v", err)
			}
			if admitted.model.ExecutionMode != test.wantMode {
				t.Fatalf("execution mode=%q want=%q", admitted.model.ExecutionMode, test.wantMode)
			}
		})
	}
}

func TestSubscribeAndRunRejectTheSameMalformedPayload(t *testing.T) {
	ds := &KdbDatasource{}
	raw := json.RawMessage(`{"queryText":"1","refId":"A","intervalMs":-1}`)
	subscribe, subscribeErr := ds.SubscribeStream(context.Background(), &backend.SubscribeStreamRequest{
		Path: "async/id",
		Data: raw,
	})
	if subscribeErr == nil || subscribe.Status != backend.SubscribeStreamStatusPermissionDenied {
		t.Fatalf("SubscribeStream response=%#v error=%v", subscribe, subscribeErr)
	}
	runErr := ds.RunStream(context.Background(), &backend.RunStreamRequest{
		Path: "async/id",
		Data: raw,
	}, backend.NewStreamSender(discardStreamPacketSender{}))
	if runErr == nil {
		t.Fatal("RunStream accepted payload rejected by SubscribeStream")
	}
}

func TestInvalidLiveRequestDoesNotOpenTransport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse listener port: %v", err)
	}
	ds := &KdbDatasource{Host: host, Port: port, DialTimeout: time.Second}
	err = ds.RunStream(context.Background(), &backend.RunStreamRequest{
		Path: "async/id",
		Data: json.RawMessage(`{"queryText":"1","refId":"A","intervalMs":-1}`),
	}, backend.NewStreamSender(discardStreamPacketSender{}))
	if err == nil {
		t.Fatal("invalid request was accepted")
	}
	tcpListener := listener.(*net.TCPListener)
	if err := tcpListener.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set listener deadline: %v", err)
	}
	connection, err := tcpListener.Accept()
	if err == nil {
		_ = connection.Close()
		t.Fatal("invalid request opened a transport")
	}
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("unexpected accept error: %v", err)
	}
}

func TestLiveAdmissionValidatesBoundsAndTimes(t *testing.T) {
	ds := &KdbDatasource{}
	replacements := []struct {
		old string
		new string
	}{
		{`"maxDataPoints":500`, `"maxDataPoints":-1`},
		{`"maxDataPoints":500`, `"maxDataPoints":10000001`},
		{`"intervalMs":1000`, `"intervalMs":-1`},
		{`"intervalMs":1000`, `"intervalMs":31536000001`},
		{`"pollIntervalMs":250`, `"pollIntervalMs":99`},
		{`"maxStreamRows":1000`, `"maxStreamRows":1000001`},
		{`"streamRetentionMs":0`, `"streamRetentionMs":604800001`},
		{`"from":"2026-07-28T10:00:00Z"`, `"from":"not-a-time"`},
		{`"from":"2026-07-28T10:00:00Z","to":"2026-07-28T11:00:00Z"`, `"from":"2026-07-28T12:00:00Z","to":"2026-07-28T11:00:00Z"`},
	}
	valid := string(validLiveRequestJSON(ExecutionModeAsync))
	for _, replacement := range replacements {
		raw := strings.Replace(valid, replacement.old, replacement.new, 1)
		if _, err := ds.admitLiveQueryRequest(backend.PluginContext{}, json.RawMessage(raw), "async/id"); err == nil {
			t.Fatalf("invalid bound replacement %q was accepted", replacement.new)
		}
	}
}

func TestLiveFeatureFlagsAreAppliedAfterAdmission(t *testing.T) {
	ds := &KdbDatasource{asyncConfigured: true, streamConfigured: true}
	for _, test := range []struct {
		path string
		mode string
	}{
		{path: "async/id", mode: ExecutionModeAsync},
		{path: "stream/id", mode: ExecutionModeStream},
	} {
		response, err := ds.SubscribeStream(context.Background(), &backend.SubscribeStreamRequest{
			Path: test.path,
			Data: validLiveRequestJSON(test.mode),
		})
		if err == nil || response.Status != backend.SubscribeStreamStatusPermissionDenied {
			t.Fatalf("disabled %s response=%#v error=%v", test.mode, response, err)
		}
	}
}

func TestAsyncResourceFeatureFlagIsAppliedAfterAdmission(t *testing.T) {
	ds := &KdbDatasource{asyncConfigured: true}
	valid := callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:    "async/run-and-wait",
		Method:  http.MethodPost,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    []byte(`{"queryText":"1","executionMode":"pluginAsync"}`),
	})
	if valid.Status != http.StatusForbidden {
		t.Fatalf("disabled async resource status=%d want=%d", valid.Status, http.StatusForbidden)
	}
	invalid := callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:    "async/run-and-wait",
		Method:  http.MethodPost,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    []byte(`{"queryText":"1","unknown":true}`),
	})
	if invalid.Status != http.StatusBadRequest {
		t.Fatalf("invalid disabled async request status=%d want=%d", invalid.Status, http.StatusBadRequest)
	}
}

func TestResourceAdmissionMethodPathBodyAndHeaders(t *testing.T) {
	ds := &KdbDatasource{}
	for path, endpoint := range resourceEndpoints {
		wrongMethod := http.MethodPost
		if endpoint.method == http.MethodPost {
			wrongMethod = http.MethodGet
		}
		response := callResourceForTest(t, ds, &backend.CallResourceRequest{Path: path, Method: wrongMethod})
		if response.Status != http.StatusMethodNotAllowed {
			t.Fatalf("%s wrong method status=%d", path, response.Status)
		}
		if got := response.Headers["allow"]; len(got) != 1 || got[0] != endpoint.method {
			t.Fatalf("%s Allow=%v want=%s", path, got, endpoint.method)
		}
		assertSecureJSONHeaders(t, response)
		var errorBody map[string]json.RawMessage
		if err := json.Unmarshal(response.Body, &errorBody); err != nil {
			t.Fatalf("%s error response was not JSON: %v", path, err)
		}
		if _, present := errorBody["status"]; present {
			t.Fatalf("%s generic error response contained an unrelated cache status", path)
		}
	}

	for _, path := range []string{" cache/status", "/cache/status", "cache/status/", "cache/status?x=1", "cache//status", "report", "unknown"} {
		response := callResourceForTest(t, ds, &backend.CallResourceRequest{Path: path, Method: http.MethodGet})
		if response.Status != http.StatusNotFound {
			t.Fatalf("invalid path %q status=%d", path, response.Status)
		}
	}
	response := callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:   "cache/status",
		Method: http.MethodGet,
		Body:   []byte(" "),
	})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("GET body status=%d", response.Status)
	}
	response = callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:   "cache/status",
		Method: http.MethodGet,
		URL:    strings.Repeat("x", maxResourceURLBytes+1),
	})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("oversized URL status=%d", response.Status)
	}
	response = callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:   "async/run-and-wait",
		Method: http.MethodPost,
		Body:   bytes.Repeat([]byte{' '}, maxQueryJSONBytes+1),
	})
	if response.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized async body status=%d", response.Status)
	}
	response = callResourceForTest(t, ds, &backend.CallResourceRequest{
		Path:   "report/generate",
		Method: http.MethodPost,
		Body:   bytes.Repeat([]byte{' '}, maxResourceJSONBytes+1),
	})
	if response.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized report body status=%d", response.Status)
	}
}

func TestResourceAdmissionRequiresAnExactSupportedContentType(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		headers   map[string][]string
		wantMedia string
		wantOK    bool
	}{
		{
			name:      "JSON",
			path:      "cache/clear",
			headers:   map[string][]string{"Content-Type": {"application/json"}},
			wantMedia: resourceJSONMediaType,
			wantOK:    true,
		},
		{
			name:      "JSON UTF-8 charset",
			path:      "cache/clear",
			headers:   map[string][]string{"content-type": {"Application/JSON; Charset=UTF-8"}},
			wantMedia: resourceJSONMediaType,
			wantOK:    true,
		},
		{
			name:      "report form",
			path:      "report/generate",
			headers:   map[string][]string{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}},
			wantMedia: resourceFormMediaType,
			wantOK:    true,
		},
		{name: "missing", path: "cache/clear"},
		{name: "empty", path: "cache/clear", headers: map[string][]string{"Content-Type": {""}}},
		{
			name:    "multiple values",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"application/json", "application/json"}},
		},
		{
			name:    "comma joined values",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"application/json, application/json"}},
		},
		{
			name:    "unsupported media type",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"text/plain"}},
		},
		{
			name:    "form on JSON-only endpoint",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}},
		},
		{
			name:    "unsupported charset",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"application/json; charset=iso-8859-1"}},
		},
		{
			name:    "extra parameter",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {"application/json; charset=utf-8; version=1"}},
		},
		{
			name:    "malformed",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {`application/json; charset="`}},
		},
		{
			name:    "oversized",
			path:    "cache/clear",
			headers: map[string][]string{"Content-Type": {strings.Repeat("x", maxResourceContentTypeBytes+1)}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, status, err := admitResourceRequest(&backend.CallResourceRequest{
				Path:    test.path,
				Method:  http.MethodPost,
				Headers: test.headers,
				Body:    []byte(`{}`),
			})
			if test.wantOK {
				if err != nil || status != http.StatusOK || endpoint.mediaType != test.wantMedia {
					t.Fatalf("admission endpoint=%#v status=%d error=%v", endpoint, status, err)
				}
				return
			}
			if err == nil || status != http.StatusUnsupportedMediaType || endpoint.mediaType != "" {
				t.Fatalf("unsupported Content-Type endpoint=%#v status=%d error=%v", endpoint, status, err)
			}
		})
	}
}

func TestCacheResourceDecoderRequiresExactExplicitScopeAndKey(t *testing.T) {
	validKey := strings.Repeat("a", 64)
	for _, test := range []struct {
		path string
		raw  string
	}{
		{path: "cache/clear", raw: `{}`},
		{path: "cache/clear", raw: `{"scope":null}`},
		{path: "cache/clear", raw: `{"Scope":"both"}`},
		{path: "cache/clear", raw: `{"scope":"BOTH"}`},
		{path: "cache/clear", raw: `{"scope":"both","scope":"memory"}`},
		{path: "cache/clear", raw: `{"scope":"both","key":"` + validKey + `"}`},
		{path: "cache/clear-entry", raw: `{"scope":"both"}`},
		{path: "cache/clear-entry", raw: `{"scope":"both","key":"ABC"}`},
		{path: "cache/clear-entry", raw: `{"scope":"both","key":"` + strings.Repeat("a", 16) + `"}`},
		{path: "cache/clear-entry", raw: `{"scope":"both","key":"` + validKey + `","extra":true}`},
		{path: "cache/clear-expired", raw: `{"scope":"both"} trailing`},
	} {
		var target cacheResourceRequest
		if err := decodeCacheResourceRequest([]byte(test.raw), test.path, &target); err == nil {
			t.Fatalf("%s accepted invalid body %s", test.path, test.raw)
		}
	}
	for _, path := range []string{"cache/clear", "cache/clear-expired"} {
		var target cacheResourceRequest
		if err := decodeCacheResourceRequest([]byte(`{"scope":"both"}`), path, &target); err != nil {
			t.Fatalf("%s rejected valid body: %v", path, err)
		}
	}
	var target cacheResourceRequest
	if err := decodeCacheResourceRequest([]byte(`{"scope":"disk","key":"`+validKey+`"}`), "cache/clear-entry", &target); err != nil {
		t.Fatalf("clear-entry rejected valid body: %v", err)
	}
}

func TestResourceRolesAndReportValidationAccess(t *testing.T) {
	ds := cachedTestDatasource()
	for _, test := range []struct {
		path   string
		role   string
		status int
	}{
		{path: "cache/status", role: "Viewer", status: http.StatusOK},
		{path: "cache/entries", role: "Viewer", status: http.StatusForbidden},
		{path: "cache/entries", role: "Editor", status: http.StatusOK},
		{path: "report/validate", role: "Viewer", status: http.StatusForbidden},
		{path: "report/validate", role: "Admin", status: http.StatusOK},
	} {
		response := callResourceForTest(t, ds, &backend.CallResourceRequest{
			PluginContext: backend.PluginContext{User: &backend.User{Role: test.role}},
			Path:          test.path,
			Method:        http.MethodGet,
		})
		if response.Status != test.status {
			t.Fatalf("%s role=%s status=%d want=%d", test.path, test.role, response.Status, test.status)
		}
	}
	response := callResourceForTest(t, ds, &backend.CallResourceRequest{
		PluginContext: backend.PluginContext{User: &backend.User{Role: " Editor "}},
		Path:          "cache/entries",
		Method:        http.MethodGet,
	})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("ambiguous role status=%d", response.Status)
	}
}

func TestSendResourceJSONRejectsOversizedPayloadBeforeSend(t *testing.T) {
	called := false
	err := sendResourceJSON(
		backend.CallResourceResponseSenderFunc(func(*backend.CallResourceResponse) error {
			called = true
			return nil
		}),
		http.StatusOK,
		strings.Repeat("x", maxResourceResponseJSON),
	)
	if err == nil {
		t.Fatal("oversized JSON response was accepted")
	}
	if called {
		t.Fatal("oversized JSON response reached the sender")
	}
}

type resourceJSONMarshalProbe struct {
	called *bool
}

func (probe resourceJSONMarshalProbe) MarshalJSON() ([]byte, error) {
	*probe.called = true
	return []byte(`"unexpected"`), nil
}

func TestResourceJSONPreflightHonorsExactLimitAndEscaping(t *testing.T) {
	const limit = 64
	exact := strings.Repeat("x", limit-2)
	if size, err := estimateResourceJSONSize(exact, limit); err != nil || size != limit {
		t.Fatalf("exact-limit string size=%d error=%v", size, err)
	}
	if _, err := estimateResourceJSONSize(exact+"x", limit); err == nil {
		t.Fatal("one-byte-over-limit string was accepted")
	}

	controlHeavy := "\x00\"\\<&é"
	size, err := estimateResourceJSONSize(controlHeavy, 1024)
	if err != nil {
		t.Fatalf("control-heavy string was rejected: %v", err)
	}
	marshaled, err := json.Marshal(controlHeavy)
	if err != nil {
		t.Fatalf("marshal control-heavy string: %v", err)
	}
	if size < int64(len(marshaled)) {
		t.Fatalf("control-heavy estimate=%d is smaller than encoded size=%d", size, len(marshaled))
	}
	if _, err := estimateResourceJSONSize(controlHeavy, int(size)-1); err == nil {
		t.Fatal("control-heavy string exceeded the estimate without rejection")
	}

	invalidUTF8 := string([]byte{0xff, 'x'})
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "ampersands", value: strings.Repeat("&", 96)},
		{name: "quotes", value: strings.Repeat(`"`, 96)},
		{name: "slashes", value: strings.Repeat("/", 96)},
		{name: "backslashes", value: strings.Repeat(`\`, 96)},
		{name: "controls", value: strings.Repeat("\x00\b\f\n\r\t\x1f", 16)},
		{name: "U+2028", value: strings.Repeat("\u2028", 96)},
		{name: "U+2029", value: strings.Repeat("\u2029", 96)},
		{name: "non-ASCII", value: strings.Repeat("zażółć", 32)},
		{name: "invalid UTF-8", value: strings.Repeat(invalidUTF8, 48)},
	} {
		t.Run("quoted "+test.name, func(t *testing.T) {
			quoted := struct {
				Value string `json:"value,string"`
			}{Value: test.value}
			quotedJSON, err := json.Marshal(quoted)
			if err != nil {
				t.Fatalf("marshal quoted string: %v", err)
			}
			quotedSize, err := estimateResourceJSONSize(quoted, len(quotedJSON))
			if err != nil {
				t.Fatalf("exact-limit quoted string was rejected: %v", err)
			}
			if quotedSize != int64(len(quotedJSON)) {
				t.Fatalf("quoted string estimate=%d want exact encoded size=%d", quotedSize, len(quotedJSON))
			}
			if _, err := estimateResourceJSONSize(quoted, len(quotedJSON)-1); err == nil {
				t.Fatal("quoted string one byte beyond the limit was accepted")
			}

			value := struct {
				Text    string  `json:"text,string"`
				Pointer *string `json:"pointer,string"`
				Bool    bool    `json:"bool,string"`
				Int     int64   `json:"int,string"`
				Uint    uint64  `json:"uint,string"`
				Float   float64 `json:"float,string"`
			}{
				Text:    test.value,
				Pointer: resourceJSONPointer(test.value),
				Bool:    false,
				Int:     math.MinInt64,
				Uint:    math.MaxUint64,
				Float:   math.MaxFloat64,
			}
			assertResourceJSONEstimateCoversMarshal(t, value)
		})
	}

	scannerInput := strings.Repeat("<&\"\\\u2028zażółć", 32) + invalidUTF8
	var scannerErr error
	allocations := testing.AllocsPerRun(1000, func() {
		_, _, scannerErr = resourceJSONStringEncodingSizes(scannerInput)
	})
	if scannerErr != nil {
		t.Fatalf("quoted-string size scanner failed: %v", scannerErr)
	}
	if allocations != 0 {
		t.Fatalf("quoted-string size scanner allocations=%v want=0", allocations)
	}
}

func TestResourceJSONPreflightRejectsCyclesNilFramesAndCustomMarshalers(t *testing.T) {
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	if _, err := estimateResourceJSONSize(cycle, maxResourceResponseJSON); err == nil {
		t.Fatal("cyclic response was accepted")
	}
	if _, err := estimateResourceJSONSize(data.Frames{nil}, maxResourceResponseJSON); err == nil {
		t.Fatal("nil data frame was accepted")
	}
	if _, err := estimateResourceJSONSize(data.Frames{data.NewFrame("nil-field", nil)}, maxResourceResponseJSON); err == nil {
		t.Fatal("nil data frame field was accepted")
	}
	tooManyFields := &data.Frame{
		Name:   "too-many-fields",
		Fields: make([]*data.Field, maxResourceJSONFields+1),
	}
	if _, err := estimateResourceJSONSize(data.Frames{tooManyFields}, maxResourceResponseJSON); err == nil ||
		!strings.Contains(err.Error(), "data frame fields") {
		t.Fatalf("frame field-count limit did not win before field traversal: %v", err)
	}

	marshalCalled := false
	sendCalled := false
	err := sendResourceJSON(
		backend.CallResourceResponseSenderFunc(func(*backend.CallResourceResponse) error {
			sendCalled = true
			return nil
		}),
		http.StatusOK,
		resourceJSONMarshalProbe{called: &marshalCalled},
	)
	if err == nil {
		t.Fatal("unsupported custom marshaler was accepted")
	}
	if marshalCalled {
		t.Fatal("custom marshaler ran during response preflight")
	}
	if sendCalled {
		t.Fatal("response with a custom marshaler reached the sender")
	}
}

func TestResourceJSONPreflightRejectsUnexportedAnonymousStructPromotion(t *testing.T) {
	hidden := struct {
		resourceJSONHiddenEmbedded
	}{
		resourceJSONHiddenEmbedded: resourceJSONHiddenEmbedded{
			Payload: strings.Repeat("x", maxResourceResponseJSON+1),
		},
	}
	if _, err := estimateResourceJSONSize(hidden, maxResourceResponseJSON); err == nil {
		t.Fatal("unexported anonymous embedded struct was accepted")
	}
	hiddenPointer := struct {
		*resourceJSONHiddenEmbedded
	}{
		resourceJSONHiddenEmbedded: &resourceJSONHiddenEmbedded{Payload: "promoted"},
	}
	if _, err := estimateResourceJSONSize(hiddenPointer, maxResourceResponseJSON); err == nil {
		t.Fatal("unexported anonymous embedded struct pointer was accepted")
	}

	type hiddenProbeContainer struct {
		Probe resourceJSONMarshalProbe `json:"probe"`
	}
	type hiddenProbeOuter struct {
		hiddenProbeContainer
	}
	marshalCalled := false
	probed := hiddenProbeOuter{
		hiddenProbeContainer: hiddenProbeContainer{
			Probe: resourceJSONMarshalProbe{called: &marshalCalled},
		},
	}
	if _, err := estimateResourceJSONSize(probed, maxResourceResponseJSON); err == nil {
		t.Fatal("hidden promoted marshal probe was accepted")
	}
	if marshalCalled {
		t.Fatal("hidden promoted marshal probe ran during preflight")
	}
}

func TestResourceJSONPreflightHandlesIgnoredAndExportedEmbeddedFieldsConservatively(t *testing.T) {
	type ignoredNamedField struct {
		hidden   resourceJSONHiddenEmbedded
		optional string
		Public   string `json:"public"`
	}
	assertResourceJSONEstimateCoversMarshal(t, ignoredNamedField{
		hidden: resourceJSONHiddenEmbedded{
			Payload: strings.Repeat("ignored", 100),
		},
		Public: "visible",
	})

	type exportedOuter struct {
		ResourceJSONExportedEmbedded
		Optional string `json:"optional,omitempty"`
	}
	assertResourceJSONEstimateCoversMarshal(t, exportedOuter{
		ResourceJSONExportedEmbedded: ResourceJSONExportedEmbedded{Payload: "<visible>"},
	})
	type invalidTag struct {
		LongExportedFieldName string `json:"☃"`
	}
	assertResourceJSONEstimateCoversMarshal(t, invalidTag{LongExportedFieldName: "value"})

	pointerOnlyCalled := false
	type pointerOnlyOuter struct {
		resourceJSONPointerOnlyEmbedded
	}
	value := pointerOnlyOuter{
		resourceJSONPointerOnlyEmbedded: resourceJSONPointerOnlyEmbedded{called: &pointerOnlyCalled},
	}
	if _, err := estimateResourceJSONSize(value, maxResourceResponseJSON); err == nil {
		t.Fatal("address-sensitive promoted pointer marshaler was accepted")
	}
	if pointerOnlyCalled {
		t.Fatal("promoted pointer-only marshaler ran during preflight")
	}

	frameValue := *data.NewFrame("value", data.NewField("x", nil, []int64{1}))
	if _, err := estimateResourceJSONSize(frameValue, maxResourceResponseJSON); err == nil {
		t.Fatal("address-sensitive data.Frame value was accepted")
	}
}

func TestResourceJSONPreflightByteSliceMethodSetsAndArrayParity(t *testing.T) {
	assertResourceJSONEstimateCoversMarshal(t, []byte{0, 1, 2, 254, 255})
	assertResourceJSONEstimateCoversMarshal(t, resourceJSONSafeBytes{0, 1, 2, 254, 255})
	assertResourceJSONEstimateCoversMarshal(t, [5]byte{0, 1, 2, 254, 255})
	assertResourceJSONEstimateCoversMarshal(t, [5]resourceJSONSafeByte{0, 1, 2, 254, 255})

	resourceJSONCustomByteCalls.Store(0)
	if _, err := estimateResourceJSONSize([]resourceJSONCustomByte{1}, maxResourceResponseJSON); err == nil {
		t.Fatal("byte slice with value JSON marshaler was accepted")
	}
	if calls := resourceJSONCustomByteCalls.Load(); calls != 0 {
		t.Fatalf("custom byte JSON marshaler calls=%d want=0", calls)
	}

	resourceJSONPointerCustomByteCalls.Store(0)
	if _, err := estimateResourceJSONSize([]resourceJSONPointerCustomByte{1}, maxResourceResponseJSON); err == nil {
		t.Fatal("byte slice with pointer JSON marshaler was accepted")
	}
	if calls := resourceJSONPointerCustomByteCalls.Load(); calls != 0 {
		t.Fatalf("custom pointer byte JSON marshaler calls=%d want=0", calls)
	}

	resourceJSONTextByteCalls.Store(0)
	if _, err := estimateResourceJSONSize([]resourceJSONTextByte{1}, maxResourceResponseJSON); err == nil {
		t.Fatal("byte slice with value TextMarshaler was accepted")
	}
	if calls := resourceJSONTextByteCalls.Load(); calls != 0 {
		t.Fatalf("custom byte text marshaler calls=%d want=0", calls)
	}

	resourceJSONPointerTextByteCalls.Store(0)
	if _, err := estimateResourceJSONSize([]resourceJSONPointerTextByte{1}, maxResourceResponseJSON); err == nil {
		t.Fatal("byte slice with pointer TextMarshaler was accepted")
	}
	if calls := resourceJSONPointerTextByteCalls.Load(); calls != 0 {
		t.Fatalf("custom byte text marshaler calls=%d want=0", calls)
	}
}

func TestResourceJSONPreflightMapKeyParityAndNoCustomKeyInvocation(t *testing.T) {
	assertResourceJSONEstimateCoversMarshal(t, map[int64]string{
		-1 << 63:  "minimum",
		1<<63 - 1: "maximum",
	})
	assertResourceJSONEstimateCoversMarshal(t, map[uint64]string{
		0:          "zero",
		^uint64(0): "maximum",
	})
	resourceJSONStringMapKeyCalls.Store(0)
	assertResourceJSONEstimateCoversMarshal(t, map[resourceJSONStringMapKey]string{
		"<direct>": "value",
	})
	if calls := resourceJSONStringMapKeyCalls.Load(); calls != 0 {
		t.Fatalf("string-kind map-key TextMarshaler calls=%d want=0", calls)
	}
	resourceJSONPointerTextMapKeyCalls.Store(0)
	assertResourceJSONEstimateCoversMarshal(t, map[resourceJSONPointerTextMapKey]string{
		1: "integer encoding",
	})
	if calls := resourceJSONPointerTextMapKeyCalls.Load(); calls != 0 {
		t.Fatalf("pointer-only integer map-key TextMarshaler calls=%d want=0", calls)
	}

	resourceJSONTextMapKeyCalls.Store(0)
	if _, err := estimateResourceJSONSize(
		map[resourceJSONTextMapKey]string{1: "value"},
		maxResourceResponseJSON,
	); err == nil {
		t.Fatal("map with custom text key encoder was accepted")
	}
	if calls := resourceJSONTextMapKeyCalls.Load(); calls != 0 {
		t.Fatalf("custom map-key marshaler calls=%d want=0", calls)
	}
}

func TestResourceJSONPreflightRawMessageDepthAndSyntax(t *testing.T) {
	depth32 := json.RawMessage(strings.Repeat("[", maxResourceJSONDepth) + "null" + strings.Repeat("]", maxResourceJSONDepth))
	assertResourceJSONEstimateCoversMarshal(t, depth32)

	depth33 := json.RawMessage(strings.Repeat("[", maxResourceJSONDepth+1) + "null" + strings.Repeat("]", maxResourceJSONDepth+1))
	if _, err := estimateResourceJSONSize(depth33, maxResourceResponseJSON); err == nil {
		t.Fatal("raw JSON one level beyond the depth limit was accepted")
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(""),
		json.RawMessage("null null"),
		json.RawMessage(`{"missing":}`),
		json.RawMessage(`[1,]`),
		json.RawMessage(`{"unterminated":"value}`),
	} {
		if _, err := estimateResourceJSONSize(raw, maxResourceResponseJSON); err == nil {
			t.Fatalf("malformed or trailing raw JSON was accepted: %q", raw)
		}
	}
}

func TestResourceJSONPreflightRawMessageSharedCounterBoundaries(t *testing.T) {
	newBudget := func() *resourceJSONBudget {
		return &resourceJSONBudget{
			limit:  maxResourceResponseJSON,
			active: make(map[resourceJSONVisit]struct{}),
		}
	}

	nodeExact := newBudget()
	nodeExact.nodes = maxResourceJSONNodes - 2
	if err := nodeExact.addRawJSON(json.RawMessage(`[null]`), 1); err != nil {
		t.Fatalf("exact raw JSON node boundary rejected: %v", err)
	}
	if nodeExact.nodes != maxResourceJSONNodes {
		t.Fatalf("raw JSON nodes=%d want=%d", nodeExact.nodes, maxResourceJSONNodes)
	}
	nodeOver := newBudget()
	nodeOver.nodes = maxResourceJSONNodes - 1
	if err := nodeOver.addRawJSON(json.RawMessage(`[null]`), 1); err == nil {
		t.Fatal("raw JSON node boundary +1 was accepted")
	}

	arrayExact := newBudget()
	arrayExact.collectionItems = maxResourceJSONCollectionItems - 1
	if err := arrayExact.addRawJSON(json.RawMessage(`[null]`), 1); err != nil {
		t.Fatalf("exact raw JSON array-item boundary rejected: %v", err)
	}
	if arrayExact.collectionItems != maxResourceJSONCollectionItems {
		t.Fatalf("raw JSON collection items=%d want=%d", arrayExact.collectionItems, maxResourceJSONCollectionItems)
	}
	arrayOver := newBudget()
	arrayOver.collectionItems = maxResourceJSONCollectionItems
	if err := arrayOver.addRawJSON(json.RawMessage(`[null]`), 1); err == nil {
		t.Fatal("raw JSON array-item boundary +1 was accepted")
	}

	mapExact := newBudget()
	mapExact.mapEntries = maxResourceJSONMapEntries - 1
	if err := mapExact.addRawJSON(json.RawMessage(`{"key":null}`), 1); err != nil {
		t.Fatalf("exact raw JSON map-entry boundary rejected: %v", err)
	}
	if mapExact.mapEntries != maxResourceJSONMapEntries {
		t.Fatalf("raw JSON map entries=%d want=%d", mapExact.mapEntries, maxResourceJSONMapEntries)
	}
	mapOver := newBudget()
	mapOver.mapEntries = maxResourceJSONMapEntries
	if err := mapOver.addRawJSON(json.RawMessage(`{"key":null}`), 1); err == nil {
		t.Fatal("raw JSON map-entry boundary +1 was accepted")
	}

	mapCollectionExact := newBudget()
	mapCollectionExact.collectionItems = maxResourceJSONCollectionItems - 1
	if err := mapCollectionExact.addRawJSON(json.RawMessage(`{"key":null}`), 1); err != nil {
		t.Fatalf("exact raw JSON map collection boundary rejected: %v", err)
	}
	mapCollectionOver := newBudget()
	mapCollectionOver.collectionItems = maxResourceJSONCollectionItems
	if err := mapCollectionOver.addRawJSON(json.RawMessage(`{"key":null}`), 1); err == nil {
		t.Fatal("raw JSON map collection boundary +1 was accepted")
	}
}

func TestResourceJSONPreflightRawMessageRealCollectionBoundaries(t *testing.T) {
	exactArray := resourceJSONArrayOfNulls(maxResourceJSONCollectionItems)
	if _, err := estimateResourceJSONSize(exactArray, maxResourceResponseJSON); err != nil {
		t.Fatalf("raw JSON array at the collection limit was rejected: %v", err)
	}
	overArray := resourceJSONArrayOfNulls(maxResourceJSONCollectionItems + 1)
	if _, err := estimateResourceJSONSize(overArray, maxResourceResponseJSON); err == nil {
		t.Fatal("raw JSON array one item beyond the collection limit was accepted")
	}

	exactMap := resourceJSONObjectOfNulls(maxResourceJSONMapEntries)
	if _, err := estimateResourceJSONSize(exactMap, maxResourceResponseJSON); err != nil {
		t.Fatalf("raw JSON object at the map-entry limit was rejected: %v", err)
	}
	overMap := resourceJSONObjectOfNulls(maxResourceJSONMapEntries + 1)
	if _, err := estimateResourceJSONSize(overMap, maxResourceResponseJSON); err == nil {
		t.Fatal("raw JSON object one entry beyond the map-entry limit was accepted")
	}
}

func TestResourceJSONPreflightRawMessageSizeAndSDKEmbedding(t *testing.T) {
	raw := json.RawMessage("{\"html\":\"<>&\",\"line\":\"\u2028\",\"nested\":[1,true,null]}")
	assertResourceJSONEstimateCoversMarshal(t, raw)

	budget := &resourceJSONBudget{
		limit:  8,
		used:   1,
		active: make(map[resourceJSONVisit]struct{}),
	}
	tooLargeAndInvalid := json.RawMessage{'x', 'x', 'x', 'x', 'x', 'x', 'x', 0xff}
	err := budget.addRawJSON(tooLargeAndInvalid, 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds") || strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("large raw JSON was not rejected by the early size check: %v", err)
	}
	if budget.nodes != 0 || budget.collectionItems != 0 || budget.mapEntries != 0 {
		t.Fatalf("oversized raw JSON was structurally decoded: %#v", budget)
	}

	hostileLength := maxResourceResponseJSON/6 + 1
	hostile := make(json.RawMessage, hostileLength+2)
	hostile[0] = '"'
	hostile[len(hostile)-1] = '"'
	for index := 1; index < len(hostile)-1; index++ {
		hostile[index] = '&'
	}
	hostileBudget := &resourceJSONBudget{
		limit:  maxResourceResponseJSON,
		active: make(map[resourceJSONVisit]struct{}),
	}
	if err := hostileBudget.addRawJSON(hostile, 1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("hostile escaped-size raw JSON was not rejected before parsing: %v", err)
	}
	if hostileBudget.used != 0 || hostileBudget.nodes != 0 ||
		hostileBudget.collectionItems != 0 || hostileBudget.mapEntries != 0 {
		t.Fatalf("hostile escaped-size raw JSON changed structural counters: %#v", hostileBudget)
	}

	frame := data.NewFrame(
		"raw",
		data.NewField("payload", nil, []json.RawMessage{
			json.RawMessage(`{"index":0,"value":"<&"}`),
			json.RawMessage(`[1,2,3]`),
		}),
	)
	frame.Meta = &data.FrameMeta{
		Custom: json.RawMessage(`{"metadata":{"safe":true,"values":[null,1]}}`),
	}
	assertResourceJSONEstimateCoversMarshal(t, data.Frames{frame})
}

func TestResourceJSONPreflightRejectsInvalidUTF8RawMessageBeforeAccounting(t *testing.T) {
	invalid := json.RawMessage{'"', 0xff, '"'}
	budget := &resourceJSONBudget{
		limit:           maxResourceResponseJSON,
		used:            17,
		nodes:           19,
		collectionItems: 23,
		mapEntries:      29,
		active:          make(map[resourceJSONVisit]struct{}),
	}
	if err := budget.addRawJSON(invalid, 1); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 raw JSON was not rejected explicitly: %v", err)
	}
	if budget.used != 17 || budget.nodes != 19 || budget.collectionItems != 23 || budget.mapEntries != 29 {
		t.Fatalf("invalid UTF-8 raw JSON changed budget counters: %#v", budget)
	}

	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "direct", value: invalid},
		{
			name: "SDK field JSON",
			value: data.Frames{data.NewFrame(
				"invalid-field-json",
				data.NewField("payload", nil, []json.RawMessage{invalid}),
			)},
		},
		{
			name: "frame metadata custom",
			value: data.Frames{func() *data.Frame {
				frame := data.NewFrame("invalid-meta")
				frame.Meta = &data.FrameMeta{Custom: invalid}
				return frame
			}()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := estimateResourceJSONSize(test.value, maxResourceResponseJSON); err == nil ||
				!strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("invalid UTF-8 RawMessage embedding was accepted: %v", err)
			}
		})
	}

	oversizedInvalid := json.RawMessage(bytes.Repeat([]byte{'x'}, 2048))
	oversizedInvalid[len(oversizedInvalid)-1] = 0xff
	oversizedRoutes := []struct {
		name  string
		value interface{}
	}{
		{name: "direct", value: oversizedInvalid},
		{
			name: "SDK field JSON",
			value: data.Frames{data.NewFrame(
				"oversized-invalid-field-json",
				data.NewField("payload", nil, []json.RawMessage{oversizedInvalid}),
			)},
		},
		{
			name: "frame metadata custom",
			value: data.Frames{func() *data.Frame {
				frame := data.NewFrame("oversized-invalid-meta")
				frame.Meta = &data.FrameMeta{Custom: oversizedInvalid}
				return frame
			}()},
		},
	}
	for _, test := range oversizedRoutes {
		t.Run("oversized "+test.name, func(t *testing.T) {
			if _, err := estimateResourceJSONSize(test.value, 1024); err == nil ||
				!strings.Contains(err.Error(), "exceeds") ||
				strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("oversized invalid RawMessage did not report size first: %v", err)
			}
		})
	}
}

func TestResourceJSONPreflightAcceptsNormalFramesAndCacheResponses(t *testing.T) {
	mapper := data.ValueMapper{
		"1": {Text: "one", Color: "green"},
	}
	frame := data.NewFrame(
		"response",
		data.NewField("value", data.Labels{"host": "alpha"}, []float64{1.25, 2.5}),
		data.NewField("message", nil, []string{"ok", "<ready>"}),
	)
	frame.RefID = "A"
	frame.Meta = &data.FrameMeta{
		Custom:  map[string]interface{}{"asyncqState": "done"},
		Notices: []data.Notice{{Severity: data.NoticeSeverityInfo, Text: "complete"}},
	}
	frame.Fields[0].Config = &data.FieldConfig{
		Unit:     "ms",
		Mappings: data.ValueMappings{&mapper},
	}
	asyncResponse := asyncRunAndWaitResponse{
		OK:        true,
		RequestID: "request",
		Statuses: []asyncRunAndWaitStatusEvent{{
			State:    "done",
			Progress: 1,
			Final:    true,
		}},
		Frames: data.Frames{frame},
	}
	asyncEstimate, err := estimateResourceJSONSize(asyncResponse, maxResourceResponseJSON)
	if err != nil {
		t.Fatalf("normal async frame response was rejected: %v", err)
	}
	asyncJSON, err := json.Marshal(asyncResponse)
	if err != nil {
		t.Fatalf("marshal normal async frame response: %v", err)
	}
	if asyncEstimate < int64(len(asyncJSON)) {
		t.Fatalf("async estimate=%d is smaller than encoded size=%d", asyncEstimate, len(asyncJSON))
	}

	cacheResponse := cacheResourceResponse{
		OK: true,
		Status: &syncQueryCacheStatus{
			Enabled: true,
			Memory: syncQueryMemoryStatus{
				Enabled: true,
				Keys: []syncQueryCacheKeyInfo{{
					Key:     strings.Repeat("a", 64),
					Storage: "memory",
					RefID:   "A",
				}},
			},
		},
	}
	cacheEstimate, err := estimateResourceJSONSize(&cacheResponse, maxResourceResponseJSON)
	if err != nil {
		t.Fatalf("normal cache response was rejected: %v", err)
	}
	cacheJSON, err := json.Marshal(&cacheResponse)
	if err != nil {
		t.Fatalf("marshal normal cache response: %v", err)
	}
	if cacheEstimate < int64(len(cacheJSON)) {
		t.Fatalf("cache estimate=%d is smaller than encoded size=%d", cacheEstimate, len(cacheJSON))
	}
}

func TestResourceJSONPreflightRejectsLargeFramesAndBoundedCollections(t *testing.T) {
	values := make([]float64, maxResourceResponseJSON/resourceJSONFloatCellBytes+1)
	largeFrame := data.NewFrame("large", data.NewField("value", nil, values))
	if _, err := estimateResourceJSONSize(data.Frames{largeFrame}, maxResourceResponseJSON); err == nil {
		t.Fatal("large numeric frame was accepted")
	}

	status := syncQueryCacheStatus{
		Memory: syncQueryMemoryStatus{
			Keys: make([]syncQueryCacheKeyInfo, maxResourceJSONCacheKeys+1),
		},
	}
	if _, err := estimateResourceJSONSize(status, maxResourceResponseJSON); err == nil {
		t.Fatal("cache response with too many keys was accepted")
	}

	asyncResponse := asyncRunAndWaitResponse{
		Statuses: make([]asyncRunAndWaitStatusEvent, maxAsyncStatusEvents+1),
	}
	if _, err := estimateResourceJSONSize(asyncResponse, maxResourceResponseJSON); err == nil {
		t.Fatal("async response with too many status events was accepted")
	}
}

func TestResourceJSONPreflightCoversEveryNullableSDKFieldEncoding(t *testing.T) {
	timestamp := time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	raw := json.RawMessage(`{"nested":[null,"<&",{"ok":true}]}`)
	enum := data.EnumItemIndex(^uint16(0))
	tests := []struct {
		name  string
		field *data.Field
	}{
		{name: "int8", field: data.NewField("value", nil, []*int8{resourceJSONPointer(int8(-128)), nil})},
		{name: "int16", field: data.NewField("value", nil, []*int16{resourceJSONPointer(int16(-32768)), nil})},
		{name: "int32", field: data.NewField("value", nil, []*int32{resourceJSONPointer(int32(-2147483648)), nil})},
		{name: "int64", field: data.NewField("value", nil, []*int64{resourceJSONPointer(int64(-1 << 63)), nil})},
		{name: "uint8", field: data.NewField("value", nil, []*uint8{resourceJSONPointer(^uint8(0)), nil})},
		{name: "uint16", field: data.NewField("value", nil, []*uint16{resourceJSONPointer(^uint16(0)), nil})},
		{name: "uint32", field: data.NewField("value", nil, []*uint32{resourceJSONPointer(^uint32(0)), nil})},
		{name: "uint64", field: data.NewField("value", nil, []*uint64{resourceJSONPointer(^uint64(0)), nil})},
		{name: "float32", field: data.NewField("value", nil, []*float32{resourceJSONPointer(float32(math.Inf(1))), nil})},
		{name: "float64", field: data.NewField("value", nil, []*float64{resourceJSONPointer(math.NaN()), nil})},
		{name: "bool", field: data.NewField("value", nil, []*bool{resourceJSONPointer(false), nil})},
		{name: "time", field: data.NewField("value", nil, []*time.Time{&timestamp, nil})},
		{name: "string", field: data.NewField("value", nil, []*string{resourceJSONPointer("<&\x00é"), nil})},
		{name: "json", field: data.NewField("value", nil, []*json.RawMessage{&raw, nil})},
		{name: "enum", field: data.NewField("value", nil, []*data.EnumItemIndex{&enum, nil})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frames := data.Frames{data.NewFrame(test.name, test.field)}
			assertResourceJSONEstimateCoversMarshal(t, frames)
		})
	}
}

func TestResourceJSONPreflightDistinguishesNilAndEmptySDKWrappers(t *testing.T) {
	var nilFrames data.Frames
	nilSize, err := estimateResourceJSONSize(nilFrames, maxResourceResponseJSON)
	if err != nil || nilSize != 4 {
		t.Fatalf("nil Frames size=%d error=%v want=4", nilSize, err)
	}
	assertResourceJSONEstimateCoversMarshal(t, nilFrames)

	emptyFrames := data.Frames{}
	emptySize, err := estimateResourceJSONSize(emptyFrames, maxResourceResponseJSON)
	if err != nil || emptySize != 2 {
		t.Fatalf("empty Frames size=%d error=%v want=2", emptySize, err)
	}
	assertResourceJSONEstimateCoversMarshal(t, emptyFrames)

	var nilFramePointer *data.Frames
	assertResourceJSONEstimateCoversMarshal(t, nilFramePointer)
	assertResourceJSONEstimateCoversMarshal(t, &nilFrames)
	assertResourceJSONEstimateCoversMarshal(t, data.Labels(nil))
	var nilLabels *data.Labels
	assertResourceJSONEstimateCoversMarshal(t, nilLabels)
	assertResourceJSONEstimateCoversMarshal(t, data.ValueMappings(nil))
	var nilMappings *data.ValueMappings
	assertResourceJSONEstimateCoversMarshal(t, nilMappings)

	var nilMapper data.ValueMapper
	assertResourceJSONEstimateCoversMarshal(t, data.ValueMappings{nilMapper})
	var typedNilValue *data.ValueMapper
	var typedNilRange *data.RangeValueMapper
	var typedNilSpecial *data.SpecialValueMapper
	for name, typedNilMapping := range map[string]data.ValueMapping{
		"value":   typedNilValue,
		"range":   typedNilRange,
		"special": typedNilSpecial,
	} {
		if _, err := estimateResourceJSONSize(data.ValueMappings{typedNilMapping}, maxResourceResponseJSON); err == nil {
			t.Fatalf("interface-held typed-nil %s value mapping was accepted", name)
		}
	}
}

func TestResourceJSONPreflightClosesNullableFrameLimitGapBeforeMarshal(t *testing.T) {
	const rows = maxResourceJSONCells
	paddingLength := maxResourceResponseJSON - 4*rows
	if paddingLength <= 0 {
		t.Fatal("test assumptions no longer produce positive padding")
	}
	frame := data.NewFrame(
		strings.Repeat("p", paddingLength),
		data.NewField("nullable", nil, make([]*string, rows)),
	)
	frames := data.Frames{frame}

	correctedEstimate, err := estimateResourceJSONSize(frames, maxResourceResponseJSON+8*rows)
	if err != nil {
		t.Fatalf("estimate padded frame with diagnostic headroom: %v", err)
	}
	oldEstimate := correctedEstimate - int64((resourceJSONStringCellBytes-3)*rows)
	if oldEstimate > maxResourceResponseJSON {
		t.Fatalf("old estimate reproduction=%d exceeds limit=%d", oldEstimate, maxResourceResponseJSON)
	}
	actual, err := json.Marshal(frames)
	if err != nil {
		t.Fatalf("marshal padded frame reproduction: %v", err)
	}
	if len(actual) <= maxResourceResponseJSON {
		t.Fatalf("padded frame actual size=%d did not exceed limit=%d", len(actual), maxResourceResponseJSON)
	}

	marshalCalled := false
	sendCalled := false
	envelope := struct {
		Frames data.Frames              `json:"frames"`
		Probe  resourceJSONMarshalProbe `json:"probe"`
	}{
		Frames: frames,
		Probe:  resourceJSONMarshalProbe{called: &marshalCalled},
	}
	err = sendResourceJSON(
		backend.CallResourceResponseSenderFunc(func(*backend.CallResourceResponse) error {
			sendCalled = true
			return nil
		}),
		http.StatusOK,
		&envelope,
	)
	if err == nil {
		t.Fatal("padded nullable frame response was accepted")
	}
	if marshalCalled {
		t.Fatal("JSON marshaling began before the padded frame was rejected")
	}
	if sendCalled {
		t.Fatal("padded nullable frame reached the response sender")
	}
}

func TestResourceJSONPreflightRepresentativeAcceptedValuesCoverMarshal(t *testing.T) {
	type representative struct {
		Bool       bool                   `json:"bool"`
		Integer    int64                  `json:"integer"`
		Unsigned   uint64                 `json:"unsigned"`
		Float      float64                `json:"float"`
		Text       string                 `json:"text"`
		When       time.Time              `json:"when"`
		Bytes      []byte                 `json:"bytes"`
		Raw        json.RawMessage        `json:"raw"`
		StringMap  map[string]interface{} `json:"stringMap"`
		IntegerMap map[int]string         `json:"integerMap"`
		List       []interface{}          `json:"list"`
		Quoted     string                 `json:"quoted,string"`
		Optional   *string                `json:"optional,omitempty"`
	}
	values := []interface{}{
		true,
		int64(-1 << 63),
		^uint64(0),
		1.7976931348623157e+308,
		"<control>\x00é",
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		[]byte{0, 1, 2, 255},
		[]interface{}{"value", int64(-1), true, nil},
		map[string]interface{}{"nested": []interface{}{1, "two", false}},
		map[int]string{-1: "negative", 1: "positive"},
		representative{
			Bool:     true,
			Integer:  -1 << 63,
			Unsigned: ^uint64(0),
			Float:    1.7976931348623157e+308,
			Text:     "<&>",
			When:     time.Date(2026, 7, 29, 12, 0, 0, 1, time.UTC),
			Bytes:    []byte{0, 255},
			Raw:      json.RawMessage(`{"ok":true}`),
			StringMap: map[string]interface{}{
				"list": []interface{}{nil, "value"},
			},
			IntegerMap: map[int]string{-1: "negative"},
			List:       []interface{}{uint64(1), "two"},
			Quoted:     "\"quoted\\\x00",
		},
	}
	for _, value := range values {
		assertResourceJSONEstimateCoversMarshal(t, value)
	}
}

func TestResourceJSONPreflightRejectsInvalidFramesPointerCyclesAndArithmeticOverflow(t *testing.T) {
	invalidFrame := data.NewFrame(
		"invalid",
		data.NewField("one", nil, []int64{1}),
		data.NewField("two", nil, []int64{1, 2}),
	)
	if _, err := estimateResourceJSONSize(data.Frames{invalidFrame}, maxResourceResponseJSON); err == nil {
		t.Fatal("frame with unequal field lengths was accepted")
	}

	type cyclicNode struct {
		Next *cyclicNode `json:"next"`
	}
	node := &cyclicNode{}
	node.Next = node
	if _, err := estimateResourceJSONSize(node, maxResourceResponseJSON); err == nil {
		t.Fatal("pointer cycle was accepted")
	}

	budget := &resourceJSONBudget{limit: math.MaxInt64}
	if err := budget.addRepeated(math.MaxInt64, 2); err == nil || budget.used != 0 {
		t.Fatalf("multiplication overflow changed budget: used=%d error=%v", budget.used, err)
	}
	if err := budget.addSize(math.MaxInt64); err != nil {
		t.Fatalf("exact maximum budget rejected: %v", err)
	}
	if err := budget.addSize(1); err == nil || budget.used != math.MaxInt64 {
		t.Fatalf("addition overflow changed budget: used=%d error=%v", budget.used, err)
	}
}

func TestAsyncStatusTimelineIsBoundedCoalescedAndFinal(t *testing.T) {
	response := asyncRunAndWaitResponse{RequestID: "request"}
	start := time.Now()
	for index := 0; index < maxAsyncStatusEvents*2; index++ {
		response.addStatus(start, fmt.Sprintf("state-%d", index), asyncQStatus{
			ID:        strings.Repeat("j", 500),
			RawStatus: strings.Repeat("r", 500),
			Message:   strings.Repeat("m", 2000),
			Error:     strings.Repeat("e", 4000),
			Progress:  2,
		}, false)
	}
	response.addStatus(start, "done", asyncQStatus{ID: "job", Progress: 1}, true)
	if len(response.Statuses) != maxAsyncStatusEvents {
		t.Fatalf("status count=%d want=%d", len(response.Statuses), maxAsyncStatusEvents)
	}
	last := response.Statuses[len(response.Statuses)-1]
	if !last.Final || last.State != "done" {
		t.Fatalf("final status was not retained: %#v", last)
	}
	for _, event := range response.Statuses {
		if len(event.JobID) > maxLiveIDBytes || len(event.RawStatus) > 256 || len(event.Message) > 1024 || len(event.Error) > 2048 {
			t.Fatalf("status text was not bounded: %#v", event)
		}
		if event.Progress < 0 || event.Progress > 1 {
			t.Fatalf("progress was not clamped: %f", event.Progress)
		}
	}

	coalesced := asyncRunAndWaitResponse{}
	coalesced.addStatus(start, "running", asyncQStatus{Message: "first"}, false)
	coalesced.addStatus(start, "running", asyncQStatus{Message: "latest"}, false)
	if len(coalesced.Statuses) != 1 || coalesced.Statuses[0].Message != "latest" {
		t.Fatalf("identical consecutive state was not coalesced: %#v", coalesced.Statuses)
	}

	for _, value := range []float64{-1, 2, 0.5} {
		progress := boundedStatusProgress(value)
		if progress < 0 || progress > 1 {
			t.Fatalf("bounded progress %f is outside [0,1]", progress)
		}
	}
	if bounded := boundedStatusText(string(bytes.Repeat([]byte{0xff}, 64)), 32); len(bounded) > 32 || !utf8.ValidString(bounded) {
		t.Fatalf("invalid UTF-8 was not safely bounded: %q", bounded)
	}
}

func TestLiveFrameMetadataIsNilSafeAndBounded(t *testing.T) {
	markFrame(nil, "mode", "state", "id", false, false)
	frame := data.NewFrame("response")
	markFrame(
		frame,
		strings.Repeat("m", 100),
		strings.Repeat("s", 100),
		strings.Repeat("i", maxLiveIDBytes+100),
		true,
		false,
	)
	custom, ok := frame.Meta.Custom.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected frame metadata: %#v", frame.Meta.Custom)
	}
	if len(custom["asyncqMode"].(string)) > 32 ||
		len(custom["asyncqState"].(string)) > 32 ||
		len(custom["asyncqID"].(string)) > maxLiveIDBytes {
		t.Fatalf("live metadata was not bounded: %#v", custom)
	}
}

func TestAsyncRequestIDUsesCryptoRandomAndHandlesFailure(t *testing.T) {
	id, err := newAsyncRequestID(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)))
	if err != nil {
		t.Fatalf("newAsyncRequestID returned error: %v", err)
	}
	if id != "async-wait-"+strings.Repeat("ab", 16) {
		t.Fatalf("unexpected request ID: %q", id)
	}
	if err := validateExternalID(id, "requestId"); err != nil {
		t.Fatalf("generated request ID is not canonical: %v", err)
	}
	if _, err := newAsyncRequestID(errorReader{}); err == nil {
		t.Fatal("failing random source was ignored")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random failed") }

func TestAsyncRunAndWaitExactAdmissionAndTimeOverflow(t *testing.T) {
	ds := &KdbDatasource{}
	for _, raw := range []string{
		``,
		`null`,
		`{"queryText":"1","unknown":true}`,
		`{"queryText":"1","queryText":"2"}`,
		`{"queryText":"1","RequestId":"id"}`,
		`{"queryText":"1","requestId":null}`,
		`{"queryText":"1","requestId":"bad/id"}`,
		`{"queryText":"1","executionMode":"stream"}`,
		`{"queryText":"1","executionMode":"sync"}`,
		`{"queryText":"1","timeRange":{"from":"now-999999999999999999999d","to":"now"}}`,
		`{"queryText":"1","timeRange":{"from":"now--1h","to":"now"}}`,
		`{"queryText":"1","timeRange":{"from":"now","to":"now"}}`,
		`{"queryText":"1","intervalMs":31536000001}`,
	} {
		if _, _, _, _, err := ds.decodeAsyncRunAndWaitRequest(backend.PluginContext{}, []byte(raw)); err == nil {
			t.Fatalf("invalid async request was accepted: %s", raw)
		}
	}
	valid := []byte(`{
		"queryText":"1",
		"executionMode":"pluginAsync",
		"requestId":"request._~-1",
		"refId":"A",
		"key":"mcp-request-A",
		"hide":false,
		"queryType":"table",
		"datasource":{"type":"asyncq-kdbbackend-datasource","uid":"asyncq-main","apiVersion":"v1"},
		"maxDataPoints":0,
		"intervalMs":0,
		"timeRange":{"from":"now-1h","to":"now"}
	}`)
	_, query, model, requestID, err := ds.decodeAsyncRunAndWaitRequest(backend.PluginContext{}, valid)
	if err != nil {
		t.Fatalf("valid async request rejected: %v", err)
	}
	if model.ExecutionMode != ExecutionModePluginAsync || requestID != "request._~-1" || query.Interval != 0 || query.MaxDataPoints != 0 {
		t.Fatalf("valid boundaries changed: model=%#v requestID=%q query=%#v", model, requestID, query)
	}
}

func assertResourceJSONEstimateCoversMarshal(t *testing.T, value interface{}) {
	t.Helper()
	estimate, err := estimateResourceJSONSize(value, maxResourceResponseJSON)
	if err != nil {
		t.Fatalf("resource JSON estimate rejected %T: %v", value, err)
	}
	actual, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	if estimate < int64(len(actual)) {
		t.Fatalf("resource JSON estimate for %T=%d is smaller than encoded size=%d", value, estimate, len(actual))
	}
}

func resourceJSONArrayOfNulls(count int) json.RawMessage {
	var builder strings.Builder
	builder.Grow(2 + count*5)
	builder.WriteByte('[')
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString("null")
	}
	builder.WriteByte(']')
	return json.RawMessage(builder.String())
}

func resourceJSONObjectOfNulls(count int) json.RawMessage {
	var builder strings.Builder
	builder.Grow(2 + count*16)
	builder.WriteByte('{')
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`"key`)
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString(`":null`)
	}
	builder.WriteByte('}')
	return json.RawMessage(builder.String())
}

func callResourceForTest(t *testing.T, ds *KdbDatasource, req *backend.CallResourceRequest) *backend.CallResourceResponse {
	t.Helper()
	var response *backend.CallResourceResponse
	err := ds.CallResource(context.Background(), req, backend.CallResourceResponseSenderFunc(func(value *backend.CallResourceResponse) error {
		response = value
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}
	if response == nil {
		t.Fatal("CallResource did not send a response")
	}
	return response
}

func assertSecureJSONHeaders(t *testing.T, response *backend.CallResourceResponse) {
	t.Helper()
	for key, want := range map[string]string{
		"content-type":           "application/json",
		"cache-control":          "no-store",
		"x-content-type-options": "nosniff",
		"referrer-policy":        "no-referrer",
	} {
		if got := response.Headers[key]; len(got) != 1 || got[0] != want {
			t.Fatalf("header %s=%v want=%q", key, got, want)
		}
	}
}

func FuzzAdmitLiveQueryRequest(f *testing.F) {
	f.Add([]byte(validLiveRequestJSON(ExecutionModeAsync)), "async/id")
	f.Add([]byte(`{"queryText":"1","refId":"A"}`), "stream/id")
	f.Fuzz(func(t *testing.T, raw []byte, path string) {
		ds := &KdbDatasource{}
		_, _ = ds.admitLiveQueryRequest(backend.PluginContext{}, json.RawMessage(raw), path)
	})
}

func FuzzDecodeCacheResourceRequest(f *testing.F) {
	f.Add([]byte(`{"scope":"both"}`), "cache/clear")
	f.Add([]byte(`{"scope":"memory","key":"`+strings.Repeat("a", 64)+`"}`), "cache/clear-entry")
	f.Fuzz(func(t *testing.T, raw []byte, path string) {
		var target cacheResourceRequest
		_ = decodeCacheResourceRequest(raw, path, &target)
	})
}

func FuzzAdmitResourceRequest(f *testing.F) {
	f.Add("cache/status", http.MethodGet, "", []byte{})
	f.Add("async/run-and-wait", http.MethodPost, "", []byte(`{"queryText":"1"}`))
	f.Fuzz(func(t *testing.T, path string, method string, resourceURL string, body []byte) {
		_, _, _ = admitResourceRequest(&backend.CallResourceRequest{
			Path:   path,
			Method: method,
			URL:    resourceURL,
			Body:   body,
		})
	})
}

func FuzzResourceJSONPreflightEstimateCoversMarshal(f *testing.F) {
	f.Add("plain", []byte(`{"ok":true}`), int64(-1), uint64(1), true)
	f.Add("<&\x00é", []byte(`[null,1,"<&"]`), int64(-1<<63), ^uint64(0), false)
	f.Fuzz(func(t *testing.T, text string, raw []byte, signed int64, unsigned uint64, flag bool) {
		if len(text) > 4096 || len(raw) > 4096 {
			t.Skip()
		}
		value := struct {
			Text     string          `json:"text"`
			Quoted   string          `json:"quoted,string"`
			Raw      json.RawMessage `json:"raw"`
			Bytes    []byte          `json:"bytes"`
			Signed   int64           `json:"signed"`
			Unsigned uint64          `json:"unsigned"`
			Flag     bool            `json:"flag"`
			Map      map[int]string  `json:"map"`
			List     []interface{}   `json:"list"`
		}{
			Text:     text,
			Quoted:   text,
			Raw:      json.RawMessage(raw),
			Bytes:    append([]byte(nil), raw...),
			Signed:   signed,
			Unsigned: unsigned,
			Flag:     flag,
			Map:      map[int]string{int(signed): text},
			List:     []interface{}{text, signed, unsigned, flag},
		}
		estimate, err := estimateResourceJSONSize(value, maxResourceResponseJSON)
		if err != nil {
			return
		}
		actual, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("resource JSON preflight accepted a value that failed marshaling: %v", err)
		}
		if estimate < int64(len(actual)) {
			t.Fatalf("resource JSON estimate=%d is smaller than encoded size=%d", estimate, len(actual))
		}
	})
}
