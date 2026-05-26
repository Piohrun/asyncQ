package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"golang.org/x/sync/singleflight"
)

type syncQueryResult struct {
	frames       []*data.Frame
	fields       []interface{}
	errorMessage string
}

type syncQueryCachePolicy struct {
	enabled    bool
	read       bool
	store      bool
	mode       string
	keyMode    string
	ttl        time.Duration
	staleTTL   time.Duration
	timeBucket time.Duration
}

type syncQueryCache struct {
	mu         sync.Mutex
	entries    map[string]*syncQueryCacheEntry
	refreshing map[string]struct{}
	maxEntries int
	group      singleflight.Group
}

type syncQueryCacheEntry struct {
	frames     []*data.Frame
	refID      string
	createdAt  time.Time
	lastAccess time.Time
}

type syncQueryCacheLookup struct {
	frames []*data.Frame
	age    time.Duration
	key    string
	stale  bool
}

type syncQueryCacheKeyPayload struct {
	OrgID           int64              `json:"orgID"`
	User            syncQueryCacheUser `json:"user"`
	Datasource      syncQueryCacheDS   `json:"datasource"`
	RefID           string             `json:"refID,omitempty"`
	QueryType       string             `json:"queryType"`
	MaxDataPoints   int64              `json:"maxDataPoints"`
	IntervalNs      int64              `json:"intervalNs"`
	TimeFrom        string             `json:"timeFrom"`
	TimeTo          string             `json:"timeTo"`
	TimeBucketSec   int64              `json:"timeBucketSec,omitempty"`
	QueryText       string             `json:"queryText"`
	OriginalQuery   string             `json:"originalQuery"`
	Execution       string             `json:"execution"`
	Compatibility   string             `json:"compatibility"`
	UseTimeColumn   bool               `json:"useTimeColumn"`
	TimeColumn      string             `json:"timeColumn"`
	IncludeKeys     bool               `json:"includeKeys"`
	PanoWrapper     string             `json:"panoWrapper"`
	PanoRequestFunc string             `json:"panoRequestFunc"`
	CacheKeyMode    string             `json:"cacheKeyMode"`
}

type syncQueryCacheUser struct {
	Login string `json:"login,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Role  string `json:"role,omitempty"`
}

type syncQueryCacheDS struct {
	ID   int64  `json:"id,omitempty"`
	UID  string `json:"uid,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
	User string `json:"user,omitempty"`
}

func (d *KdbDatasource) runSyncQueryWithCache(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, fields []interface{}) (syncQueryResult, error) {
	policy := d.syncQueryCachePolicy(model)
	cache := d.syncQueryCache(policy)
	if cache == nil {
		status := "disabled"
		if policy.mode == QueryCacheModeBypass {
			status = "bypassed"
		}
		return d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, policy, status, "", 0, false, false, false))
	}

	cacheKey, err := syncQueryCacheKey(pCtx, query, model, policy)
	if err != nil {
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, "key-error", "", 0, false, false, false)
		result, runErr := d.runSyncQueryUncached(pCtx, query, model, cacheFields)
		if runErr != nil {
			return result, runErr
		}
		result.fields = appendDiagnosticError(result.fields, err)
		d.logDiagnosticError("sync query cache key failed", appendDiagnosticError(cacheFields, err)...)
		return result, nil
	}

	if policy.read {
		if hit, ok := cache.get(cacheKey, query.RefID, policy); ok {
			status := "hit"
			refreshStarted := false
			if hit.stale {
				status = "stale"
				refreshStarted = d.refreshSyncQueryCache(cache, pCtx, query, model, policy, cacheKey, fields)
			}
			cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, status, cacheKey, hit.age, false, false, refreshStarted)
			d.logDiagnostics("sync query cache "+status, cacheFields...)
			return syncQueryResult{frames: hit.frames, fields: cacheFields}, nil
		}
	}

	value, err, shared := cache.group.Do(cacheKey, func() (interface{}, error) {
		if policy.read {
			if hit, ok := cache.get(cacheKey, query.RefID, policy); ok {
				return hit, nil
			}
		}

		status := "miss"
		if !policy.read {
			status = "refresh"
		}
		result, err := d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, policy, status, cacheKey, 0, false, false, false))
		if err != nil {
			return result, err
		}
		if policy.store {
			cache.put(cacheKey, result.frames, query.RefID)
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
			result.fields = appendSyncQueryCacheDiagnosticFields(result.fields, policy, "stored", cacheKey, 0, false, true, false)
		}
		return result, nil
	})
	if err != nil {
		if result, ok := value.(syncQueryResult); ok {
			return result, err
		}
		return syncQueryResult{fields: appendSyncQueryCacheDiagnosticFields(fields, policy, "miss", cacheKey, 0, shared, false, false), errorMessage: "sync query failed"}, err
	}

	switch result := value.(type) {
	case syncQueryCacheLookup:
		frames := result.frames
		if shared {
			frames = cloneFramesForRefID(frames, query.RefID, query.RefID)
		}
		status := "hit"
		refreshStarted := false
		if result.stale {
			status = "stale"
			refreshStarted = d.refreshSyncQueryCache(cache, pCtx, query, model, policy, cacheKey, fields)
		}
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, status, cacheKey, result.age, shared, false, refreshStarted)
		d.logDiagnostics("sync query cache "+status, cacheFields...)
		return syncQueryResult{frames: frames, fields: cacheFields}, nil
	case syncQueryResult:
		if shared {
			result.fields = cloneDiagnosticFields(result.fields)
			result.fields = append(result.fields, "queryCacheShared", true)
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
		}
		return result, nil
	default:
		return syncQueryResult{fields: fields, errorMessage: "sync query failed"}, fmt.Errorf("unexpected sync query cache result type %T", value)
	}
}

