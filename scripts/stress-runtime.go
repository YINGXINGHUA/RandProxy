package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/controlplane"
	"github.com/user/randproxy/internal/pool"
	proxycore "github.com/user/randproxy/internal/proxy"
	"github.com/user/randproxy/internal/server"
)

type stressResult struct {
	HTTPForwardOK      atomic.Int64
	HTTPConnectOK      atomic.Int64
	SOCKSConnectOK     atomic.Int64
	Failures           atomic.Int64
	MonitorFailures    atomic.Int64
	OverviewStatusCode int
	PoolStatusCode     int
	ActiveConnections  int64
	ActiveLeases       int64
	PeakConnections    int64
	PeakLeases         int64
	Window30mRequests  int64
	PoolReady          int
	PoolActiveLeases   int
}

type roundSummary struct {
	Round             int            `json:"round"`
	Duration          string         `json:"duration"`
	Concurrency       int            `json:"concurrency"`
	HTTPForwardOK     int64          `json:"http_forward_ok"`
	HTTPConnectOK     int64          `json:"http_connect_ok"`
	SOCKSConnectOK    int64          `json:"socks_connect_ok"`
	Failures          int64          `json:"failures"`
	MonitorFailures   int64          `json:"monitor_failures"`
	OverviewStatus    int            `json:"overview_status"`
	PoolStatus        int            `json:"pool_status"`
	ActiveConnections int64          `json:"active_connections"`
	ActiveLeases      int64          `json:"active_leases"`
	PeakConnections   int64          `json:"peak_connections"`
	PeakLeases        int64          `json:"peak_leases"`
	Window30mRequests int64          `json:"window_30m_requests"`
	PoolReady         int            `json:"pool_ready"`
	PoolActiveLeases  int            `json:"pool_active_leases"`
	Goroutines        int            `json:"goroutines"`
	AllocBytes        uint64         `json:"alloc_bytes"`
	FailureKinds      map[string]int `json:"failure_kinds,omitempty"`
	FailureSamples    []string       `json:"failure_samples,omitempty"`
	Anomalies         []string       `json:"anomalies,omitempty"`
}

type failureRecorder struct {
	mu      sync.Mutex
	kinds   map[string]int
	samples []string
}

func newFailureRecorder() *failureRecorder {
	return &failureRecorder{kinds: make(map[string]int)}
}

func (r *failureRecorder) record(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	kind := classifyFailure(message)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[kind]++
	if len(r.samples) < 20 {
		r.samples = append(r.samples, message)
	}
}

func (r *failureRecorder) snapshot() (map[string]int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kinds := make(map[string]int, len(r.kinds))
	for key, value := range r.kinds {
		kinds[key] = value
	}
	samples := append([]string(nil), r.samples...)
	return kinds, samples
}

var jsonClient = &http.Client{Timeout: 2 * time.Second}

