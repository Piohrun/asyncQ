package plugin

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func TestQueryDataCachesSuccessfulSyncResults(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	first, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	second, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("expected one kdb call after cache hit, got %d", calls)
	}
	firstValue := first.Responses["A"].Frames[0].At(0, 0).(int64)
	secondValue := second.Responses["A"].Frames[0].At(0, 0).(int64)
	if firstValue != 1 || secondValue != 1 {
		t.Fatalf("unexpected cached values: first=%d second=%d", firstValue, secondValue)
	}
}

func TestQueryDataCacheCanBeBypassedFromQueryText(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequest(t, "A", "/ asyncq:cache=off\n1", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected cache bypass to call kdb twice, got %d", calls)
	}
}

func TestQueryDataCacheKeyIncludesTimeRange(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	from := time.Date(2026, 5, 26, 8, 0, 0, 0, time.UTC)
	req1 := cacheTestRequest(t, "A", "1", from, from.Add(time.Minute))
	req2 := cacheTestRequest(t, "A", "1", from.Add(time.Minute), from.Add(2*time.Minute))
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req2); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected distinct time ranges to call kdb twice, got %d", calls)
	}
}

func TestQueryDataCacheCanBucketTimeRange(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheTimeBucketSeconds = 60
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	from := time.Date(2026, 5, 26, 8, 0, 1, 0, time.UTC)
	req1 := cacheTestRequest(t, "A", "1", from, from.Add(time.Minute))
	req2 := cacheTestRequest(t, "A", "1", from.Add(20*time.Second), from.Add(time.Minute+20*time.Second))
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req2); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected bucketed time ranges to share a cache entry, got %d kdb calls", calls)
	}
}

func TestQueryDataCacheExpiresEntries(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheTTLSeconds = 1
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	ds.queryCache.mu.Lock()
	for _, entry := range ds.queryCache.entries {
		entry.createdAt = time.Now().Add(-2 * time.Second)
	}
	ds.queryCache.mu.Unlock()
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected expired entry to call kdb again, got %d calls", calls)
	}
}

func TestQueryDataCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheMaxEntries = 1
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req1 := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	req2 := cacheTestRequest(t, "A", "2", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req2); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("third QueryData returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected first query to be evicted, got %d kdb calls", calls)
	}
}

func TestQueryDataCacheReturnsClonedFrames(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		atomic.AddInt32(&calls, 1)
		return kdb.Long(1), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	first, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	first.Responses["A"].Frames[0].Set(0, 0, int64(99))

	second, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected second response from cache, got %d kdb calls", calls)
	}
	if got := second.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("cached frame was mutated through previous response, got %d", got)
	}
}

func TestQueryDataCacheCoalescesConcurrentMisses(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		atomic.AddInt32(&calls, 1)
		entered <- struct{}{}
		<-release
		return kdb.Long(1), nil
	}

	query := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{}).Queries[0]
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := ds.query(context.Background(), backend.PluginContext{}, query, "request")
			errs <- res.Error
		}()
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first kdb call did not start")
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("query returned error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected concurrent cache miss to coalesce to one kdb call, got %d", calls)
	}
}

func cachedTestDatasource() *KdbDatasource {
	ds := &KdbDatasource{
		QueryCacheEnabled:    true,
		QueryCacheTTLSeconds: 60,
		QueryCacheMaxEntries: 8,
	}
	ds.setupKdbConnectionHandlers()
	ds.normalizeDatasourceDefaults()
	return ds
}

func cacheTestRequest(t *testing.T, refID string, queryText string, from time.Time, to time.Time) *backend.QueryDataRequest {
	t.Helper()
	raw, err := json.Marshal(QueryModel{
		QueryText:         queryText,
		ExecutionMode:     ExecutionModeSync,
		CompatibilityMode: CompatibilityModePanopticon,
	})
	if err != nil {
		t.Fatalf("failed to marshal query model: %v", err)
	}
	return &backend.QueryDataRequest{
		Queries: []backend.DataQuery{
			{
				RefID:         refID,
				MaxDataPoints: 100,
				Interval:      time.Second,
				TimeRange:     backend.TimeRange{From: from, To: to},
				JSON:          raw,
			},
		},
	}
}
