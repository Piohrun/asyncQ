package kdb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listenerPort(t *testing.T, listener net.Listener) int {
	t.Helper()
	_, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func testCertificate(t *testing.T, dnsNames []string, ipAddresses []net.IP) (tls.Certificate, *x509.CertPool, []byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "asyncq local test"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots, certPEM, keyPEM
}

func startTLSAuthServer(t *testing.T, config *tls.Config) (net.Listener, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	outcome := make(chan error, 1)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			outcome <- err
			return
		}
		defer raw.Close()
		conn := tls.Server(raw, config)
		if err := conn.Handshake(); err != nil {
			outcome <- err
			return
		}
		if _, err := bufio.NewReader(conn).ReadString(0); err != nil {
			outcome <- err
			return
		}
		if _, err := conn.Write([]byte{3}); err != nil {
			outcome <- err
			return
		}
		outcome <- nil
	}()
	return listener, outcome
}

func TestHandleClientConnectionUsesBoundedSameBufferHandshake(t *testing.T) {
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		HandleClientConnection(server)
		close(done)
	}()
	defer client.Close()

	var request bytes.Buffer
	request.Write([]byte{':', 3, 0})
	if err := Encode(&request, SYNC, Long(1)); err != nil {
		t.Fatal(err)
	}
	writeOutcome := make(chan error, 1)
	go func() { writeOutcome <- writeAll(client, request.Bytes()) }()
	var capability [1]byte
	if _, err := io.ReadFull(client, capability[:]); err != nil {
		t.Fatalf("read server capability: %v", err)
	}
	if capability[0] != 3 {
		t.Fatalf("server capability = %d, want 3", capability[0])
	}
	if err := <-writeOutcome; err != nil {
		t.Fatalf("write pipelined handshake/request: %v", err)
	}
	response, responseType, err := Decode(bufio.NewReader(client))
	if err == nil || err.Error() != ErrSyncRequest.Error() {
		t.Fatalf("decode server response error = %v, want %v", err, ErrSyncRequest)
	}
	if responseType != RESPONSE || response != nil {
		t.Fatalf("unexpected server response: type=%d value=%#v", responseType, response)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server handler did not exit after client close")
	}
}

func TestHandleClientConnectionRejectsMalformedHandshakesWithoutHanging(t *testing.T) {
	tests := []struct {
		name       string
		handshake  []byte
		closeAfter bool
	}{
		{name: "oversized without NUL", handshake: bytes.Repeat([]byte{'x'}, maxAuthBytes+2)},
		{name: "short without NUL", handshake: []byte{':', 3}, closeAfter: true},
		{name: "invalid capability", handshake: []byte{':', 4, 0}},
		{name: "missing capability", handshake: []byte{0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			done := make(chan struct{})
			go func() {
				HandleClientConnection(server)
				close(done)
			}()
			if err := writeAll(client, test.handshake); err != nil {
				t.Fatalf("write malformed handshake: %v", err)
			}
			if test.closeAfter {
				_ = client.Close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("malformed handshake did not terminate the handler")
			}
			_ = client.Close()
		})
	}
}

type zeroWriteConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *zeroWriteConn) Write([]byte) (int, error) {
	c.writes.Add(1)
	return 0, nil
}

type invalidWriteCountConn struct {
	net.Conn
	count func(int) int
}

func (c *invalidWriteCountConn) Write(value []byte) (int, error) {
	return c.count(len(value)), nil
}

func TestWriteAllRejectsContractViolatingCountsWithoutPanicking(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	tests := []struct {
		name  string
		count func(int) int
	}{
		{name: "negative", count: func(int) int { return -1 }},
		{name: "too large", count: func(length int) int { return length + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &invalidWriteCountConn{Conn: client, count: test.count}
			if err := writeAll(conn, []byte{1}); !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("invalid write count error = %v, want io.ErrShortWrite", err)
			}
		})
	}
}

