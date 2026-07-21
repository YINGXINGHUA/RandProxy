package server

import (
	"net"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
)

func TestSOCKS5UDPAssociateReturnsGeneralFailureWhenNoProxyAvailable(t *testing.T) {
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)

	runServerConnHandler(t, func(serverConn net.Conn) {
		s.handleSOCKS5UDPAssociate(serverConn, "127.0.0.1:53")
	}, func(clientConn net.Conn) {
		rep := readSOCKS5Reply(t, clientConn)
		if rep != 0x01 {
			t.Fatalf("reply rep = %d, want 1", rep)
		}
	})
}

func TestSOCKS5UDPAssociateRelayRecordsSuccessfulAcquisitionAccounting(t *testing.T) {
	echoAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()
	upstream := startFakeUpstreamUDPProxy(t, 0x00)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 50)
	entry.LastUsed = time.Unix(0, 0)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	proxyAddr, stopProxy := startTCPProxyListener(t, s)
	defer stopProxy()
	controlConn, relayAddr := socks5UDPAssociate(t, proxyAddr)
	defer controlConn.Close()

	accounting := p.EntryAccountingSnapshot(entry)
	if accounting.UseCount != 1 {
		t.Fatalf("use count = %d, want 1", accounting.UseCount)
	}
	if accounting.LastUsed.Equal(time.Unix(0, 0)) {
		t.Fatalf("last used was not updated")
	}
	if active := s.activeLeaseSnapshot().Total; active != 1 {
		t.Fatalf("active leases = %d, want 1 while UDP association is open", active)
	}
	if accounting.LatencyCount != 2 {
		t.Fatalf("latency count = %d, want 2 before UDP association closes", accounting.LatencyCount)
	}

	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen client udp: %v", err)
	}
	defer clientUDP.Close()
	frame, err := buildSocks5UDPDatagram(echoAddr.IP.String(), echoAddr.Port, []byte("ping"))
	if err != nil {
		t.Fatalf("build datagram: %v", err)
	}
	if _, err := clientUDP.WriteToUDP(frame, relayAddr); err != nil {
		t.Fatalf("write relay datagram: %v", err)
	}
	_ = clientUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := clientUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read relay response: %v", err)
	}
	parsed, err := parseSocks5UDPDatagram(buf[:n])
	if err != nil {
		t.Fatalf("parse relay response: %v", err)
	}
	if string(parsed.Data) != "echo:ping" {
		t.Fatalf("payload = %q", string(parsed.Data))
	}

	controlConn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		accounting := p.EntryAccountingSnapshot(entry)
		if s.activeLeaseSnapshot().Total == 0 && accounting.LatencyCount == 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	accounting = p.EntryAccountingSnapshot(entry)
	t.Fatalf("lease did not finish after UDP association closed: active=%d latency_count=%d", s.activeLeaseSnapshot().Total, accounting.LatencyCount)
}
