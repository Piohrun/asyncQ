package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	disk       syncQueryDiskCachePolicy
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
	frames    []*data.Frame
	age       time.Duration
	key       string
	stale     bool
	createdAt time.Time
	storage   string
}

type syncQueryDiskCachePolicy struct {
	enabled    bool
	dir        string
	maxBytes   int64
	maxEntries int
}

type syncQueryDiskCache struct {
	mu         sync.Mutex
	dir        string
	maxBytes   int64
	maxEntries int
}

type syncQueryDiskCacheFile struct {
	Version    int           `json:"version"`
	Key        string        `json:"key"`
	RefID      string        `json:"refID"`
	CreatedAt  time.Time     `json:"createdAt"`
	LastAccess time.Time     `json:"lastAccess"`
	Frames     []*data.Frame `json:"frames"`
}

type syncQueryDiskCacheFileInfo struct {
	path    string
	key     string
	size    int64
	modTime time.Time
}

type syncQueryCacheStatus struct {
	Enabled           bool                  `json:"enabled"`
	ControlEnabled    bool                  `json:"controlEnabled"`
	TTLSeconds        int64                 `json:"ttlSeconds"`
	StaleTTLSeconds   int64                 `json:"staleTTLSeconds"`
	TimeBucketSeconds int64                 `json:"timeBucketSeconds"`
	KeyMode           string                `json:"keyMode"`
	Memory            syncQueryMemoryStatus `json:"memory"`
	Disk              syncQueryDiskStatus   `json:"disk"`
}

type syncQueryMemoryStatus struct {
	Enabled    bool                    `json:"enabled"`
	Entries    int                     `json:"entries"`
	MaxEntries int                     `json:"maxEntries"`
	Keys       []syncQueryCacheKeyInfo `json:"keys,omitempty"`
}

