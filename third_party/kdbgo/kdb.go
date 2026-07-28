package kdb

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultDialTimeout bounds legacy dial wrappers and context-aware dials
	// whose caller did not provide an earlier deadline.
	DefaultDialTimeout = 10 * time.Second
	maxAuthBytes       = 253
)

// KDBConn establishes a sequential connection using the q IPC protocol.
type KDBConn struct {
	con     net.Conn
	rbuf    *bufio.Reader
	Host    string
	Port    string
	userpwd string

	opMu         contextMutex
	stateMu      sync.Mutex
	closed       bool
	decodeLimits DecodeLimits
}

type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) initialize() {
	m.once.Do(func() {
		m.token = make(chan struct{}, 1)
		m.token <- struct{}{}
	})
}

func (m *contextMutex) Lock() {
	m.initialize()
	<-m.token
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	m.initialize()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.token:
		select {
		case <-ctx.Done():
			m.Unlock()
			return contextStatus(ctx)
		default:
			return nil
		}
	default:
	}
	if err := contextStatus(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return contextStatus(ctx)
	case <-m.token:
		if err := contextStatus(ctx); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

func (m *contextMutex) Unlock() {
	m.initialize()
	select {
	case m.token <- struct{}{}:
	default:
		panic("unlock of unlocked contextMutex")
	}
}

func (c *KDBConn) connection() (net.Conn, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed || c.con == nil {
		return nil, errors.New("Closed connection")
	}
	return c.con, nil
}

func (c *KDBConn) closeInternal() error {
	c.stateMu.Lock()
	if c.closed || c.con == nil {
		c.closed = true
		c.stateMu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.con
	c.stateMu.Unlock()
	return conn.Close()
}

// Close closes the connection.
func (c *KDBConn) Close() error {
	if c == nil {
		return errors.New("Closed connection")
	}
	c.stateMu.Lock()
	alreadyClosed := c.closed || c.con == nil
	c.stateMu.Unlock()
	if alreadyClosed {
		return errors.New("Closed connection")
	}
	return c.closeInternal()
}

func (c *KDBConn) ok() bool {
	if c == nil {
		return false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return !c.closed && c.con != nil
}

// SetDecodeLimits changes the limits used by subsequent reads.
func (c *KDBConn) SetDecodeLimits(limits DecodeLimits) error {
	if c == nil {
		return errors.New("nil KDBConn")
	}
	normalized, err := limits.normalized()
	if err != nil {
		return err
	}
	c.opMu.Lock()
	c.decodeLimits = normalized
	c.opMu.Unlock()
	return nil
}

func (c *KDBConn) limits() DecodeLimits {
	if c.decodeLimits.MaxWireFrame == 0 {
		return DefaultDecodeLimits()
	}
	return c.decodeLimits
}

func contextStatus(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	err := ctx.Err()
	if err == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(cause, err) {
		return err
	}
	return errors.Join(err, cause)
}

func normalizeContextOperationError(ctx context.Context, operationErr error) error {
	if ctxErr := contextStatus(ctx); ctxErr != nil {
		return ctxErr
	}
	if operationErr == nil || ctx == nil {
		return operationErr
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || time.Now().Before(deadline) {
		return operationErr
	}
	var timeoutError net.Error
	if !errors.As(operationErr, &timeoutError) || !timeoutError.Timeout() {
		return operationErr
	}
	cause := context.Cause(ctx)
	if cause != nil && !errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(context.DeadlineExceeded, cause)
	}
	return context.DeadlineExceeded
}

func (c *KDBConn) withContext(ctx context.Context, operation func(net.Conn) error) error {
	if c == nil {
		return errors.New("nil KDBConn")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextStatus(ctx); err != nil {
		return err
	}

	if err := c.opMu.LockContext(ctx); err != nil {
		return err
	}
	defer c.opMu.Unlock()
	conn, err := c.connection()
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = c.closeInternal()
			return err
		}
	} else if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = c.closeInternal()
		return err
	}

	stop := context.AfterFunc(ctx, func() {
		_ = c.closeInternal()
	})
	err = operation(conn)
	stopped := stop()
	if ctxErr := contextStatus(ctx); ctxErr != nil {
		_ = c.closeInternal()
		return ctxErr
	}
	if !stopped {
		_ = c.closeInternal()
		if ctxErr := contextStatus(ctx); ctxErr != nil {
			return ctxErr
		}
		if normalized := normalizeContextOperationError(ctx, err); errors.Is(normalized, context.DeadlineExceeded) {
			return normalized
		}
		return errors.New("operation context callback ran before completion")
	}
	if err != nil {
		_ = c.closeInternal()
		return normalizeContextOperationError(ctx, err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = c.closeInternal()
		return err
	}
	return nil
}

func readClientHandshake(reader *bufio.Reader) (byte, error) {
	if reader == nil {
		return 0, errors.New("nil authentication reader")
	}
	var payload [maxAuthBytes + 1]byte
	length := 0
	for {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("kdb+ authentication handshake is missing its NUL terminator")
			}
			return 0, fmt.Errorf("read kdb+ authentication handshake: %w", err)
		}
		if value == 0 {
			if length == 0 {
				return 0, errors.New("kdb+ authentication handshake is missing its capability byte")
			}
			capability := payload[length-1]
			if capability < 1 || capability > 3 {
				return 0, fmt.Errorf("kdb+ authentication handshake has invalid capability %d", capability)
			}
			if err := validateAuth(string(payload[:length-1])); err != nil {
				return 0, err
			}
			return capability, nil
		}
		if length == len(payload) {
			return 0, fmt.Errorf(
				"kdb+ authentication handshake exceeds %d authentication bytes plus one capability byte",
				maxAuthBytes,
			)
		}
		payload[length] = value
		length++
	}
}