func TestHandleClientConnectionRejectsZeroLengthCapabilityWrite(t *testing.T) {
	server, client := net.Pipe()
	wrapped := &zeroWriteConn{Conn: server}
	done := make(chan struct{})
	go func() {
		HandleClientConnection(wrapped)
		close(done)
	}()
	if err := writeAll(client, []byte{':', 3, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("zero-length capability write did not terminate the handler")
	}
	if wrapped.writes.Load() != 1 {
		t.Fatalf("capability write attempts = %d, want 1", wrapped.writes.Load())
	}
	_ = client.Close()
}

type controlledConn struct {
	net.Conn
	mu                  sync.Mutex
	deadlineCalls       []time.Time
	accelerateDeadlines bool
	failDeadlineCall    int
	failWriteCall       int
	writeCalls          int
}

func (c *controlledConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlineCalls = append(c.deadlineCalls, deadline)
	call := len(c.deadlineCalls)
	c.mu.Unlock()
	if call == c.failDeadlineCall {
		return errors.New("injected SetDeadline failure")
	}
	if c.accelerateDeadlines && !deadline.IsZero() {
		deadline = time.Now().Add(20 * time.Millisecond)
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *controlledConn) Write(value []byte) (int, error) {
	c.mu.Lock()
	c.writeCalls++
	call := c.writeCalls
	c.mu.Unlock()
	if call == c.failWriteCall {
		return 0, errors.New("injected write failure")
	}
	return c.Conn.Write(value)
}

func TestHandleClientConnectionBoundsAuthenticationDeadlineAndClearsIt(t *testing.T) {
	t.Run("slowloris", func(t *testing.T) {
		server, client := net.Pipe()
		wrapped := &controlledConn{Conn: server, accelerateDeadlines: true}
		done := make(chan struct{})
		go func() {
			HandleClientConnection(wrapped)
			close(done)
		}()
		defer client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("authentication deadline did not release the handler")
		}
	})

	t.Run("established reads have no handshake deadline", func(t *testing.T) {
		server, client := net.Pipe()
		wrapped := &controlledConn{Conn: server, accelerateDeadlines: true}
		done := make(chan struct{})
		go func() {
			HandleClientConnection(wrapped)
			close(done)
		}()
		defer client.Close()
		if err := writeAll(client, []byte{':', 3, 0}); err != nil {
			t.Fatal(err)
		}
		var capability [1]byte
		if _, err := io.ReadFull(client, capability[:]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if err := Encode(client, SYNC, Long(1)); err != nil {
			t.Fatalf("established write after handshake deadline: %v", err)
		}
		_, responseType, err := Decode(bufio.NewReader(client))
		if err == nil || err.Error() != ErrSyncRequest.Error() || responseType != RESPONSE {
			t.Fatalf("established read did not survive cleared deadline: type=%d error=%v", responseType, err)
		}
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("handler did not exit")
		}
		wrapped.mu.Lock()
		defer wrapped.mu.Unlock()
		if len(wrapped.deadlineCalls) < 2 || !wrapped.deadlineCalls[len(wrapped.deadlineCalls)-1].IsZero() {
			t.Fatalf("handshake deadline was not cleared: %#v", wrapped.deadlineCalls)
		}
	})
}

func TestHandleClientConnectionFailsClosedOnDeadlineAndResponseWriteErrors(t *testing.T) {
	t.Run("initial deadline", func(t *testing.T) {
		server, client := net.Pipe()
		wrapped := &controlledConn{Conn: server, failDeadlineCall: 1}
		done := make(chan struct{})
		go func() {
			HandleClientConnection(wrapped)
			close(done)
		}()
		defer client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("initial deadline failure did not terminate handler")
		}
	})

	t.Run("clear deadline", func(t *testing.T) {
		server, client := net.Pipe()
		wrapped := &controlledConn{Conn: server, failDeadlineCall: 2}
		done := make(chan struct{})
		go func() {
			HandleClientConnection(wrapped)
			close(done)
		}()
		defer client.Close()
		if err := writeAll(client, []byte{':', 3, 0}); err != nil {
			t.Fatal(err)
		}
		var capability [1]byte
		if _, err := io.ReadFull(client, capability[:]); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("deadline-clear failure did not terminate handler")
		}
	})

	t.Run("response encode", func(t *testing.T) {
		server, client := net.Pipe()
		wrapped := &controlledConn{Conn: server, failWriteCall: 2}
		done := make(chan struct{})
		go func() {
			HandleClientConnection(wrapped)
			close(done)
		}()
		defer client.Close()
		if err := writeAll(client, []byte{':', 3, 0}); err != nil {
			t.Fatal(err)
		}
		var capability [1]byte
		if _, err := io.ReadFull(client, capability[:]); err != nil {
			t.Fatal(err)
		}
		if err := Encode(client, SYNC, Long(1)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("response encode failure did not terminate handler")
		}
	})
}