func main() {
	concurrency := flag.Int("concurrency", 20, "concurrent workers")
	duration := flag.Duration("duration", 20*time.Second, "stress duration")
	rounds := flag.Int("rounds", 1, "number of stress rounds")
	pause := flag.Duration("pause", time.Second, "pause between rounds")
	upstreamCount := flag.Int("upstreams", 4, "number of local SOCKS5 upstreams to seed")
	settle := flag.Duration("settle", 500*time.Millisecond, "time to wait before final gauge sampling")
	outputPath := flag.String("output", "", "optional JSON evidence output path")
	flag.Parse()

	if *concurrency <= 0 {
		log.Fatal("concurrency must be positive")
	}
	if *duration <= 0 {
		log.Fatal("duration must be positive")
	}
	if *rounds <= 0 {
		log.Fatal("rounds must be positive")
	}
	if *upstreamCount <= 0 {
		log.Fatal("upstreams must be positive")
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	upstreams := make([]*socks5Upstream, 0, *upstreamCount)
	for i := 0; i < *upstreamCount; i++ {
		upstream, err := startSOCKS5Upstream()
		if err != nil {
			log.Fatalf("start upstream: %v", err)
		}
		defer upstream.Close()
		upstreams = append(upstreams, upstream)
	}

	proxyListen, err := freeListenAddr()
	if err != nil {
		log.Fatalf("proxy addr: %v", err)
	}
	webListen, err := freeListenAddr()
	if err != nil {
		log.Fatalf("web addr: %v", err)
	}

	p := pool.New(1, *upstreamCount, 1_000_000_000, *upstreamCount, time.Hour, time.Hour, time.Second, 0.3, 3, 1, "")
	for _, upstream := range upstreams {
		p.Promote(observedEntry(upstream.Addr()))
	}
	allocator := allocation.New(p)
	srv := server.NewWithAllocator(server.Config{Listen: proxyListen, WebListen: webListen, RelayIdleTimeout: 5 * time.Second, MaxConnections: *concurrency * 4}, p, allocator)
	srv.SetStatsProvider(func() []proxycore.Provider { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := newControlPlaneManager(proxyListen, webListen)
	if err != nil {
		log.Fatalf("control-plane manager: %v", err)
	}
	srv.SetControlPlaneManager(manager)
	go func() {
		if err := srv.Run(ctx); err != nil {
			log.Printf("proxy server stopped: %v", err)
		}
	}()
	go func() {
		if err := srv.RunWeb(ctx); err != nil {
			log.Printf("web server stopped: %v", err)
		}
	}()
	if err := waitHTTP(fmt.Sprintf("http://%s/stats", webListen), 5*time.Second); err != nil {
		log.Fatalf("server did not start: %v", err)
	}
	log.SetOutput(io.Discard)

	summaries := make([]roundSummary, 0, *rounds)
	for round := 1; round <= *rounds; round++ {
		summary := runStressRound(round, *duration, *concurrency, *settle, proxyListen, webListen, target.URL)
		summaries = append(summaries, summary)
		if round < *rounds {
			time.Sleep(*pause)
		}
	}

	totalFailures := int64(0)
	totalAnomalies := 0
	for _, summary := range summaries {
		totalFailures += summary.Failures + summary.MonitorFailures
		totalAnomalies += len(summary.Anomalies)
	}
	upstreamAddrs := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstreamAddrs = append(upstreamAddrs, upstream.Addr())
	}
	data, err := json.MarshalIndent(map[string]any{
		"proxy_listen":    proxyListen,
		"web_listen":      webListen,
		"upstreams":       upstreamAddrs,
		"target":          target.URL,
		"duration":        duration.String(),
		"concurrency":     *concurrency,
		"rounds":          *rounds,
		"round_results":   summaries,
		"total_failures":  totalFailures,
		"total_anomalies": totalAnomalies,
	}, "", "  ")
	if err != nil {
		log.Fatalf("marshal result: %v", err)
	}
	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, append(data, '\n'), 0o644); err != nil {
			log.Fatalf("write output: %v", err)
		}
	}
	_, _ = os.Stdout.Write(data)
	_, _ = os.Stdout.Write([]byte("\n"))

	if totalFailures > 0 || totalAnomalies > 0 {
		os.Exit(1)
	}
}