// HandleClientConnection provides the legacy minimal IPC handler.
func HandleClientConnection(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetNoDelay(true)
	}
	if err := conn.SetDeadline(time.Now().Add(DefaultDialTimeout)); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	capability, err := readClientHandshake(reader)
	if err != nil {
		return
	}
	if err := writeAll(conn, []byte{capability}); err != nil {
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	for {
		_, msgtype, err := Decode(reader)
		if err != nil {
			return
		}
		if msgtype == SYNC {
			if err := Encode(conn, RESPONSE, Error(ErrSyncRequest)); err != nil {
				return
			}
		}
	}
}

func callValue(cmd string, args ...*K) *K {
	command := &K{KC, NONE, cmd}
	if len(args) == 0 {
		return command
	}
	return &K{K0, NONE, append([]*K{command}, args...)}
}

// CallContext performs a synchronous call with cancellation/deadline support.
// A canceled or failed operation closes the connection so it cannot be reused
// with an ambiguous stream position.
func (c *KDBConn) CallContext(ctx context.Context, cmd string, args ...*K) (*K, error) {
	var result *K
	err := c.withContext(ctx, func(conn net.Conn) error {
		if err := Encode(conn, SYNC, callValue(cmd, args...)); err != nil {
			return err
		}
		value, responseType, err := DecodeWithLimits(c.rbuf, c.limits())
		if err == nil && responseType != RESPONSE {
			result = nil
			return fmt.Errorf("%w: synchronous call received request type %d instead of RESPONSE", ErrBadMsg, responseType)
		}
		result = value
		return err
	})
	return result, err
}

// CallMessageContext sends an already-built q value as a synchronous request
// and reads its response under one context budget.
func (c *KDBConn) CallMessageContext(ctx context.Context, value *K) (*K, ReqType, error) {
	var (
		result   *K
		response ReqType = -1
	)
	err := c.withContext(ctx, func(conn net.Conn) error {
		if err := Encode(conn, SYNC, value); err != nil {
			return err
		}
		var err error
		result, response, err = DecodeWithLimits(c.rbuf, c.limits())
		if err == nil && response != RESPONSE {
			result = nil
			return fmt.Errorf("%w: synchronous call received request type %d instead of RESPONSE", ErrBadMsg, response)
		}
		return err
	})
	return result, response, err
}

// Call performs the legacy unbounded synchronous operation.
func (c *KDBConn) Call(cmd string, args ...*K) (*K, error) {
	return c.CallContext(context.Background(), cmd, args...)
}

// AsyncCallContext performs an asynchronous call.
func (c *KDBConn) AsyncCallContext(ctx context.Context, cmd string, args ...*K) error {
	return c.withContext(ctx, func(conn net.Conn) error {
		return Encode(conn, ASYNC, callValue(cmd, args...))
	})
}

func (c *KDBConn) AsyncCall(cmd string, args ...*K) error {
	return c.AsyncCallContext(context.Background(), cmd, args...)
}

// ResponseContext sends a response with cancellation/deadline support.
func (c *KDBConn) ResponseContext(ctx context.Context, data *K) error {
	return c.WriteMessageContext(ctx, RESPONSE, data)
}

func (c *KDBConn) Response(data *K) error {
	return c.ResponseContext(context.Background(), data)
}

// ReadMessageContext reads one complete frame.
func (c *KDBConn) ReadMessageContext(ctx context.Context) (*K, ReqType, error) {
	var (
		data    *K
		msgtype ReqType = -1
	)
	err := c.withContext(ctx, func(net.Conn) error {
		var err error
		data, msgtype, err = DecodeWithLimits(c.rbuf, c.limits())
		return err
	})
	return data, msgtype, err
}

