package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/randproxy/internal/fetcher"
	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/proxy"
	"github.com/user/randproxy/internal/validator"
)

//go:embed dashboard.html
var dashboardHTML string

const (
	maxRecentReqs                 = 50
	requestSecondWindowBucketSize = 30*60 + 1
	requestMinuteWindowBucketSize = 3*24*60 + 1
)

type RequestRecord struct {
	Timestamp string `json:"timestamp"`
	Time      string `json:"time"`
	Target    string `json:"target"`
	ProxyIP   string `json:"proxy_ip"`
	LatencyMs int64  `json:"latency_ms"`
	Success   bool   `json:"success"`
}

type requestWindowBucket struct {
	slot      int64
	total     int64
	success   int64
	fail      int64
	latencyMs int64
}

type Stats struct {
	mu             sync.RWMutex
	totalReqs      atomic.Int64
	successReqs    atomic.Int64
	failReqs       atomic.Int64
	totalLatencyMs atomic.Int64
	activeConns    atomic.Int64
	startTime      time.Time

	readyHistory []float64
	lastSample   time.Time
	providersFn  func() []proxy.Provider

	recentReqs           []RequestRecord
	reqNext              int
	requestSecondBuckets [requestSecondWindowBucketSize]requestWindowBucket
	requestMinuteBuckets [requestMinuteWindowBucketSize]requestWindowBucket

	fetchFn func() []fetcher.FetchStat
	validFn func() []validator.ValStat
}

func newStats(providersFn func() []proxy.Provider) *Stats {
	return &Stats{
		readyHistory: make([]float64, 0, 60),
		lastSample:   time.Now(),
		startTime:    time.Now(),
		providersFn:  providersFn,
		recentReqs:   make([]RequestRecord, maxRecentReqs),
	}
}

func (s *Stats) SetFetchStats(fn func() []fetcher.FetchStat) { s.fetchFn = fn }
func (s *Stats) SetValidStats(fn func() []validator.ValStat) { s.validFn = fn }

func (s *Stats) record(dur time.Duration, success bool) {
	latencyMs := dur.Milliseconds()
	s.totalReqs.Add(1)
	s.totalLatencyMs.Add(latencyMs)
	if success {
		s.successReqs.Add(1)
	} else {
		s.failReqs.Add(1)
	}
	s.recordWindow(time.Now(), latencyMs, success)
}

func (s *Stats) connectionOpened() { s.activeConns.Add(1) }
func (s *Stats) connectionClosed() { s.activeConns.Add(-1) }

func (s *Stats) recordWindow(now time.Time, latencyMs int64, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	recordRequestBucketLocked(s.requestSecondBuckets[:], now.Unix(), latencyMs, success)
	recordRequestBucketLocked(s.requestMinuteBuckets[:], now.Unix()/60, latencyMs, success)
}

func recordRequestBucketLocked(buckets []requestWindowBucket, slot int64, latencyMs int64, success bool) {
	idx := int(slot % int64(len(buckets)))
	bucket := &buckets[idx]
	if bucket.slot != slot {
		*bucket = requestWindowBucket{slot: slot}
	}
	bucket.total++
	bucket.latencyMs += latencyMs
	if success {
		bucket.success++
	} else {
		bucket.fail++
	}
}

func (s *Stats) recordRequest(target, proxyIP string, latencyMs int64, success bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recentReqs[s.reqNext] = RequestRecord{
		Timestamp: now.UTC().Format(time.RFC3339Nano),
		Time:      now.Format("15:04:05"),
		Target:    target,
		ProxyIP:   proxyIP,
		LatencyMs: latencyMs,
		Success:   success,
	}
	s.reqNext = (s.reqNext + 1) % maxRecentReqs
}

func (s *Stats) sampleReady(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastSample) < 30*time.Second {
		return
	}
	s.lastSample = time.Now()
	s.readyHistory = append(s.readyHistory, float64(n))
	if len(s.readyHistory) > 60 {
		s.readyHistory = s.readyHistory[1:]
	}
}

