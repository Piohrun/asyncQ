package plugin

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
)

const ADAPTOR_VERSION = float64(2.0)

const (
	ExecutionModeSync          = "sync"
	ExecutionModeAsync         = "async"
	ExecutionModePluginAsync   = "pluginAsync"
	ExecutionModeDeferredAsync = "deferredAsync"
	ExecutionModeLegacyAsync   = "legacyAsync"
	ExecutionModeStream        = "stream"

	CompatibilityModeNative     = "native"
	CompatibilityModeAquaQ      = "aquaq"
	CompatibilityModePanopticon = "panopticon"

	QueryCacheModeDefault  = "default"
	QueryCacheModeEnabled  = "enabled"
	QueryCacheModeDisabled = "disabled"
	QueryCacheModeBypass   = "bypass"
	QueryCacheModeRefresh  = "refresh"

	QueryCacheKeyModeStrict = "strict"
	QueryCacheKeyModeShared = "shared"

	LegacyAsyncRequestModeQueryText         = "queryText"
	LegacyAsyncRequestModeCompiledQueryText = "compiledQueryText"
	LegacyAsyncRequestModeRequestDict       = "requestDict"
	LegacyAsyncRequestModePanopticonDict    = "panopticonDict"

	defaultQueryTimeout         = 10000
	defaultPollIntervalMs       = 1000
	defaultMaxStreamRows        = 1000
	defaultAsyncMaxJobs         = 16
	defaultSyncMaxConnections   = 4
	defaultQueryCacheEnabled    = true
	defaultQueryCacheTTL        = 60
	defaultQueryCacheMax        = 128
	defaultQueryCacheTimeBucket = 0
	defaultQueryCacheStaleTTL   = 0
	defaultQueryCacheDisk       = true
	defaultQueryCacheDiskBytes  = int64(1024 * 1024 * 1024)
	defaultQueryCacheDiskMax    = 10000
	defaultLegacyAsyncJobIDPath = "jobId"
	defaultLegacyAsyncStatus    = "status"
	defaultLegacyAsyncProgress  = "progress"
	defaultLegacyAsyncMessage   = "message"
	defaultLegacyAsyncError     = "error"
	defaultLegacyAsyncPayload   = "result"
	defaultConnectionTimeoutMs  = 1000
	maxConnectionTimeoutMs      = 300000
	maxTLSCertificateBytes      = 1 << 20
	maxTLSPrivateKeyBytes       = 1 << 20
	maxTLSCABundleBytes         = 4 << 20
	asyncQHelperUnavailable     = "async/stream queries require q/asyncq_grafana.q to be loaded in the target kdb+ process or gateway"

	healthDatasourceUnavailableMessage = "datasource is unavailable"
	healthRequestValidationMessage     = "health check request failed validation"
)

var (
	_ backend.QueryDataHandler      = (*KdbDatasource)(nil)
	_ backend.CheckHealthHandler    = (*KdbDatasource)(nil)
	_ backend.CallResourceHandler   = (*KdbDatasource)(nil)
	_ backend.StreamHandler         = (*KdbDatasource)(nil)
	_ instancemgmt.InstanceDisposer = (*KdbDatasource)(nil)
)

type QueryModel struct {
	QueryText                   string `json:"queryText"`
	Timeout                     int    `json:"timeOut,omitempty"`
	UseTimeColumn               bool   `json:"useTimeColumn"`
	TimeColumn                  string `json:"timeColumn"`
	IncludeKeyColumns           bool   `json:"includeKeyColumns"`
	ExecutionMode               string `json:"executionMode,omitempty"`
	CompatibilityMode           string `json:"compatibilityMode,omitempty"`
	DeferredQueryWrapper        string `json:"deferredQueryWrapper,omitempty"`
	PanopticonQueryWrapper      string `json:"panopticonQueryWrapper,omitempty"`
	PanopticonRequestFunction   string `json:"panopticonRequestFunction,omitempty"`
	LegacyAsyncSubmit           string `json:"legacyAsyncSubmit,omitempty"`
	LegacyAsyncStatus           string `json:"legacyAsyncStatus,omitempty"`
	LegacyAsyncResult           string `json:"legacyAsyncResult,omitempty"`
	LegacyAsyncCancel           string `json:"legacyAsyncCancel,omitempty"`
	LegacyAsyncRequestMode      string `json:"legacyAsyncRequestMode,omitempty"`
	LegacyAsyncJobIDPath        string `json:"legacyAsyncJobIDPath,omitempty"`
	LegacyAsyncStatusPath       string `json:"legacyAsyncStatusPath,omitempty"`
	LegacyAsyncProgressPath     string `json:"legacyAsyncProgressPath,omitempty"`
	LegacyAsyncMessagePath      string `json:"legacyAsyncMessagePath,omitempty"`
	LegacyAsyncErrorPath        string `json:"legacyAsyncErrorPath,omitempty"`
	LegacyAsyncPayloadPath      string `json:"legacyAsyncPayloadPath,omitempty"`
	LegacyAsyncQueuedValues     string `json:"legacyAsyncQueuedValues,omitempty"`
	LegacyAsyncRunningValues    string `json:"legacyAsyncRunningValues,omitempty"`
	LegacyAsyncDoneValues       string `json:"legacyAsyncDoneValues,omitempty"`
	LegacyAsyncErrorValues      string `json:"legacyAsyncErrorValues,omitempty"`
	LegacyAsyncCancelledValues  string `json:"legacyAsyncCancelledValues,omitempty"`
	StreamName                  string `json:"streamName,omitempty"`
	PollIntervalMs              int    `json:"pollIntervalMs,omitempty"`
	MaxStreamRows               int    `json:"maxStreamRows,omitempty"`
	StreamRetentionMs           int    `json:"streamRetentionMs,omitempty"`
	QueryCacheMode              string `json:"queryCacheMode,omitempty"`
	QueryCacheKeyMode           string `json:"queryCacheKeyMode,omitempty"`
	QueryCacheTTLSeconds        *int   `json:"queryCacheTTLSeconds,omitempty"`
	QueryCacheStaleTTLSeconds   *int   `json:"queryCacheStaleTTLSeconds,omitempty"`
	QueryCacheTimeBucketSeconds *int   `json:"queryCacheTimeBucketSeconds,omitempty"`
	OriginalQueryText           string `json:"-"`
}

