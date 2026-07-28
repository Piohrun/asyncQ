package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func (d *KdbDatasource) setupKdbConnectionHandlers() {
	d.RunKdbQuerySync = d.runKdbQuerySync
}

func normalizeSyncQueryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func syncQueryContextError(ctx context.Context, message string) error {
	ctx = normalizeSyncQueryContext(ctx)
	err := ctx.Err()
	if err == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil || cause == err {
		return fmt.Errorf("%s: %w", message, err)
	}
	return fmt.Errorf("%s: %w", message, errors.Join(err, cause))
}

func syncQueryTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return time.Duration(defaultQueryTimeout) * time.Millisecond
	}
	return timeout
}

func detachedSyncQueryContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(normalizeSyncQueryContext(ctx))
	return context.WithTimeout(base, syncQueryTimeout(timeout))
}

func (d *KdbDatasource) runKdbQuerySync(ctx context.Context, query *kdb.K, timeout time.Duration, diagnosticFields ...interface{}) (*kdb.K, error) {
	ctx = normalizeSyncQueryContext(ctx)
	timeout = syncQueryTimeout(timeout)
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, acquireInfo, err := d.acquireSyncConnection(queryCtx)
	if err != nil {
		d.logDiagnostics("sync pool acquire failed", appendSyncPoolDiagnosticFields(appendDiagnosticError(diagnosticFields, err), acquireInfo.snapshot, syncPoolDiagnosticOptions{
			acquireWaitMs: acquireInfo.wait.Milliseconds(),
			acquireSource: "failed",
		})...)
		return nil, err
	}
	d.logDiagnostics("sync pool acquired", appendSyncPoolDiagnosticFields(diagnosticFields, acquireInfo.snapshot, syncPoolDiagnosticOptions{
		acquireWaitMs: acquireInfo.wait.Milliseconds(),
		acquireSource: syncPoolAcquireSource(acquireInfo.reused),
	})...)

	start := time.Now()
	result, reusable, err := runKdbQueryOnConnection(queryCtx, conn, query)
	duration := time.Since(start)
	reusablePtr := &reusable
	if reusable {
		releaseInfo := d.releaseSyncConnection(conn)
		d.logDiagnostics("sync pool connection released", appendSyncPoolDiagnosticFields(appendDiagnosticError(diagnosticFields, err), releaseInfo.snapshot, syncPoolDiagnosticOptions{
			action:      releaseInfo.action,
			reusable:    reusablePtr,
			transportMs: duration.Milliseconds(),
		})...)
	} else {
		releaseInfo := d.discardClosedSyncConnection(conn)
		d.logDiagnostics("sync pool connection discarded", appendSyncPoolDiagnosticFields(appendDiagnosticError(diagnosticFields, err), releaseInfo.snapshot, syncPoolDiagnosticOptions{
			action:      releaseInfo.action,
			reusable:    reusablePtr,
			transportMs: duration.Milliseconds(),
		})...)
	}
	return result, err
}

func syncPoolAcquireSource(reused bool) string {
	if reused {
		return "reused"
	}
	return "opened"
}

func runKdbQueryOnConnection(ctx context.Context, conn *kdb.KDBConn, query *kdb.K) (*kdb.K, bool, error) {
	type queryResult struct {
		result *kdb.K
		err    error
	}

	ctx = normalizeSyncQueryContext(ctx)
	if err := syncQueryContextError(ctx, "sync query interrupted before transport"); err != nil {
		_ = conn.Close()
		return nil, false, err
	}

	done := make(chan queryResult, 1)
	go func() {
		if err := conn.WriteMessage(kdb.SYNC, query); err != nil {
			done <- queryResult{err: err}
			return
		}
		result, _, err := conn.ReadMessage()
		done <- queryResult{result: result, err: err}
	}()

	select {
	case msg := <-done:
		if err := syncQueryContextError(ctx, "sync query transport interrupted after response"); err != nil {
			_ = conn.Close()
			return nil, false, err
		}
		if msg.err != nil {
			_ = conn.Close()
			return nil, false, msg.err
		}
		return msg.result, true, nil
	case <-ctx.Done():
		_ = conn.Close()
		return nil, false, syncQueryContextError(ctx, "sync query transport interrupted")
	}
}

func buildDatasourceKdbDict(settings *backend.DataSourceInstanceSettings) *kdb.K {
	datasourceKeys := kdb.SymbolV([]string{"ID", "Name", "UID", "URL", "Updated", "User"})
	var datasourceValues *kdb.K
	if settings == nil {
		datasourceValues = kdb.NewList(
			kdb.Long(-1),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(-kdb.KP, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)),
			kdb.Atom(kdb.KC, ""))
	} else {
		datasourceValues = kdb.NewList(
			kdb.Long(settings.ID),
			kdb.Atom(kdb.KC, settings.Name),
			kdb.Atom(kdb.KC, settings.UID),
			kdb.Atom(kdb.KC, settings.URL),
			kdb.Atom(-kdb.KP, settings.Updated),
			kdb.Atom(kdb.KC, settings.User))
	}
	return kdb.NewDict(datasourceKeys, datasourceValues)
}

func buildUserKdbDict(settings *backend.User) *kdb.K {
	userKeys := kdb.SymbolV([]string{"UserName", "UserEmail", "UserLogin", "UserRole"})
	var userValues *kdb.K
	if settings == nil {
		userValues = kdb.NewList(
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""))
	} else {
		userValues = kdb.NewList(
			kdb.Atom(kdb.KC, settings.Name),
			kdb.Atom(kdb.KC, settings.Email),
			kdb.Atom(kdb.KC, settings.Login),
			kdb.Atom(kdb.KC, settings.Role))
	}
	return kdb.NewDict(userKeys, userValues)
}

func buildQueryKdbDict(q backend.DataQuery, model QueryModel) *kdb.K {
	originalQuery := model.OriginalQueryText
	if originalQuery == "" {
		originalQuery = model.QueryText
	}
	queryKeys := kdb.SymbolV([]string{"RefID", "Query", "QueryType", "MaxDataPoints", "Interval", "TimeRange", "OriginalQuery", "CompiledQuery", "PanopticonQueryWrapper", "PanopticonRequestFunction"})
	queryValues := kdb.NewList(
		kdb.Atom(kdb.KC, q.RefID),
		kdb.Atom(kdb.KC, model.QueryText),
		kdb.Symbol("QUERY"),
		kdb.Long(q.MaxDataPoints),
		kdb.Long(int64(q.Interval)),
		kdb.Atom(kdb.KP, []time.Time{q.TimeRange.From, q.TimeRange.To}),
		kdb.Atom(kdb.KC, originalQuery),
		kdb.Atom(kdb.KC, model.QueryText),
		kdb.Atom(kdb.KC, model.PanopticonQueryWrapper),
		kdb.Atom(kdb.KC, model.PanopticonRequestFunction))
	return kdb.NewDict(queryKeys, queryValues)
}
