package plugin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
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
	asyncQHelperUnavailable     = "async/stream queries require q/asyncq_grafana.q to be loaded in the target kdb+ process or gateway"
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
	Timeout                     int    `json:"timeOut"`
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

type kdbSyncQuery struct {
	query   *kdb.K
	id      uint32
	timeout time.Duration
}

type kdbRawRead struct {
	result  *kdb.K
	msgType kdb.ReqType
	err     error
}

type kdbSyncRes struct {
	result *kdb.K
	err    error
	id     uint32
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
	asyncConfigured             bool
	streamConfigured            bool
	queryCacheConfigured        bool
	queryCacheDefaultEnabled    bool
	queryCacheDiskConfigured    bool
	queryCacheDiskDefault       bool
	queryCacheControlConfigured bool
	queryCacheControlDefault    bool

	user             string
	pass             string
	instanceID       int64
	instanceUID      string
	instanceName     string
	TlsCertificate   string
	TlsKey           string
	CaCert           string
	TlsServerConfig  *tls.Config
	DialTimeout      time.Duration
	KdbHandle        *kdb.KDBConn
	asyncJobs        chan struct{}
	syncPool         chan *kdb.KDBConn
	syncPoolSlots    chan struct{}
	syncPoolActive   map[*kdb.KDBConn]struct{}
	syncPoolMax      int
	syncPoolMu       sync.Mutex
	syncPoolClosed   bool
	queryCache       *syncQueryCache
	queryCacheMu     sync.Mutex
	queryDiskCache   *syncQueryDiskCache
	queryDiskCacheMu sync.Mutex

	signals             chan int
	syncQueue           chan *kdbSyncQuery
	rawReadChan         chan *kdbRawRead
	syncResChan         chan *kdbSyncRes
	kdbSyncQueryCounter uint32
	IsOpen              bool

	KdbHandleListener func()
	RunKdbQuerySync   func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error)
	OpenConnection    func() error
	CloseConnection   func() error
	WriteConnection   func(kdb.ReqType, *kdb.K) error
	ReadConnection    func() (*kdb.K, kdb.ReqType, error)
}

