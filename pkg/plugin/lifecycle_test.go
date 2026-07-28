package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
)

func TestDisposeRacesAsyncSlotAcquisition(t *testing.T) {
	ds := &KdbDatasource{AsyncMaxJobs: 32}
	ds.normalizeDatasourceDefaults()

	operationCtx, finishOperation, err := ds.beginOperation(context.Background())
	if err != nil {
		t.Fatalf("failed to admit pre-canceled async acquisition: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(operationCtx)
	cancel()
	if err := ds.acquireAsyncSlot(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled acquisition error = %v, want context.Canceled", err)
	}
	finishOperation()
	if got := len(ds.asyncJobs); got != 0 {
		t.Fatalf("pre-canceled acquisition retained %d tokens", got)
	}

	const attempts = 5000
	const disposers = 32
	start := make(chan struct{})
	var attemptsWG sync.WaitGroup
	var disposeWG sync.WaitGroup
	var unexpected atomic.Int64
	attemptsWG.Add(attempts)
	for range attempts {
		operationCtx, finishOperation, err := ds.beginOperation(context.Background())
		if err != nil {
			t.Fatalf("failed to admit racing async acquisition: %v", err)
		}
		go func(ctx context.Context, finish func()) {
			defer attemptsWG.Done()
			defer finish()
			<-start
			err := ds.acquireAsyncSlot(ctx)
			switch {
			case err == nil:
				ds.releaseAsyncSlot()
			case errors.Is(err, ErrDatasourceDisposed):
			case strings.Contains(err.Error(), "async job limit reached"):
			default:
				unexpected.Add(1)
			}
		}(operationCtx, finishOperation)
	}
	disposeWG.Add(disposers)
	for range disposers {
		go func() {
			defer disposeWG.Done()
			<-start
			ds.Dispose()
		}()
	}
	close(start)
	attemptsWG.Wait()
	disposeWG.Wait()

	if got := unexpected.Load(); got != 0 {
		t.Fatalf("async acquisition produced %d unexpected errors", got)
	}
	if got := len(ds.asyncJobs); got != 0 {
		t.Fatalf("async semaphore retained %d tokens after all releases", got)
	}
	if err := ds.acquireAsyncSlot(context.Background()); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("post-dispose acquisition error = %v, want ErrDatasourceDisposed", err)
	}
}

func TestAcquireAsyncSlotRequiresLifecycleLeaseWithoutMutatingState(t *testing.T) {
	ds := &KdbDatasource{AsyncMaxJobs: 2}

	if err := ds.acquireAsyncSlot(context.Background()); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("unleased async slot error = %v, want lifecycle error wrapping ErrDatasourceDisposed", err)
	}
	if ds.asyncJobs != nil {
		t.Fatal("unleased async acquisition initialized semaphore state")
	}

	ds.asyncJobs = make(chan struct{}, 2)
	ds.asyncJobs <- struct{}{}
	if err := ds.acquireAsyncSlot(context.Background()); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("unleased async slot error with existing state = %v, want ErrDatasourceDisposed", err)
	}
	if got := len(ds.asyncJobs); got != 1 {
		t.Fatalf("unleased async acquisition changed semaphore token count to %d, want 1", got)
	}
}

