package plugin

import (
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	kdb "github.com/sv/kdbgo"
)

const kdbEOF = "Failed to read message header:"

// wrappers for correct run-time evaluation of KdbHandle pointer and to enable unit testing
func (d *KdbDatasource) writeMessage(msgtype kdb.ReqType, obj *kdb.K) error {
	return d.KdbHandle.WriteMessage(msgtype, obj)
}

func (d *KdbDatasource) readMessage() (*kdb.K, kdb.ReqType, error) {
	return d.KdbHandle.ReadMessage()
}

// support maximum queue of 100 000 per handle
func (d *KdbDatasource) getKdbSyncQueryId() uint32 {
	if d.kdbSyncQueryCounter > 100000 {
		d.kdbSyncQueryCounter = 0
	}
	d.kdbSyncQueryCounter += 1
	return d.kdbSyncQueryCounter
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
	done := make(chan *kdbRawRead, 1)
	go func() {
		if err := conn.WriteMessage(kdb.SYNC, query); err != nil {
			done <- &kdbRawRead{err: err}
			return
		}
		result, msgType, err := conn.ReadMessage()
		done <- &kdbRawRead{result: result, msgType: msgType, err: err}
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

func (d *KdbDatasource) syncQueryRunner() {
	log.DefaultLogger.Info("Beginning synchronous query listener")
	var err error
	// Open the kdb Handle
	err = d.OpenConnection()
	if err != nil {
		log.DefaultLogger.Error(fmt.Sprintf("Error opening handle to kdb+ process when creating datasource: %v", err))
	}
	for {
		select {
		case signal, ok := <-d.signals:
			if !ok || signal == 3 {
				log.DefaultLogger.Info("Returning from query runner")
				return
			}
		case query, ok := <-d.syncQueue:
			if !ok {
				log.DefaultLogger.Info("Sync query channel closed, returning from query runner")
				return
			}
			if query == nil {
				continue
			}
			// If handle isn't open, attempt to open
			if !d.IsOpen {
				log.DefaultLogger.Info("Handle not open, opening new handle...")
				err = d.OpenConnection()
				// Return error if unable to open handle
				if err != nil {
					log.DefaultLogger.Info(fmt.Sprintf("Unable to open handle on-demand in syncQueryRunner: %v", err))
					d.syncResChan <- &kdbSyncRes{result: nil, err: err, id: query.id}
					continue
				}
			}
			// If handle is open, query the kdb+ process
			err = d.WriteConnection(kdb.SYNC, query.query)
			if err != nil {
				log.DefaultLogger.Error("Error writing message", err.Error())
				d.syncResChan <- &kdbSyncRes{result: nil, err: err, id: query.id}
				continue
			}

			select {
			case msg, ok := <-d.rawReadChan:
				if !ok {
					d.syncResChan <- &kdbSyncRes{result: nil, err: fmt.Errorf("kdb raw read channel closed"), id: query.id}
					continue
				}
				d.syncResChan <- &kdbSyncRes{result: msg.result, err: msg.err, id: query.id}
				if msg.err != nil && strings.Contains(msg.err.Error(), kdbEOF) {
					log.DefaultLogger.Info("Closing rawReadChan within syncQueryRunner")
					d.CloseConnection()
				}
			case <-time.After(query.timeout):
				d.syncResChan <- &kdbSyncRes{result: nil, err: fmt.Errorf("Queried timed out after %v", query.timeout), id: query.id}
				d.CloseConnection()
			}
		}
	}
}

func (d *KdbDatasource) kdbHandleListener() {
	for {
		if !d.IsOpen {
			log.DefaultLogger.Info("Handle not open, kdbHandleListener returning...")
			return
		}
		res, msgType, err := d.ReadConnection()
		if err != nil {
			log.DefaultLogger.Info(err.Error())
			if strings.Contains(err.Error(), kdbEOF) {
				log.DefaultLogger.Info("Handle read error, publishing error and returning from kdbHandleListener")
				if d.IsOpen {
					log.DefaultLogger.Info("d.IsOpen inside kdbHandleListener, publishing read error to kdbRawRead channel")
					d.IsOpen = false
					d.rawReadChan <- &kdbRawRead{result: res, msgType: msgType, err: err}
				}
				return
			}
		}
		d.rawReadChan <- &kdbRawRead{result: res, msgType: msgType, err: err}
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