type KdbDatasource struct {
	Host                        string `json:"host"`
	Port                        int    `json:"port"`
	Timeout                     string `json:"timeout"`
	WithTls                     bool   `json:"withTLS"`
	SkipVertifyTLS              bool   `json:"skipVerifyTLS"`
	WithCACert                  bool   `json:"withCACert"`
	EnableAsync                 bool   `json:"enableAsync"`
	EnableStreaming             bool   `json:"enableStreaming"`
	ExecutionMode               string `json:"executionMode,omitempty"`
	CompatibilityMode           string `json:"compatibilityMode,omitempty"`
	DeferredQueryWrapper        string `json:"deferredQueryWrapper,omitempty"`
	PanopticonQueryWrapper      string `json:"panopticonQueryWrapper,omitempty"`
	PanopticonRequestFunction   string `json:"panopticonRequestFunction,omitempty"`
	LegacyAsyncSubmit           string `json:"legacyAsyncSubmit,omitempty"`
	LegacyAsyncStatus           string `json:"legacyAsyncStatus,omitempty"`
	LegacyAsyncResult           string `json:"legacyAsyncResult,omitempty"`
	LegacyAsyncCancel           string `json:"legacyAsyncCancel,omitempty"`
	LegacyAsyncRequestMode      string `json:"legacyAsyncRequestMode,omitempty"`
	LegacyAsyncJobIDPath        string `json:"legacyAsyncJobIDPath,omitempty"`
	LegacyAsyncStatusPath       string `json:"legacyAsyncStatusPath,omitempty"`
	LegacyAsyncProgressPath     string `json:"legacyAsyncProgressPath,omitempty"`
	LegacyAsyncMessagePath      string `json:"legacyAsyncMessagePath,omitempty"`
	LegacyAsyncErrorPath        string `json:"legacyAsyncErrorPath,omitempty"`
	LegacyAsyncPayloadPath      string `json:"legacyAsyncPayloadPath,omitempty"`
	LegacyAsyncQueuedValues     string `json:"legacyAsyncQueuedValues,omitempty"`
	LegacyAsyncRunningValues    string `json:"legacyAsyncRunningValues,omitempty"`
	LegacyAsyncDoneValues       string `json:"legacyAsyncDoneValues,omitempty"`
	LegacyAsyncErrorValues      string `json:"legacyAsyncErrorValues,omitempty"`
	LegacyAsyncCancelledValues  string `json:"legacyAsyncCancelledValues,omitempty"`
	AsyncMaxJobs                int    `json:"asyncMaxJobs,omitempty"`
	SyncMaxConnections          int    `json:"syncMaxConnections,omitempty"`
	QueryCacheEnabled           bool   `json:"queryCacheEnabled,omitempty"`
	QueryCacheTTLSeconds        int    `json:"queryCacheTTLSeconds,omitempty"`
	QueryCacheMaxEntries        int    `json:"queryCacheMaxEntries,omitempty"`
	QueryCacheTimeBucketSeconds int    `json:"queryCacheTimeBucketSeconds,omitempty"`
	QueryCacheStaleTTLSeconds   int    `json:"queryCacheStaleTTLSeconds,omitempty"`
	QueryCacheKeyMode           string `json:"queryCacheKeyMode,omitempty"`
	QueryCacheDiskEnabled       bool   `json:"queryCacheDiskEnabled,omitempty"`
	QueryCacheDiskPath          string `json:"queryCacheDiskPath,omitempty"`
	QueryCacheDiskMaxBytes      int64  `json:"queryCacheDiskMaxBytes,omitempty"`
	QueryCacheDiskMaxEntries    int    `json:"queryCacheDiskMaxEntries,omitempty"`
	QueryCacheControlEnabled    bool   `json:"queryCacheControlEnabled,omitempty"`
	DiagnosticsEnabled          bool   `json:"diagnosticsEnabled,omitempty"`
	DiagnosticsLogQueryText     bool   `json:"diagnosticsLogQueryText,omitempty"`
	ExcelReports                string `json:"excelReports,omitempty"`
	ExcelReportTemplateDirs     string `json:"excelReportTemplateDirs,omitempty"`
	ExcelReportMaxRows          int    `json:"excelReportMaxRows,omitempty"`
	ExcelReportMaxFileBytes     int64  `json:"excelReportMaxFileBytes,omitempty"`
	ExcelReportTimeoutMs        int    `json:"excelReportTimeoutMs,omitempty"`
	asyncConfigured             bool
	streamConfigured            bool
	queryCacheConfigured        bool
	queryCacheDefaultEnabled    bool
	queryCacheDiskConfigured    bool
	queryCacheDiskDefault       bool
	queryCacheControlConfigured bool
	queryCacheControlDefault    bool
	datasourceDefaultsOnce      sync.Once

	user              string
	pass              string
	instanceID        int64
	instanceUID       string
	instanceName      string
	TlsCertificate    string
	TlsKey            string
	CaCert            string
	TlsServerConfig   *tls.Config
	DialTimeout       time.Duration
	asyncJobs         chan struct{}
	syncPool          chan *kdb.KDBConn
	syncPoolSlots     chan struct{}
	syncPoolActive    map[*kdb.KDBConn]struct{}
	syncPoolMax       int
	syncPoolMu        sync.Mutex
	syncPoolClosed    bool
	queryCache        *syncQueryCache
	queryCacheMu      sync.Mutex
	queryDiskCache    *syncQueryDiskCache
	queryDiskCacheMu  sync.Mutex
	excelDownloads    map[string]excelReportDownload
	excelDownloadsMu  sync.Mutex
	lifecycleMu       sync.Mutex
	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelCauseFunc
	lifecycleStopping bool
	lifecycleDone     chan struct{}
	lifecycleWG       sync.WaitGroup

	RunKdbQuerySync func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error)
}

