package plugin

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kdb "github.com/sv/kdbgo"
)

func TestSyncQueryContextErrorPreservesCancellationCause(t *testing.T) {
	cause := errors.New("dashboard request superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)

	err := syncQueryContextError(ctx, "sync query interrupted")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected custom cancellation cause, got %v", err)
	}
}

func TestAcquireSyncConnectionCanceledWaiterPreservesPoolAccounting(t *testing.T) {
	ds := &KdbDatasource{SyncMaxConnections: 1}
	ds.normalizeDatasourceDefaults()
	if err := ds.ensureSyncPool(); err != nil {
		t.Fatalf("ensureSyncPool returned error: %v", err)
	}

	held := &kdb.KDBConn{}
	ds.syncPoolSlots <- struct{}{}
	ds.syncPoolMu.Lock()
	ds.syncPoolActive[held] = struct{}{}
	ds.syncPoolMu.Unlock()

	cause := errors.New("pool waiter superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, _, err := ds.acquireSyncConnection(ctx)
		outcome <- err
	}()

	time.Sleep(25 * time.Millisecond)
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected pool cancellation cause, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled pool waiter did not return promptly")
	}

	snapshot := ds.syncPoolSnapshot()
	if snapshot.active != 1 || snapshot.idle != 0 || snapshot.slots != 1 || snapshot.available != 0 {
		t.Fatalf("canceled waiter changed held-connection accounting: %+v", snapshot)
	}
	ds.discardSyncConnection(held)
	assertEmptySyncPoolSnapshot(t, ds.syncPoolSnapshot())
}

func TestRunKdbQuerySyncCancellationClosesAndDiscardsTransport(t *testing.T) {
	server := startBlockingKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	cause := errors.New("transport no longer needed")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, err := ds.runKdbQuerySync(ctx, kdb.Long(1), 2*time.Second)
		outcome <- err
	}()

	waitForTestSignal(t, server.queryReceived, "kdb server did not receive query")
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected custom cancellation cause, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled transport did not return promptly")
	}
	waitForTestSignal(t, server.clientClosed, "canceled transport did not close the connection")
	assertEmptySyncPoolSnapshot(t, ds.syncPoolSnapshot())
}

func TestRunKdbQueryOnConnectionRejectsResponseWhenCancellationWinsReuseCheck(t *testing.T) {
	server := startRespondingKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())
	conn, _, err := ds.acquireSyncConnection(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire test connection: %v", err)
	}

	ctx := &cancelOnSecondErrContext{}
	result, reusable, err := runKdbQueryOnConnection(ctx, conn, kdb.Long(1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected post-response cancellation, got result=%v reusable=%v err=%v", result, reusable, err)
	}
	if result != nil {
		t.Fatalf("expected canceled response to be discarded, got %v", result)
	}
	if reusable {
		t.Fatal("connection was marked reusable after cancellation became visible")
	}
	ds.discardClosedSyncConnection(conn)
	waitForTestSignal(t, server.clientClosed, "post-response cancellation did not close the connection")
	assertEmptySyncPoolSnapshot(t, ds.syncPoolSnapshot())
}

func TestRunKdbQuerySyncUsesOneTotalDeadlineAcrossPoolWaitAndTransport(t *testing.T) {
	server := startBlockingKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	held, _, err := ds.acquireSyncConnection(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire held connection: %v", err)
	}

	const budget = 500 * time.Millisecond
	const poolWait = 200 * time.Millisecond
	start := time.Now()
	outcome := make(chan error, 1)
	go func() {
		_, err := ds.runKdbQuerySync(context.Background(), kdb.Long(1), budget)
		outcome <- err
	}()

	time.Sleep(poolWait)
	ds.releaseSyncConnection(held)
	waitForTestSignal(t, server.queryReceived, "query did not start after the held connection was released")

	select {
	case err := <-outcome:
		elapsed := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected total deadline error, got %v", err)
		}
		if elapsed < 400*time.Millisecond {
			t.Fatalf("query returned before its total budget elapsed: %v", elapsed)
		}
		if elapsed > 650*time.Millisecond {
			t.Fatalf("query timeout appears to have reset after pool wait: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("query did not honor its total deadline")
	}
	waitForTestSignal(t, server.clientClosed, "deadline did not close the transport")
	assertEmptySyncPoolSnapshot(t, ds.syncPoolSnapshot())
}

func TestCanceledDialClosesLateConnectionBeforeReleasingSlot(t *testing.T) {
	server := startDelayedHandshakeKDBServer(t)
	ds := syncTransportTestDatasource(server.listener.Addr())

	cause := errors.New("dial superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, _, err := ds.acquireSyncConnection(ctx)
		outcome <- err
	}()

	waitForTestSignal(t, server.handshakeReceived, "kdb server did not receive handshake")
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled dial, got %v", err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected dial cancellation cause, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled dial did not return promptly")
	}

	snapshot := ds.syncPoolSnapshot()
	if snapshot.slots != 1 || snapshot.active != 0 || snapshot.idle != 0 {
		t.Fatalf("dial slot was released before the hidden dial completed: %+v", snapshot)
	}
	server.releaseHandshake()
	waitForTestSignal(t, server.clientClosed, "late dial connection was not closed")
	waitForEmptySyncPool(t, ds)
}

