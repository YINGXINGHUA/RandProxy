package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	socks5 "golang.org/x/net/proxy"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/fetcher"
	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/proxy"
	"github.com/user/randproxy/internal/validator"
)

const (
	defaultMaxConcurrentConns = 200
	maxSupportedConns         = 10000
	overCapacityProtocolPeek  = 10 * time.Millisecond
)

var bufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// Config for the proxy server.
type Config struct {
	Listen           string // e.g. ":8080" — built from config.ListenAddr()
	WebListen        string
	RelayIdleTimeout time.Duration
	MaxConnections   int
}

// ProxyServer supports both HTTP CONNECT and SOCKS5 on the same port.
type ProxyServer struct {
	cfg          Config
	pool         *pool.Pool
	allocator    allocation.Allocator
	stats        *Stats
	controlPlane *controlPlaneState
	connSem      chan struct{}
}

func New(cfg Config, p *pool.Pool) *ProxyServer {
	return NewWithAllocator(cfg, p, allocation.New(p))
}

func NewWithAllocator(cfg Config, p *pool.Pool, allocator allocation.Allocator) *ProxyServer {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.RelayIdleTimeout <= 0 {
		cfg.RelayIdleTimeout = 60 * time.Second
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = defaultMaxConcurrentConns
	}
	if cfg.MaxConnections > maxSupportedConns {
		cfg.MaxConnections = maxSupportedConns
	}
	if allocator == nil {
		allocator = allocation.New(p)
	}
	return &ProxyServer{cfg: cfg, pool: p, allocator: allocator, connSem: make(chan struct{}, cfg.MaxConnections)}
}

func (s *ProxyServer) acquireLease(ctx context.Context) (*allocation.Lease, error) {
	if s.allocator == nil {
		return nil, allocation.ErrNoProxyAvailable
	}

	return s.allocator.Acquire(ctx, allocation.AcquireRequest{})
}

// SetStatsProvider wires the stats tracker with provider status access.
func (s *ProxyServer) SetStatsProvider(fn func() []proxy.Provider) {
	s.stats = newStats(fn)
}

func (s *ProxyServer) SetFetchStats(fn func() []fetcher.FetchStat) {
	if s.stats != nil {
		s.stats.SetFetchStats(fn)
	}
}

func (s *ProxyServer) SetValidStats(fn func() []validator.ValStat) {
	if s.stats != nil {
		s.stats.SetValidStats(fn)
	}
}

func (s *ProxyServer) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	log.Printf("[INFO] [server] proxy listening on %s (HTTP CONNECT + SOCKS5)", s.cfg.Listen)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			log.Printf("[ERROR] [server] accept: %v", err)
			continue
		}
		go s.handleConn(conn)
	}
}

// --- Protocol detection ---

func (s *ProxyServer) handleConn(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ERROR] [server] handleConn panic: %v", r)
		}
		conn.Close()
	}()

	// Acquire semaphore slot. If full, close connection immediately.
	select {
	case s.connectionSemaphore() <- struct{}{}:
	default:
		s.rejectOverCapacity(conn)
		return
	}
	defer func() { <-s.connectionSemaphore() }()
	if s.stats != nil {
		s.stats.connectionOpened()
		defer s.stats.connectionClosed()
	}

	// Peek the first byte to detect protocol
	buf := make([]byte, 1)
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	if buf[0] == 0x05 {
		s.handleSOCKS5(conn, buf[0])
	} else {
		s.handleHTTP(conn, buf[0])
	}
}

func (s *ProxyServer) connectionSemaphore() chan struct{} {
	if s.connSem == nil {
		s.connSem = make(chan struct{}, defaultMaxConcurrentConns)
	}
	return s.connSem
}

func (s *ProxyServer) rejectOverCapacity(conn net.Conn) {
	if err := conn.SetReadDeadline(time.Now().Add(overCapacityProtocolPeek)); err != nil {
		return
	}
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if first[0] == 0x05 {
		_, _ = conn.Write([]byte{0x05, 0xFF})
		return
	}
	_, _ = conn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 17\r\n\r\nproxy overloaded\n"))
}