// NewKdbDatasource creates a new datasource instance.
func NewKdbDatasource(_ context.Context, settings backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	client := &KdbDatasource{}
	rawSettings, err := decodeDatasourceSettings(settings.JSONData, client)
	if err != nil {
		log.DefaultLogger.Error("Invalid datasource settings", "error", err)
		return nil, err
	}
	_, client.asyncConfigured = rawSettings["enableAsync"]
	_, client.streamConfigured = rawSettings["enableStreaming"]
	_, client.queryCacheConfigured = rawSettings["queryCacheEnabled"]
	_, client.queryCacheDiskConfigured = rawSettings["queryCacheDiskEnabled"]
	_, client.queryCacheControlConfigured = rawSettings["queryCacheControlEnabled"]
	client.queryCacheDefaultEnabled = true
	client.queryCacheDiskDefault = true
	client.queryCacheControlDefault = true
	client.instanceID = settings.ID
	client.instanceUID = settings.UID
	client.instanceName = settings.Name
	normalizedHost, err := kdb.ValidateEndpoint(client.Host, client.Port)
	if err != nil {
		return nil, fmt.Errorf("invalid kdb+ endpoint: %w", err)
	}
	client.Host = normalizedHost

	timeoutText := strings.TrimSpace(client.Timeout)
	if timeoutText == "" {
		client.DialTimeout = time.Duration(defaultConnectionTimeoutMs) * time.Millisecond
	} else {
		if timeoutText != client.Timeout {
			return nil, fmt.Errorf("invalid connection timeout %q: surrounding whitespace is not allowed", client.Timeout)
		}
		timeoutMs, err := strconv.ParseUint(timeoutText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid connection timeout %q: enter an integer number of milliseconds", client.Timeout)
		}
		if timeoutMs < 1 || timeoutMs > maxConnectionTimeoutMs {
			return nil, fmt.Errorf("connection timeout must be between 1 and %d milliseconds; got %d", maxConnectionTimeoutMs, timeoutMs)
		}
		client.DialTimeout = time.Duration(timeoutMs) * time.Millisecond
	}

	username, ok := settings.DecryptedSecureJSONData["username"]
	if ok {
		client.user = username
	} else {
		client.user = ""
		log.DefaultLogger.Info("No username provided; using default")
	}

	pass, ok := settings.DecryptedSecureJSONData["password"]
	if ok {
		client.pass = pass
	} else {
		client.pass = ""
		log.DefaultLogger.Info("No password provided; using default")
	}
	if strings.Contains(client.user, ":") {
		return nil, fmt.Errorf("kdb+ username must not contain ':'")
	}
	if err := kdb.ValidateAuth(client.user + ":" + client.pass); err != nil {
		return nil, fmt.Errorf("invalid kdb+ credentials: %w", err)
	}

	if client.WithTls {
		log.DefaultLogger.Info("TLS enabled for new kdb datasource, creating tls config...")
		tlsCertificate := settings.DecryptedSecureJSONData["tlsCertificate"]
		if len(tlsCertificate) > maxTLSCertificateBytes {
			return nil, fmt.Errorf("TLS client certificate exceeds the %d-byte limit", maxTLSCertificateBytes)
		}
		if strings.TrimSpace(tlsCertificate) == "" {
			return nil, fmt.Errorf("TLS client certificate is required when Client Auth is enabled")
		}
		client.TlsCertificate = tlsCertificate

		tlsKey := settings.DecryptedSecureJSONData["tlsKey"]
		if len(tlsKey) > maxTLSPrivateKeyBytes {
			return nil, fmt.Errorf("TLS client key exceeds the %d-byte limit", maxTLSPrivateKeyBytes)
		}
		if strings.TrimSpace(tlsKey) == "" {
			return nil, fmt.Errorf("TLS client key is required when Client Auth is enabled")
		}
		client.TlsKey = tlsKey

		cert, err := tls.X509KeyPair([]byte(client.TlsCertificate), []byte(client.TlsKey))
		if err != nil {
			return nil, fmt.Errorf("invalid TLS client certificate/key pair: %w", err)
		}
		tlsServerConfig := &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: client.SkipVertifyTLS,
			MinVersion:         tls.VersionTLS12,
		}
		if client.SkipVertifyTLS {
			log.DefaultLogger.Warn("TLS server certificate and hostname verification are disabled for this kdb+ datasource")
		}

		if client.WithCACert {
			caCert := settings.DecryptedSecureJSONData["caCert"]
			if len(caCert) > maxTLSCABundleBytes {
				return nil, fmt.Errorf("custom CA certificate exceeds the %d-byte limit", maxTLSCABundleBytes)
			}
			if strings.TrimSpace(caCert) == "" {
				return nil, fmt.Errorf("custom CA certificate is required when Custom CA is enabled")
			}
			client.CaCert = caCert
			roots, poolErr := x509.SystemCertPool()
			if poolErr != nil {
				log.DefaultLogger.Warn("Unable to load system TLS roots; using the configured custom CA only", "error", poolErr)
			}
			if roots == nil {
				roots = x509.NewCertPool()
			}
			if err := appendValidatedCACertificates(roots, []byte(client.CaCert)); err != nil {
				return nil, fmt.Errorf("invalid custom CA certificate: %w", err)
			}
			tlsServerConfig.RootCAs = roots
		}
		client.TlsServerConfig = tlsServerConfig
	}

	client.setupKdbConnectionHandlers()
	client.normalizeDatasourceDefaults()
	client.initializeLifecycle()

	log.DefaultLogger.Info("KDB Datasource created successfully", "syncMaxConnections", client.SyncMaxConnections, "asyncMaxJobs", client.AsyncMaxJobs)
	return client, nil
}

