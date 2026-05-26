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

type syncQueryCache struct {
	mu         sync.Mutex
	entries    map[string]*syncQueryCacheEntry
	maxEntries int
	ttl        time.Duration
	timeBucket time.Duration
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
}

type syncQueryCacheKeyPayload struct {
	OrgID           int64              `json:"orgID"`
	User            syncQueryCacheUser `json:"user"`
	Datasource      syncQueryCacheDS   `json:"datasource"`
	RefID           string             `json:"refID"`
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
	cache := d.syncQueryCache()
	if cache == nil {
		return d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, false, "disabled", "", 0, false, false))
	}
	if syncQueryCacheBypassed(model) {
		return d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, false, "bypassed", "", 0, false, false))
	}

	cacheKey, err := syncQueryCacheKey(pCtx, query, model, cache.timeBucket)
	if err != nil {
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, true, "key-error", "", 0, false, false)
		result, runErr := d.runSyncQueryUncached(pCtx, query, model, cacheFields)
		if runErr != nil {
			return result, runErr
		}
		result.fields = appendDiagnosticError(result.fields, err)
		d.logDiagnosticError("sync query cache key failed", appendDiagnosticError(cacheFields, err)...)
		return result, nil
	}

	if hit, ok := cache.get(cacheKey, query.RefID); ok {
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, true, "hit", cacheKey, hit.age, false, false)
		d.logDiagnostics("sync query cache hit", cacheFields...)
		return syncQueryResult{frames: hit.frames, fields: cacheFields}, nil
	}

	value, err, shared := cache.group.Do(cacheKey, func() (interface{}, error) {
		if hit, ok := cache.get(cacheKey, query.RefID); ok {
			return syncQueryCacheLookup{frames: hit.frames, age: hit.age, key: cacheKey}, nil
		}
		result, err := d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, true, "miss", cacheKey, 0, false, false))
		if err != nil {
			return result, err
		}
		cache.put(cacheKey, result.frames, query.RefID)
		result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
		result.fields = appendSyncQueryCacheDiagnosticFields(result.fields, true, "stored", cacheKey, 0, false, true)
		return result, nil
	})
	if err != nil {
		if result, ok := value.(syncQueryResult); ok {
			return result, err
		}
		return syncQueryResult{fields: appendSyncQueryCacheDiagnosticFields(fields, true, "miss", cacheKey, 0, shared, false), errorMessage: "sync query failed"}, err
	}

	switch result := value.(type) {
	case syncQueryCacheLookup:
		frames := result.frames
		if shared {
			frames = cloneFramesForRefID(frames, query.RefID, query.RefID)
		}
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, true, "hit", cacheKey, result.age, shared, false)
		d.logDiagnostics("sync query cache hit", cacheFields...)
		return syncQueryResult{frames: frames, fields: cacheFields}, nil
	case syncQueryResult:
		if shared {
			result.fields = append(result.fields, "queryCacheShared", true)
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
		}
		return result, nil
	default:
		return syncQueryResult{fields: fields, errorMessage: "sync query failed"}, fmt.Errorf("unexpected sync query cache result type %T", value)
	}
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

func (d *KdbDatasource) syncQueryCache() *syncQueryCache {
	d.normalizeDatasourceDefaults()
	if !d.QueryCacheEnabled {
		return nil
	}

	d.queryCacheMu.Lock()
	defer d.queryCacheMu.Unlock()
	ttl := time.Duration(d.QueryCacheTTLSeconds) * time.Second
	timeBucket := time.Duration(d.QueryCacheTimeBucketSeconds) * time.Second
	if d.queryCache == nil || d.queryCache.maxEntries != d.QueryCacheMaxEntries || d.queryCache.ttl != ttl || d.queryCache.timeBucket != timeBucket {
		d.queryCache = &syncQueryCache{
			entries:    make(map[string]*syncQueryCacheEntry),
			maxEntries: d.QueryCacheMaxEntries,
			ttl:        ttl,
			timeBucket: timeBucket,
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

func (c *syncQueryCache) get(key string, refID string) (syncQueryCacheLookup, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return syncQueryCacheLookup{}, false
	}
	if now.Sub(entry.createdAt) > c.ttl {
		delete(c.entries, key)
		return syncQueryCacheLookup{}, false
	}
	entry.lastAccess = now
	return syncQueryCacheLookup{
		frames: cloneFramesForRefID(entry.frames, entry.refID, refID),
		age:    now.Sub(entry.createdAt),
		key:    key,
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

func syncQueryCacheKey(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, timeBucket time.Duration) (string, error) {
	originalQuery := model.OriginalQueryText
	if originalQuery == "" {
		originalQuery = model.QueryText
	}
	timeBucketSeconds := int64(0)
	if timeBucket > 0 {
		timeBucketSeconds = int64(timeBucket / time.Second)
	}
	payload := syncQueryCacheKeyPayload{
		OrgID:           pCtx.OrgID,
		RefID:           query.RefID,
		QueryType:       query.QueryType,
		MaxDataPoints:   query.MaxDataPoints,
		IntervalNs:      int64(query.Interval),
		TimeFrom:        syncQueryCacheTime(query.TimeRange.From, timeBucket),
		TimeTo:          syncQueryCacheTime(query.TimeRange.To, timeBucket),
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

func syncQueryCacheBypassed(model QueryModel) bool {
	return strings.Contains(model.QueryText, "asyncq:cache=off") ||
		strings.Contains(model.QueryText, "asyncq:cache=bypass") ||
		strings.Contains(model.OriginalQueryText, "asyncq:cache=off") ||
		strings.Contains(model.OriginalQueryText, "asyncq:cache=bypass")
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

func appendSyncQueryCacheDiagnosticFields(fields []interface{}, enabled bool, status string, key string, age time.Duration, shared bool, stored bool) []interface{} {
	fields = append(fields,
		"queryCacheEnabled", enabled,
		"queryCacheStatus", status,
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
	return fields
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