func TestPublicEntrypointsRejectAfterDisposeWithoutRecreatingState(t *testing.T) {
	var queryCalls atomic.Int64
	ds := &KdbDatasource{
		QueryCacheEnabled:    true,
		QueryCacheMaxEntries: 2,
		excelDownloads: map[string]excelReportDownload{
			"owned": {Body: []byte("workbook"), FileName: "owned.xlsx", ExpiresAt: time.Now().Add(time.Hour)},
		},
	}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(context.Context, *kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		queryCalls.Add(1)
		return kdb.Long(2), nil
	}
	ds.queryCache = &syncQueryCache{
		entries: map[string]*syncQueryCacheEntry{
			"owned": {frames: []*data.Frame{data.NewFrame("owned")}, createdAt: time.Now(), lastAccess: time.Now()},
		},
		refreshing: map[string]struct{}{},
		maxEntries: 2,
	}
	ds.queryDiskCache = &syncQueryDiskCache{dir: t.TempDir(), maxBytes: 1024, maxEntries: 2}
	ds.Dispose()

	if _, err := ds.QueryData(context.Background(), nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("QueryData error = %v, want ErrDatasourceDisposed", err)
	}
	if _, err := ds.CheckHealth(context.Background(), nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("CheckHealth error = %v, want ErrDatasourceDisposed", err)
	}
	if err := ds.CallResource(context.Background(), nil, nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("CallResource error = %v, want ErrDatasourceDisposed", err)
	}
	if _, err := ds.SubscribeStream(context.Background(), nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("SubscribeStream error = %v, want ErrDatasourceDisposed", err)
	}
	if _, err := ds.PublishStream(context.Background(), nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("PublishStream error = %v, want ErrDatasourceDisposed", err)
	}
	if err := ds.RunStream(context.Background(), nil, nil); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("RunStream error = %v, want ErrDatasourceDisposed", err)
	}
	if err := ds.acquireAsyncSlot(context.Background()); !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("async slot error = %v, want ErrDatasourceDisposed", err)
	}

	if got := queryCalls.Load(); got != 0 {
		t.Fatalf("post-dispose entrypoints reached the query hook %d times", got)
	}
	if ds.asyncJobs != nil {
		t.Fatal("post-dispose async acquisition initialized the semaphore")
	}
	if ds.queryCache != nil {
		t.Fatal("Dispose did not release the memory cache")
	}
	if ds.queryDiskCache != nil {
		t.Fatal("Dispose did not release the disk cache handle")
	}
	if ds.excelDownloads != nil {
		t.Fatal("Dispose did not clear owned Excel downloads")
	}
	policy := syncQueryCachePolicy{
		enabled: true,
		disk: syncQueryDiskCachePolicy{
			enabled:    true,
			dir:        t.TempDir(),
			maxBytes:   1024,
			maxEntries: 2,
		},
	}
	if cache := ds.syncQueryCache(policy); cache != nil {
		t.Fatal("memory cache was recreated after disposal")
	}
	if cache := ds.syncQueryDiskCache(policy); cache != nil {
		t.Fatal("disk cache handle was recreated after disposal")
	}
}

func TestDisposeInterruptsAndWaitsForActivePooledCallAndPoolWaiter(t *testing.T) {
	server := startBlockingKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	activeDone := make(chan error, 1)
	go func() {
		_, err := ds.runKdbQuerySync(context.Background(), kdb.Long(1), time.Hour)
		activeDone <- err
	}()
	waitForTestSignal(t, server.queryReceived, "active pooled call did not reach the q server")

	waiterStarted := make(chan struct{})
	waiterDone := make(chan error, 1)
	go func() {
		close(waiterStarted)
		_, err := ds.runKdbQuerySync(context.Background(), kdb.Long(2), time.Hour)
		waiterDone <- err
	}()
	waitForTestSignal(t, waiterStarted, "pool waiter did not start")
	// The active connection owns the only slot. Give the waiter an opportunity
	// to enter the pool select before terminal cancellation.
	time.Sleep(20 * time.Millisecond)

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()

	select {
	case err := <-activeDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("active pooled call error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active pooled call was not interrupted by disposal")
	}
	select {
	case err := <-waiterDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("pool waiter error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool waiter was not interrupted by disposal")
	}
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not wait for the active pooled call and waiter to finish")
	}
	waitForTestSignal(t, server.clientClosed, "Dispose did not close the active pooled transport")
}

func TestDisposeInterruptsAndWaitsForDial(t *testing.T) {
	server := startDelayedHandshakeKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	dialDone := make(chan error, 1)
	go func() {
		operationCtx, finish, err := ds.beginOperation(context.Background())
		if err != nil {
			dialDone <- err
			return
		}
		defer func() {
			err = disposedOperationError(operationCtx, err)
			finish()
			dialDone <- err
		}()
		_, _, err = ds.acquireSyncConnection(operationCtx)
	}()
	waitForTestSignal(t, server.handshakeReceived, "dial did not reach the q authentication handshake")

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()

	select {
	case err := <-dialDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("dial error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dial was not interrupted by disposal")
	}
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose returned before the interrupted dial finished")
	}
	server.releaseHandshake()
	waitForTestSignal(t, server.clientClosed, "interrupted dial connection was not closed")
}

func TestAcquireSyncConnectionRequiresLifecycleLease(t *testing.T) {
	server := startDelayedHandshakeKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	conn, _, err := ds.acquireSyncConnection(context.Background())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("unleased pool acquisition returned a connection")
	}
	if !errors.Is(err, ErrDatasourceDisposed) {
		t.Fatalf("unleased pool acquisition error = %v, want lifecycle error wrapping ErrDatasourceDisposed", err)
	}
	select {
	case <-server.handshakeReceived:
		t.Fatal("unleased pool acquisition reached the q server")
	default:
	}
	ds.syncPoolMu.Lock()
	poolCreated := ds.syncPool != nil || ds.syncPoolSlots != nil || ds.syncPoolActive != nil
	ds.syncPoolMu.Unlock()
	if poolCreated {
		t.Fatal("unleased pool acquisition initialized pool state")
	}
	ds.Dispose()
}