func appendValidatedCACertificates(pool *x509.CertPool, bundle []byte) error {
	if pool == nil {
		return fmt.Errorf("destination certificate pool is nil")
	}
	remaining := bytes.TrimSpace(bundle)
	added := 0
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return fmt.Errorf("unexpected data outside CERTIFICATE PEM blocks")
		}
		block, rest := pem.Decode(remaining)
		if block == nil {
			return fmt.Errorf("PEM data contains no decodable CERTIFICATE block")
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("unexpected PEM block type %q", block.Type)
		}
		certificates, err := x509.ParseCertificates(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse CERTIFICATE block: %w", err)
		}
		for _, certificate := range certificates {
			if !certificate.IsCA || !certificate.BasicConstraintsValid {
				return fmt.Errorf("certificate %q is not a CA certificate", certificate.Subject.String())
			}
			if certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
				return fmt.Errorf("CA certificate %q is not permitted to sign certificates", certificate.Subject.String())
			}
			pool.AddCert(certificate)
			added++
		}
		remaining = bytes.TrimSpace(rest)
	}
	if added == 0 {
		return fmt.Errorf("PEM data contains no certificates")
	}
	return nil
}

func (d *KdbDatasource) normalizeDatasourceDefaults() {
	d.datasourceDefaultsOnce.Do(d.applyDatasourceDefaults)
}

func (d *KdbDatasource) applyDatasourceDefaults() {
	if d.ExecutionMode == "" {
		d.ExecutionMode = ExecutionModeSync
	}
	if d.CompatibilityMode == "" {
		d.CompatibilityMode = CompatibilityModeNative
	}
	if d.AsyncMaxJobs < 1 {
		d.AsyncMaxJobs = defaultAsyncMaxJobs
	}
	if d.SyncMaxConnections < 1 {
		d.SyncMaxConnections = defaultSyncMaxConnections
	}
	if !d.queryCacheConfigured && d.queryCacheDefaultEnabled {
		d.QueryCacheEnabled = defaultQueryCacheEnabled
	}
	if d.QueryCacheTTLSeconds < 1 {
		d.QueryCacheTTLSeconds = defaultQueryCacheTTL
	}
	if d.QueryCacheMaxEntries < 1 {
		d.QueryCacheMaxEntries = defaultQueryCacheMax
	}
	if d.QueryCacheTimeBucketSeconds < 0 {
		d.QueryCacheTimeBucketSeconds = defaultQueryCacheTimeBucket
	}
	if d.QueryCacheStaleTTLSeconds < 0 {
		d.QueryCacheStaleTTLSeconds = defaultQueryCacheStaleTTL
	}
	if d.QueryCacheKeyMode == "" {
		d.QueryCacheKeyMode = QueryCacheKeyModeStrict
	}
	if d.QueryCacheKeyMode != QueryCacheKeyModeStrict && d.QueryCacheKeyMode != QueryCacheKeyModeShared {
		d.QueryCacheKeyMode = QueryCacheKeyModeStrict
	}
	if !d.queryCacheDiskConfigured && d.queryCacheDiskDefault {
		d.QueryCacheDiskEnabled = defaultQueryCacheDisk
	}
	if !d.queryCacheControlConfigured && d.queryCacheControlDefault {
		d.QueryCacheControlEnabled = true
	}
	if d.QueryCacheDiskMaxBytes < 1 {
		d.QueryCacheDiskMaxBytes = defaultQueryCacheDiskBytes
	}
	if d.QueryCacheDiskMaxEntries < 1 {
		d.QueryCacheDiskMaxEntries = defaultQueryCacheDiskMax
	}
	normalizeLegacyAsyncDefaults(
		&d.LegacyAsyncRequestMode,
		&d.LegacyAsyncJobIDPath,
		&d.LegacyAsyncStatusPath,
		&d.LegacyAsyncProgressPath,
		&d.LegacyAsyncMessagePath,
		&d.LegacyAsyncErrorPath,
		&d.LegacyAsyncPayloadPath,
		&d.LegacyAsyncQueuedValues,
		&d.LegacyAsyncRunningValues,
		&d.LegacyAsyncDoneValues,
		&d.LegacyAsyncErrorValues,
		&d.LegacyAsyncCancelledValues,
	)
	if d.asyncJobs == nil {
		d.asyncJobs = make(chan struct{}, d.AsyncMaxJobs)
	}
}

func logDatasourceDispose() {
	log.DefaultLogger.Info("Dispose called")
}

func (d *KdbDatasource) newConnection(ctx context.Context) (conn *kdb.KDBConn, err error) {
	ctx = normalizeSyncQueryContext(ctx)
	if !d.hasActiveLifecycleLease(ctx) {
		return nil, fmt.Errorf("kdb+ connection requires an admitted datasource operation: %w", ErrDatasourceDisposed)
	}
	if err := syncQueryContextError(ctx, "kdb+ connection establishment interrupted"); err != nil {
		return nil, err
	}

	log.DefaultLogger.Info("Opening connection to kdb+", "host", d.Host, "port", d.Port)
	auth := fmt.Sprintf("%s:%s", d.user, d.pass)
	dialCtx, cancel := context.WithTimeout(ctx, d.DialTimeout)
	defer cancel()
	if d.WithTls {
		conn, err = kdb.DialTLSContext(dialCtx, d.Host, d.Port, auth, d.TlsServerConfig)
	} else {
		conn, err = kdb.DialKDBContext(dialCtx, d.Host, d.Port, auth)
	}
	if err != nil {
		log.DefaultLogger.Error("Error establishing kdb connection", "error", err)
		return nil, err
	}
	if contextErr := syncQueryContextError(dialCtx, "kdb+ connection establishment interrupted"); contextErr != nil {
		_ = conn.Close()
		return nil, contextErr
	}
	log.DefaultLogger.Info("Dialled kdb+ successfully", "host", d.Host, "port", d.Port)
	return conn, nil
}

