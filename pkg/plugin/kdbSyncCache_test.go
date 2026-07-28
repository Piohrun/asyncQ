package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
)

func TestQueryDataCachesSuccessfulSyncResults(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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

func TestQueryDataCacheCanBeEnabledPerQuery(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheEnabled = false
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequestWithModel(t, "A", "1", time.Time{}, time.Time{}, func(model *QueryModel) {
		model.QueryCacheMode = QueryCacheModeEnabled
	})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected per-query cache enablement to call kdb once, got %d", calls)
	}
}

func TestQueryDataCacheCanBeDisabledPerQuery(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequestWithModel(t, "A", "1", time.Time{}, time.Time{}, func(model *QueryModel) {
		model.QueryCacheMode = QueryCacheModeDisabled
	})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected per-query cache disablement to call kdb twice, got %d", calls)
	}
}

func TestQueryDataCacheKeyIncludesTimeRange(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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

func TestQueryDataCacheStrictKeyIncludesRefID(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req1 := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	req2 := cacheTestRequest(t, "B", "1", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req2); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected strict cache key to include refID, got %d kdb calls", calls)
	}
}

func TestQueryDataCacheSharedKeyIgnoresRefID(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheKeyMode = QueryCacheKeyModeShared
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req1 := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	req2 := cacheTestRequest(t, "B", "1", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	second, err := ds.QueryData(context.Background(), req2)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected shared cache key to reuse cached response, got %d kdb calls", calls)
	}
	if got := second.Responses["B"].Frames[0].RefID; got != "B" {
		t.Fatalf("expected cached frame refID to be rewritten for requester, got %q", got)
	}
	if got := second.Responses["B"].Frames[0].Name; got != "B" {
		t.Fatalf("expected cached frame name to be rewritten for requester, got %q", got)
	}
}

func TestQueryDataCacheCanBucketTimeRange(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheTimeBucketSeconds = 60
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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

func TestQueryDataCacheUsesPerQueryTimeBucket(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	bucket := 60
	from := time.Date(2026, 5, 26, 8, 0, 1, 0, time.UTC)
	req1 := cacheTestRequestWithModel(t, "A", "1", from, from.Add(time.Minute), func(model *QueryModel) {
		model.QueryCacheTimeBucketSeconds = &bucket
	})
	req2 := cacheTestRequestWithModel(t, "A", "1", from.Add(20*time.Second), from.Add(time.Minute+20*time.Second), func(model *QueryModel) {
		model.QueryCacheTimeBucketSeconds = &bucket
	})
	if _, err := ds.QueryData(context.Background(), req1); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if _, err := ds.QueryData(context.Background(), req2); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected per-query bucketed time ranges to share a cache entry, got %d kdb calls", calls)
	}
}

func TestQueryDataCacheExpiresEntries(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheTTLSeconds = 1
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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

func TestQueryDataCacheReturnsStaleAndRefreshes(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheTTLSeconds = 1
	ds.QueryCacheStaleTTLSeconds = 60
	var calls int32
	refreshDone := make(chan struct{}, 1)
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		call := atomic.AddInt32(&calls, 1)
		if call == 2 {
			defer func() { refreshDone <- struct{}{} }()
		}
		return kdb.Long(int64(call)), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	first, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if got := first.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("expected first value 1, got %d", got)
	}
	ageCacheEntries(ds, 2*time.Second)

	second, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if got := second.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("expected stale value 1, got %d", got)
	}
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("background cache refresh did not complete")
	}

	third, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("third QueryData returned error: %v", err)
	}
	if got := third.Responses["A"].Frames[0].At(0, 0).(int64); got != 2 {
		t.Fatalf("expected refreshed cached value 2, got %d", got)
	}
}

func TestQueryDataCacheRefreshModeUpdatesCache(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	refreshReq := cacheTestRequestWithModel(t, "A", "1", time.Time{}, time.Time{}, func(model *QueryModel) {
		model.QueryCacheMode = QueryCacheModeRefresh
	})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	refreshed, err := ds.QueryData(context.Background(), refreshReq)
	if err != nil {
		t.Fatalf("refresh QueryData returned error: %v", err)
	}
	if got := refreshed.Responses["A"].Frames[0].At(0, 0).(int64); got != 2 {
		t.Fatalf("expected refresh value 2, got %d", got)
	}
	third, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("third QueryData returned error: %v", err)
	}
	if got := third.Responses["A"].Frames[0].At(0, 0).(int64); got != 2 {
		t.Fatalf("expected cached refreshed value 2, got %d", got)
	}
	if calls != 2 {
		t.Fatalf("expected refresh mode to update cache with two kdb calls, got %d", calls)
	}
}

func TestQueryDataCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheMaxEntries = 1
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
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
	ds.QueryCacheKeyMode = QueryCacheKeyModeShared
	var calls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return kdb.Long(1), nil
	}

	queryA, model := preparedCacheTestQuery(t, ds, "A", "1")
	queryB := queryA
	queryB.RefID = "B"
	const callers = 24
	type outcome struct {
		result    syncQueryResult
		caller    int
		requestID string
		refID     string
		err       error
	}
	var wg sync.WaitGroup
	waitersReady := sync.WaitGroup{}
	waitersReady.Add(callers - 1)
	outcomes := make(chan outcome, callers)
	run := func(index int, ready *sync.WaitGroup) {
		defer wg.Done()
		if ready != nil {
			ready.Done()
		}
		query := queryA
		if index%2 != 0 {
			query = queryB
		}
		requestID := fmt.Sprintf("request-%d", index)
		fields := make([]interface{}, 0, 512)
		fields = append(fields, "caller", index, "requestID", requestID, "refID", query.RefID)
		result, err := ds.runSyncQueryWithCache(context.Background(), backend.PluginContext{}, query, model, fields)
		outcomes <- outcome{result: result, caller: index, requestID: requestID, refID: query.RefID, err: err}
	}

	wg.Add(1)
	go run(0, nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first kdb call did not start")
	}

	for i := 1; i < callers; i++ {
		wg.Add(1)
		go run(i, &waitersReady)
	}
	waitersReady.Wait()
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	close(outcomes)

	results := make([]syncQueryResult, 0, callers)
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("query returned error: %v", outcome.err)
		}
		if len(outcome.result.frames) != 1 {
			t.Fatalf("expected one result frame, got %d", len(outcome.result.frames))
		}
		if got := outcome.result.frames[0].RefID; got != outcome.refID {
			t.Fatalf("expected frame refID %q, got %q", outcome.refID, got)
		}
		if got := outcome.result.frames[0].Name; got != outcome.refID {
			t.Fatalf("expected frame name %q, got %q", outcome.refID, got)
		}
		diagnostics := diagnosticFieldMap(outcome.result.fields)
		if got := diagnostics["caller"]; got != outcome.caller {
			t.Fatalf("expected caller diagnostic %d, got %#v", outcome.caller, got)
		}
		if got := diagnostics["requestID"]; got != outcome.requestID {
			t.Fatalf("expected requestID diagnostic %q, got %#v", outcome.requestID, got)
		}
		if got := diagnostics["refID"]; got != outcome.refID {
			t.Fatalf("expected refID diagnostic %q, got %#v", outcome.refID, got)
		}
		if diagnostics["queryCacheShared"] != true {
			t.Fatalf("expected coalesced query diagnostics, got %#v", diagnostics)
		}
		results = append(results, outcome.result)
	}
	if calls != 1 {
		t.Fatalf("expected concurrent cache miss to coalesce to one kdb call, got %d", calls)
	}
	assertDiagnosticFieldsIndependent(t, results)
}

func TestQueryDataCacheCoalescesConcurrentErrors(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	wantErr := errors.New("test kdb failure")
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil, wantErr
	}

	query, model := preparedCacheTestQuery(t, ds, "A", "1")
	const callers = 24
	type outcome struct {
		result syncQueryResult
		err    error
	}
	var wg sync.WaitGroup
	waitersReady := sync.WaitGroup{}
	waitersReady.Add(callers - 1)
	outcomes := make(chan outcome, callers)
	run := func(index int, ready *sync.WaitGroup) {
		defer wg.Done()
		if ready != nil {
			ready.Done()
		}
		fields := make([]interface{}, 0, 512)
		fields = append(fields, "caller", index)
		result, err := ds.runSyncQueryWithCache(context.Background(), backend.PluginContext{}, query, model, fields)
		outcomes <- outcome{result: result, err: err}
	}

	wg.Add(1)
	go run(0, nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first kdb call did not start")
	}

	for i := 1; i < callers; i++ {
		wg.Add(1)
		go run(i, &waitersReady)
	}
	waitersReady.Wait()
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	close(outcomes)

	results := make([]syncQueryResult, 0, callers)
	for outcome := range outcomes {
		if !errors.Is(outcome.err, wantErr) {
			t.Fatalf("expected %v, got %v", wantErr, outcome.err)
		}
		diagnostics := diagnosticFieldMap(outcome.result.fields)
		if _, ok := diagnostics["profileCacheSingleflightMs"]; !ok {
			t.Fatalf("expected singleflight timing diagnostic, got %#v", diagnostics)
		}
		results = append(results, outcome.result)
	}
	if calls != 1 {
		t.Fatalf("expected concurrent cache error to coalesce to one kdb call, got %d", calls)
	}
	assertDiagnosticFieldsIndependent(t, results)
}