func (c *KDBConn) ReadMessage() (*K, ReqType, error) {
	return c.ReadMessageContext(context.Background())
}

// WriteMessageContext writes one complete frame.
func (c *KDBConn) WriteMessageContext(ctx context.Context, msgtype ReqType, data *K) error {
	return c.withContext(ctx, func(conn net.Conn) error {
		return Encode(conn, msgtype, data)
	})
}

func (c *KDBConn) WriteMessage(msgtype ReqType, data *K) error {
	return c.WriteMessageContext(context.Background(), msgtype, data)
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("kdb+ port must be between 1 and 65535; got %d", port)
	}
	return nil
}

// ValidateEndpoint validates and normalizes a TCP/TLS host and port without
// performing any network activity.
func ValidateEndpoint(host string, port int) (string, error) {
	normalized, err := normalizeHost(host)
	if err != nil {
		return "", err
	}
	if err := validatePort(port); err != nil {
		return "", err
	}
	return normalized, nil
}

func normalizeHost(host string) (string, error) {
	if host == "" {
		return "", errors.New("kdb+ host must not be empty")
	}
	if strings.TrimSpace(host) != host || strings.IndexFunc(host, func(r rune) bool {
		return r == 0 || r == '\r' || r == '\n' || r == '\t' || r == ' '
	}) >= 0 {
		return "", errors.New("kdb+ host contains whitespace or control characters")
	}
	hasOpenBracket := strings.HasPrefix(host, "[")
	hasCloseBracket := strings.HasSuffix(host, "]")
	if hasOpenBracket != hasCloseBracket {
		return "", fmt.Errorf("kdb+ host %q has mismatched IPv6 brackets", host)
	}
	if hasOpenBracket {
		unbracketed := host[1 : len(host)-1]
		address, err := netip.ParseAddr(unbracketed)
		if err != nil || !address.Is6() {
			return "", fmt.Errorf("kdb+ bracketed host %q is not an IPv6 address", host)
		}
		return unbracketed, nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return host, nil
	}
	if strings.Contains(host, ":") || len(host) > 253 {
		return "", fmt.Errorf("kdb+ host %q is not a valid IP address or DNS name", host)
	}
	domain := strings.TrimSuffix(host, ".")
	if domain == "" {
		return "", fmt.Errorf("kdb+ host %q is not a valid DNS name", host)
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("kdb+ host %q contains an invalid DNS label", host)
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return "", fmt.Errorf("kdb+ host %q contains an invalid DNS character", host)
			}
		}
	}
	return host, nil
}

func tlsServerName(host string) string {
	address, err := netip.ParseAddr(host)
	if err != nil || address.Zone() == "" {
		return host
	}
	return address.WithZone("").String()
}

func validateAuth(auth string) error {
	if strings.IndexByte(auth, 0) >= 0 {
		return errors.New("kdb+ authentication value must not contain NUL")
	}
	if len(auth) > maxAuthBytes {
		return fmt.Errorf("kdb+ authentication value is %d bytes; maximum is %d", len(auth), maxAuthBytes)
	}
	return nil
}

// ValidateAuth validates the complete q IPC authentication field, including
// any username/password separator, without performing network activity.
func ValidateAuth(auth string) error {
	return validateAuth(auth)
}

func boundedDialContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("dial context must not be nil")
	}
	if err := contextStatus(ctx); err != nil {
		return nil, nil, err
	}
	if _, ok := ctx.Deadline(); ok {
		child, cancel := context.WithCancel(ctx)
		return child, cancel, nil
	}
	child, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
	return child, cancel, nil
}

