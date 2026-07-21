package server

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/pool"
)

func TestHTTPConnectReturnsServiceUnavailableWhenNoProxyAvailable(t *testing.T) {
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	req := &http.Request{Method: http.MethodConnect, Host: "example.com:443", Header: make(http.Header)}

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleHTTPConnect(serverConn, bufio.NewReader(strings.NewReader("")), req)
	}, func(clientConn net.Conn) {
		resp := readHTTPProxyResponse(t, clientConn, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
		}
	})
}

func TestHTTPConnectRecordsFailureWhenUpstreamConnectFails(t *testing.T) {
	upstream := startFakeUpstreamTCPProxy(t, 0x04)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 5)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	req := &http.Request{Method: http.MethodConnect, Host: "example.com:443", Header: make(http.Header)}

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleHTTPConnect(serverConn, bufio.NewReader(strings.NewReader("")), req)
	}, func(clientConn net.Conn) {
		resp := readHTTPProxyResponse(t, clientConn, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
	})

	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
}

func TestHTTPConnectUsesAllocatorLeaseWhenServerPoolIsEmpty(t *testing.T) {
	upstream := startFakeUpstreamTCPProxy(t, 0x04)
	defer upstream.close()

	serverPool := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	allocatorPool := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(allocatorPool, upstream.tcpAddr, 5)
	s := NewWithAllocator(
		Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second},
		serverPool,
		allocation.New(allocatorPool),
	)
	req := &http.Request{Method: http.MethodConnect, Host: "example.com:443", Header: make(http.Header)}

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleHTTPConnect(serverConn, bufio.NewReader(strings.NewReader("")), req)
	}, func(clientConn net.Conn) {
		resp := readHTTPProxyResponse(t, clientConn, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
	})

	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
}

func TestHTTPForwardReturnsServiceUnavailableWhenNoProxyAvailable(t *testing.T) {
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	req, err := http.NewRequest(http.MethodGet, "http://example.com/resource", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleHTTPForward(serverConn, bufio.NewReader(strings.NewReader("")), req)
	}, func(clientConn net.Conn) {
		resp := readHTTPProxyResponse(t, clientConn, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
		}
	})
}

func TestHTTPForwardRecordsFailureWhenUpstreamConnectFails(t *testing.T) {
	upstream := startFakeUpstreamTCPProxy(t, 0x04)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 5)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	req, err := http.NewRequest(http.MethodGet, "http://example.com/resource", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleHTTPForward(serverConn, bufio.NewReader(strings.NewReader("")), req)
	}, func(clientConn net.Conn) {
		resp := readHTTPProxyResponse(t, clientConn, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
	})

	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
}