func (d *KdbDatasource) refreshSyncQueryCache(cache *syncQueryCache, pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, policy syncQueryCachePolicy, cacheKey string, fields []interface{}) bool {
	if !policy.store || !cache.startRefresh(cacheKey) {
		return false
	}

	go func() {
		defer cache.finishRefresh(cacheKey)
		refreshFields := cloneDiagnosticFields(fields)
		value, err, _ := cache.group.Do(cacheKey, func() (interface{}, error) {
			result, runErr := d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(refreshFields, policy, "refresh", cacheKey, 0, false, false, false))
			if runErr != nil {
				return result, runErr
			}
			cache.put(cacheKey, result.frames, query.RefID)
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
			result.fields = appendSyncQueryCacheDiagnosticFields(result.fields, policy, "stored", cacheKey, 0, false, true, false)
			return result, nil
		})
		if err != nil {
			if result, ok := value.(syncQueryResult); ok {
				d.logDiagnosticError("sync query cache refresh failed", appendDiagnosticError(result.fields, err)...)
				return
			}
			d.logDiagnosticError("sync query cache refresh failed", appendDiagnosticError(appendSyncQueryCacheDiagnosticFields(refreshFields, policy, "refresh", cacheKey, 0, false, false, false), err)...)
			return
		}
		if result, ok := value.(syncQueryResult); ok {
			d.logDiagnostics("sync query cache refreshed", appendDiagnosticFrames(result.fields, result.frames)...)
		}
	}()
	return true
}

func (d *KdbDatasource) runSyncQueryUncached(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, fields []interface{}) (syncQueryResult, error) {
	kdbResponse, err := d.RunKdbQuerySync(buildSyncQueryPayload(pCtx, query, model), time.Duration(model.Timeout)*time.Millisecond, fields...)
	if err != nil {
		return syncQueryResult{fields: fields, errorMessage: "sync query failed"}, err
	}
	fields = appendDiagnosticKdbObject(fields, "kdbResponse", kdbResponse)

	frames, err := parseKdbResponseToFrames(kdbResponse, model, query.RefID)
	if err != nil {
		return syncQueryResult{fields: fields, errorMessage: "sync result parse failed"}, err
	}
	return syncQueryResult{frames: frames, fields: fields}, nil
}

func (d *KdbDatasource) syncQueryCachePolicy(model QueryModel) syncQueryCachePolicy {
	d.normalizeDatasourceDefaults()

	mode := normalizeSyncQueryCacheMode(model.QueryCacheMode)
	if directive := syncQueryCacheDirective(model); directive != "" {
		mode = directive
	}

	enabled := d.QueryCacheEnabled
	read := true
	store := true
	switch mode {
	case QueryCacheModeEnabled:
		enabled = true
	case QueryCacheModeDisabled:
		enabled = false
		store = false
	case QueryCacheModeBypass:
		enabled = false
		read = false
		store = false
	case QueryCacheModeRefresh:
		enabled = true
		read = false
	default:
		mode = QueryCacheModeDefault
	}

	ttlSeconds := queryCacheIntOverride(model.QueryCacheTTLSeconds, d.QueryCacheTTLSeconds)
	if ttlSeconds < 1 {
		ttlSeconds = defaultQueryCacheTTL
	}
	staleSeconds := queryCacheIntOverride(model.QueryCacheStaleTTLSeconds, d.QueryCacheStaleTTLSeconds)
	if staleSeconds < 0 {
		staleSeconds = defaultQueryCacheStaleTTL
	}
	timeBucketSeconds := queryCacheIntOverride(model.QueryCacheTimeBucketSeconds, d.QueryCacheTimeBucketSeconds)
	if timeBucketSeconds < 0 {
		timeBucketSeconds = defaultQueryCacheTimeBucket
	}

	keyMode := d.QueryCacheKeyMode
	if normalized := normalizeSyncQueryCacheKeyMode(model.QueryCacheKeyMode); normalized != "" {
		keyMode = normalized
	}

	return syncQueryCachePolicy{
		enabled:    enabled,
		read:       read,
		store:      store,
		mode:       mode,
		keyMode:    keyMode,
		ttl:        time.Duration(ttlSeconds) * time.Second,
		staleTTL:   time.Duration(staleSeconds) * time.Second,
		timeBucket: time.Duration(timeBucketSeconds) * time.Second,
	}
}