func TestCanceledCoalescedWaiterReturnsWhileSharedExecutionCompletes(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheKeyMode = QueryCacheKeyModeShared
	var calls int32
	entered := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var completeOnce sync.Once
	ds.RunKdbQuerySync = func(ctx context.Context, _ *kdb.K, _ time.Duration, _ ...interface{}) (*kdb.K, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
		}
		<-release
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("shared hook context was canceled: %w", err)
		}
		completeOnce.Do(func() { close(completed) })
		return kdb.Long(1), nil
	}

	query, model := preparedCacheTestQuery(t, ds, "A", "1")
	cancelCause := errors.New("coalesced waiter superseded")
	firstCtx, cancelFirst := context.WithCancelCause(context.Background())
	firstOutcome := make(chan error, 1)
	go func() {
		_, err := ds.runSyncQueryWithCache(firstCtx, backend.PluginContext{}, query, model, []interface{}{"caller", "first"})
		firstOutcome <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("shared cache miss did not start")
	}
	cancelFirst(cancelCause)
	select {
	case err := <-firstOutcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled waiter error, got %v", err)
		}
		if !errors.Is(err, cancelCause) {
			t.Fatalf("expected coalesced waiter cancellation cause, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled coalesced waiter did not return promptly")
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	second, err := ds.runSyncQueryWithCache(context.Background(), backend.PluginContext{}, query, model, []interface{}{"caller", "second"})
	if err != nil {
		t.Fatalf("live waiter returned error: %v", err)
	}
	if len(second.frames) != 1 || second.frames[0].At(0, 0).(int64) != 1 {
		t.Fatalf("unexpected shared result: %#v", second.frames)
	}
	select {
	case <-completed:
	default:
		t.Fatal("shared execution did not complete")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected one detached shared execution, got %d", got)
	}
}

func TestUncachedQueryCancellationReachesHook(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheEnabled = false
	entered := make(chan struct{})
	observed := make(chan error, 1)
	ds.RunKdbQuerySync = func(ctx context.Context, _ *kdb.K, _ time.Duration, _ ...interface{}) (*kdb.K, error) {
		close(entered)
		<-ctx.Done()
		observed <- ctx.Err()
		return nil, fmt.Errorf("hook interrupted: %w", ctx.Err())
	}

	ctx, cancel := context.WithCancel(context.Background())
	request := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	outcome := make(chan *backend.QueryDataResponse, 1)
	go func() {
		response, _ := ds.QueryData(ctx, request)
		outcome <- response
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("uncached query did not reach hook")
	}
	cancel()
	select {
	case response := <-outcome:
		if response == nil {
			t.Fatal("QueryData returned nil response")
		}
		if err := response.Responses["A"].Error; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled query response, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("uncached QueryData did not return after cancellation")
	}
	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("hook observed unexpected context error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hook did not observe caller cancellation")
	}
}

func TestQueryDataDiskCachePersistsAcrossDatasourceInstances(t *testing.T) {
	cacheDir := t.TempDir()
	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})

	ds1 := diskCachedTestDatasource(cacheDir)
	var calls1 int32
	ds1.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls1, 1))), nil
	}
	first, err := ds1.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	if got := first.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("expected first value 1, got %d", got)
	}

	ds2 := diskCachedTestDatasource(cacheDir)
	var calls2 int32
	ds2.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls2, 1))), nil
	}
	second, err := ds2.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if got := second.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("expected disk cached value 1, got %d", got)
	}
	if calls1 != 1 || calls2 != 0 {
		t.Fatalf("expected first instance to call once and second to hit disk cache, got calls1=%d calls2=%d", calls1, calls2)
	}

	diagnostics := asyncQDiagnosticsFromFrame(t, second.Responses["A"].Frames[0])
	if diagnostics["queryCacheStorage"] != "disk" {
		t.Fatalf("expected disk cache diagnostic, got %#v", diagnostics["queryCacheStorage"])
	}
}

