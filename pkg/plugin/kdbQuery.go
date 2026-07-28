package plugin

import (
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

func (d *KdbDatasource) setupKdbConnectionHandlers() {
	d.RunKdbQuerySync = d.runKdbQuerySync
}

func (d *KdbDatasource) runKdbQuerySync(query *kdb.K, timeout time.Duration, diagnosticFields ...interface{}) (*kdb.K, error) {
	if timeout <= 0 {
		timeout = time.Duration(defaultQueryTimeout) * time.Millisecond
	}

	conn, acquireInfo, err := d.acquireSyncConnection(timeout)
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
	result, reusable, err := runKdbQueryOnConnection(conn, query, timeout)
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
		releaseInfo := d.discardSyncConnection(conn)
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

func runKdbQueryOnConnection(conn *kdb.KDBConn, query *kdb.K, timeout time.Duration) (*kdb.K, bool, error) {
	type queryResult struct {
		result *kdb.K
		err    error
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
		if msg.err != nil {
			return nil, false, msg.err
		}
		return msg.result, true, nil
	case <-time.After(timeout):
		_ = conn.Close()
		return nil, false, fmt.Errorf("query timed out after %v", timeout)
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
