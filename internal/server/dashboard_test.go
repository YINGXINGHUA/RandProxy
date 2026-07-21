package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/proxy"
)

func newTestServer(t *testing.T) *ProxyServer {
	t.Helper()
	p := pool.New(1, 2, 2, 10, 0, 0, 0, 0.3, 1, 1, "")
	s := New(Config{Listen: ":0", WebListen: ":0"}, p)
	s.SetStatsProvider(func() []proxy.Provider { return nil })
	return s
}

func TestWebHandlerServesDashboardRoutes_when_requested(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.webHandler())
	defer ts.Close()

	tests := []struct {
		name string
		path string
	}{
		{name: "root route", path: "/"},
		{name: "dashboard route", path: "/dashboard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			url := ts.URL + tt.path

			// When
			resp, err := http.Get(url)
			if err != nil {
				t.Fatalf("get %s: %v", tt.path, err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %s body: %v", tt.path, err)
			}

			// Then
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d", tt.path, resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("%s content-type = %q", tt.path, got)
			}
			if len(body) == 0 {
				t.Fatalf("%s body is empty", tt.path)
			}
		})
	}
}

func TestWebHandlerServesStatsTopLevelShape_when_requested(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.webHandler())
	defer ts.Close()

	// When
	statsResp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer statsResp.Body.Close()

	// Then
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", statsResp.StatusCode)
	}
	if got := statsResp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("stats content-type = %q", got)
	}

	var body map[string]any
	if err := json.NewDecoder(statsResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	requiredKeys := []string{"pool", "server", "sources", "ready_history", "recent_requests"}
	for _, key := range requiredKeys {
		if _, ok := body[key]; !ok {
			t.Fatalf("stats response missing %q field: %#v", key, body)
		}
	}
}