type syncQueryDiskStatus struct {
	Enabled    bool                    `json:"enabled"`
	Path       string                  `json:"path,omitempty"`
	Exists     bool                    `json:"exists"`
	Entries    int                     `json:"entries"`
	Bytes      int64                   `json:"bytes"`
	MaxEntries int                     `json:"maxEntries"`
	MaxBytes   int64                   `json:"maxBytes"`
	Keys       []syncQueryCacheKeyInfo `json:"keys,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

type syncQueryCacheKeyInfo struct {
	Key       string `json:"key"`
	AgeMs     int64  `json:"ageMs,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
	Storage   string `json:"storage"`
	RefID     string `json:"refID,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
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
	policyStart := time.Now()
	policy := d.syncQueryCachePolicy(model)
	fields = appendDiagnosticDuration(fields, "profileCachePolicyMs", policyStart)
	cache := d.syncQueryCache(policy)
	if cache == nil {
		status := "disabled"
		if policy.mode == QueryCacheModeBypass {
			status = "bypassed"
		}
		return d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, policy, status, "none", "", 0, false, false, false))
	}
	diskCache := d.syncQueryDiskCache(policy)

	keyStart := time.Now()
	cacheKey, err := syncQueryCacheKey(pCtx, query, model, policy)
	fields = appendDiagnosticDuration(fields, "profileCacheKeyMs", keyStart)
	if err != nil {
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, "key-error", "none", "", 0, false, false, false)
		result, runErr := d.runSyncQueryUncached(pCtx, query, model, cacheFields)
		if runErr != nil {
			return result, runErr
		}
		result.fields = appendDiagnosticError(result.fields, err)
		d.logDiagnosticError("sync query cache key failed", appendDiagnosticError(cacheFields, err)...)
		return result, nil
	}

	if policy.read {
		memoryLookupStart := time.Now()
		if hit, ok := cache.get(cacheKey, query.RefID, policy); ok {
			status := "hit"
			refreshStarted := false
			if hit.stale {
				status = "stale"
				refreshStarted = d.refreshSyncQueryCache(cache, diskCache, pCtx, query, model, policy, cacheKey, fields)
			}
			cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, status, hit.storage, cacheKey, hit.age, false, false, refreshStarted)
			cacheFields = appendDiagnosticDuration(cacheFields, "profileCacheMemoryLookupMs", memoryLookupStart)
			d.logDiagnostics("sync query cache "+status, cacheFields...)
			return syncQueryResult{frames: hit.frames, fields: cacheFields}, nil
		}
		fields = appendDiagnosticDuration(fields, "profileCacheMemoryLookupMs", memoryLookupStart)
		if diskCache != nil {
			diskLookupStart := time.Now()
			hit, ok, diskErr := diskCache.get(cacheKey, query.RefID, policy)
			fields = appendDiagnosticDuration(fields, "profileCacheDiskLookupMs", diskLookupStart)
			if diskErr != nil {
				d.logDiagnosticError("sync query disk cache read failed", appendSyncQueryCacheDiskError(appendSyncQueryCacheDiagnosticFields(fields, policy, "disk-error", "disk", cacheKey, 0, false, false, false), diskErr)...)
			} else if ok {
				cache.putWithCreatedAt(cacheKey, hit.frames, query.RefID, hit.createdAt)
				status := "hit"
				refreshStarted := false
				if hit.stale {
					status = "stale"
					refreshStarted = d.refreshSyncQueryCache(cache, diskCache, pCtx, query, model, policy, cacheKey, fields)
				}
				cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, status, hit.storage, cacheKey, hit.age, false, false, refreshStarted)
				d.logDiagnostics("sync query disk cache "+status, cacheFields...)
				return syncQueryResult{frames: hit.frames, fields: cacheFields}, nil
			}
		}
	}

	singleflightStart := time.Now()
	value, err, shared := cache.group.Do(cacheKey, func() (interface{}, error) {
		if policy.read {
			if hit, ok := cache.get(cacheKey, query.RefID, policy); ok {
				return hit, nil
			}
			if diskCache != nil {
				hit, ok, diskErr := diskCache.get(cacheKey, query.RefID, policy)
				if diskErr != nil {
					d.logDiagnosticError("sync query disk cache read failed", appendSyncQueryCacheDiskError(appendSyncQueryCacheDiagnosticFields(fields, policy, "disk-error", "disk", cacheKey, 0, false, false, false), diskErr)...)
				} else if ok {
					cache.putWithCreatedAt(cacheKey, hit.frames, query.RefID, hit.createdAt)
					return hit, nil
				}
			}
		}

		status := "miss"
		if !policy.read {
			status = "refresh"
		}
		result, err := d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(fields, policy, status, "none", cacheKey, 0, false, false, false))
		if err != nil {
			return result, err
		}
		if policy.store {
			cache.put(cacheKey, result.frames, query.RefID)
			storage := "memory"
			if diskCache != nil {
				if err := diskCache.put(cacheKey, result.frames, query.RefID); err != nil {
					result.fields = appendSyncQueryCacheDiskError(result.fields, err)
					d.logDiagnosticError("sync query disk cache store failed", appendSyncQueryCacheDiskError(appendSyncQueryCacheDiagnosticFields(fields, policy, "disk-error", "disk", cacheKey, 0, false, false, false), err)...)
				} else {
					storage = "memory+disk"
				}
			}
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
			result.fields = appendSyncQueryCacheDiagnosticFields(result.fields, policy, "stored", storage, cacheKey, 0, false, true, false)
		}
		return result, nil
	})
	singleflightMs := diagnosticDurationMs(time.Since(singleflightStart))
	if err != nil {
		if result, ok := value.(syncQueryResult); ok {
			result.fields = append(result.fields, "profileCacheSingleflightMs", singleflightMs)
			return result, err
		}
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, "miss", "none", cacheKey, 0, shared, false, false)
		cacheFields = append(cacheFields, "profileCacheSingleflightMs", singleflightMs)
		return syncQueryResult{fields: cacheFields, errorMessage: "sync query failed"}, err
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
			refreshStarted = d.refreshSyncQueryCache(cache, diskCache, pCtx, query, model, policy, cacheKey, fields)
		}
		cacheFields := appendSyncQueryCacheDiagnosticFields(fields, policy, status, result.storage, cacheKey, result.age, shared, false, refreshStarted)
		cacheFields = append(cacheFields, "profileCacheSingleflightMs", singleflightMs)
		d.logDiagnostics("sync query cache "+status, cacheFields...)
		return syncQueryResult{frames: frames, fields: cacheFields}, nil
	case syncQueryResult:
		result.fields = append(result.fields, "profileCacheSingleflightMs", singleflightMs)
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

func (d *KdbDatasource) refreshSyncQueryCache(cache *syncQueryCache, diskCache *syncQueryDiskCache, pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, policy syncQueryCachePolicy, cacheKey string, fields []interface{}) bool {
	if !policy.store || !cache.startRefresh(cacheKey) {
		return false
	}

	go func() {
		defer cache.finishRefresh(cacheKey)
		refreshFields := cloneDiagnosticFields(fields)
		value, err, _ := cache.group.Do(cacheKey, func() (interface{}, error) {
			result, runErr := d.runSyncQueryUncached(pCtx, query, model, appendSyncQueryCacheDiagnosticFields(refreshFields, policy, "refresh", "none", cacheKey, 0, false, false, false))
			if runErr != nil {
				return result, runErr
			}
			cache.put(cacheKey, result.frames, query.RefID)
			storage := "memory"
			if diskCache != nil {
				if err := diskCache.put(cacheKey, result.frames, query.RefID); err != nil {
					result.fields = appendSyncQueryCacheDiskError(result.fields, err)
					d.logDiagnosticError("sync query disk cache refresh store failed", appendSyncQueryCacheDiskError(appendSyncQueryCacheDiagnosticFields(refreshFields, policy, "disk-error", "disk", cacheKey, 0, false, false, false), err)...)
				} else {
					storage = "memory+disk"
				}
			}
			result.frames = cloneFramesForRefID(result.frames, query.RefID, query.RefID)
			result.fields = appendSyncQueryCacheDiagnosticFields(result.fields, policy, "stored", storage, cacheKey, 0, false, true, false)
			return result, nil
		})
		if err != nil {
			if result, ok := value.(syncQueryResult); ok {
				d.logDiagnosticError("sync query cache refresh failed", appendDiagnosticError(result.fields, err)...)
				return
			}
			d.logDiagnosticError("sync query cache refresh failed", appendDiagnosticError(appendSyncQueryCacheDiagnosticFields(refreshFields, policy, "refresh", "none", cacheKey, 0, false, false, false), err)...)
			return
		}
		if result, ok := value.(syncQueryResult); ok {
			d.logDiagnostics("sync query cache refreshed", appendDiagnosticFrames(result.fields, result.frames)...)
		}
	}()
	return true
}

func (d *KdbDatasource) runSyncQueryUncached(pCtx backend.PluginContext, query backend.DataQuery, model QueryModel, fields []interface{}) (syncQueryResult, error) {
	payloadStart := time.Now()
	payload := buildSyncQueryPayload(pCtx, query, model)
	fields = appendDiagnosticDuration(fields, "profilePayloadBuildMs", payloadStart)

	kdbCallStart := time.Now()
	kdbResponse, err := d.RunKdbQuerySync(payload, time.Duration(model.Timeout)*time.Millisecond, fields...)
	fields = appendDiagnosticDuration(fields, "profileKdbCallMs", kdbCallStart)
	if err != nil {
		return syncQueryResult{fields: fields, errorMessage: "sync query failed"}, err
	}
	fields = appendDiagnosticKdbObject(fields, "kdbResponse", kdbResponse)

	parseStart := time.Now()
	frames, err := parseKdbResponseToFrames(kdbResponse, model, query.RefID)
	fields = appendDiagnosticDuration(fields, "profileFrameParseMs", parseStart)
	if err != nil {
		return syncQueryResult{fields: fields, errorMessage: "sync result parse failed"}, err
	}
	fields = appendDiagnosticFrameProfile(fields, frames)
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
		disk: syncQueryDiskCachePolicy{
			enabled:    d.QueryCacheDiskEnabled,
			dir:        d.syncQueryDiskCacheDir(),
			maxBytes:   d.QueryCacheDiskMaxBytes,
			maxEntries: d.QueryCacheDiskMaxEntries,
		},
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

func (d *KdbDatasource) syncQueryDiskCache(policy syncQueryCachePolicy) *syncQueryDiskCache {
	if !policy.enabled || !policy.disk.enabled || policy.disk.dir == "" {
		return nil
	}

	d.queryDiskCacheMu.Lock()
	defer d.queryDiskCacheMu.Unlock()
	if d.queryDiskCache == nil ||
		d.queryDiskCache.dir != policy.disk.dir ||
		d.queryDiskCache.maxBytes != policy.disk.maxBytes ||
		d.queryDiskCache.maxEntries != policy.disk.maxEntries {
		d.queryDiskCache = &syncQueryDiskCache{
			dir:        policy.disk.dir,
			maxBytes:   policy.disk.maxBytes,
			maxEntries: policy.disk.maxEntries,
		}
	}
	return d.queryDiskCache
}

func (d *KdbDatasource) syncQueryDiskCacheDir() string {
	base := strings.TrimSpace(d.QueryCacheDiskPath)
	if base == "" {
		if grafanaData := strings.TrimSpace(os.Getenv("GF_PATHS_DATA")); grafanaData != "" {
			base = filepath.Join(grafanaData, "plugins", "asyncq-kdbbackend-datasource", "query-cache")
		} else if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
			base = filepath.Join(userCache, "asyncq-kdbbackend-datasource", "query-cache")
		} else {
			base = filepath.Join(os.TempDir(), "asyncq-kdbbackend-datasource", "query-cache")
		}
	}
	return filepath.Join(base, d.syncQueryDiskCacheNamespace())
}

func (d *KdbDatasource) syncQueryDiskCacheNamespace() string {
	identity := fmt.Sprintf("%d|%s|%s|%s|%d|%s", d.instanceID, d.instanceUID, d.instanceName, d.Host, d.Port, d.user)
	sum := sha256.Sum256([]byte(identity))
	prefix := d.instanceUID
	if prefix == "" {
		prefix = d.instanceName
	}
	if prefix == "" {
		prefix = fmt.Sprintf("%s-%d", d.Host, d.Port)
	}
	prefix = diagnosticIDPart(prefix)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(sum[:])[:16])
}

func (d *KdbDatasource) closeSyncQueryCache() {
	d.queryCacheMu.Lock()
	cache := d.queryCache
	d.queryCache = nil
	d.queryCacheMu.Unlock()
	if cache != nil {
		cache.clear()
	}
	d.queryDiskCacheMu.Lock()
	d.queryDiskCache = nil
	d.queryDiskCacheMu.Unlock()
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
		frames:    cloneFramesForRefID(entry.frames, entry.refID, refID),
		age:       age,
		key:       key,
		stale:     stale,
		createdAt: entry.createdAt,
		storage:   "memory",
	}, true
}

func (c *syncQueryCache) put(key string, frames []*data.Frame, refID string) {
	c.putWithCreatedAt(key, frames, refID, time.Now())
}

func (c *syncQueryCache) putWithCreatedAt(key string, frames []*data.Frame, refID string, createdAt time.Time) {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.entries[key] = &syncQueryCacheEntry{
		frames:     cloneFramesForRefID(frames, refID, refID),
		refID:      refID,
		createdAt:  createdAt,
		lastAccess: now,
	}
	c.evictLocked()
}

func (c *syncQueryCache) clearKey(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, existed := c.entries[key]
	delete(c.entries, key)
	delete(c.refreshing, key)
	return existed
}

func (c *syncQueryCache) clearExpired(policy syncQueryCachePolicy) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0
	for key, entry := range c.entries {
		if age := now.Sub(entry.createdAt); age > policy.ttl+policy.staleTTL {
			delete(c.entries, key)
			removed++
		}
	}
	return removed
}

func (c *syncQueryCache) clear() {
	_ = c.clearCount()
}

func (c *syncQueryCache) clearCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := len(c.entries)
	c.entries = make(map[string]*syncQueryCacheEntry)
	c.refreshing = make(map[string]struct{})
	return removed
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

func (c *syncQueryCache) status(policy syncQueryCachePolicy, includeKeys bool) syncQueryMemoryStatus {
	status := syncQueryMemoryStatus{
		Enabled:    policy.enabled,
		MaxEntries: c.maxEntries,
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	status.Entries = len(c.entries)
	if !includeKeys {
		return status
	}

	now := time.Now()
	status.Keys = make([]syncQueryCacheKeyInfo, 0, len(c.entries))
	for key, entry := range c.entries {
		age := now.Sub(entry.createdAt)
		status.Keys = append(status.Keys, syncQueryCacheKeyInfo{
			Key:     key,
			AgeMs:   age.Milliseconds(),
			Stale:   age > policy.ttl,
			Storage: "memory",
			RefID:   entry.refID,
		})
	}
	sort.Slice(status.Keys, func(i, j int) bool {
		return status.Keys[i].Key < status.Keys[j].Key
	})
	return status
}

func (c *syncQueryDiskCache) get(key string, refID string, policy syncQueryCachePolicy) (syncQueryCacheLookup, bool, error) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.readEntryLocked(key)
	if err != nil {
		if os.IsNotExist(err) {
			return syncQueryCacheLookup{}, false, nil
		}
		_, _ = c.removeKeyLocked(key)
		return syncQueryCacheLookup{}, false, err
	}
	age := now.Sub(entry.CreatedAt)
	stale := false
	if age > policy.ttl {
		if policy.staleTTL <= 0 || age > policy.ttl+policy.staleTTL {
			_, _ = c.removeKeyLocked(key)
			return syncQueryCacheLookup{}, false, nil
		}
		stale = true
	}
	if path, err := c.filePath(key); err == nil {
		_ = os.Chtimes(path, now, now)
	}
	return syncQueryCacheLookup{
		frames:    cloneFramesForRefID(entry.Frames, entry.RefID, refID),
		age:       age,
		key:       key,
		stale:     stale,
		createdAt: entry.CreatedAt,
		storage:   "disk",
	}, true, nil
}

func (c *syncQueryDiskCache) put(key string, frames []*data.Frame, refID string) error {
	now := time.Now()
	entry := syncQueryDiskCacheFile{
		Version:    1,
		Key:        key,
		RefID:      refID,
		CreatedAt:  now,
		LastAccess: now,
		Frames:     cloneFramesForRefID(frames, refID, refID),
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	path, err := c.filePath(key)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%d", path, os.Getpid(), now.UnixNano())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(path, now, now)
	return c.enforceLimitsLocked()
}

func (c *syncQueryDiskCache) clear() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	files, err := c.listFilesLocked()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, file := range files {
		if err := os.Remove(file.path); err == nil {
			removed++
		}
	}
	return removed, nil
}

func (c *syncQueryDiskCache) clearKey(key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removeKeyLocked(key)
}

func (c *syncQueryDiskCache) clearExpired(policy syncQueryCachePolicy) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	files, err := c.listFilesLocked()
	if err != nil {
		return 0, err
	}
	removed := 0
	now := time.Now()
	for _, file := range files {
		entry, err := c.readEntryByPathLocked(file.path)
		if err != nil {
			if os.Remove(file.path) == nil {
				removed++
			}
			continue
		}
		if age := now.Sub(entry.CreatedAt); age > policy.ttl+policy.staleTTL {
			if os.Remove(file.path) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

func (c *syncQueryDiskCache) status(policy syncQueryCachePolicy, includeKeys bool) syncQueryDiskStatus {
	status := syncQueryDiskStatus{
		Enabled:    policy.enabled && policy.disk.enabled,
		Path:       c.dir,
		MaxEntries: c.maxEntries,
		MaxBytes:   c.maxBytes,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := os.Stat(c.dir); err == nil {
		status.Exists = true
	} else if err != nil && !os.IsNotExist(err) {
		status.Error = err.Error()
		return status
	}
	files, err := c.listFilesLocked()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Entries = len(files)
	for _, file := range files {
		status.Bytes += file.size
	}
	if !includeKeys {
		return status
	}

	now := time.Now()
	status.Keys = make([]syncQueryCacheKeyInfo, 0, len(files))
	for _, file := range files {
		info := syncQueryCacheKeyInfo{
			Key:       file.key,
			Storage:   "disk",
			SizeBytes: file.size,
		}
		if entry, err := c.readEntryByPathLocked(file.path); err == nil {
			age := now.Sub(entry.CreatedAt)
			info.AgeMs = age.Milliseconds()
			info.Stale = age > policy.ttl
			info.RefID = entry.RefID
		}
		status.Keys = append(status.Keys, info)
	}
	sort.Slice(status.Keys, func(i, j int) bool {
		return status.Keys[i].Key < status.Keys[j].Key
	})
	return status
}

func (c *syncQueryDiskCache) readEntryLocked(key string) (syncQueryDiskCacheFile, error) {
	path, err := c.filePath(key)
	if err != nil {
		return syncQueryDiskCacheFile{}, err
	}
	return c.readEntryByPathLocked(path)
}

func (c *syncQueryDiskCache) readEntryByPathLocked(path string) (syncQueryDiskCacheFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return syncQueryDiskCacheFile{}, err
	}
	var entry syncQueryDiskCacheFile
	if err := json.Unmarshal(raw, &entry); err != nil {
		return syncQueryDiskCacheFile{}, err
	}
	if entry.Version != 1 {
		return syncQueryDiskCacheFile{}, fmt.Errorf("unsupported disk cache version %d", entry.Version)
	}
	if entry.Key == "" || entry.CreatedAt.IsZero() {
		return syncQueryDiskCacheFile{}, fmt.Errorf("invalid disk cache entry")
	}
	return entry, nil
}

func (c *syncQueryDiskCache) removeKeyLocked(key string) (bool, error) {
	path, err := c.filePath(key)
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (c *syncQueryDiskCache) enforceLimitsLocked() error {
	files, err := c.listFilesLocked()
	if err != nil {
		return err
	}
	total := int64(0)
	for _, file := range files {
		total += file.size
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	removed := 0
	for _, file := range files {
		entriesOver := c.maxEntries > 0 && len(files)-removed > c.maxEntries
		bytesOver := c.maxBytes > 0 && total > c.maxBytes
		if !entriesOver && !bytesOver {
			break
		}
		if err := os.Remove(file.path); err == nil {
			removed++
			total -= file.size
		}
	}
	return nil
}

func (c *syncQueryDiskCache) listFilesLocked() ([]syncQueryDiskCacheFileInfo, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]syncQueryDiskCacheFileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(c.dir, entry.Name())
		files = append(files, syncQueryDiskCacheFileInfo{
			path:    path,
			key:     strings.TrimSuffix(entry.Name(), ".json"),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	return files, nil
}

func (c *syncQueryDiskCache) filePath(key string) (string, error) {
	file, err := syncQueryDiskCacheFileName(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.dir, file), nil
}

func syncQueryDiskCacheFileName(key string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) < 16 || len(key) > 128 {
		return "", fmt.Errorf("invalid cache key length")
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("invalid cache key")
		}
	}
	return key + ".json", nil
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

func appendSyncQueryCacheDiagnosticFields(fields []interface{}, policy syncQueryCachePolicy, status string, storage string, key string, age time.Duration, shared bool, stored bool, refreshStarted bool) []interface{} {
	fields = append(fields,
		"queryCacheEnabled", policy.enabled,
		"queryCacheMode", policy.mode,
		"queryCacheKeyMode", policy.keyMode,
		"queryCacheStatus", status,
		"queryCacheStorage", storage,
		"queryCacheTTLSeconds", int64(policy.ttl/time.Second),
		"queryCacheStaleTTLSeconds", int64(policy.staleTTL/time.Second),
		"queryCacheTimeBucketSeconds", int64(policy.timeBucket/time.Second),
		"queryCacheDiskEnabled", policy.disk.enabled,
		"queryCacheDiskMaxEntries", policy.disk.maxEntries,
		"queryCacheDiskMaxBytes", policy.disk.maxBytes,
	)
	if policy.disk.dir != "" {
		fields = append(fields, "queryCacheDiskPathHash", diagnosticHash(policy.disk.dir))
	}
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

func appendSyncQueryCacheDiskError(fields []interface{}, err error) []interface{} {
	if err == nil {
		return fields
	}
	return append(fields, "queryCacheDiskError", err.Error())
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