func TestQueryDataAttachesDiagnosticsToFrames(t *testing.T) {
	ds := cachedTestDatasource()
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(1), nil
	}

	resp, err := ds.QueryData(context.Background(), cacheTestRequest(t, "A", "1", time.Time{}, time.Time{}))
	if err != nil {
		t.Fatalf("QueryData returned error: %v", err)
	}

	diagnostics := asyncQDiagnosticsFromFrame(t, resp.Responses["A"].Frames[0])
	if diagnostics["requestID"] == "" {
		t.Fatalf("expected requestID diagnostic, got %#v", diagnostics)
	}
	if diagnostics["queryCacheStatus"] != "stored" {
		t.Fatalf("expected stored cache diagnostic, got %#v", diagnostics["queryCacheStatus"])
	}
	for _, key := range []string{"profilePrepareMs", "profileCachePathMs", "profileKdbCallMs", "profileFrameParseMs", "profileFrameRows", "profileFrameFields", "profileFrameCells"} {
		if _, ok := diagnostics[key]; !ok {
			t.Fatalf("expected %s diagnostic, got %#v", key, diagnostics)
		}
	}
	if diagnostics["profileFrameRows"] != 1 {
		t.Fatalf("expected one profiled frame row, got %#v", diagnostics["profileFrameRows"])
	}
}

func TestQueryDataCacheHitAttachesFrameProfileDiagnostics(t *testing.T) {
	ds := cachedTestDatasource()
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		atomic.AddInt32(&calls, 1)
		return kdb.LongV([]int64{1, 2, 3}), nil
	}

	req := cacheTestRequest(t, "A", "1 2 3", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	second, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected second query to hit cache, got %d kdb calls", calls)
	}
	diagnostics := asyncQDiagnosticsFromFrame(t, second.Responses["A"].Frames[0])
	if diagnostics["queryCacheStatus"] != "hit" {
		t.Fatalf("expected hit cache diagnostic, got %#v", diagnostics["queryCacheStatus"])
	}
	if diagnostics["profileFrameRows"] != 3 || diagnostics["profileFrameFields"] != 1 || diagnostics["profileFrameCells"] != 3 {
		t.Fatalf("expected cached frame profile diagnostics, got rows=%#v fields=%#v cells=%#v", diagnostics["profileFrameRows"], diagnostics["profileFrameFields"], diagnostics["profileFrameCells"])
	}
}

func TestCacheResourceClearEntryEvictsMemoryAndDisk(t *testing.T) {
	ds := diskCachedTestDatasource(t.TempDir())
	var calls int32
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Long(int64(atomic.AddInt32(&calls, 1))), nil
	}

	req := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	first, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("first QueryData returned error: %v", err)
	}
	diagnostics := asyncQDiagnosticsFromFrame(t, first.Responses["A"].Frames[0])
	key, ok := diagnostics["queryCacheKey"].(string)
	if !ok || key == "" {
		t.Fatalf("expected queryCacheKey diagnostic, got %#v", diagnostics["queryCacheKey"])
	}

	raw, err := json.Marshal(cacheResourceRequest{Scope: "both", Key: key})
	if err != nil {
		t.Fatalf("failed to marshal resource body: %v", err)
	}
	var resourceResp *backend.CallResourceResponse
	err = ds.CallResource(context.Background(), &backend.CallResourceRequest{
		PluginContext: backend.PluginContext{User: &backend.User{Role: "Editor"}},
		Path:          "cache/clear-entry",
		Body:          raw,
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		resourceResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}
	if resourceResp == nil || resourceResp.Status != 200 {
		t.Fatalf("unexpected resource response: %#v", resourceResp)
	}

	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("second QueryData returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected cache clear-entry to force a second kdb call, got %d", calls)
	}
}

func TestCacheResourceControlsRequireEditorOrAdmin(t *testing.T) {
	ds := cachedTestDatasource()
	raw, err := json.Marshal(cacheResourceRequest{Scope: "both"})
	if err != nil {
		t.Fatalf("failed to marshal resource body: %v", err)
	}

	var resourceResp *backend.CallResourceResponse
	err = ds.CallResource(context.Background(), &backend.CallResourceRequest{
		PluginContext: backend.PluginContext{User: &backend.User{Role: "Viewer"}},
		Path:          "cache/clear",
		Body:          raw,
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		resourceResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}
	if resourceResp == nil || resourceResp.Status != 403 {
		t.Fatalf("expected viewer cache clear to be forbidden, got %#v", resourceResp)
	}

	resourceResp = nil
	err = ds.CallResource(context.Background(), &backend.CallResourceRequest{
		PluginContext: backend.PluginContext{User: &backend.User{Role: "Viewer"}},
		Path:          "cache/status",
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		resourceResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource status returned error: %v", err)
	}
	if resourceResp == nil || resourceResp.Status != 200 {
		t.Fatalf("expected viewer cache status to be allowed, got %#v", resourceResp)
	}
}

func TestCacheResourceControlsCanBeDisabled(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheControlEnabled = false
	ds.queryCacheControlConfigured = true

	raw, err := json.Marshal(cacheResourceRequest{Scope: "both"})
	if err != nil {
		t.Fatalf("failed to marshal resource body: %v", err)
	}
	var resourceResp *backend.CallResourceResponse
	err = ds.CallResource(context.Background(), &backend.CallResourceRequest{
		PluginContext: backend.PluginContext{User: &backend.User{Role: "Admin"}},
		Path:          "cache/clear",
		Body:          raw,
	}, backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
		resourceResp = resp
		return nil
	}))
	if err != nil {
		t.Fatalf("CallResource returned error: %v", err)
	}
	if resourceResp == nil || resourceResp.Status != 403 {
		t.Fatalf("expected disabled cache controls to be forbidden, got %#v", resourceResp)
	}
}