type sourceStats struct {
	Name   string `json:"name"`
	Status string `json:"status"`

	// Fetch stats
	Fetched   int64  `json:"total_fetched"`
	Errors    int64  `json:"fetch_errors"`
	LastFetch string `json:"last_fetch"`
	LastError string `json:"last_error"`

	// Validation stats
	Passed   int64   `json:"validated"`
	Failed   int64   `json:"validation_failed"`
	PassRate float64 `json:"pass_rate"`

	// Pool
	InReady int `json:"in_ready"`
}

type statsResponse struct {
	Pool         poolStats       `json:"pool"`
	Server       serverStats     `json:"server"`
	Source       []sourceStats   `json:"sources"`
	ReadyHistory []float64       `json:"ready_history"`
	RecentReqs   []RequestRecord `json:"recent_requests"`
}

type poolStats struct {
	Buffer    int `json:"buffer"`
	Ready     int `json:"ready"`
	Blacklist int `json:"blacklist"`
}

type serverStats struct {
	TotalReqs         int64              `json:"total_requests"`
	SuccessReqs       int64              `json:"success_requests"`
	FailReqs          int64              `json:"fail_requests"`
	AvgLatencyMs      float64            `json:"avg_latency_ms"`
	UptimeSeconds     int                `json:"uptime_seconds"`
	ActiveConnections int64              `json:"active_connections"`
	ActiveLeases      int                `json:"active_leases"`
	RequestWindow1m   requestWindowStats `json:"request_window_1m"`
	RequestWindow5m   requestWindowStats `json:"request_window_5m"`
	RequestWindow30m  requestWindowStats `json:"request_window_30m"`
	RequestWindow24h  requestWindowStats `json:"request_window_24h"`
	RequestWindow3d   requestWindowStats `json:"request_window_3d"`
}

