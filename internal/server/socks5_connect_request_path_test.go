package server

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
)

func TestSOCKS5ConnectReturnsGeneralFailureWhenNoProxyAvailable(t *testing.T) {
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleSOCKS5Connect(serverConn, "127.0.0.1:80")
	}, func(clientConn net.Conn) {
		rep := readSOCKS5Reply(t, clientConn)
		if rep != 0x01 {
			t.Fatalf("reply rep = %d, want 1", rep)
		}
	})
}

func TestSOCKS5ConnectRecordsFailureWhenUpstreamConnectFails(t *testing.T) {
	upstream := startFakeUpstreamTCPProxy(t, 0x04)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 5)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleSOCKS5Connect(serverConn, "127.0.0.1:80")
	}, func(clientConn net.Conn) {
		rep := readSOCKS5Reply(t, clientConn)
		if rep != 0x04 {
			t.Fatalf("reply rep = %d, want 4", rep)
		}
	})

	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
	if p.ReadyCount() != 1 {
		t.Fatalf("ready count = %d, want 1", p.ReadyCount())
	}
	if p.BlacklistCount() != 0 {
		t.Fatalf("blacklist count = %d, want 0", p.BlacklistCount())
	}
}

func TestSOCKS5ConnectRecordsLatencyAndBlacklistsWhenSuccessfulConnectExhaustsMaxUse(t *testing.T) {
	echoAddr, stopEcho := startTCPEchoServer(t)
	defer stopEcho()
	upstream := startFakeUpstreamTCPProxy(t, 0x00)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 1)
	entry.LastUsed = time.Unix(0, 0)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleSOCKS5Connect(serverConn, echoAddr)
	}, func(clientConn net.Conn) {
		rep := readSOCKS5Reply(t, clientConn)
		if rep != 0x00 {
			t.Fatalf("reply rep = %d, want 0", rep)
		}
		if err := clientConn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		if _, err := clientConn.Write([]byte("ping")); err != nil {
			t.Fatalf("write echo payload: %v", err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(clientConn, buf); err != nil {
			t.Fatalf("read echo payload: %v", err)
		}
		if string(buf) != "ping" {
			t.Fatalf("echo payload = %q", string(buf))
		}
	})

	if entry.UseCount != 1 {
		t.Fatalf("use count = %d, want 1", entry.UseCount)
	}
	if entry.LastUsed.Equal(time.Unix(0, 0)) {
		t.Fatalf("last used was not updated")
	}
	if entry.LatencyCount != 3 {
		t.Fatalf("latency count = %d, want 3", entry.LatencyCount)
	}
	if p.ReadyCount() != 0 {
		t.Fatalf("ready count = %d, want 0", p.ReadyCount())
	}
	if p.BlacklistCount() != 1 {
		t.Fatalf("blacklist count = %d, want 1", p.BlacklistCount())
	}
}