type laggingDeadlineContext struct {
	deadline time.Time
}

func (c laggingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (laggingDeadlineContext) Done() <-chan struct{}         { return nil }
func (laggingDeadlineContext) Err() error                    { return nil }
func (laggingDeadlineContext) Value(interface{}) interface{} { return nil }

type timeoutOperationError struct{}

func (timeoutOperationError) Error() string   { return "injected socket timeout" }
func (timeoutOperationError) Timeout() bool   { return true }
func (timeoutOperationError) Temporary() bool { return true }

type timeoutWriteConn struct {
	net.Conn
}

func (c timeoutWriteConn) Write([]byte) (int, error) {
	return 0, timeoutOperationError{}
}

func TestContextOperationsNormalizeSocketDeadlineRace(t *testing.T) {
	expired := laggingDeadlineContext{deadline: time.Now().Add(-time.Millisecond)}
	t.Run("operation", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		conn := newKDBConn(client, "pipe", 1, ":")
		err := conn.withContext(expired, func(net.Conn) error {
			return timeoutOperationError{}
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("socket timeout was not normalized: %v", err)
		}
	})

	t.Run("authentication", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		err := kdbHandshakeContext(expired, timeoutWriteConn{Conn: client}, ":")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("authentication timeout was not normalized: %v", err)
		}
	})

	t.Run("unrelated timeout", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		conn := newKDBConn(client, "pipe", 1, ":")
		err := conn.withContext(context.Background(), func(net.Conn) error {
			return timeoutOperationError{}
		})
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unrelated timeout was misclassified: %v", err)
		}
		var timeoutError net.Error
		if !errors.As(err, &timeoutError) || !timeoutError.Timeout() {
			t.Fatalf("unrelated network timeout was not preserved: %v", err)
		}
	})
}

func TestDialKDBContextCancellationClosesBlockedAuthAndPreservesCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	handshake := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadString(0); err != nil {
			return
		}
		close(handshake)
		var one [1]byte
		_, _ = conn.Read(one[:])
		close(closed)
	}()

	cause := errors.New("dial superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, err := DialKDBContext(ctx, "127.0.0.1", listenerPort(t, listener), ":")
		outcome <- err
	}()
	select {
	case <-handshake:
	case <-time.After(time.Second):
		t.Fatal("server did not receive authentication")
	}
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("cancellation cause was not preserved: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled authentication did not return promptly")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled authentication did not close the socket")
	}
}

func TestDialTLSContextCancellationClosesBlockedHandshakeAndPreservesCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()

	cause := errors.New("TLS no longer needed")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, err := DialTLSContext(ctx, "127.0.0.1", listenerPort(t, listener), ":", &tls.Config{InsecureSkipVerify: true})
		outcome <- err
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("TLS server did not accept connection")
	}
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("TLS cancellation cause was not preserved: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled TLS handshake did not return promptly")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled TLS handshake did not close the socket")
	}
}