// --- SOCKS5 downstream handler ---

func (s *ProxyServer) handleSOCKS5(conn net.Conn, firstByte byte) {
	// firstByte (0x05) already consumed by handleConn for protocol detection.
	// Read nmethods + methods to reconstruct the auth negotiation.
	_ = firstByte
	nmb := make([]byte, 1)
	if _, err := io.ReadFull(conn, nmb); err != nil {
		return
	}
	methods := make([]byte, nmb[0])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	var selectedMethod byte = 0xFF
	for _, m := range methods {
		if m == 0x00 {
			selectedMethod = 0x00
			break
		}
		if m == 0x02 {
			selectedMethod = 0x02
		}
	}

	if _, err := conn.Write([]byte{0x05, selectedMethod}); err != nil {
		return
	}

	if selectedMethod == 0xFF {
		return
	}

	if selectedMethod == 0x02 {
		verBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, verBuf); err != nil {
			return
		}
		if verBuf[0] != 0x01 {
			return
		}

		ulenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, ulenBuf); err != nil {
			return
		}
		uname := make([]byte, ulenBuf[0])
		if _, err := io.ReadFull(conn, uname); err != nil {
			return
		}

		plenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, plenBuf); err != nil {
			return
		}
		passwd := make([]byte, plenBuf[0])
		if _, err := io.ReadFull(conn, passwd); err != nil {
			return
		}

		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
	}

	// 2. Request: [ver, cmd, rsv, atyp, addr, port]
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	if header[0] != 0x05 {
		return
	}
	cmd := header[1]
	if cmd != 0x01 && cmd != 0x03 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // command not supported
		return
	}
	host, port, err := readSocks5Address(conn, header[3])
	if err != nil {
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))

	if cmd == 0x03 {
		s.handleSOCKS5UDPAssociate(conn, target)
		return
	}

	s.handleSOCKS5Connect(conn, target)
}

func (s *ProxyServer) handleSOCKS5Connect(conn net.Conn, target string) {

	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := s.acquireLease(ctx)
	var upstream string

	record := func(success bool, latency time.Duration) {
		if s.stats != nil {
			s.stats.record(latency, success)
			s.stats.recordRequest(target, upstream, latency.Milliseconds(), success)
		}
	}

	if err != nil {
		log.Printf("[WARN] [server] SOCKS5 no proxy for %s", target)
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // general failure
		record(false, time.Since(t0))
		return
	}
	defer lease.Finish(allocation.Result{Success: false})

	upstream = lease.UpstreamAddr()
	dialer, err := socks5.SOCKS5("tcp", upstream, nil, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		log.Printf("[ERROR] [server] SOCKS5 dialer %s: %v", upstream, err)
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		record(false, time.Since(t0))
		return
	}

	cd, ok := dialer.(socks5.ContextDialer)
	if !ok {
		log.Printf("[ERROR] [server] SOCKS5 dialer does not support context")
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		record(false, time.Since(t0))
		return
	}
	upConn, err := cd.DialContext(ctx, "tcp", target)
	if err != nil {
		log.Printf("[WARN] [server] SOCKS5 dial %s via %s: %v", target, upstream, err)
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // host unreachable
		record(false, time.Since(t0))
		return
	}
	defer upConn.Close()

	// SOCKS5 success response
	resp := []byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if _, err := conn.Write(resp); err != nil {
		record(false, time.Since(t0))
		return
	}

	// Capture connection establishment time before relay (streaming may last indefinitely).
	// Dashboard stats and health scoring use connect latency only, not the relay duration.
	connTime := time.Since(t0)
	log.Printf("[DEBUG] [server] SOCKS5 relaying %s ↔ %s via %s", conn.RemoteAddr(), target, upstream)
	relay(conn, upConn, s.cfg.RelayIdleTimeout)
	lease.Finish(allocation.Result{Success: true, ConnectLatency: connTime})
	record(true, connTime)
}