type blockingKDBServer struct {
	listener      net.Listener
	queryReceived <-chan struct{}
	clientClosed  <-chan struct{}
}

type cancelOnSecondErrContext struct {
	errCalls atomic.Int32
}

func (c *cancelOnSecondErrContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *cancelOnSecondErrContext) Done() <-chan struct{} {
	return nil
}

func (c *cancelOnSecondErrContext) Err() error {
	if c.errCalls.Add(1) >= 2 {
		return context.Canceled
	}
	return nil
}

func (c *cancelOnSecondErrContext) Value(interface{}) interface{} {
	return nil
}

func startBlockingKDBServer(t *testing.T) blockingKDBServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	queryReceived := make(chan struct{})
	clientClosed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes(0); err != nil {
			return
		}
		if _, err := conn.Write([]byte{3}); err != nil {
			return
		}
		if _, _, err := kdb.Decode(reader); err != nil {
			return
		}
		close(queryReceived)
		_, _ = reader.ReadByte()
		close(clientClosed)
	}()
	return blockingKDBServer{
		listener:      listener,
		queryReceived: queryReceived,
		clientClosed:  clientClosed,
	}
}

func startRespondingKDBServer(t *testing.T) blockingKDBServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	queryReceived := make(chan struct{})
	clientClosed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes(0); err != nil {
			return
		}
		if _, err := conn.Write([]byte{3}); err != nil {
			return
		}
		if _, _, err := kdb.Decode(reader); err != nil {
			return
		}
		close(queryReceived)
		if err := kdb.Encode(conn, kdb.RESPONSE, kdb.Long(1)); err != nil {
			return
		}
		_, _ = reader.ReadByte()
		close(clientClosed)
	}()
	return blockingKDBServer{
		listener:      listener,
		queryReceived: queryReceived,
		clientClosed:  clientClosed,
	}
}

type delayedHandshakeKDBServer struct {
	listener          net.Listener
	handshakeReceived <-chan struct{}
	releaseHandshake  func()
	clientClosed      <-chan struct{}
}

func startDelayedHandshakeKDBServer(t *testing.T) delayedHandshakeKDBServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	handshakeReceived := make(chan struct{})
	releaseHandshake := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHandshake) })
	}
	t.Cleanup(release)
	clientClosed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes(0); err != nil {
			return
		}
		close(handshakeReceived)
		<-releaseHandshake
		if _, err := conn.Write([]byte{3}); err != nil {
			return
		}
		_, _ = reader.ReadByte()
		close(clientClosed)
	}()
	return delayedHandshakeKDBServer{
		listener:          listener,
		handshakeReceived: handshakeReceived,
		releaseHandshake:  release,
		clientClosed:      clientClosed,
	}
}

func syncTransportTestDatasource(address net.Addr) *KdbDatasource {
	host, rawPort, err := net.SplitHostPort(address.String())
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		panic(err)
	}
	ds := &KdbDatasource{
		Host:               host,
		Port:               port,
		DialTimeout:        2 * time.Second,
		SyncMaxConnections: 1,
	}
	ds.setupKdbConnectionHandlers()
	ds.normalizeDatasourceDefaults()
	return ds
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertEmptySyncPoolSnapshot(t *testing.T, snapshot syncPoolSnapshot) {
	t.Helper()
	if snapshot.active != 0 || snapshot.idle != 0 || snapshot.slots != 0 || snapshot.available != snapshot.max {
		t.Fatalf("expected empty sync pool, got %+v", snapshot)
	}
}

func waitForEmptySyncPool(t *testing.T, ds *KdbDatasource) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := ds.syncPoolSnapshot()
		if snapshot.active == 0 && snapshot.idle == 0 && snapshot.slots == 0 && snapshot.available == snapshot.max {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync pool did not reconcile before deadline: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