func TestTLSHostnameVerificationAndCallerConfigClone(t *testing.T) {
	certificate, roots, _, _ := testCertificate(t, []string{"localhost"}, nil)
	listener, serverOutcome := startTLSAuthServer(t, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	callerConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := DialTLSContext(ctx, "localhost", listenerPort(t, listener), ":", callerConfig)
	if err != nil {
		t.Fatalf("verified TLS dial failed: %v", err)
	}
	_ = conn.Close()
	if callerConfig.ServerName != "" || callerConfig.MinVersion != tls.VersionTLS10 {
		t.Fatalf("caller TLS config was mutated: %#v", callerConfig)
	}
	if err := <-serverOutcome; err != nil {
		t.Fatalf("TLS server failed: %v", err)
	}

	listener, _ = startTLSAuthServer(t, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := DialTLSContext(ctx, "127.0.0.1", listenerPort(t, listener), ":", &tls.Config{RootCAs: roots}); err == nil {
		t.Fatal("certificate without an IP SAN verified for 127.0.0.1")
	}
}

func TestTLSIPServerNameVerification(t *testing.T) {
	certificate, roots, _, _ := testCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	listener, serverOutcome := startTLSAuthServer(t, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := DialTLSContext(ctx, "127.0.0.1", listenerPort(t, listener), ":", &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatalf("IP SAN verification failed: %v", err)
	}
	_ = conn.Close()
	if err := <-serverOutcome; err != nil {
		t.Fatalf("TLS server failed: %v", err)
	}
}

func TestDialTLSRaisesMinimumVersionToTLS12(t *testing.T) {
	certificate, _, _, _ := testCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		_ = tls.Server(raw, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS11,
			MaxVersion:   tls.VersionTLS11,
		}).Handshake()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = DialTLSContext(ctx, "127.0.0.1", listenerPort(t, listener), ":", &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS10,
	})
	if err == nil {
		t.Fatal("client negotiated TLS below 1.2")
	}
}

func TestCanceledOperationClosesConnectionAndPreservesCause(t *testing.T) {
	client, server := net.Pipe()
	conn := newKDBConn(client, "pipe", 1, ":")
	requestReceived := make(chan struct{})
	serverClosed := make(chan struct{})
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		if _, _, err := Decode(reader); err != nil {
			return
		}
		close(requestReceived)
		_, _ = reader.ReadByte()
		close(serverClosed)
	}()

	cause := errors.New("query superseded")
	ctx, cancel := context.WithCancelCause(context.Background())
	outcome := make(chan error, 1)
	go func() {
		_, _, err := conn.CallMessageContext(ctx, Long(1))
		outcome <- err
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive query")
	}
	cancel(cause)
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("operation cancellation cause was not preserved: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled operation did not return promptly")
	}
	select {
	case <-serverClosed:
	case <-time.After(time.Second):
		t.Fatal("canceled operation did not close the connection")
	}
	if _, _, err := conn.ReadMessageContext(context.Background()); err == nil {
		t.Fatal("canceled connection was reusable")
	}
}

func TestCanceledOperationBeforeLockAcquisitionDoesNotPoisonConnection(t *testing.T) {
	t.Run("queued waiter", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		conn := newKDBConn(client, "pipe", 1, ":")
		defer conn.closeInternal()

		entered := make(chan struct{})
		release := make(chan struct{})
		activeOutcome := make(chan error, 1)
		go func() {
			activeOutcome <- conn.withContext(context.Background(), func(net.Conn) error {
				close(entered)
				<-release
				return nil
			})
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("active operation did not acquire connection lock")
		}

		cause := errors.New("queued operation superseded")
		ctx, cancel := context.WithCancelCause(context.Background())
		invoked := atomic.Bool{}
		waiterOutcome := make(chan error, 1)
		go func() {
			waiterOutcome <- conn.withContext(ctx, func(net.Conn) error {
				invoked.Store(true)
				return nil
			})
		}()
		cancel(cause)
		select {
		case err := <-waiterOutcome:
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("queued cancellation cause was not preserved: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled queued operation did not return promptly")
		}
		if invoked.Load() {
			t.Fatal("queued operation ran after its context was canceled")
		}
		if !conn.ok() {
			t.Fatal("queued cancellation closed the active connection")
		}
		select {
		case err := <-activeOutcome:
			t.Fatalf("queued cancellation interrupted active operation: %v", err)
		default:
		}

		close(release)
		select {
		case err := <-activeOutcome:
			if err != nil {
				t.Fatalf("active operation failed after queued cancellation: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("active operation did not finish")
		}
		if err := conn.withContext(context.Background(), func(net.Conn) error { return nil }); err != nil {
			t.Fatalf("connection was not reusable after queued cancellation: %v", err)
		}
	})

	t.Run("already canceled idle operation", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		conn := newKDBConn(client, "pipe", 1, ":")
		defer conn.closeInternal()

		cause := errors.New("operation never started")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		invoked := false
		err := conn.withContext(ctx, func(net.Conn) error {
			invoked = true
			return nil
		})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("idle cancellation cause was not preserved: %v", err)
		}
		if invoked {
			t.Fatal("already-canceled operation ran")
		}
		if !conn.ok() {
			t.Fatal("already-canceled idle operation closed the connection")
		}
		if err := conn.withContext(context.Background(), func(net.Conn) error { return nil }); err != nil {
			t.Fatalf("connection was not reusable after idle cancellation: %v", err)
		}
	})
}