// --- HTTP handler ---

// prependReader serves the first byte before falling through to the underlying reader.
type prependReader struct {
	first byte
	done  bool
	rest  io.Reader
}

func (r *prependReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !r.done {
		r.done = true
		p[0] = r.first
		return 1, nil
	}
	return r.rest.Read(p)
}

// prefixedReaderConn wraps a net.Conn and a bufio.Reader so that reads
// drain the bufio buffer first, then fall through to the connection.
// This prevents data loss when http.ReadRequest buffered bytes that
// should be relayed (e.g., pipelined data after a CONNECT request).
type prefixedReaderConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixedReaderConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

// rawResponseWriter writes HTTP responses directly to a net.Conn.
type rawResponseWriter struct {
	conn    net.Conn
	header  http.Header
	written bool
}

func (w *rawResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *rawResponseWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.written = true
	status := http.StatusText(code)
	fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", code, status)
	w.header.Write(w.conn)
	w.conn.Write([]byte("\r\n"))
}

func (w *rawResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(b)
}

func (s *ProxyServer) handleHTTP(conn net.Conn, firstByte byte) {
	pr := &prependReader{first: firstByte, rest: conn}
	br := bufio.NewReader(pr)

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	switch {
	case req.Method == http.MethodConnect:
		s.handleHTTPConnect(conn, br, req)
	default:
		s.handleHTTPForward(conn, br, req)
	}
}

func (s *ProxyServer) handleHTTPConnect(conn net.Conn, br *bufio.Reader, r *http.Request) {
	target := r.Host
	log.Printf("[DEBUG] [server] CONNECT %s", target)

	t0 := time.Now()
	lease, err := s.acquireLease(r.Context())
	var upstream string

	record := func(success bool, latency time.Duration) {
		if s.stats != nil {
			s.stats.record(latency, success)
			s.stats.recordRequest(target, upstream, latency.Milliseconds(), success)
		}
	}

	if err != nil {
		log.Printf("[WARN] [server] no proxy for %s", target)
		writeHTTPError(conn, http.StatusServiceUnavailable, "No proxy available")
		record(false, time.Since(t0))
		return
	}
	defer lease.Finish(allocation.Result{Success: false})

	upstream = lease.UpstreamAddr()
	dialer, err := socks5.SOCKS5("tcp", upstream, nil, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		log.Printf("[ERROR] [server] SOCKS5 dialer %s: %v", upstream, err)
		writeHTTPError(conn, http.StatusBadGateway, "Bad gateway")
		record(false, time.Since(t0))
		return
	}

	cd, ok := dialer.(socks5.ContextDialer)
	if !ok {
		log.Printf("[ERROR] [server] SOCKS5 dialer does not support context")
		writeHTTPError(conn, http.StatusBadGateway, "Bad gateway")
		record(false, time.Since(t0))
		return
	}
	upConn, err := cd.DialContext(r.Context(), "tcp", target)
	if err != nil {
		log.Printf("[WARN] [server] dial %s via %s: %v", target, upstream, err)
		writeHTTPError(conn, http.StatusBadGateway, "Bad gateway")
		record(false, time.Since(t0))
		return
	}
	defer upConn.Close()

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		log.Printf("[ERROR] [server] write 200: %v", err)
		record(false, time.Since(t0))
		return
	}

	// Capture connection time before relay (streaming may last indefinitely).
	connTime := time.Since(t0)
	log.Printf("[DEBUG] [server] HTTP relaying %s ↔ %s via %s", conn.RemoteAddr(), target, upstream)
	relay(&prefixedReaderConn{Conn: conn, r: br}, upConn, s.cfg.RelayIdleTimeout)
	lease.Finish(allocation.Result{Success: true, ConnectLatency: connTime})
	record(true, connTime)
}