func (d *KdbDatasource) syncQueryCache(policy syncQueryCachePolicy) *syncQueryCache {
	if !policy.enabled {
		return nil
	}

	d.queryCacheMu.Lock()
	defer d.queryCacheMu.Unlock()
	if d.queryCache == nil || d.queryCache.maxEntries != d.QueryCacheMaxEntries {
		d.queryCache = &syncQueryCache{
			entries:    make(map[string]*syncQueryCacheEntry),
			refreshing: make(map[string]struct{}),
			maxEntries: d.QueryCacheMaxEntries,
		}
	}
	return d.queryCache
}

func (d *KdbDatasource) closeSyncQueryCache() {
	d.queryCacheMu.Lock()
	cache := d.queryCache
	d.queryCache = nil
	d.queryCacheMu.Unlock()
	if cache != nil {
		cache.clear()
	}
}

func (c *syncQueryCache) get(key string, refID string, policy syncQueryCachePolicy) (syncQueryCacheLookup, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return syncQueryCacheLookup{}, false
	}
	age := now.Sub(entry.createdAt)
	stale := false
	if age > policy.ttl {
		if policy.staleTTL <= 0 || age > policy.ttl+policy.staleTTL {
			delete(c.entries, key)
			return syncQueryCacheLookup{}, false
		}
		stale = true
	}
	entry.lastAccess = now
	return syncQueryCacheLookup{
		frames: cloneFramesForRefID(entry.frames, entry.refID, refID),
		age:    age,
		key:    key,
		stale:  stale,
	}, true
}

func (c *syncQueryCache) put(key string, frames []*data.Frame, refID string) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &syncQueryCacheEntry{
		frames:     cloneFramesForRefID(frames, refID, refID),
		refID:      refID,
		createdAt:  now,
		lastAccess: now,
	}
	c.evictLocked()
}

func (c *syncQueryCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*syncQueryCacheEntry)
	c.refreshing = make(map[string]struct{})
}

func (c *syncQueryCache) startRefresh(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = make(map[string]struct{})
	}
	if _, ok := c.refreshing[key]; ok {
		return false
	}
	c.refreshing[key] = struct{}{}
	return true
}

func (c *syncQueryCache) finishRefresh(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.refreshing, key)
}

func (c *syncQueryCache) evictLocked() {
	for len(c.entries) > c.maxEntries {
		var oldestKey string
		var oldestAccess time.Time
		first := true
		for key, entry := range c.entries {
			if first || entry.lastAccess.Before(oldestAccess) {
				oldestKey = key
				oldestAccess = entry.lastAccess
				first = false
			}
		}
		delete(c.entries, oldestKey)
	}
}