func (d *KdbDatasource) newCleanupConnection(ctx context.Context) (*kdb.KDBConn, error) {
	ctx = normalizeSyncQueryContext(ctx)
	if !d.hasActiveLifecycleLease(ctx) {
		return nil, fmt.Errorf("q-side cleanup requires an admitted datasource operation: %w", ErrDatasourceDisposed)
	}
	if err := syncQueryContextError(ctx, "q-side cleanup connection interrupted"); err != nil {
		return nil, err
	}

	dialTimeout := d.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = time.Duration(defaultConnectionTimeoutMs) * time.Millisecond
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	auth := fmt.Sprintf("%s:%s", d.user, d.pass)
	var (
		conn *kdb.KDBConn
		err  error
	)
	if d.WithTls {
		conn, err = kdb.DialTLSContext(dialCtx, d.Host, d.Port, auth, d.TlsServerConfig)
	} else {
		conn, err = kdb.DialKDBContext(dialCtx, d.Host, d.Port, auth)
	}
	if err != nil {
		return nil, err
	}
	if contextErr := syncQueryContextError(dialCtx, "q-side cleanup connection interrupted"); contextErr != nil {
		_ = conn.Close()
		return nil, contextErr
	}
	return conn, nil
}

func (d *KdbDatasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (response *backend.QueryDataResponse, err error) {
	if d == nil {
		return nil, backend.PluginErrorf("invalid query request: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()

	if err := validateQueryRequestShape(req); err != nil {
		return nil, err
	}
	d.normalizeDatasourceDefaults()
	response = backend.NewQueryDataResponse()
	if len(req.Queries) == 0 {
		return response, nil
	}
	admitted, admissionErr := d.admitDataQueries(req)
	if admissionErr != nil {
		return queryAdmissionResponse(req, admissionErr), nil
	}

	workerCount := min(len(admitted), d.SyncMaxConnections)
	var nextQuery atomic.Int64
	var workers sync.WaitGroup
	var responseMu sync.Mutex
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				index := int(nextQuery.Add(1) - 1)
				if index >= len(admitted) {
					return
				}
				admittedQuery := admitted[index]
				result := d.queryAdmitted(ctx, req.PluginContext, admittedQuery, syncDiagnosticRequestID(req, admittedQuery.query, index))
				responseMu.Lock()
				response.Responses[admittedQuery.query.RefID] = result
				responseMu.Unlock()
			}
		}()
	}
	workers.Wait()

	if contextErr := syncQueryContextError(ctx, "query request interrupted"); contextErr != nil {
		for _, query := range req.Queries {
			result, ok := response.Responses[query.RefID]
			if !ok {
				result = backend.DataResponse{Error: contextErr}
			} else if result.Error == nil {
				result.Error = contextErr
			} else if !errors.Is(result.Error, context.Cause(ctx)) {
				result.Error = errors.Join(result.Error, contextErr)
			}
			response.Responses[query.RefID] = result
		}
	}
	return response, nil
}

func (d *KdbDatasource) queryAdmitted(ctx context.Context, pCtx backend.PluginContext, admitted admittedDataQuery, requestID string) backend.DataResponse {
	ctx = normalizeSyncQueryContext(ctx)
	query := admitted.query
	model := admitted.model
	response := backend.DataResponse{}
	start := time.Now()
	fields := append(
		d.diagnosticQueryFields(pCtx, query, model, requestID),
		"profileDecodeMs", admitted.decodeMs,
		"profilePrepareMs", admitted.prepareMs,
	)
	d.logDiagnostics("sync query received", fields...)

	cacheStart := time.Now()
	result, err := d.runSyncQueryWithCache(ctx, pCtx, query, model, fields)
	if err != nil {
		result.fields = appendDiagnosticDuration(result.fields, "profileCachePathMs", cacheStart)
		d.logDiagnosticError(result.errorMessage, appendDiagnosticError(result.fields, err)...)
		response.Error = err
		return response
	}
	fields = appendDiagnosticDuration(result.fields, "profileCachePathMs", cacheStart)
	fields = appendDiagnosticFrames(fields, result.frames)
	fields = ensureDiagnosticFrameProfile(fields, result.frames)
	fields = append(fields, "durationMs", diagnosticDurationMs(time.Since(start)))
	attachAsyncQDiagnostics(result.frames, fields)
	response.Frames = append(response.Frames, result.frames...)
	d.logDiagnostics("sync query completed", fields...)
	return response
}

func normalizeQueryModel(model *QueryModel) {
	normalizeQueryModelWithDefaults(model, ExecutionModeSync, CompatibilityModeNative, "", "", "")
}