func runStressRound(round int, duration time.Duration, concurrency int, settle time.Duration, proxyListen string, webListen string, targetURL string) roundSummary {
	var result stressResult
	failures := newFailureRecorder()
	deadline := time.Now().Add(duration)
	done := make(chan struct{})
	go monitorRuntimeStats(webListen, done, &result)
	var wg sync.WaitGroup
	for id := 0; id < concurrency; id++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				switch workerID % 3 {
				case 0:
					if err := doHTTPForward(proxyListen, targetURL); err != nil {
						failures.record(fmt.Errorf("http_forward: %w", err))
						result.Failures.Add(1)
					} else {
						result.HTTPForwardOK.Add(1)
					}
				case 1:
					if err := doHTTPConnect(proxyListen, mustURL(targetURL)); err != nil {
						failures.record(fmt.Errorf("http_connect: %w", err))
						result.Failures.Add(1)
					} else {
						result.HTTPConnectOK.Add(1)
					}
				default:
					if err := doSOCKSConnect(proxyListen, mustURL(targetURL)); err != nil {
						failures.record(fmt.Errorf("socks_connect: %w", err))
						result.Failures.Add(1)
					} else {
						result.SOCKSConnectOK.Add(1)
					}
				}
			}
		}(id)
	}
	wg.Wait()
	close(done)
	time.Sleep(settle)
	populateRuntimeStats(webListen, &result)
	failureKinds, failureSamples := failures.snapshot()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	summary := roundSummary{
		Round:             round,
		Duration:          duration.String(),
		Concurrency:       concurrency,
		HTTPForwardOK:     result.HTTPForwardOK.Load(),
		HTTPConnectOK:     result.HTTPConnectOK.Load(),
		SOCKSConnectOK:    result.SOCKSConnectOK.Load(),
		Failures:          result.Failures.Load(),
		MonitorFailures:   result.MonitorFailures.Load(),
		OverviewStatus:    result.OverviewStatusCode,
		PoolStatus:        result.PoolStatusCode,
		ActiveConnections: result.ActiveConnections,
		ActiveLeases:      result.ActiveLeases,
		PeakConnections:   atomic.LoadInt64(&result.PeakConnections),
		PeakLeases:        atomic.LoadInt64(&result.PeakLeases),
		Window30mRequests: result.Window30mRequests,
		PoolReady:         result.PoolReady,
		PoolActiveLeases:  result.PoolActiveLeases,
		Goroutines:        runtime.NumGoroutine(),
		AllocBytes:        mem.Alloc,
		FailureKinds:      failureKinds,
		FailureSamples:    failureSamples,
	}
	summary.Anomalies = detectAnomalies(summary)
	return summary
}

func monitorRuntimeStats(webListen string, done <-chan struct{}, result *stressResult) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			var overview struct {
				Overview struct {
					Server struct {
						ActiveConnections int64 `json:"active_connections"`
						ActiveLeases      int64 `json:"active_leases"`
					} `json:"server"`
				} `json:"overview"`
			}
			if getJSON("http://"+webListen+"/api/v1/overview", &overview) != http.StatusOK {
				result.MonitorFailures.Add(1)
				continue
			}
			updateMax(&result.PeakConnections, overview.Overview.Server.ActiveConnections)
			updateMax(&result.PeakLeases, overview.Overview.Server.ActiveLeases)
		}
	}
}

func classifyFailure(message string) string {
	lower := strings.ToLower(message)
	classifiers := []struct {
		name string
		find string
	}{
		{name: "timeout", find: "timeout"},
		{name: "connection_reset", find: "connection reset"},
		{name: "connection_refused", find: "connection refused"},
		{name: "bad_gateway", find: "bad gateway"},
		{name: "status_error", find: "status"},
		{name: "body_mismatch", find: "body"},
		{name: "unexpected_eof", find: "unexpected eof"},
		{name: "socks_error", find: "socks"},
		{name: "read_error", find: "read"},
		{name: "write_error", find: "write"},
	}
	for _, classifier := range classifiers {
		if strings.Contains(lower, classifier.find) {
			return classifier.name
		}
	}
	return "other"
}

func detectAnomalies(summary roundSummary) []string {
	var anomalies []string
	if summary.Failures > 0 {
		anomalies = append(anomalies, fmt.Sprintf("request failures=%d", summary.Failures))
	}
	if summary.MonitorFailures > 0 {
		anomalies = append(anomalies, fmt.Sprintf("monitor failures=%d", summary.MonitorFailures))
	}
	if summary.OverviewStatus != http.StatusOK {
		anomalies = append(anomalies, fmt.Sprintf("overview status=%d", summary.OverviewStatus))
	}
	if summary.PoolStatus != http.StatusOK {
		anomalies = append(anomalies, fmt.Sprintf("pool status=%d", summary.PoolStatus))
	}
	if summary.ActiveConnections != 0 {
		anomalies = append(anomalies, fmt.Sprintf("active connections did not return to zero: %d", summary.ActiveConnections))
	}
	if summary.ActiveLeases != 0 {
		anomalies = append(anomalies, fmt.Sprintf("active leases did not return to zero: %d", summary.ActiveLeases))
	}
	if summary.PoolActiveLeases != 0 {
		anomalies = append(anomalies, fmt.Sprintf("pool active leases did not return to zero: %d", summary.PoolActiveLeases))
	}
	if summary.PoolReady == 0 {
		anomalies = append(anomalies, "ready pool is empty")
	}
	if summary.Window30mRequests < summary.HTTPForwardOK+summary.HTTPConnectOK+summary.SOCKSConnectOK {
		anomalies = append(anomalies, fmt.Sprintf("request window below successful requests: window=%d success=%d", summary.Window30mRequests, summary.HTTPForwardOK+summary.HTTPConnectOK+summary.SOCKSConnectOK))
	}
	sort.Strings(anomalies)
	return anomalies
}