func TestSynchronousCallsRejectNonResponseFramesAndCloseConnection(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *KDBConn) error
	}{
		{
			name: "CallContext",
			call: func(ctx context.Context, conn *KDBConn) error {
				_, err := conn.CallContext(ctx, "1+1")
				return err
			},
		},
		{
			name: "CallMessageContext",
			call: func(ctx context.Context, conn *KDBConn) error {
				_, responseType, err := conn.CallMessageContext(ctx, Long(1))
				if responseType != ASYNC {
					return fmt.Errorf("decoded request type = %d, want ASYNC", responseType)
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			conn := newKDBConn(client, "pipe", 1, ":")
			go func() {
				defer server.Close()
				reader := bufio.NewReader(server)
				if _, _, err := Decode(reader); err != nil {
					return
				}
				_ = Encode(server, ASYNC, Long(2))
			}()
			err := test.call(context.Background(), conn)
			if !errors.Is(err, ErrBadMsg) {
				t.Fatalf("unexpected request type error = %v, want ErrBadMsg", err)
			}
			if _, _, err := conn.ReadMessageContext(context.Background()); err == nil {
				t.Fatal("connection remained reusable after invalid sync response framing")
			}
		})
	}
}

func TestNilKDBConnExportedOperationsReturnErrors(t *testing.T) {
	var conn *KDBConn
	operations := []struct {
		name string
		call func() error
	}{
		{name: "SetDecodeLimits", call: func() error { return conn.SetDecodeLimits(DefaultDecodeLimits()) }},
		{name: "Call", call: func() error { _, err := conn.Call("1"); return err }},
		{name: "CallContext", call: func() error { _, err := conn.CallContext(context.Background(), "1"); return err }},
		{name: "AsyncCall", call: func() error { return conn.AsyncCall("1") }},
		{name: "Response", call: func() error { return conn.Response(Long(1)) }},
		{name: "ReadMessage", call: func() error { _, _, err := conn.ReadMessage(); return err }},
		{name: "WriteMessage", call: func() error { return conn.WriteMessage(ASYNC, Long(1)) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); err == nil {
				t.Fatal("nil connection operation returned no error")
			}
		})
	}
}

func TestDialValidation(t *testing.T) {
	tests := []struct {
		host string
		port int
		auth string
	}{
		{host: "", port: 5000, auth: ":"},
		{host: "bad host", port: 5000, auth: ":"},
		{host: "localhost", port: 0, auth: ":"},
		{host: "localhost", port: 65536, auth: ":"},
		{host: "localhost", port: 5000, auth: "bad\x00auth"},
		{host: "localhost", port: 5000, auth: string(make([]byte, maxAuthBytes+1))},
	}
	for _, test := range tests {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := DialKDBContext(ctx, test.host, test.port, test.auth)
		cancel()
		if err == nil {
			t.Fatalf("invalid dial parameters succeeded: host=%q port=%d", test.host, test.port)
		}
	}
	if _, err := DialKDBTimeout("localhost", 5000, ":", 0); err == nil {
		t.Fatal("zero legacy dial timeout was accepted")
	}
}

func TestEndpointIPv6BracketsAndScopedTLSServerName(t *testing.T) {
	for _, host := range []string{"[example.com]", "[127.0.0.1]"} {
		if _, err := ValidateEndpoint(host, 5000); err == nil {
			t.Fatalf("non-IPv6 bracketed host %q was accepted", host)
		}
	}
	if host, err := ValidateEndpoint("[::1]", 5000); err != nil || host != "::1" {
		t.Fatalf("bracketed IPv6 normalization = %q, %v", host, err)
	}
	const scoped = "fe80::1%eth0"
	host, err := ValidateEndpoint("["+scoped+"]", 5000)
	if err != nil || host != scoped {
		t.Fatalf("scoped IPv6 normalization = %q, %v", host, err)
	}
	if got := net.JoinHostPort(host, "5000"); got != "[fe80::1%eth0]:5000" {
		t.Fatalf("scoped IPv6 dial address = %q", got)
	}
	if got := tlsServerName(host); got != "fe80::1" {
		t.Fatalf("scoped IPv6 TLS server name = %q, want fe80::1", got)
	}
}