func TestStatsHandlerAddsRuntimeServerFields_whenRequested(t *testing.T) {
	s := newTestServer(t)
	entry := promoteObservedTestProxy(s.pool, "203.0.113.10:1080", 10)
	lease, err := s.allocator.Acquire(context.Background(), allocation.AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	defer lease.Finish(allocation.Result{Success: false})

	s.stats.connectionOpened()
	defer s.stats.connectionClosed()
	s.stats.record(100*time.Millisecond, true)
	s.stats.record(300*time.Millisecond, false)

	ts := httptest.NewServer(s.webHandler())
	defer ts.Close()

	// When
	statsResp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer statsResp.Body.Close()

	// Then
	var body struct {
		Server struct {
			ActiveConnections int `json:"active_connections"`
			ActiveLeases      int `json:"active_leases"`
			RequestWindow1m   struct {
				TotalReqs    int     `json:"total_requests"`
				SuccessReqs  int     `json:"success_requests"`
				FailReqs     int     `json:"fail_requests"`
				AvgLatencyMs float64 `json:"avg_latency_ms"`
			} `json:"request_window_1m"`
			RequestWindow5m struct {
				TotalReqs    int     `json:"total_requests"`
				SuccessReqs  int     `json:"success_requests"`
				FailReqs     int     `json:"fail_requests"`
				AvgLatencyMs float64 `json:"avg_latency_ms"`
			} `json:"request_window_5m"`
			RequestWindow30m struct {
				TotalReqs int `json:"total_requests"`
			} `json:"request_window_30m"`
			RequestWindow24h struct {
				TotalReqs int `json:"total_requests"`
			} `json:"request_window_24h"`
			RequestWindow3d struct {
				TotalReqs int `json:"total_requests"`
			} `json:"request_window_3d"`
		} `json:"server"`
	}
	if err := json.NewDecoder(statsResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	if body.Server.ActiveConnections != 1 {
		t.Fatalf("active_connections = %d, want 1", body.Server.ActiveConnections)
	}
	if body.Server.ActiveLeases != 1 {
		t.Fatalf("active_leases = %d, want 1", body.Server.ActiveLeases)
	}
	if body.Server.RequestWindow1m.TotalReqs != 2 || body.Server.RequestWindow1m.SuccessReqs != 1 || body.Server.RequestWindow1m.FailReqs != 1 {
		t.Fatalf("request_window_1m = %#v", body.Server.RequestWindow1m)
	}
	if body.Server.RequestWindow1m.AvgLatencyMs != 200 {
		t.Fatalf("request_window_1m avg_latency_ms = %v, want 200", body.Server.RequestWindow1m.AvgLatencyMs)
	}
	if body.Server.RequestWindow5m.TotalReqs != 2 || body.Server.RequestWindow5m.SuccessReqs != 1 || body.Server.RequestWindow5m.FailReqs != 1 {
		t.Fatalf("request_window_5m = %#v", body.Server.RequestWindow5m)
	}
	if body.Server.RequestWindow30m.TotalReqs != 2 || body.Server.RequestWindow24h.TotalReqs != 2 || body.Server.RequestWindow3d.TotalReqs != 2 {
		t.Fatalf("long request windows = 30m:%#v 24h:%#v 3d:%#v", body.Server.RequestWindow30m, body.Server.RequestWindow24h, body.Server.RequestWindow3d)
	}
	if entry.Proxy.Addr() != lease.UpstreamAddr() {
		t.Fatalf("test lease upstream = %q, want %q", lease.UpstreamAddr(), entry.Proxy.Addr())
	}
}

func TestStatsCollectDropsStaleRequestWindowBuckets_whenIdle(t *testing.T) {
	s := newTestServer(t)
	oldSecond := time.Now().Add(-10 * time.Minute).Unix()
	idx := int(oldSecond % requestSecondWindowBucketSize)

	s.stats.mu.Lock()
	s.stats.requestSecondBuckets[idx] = requestWindowBucket{slot: oldSecond, total: 1, success: 1, latencyMs: 100}
	s.stats.mu.Unlock()

	// When
	var body struct {
		Server struct {
			RequestWindow5m struct {
				TotalReqs int `json:"total_requests"`
			} `json:"request_window_5m"`
		} `json:"server"`
	}
	if err := json.Unmarshal(s.stats.collect(s.pool, 0), &body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	// Then
	if body.Server.RequestWindow5m.TotalReqs != 0 {
		t.Fatalf("request_window_5m total_requests = %d, want 0", body.Server.RequestWindow5m.TotalReqs)
	}
}

func TestStatsCollectProductWindowsIncludeAndExcludeBoundedBuckets_whenIdle(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().Truncate(time.Minute)
	s.stats.recordWindow(now.Add(-29*time.Minute), 100, true)
	s.stats.recordWindow(now.Add(-31*time.Minute), 200, false)
	s.stats.recordWindow(now.Add(-71*time.Hour), 300, true)
	s.stats.recordWindow(now.Add(-73*time.Hour), 400, false)

	// When
	var body struct {
		Server struct {
			RequestWindow30m struct {
				TotalReqs    int     `json:"total_requests"`
				SuccessReqs  int     `json:"success_requests"`
				FailReqs     int     `json:"fail_requests"`
				AvgLatencyMs float64 `json:"avg_latency_ms"`
			} `json:"request_window_30m"`
			RequestWindow3d struct {
				TotalReqs    int     `json:"total_requests"`
				SuccessReqs  int     `json:"success_requests"`
				FailReqs     int     `json:"fail_requests"`
				AvgLatencyMs float64 `json:"avg_latency_ms"`
			} `json:"request_window_3d"`
		} `json:"server"`
	}
	if err := json.Unmarshal(s.stats.collect(s.pool, 0), &body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	// Then
	if body.Server.RequestWindow30m.TotalReqs != 1 || body.Server.RequestWindow30m.SuccessReqs != 1 || body.Server.RequestWindow30m.FailReqs != 0 {
		t.Fatalf("request_window_30m = %#v", body.Server.RequestWindow30m)
	}
	if body.Server.RequestWindow30m.AvgLatencyMs != 100 {
		t.Fatalf("request_window_30m avg_latency_ms = %v, want 100", body.Server.RequestWindow30m.AvgLatencyMs)
	}
	if body.Server.RequestWindow3d.TotalReqs != 3 || body.Server.RequestWindow3d.SuccessReqs != 2 || body.Server.RequestWindow3d.FailReqs != 1 {
		t.Fatalf("request_window_3d = %#v", body.Server.RequestWindow3d)
	}
	if body.Server.RequestWindow3d.AvgLatencyMs != 200 {
		t.Fatalf("request_window_3d avg_latency_ms = %v, want 200", body.Server.RequestWindow3d.AvgLatencyMs)
	}
}

func TestStatsRecentRequestsIncludeTimestampAndTime_whenRecorded(t *testing.T) {
	s := newTestServer(t)
	s.stats.recordRequest("api.literouter.com:443", "203.0.113.10:1080", 42, true)

	// When
	var body struct {
		RecentReqs []struct {
			Timestamp string `json:"timestamp"`
			Time      string `json:"time"`
		} `json:"recent_requests"`
	}
	if err := json.Unmarshal(s.stats.collect(s.pool, 0), &body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	// Then
	if len(body.RecentReqs) != 1 {
		t.Fatalf("recent_requests len = %d, want 1", len(body.RecentReqs))
	}
	if body.RecentReqs[0].Time == "" {
		t.Fatal("recent request time is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, body.RecentReqs[0].Timestamp); err != nil {
		t.Fatalf("recent request timestamp %q is not RFC3339Nano: %v", body.RecentReqs[0].Timestamp, err)
	}
}