func (s *ProxyServer) handleHTTPForward(conn net.Conn, br *bufio.Reader, req *http.Request) {
	t0 := time.Now()
	lease, err := s.acquireLease(req.Context())
	var upstream string

	record := func(success bool, latency time.Duration) {
		if s.stats != nil {
			s.stats.record(latency, success)
			s.stats.recordRequest(req.URL.String(), upstream, latency.Milliseconds(), success)
		}
	}

	if err != nil {
		log.Printf("[WARN] [server] no proxy for %s %s", req.Method, req.URL)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "No proxy available", http.StatusServiceUnavailable)
		record(false, time.Since(t0))
		return
	}
	defer lease.Finish(allocation.Result{Success: false})

	upstream = lease.UpstreamAddr()

	outReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		log.Printf("[ERROR] [server] build request: %v", err)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad request", http.StatusBadRequest)
		record(false, time.Since(t0))
		return
	}

	outReq.Header = req.Header.Clone()
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authorization")

	dialer, err := socks5.SOCKS5("tcp", upstream, nil, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		log.Printf("[ERROR] [server] SOCKS5 dialer %s: %v", upstream, err)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad gateway", http.StatusBadGateway)
		record(false, time.Since(t0))
		return
	}

	cd, ok := dialer.(socks5.ContextDialer)
	if !ok {
		log.Printf("[ERROR] [server] SOCKS5 dialer does not support context")
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad gateway", http.StatusBadGateway)
		record(false, time.Since(t0))
		return
	}

	targetHost := req.URL.Host
	if req.URL.Port() == "" {
		if req.URL.Scheme == "https" {
			targetHost = req.URL.Host + ":443"
		} else {
			targetHost = req.URL.Host + ":80"
		}
	}

	upConn, err := cd.DialContext(req.Context(), "tcp", targetHost)
	if err != nil {
		log.Printf("[WARN] [server] dial %s via %s: %v", targetHost, upstream, err)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad gateway", http.StatusBadGateway)
		record(false, time.Since(t0))
		return
	}
	defer upConn.Close()

	if err := outReq.Write(upConn); err != nil {
		log.Printf("[ERROR] [server] write request: %v", err)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad gateway", http.StatusBadGateway)
		record(false, time.Since(t0))
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(upConn), outReq)
	if err != nil {
		log.Printf("[ERROR] [server] read response: %v", err)
		rw := &rawResponseWriter{conn: conn}
		http.Error(rw, "Bad gateway", http.StatusBadGateway)
		record(false, time.Since(t0))
		return
	}
	defer resp.Body.Close()

	rw := &rawResponseWriter{conn: conn}
	for k, vv := range resp.Header {
		for _, v := range vv {
			rw.Header().Add(k, v)
		}
	}
	rw.WriteHeader(resp.StatusCode)
	connTime := time.Since(t0)
	if _, err := io.Copy(rw, resp.Body); err != nil {
		log.Printf("[ERROR] [server] copy response: %v", err)
	}
	lease.Finish(allocation.Result{Success: true, ConnectLatency: connTime})

	record(true, connTime)
}

func writeHTTPError(conn net.Conn, code int, msg string) {
	status := http.StatusText(code)
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\n\r\n%s", code, status, len(msg), msg)
}

// relay copies data bidirectionally between two connections.
func relay(a, b net.Conn, idle time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(a, b, idle)
		a.Close()
		b.Close()
	}()
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(b, a, idle)
		b.Close()
		a.Close()
	}()
	wg.Wait()
}

func copyWithIdleTimeout(dst, src net.Conn, idle time.Duration) {
	bp := bufPool.Get().(*[]byte)
	buf := *bp
	defer bufPool.Put(bp)
	for {
		if idle > 0 {
			if err := src.SetReadDeadline(time.Now().Add(idle)); err != nil {
				return
			}
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