func syncQueryCacheKey(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, policy syncQueryCachePolicy) (string, error) {
	originalQuery := model.OriginalQueryText
	if originalQuery == "" {
		originalQuery = model.QueryText
	}
	timeBucketSeconds := int64(0)
	if policy.timeBucket > 0 {
		timeBucketSeconds = int64(policy.timeBucket / time.Second)
	}
	payload := syncQueryCacheKeyPayload{
		OrgID:           pCtx.OrgID,
		QueryType:       query.QueryType,
		MaxDataPoints:   query.MaxDataPoints,
		IntervalNs:      int64(query.Interval),
		TimeFrom:        syncQueryCacheTime(query.TimeRange.From, policy.timeBucket),
		TimeTo:          syncQueryCacheTime(query.TimeRange.To, policy.timeBucket),
		TimeBucketSec:   timeBucketSeconds,
		QueryText:       model.QueryText,
		OriginalQuery:   originalQuery,
		Execution:       model.ExecutionMode,
		Compatibility:   model.CompatibilityMode,
		UseTimeColumn:   model.UseTimeColumn,
		TimeColumn:      model.TimeColumn,
		IncludeKeys:     model.IncludeKeyColumns,
		PanoWrapper:     model.PanopticonQueryWrapper,
		PanoRequestFunc: model.PanopticonRequestFunction,
		CacheKeyMode:    policy.keyMode,
	}
	if policy.keyMode == QueryCacheKeyModeStrict {
		payload.RefID = query.RefID
	}
	if pCtx.User != nil {
		payload.User = syncQueryCacheUser{
			Login: pCtx.User.Login,
			Name:  pCtx.User.Name,
			Email: pCtx.User.Email,
			Role:  pCtx.User.Role,
		}
	}
	if pCtx.DataSourceInstanceSettings != nil {
		payload.Datasource = syncQueryCacheDS{
			ID:   pCtx.DataSourceInstanceSettings.ID,
			UID:  pCtx.DataSourceInstanceSettings.UID,
			Name: pCtx.DataSourceInstanceSettings.Name,
			URL:  pCtx.DataSourceInstanceSettings.URL,
			User: pCtx.DataSourceInstanceSettings.User,
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func syncQueryCacheDirective(model QueryModel) string {
	text := model.QueryText + "\n" + model.OriginalQueryText
	if strings.Contains(text, "asyncq:cache=off") || strings.Contains(text, "asyncq:cache=bypass") {
		return QueryCacheModeBypass
	}
	if strings.Contains(text, "asyncq:cache=refresh") {
		return QueryCacheModeRefresh
	}
	if strings.Contains(text, "asyncq:cache=on") || strings.Contains(text, "asyncq:cache=enabled") {
		return QueryCacheModeEnabled
	}
	return ""
}

func normalizeSyncQueryCacheMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", QueryCacheModeDefault:
		return QueryCacheModeDefault
	case QueryCacheModeEnabled, "on", "true":
		return QueryCacheModeEnabled
	case QueryCacheModeDisabled, "off", "false":
		return QueryCacheModeDisabled
	case QueryCacheModeBypass:
		return QueryCacheModeBypass
	case QueryCacheModeRefresh:
		return QueryCacheModeRefresh
	default:
		return QueryCacheModeDefault
	}
}

func normalizeSyncQueryCacheKeyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", QueryCacheModeDefault:
		return ""
	case QueryCacheKeyModeStrict:
		return QueryCacheKeyModeStrict
	case QueryCacheKeyModeShared:
		return QueryCacheKeyModeShared
	default:
		return ""
	}
}

func queryCacheIntOverride(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func syncQueryCacheTime(t time.Time, bucket time.Duration) string {
	if t.IsZero() {
		return ""
	}
	if bucket <= 0 {
		return diagnosticTime(t)
	}
	return t.UTC().Truncate(bucket).Format(time.RFC3339Nano)
}

func appendSyncQueryCacheDiagnosticFields(fields []interface{}, policy syncQueryCachePolicy, status string, key string, age time.Duration, shared bool, stored bool, refreshStarted bool) []interface{} {
	fields = append(fields,
		"queryCacheEnabled", policy.enabled,
		"queryCacheMode", policy.mode,
		"queryCacheKeyMode", policy.keyMode,
		"queryCacheStatus", status,
		"queryCacheTTLSeconds", int64(policy.ttl/time.Second),
		"queryCacheStaleTTLSeconds", int64(policy.staleTTL/time.Second),
		"queryCacheTimeBucketSeconds", int64(policy.timeBucket/time.Second),
	)
	if key != "" {
		fields = append(fields, "queryCacheKey", diagnosticIDPart(key))
	}
	if age > 0 {
		fields = append(fields, "queryCacheAgeMs", age.Milliseconds())
	}
	if shared {
		fields = append(fields, "queryCacheShared", shared)
	}
	if stored {
		fields = append(fields, "queryCacheStored", stored)
	}
	if refreshStarted {
		fields = append(fields, "queryCacheRefreshStarted", refreshStarted)
	}
	return fields
}

func cloneDiagnosticFields(fields []interface{}) []interface{} {
	cloned := make([]interface{}, len(fields))
	copy(cloned, fields)
	return cloned
}

func cloneFramesForRefID(frames []*data.Frame, storedRefID string, refID string) []*data.Frame {
	cloned := make([]*data.Frame, 0, len(frames))
	for _, frame := range frames {
		if frame == nil {
			continue
		}
		frameCopy := frame.EmptyCopy()
		for i, field := range frame.Fields {
			frameCopy.Fields[i].AppendAll(field)
		}
		if frame.Meta != nil {
			frameCopy.Meta = cloneFrameMeta(frame.Meta)
		}
		if refID != "" {
			frameCopy.RefID = refID
			if frameCopy.Name == storedRefID {
				frameCopy.Name = refID
			}
		}
		cloned = append(cloned, frameCopy)
	}
	return cloned
}

func cloneFrameMeta(meta *data.FrameMeta) *data.FrameMeta {
	raw, err := json.Marshal(meta)
	if err != nil {
		return meta
	}
	var cloned data.FrameMeta
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return meta
	}
	return &cloned
}
