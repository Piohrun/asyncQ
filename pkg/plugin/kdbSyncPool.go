package plugin

import (
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	kdb "github.com/sv/kdbgo"
)

type syncPoolAcquireInfo struct {
	reused   bool
	wait     time.Duration
	snapshot syncPoolSnapshot
}

type syncPoolReleaseInfo struct {
	action   string
	snapshot syncPoolSnapshot
}

type syncPoolSnapshot struct {
	max       int
	active    int
	idle      int
	slots     int
	available int
	closed    bool
}

type syncPoolDiagnosticOptions struct {
	acquireWaitMs int64
	acquireSource string
	action        string
	reusable      *bool
	transportMs   int64
}

func (d *KdbDatasource) ensureSyncPool() error {
	d.normalizeDatasourceDefaults()

	d.syncPoolMu.Lock()
	defer d.syncPoolMu.Unlock()

	if d.syncPoolClosed {
		return fmt.Errorf("datasource disposed before sync connection could be acquired")
	}
	if d.syncPool != nil && d.syncPoolSlots != nil {
		return nil
	}

	d.syncPoolMax = d.SyncMaxConnections
	d.syncPool = make(chan *kdb.KDBConn, d.syncPoolMax)
	d.syncPoolSlots = make(chan struct{}, d.syncPoolMax)
	d.syncPoolActive = make(map[*kdb.KDBConn]struct{})
	log.DefaultLogger.Info("Created kdb+ sync connection pool", "host", d.Host, "port", d.Port, "maxConnections", d.syncPoolMax)
	return nil
}