func (d *KdbDatasource) normalizeQueryModel(model *QueryModel) {
	d.normalizeDatasourceDefaults()
	normalizeQueryModelWithDefaults(model, d.ExecutionMode, d.CompatibilityMode, d.DeferredQueryWrapper, d.PanopticonQueryWrapper, d.PanopticonRequestFunction)
	if model.LegacyAsyncSubmit == "" {
		model.LegacyAsyncSubmit = d.LegacyAsyncSubmit
	}
	if model.LegacyAsyncStatus == "" {
		model.LegacyAsyncStatus = d.LegacyAsyncStatus
	}
	if model.LegacyAsyncResult == "" {
		model.LegacyAsyncResult = d.LegacyAsyncResult
	}
	if model.LegacyAsyncCancel == "" {
		model.LegacyAsyncCancel = d.LegacyAsyncCancel
	}
	if model.LegacyAsyncRequestMode == "" {
		model.LegacyAsyncRequestMode = d.LegacyAsyncRequestMode
	}
	if model.LegacyAsyncJobIDPath == "" {
		model.LegacyAsyncJobIDPath = d.LegacyAsyncJobIDPath
	}
	if model.LegacyAsyncStatusPath == "" {
		model.LegacyAsyncStatusPath = d.LegacyAsyncStatusPath
	}
	if model.LegacyAsyncProgressPath == "" {
		model.LegacyAsyncProgressPath = d.LegacyAsyncProgressPath
	}
	if model.LegacyAsyncMessagePath == "" {
		model.LegacyAsyncMessagePath = d.LegacyAsyncMessagePath
	}
	if model.LegacyAsyncErrorPath == "" {
		model.LegacyAsyncErrorPath = d.LegacyAsyncErrorPath
	}
	if model.LegacyAsyncPayloadPath == "" {
		model.LegacyAsyncPayloadPath = d.LegacyAsyncPayloadPath
	}
	if model.LegacyAsyncQueuedValues == "" {
		model.LegacyAsyncQueuedValues = d.LegacyAsyncQueuedValues
	}
	if model.LegacyAsyncRunningValues == "" {
		model.LegacyAsyncRunningValues = d.LegacyAsyncRunningValues
	}
	if model.LegacyAsyncDoneValues == "" {
		model.LegacyAsyncDoneValues = d.LegacyAsyncDoneValues
	}
	if model.LegacyAsyncErrorValues == "" {
		model.LegacyAsyncErrorValues = d.LegacyAsyncErrorValues
	}
	if model.LegacyAsyncCancelledValues == "" {
		model.LegacyAsyncCancelledValues = d.LegacyAsyncCancelledValues
	}
	normalizeLegacyAsyncDefaults(
		&model.LegacyAsyncRequestMode,
		&model.LegacyAsyncJobIDPath,
		&model.LegacyAsyncStatusPath,
		&model.LegacyAsyncProgressPath,
		&model.LegacyAsyncMessagePath,
		&model.LegacyAsyncErrorPath,
		&model.LegacyAsyncPayloadPath,
		&model.LegacyAsyncQueuedValues,
		&model.LegacyAsyncRunningValues,
		&model.LegacyAsyncDoneValues,
		&model.LegacyAsyncErrorValues,
		&model.LegacyAsyncCancelledValues,
	)
}

func normalizeQueryModelWithDefaults(model *QueryModel, executionMode string, compatibilityMode string, deferredWrapper string, panopticonWrapper string, panopticonRequestFunction string) {
	if model.Timeout < 1 {
		model.Timeout = defaultQueryTimeout
	}
	if model.ExecutionMode == "" {
		model.ExecutionMode = executionMode
	}
	if model.CompatibilityMode == "" {
		model.CompatibilityMode = compatibilityMode
	}
	if model.DeferredQueryWrapper == "" {
		model.DeferredQueryWrapper = deferredWrapper
	}
	if model.PanopticonQueryWrapper == "" {
		model.PanopticonQueryWrapper = panopticonWrapper
	}
	if model.PanopticonRequestFunction == "" {
		model.PanopticonRequestFunction = panopticonRequestFunction
	}
	if model.PollIntervalMs < 1 {
		model.PollIntervalMs = defaultPollIntervalMs
	}
	if model.MaxStreamRows < 1 {
		model.MaxStreamRows = defaultMaxStreamRows
	}
	if model.StreamRetentionMs < 0 {
		model.StreamRetentionMs = 0
	}
}

func buildSyncQueryPayload(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel) *kdb.K {
	masterKeys, masterValues := buildMasterKdbLists(pCtx, query, model)
	return kdb.NewList(kdb.Atom(kdb.KC, queryExecutionFunction(model)), kdb.NewDict(masterKeys, masterValues))
}

func buildDirectQueryRequest(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel) *kdb.K {
	masterKeys, masterValues := buildMasterKdbLists(pCtx, query, model)
	return kdb.NewDict(masterKeys, masterValues)
}

func buildMasterKdbLists(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel) (*kdb.K, *kdb.K) {
	userDict := buildUserKdbDict(pCtx.User)
	datasourceDict := buildDatasourceKdbDict(pCtx.DataSourceInstanceSettings)
	queryDict := buildQueryKdbDict(query, model)
	panopticonDict := buildPanopticonKdbDict(query, model)
	masterKeys := []string{"AQUAQ_KDB_BACKEND_GRAF_DATASOURCE", "Time", "OrgID", "Datasource", "User", "Query", "Timeout", "ExecutionMode", "CompatibilityMode", "Panopticon"}
	masterValues := []*kdb.K{
		kdb.Float(ADAPTOR_VERSION),
		kdb.Atom(-kdb.KP, time.Now()),
		kdb.Long(pCtx.OrgID),
		datasourceDict,
		userDict,
		queryDict,
		kdb.Long(int64(model.Timeout)),
		kdb.Symbol(model.ExecutionMode),
		kdb.Symbol(model.CompatibilityMode),
		panopticonDict,
	}
	panopticonKeys, panopticonValues := buildPanopticonContextKdbLists(query)
	masterKeys = append(masterKeys, panopticonKeys...)
	masterValues = append(masterValues, panopticonValues...)
	return kdb.SymbolV(masterKeys), kdb.NewList(masterValues...)
}

