package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func TestCheckHealthSuccess(t *testing.T) {
	// Init
	ds := &KdbDatasource{}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) { return kdb.Long(2), nil }
	res, err := ds.CheckHealth(nil, nil)
	if err != nil {
		t.Errorf("Error running CheckHealth: %v", err)
		return
	}
	if res.Status != backend.HealthStatusOk {
		t.Errorf("Successful CheckHealth did not return OK health status")
		return
	}
}

func TestCheckHealthFailValue(t *testing.T) {
	// Init
	ds := &KdbDatasource{}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) { return kdb.Long(3), nil }
	res, err := ds.CheckHealth(nil, nil)
	if err != nil {
		t.Errorf("Error running CheckHealth: %v", err)
		return
	}
	if res.Status != backend.HealthStatusError {
		t.Errorf("Erroring CheckHealth did not return Error health status")
		return
	}
}

func TestCheckHealthFailType(t *testing.T) {
	// Init
	ds := &KdbDatasource{}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return kdb.Error(fmt.Errorf("kdb+ server-side error")), nil
	}
	res, err := ds.CheckHealth(nil, nil)
	if err != nil {
		t.Errorf("Error running CheckHealth: %v", err)
		return
	}
	if res.Status != backend.HealthStatusError {
		t.Errorf("Erroring CheckHealth did not return Error health status")
		return
	}
}

func TestCheckHealthFailError(t *testing.T) {
	// Init
	ds := &KdbDatasource{}
	ds.setupKdbConnectionHandlers()
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		return nil, fmt.Errorf("Go backend-side error")
	}
	res, err := ds.CheckHealth(nil, nil)
	if err != nil {
		t.Errorf("Error running CheckHealth: %v", err)
		return
	}
	if res.Status != backend.HealthStatusError {
		t.Errorf("Erroring CheckHealth did not return Error health status")
		return
	}
}

func TestNormalizeDatasourceDefaultsSetsSyncMaxConnections(t *testing.T) {
	ds := &KdbDatasource{}
	ds.normalizeDatasourceDefaults()
	if ds.SyncMaxConnections != defaultSyncMaxConnections {
		t.Fatalf("expected default sync max connections %d, got %d", defaultSyncMaxConnections, ds.SyncMaxConnections)
	}
	if ds.AsyncMaxJobs != defaultAsyncMaxJobs {
		t.Fatalf("expected default async max jobs %d, got %d", defaultAsyncMaxJobs, ds.AsyncMaxJobs)
	}
}

func TestNormalizeDatasourceDefaultsPreservesConfiguredSyncMaxConnections(t *testing.T) {
	ds := &KdbDatasource{SyncMaxConnections: 1, AsyncMaxJobs: 2}
	ds.normalizeDatasourceDefaults()
	if ds.SyncMaxConnections != 1 {
		t.Fatalf("expected configured sync max connections to be preserved, got %d", ds.SyncMaxConnections)
	}
	if ds.AsyncMaxJobs != 2 {
		t.Fatalf("expected configured async max jobs to be preserved, got %d", ds.AsyncMaxJobs)
	}
}

func TestNormalizeQueryModelInitializesDatasourceDefaultsConcurrently(t *testing.T) {
	const concurrency = 64

	ds := &KdbDatasource{}
	models := make([]QueryModel, concurrency)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := range models {
		go func(index int) {
			defer wg.Done()
			<-start
			ds.normalizeQueryModel(&models[index])
		}(i)
	}

	close(start)
	wg.Wait()

	for i, model := range models {
		if model.ExecutionMode != ExecutionModeSync {
			t.Fatalf("model %d: expected execution mode %q, got %q", i, ExecutionModeSync, model.ExecutionMode)
		}
		if model.CompatibilityMode != CompatibilityModeNative {
			t.Fatalf("model %d: expected compatibility mode %q, got %q", i, CompatibilityModeNative, model.CompatibilityMode)
		}
		if model.LegacyAsyncRequestMode != LegacyAsyncRequestModeRequestDict {
			t.Fatalf("model %d: expected legacy request mode %q, got %q", i, LegacyAsyncRequestModeRequestDict, model.LegacyAsyncRequestMode)
		}
		if model.LegacyAsyncJobIDPath != defaultLegacyAsyncJobIDPath {
			t.Fatalf("model %d: expected legacy job ID path %q, got %q", i, defaultLegacyAsyncJobIDPath, model.LegacyAsyncJobIDPath)
		}
	}
	if ds.SyncMaxConnections != defaultSyncMaxConnections {
		t.Fatalf("expected default sync max connections %d, got %d", defaultSyncMaxConnections, ds.SyncMaxConnections)
	}
	if ds.AsyncMaxJobs != defaultAsyncMaxJobs {
		t.Fatalf("expected default async max jobs %d, got %d", defaultAsyncMaxJobs, ds.AsyncMaxJobs)
	}
	if cap(ds.asyncJobs) != defaultAsyncMaxJobs {
		t.Fatalf("expected async slot capacity %d, got %d", defaultAsyncMaxJobs, cap(ds.asyncJobs))
	}
}

func TestQueryDataRunsSyncQueriesConcurrently(t *testing.T) {
	ds := &KdbDatasource{}
	ds.setupKdbConnectionHandlers()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct {
		response *backend.QueryDataResponse
		err      error
	}, 1)

	var inFlight int32
	var maxInFlight int32
	ds.RunKdbQuerySync = func(*kdb.K, time.Duration, ...interface{}) (*kdb.K, error) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			max := atomic.LoadInt32(&maxInFlight)
			if current <= max || atomic.CompareAndSwapInt32(&maxInFlight, max, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		atomic.AddInt32(&inFlight, -1)
		return kdb.Long(1), nil
	}

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{
			{RefID: "A", JSON: mustQueryJSON(t, QueryModel{QueryText: "1", CompatibilityMode: CompatibilityModePanopticon})},
			{RefID: "B", JSON: mustQueryJSON(t, QueryModel{QueryText: "1", CompatibilityMode: CompatibilityModePanopticon})},
		},
	}

	go func() {
		response, err := ds.QueryData(context.Background(), req)
		done <- struct {
			response *backend.QueryDataResponse
			err      error
		}{response: response, err: err}
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatalf("query %d did not start before timeout; max in-flight was %d", i+1, atomic.LoadInt32(&maxInFlight))
		}
	}
	if got := atomic.LoadInt32(&maxInFlight); got < 2 {
		close(release)
		t.Fatalf("expected at least 2 concurrent queries, got %d", got)
	}
	close(release)

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("QueryData returned error: %v", result.err)
		}
		if len(result.response.Responses) != 2 {
			t.Fatalf("expected 2 query responses, got %d", len(result.response.Responses))
		}
		for refID, dataResponse := range result.response.Responses {
			if dataResponse.Error != nil {
				t.Fatalf("query %s returned error: %v", refID, dataResponse.Error)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("QueryData did not return after releasing queries")
	}
}

func mustQueryJSON(t *testing.T, model QueryModel) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("failed to marshal query model: %v", err)
	}
	return raw
}

/* func TestRunKdbQuerySync(t *testing.T) {
ds := KdbDatasource{}
log.Print(ds)

}

func TestQueryData(t *testing.T) {
	ds := KdbDatasource{}

	resp, err := ds.QueryData(
		context.Background(),
		&backend.QueryDataRequest{
			Queries: []backend.DataQuery{
				{RefID: "A"},
			},
		},
	)
	if err != nil {
		t.Error(err)
	}

	if len(resp.Responses) != 1 {
		t.Fatal("QueryData must return a response")
	}
} */
