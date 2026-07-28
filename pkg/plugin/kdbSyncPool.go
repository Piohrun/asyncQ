package plugin

import (
	"context"
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
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
		return fmt.Errorf("sync connection pool is closed: %w", ErrDatasourceDisposed)
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

func (d *KdbDatasource) acquireSyncConnection(ctx context.Context) (conn *kdb.KDBConn, info syncPoolAcquireInfo, err error) {
	ctx = normalizeSyncQueryContext(ctx)
	if !d.hasActiveLifecycleLease(ctx) {
		return nil, syncPoolAcquireInfo{}, fmt.Errorf("sync connection acquisition requires an admitted datasource operation: %w", ErrDatasourceDisposed)
	}
	if err := syncQueryContextError(ctx, "sync connection acquisition interrupted"); err != nil {
		return nil, syncPoolAcquireInfo{}, err
	}

	start := time.Now()
	if err := d.ensureSyncPool(); err != nil {
		return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
	}

	for {
		if err := syncQueryContextError(ctx, "sync connection acquisition interrupted"); err != nil {
			return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
		}
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
			if err := syncQueryContextError(ctx, "sync connection acquisition interrupted"); err != nil {
				d.discardSyncConnection(conn)
				return nil, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		default:
		}

		select {
		case <-ctx.Done():
			return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, syncQueryContextError(ctx, "sync connection acquisition interrupted")
		case conn := <-pool:
			if conn == nil {
				continue
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			if err := syncQueryContextError(ctx, "sync connection acquisition interrupted"); err != nil {
				d.discardSyncConnection(conn)
				return nil, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{reused: true, wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		case slots <- struct{}{}:
			conn, err := d.dialSyncConnection(ctx)
			if err != nil {
				return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			if err := syncQueryContextError(ctx, "sync connection acquisition interrupted"); err != nil {
				d.discardSyncConnection(conn)
				return nil, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, err
			}
			return conn, syncPoolAcquireInfo{wait: time.Since(start), snapshot: d.syncPoolSnapshot()}, nil
		}
	}
}

func (d *KdbDatasource) dialSyncConnection(ctx context.Context) (*kdb.KDBConn, error) {
	ctx = normalizeSyncQueryContext(ctx)
	if err := syncQueryContextError(ctx, "sync connection establishment interrupted"); err != nil {
		d.releaseSyncPoolSlot()
		return nil, err
	}
	conn, err := d.newConnection(ctx)
	if err != nil {
		d.releaseSyncPoolSlot()
		if contextErr := syncQueryContextError(ctx, "sync connection establishment interrupted"); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	if err := syncQueryContextError(ctx, "sync connection establishment interrupted"); err != nil {
		_ = conn.Close()
		d.releaseSyncPoolSlot()
		return nil, err
	}
	return conn, nil
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
	return d.removeSyncConnection(conn, true)
}

func (d *KdbDatasource) discardClosedSyncConnection(conn *kdb.KDBConn) syncPoolReleaseInfo {
	return d.removeSyncConnection(conn, false)
}

func (d *KdbDatasource) removeSyncConnection(conn *kdb.KDBConn, closeConnection bool) syncPoolReleaseInfo {
	d.syncPoolMu.Lock()
	delete(d.syncPoolActive, conn)
	d.syncPoolMu.Unlock()

	if closeConnection && conn != nil {
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
		return nil, nil, fmt.Errorf("sync connection pool is closed: %w", ErrDatasourceDisposed)
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
		return fmt.Errorf("sync connection pool is closed: %w", ErrDatasourceDisposed)
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