// NewKdbDatasource creates a new datasource instance.
func NewKdbDatasource(_ context.Context, settings backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	client := KdbDatasource{}
	var rawSettings map[string]json.RawMessage
	_ = json.Unmarshal(settings.JSONData, &rawSettings)
	err := json.Unmarshal(settings.JSONData, &client)
	if err != nil {
		log.DefaultLogger.Error("Error decrypting Host and Port information", "error", err)
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

	if client.WithTls {
		tlsServerConfig := new(tls.Config)
		log.DefaultLogger.Info("TLS enabled for new kdb datasource, creating tls config...")
		tlsCertificate, certOk := settings.DecryptedSecureJSONData["tlsCertificate"]
		if !certOk {
			log.DefaultLogger.Info("Error decrypting TLS Cert or no TLS Cert provided")
		}
		client.TlsCertificate = tlsCertificate

		tlsKey, keyOk := settings.DecryptedSecureJSONData["tlsKey"]
		if !keyOk {
			log.DefaultLogger.Error("Error decrypting TLS Key or no TLS Key provided")
		}
		client.TlsKey = tlsKey

		if client.SkipVertifyTLS {
			log.DefaultLogger.Info("New kdb+ datasource config setup to skip TLS verification")
		}

		if client.WithCACert {
			caCert, keyOk := settings.DecryptedSecureJSONData["caCert"]
			if !keyOk {
				log.DefaultLogger.Error("Error decrypting CA Cert or no CA Cert provided")
			}
			client.CaCert = caCert
			log.DefaultLogger.Info("Setting custom CA certificate...")
			tlsCaCert := x509.NewCertPool()
			r := tlsCaCert.AppendCertsFromPEM([]byte(client.CaCert))
			if !r {
				log.DefaultLogger.Info("Error parsing custom CA certificate")
			}
			tlsServerConfig.RootCAs = tlsCaCert
		}

		cert, err := tls.X509KeyPair([]byte(client.TlsCertificate), []byte(client.TlsKey))
		if err != nil {
			log.DefaultLogger.Error("Cert convert error", "error", err)
		}

		tlsServerConfig.Certificates = []tls.Certificate{cert}
		tlsServerConfig.InsecureSkipVerify = client.SkipVertifyTLS
		client.TlsServerConfig = tlsServerConfig
	}

	timeOutDuration, err := time.ParseDuration(client.Timeout + "ms")
	if nil != err {
		log.DefaultLogger.Info("Using default timeout")
		timeOutDuration = time.Second
	}
	client.DialTimeout = timeOutDuration
	client.setupKdbConnectionHandlers()
	client.IsOpen = false
	client.normalizeDatasourceDefaults()

	log.DefaultLogger.Info("Making synchronous query channel")
	client.syncQueue = make(chan *kdbSyncQuery)

	log.DefaultLogger.Info("Making synchronous response channel")
	client.syncResChan = make(chan *kdbSyncRes)

	log.DefaultLogger.Info("Making signals channel")
	client.signals = make(chan int)

	log.DefaultLogger.Info("KDB Datasource created successfully", "syncMaxConnections", client.SyncMaxConnections, "asyncMaxJobs", client.AsyncMaxJobs)
	return &client, nil
}

func (d *KdbDatasource) normalizeDatasourceDefaults() {
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

func (d *KdbDatasource) Dispose() {
	log.DefaultLogger.Info("Dispose called")
	d.closeSyncPool()
	d.closeSyncQueryCache()
	if d.IsOpen {
		log.DefaultLogger.Info("Handle open when dispose called, closing handle")
		if err := d.CloseConnection(); err != nil {
			log.DefaultLogger.Error("Error closing KDB connection", "error", err)
		}
	}
	safeCloseIntChan(d.signals)
	safeCloseSyncQueryChan(d.syncQueue)
	safeCloseSyncResChan(d.syncResChan)
	safeCloseStructChan(d.asyncJobs)
}

func safeCloseIntChan(ch chan int) {
	defer func() { _ = recover() }()
	if ch != nil {
		close(ch)
	}
}

func safeCloseSyncQueryChan(ch chan *kdbSyncQuery) {
	defer func() { _ = recover() }()
	if ch != nil {
		close(ch)
	}
}

func safeCloseSyncResChan(ch chan *kdbSyncRes) {
	defer func() { _ = recover() }()
	if ch != nil {
		close(ch)
	}
}

func safeCloseStructChan(ch chan struct{}) {
	defer func() { _ = recover() }()
	if ch != nil {
		close(ch)
	}
}

func (d *KdbDatasource) newConnection() (*kdb.KDBConn, error) {
	log.DefaultLogger.Info("Opening connection to kdb+", "host", d.Host, "port", d.Port)
	auth := fmt.Sprintf("%s:%s", d.user, d.pass)
	var conn *kdb.KDBConn
	var err error
	if d.WithTls {
		conn, err = kdb.DialTLS(d.Host, d.Port, auth, d.TlsServerConfig)
	} else {
		conn, err = kdb.DialKDBTimeout(d.Host, d.Port, auth, d.DialTimeout)
	}
	if err != nil {
		log.DefaultLogger.Error("Error establishing kdb connection", "error", err)
		return nil, err
	}
	log.DefaultLogger.Info("Dialled kdb+ successfully", "host", d.Host, "port", d.Port)
	return conn, nil
}

func (d *KdbDatasource) openConnection() error {
	conn, err := d.newConnection()
	if err != nil {
		d.KdbHandle = nil
		return err
	}
	d.KdbHandle = conn
	d.IsOpen = true

	log.DefaultLogger.Info("Making raw response channel")
	d.rawReadChan = make(chan *kdbRawRead, 16)

	log.DefaultLogger.Info("Beginning handle listener")
	go d.KdbHandleListener()
	return nil
}

func (d *KdbDatasource) closeConnection() error {
	if !d.IsOpen {
		log.DefaultLogger.Info("Connection already closed", "host", d.Host, "port", d.Port)
		return nil
	}
	log.DefaultLogger.Info("Closing connection", "host", d.Host, "port", d.Port)
	err := d.KdbHandle.Close()
	if err != nil {
		log.DefaultLogger.Error("Error closing handle", "host", d.Host, "port", d.Port, "error", err)
	}
	d.IsOpen = false
	return err
}

func (d *KdbDatasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	response := backend.NewQueryDataResponse()

	var wg sync.WaitGroup
	var mu sync.Mutex
	for index, q := range req.Queries {
		wg.Add(1)
		go func(index int, q backend.DataQuery) {
			defer wg.Done()
			res := d.query(ctx, req.PluginContext, q, syncDiagnosticRequestID(req, q, index))
			mu.Lock()
			response.Responses[q.RefID] = res
			mu.Unlock()
		}(index, q)
	}
	wg.Wait()
	return response, nil
}

func (d *KdbDatasource) query(_ context.Context, pCtx backend.PluginContext, query backend.DataQuery, requestID string) backend.DataResponse {
	var model QueryModel
	response := backend.DataResponse{}
	err := json.Unmarshal(query.JSON, &model)
	if err != nil {
		d.logDiagnosticError("unable to decode query JSON", "requestID", requestID, "refID", query.RefID, "error", err.Error())
		response.Error = err
		return response
	}
	d.normalizeQueryModel(&model)
	fields := d.diagnosticQueryFields(pCtx, query, model, requestID)
	start := time.Now()
	d.logDiagnostics("sync query received", fields...)
	if model.ExecutionMode != ExecutionModeSync {
		response.Error = fmt.Errorf("%s mode is served through Grafana Live, not the standard query endpoint", model.ExecutionMode)
		d.logDiagnosticError("sync query rejected", appendDiagnosticError(fields, response.Error)...)
		return response
	}
	if err := prepareQueryForExecution(pCtx, query, &model); err != nil {
		d.logDiagnosticError("query preparation failed", appendDiagnosticError(fields, err)...)
		response.Error = err
		return response
	}
	fields = d.diagnosticQueryFields(pCtx, query, model, requestID)
	d.logDiagnostics("sync query prepared", fields...)

	result, err := d.runSyncQueryWithCache(pCtx, query, model, fields)
	if err != nil {
		d.logDiagnosticError(result.errorMessage, appendDiagnosticError(result.fields, err)...)
		response.Error = err
		return response
	}
	fields = appendDiagnosticFrames(result.fields, result.frames)
	fields = append(fields, "durationMs", time.Since(start).Milliseconds())
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

func parseKdbResponseToFrames(kdbResponse *kdb.K, model QueryModel, refID string) ([]*data.Frame, error) {
	var frames []*data.Frame
	switch {
	case kdbResponse == nil:
		return nil, fmt.Errorf("kdb+ returned nil response")
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

func (d *KdbDatasource) CheckHealth(_ context.Context, req *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	pCtx := backend.PluginContext{}
	if req != nil {
		pCtx = req.PluginContext
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

	test, err := d.RunKdbQuerySync(kdb.NewList(kdb.Atom(kdb.KC, "{[x] value x[`Query;`Query]}"), kdb.NewDict(k, v)), d.DialTimeout)
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

	if test.Type != -kdb.KJ {
		status = backend.HealthStatusError
		message = fmt.Sprintf("kdb+ result not of expected type; received type %v", test.Type)
		d.logDiagnosticError("health check returned unexpected type", append(healthFields, "kdbResponse", describeKdbObject(test))...)
		return &backend.CheckHealthResult{
			Status:  status,
			Message: message,
		}, nil
	}
	val := test.Data.(int64)

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