func cachedTestDatasource() *KdbDatasource {
	ds := &KdbDatasource{
		QueryCacheEnabled:        true,
		QueryCacheTTLSeconds:     60,
		QueryCacheMaxEntries:     8,
		QueryCacheControlEnabled: true,
	}
	ds.setupKdbConnectionHandlers()
	ds.normalizeDatasourceDefaults()
	return ds
}

func diskCachedTestDatasource(cacheDir string) *KdbDatasource {
	ds := cachedTestDatasource()
	ds.QueryCacheDiskEnabled = true
	ds.QueryCacheDiskPath = cacheDir
	ds.QueryCacheDiskMaxBytes = 1024 * 1024
	ds.QueryCacheDiskMaxEntries = 64
	return ds
}

func asyncQDiagnosticsFromFrame(t *testing.T, frame *data.Frame) map[string]interface{} {
	t.Helper()
	if frame == nil || frame.Meta == nil {
		t.Fatal("frame diagnostics metadata missing")
	}
	custom, ok := frame.Meta.Custom.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata custom map, got %T", frame.Meta.Custom)
	}
	diagnostics, ok := custom[asyncQDiagnosticsMetaKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected %s map, got %#v", asyncQDiagnosticsMetaKey, custom[asyncQDiagnosticsMetaKey])
	}
	return diagnostics
}

func preparedCacheTestQuery(t *testing.T, ds *KdbDatasource, refID string, queryText string) (backend.DataQuery, QueryModel) {
	t.Helper()
	query := cacheTestRequest(t, refID, queryText, time.Time{}, time.Time{}).Queries[0]
	var model QueryModel
	if err := json.Unmarshal(query.JSON, &model); err != nil {
		t.Fatalf("failed to decode cache test query: %v", err)
	}
	ds.normalizeQueryModel(&model)
	if err := prepareQueryForExecution(backend.PluginContext{}, query, &model); err != nil {
		t.Fatalf("failed to prepare cache test query: %v", err)
	}
	return query, model
}

func diagnosticFieldMap(fields []interface{}) map[string]interface{} {
	diagnostics := make(map[string]interface{}, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if ok {
			diagnostics[key] = fields[i+1]
		}
	}
	return diagnostics
}

func assertDiagnosticFieldsIndependent(t *testing.T, results []syncQueryResult) {
	t.Helper()
	if len(results) < 2 {
		t.Fatalf("expected at least two results, got %d", len(results))
	}
	for i, result := range results {
		if len(result.fields) < 2 || result.fields[0] != "caller" {
			t.Fatalf("result %d does not contain the caller diagnostic: %#v", i, result.fields)
		}
	}

	marker := &struct{}{}
	results[0].fields[1] = marker
	for i := 1; i < len(results); i++ {
		if results[i].fields[1] == marker {
			t.Fatalf("result %d shares diagnostic field storage with result 0", i)
		}
	}
}

func cacheTestRequest(t *testing.T, refID string, queryText string, from time.Time, to time.Time) *backend.QueryDataRequest {
	return cacheTestRequestWithModel(t, refID, queryText, from, to, nil)
}

func cacheTestRequestWithModel(t *testing.T, refID string, queryText string, from time.Time, to time.Time, configure func(*QueryModel)) *backend.QueryDataRequest {
	t.Helper()
	model := QueryModel{
		QueryText:         queryText,
		ExecutionMode:     ExecutionModeSync,
		CompatibilityMode: CompatibilityModePanopticon,
	}
	if configure != nil {
		configure(&model)
	}
	raw, err := json.Marshal(model)
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

func ageCacheEntries(ds *KdbDatasource, age time.Duration) {
	ds.queryCache.mu.Lock()
	defer ds.queryCache.mu.Unlock()
	for _, entry := range ds.queryCache.entries {
		entry.createdAt = time.Now().Add(-age)
	}
}