type requestWindowStats struct {
	TotalReqs    int64   `json:"total_requests"`
	SuccessReqs  int64   `json:"success_requests"`
	FailReqs     int64   `json:"fail_requests"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

func (s *Stats) collect(p *pool.Pool, activeLeases int) []byte {
	ready := p.ReadyCount()
	s.sampleReady(ready)

	total := s.totalReqs.Load()
	success := s.successReqs.Load()
	fail := s.failReqs.Load()
	totalLat := s.totalLatencyMs.Load()

	var avgLat float64
	if total > 0 {
		avgLat = float64(totalLat) / float64(total)
		avgLat = math.Round(avgLat*10) / 10
	}

	// Collect source info from providers + fetch stats + validation stats + pool
	fetchMap := make(map[string]fetcher.FetchStat)
	if s.fetchFn != nil {
		for _, fs := range s.fetchFn() {
			fetchMap[fs.Name] = fs
		}
	}
	validMap := make(map[string]validator.ValStat)
	if s.validFn != nil {
		for _, vs := range s.validFn() {
			validMap[vs.Name] = vs
		}
	}
	readyBySrc := p.ReadyBySource()

	srcs := make([]sourceStats, 0)
	if s.providersFn != nil {
		for _, np := range s.providersFn() {
			st := "unknown"
			switch np.Status() {
			case proxy.StatusOnline:
				st = "online"
			case proxy.StatusOffline:
				st = "offline"
			}
			name := np.Name()
			ss := sourceStats{Name: name, Status: st}
			if fs, ok := fetchMap[name]; ok {
				ss.Fetched = fs.Fetched
				ss.Errors = fs.Errors
				ss.LastFetch = fs.LastFetch
				ss.LastError = fs.LastError
			}
			if vs, ok := validMap[name]; ok {
				ss.Passed = vs.Passed
				ss.Failed = vs.Failed
				t := vs.Passed + vs.Failed
				if t > 0 {
					ss.PassRate = math.Round(float64(vs.Passed)/float64(t)*1000) / 10
				}
			}
			ss.InReady = readyBySrc[name]
			srcs = append(srcs, ss)
		}
	}

	now := time.Now()
	s.mu.RLock()
	hist := make([]float64, len(s.readyHistory))
	copy(hist, s.readyHistory)
	reqs := make([]RequestRecord, 0, maxRecentReqs)
	for i := 0; i < maxRecentReqs; i++ {
		idx := (s.reqNext + i) % maxRecentReqs
		if s.recentReqs[idx].Time != "" {
			reqs = append(reqs, s.recentReqs[idx])
		}
	}
	window1m := s.requestSecondWindowStatsLocked(now, time.Minute)
	window5m := s.requestSecondWindowStatsLocked(now, 5*time.Minute)
	window30m := s.requestSecondWindowStatsLocked(now, 30*time.Minute)
	window24h := s.requestMinuteWindowStatsLocked(now, 24*time.Hour)
	window3d := s.requestMinuteWindowStatsLocked(now, 3*24*time.Hour)
	s.mu.RUnlock()

	resp := statsResponse{
		Pool: poolStats{
			Buffer:    p.BufferCount(),
			Ready:     ready,
			Blacklist: p.BlacklistCount(),
		},
		Server: serverStats{
			TotalReqs:         total,
			SuccessReqs:       success,
			FailReqs:          fail,
			AvgLatencyMs:      avgLat,
			UptimeSeconds:     int(time.Since(s.startTime).Seconds()),
			ActiveConnections: s.activeConns.Load(),
			ActiveLeases:      activeLeases,
			RequestWindow1m:   window1m,
			RequestWindow5m:   window5m,
			RequestWindow30m:  window30m,
			RequestWindow24h:  window24h,
			RequestWindow3d:   window3d,
		},
		Source:       srcs,
		ReadyHistory: hist,
		RecentReqs:   reqs,
	}

	data, _ := json.Marshal(resp)
	return data
}

func (s *Stats) requestSecondWindowStatsLocked(now time.Time, window time.Duration) requestWindowStats {
	return requestWindowStatsFromBucketsLocked(s.requestSecondBuckets[:], now.Unix(), int64(window/time.Second))
}

func (s *Stats) requestMinuteWindowStatsLocked(now time.Time, window time.Duration) requestWindowStats {
	return requestWindowStatsFromBucketsLocked(s.requestMinuteBuckets[:], now.Unix()/60, int64(window/time.Minute))
}

func requestWindowStatsFromBucketsLocked(buckets []requestWindowBucket, currentSlot int64, windowSlots int64) requestWindowStats {
	cutoff := currentSlot - windowSlots
	var stats requestWindowStats
	var totalLatency int64
	for idx := range buckets {
		bucket := buckets[idx]
		if bucket.slot <= cutoff {
			continue
		}
		stats.TotalReqs += bucket.total
		stats.SuccessReqs += bucket.success
		stats.FailReqs += bucket.fail
		totalLatency += bucket.latencyMs
	}
	if stats.TotalReqs > 0 {
		stats.AvgLatencyMs = math.Round(float64(totalLatency)/float64(stats.TotalReqs)*10) / 10
	}
	return stats
}

// --- HTTP handlers ---

func (s *ProxyServer) statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.stats == nil {
		w.Write([]byte("{}"))
		return
	}
	w.Write(s.stats.collect(s.pool, s.activeLeaseSnapshot().Total))
}

func (s *ProxyServer) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func (s *ProxyServer) webHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/overview", s.overviewHandler)
	mux.HandleFunc("/api/v1/config", s.configHandler)
	mux.HandleFunc("/api/v1/pool", s.poolHandler)
	mux.HandleFunc("/api/v1/pool/proxies/", s.poolProxyActionHandler)
	mux.HandleFunc("/api/v1/sources/", s.sourceMutationHandler)
	mux.HandleFunc("/stats", s.statsHandler)
	mux.HandleFunc("/dashboard", s.dashboardHandler)
	mux.HandleFunc("/", s.dashboardHandler)
	return mux
}

func (s *ProxyServer) RunWeb(ctx context.Context) error {
	if s.cfg.WebListen == "" {
		return nil
	}
	listener, err := net.Listen("tcp", s.cfg.WebListen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.WebListen, err)
	}
	log.Printf("[INFO] [server] web listening on %s", s.cfg.WebListen)

	httpServer := &http.Server{Handler: s.webHandler()}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	err = httpServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
