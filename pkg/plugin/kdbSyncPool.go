package plugin

import (
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	kdb "github.com/sv/kdbgo"
)

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

func (d *KdbDatasource) acquireSyncConnection(timeout time.Duration) (*kdb.KDBConn, error) {
	if timeout <= 0 {
		timeout = time.Duration(defaultQueryTimeout) * time.Millisecond
	}
	if err := d.ensureSyncPool(); err != nil {
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		pool, slots, err := d.syncPoolChannels()
		if err != nil {
			return nil, err
		}

		select {
		case conn := <-pool:
			if conn == nil {
				continue
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, err
			}
			return conn, nil
		default:
		}

		select {
		case conn := <-pool:
			if conn == nil {
				continue
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, err
			}
			return conn, nil
		case slots <- struct{}{}:
			conn, err := d.newConnection()
			if err != nil {
				d.releaseSyncPoolSlot()
				return nil, err
			}
			if err := d.activateSyncConnection(conn); err != nil {
				return nil, err
			}
			return conn, nil
		case <-timer.C:
			return nil, fmt.Errorf("timed out waiting for sync connection after %v", timeout)
		}
	}
}

func (d *KdbDatasource) releaseSyncConnection(conn *kdb.KDBConn) {
	if conn == nil {
		return
	}

	d.syncPoolMu.Lock()
	delete(d.syncPoolActive, conn)
	if d.syncPoolClosed || d.syncPool == nil {
		d.syncPoolMu.Unlock()
		_ = conn.Close()
		d.releaseSyncPoolSlot()
		return
	}
	select {
	case d.syncPool <- conn:
		d.syncPoolMu.Unlock()
	default:
		d.syncPoolMu.Unlock()
		_ = conn.Close()
		d.releaseSyncPoolSlot()
	}
}

func (d *KdbDatasource) discardSyncConnection(conn *kdb.KDBConn) {
	d.syncPoolMu.Lock()
	delete(d.syncPoolActive, conn)
	d.syncPoolMu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	d.releaseSyncPoolSlot()
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