func updateMax(target *int64, value int64) {
	for {
		current := atomic.LoadInt64(target)
		if value <= current {
			return
		}
		if atomic.CompareAndSwapInt64(target, current, value) {
			return
		}
	}
}

func newControlPlaneManager(proxyListen string, webListen string) (*controlplane.Manager, error) {
	proxyHost, proxyPortText, err := net.SplitHostPort(proxyListen)
	if err != nil {
		return nil, err
	}
	webHost, webPortText, err := net.SplitHostPort(webListen)
	if err != nil {
		return nil, err
	}
	proxyPort, err := strconv.Atoi(proxyPortText)
	if err != nil {
		return nil, err
	}
	webPort, err := strconv.Atoi(webPortText)
	if err != nil {
		return nil, err
	}
	baseConfig := fmt.Sprintf(`{
  "server": {"host": %q, "port": %d, "web_host": %q, "web_port": %d, "relay_idle_timeout": "5s", "max_connections": %d},
  "pool": {"min_ready": 1, "max_ready": 5, "max_use": 1000000000, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 3, "check_interval_active": "30s", "check_interval_idle": "5m"},
  "validator": {"target_host": "127.0.0.1", "target_port": 80, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
  "log": {"prefix": "[stress]", "level": "info", "file": "", "file_enable": false},
  "policy": {"mode": "balanced", "random_subset_size": 3, "stable_subset_size": 3},
  "control_plane": {"trusted_local_only": true},
  "sources": {"enabled": {}}
}`+"\n", proxyHost, proxyPort, webHost, webPort, 200)
	dir, err := os.MkdirTemp("", "randproxy-stress-")
	if err != nil {
		return nil, err
	}
	path := dir + "/config.jsonc"
	if err := os.WriteFile(path, []byte(baseConfig), 0o644); err != nil {
		return nil, err
	}
	return controlplane.NewManager(path, controlplane.ManagerOptions{})
}

type socks5Upstream struct {
	listener net.Listener
}

func (s *socks5Upstream) Addr() string { return s.listener.Addr().String() }
func (s *socks5Upstream) Close()       { _ = s.listener.Close() }

func startSOCKS5Upstream() (*socks5Upstream, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	upstream := &socks5Upstream{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5UpstreamConn(conn)
		}
	}()
	return upstream, nil
}

func handleSOCKS5UpstreamConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if err := readClientGreeting(reader, conn); err != nil {
		return
	}
	host, port, err := readConnectRequest(reader)
	if err != nil {
		_ = writeSOCKS5Failure(conn)
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		_ = writeSOCKS5Failure(conn)
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	relay(conn, target)
}

func readClientGreeting(reader *bufio.Reader, conn net.Conn) error {
	ver, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if ver != 0x05 {
		return errors.New("not socks5")
	}
	nMethods, err := reader.ReadByte()
	if err != nil {
		return err
	}
	methods := make([]byte, int(nMethods))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return err
	}
	_, err = conn.Write([]byte{0x05, 0x00})
	return err
}

func readConnectRequest(reader *bufio.Reader) (string, int, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, err
	}
	if header[0] != 0x05 || header[1] != 0x01 {
		return "", 0, errors.New("unsupported command")
	}
	var host string
	switch header[3] {
	case 0x01:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", 0, err
		}
		host = net.IP(addr).String()
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil {
			return "", 0, err
		}
		addr := make([]byte, int(length))
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", 0, err
		}
		host = string(addr)
	default:
		return "", 0, errors.New("unsupported address")
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(reader, portBytes[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(portBytes[:])), nil
}

