package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type cacheResourceRequest struct {
	Scope string `json:"scope,omitempty"`
	Key   string `json:"key,omitempty"`
}

type cacheResourceResponse struct {
	OK            bool                 `json:"ok"`
	Scope         string               `json:"scope,omitempty"`
	Key           string               `json:"key,omitempty"`
	MemoryRemoved int                  `json:"memoryRemoved,omitempty"`
	DiskRemoved   int                  `json:"diskRemoved,omitempty"`
	Status        syncQueryCacheStatus `json:"status,omitempty"`
	Role          string               `json:"role,omitempty"`
	Error         string               `json:"error,omitempty"`
}

func (d *KdbDatasource) CallResource(_ context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	path := strings.Trim(strings.TrimSpace(req.Path), "/")
	switch path {
	case "cache/status":
		return sendResourceJSON(sender, http.StatusOK, d.syncQueryCacheStatus(false))
	case "cache/entries":
		return sendResourceJSON(sender, http.StatusOK, d.syncQueryCacheStatus(true))
	case "cache/clear":
		if !d.canControlSyncQueryCache(req.PluginContext) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, cacheResourceResponse{OK: false, Error: err.Error()})
		}
		resp := d.clearSyncQueryCache(body.Scope)
		return sendResourceJSON(sender, http.StatusOK, resp)
	case "cache/clear-entry":
		if !d.canControlSyncQueryCache(req.PluginContext) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, cacheResourceResponse{OK: false, Error: err.Error()})
		}
		resp, status := d.clearSyncQueryCacheEntry(body.Scope, body.Key)
		return sendResourceJSON(sender, status, resp)
	case "cache/clear-expired":
		if !d.canControlSyncQueryCache(req.PluginContext) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, cacheResourceResponse{OK: false, Error: err.Error()})
		}
		resp := d.clearExpiredSyncQueryCache(body.Scope)
		return sendResourceJSON(sender, http.StatusOK, resp)
	default:
		return sendResourceJSON(sender, http.StatusNotFound, cacheResourceResponse{OK: false, Error: "unknown resource path"})
	}
}

func (d *KdbDatasource) canControlSyncQueryCache(pCtx backend.PluginContext) bool {
	d.normalizeDatasourceDefaults()
	if !d.QueryCacheControlEnabled {
		return false
	}
	if pCtx.User == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(pCtx.User.Role)) {
	case "admin", "editor":
		return true
	default:
		return false
	}
}

func sendCacheControlDenied(sender backend.CallResourceResponseSender, pCtx backend.PluginContext) error {
	role := ""
	if pCtx.User != nil {
		role = pCtx.User.Role
	}
	return sendResourceJSON(sender, http.StatusForbidden, cacheResourceResponse{
		OK:    false,
		Error: "cache controls require datasource cache-control enablement and an Admin or Editor role",
		Role:  role,
	})
}

func decodeCacheResourceRequest(raw []byte, target *cacheResourceRequest) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func sendResourceJSON(sender backend.CallResourceResponseSender, status int, obj interface{}) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return sender.Send(&backend.CallResourceResponse{
		Status: status,
		Headers: map[string][]string{
			"content-type": {"application/json"},
		},
		Body: body,
	})
}

func (d *KdbDatasource) syncQueryCacheStatus(includeKeys bool) syncQueryCacheStatus {
	policy := d.syncQueryCachePolicy(QueryModel{})
	status := syncQueryCacheStatus{
		Enabled:           policy.enabled,
		ControlEnabled:    d.QueryCacheControlEnabled,
		TTLSeconds:        int64(policy.ttl / time.Second),
		StaleTTLSeconds:   int64(policy.staleTTL / time.Second),
		TimeBucketSeconds: int64(policy.timeBucket / time.Second),
		KeyMode:           policy.keyMode,
		Memory: syncQueryMemoryStatus{
			Enabled:    policy.enabled,
			MaxEntries: d.QueryCacheMaxEntries,
		},
		Disk: syncQueryDiskStatus{
			Enabled:    policy.enabled && policy.disk.enabled,
			Path:       policy.disk.dir,
			MaxEntries: policy.disk.maxEntries,
			MaxBytes:   policy.disk.maxBytes,
		},
	}

	if cache := d.currentSyncQueryCache(); cache != nil {
		status.Memory = cache.status(policy, includeKeys)
	}
	if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
		status.Disk = diskCache.status(policy, includeKeys)
	}
	return status
}

func (d *KdbDatasource) currentSyncQueryCache() *syncQueryCache {
	d.queryCacheMu.Lock()
	defer d.queryCacheMu.Unlock()
	return d.queryCache
}

func (d *KdbDatasource) clearSyncQueryCache(scope string) cacheResourceResponse {
	scope = normalizeCacheResourceScope(scope)
	resp := cacheResourceResponse{OK: true, Scope: scope}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil {
			resp.MemoryRemoved = cache.clearCount()
		}
	}
	if scope == "disk" || scope == "both" {
		policy := d.syncQueryCachePolicy(QueryModel{})
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clear()
			resp.DiskRemoved = removed
			if err != nil {
				resp.OK = false
				resp.Error = err.Error()
			}
		}
	}
	resp.Status = d.syncQueryCacheStatus(false)
	return resp
}

func (d *KdbDatasource) clearSyncQueryCacheEntry(scope string, key string) (cacheResourceResponse, int) {
	scope = normalizeCacheResourceScope(scope)
	key = strings.ToLower(strings.TrimSpace(key))
	resp := cacheResourceResponse{OK: true, Scope: scope, Key: key}
	if _, err := syncQueryDiskCacheFileName(key); err != nil {
		resp.OK = false
		resp.Error = err.Error()
		return resp, http.StatusBadRequest
	}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil && cache.clearKey(key) {
			resp.MemoryRemoved = 1
		}
	}
	if scope == "disk" || scope == "both" {
		policy := d.syncQueryCachePolicy(QueryModel{})
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clearKey(key)
			if removed {
				resp.DiskRemoved = 1
			}
			if err != nil {
				resp.OK = false
				resp.Error = err.Error()
			}
		}
	}
	resp.Status = d.syncQueryCacheStatus(false)
	return resp, http.StatusOK
}

func (d *KdbDatasource) clearExpiredSyncQueryCache(scope string) cacheResourceResponse {
	scope = normalizeCacheResourceScope(scope)
	policy := d.syncQueryCachePolicy(QueryModel{})
	resp := cacheResourceResponse{OK: true, Scope: scope}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil {
			resp.MemoryRemoved = cache.clearExpired(policy)
		}
	}
	if scope == "disk" || scope == "both" {
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clearExpired(policy)
			resp.DiskRemoved = removed
			if err != nil {
				resp.OK = false
				resp.Error = err.Error()
			}
		}
	}
	resp.Status = d.syncQueryCacheStatus(false)
	return resp
}

func normalizeCacheResourceScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "memory":
		return "memory"
	case "disk":
		return "disk"
	default:
		return "both"
	}
}