func queryExecutionFunction(model QueryModel) string {
	requestFunction := strings.TrimSpace(model.PanopticonRequestFunction)
	if model.CompatibilityMode == CompatibilityModePanopticon && requestFunction != "" {
		return requestFunction
	}
	return "{[x] value x[`Query;`Query]}"
}

func buildPanopticonKdbDict(query backend.DataQuery, model QueryModel) *kdb.K {
	contextKeys, contextValues := buildPanopticonContextKdbLists(query)
	keys := append(contextKeys, "Query", "OriginalQuery", "CompiledQuery", "QueryWrapper", "RequestFunction")
	originalQuery := model.OriginalQueryText
	if originalQuery == "" {
		originalQuery = model.QueryText
	}
	values := append(contextValues,
		kdb.Atom(kdb.KC, model.QueryText),
		kdb.Atom(kdb.KC, originalQuery),
		kdb.Atom(kdb.KC, model.QueryText),
		kdb.Atom(kdb.KC, model.PanopticonQueryWrapper),
		kdb.Atom(kdb.KC, model.PanopticonRequestFunction),
	)
	return kdb.NewDict(kdb.SymbolV(keys), kdb.NewList(values...))
}

func buildPanopticonContextKdbLists(query backend.DataQuery) ([]string, []*kdb.K) {
	intervalMs := int64(query.Interval / time.Millisecond)
	keys := []string{
		"TimeWindowStart", "TimeWindowEnd", "Snapshot", "FocusTime",
		"Start", "End", "From", "To",
		"TimeWindowStartText", "TimeWindowEndText", "SnapshotText", "FocusTimeText",
		"Interval", "IntervalNs", "IntervalMs", "MaxDataPoints", "RefID",
	}
	values := []*kdb.K{
		kdb.Atom(-kdb.KP, query.TimeRange.From),
		kdb.Atom(-kdb.KP, query.TimeRange.To),
		kdb.Atom(-kdb.KP, query.TimeRange.To),
		kdb.Atom(-kdb.KP, query.TimeRange.To),
		kdb.Atom(-kdb.KP, query.TimeRange.From),
		kdb.Atom(-kdb.KP, query.TimeRange.To),
		kdb.Atom(-kdb.KP, query.TimeRange.From),
		kdb.Atom(-kdb.KP, query.TimeRange.To),
		kdb.Atom(kdb.KC, timeText(query.TimeRange.From)),
		kdb.Atom(kdb.KC, timeText(query.TimeRange.To)),
		kdb.Atom(kdb.KC, timeText(query.TimeRange.To)),
		kdb.Atom(kdb.KC, timeText(query.TimeRange.To)),
		kdb.Long(int64(query.Interval)),
		kdb.Long(int64(query.Interval)),
		kdb.Long(intervalMs),
		kdb.Long(query.MaxDataPoints),
		kdb.Atom(kdb.KC, query.RefID),
	}
	return keys, values
}

func parseKdbResponseToFrames(kdbResponse *kdb.K, model QueryModel, refID string) (frames []*data.Frame, err error) {
	if kdbResponse == nil {
		return nil, fmt.Errorf("kdb+ returned nil response")
	}
	responseType := kdbResponse.Type
	defer func() {
		if recovered := recover(); recovered != nil {
			frames = nil
			err = fmt.Errorf("unable to parse kdb+ response safely: unexpected parser failure for type %d", responseType)
		}
	}()
	if err := validateKdbObject(kdbResponse); err != nil {
		return nil, fmt.Errorf("invalid kdb+ response: %w", err)
	}

	switch {
	case kdbResponse.Type == kdb.XT:
		frame, err := ParseSimpleKdbTable(kdbResponse)
		if err != nil {
			return nil, err
		}
		frame.Name = refID
		frame.RefID = refID
		frames = append(frames, frame)
	case kdbResponse.Type == kdb.XD:
		if model.CompatibilityMode == CompatibilityModePanopticon {
			frame, err := ParseKeyedKdbTableAsFrame(kdbResponse)
			if err == nil {
				frame.Name = refID
				frame.RefID = refID
				frames = append(frames, frame)
				break
			}
			frame, err = ParseKdbDictAsFrame(kdbResponse)
			if err != nil {
				return nil, fmt.Errorf("unable to parse Panopticon dictionary result (%s): %w", describeKdbObject(kdbResponse), err)
			}
			frame.Name = refID
			frame.RefID = refID
			frames = append(frames, frame)
			break
		}
		groupedFrames, err := ParseGroupedKdbTable(kdbResponse, model.IncludeKeyColumns)
		if err != nil {
			return nil, err
		}
		for _, frame := range groupedFrames {
			frame.RefID = refID
		}
		frames = append(frames, groupedFrames...)
	case model.CompatibilityMode == CompatibilityModePanopticon && kdbResponse.Type == kdb.K0:
		frame, err := ParseKdbDictListAsFrame(kdbResponse)
		if err != nil {
			frame, err = ParseKdbObjectAsFrame(kdbResponse)
		}
		if err != nil {
			return nil, fmt.Errorf("unable to parse Panopticon generic list result (%s): %w", describeKdbObject(kdbResponse), err)
		}
		frame.Name = refID
		frame.RefID = refID
		frames = append(frames, frame)
	case model.CompatibilityMode == CompatibilityModePanopticon && (kdbResponse.Type < kdb.K0 || (kdbResponse.Type > kdb.K0 && kdbResponse.Type <= kdb.KT)):
		frame, err := ParseKdbObjectAsFrame(kdbResponse)
		if err != nil {
			return nil, fmt.Errorf("unable to parse Panopticon scalar/vector result (%s): %w", describeKdbObject(kdbResponse), err)
		}
		frame.Name = refID
		frame.RefID = refID
		frames = append(frames, frame)
	default:
		return nil, fmt.Errorf("returned unsupported kdb+ object (%s), only tables and grouped tables are supported in %s compatibility mode", describeKdbObject(kdbResponse), model.CompatibilityMode)
	}

	if model.UseTimeColumn {
		for _, frame := range frames {
			if err := moveTimeColumnToFront(frame, model.TimeColumn); err != nil {
				return nil, err
			}
		}
	}
	return frames, nil
}