func writeSOCKS5Failure(conn net.Conn) error {
	_, err := conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func relay(left, right net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(left, right)
		_ = left.SetDeadline(time.Now())
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(right, left)
		_ = right.SetDeadline(time.Now())
	}()
	wg.Wait()
}

func observedEntry(addr string) *pool.Entry {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return &pool.Entry{
		Proxy:           &proxycore.Proxy{IP: host, Port: port, Protocol: proxycore.ProtocolSOCKS5, Source: "stress-local"},
		Status:          pool.StatusBuffer,
		MaxUse:          1_000_000_000,
		AddedAt:         time.Now(),
		LatencyEWMA:     10 * time.Millisecond,
		LatencyVariance: time.Millisecond,
		LatencyCount:    10,
	}
}

func freeListenAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

func waitHTTP(rawURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := jsonClient.Get(rawURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", rawURL)
}

func doHTTPForward(proxyAddr string, targetURL string) error {
	transport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		return url.Parse("http://" + proxyAddr)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http forward status %d", resp.StatusCode)
	}
	if err := expectOKBody(resp.Body); err != nil {
		return fmt.Errorf("http forward body: %w", err)
	}
	return nil
}

func doHTTPConnect(proxyAddr string, target *url.URL) error {
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Host, target.Host)
	if _, err := conn.Write([]byte(request)); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("connect status %d", resp.StatusCode)
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", pathWithQuery(target), target.Host)
	if _, err := conn.Write([]byte(req)); err != nil {
		return err
	}
	getResp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		return fmt.Errorf("connect get status %d", getResp.StatusCode)
	}
	if err := expectOKBody(getResp.Body); err != nil {
		return fmt.Errorf("connect get body: %w", err)
	}
	return nil
}

func doSOCKSConnect(proxyAddr string, target *url.URL) error {
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	conn, err := dialer.Dial("tcp", target.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", pathWithQuery(target), target.Host)
	if _, err := conn.Write([]byte(request)); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("socks get status %d", resp.StatusCode)
	}
	if err := expectOKBody(resp.Body); err != nil {
		return fmt.Errorf("socks get body: %w", err)
	}
	return nil
}

func expectOKBody(body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if string(data) != "ok" {
		return fmt.Errorf("got %q, want %q", string(data), "ok")
	}
	return nil
}

func mustURL(rawURL string) *url.URL {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return parsed
}

func pathWithQuery(parsed *url.URL) string {
	path := parsed.RequestURI()
	if path == "" {
		return "/"
	}
	return path
}

func populateRuntimeStats(webListen string, result *stressResult) {
	var overview struct {
		Overview struct {
			Server struct {
				ActiveConnections int64 `json:"active_connections"`
				ActiveLeases      int64 `json:"active_leases"`
				RequestWindow30m  struct {
					TotalRequests int64 `json:"total_requests"`
				} `json:"request_window_30m"`
			} `json:"server"`
		} `json:"overview"`
	}
	result.OverviewStatusCode = getJSON("http://"+webListen+"/api/v1/overview", &overview)
	result.ActiveConnections = overview.Overview.Server.ActiveConnections
	result.ActiveLeases = overview.Overview.Server.ActiveLeases
	result.Window30mRequests = overview.Overview.Server.RequestWindow30m.TotalRequests

	var inventory struct {
		Ready []struct {
			ActiveLeases int `json:"active_leases"`
		} `json:"ready"`
	}
	result.PoolStatusCode = getJSON("http://"+webListen+"/api/v1/pool", &inventory)
	result.PoolReady = len(inventory.Ready)
	for _, entry := range inventory.Ready {
		result.PoolActiveLeases += entry.ActiveLeases
	}
}

func getJSON(rawURL string, target any) int {
	resp, err := jsonClient.Get(rawURL)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return -resp.StatusCode
	}
	return resp.StatusCode
}
