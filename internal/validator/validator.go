package validator

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	socks5 "golang.org/x/net/proxy"

	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/proxy"
)

const (
	defaultTarget  = "api.literouter.com"
	defaultPort    = 443
	defaultTimeout = 6 * time.Second
	defaultWorkers = 5
)

type Target struct {
	Host string
	Port int
}

// Config for the proxy validator.
type Config struct {
	TargetHost  string
	TargetPort  int
	Targets     []Target
	Timeout     time.Duration
	Concurrency int
	TLSInsecure bool
}

// Validator takes proxies from buffer, tests SOCKS5 connectivity, promotes to ready.
type Validator struct {
	pool    *pool.Pool
	cfg     Config
	targets []Target

	mu     sync.Mutex
	vstats map[string]*ValStat // source name → stats
}

// ValStat tracks per-source validation metrics.
type ValStat struct {
	Name   string `json:"name"`
	Passed int64  `json:"validated"`
	Failed int64  `json:"validation_failed"`
}

func (v *ValStat) PassRate() float64 {
	total := v.Passed + v.Failed
	if total == 0 {
		return 0
	}
	return float64(v.Passed) / float64(total) * 100
}

func New(p *pool.Pool, cfg Config) *Validator {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = defaultWorkers
	}

	// Build targets list: prefer explicit Targets, fallback to TargetHost/TargetPort
	var targets []Target
	for _, t := range cfg.Targets {
		if t.Host != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		host := cfg.TargetHost
		if host == "" {
			host = defaultTarget
		}
		port := cfg.TargetPort
		if port == 0 {
			port = defaultPort
		}
		targets = []Target{{Host: host, Port: port}}
	}

	return &Validator{pool: p, cfg: cfg, targets: targets, vstats: make(map[string]*ValStat)}
}

// SourceStats returns a copy of all validation stats.
func (v *Validator) SourceStats() []ValStat {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]ValStat, 0, len(v.vstats))
	for _, s := range v.vstats {
		out = append(out, *s)
	}
	return out
}

// Run starts worker goroutines. Blocks until ctx is cancelled.
func (v *Validator) Run(ctx context.Context) {
	log.Printf("[INFO] [validator] starting %d workers (target=%s:%d, timeout=%v)",
		v.cfg.Concurrency, v.cfg.TargetHost, v.cfg.TargetPort, v.cfg.Timeout)

	var wg sync.WaitGroup
	for i := range v.cfg.Concurrency {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			v.worker(ctx, id)
		}(i)
	}
	wg.Wait()
	log.Printf("[INFO] [validator] stopped")
}

func (v *Validator) worker(ctx context.Context, id int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ERROR] [validator w%d] panic: %v", id, r)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !v.pool.NeedRefill() {
			select {
			case <-ctx.Done():
				return
			case <-v.pool.NotifyCh():
			case <-time.After(2 * time.Second):
			}
			continue
		}

		e := v.pool.NextBuffer()
		if e == nil {
			select {
			case <-ctx.Done():
				return
			case <-v.pool.NotifyCh():
			case <-time.After(1 * time.Second):
			}
			continue
		}

		if e.Proxy.Protocol != proxy.ProtocolSOCKS5 {
			v.pool.Forget(e.Proxy)
			continue
		}

		if v.testSOCKS5(ctx, e.Proxy) {
			v.pool.Promote(e)
			v.recordStat(e.Proxy.Source, true)
			log.Printf("[INFO] [validator w%d] ✅ %s promoted to ready", id, e.Proxy.Addr())
		} else {
			v.recordStat(e.Proxy.Source, false)
			log.Printf("[DEBUG] [validator w%d] ❌ %s discarded", id, e.Proxy.Addr())
		}
	}
}

// testSOCKS5 tests a proxy by establishing a SOCKS5 tunnel to a random target,
// performing a TLS handshake, and sending a GET request.
func (v *Validator) testSOCKS5(ctx context.Context, pr *proxy.Proxy) bool {
	// SSRF protection: reject private/link-local IPs
	ip := net.ParseIP(pr.IP)
	if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		log.Printf("[DEBUG] [validator] %s internal IP, skipped", pr.Addr())
		return false
	}

	t := v.targets[rand.Intn(len(v.targets))]
	addr := fmt.Sprintf("%s:%d", t.Host, t.Port)

	dialer, err := socks5.SOCKS5("tcp", pr.Addr(), nil, &net.Dialer{Timeout: v.cfg.Timeout})
	if err != nil {
		log.Printf("[DEBUG] [validator] %s SOCKS5 dialer error: %v", pr.Addr(), err)
		return false
	}

	cd, ok := dialer.(socks5.ContextDialer)
	if !ok {
		log.Printf("[DEBUG] [validator] %s context dialer not supported", pr.Addr())
		return false
	}
	conn, err := cd.DialContext(ctx, "tcp", addr)
	if err != nil {
		log.Printf("[DEBUG] [validator] %s dial error: %v", pr.Addr(), err)
		return false
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         t.Host,
		InsecureSkipVerify: v.cfg.TLSInsecure,
	})

	if err := tlsConn.SetDeadline(time.Now().Add(v.cfg.Timeout)); err != nil {
		log.Printf("[DEBUG] [validator] %s SetDeadline: %v", pr.Addr(), err)
		return false
	}

	if err := tlsConn.Handshake(); err != nil {
		log.Printf("[DEBUG] [validator] %s TLS handshake: %v", pr.Addr(), err)
		return false
	}

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", t.Host)
	if _, err := tlsConn.Write([]byte(req)); err != nil {
		log.Printf("[DEBUG] [validator] %s write: %v", pr.Addr(), err)
		return false
	}

	buf := make([]byte, 256)
	n, err := tlsConn.Read(buf)
	if err != nil {
		log.Printf("[DEBUG] [validator] %s read: %v", pr.Addr(), err)
		return false
	}

	resp := string(buf[:n])
	parts := strings.SplitN(resp, " ", 3)
	if len(parts) < 2 {
		log.Printf("[DEBUG] [validator] %s invalid response: %q", pr.Addr(), resp[:min(len(resp), 40)])
		return false
	}
	statusCode := 0
	fmt.Sscanf(parts[1], "%d", &statusCode)
	ok = statusCode >= 200 && statusCode < 400
	log.Printf("[DEBUG] [validator] %s response: status=%d ok=%v", pr.Addr(), statusCode, ok)
	return ok
}

// TestEntry runs full SOCKS5 validation on an existing pool entry.
func (v *Validator) TestEntry(e *pool.Entry) bool {
	return v.testSOCKS5(context.Background(), e.Proxy)
}

func (v *Validator) recordStat(source string, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.vstats[source]
	if s == nil {
		s = &ValStat{Name: source}
		v.vstats[source] = s
	}
	if ok {
		s.Passed++
	} else {
		s.Failed++
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