func writeAll(conn net.Conn, value []byte) error {
	for len(value) > 0 {
		n, err := conn.Write(value)
		if n < 0 || n > len(value) {
			return fmt.Errorf("%w: writer returned invalid count %d for %d bytes", io.ErrShortWrite, n, len(value))
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		value = value[n:]
	}
	return nil
}

func kdbHandshakeContext(ctx context.Context, conn net.Conn, auth string) error {
	if err := validateAuth(auth); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	request := make([]byte, 0, len(auth)+2)
	request = append(request, auth...)
	request = append(request, 3, 0)
	if err := writeAll(conn, request); err != nil {
		stop()
		return fmt.Errorf("write kdb+ authentication handshake: %w", normalizeContextOperationError(ctx, err))
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		stop()
		return fmt.Errorf("read kdb+ authentication handshake: %w", normalizeContextOperationError(ctx, err))
	}
	stopped := stop()
	if ctxErr := contextStatus(ctx); ctxErr != nil {
		return ctxErr
	}
	if !stopped {
		return errors.New("authentication context callback ran before completion")
	}
	if reply[0] < 1 || reply[0] > 3 {
		return fmt.Errorf("kdb+ authentication was rejected or returned invalid protocol version %d", reply[0])
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear kdb+ authentication deadline: %w", err)
	}
	return nil
}

func newKDBConn(conn net.Conn, host string, port int, auth string) *KDBConn {
	return &KDBConn{
		con:          conn,
		rbuf:         bufio.NewReader(conn),
		Host:         host,
		Port:         strconv.Itoa(port),
		userpwd:      auth,
		decodeLimits: DefaultDecodeLimits(),
	}
}

// DialKDBContext connects over TCP and completes q authentication within ctx.
func DialKDBContext(ctx context.Context, host string, port int, auth string) (*KDBConn, error) {
	normalizedHost, err := normalizeHost(host)
	if err != nil {
		return nil, err
	}
	if err := validatePort(port); err != nil {
		return nil, err
	}
	if err := validateAuth(auth); err != nil {
		return nil, err
	}
	dialCtx, cancel, err := boundedDialContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	address := net.JoinHostPort(normalizedHost, strconv.Itoa(port))
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		if ctxErr := contextStatus(dialCtx); ctxErr != nil {
			return nil, fmt.Errorf("dial kdb+ TCP %s: %w", address, ctxErr)
		}
		return nil, fmt.Errorf("dial kdb+ TCP %s: %w", address, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetNoDelay(true)
	}
	if err := kdbHandshakeContext(dialCtx, conn, auth); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newKDBConn(conn, normalizedHost, port, auth), nil
}

func DialKDB(host string, port int, auth string) (*KDBConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultDialTimeout)
	defer cancel()
	return DialKDBContext(ctx, host, port, auth)
}

func DialKDBTimeout(host string, port int, auth string, timeout time.Duration) (*KDBConn, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("kdb+ dial timeout must be positive; got %v", timeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return DialKDBContext(ctx, host, port, auth)
}

// DialTLSContext connects over TCP, performs a context-aware TLS handshake,
// and then performs the bounded q authentication handshake.
func DialTLSContext(ctx context.Context, host string, port int, auth string, cfg *tls.Config) (*KDBConn, error) {
	normalizedHost, err := normalizeHost(host)
	if err != nil {
		return nil, err
	}
	if err := validatePort(port); err != nil {
		return nil, err
	}
	if err := validateAuth(auth); err != nil {
		return nil, err
	}
	dialCtx, cancel, err := boundedDialContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg != nil {
		config = cfg.Clone()
		if config.MinVersion < tls.VersionTLS12 {
			config.MinVersion = tls.VersionTLS12
		}
	}
	if config.ServerName == "" {
		config.ServerName = tlsServerName(normalizedHost)
	}

	address := net.JoinHostPort(normalizedHost, strconv.Itoa(port))
	raw, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		if ctxErr := contextStatus(dialCtx); ctxErr != nil {
			return nil, fmt.Errorf("dial kdb+ TLS %s: %w", address, ctxErr)
		}
		return nil, fmt.Errorf("dial kdb+ TLS %s: %w", address, err)
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetNoDelay(true)
	}
	conn := tls.Client(raw, config)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		_ = conn.Close()
		if ctxErr := contextStatus(dialCtx); ctxErr != nil {
			return nil, fmt.Errorf("handshake kdb+ TLS %s: %w", address, ctxErr)
		}
		return nil, fmt.Errorf("handshake kdb+ TLS %s: %w", address, err)
	}
	if err := kdbHandshakeContext(dialCtx, conn, auth); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newKDBConn(conn, normalizedHost, port, auth), nil
}

func DialTLS(host string, port int, auth string, cfg *tls.Config) (*KDBConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultDialTimeout)
	defer cancel()
	return DialTLSContext(ctx, host, port, auth, cfg)
}

// DialUnixContext connects to the legacy kdb+ Unix-domain socket path.
func DialUnixContext(ctx context.Context, host string, port int, auth string) (*KDBConn, error) {
	if err := validatePort(port); err != nil {
		return nil, err
	}
	if err := validateAuth(auth); err != nil {
		return nil, err
	}
	dialCtx, cancel, err := boundedDialContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var address string
	switch runtime.GOOS {
	case "linux":
		address = fmt.Sprintf("@/tmp/kx.%d", port)
	case "darwin":
		address = fmt.Sprintf("/tmp/kx.%d", port)
	default:
		return nil, net.UnknownNetworkError("unix")
	}
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", address)
	if err != nil {
		if ctxErr := contextStatus(dialCtx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if err := kdbHandshakeContext(dialCtx, conn, auth); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newKDBConn(conn, host, port, auth), nil
}

func DialUnix(host string, port int, auth string) (*KDBConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultDialTimeout)
	defer cancel()
	return DialUnixContext(ctx, host, port, auth)
}