func (d *KdbDatasource) acquireSyncConnection(timeout time.Duration) (*kdb.KDBConn, syncPoolAcquireInfo, error) {
	if timeout <= 0 {
		timeout = time.Duration(defaultQueryTimeout) * time.Millisecond
	}
	start := time.Now()
	if err := d.ensureSyncPool(); err != nil {
		return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		pool, slots, err := d.syncPoolChannels()
		if err != nil {
			return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
		}

		select {
		case conn := <-pool:
			if conn == nil {
				continue
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		default:
		}

		select {
		case conn := <-pool:
			if conn == nil {
				continue
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		case slots <- struct{}{}:
			conn, err := d.newConnection()
			if err != nil {
				d.releaseSyncPoolSlot()
				return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		case <-timer.C:
			return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, fmt.Errorf("timed out waiting for sync connection after %v", timeout)
		}
	}
}

func (d *KdbDatasource) releaseSyncConnection(conn *kdb.KDBConn) syncPoolReleaseInfo {
	if conn == nil {
		return syncPoolReleaseInfo{action: "none", snapshot: d.syncPoolSnapshot()}
	}

	d.syncPoolMu.Lock()
	delete(d.syncPoolActive, conn)
	if d.syncPoolClosed || d.syncPool == nil {
		d.syncPoolMu.Unlock()
		_ = conn.Close()
		d.releaseSyncPoolSlot()
		return syncPoolReleaseInfo{action: "closed", snapshot: d.syncPoolSnapshot()}
	}
	select {
	case d.syncPool <- conn:
		d.syncPoolMu.Unlock()
		return syncPoolReleaseInfo{action: "returned", snapshot: d.syncPoolSnapshot()}
	default:
		d.syncPoolMu.Unlock()
		_ = conn.Close()
		d.releaseSyncPoolSlot()
		return syncPoolReleaseInfo{action: "closed", snapshot: d.syncPoolSnapshot()}
	}
}

func (d *KdbDatasource) discardSyncConnection(conn *kdb.KDBConn) syncPoolReleaseInfo {
	d.syncPoolMu.Lock()
	delete(d.syncPoolActive, conn)
	d.syncPoolMu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	d.releaseSyncPoolSlot()
	return syncPoolReleaseInfo{action: "discarded", snapshot: d.syncPoolSnapshot()}
}

func (d *KdbDatasource) closeSyncPool() {
	d.syncPoolMu.Lock()
	if d.syncPoolClosed {
		d.syncPoolMu.Unlock()
		return
	}
	d.syncPoolClosed = true
	pool := d.syncPool
	activeConnections := make([]*kdb.KDBConn, 0, len(d.syncPoolActive))
	for conn := range d.syncPoolActive {
		activeConnections = append(activeConnections, conn)
	}
	d.syncPoolActive = nil
	d.syncPoolMu.Unlock()

	for _, conn := range activeConnections {
		if conn != nil {
			_ = conn.Close()
		}
	}
	if pool == nil {
		return
	}
	for {
		select {
		case conn := <-pool:
			if conn != nil {
				_ = conn.Close()
			}
			d.releaseSyncPoolSlot()
		default:
			return
		}
	}
}

func (d *KdbDatasource) syncPoolChannels() (chan *kdb.KDBConn, chan struct{}, error) {
	d.syncPoolMu.Lock()
	defer d.syncPoolMu.Unlock()

	if d.syncPoolClosed {
		return nil, nil, fmt.Errorf("datasource disposed before sync connection could be acquired")
	}
	if d.syncPool == nil || d.syncPoolSlots == nil {
		return nil, nil, fmt.Errorf("sync connection pool is not initialized")
	}
	return d.syncPool, d.syncPoolSlots, nil
}

func (d *KdbDatasource) activateSyncConnection(conn *kdb.KDBConn) error {
	d.syncPoolMu.Lock()
	defer d.syncPoolMu.Unlock()

	if d.syncPoolClosed {
		_ = conn.Close()
		d.releaseSyncPoolSlotUnlocked()
		return fmt.Errorf("datasource disposed before sync connection could be acquired")
	}
	if d.syncPoolActive == nil {
		d.syncPoolActive = make(map[*kdb.KDBConn]struct{})
	}
	d.syncPoolActive[conn] = struct{}{}
	return nil
}

func (d *KdbDatasource) releaseSyncPoolSlot() {
	d.syncPoolMu.Lock()
	defer d.syncPoolMu.Unlock()
	d.releaseSyncPoolSlotUnlocked()
}

func (d *KdbDatasource) releaseSyncPoolSlotUnlocked() {
	slots := d.syncPoolSlots
	if slots == nil {
		return
	}
	select {
	case <-slots:
	default:
	}
}

func (d *KdbDatasource) syncPoolSnapshot() syncPoolSnapshot {
	d.syncPoolMu.Lock()
	defer d.syncPoolMu.Unlock()

	max := d.SyncMaxConnections
	if d.syncPoolMax > 0 {
		max = d.syncPoolMax
	}
	idle := 0
	if d.syncPool != nil {
		idle = len(d.syncPool)
	}
	slots := 0
	if d.syncPoolSlots != nil {
		slots = len(d.syncPoolSlots)
	}
	active := 0
	if d.syncPoolActive != nil {
		active = len(d.syncPoolActive)
	}
	return syncPoolSnapshot{
		max:       max,
		active:    active,
		idle:      idle,
		slots:     slots,
		available: max - slots,
		closed:    d.syncPoolClosed,
	}
}

func appendSyncPoolDiagnosticFields(fields []interface{}, snapshot syncPoolSnapshot, options syncPoolDiagnosticOptions) []interface{} {
	fields = append(fields,
		"syncPoolMax", snapshot.max,
		"syncPoolActive", snapshot.active,
		"syncPoolIdle", snapshot.idle,
		"syncPoolSlots", snapshot.slots,
		"syncPoolAvailable", snapshot.available,
		"syncPoolClosed", snapshot.closed,
	)
	if options.acquireSource != "" {
		fields = append(fields,
			"syncPoolAcquireWaitMs", options.acquireWaitMs,
			"syncPoolAcquireSource", options.acquireSource,
		)
	}
	if options.action != "" {
		fields = append(fields, "syncPoolAction", options.action)
	}
	if options.reusable != nil {
		fields = append(fields, "syncPoolReusable", *options.reusable)
	}
	if options.action != "" {
		fields = append(fields, "syncTransportMs", options.transportMs)
	}
	return fields
}