func TestNewConnectionRequiresLifecycleLeaseAndTracksAdmittedDirectCall(t *testing.T) {
	t.Run("unleased dial is rejected before reaching server", func(t *testing.T) {
		server := startDelayedHandshakeKDBServer(t)
		ds := syncTransportTestDatasource(server.listener.Addr())

		conn, err := ds.newConnection(context.Background())
		if conn != nil {
			_ = conn.Close()
			t.Fatal("unleased dial returned a connection")
		}
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("unleased dial error = %v, want lifecycle error wrapping ErrDatasourceDisposed", err)
		}
		select {
		case <-server.handshakeReceived:
			t.Fatal("unleased dial reached the q server")
		default:
		}
		ds.Dispose()
	})

	t.Run("admitted direct caller remains tracked", func(t *testing.T) {
		server := startBlockingKDBServer(t)
		ds := syncTransportTestDatasource(server.listener.Addr())

		ipcReturned := make(chan struct{})
		releaseCaller := make(chan struct{})
		callDone := make(chan error, 1)
		go func() {
			operationCtx, finish, err := ds.beginOperation(context.Background())
			if err != nil {
				callDone <- err
				return
			}
			defer func() {
				err = disposedOperationError(operationCtx, err)
				finish()
				callDone <- err
			}()

			conn, err := ds.newConnection(operationCtx)
			if err != nil {
				return
			}
			defer conn.Close()
			_, _, err = conn.CallMessageContext(operationCtx, kdb.Long(1))
			close(ipcReturned)
			<-releaseCaller
		}()
		waitForTestSignal(t, server.queryReceived, "admitted direct call did not reach the q server")

		disposeDone := make(chan struct{})
		go func() {
			ds.Dispose()
			close(disposeDone)
		}()
		waitForTestSignal(t, server.clientClosed, "disposal did not close the admitted direct transport")
		waitForTestSignal(t, ipcReturned, "admitted direct IPC did not return after disposal")
		select {
		case <-disposeDone:
			t.Fatal("Dispose returned while the admitted direct caller still held its lifecycle lease")
		default:
		}
		close(releaseCaller)

		select {
		case err := <-callDone:
			if !errors.Is(err, ErrDatasourceDisposed) {
				t.Fatalf("admitted direct call error = %v, want ErrDatasourceDisposed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("admitted direct caller did not finish")
		}
		select {
		case <-disposeDone:
		case <-time.After(time.Second):
			t.Fatal("Dispose did not wait for the admitted direct caller")
		}
	})
}

func TestDisposeInterruptsAndJoinsAdmittedDirectAsyncWorker(t *testing.T) {
	server := startBlockingKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	senderEntered := make(chan struct{})
	releaseSender := make(chan struct{})
	var senderOnce sync.Once
	sender := backend.CallResourceResponseSenderFunc(func(*backend.CallResourceResponse) error {
		senderOnce.Do(func() { close(senderEntered) })
		<-releaseSender
		return nil
	})
	request := &backend.CallResourceRequest{
		Path: "async/run-and-wait",
		Body: []byte(`{
			"queryText": "1",
			"executionMode": "pluginAsync",
			"compatibilityMode": "panopticon",
			"timeOut": 3600000,
			"requestId": "direct-dispose-test"
		}`),
	}
	resourceDone := make(chan error, 1)
	go func() {
		resourceDone <- ds.CallResource(context.Background(), request, sender)
	}()
	waitForTestSignal(t, server.queryReceived, "direct async worker did not reach the q server")

	ds.syncPoolMu.Lock()
	poolCreated := ds.syncPool != nil
	ds.syncPoolMu.Unlock()
	if poolCreated {
		t.Fatal("representative direct async path unexpectedly used the sync pool")
	}

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()

	waitForTestSignal(t, server.clientClosed, "disposal did not close the direct async transport")
	waitForTestSignal(t, senderEntered, "direct async handler did not finish its joined worker")
	select {
	case <-disposeDone:
		t.Fatal("Dispose returned while the admitted direct async handler was still sending")
	default:
	}
	close(releaseSender)

	select {
	case err := <-resourceDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("direct async CallResource error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("direct async CallResource did not return after sender release")
	}
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not join the direct async handler")
	}
}

func TestWithActiveLifecycleAdmitsUnleasedActionAcrossDispose(t *testing.T) {
	ds := &KdbDatasource{}
	actionEntered := make(chan struct{})
	actionCanceled := make(chan struct{})
	releaseAction := make(chan struct{})
	actionDone := make(chan error, 1)
	go func() {
		actionDone <- ds.withActiveLifecycle(context.Background(), func() error {
			ds.lifecycleMu.Lock()
			lifecycleCtx := ds.lifecycleCtx
			ds.lifecycleMu.Unlock()
			close(actionEntered)
			<-lifecycleCtx.Done()
			close(actionCanceled)
			<-releaseAction
			return nil
		})
	}()
	waitForTestSignal(t, actionEntered, "unleased lifecycle action was not admitted")

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()
	waitForTestSignal(t, actionCanceled, "unleased lifecycle action did not observe disposal")
	select {
	case <-disposeDone:
		t.Fatal("Dispose returned while the self-admitted lifecycle action was still running")
	default:
	}
	close(releaseAction)

	select {
	case err := <-actionDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("lifecycle action error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("self-admitted lifecycle action did not finish")
	}
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not wait for the self-admitted lifecycle action")
	}
}

func TestWithActiveLifecycleIndependentlyTracksBorrowedLease(t *testing.T) {
	ds := &KdbDatasource{}
	ownerCtx, finishOwner, err := ds.beginOperation(context.Background())
	if err != nil {
		t.Fatalf("failed to admit owner operation: %v", err)
	}

	actionEntered := make(chan struct{})
	actionCanceled := make(chan struct{})
	releaseAction := make(chan struct{})
	actionDone := make(chan error, 1)
	go func() {
		actionDone <- ds.withActiveLifecycle(ownerCtx, func() error {
			ds.lifecycleMu.Lock()
			lifecycleCtx := ds.lifecycleCtx
			ds.lifecycleMu.Unlock()
			close(actionEntered)
			<-lifecycleCtx.Done()
			close(actionCanceled)
			<-releaseAction
			return nil
		})
	}()
	waitForTestSignal(t, actionEntered, "borrowed-lease lifecycle action did not start")

	finishOwner()
	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()
	waitForTestSignal(t, actionCanceled, "borrowed-lease lifecycle action did not observe disposal")
	select {
	case <-disposeDone:
		t.Fatal("Dispose returned after the owner finished but while its independently admitted action was running")
	default:
	}
	close(releaseAction)

	select {
	case err := <-actionDone:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("borrowed-lease lifecycle action returned unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("borrowed-lease lifecycle action did not finish")
	}
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not wait for the independently admitted lifecycle action")
	}
}

func TestDisposeWaitsForDetachedCacheMissAndPreventsCommit(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheDiskEnabled = false
	entered := make(chan struct{})
	canceled := make(chan error, 1)
	release := make(chan struct{})
	ds.RunKdbQuerySync = func(ctx context.Context, _ *kdb.K, _ time.Duration, _ ...interface{}) (*kdb.K, error) {
		close(entered)
		<-ctx.Done()
		canceled <- context.Cause(ctx)
		<-release
		// Deliberately return success after cancellation. The lifecycle commit
		// preflight must still reject this result.
		return kdb.Long(1), nil
	}

	queryDone := make(chan error, 1)
	go func() {
		_, err := ds.QueryData(context.Background(), cacheTestRequest(t, "A", "1", time.Time{}, time.Time{}))
		queryDone <- err
	}()
	waitForTestSignal(t, entered, "detached cache miss did not reach the query hook")

	ds.queryCacheMu.Lock()
	cache := ds.queryCache
	ds.queryCacheMu.Unlock()
	if cache == nil {
		t.Fatal("cache was not initialized before the miss")
	}

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()
	select {
	case cause := <-canceled:
		if !errors.Is(cause, ErrDatasourceDisposed) {
			t.Fatalf("detached miss cancellation cause = %v, want ErrDatasourceDisposed", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("detached cache miss did not observe disposal")
	}
	select {
	case <-disposeDone:
		t.Fatal("Dispose returned while the detached cache miss was still running")
	default:
	}
	close(release)
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not finish after the detached cache miss exited")
	}
	select {
	case err := <-queryDone:
		if !errors.Is(err, ErrDatasourceDisposed) {
			t.Fatalf("QueryData error = %v, want ErrDatasourceDisposed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("QueryData did not return after disposal")
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 0 {
		t.Fatalf("detached miss committed %d cache entries across disposal", entries)
	}
}

func TestDisposeWaitsForStaleRefreshAndPreventsCommit(t *testing.T) {
	ds := cachedTestDatasource()
	ds.QueryCacheDiskEnabled = false
	ds.QueryCacheTTLSeconds = 1
	ds.QueryCacheStaleTTLSeconds = 60

	var calls atomic.Int64
	refreshEntered := make(chan struct{})
	refreshCanceled := make(chan error, 1)
	releaseRefresh := make(chan struct{})
	ds.RunKdbQuerySync = func(ctx context.Context, _ *kdb.K, _ time.Duration, _ ...interface{}) (*kdb.K, error) {
		if calls.Add(1) == 1 {
			return kdb.Long(1), nil
		}
		close(refreshEntered)
		<-ctx.Done()
		refreshCanceled <- context.Cause(ctx)
		<-releaseRefresh
		return kdb.Long(2), nil
	}

	request := cacheTestRequest(t, "A", "1", time.Time{}, time.Time{})
	if _, err := ds.QueryData(context.Background(), request); err != nil {
		t.Fatalf("initial QueryData returned error: %v", err)
	}
	ds.queryCacheMu.Lock()
	cache := ds.queryCache
	ds.queryCacheMu.Unlock()
	if cache == nil {
		t.Fatal("cache was not initialized")
	}
	cache.mu.Lock()
	for _, entry := range cache.entries {
		entry.createdAt = time.Now().Add(-2 * time.Second)
	}
	cache.mu.Unlock()

	stale, err := ds.QueryData(context.Background(), request)
	if err != nil {
		t.Fatalf("stale QueryData returned error: %v", err)
	}
	if got := stale.Responses["A"].Frames[0].At(0, 0).(int64); got != 1 {
		t.Fatalf("stale QueryData value = %d, want 1", got)
	}
	waitForTestSignal(t, refreshEntered, "stale refresh did not start")

	disposeDone := make(chan struct{})
	go func() {
		ds.Dispose()
		close(disposeDone)
	}()
	select {
	case cause := <-refreshCanceled:
		if !errors.Is(cause, ErrDatasourceDisposed) {
			t.Fatalf("stale refresh cancellation cause = %v, want ErrDatasourceDisposed", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("stale refresh did not observe disposal")
	}
	select {
	case <-disposeDone:
		t.Fatal("Dispose returned while stale refresh was still running")
	default:
	}
	close(releaseRefresh)
	select {
	case <-disposeDone:
	case <-time.After(time.Second):
		t.Fatal("Dispose did not wait for stale refresh completion")
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 0 {
		t.Fatalf("stale refresh left %d entries after disposal", entries)
	}
}

func TestQueryDataFanoutIsBoundedAndCancellationFillsResponses(t *testing.T) {
	const queryCount = 5000
	const workerLimit = 7

	ds := &KdbDatasource{
		SyncMaxConnections:   workerLimit,
		QueryCacheEnabled:    false,
		queryCacheConfigured: true,
	}
	ds.setupKdbConnectionHandlers()
	queryJSON := mustQueryJSON(t, QueryModel{QueryText: "1", CompatibilityMode: CompatibilityModePanopticon})
	queries := make([]backend.DataQuery, queryCount)
	for index := range queries {
		queries[index] = backend.DataQuery{RefID: fmt.Sprintf("Q%d", index), JSON: queryJSON}
	}

	entered := make(chan struct{}, workerLimit)
	var active atomic.Int64
	var maxActive atomic.Int64
	ds.RunKdbQuerySync = func(ctx context.Context, _ *kdb.K, _ time.Duration, _ ...interface{}) (*kdb.K, error) {
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		active.Add(-1)
		return nil, syncQueryContextError(ctx, "fanout hook interrupted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	queryDone := make(chan struct {
		response *backend.QueryDataResponse
		err      error
	}, 1)
	go func() {
		response, err := ds.QueryData(ctx, &backend.QueryDataRequest{Queries: queries})
		queryDone <- struct {
			response *backend.QueryDataResponse
			err      error
		}{response: response, err: err}
	}()
	for range workerLimit {
		waitForTestSignal(t, entered, "bounded query worker did not start")
	}
	cancel()

	select {
	case outcome := <-queryDone:
		if outcome.err != nil {
			t.Fatalf("QueryData returned top-level error for caller cancellation: %v", outcome.err)
		}
		if len(outcome.response.Responses) != queryCount {
			t.Fatalf("QueryData returned %d responses, want %d", len(outcome.response.Responses), queryCount)
		}
		for _, query := range queries {
			if err := outcome.response.Responses[query.RefID].Error; !errors.Is(err, context.Canceled) {
				t.Fatalf("response %s error = %v, want context.Canceled", query.RefID, err)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("QueryData did not return promptly after cancellation")
	}
	if got := maxActive.Load(); got != workerLimit {
		t.Fatalf("maximum hook concurrency = %d, want %d", got, workerLimit)
	}
}