func applyDeferredQueryWrapper(queryText string, wrapper string) (string, error) {
	if strings.TrimSpace(wrapper) == "" {
		return "", fmt.Errorf("deferred async mode requires a query wrapper containing {Query}")
	}
	if strings.Count(wrapper, "{Query}") != 1 {
		return "", fmt.Errorf("deferred query wrapper must contain exactly one {Query} placeholder")
	}
	return strings.Replace(wrapper, "{Query}", queryText, 1), nil
}

func moveTimeColumnToFront(frame *data.Frame, timeColumn string) error {
	timeOverrideIndex := -1
	for v, field := range frame.Fields {
		if field.Name == timeColumn {
			timeOverrideIndex = v
			break
		}
	}
	if timeOverrideIndex == -1 {
		return fmt.Errorf("temporal column override '%v' is not present in all returned tables", timeColumn)
	}
	timeCol := frame.Fields[timeOverrideIndex]
	nonTimeCols := append(frame.Fields[:timeOverrideIndex], frame.Fields[timeOverrideIndex+1:]...)
	frame.Fields = append([]*data.Field{timeCol}, nonTimeCols...)
	return nil
}

func (d *KdbDatasource) CheckHealth(ctx context.Context, req *backend.CheckHealthRequest) (result *backend.CheckHealthResult, err error) {
	if d == nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: healthDatasourceUnavailableMessage,
		}, backend.PluginErrorf("health check failed: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: err.Error()}, err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()
	d.normalizeDatasourceDefaults()

	pCtx := backend.PluginContext{}
	if req != nil {
		pCtx = req.PluginContext
	}
	if err := validatePluginContextExpansionInputs(pCtx); err != nil {
		d.logDiagnosticError("health check request failed validation")
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: healthRequestValidationMessage,
		}, nil
	}
	healthFields := []interface{}{
		"host", d.Host,
		"port", d.Port,
		"withTLS", d.WithTls,
		"timeoutMs", int64(d.DialTimeout / time.Millisecond),
		"syncMaxConnections", d.SyncMaxConnections,
	}
	if pCtx.DataSourceInstanceSettings != nil {
		healthFields = append(healthFields,
			"datasourceUID", pCtx.DataSourceInstanceSettings.UID,
			"datasourceName", pCtx.DataSourceInstanceSettings.Name,
		)
	}
	d.logDiagnostics("health check started", healthFields...)
	userDict := buildUserKdbDict(pCtx.User)
	datasourceDict := buildDatasourceKdbDict(pCtx.DataSourceInstanceSettings)
	k := kdb.SymbolV([]string{"AQUAQ_KDB_BACKEND_GRAF_DATASOURCE", "Time", "OrgID", "Datasource", "User", "Query", "Timeout"})
	v := kdb.NewList(
		kdb.Float(ADAPTOR_VERSION),
		kdb.Atom(-kdb.KP, time.Now()),
		kdb.Long(pCtx.OrgID),
		datasourceDict,
		userDict,
		kdb.NewDict(kdb.SymbolV([]string{"Query", "QueryType"}), kdb.NewList(kdb.Atom(kdb.KC, "1+1"), kdb.Symbol("HEALTHCHECK"))),
		kdb.Long(int64(d.DialTimeout/time.Millisecond)))

	test, err := d.RunKdbQuerySync(ctx, kdb.NewList(kdb.Atom(kdb.KC, "{[x] value x[`Query;`Query]}"), kdb.NewDict(k, v)), d.DialTimeout)
	if err != nil {
		d.logDiagnosticError("health check failed", appendDiagnosticError(healthFields, err)...)
		emsg := fmt.Sprintf("Error querying kdb+ process: %v", err)
		if err == io.EOF {
			emsg += " (hint: potential authentication error)"
		}
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: emsg}, nil
	}
	var status = backend.HealthStatusUnknown
	var message = ""

	if test == nil {
		status = backend.HealthStatusError
		message = "kdb+ returned a nil health-check response"
		d.logDiagnosticError("health check returned nil response", healthFields...)
		return &backend.CheckHealthResult{
			Status:  status,
			Message: message,
		}, nil
	}
	if test.Type != -kdb.KJ {
		status = backend.HealthStatusError
		message = fmt.Sprintf("kdb+ result not of expected type; received type %v", test.Type)
		d.logDiagnosticError("health check returned unexpected type", append(healthFields, "kdbResponseType", test.Type)...)
		return &backend.CheckHealthResult{
			Status:  status,
			Message: message,
		}, nil
	}
	val, ok := test.Data.(int64)
	if !ok {
		status = backend.HealthStatusError
		message = "kdb+ health-check result had invalid long atom data"
		d.logDiagnosticError("health check returned malformed long atom", append(healthFields, "kdbResponseType", test.Type)...)
		return &backend.CheckHealthResult{
			Status:  status,
			Message: message,
		}, nil
	}

	if val == 2 {
		status = backend.HealthStatusOk
		message = "kdb+ connected successfully"
	} else {
		status = backend.HealthStatusError
		message = fmt.Sprintf("kdb+ response to \"1+1\" was correct type but incorrect value (returned %v)", val)
	}
	d.logDiagnostics("health check completed", append(healthFields, "status", status.String(), "value", val)...)

	return &backend.CheckHealthResult{
		Status:  status,
		Message: message,
	}, nil
}
