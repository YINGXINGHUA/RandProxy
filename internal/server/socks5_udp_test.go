package server

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
	proxycore "github.com/user/randproxy/internal/proxy"
)

func startTCPProxyListener(t *testing.T, s *ProxyServer) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					return
				}
			}
			go s.handleConn(conn)
		}
	}()
	return ln.Addr().String(), func() {
		close(stop)
		_ = ln.Close()
	}
}

func promoteTestProxy(p *pool.Pool, upstreamAddr string) {
	host, portStr, _ := net.SplitHostPort(upstreamAddr)
	port, _ := strconv.Atoi(portStr)
	p.Promote(&pool.Entry{
		Proxy:           &proxycore.Proxy{IP: host, Port: port, Protocol: proxycore.ProtocolSOCKS5, Source: "test"},
		MaxUse:          50,
		LatencyEWMA:     50 * time.Millisecond,
		LatencyVariance: time.Millisecond,
		LatencyCount:    2,
	})
}

func startUDPEchoServer(t *testing.T) (*net.UDPAddr, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(append([]byte("echo:"), buf[:n]...), addr)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr), func() { _ = conn.Close() }
}

type fakeUpstream struct {
	tcpAddr string
	close   func()
}

func startFakeUpstreamUDPProxy(t *testing.T, rep byte) *fakeUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					return
				}
			}
			go handleFakeUpstreamConn(conn, rep)
		}
	}()
	return &fakeUpstream{tcpAddr: ln.Addr().String(), close: func() { close(stop); _ = ln.Close() }}
}

func handleFakeUpstreamConn(conn net.Conn, rep byte) {
	defer conn.Close()
	var greeting [3]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var reqHdr [4]byte
	if _, err := io.ReadFull(conn, reqHdr[:]); err != nil {
		return
	}
	if _, _, err := readSocks5Address(conn, reqHdr[3]); err != nil {
		return
	}
	if rep != 0x00 {
		_ = writeSocks5Reply(conn, rep, "0.0.0.0", 0)
		return
	}
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return
	}
	defer udpConn.Close()
	addr := udpConn.LocalAddr().(*net.UDPAddr)
	if err := writeSocks5Reply(conn, 0x00, addr.IP.String(), addr.Port); err != nil {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		_ = udpConn.Close()
	}()
	buf := make([]byte, 64*1024)
	for {
		n, clientAddr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		r := bytes.NewReader(buf[:n])
		if _, err := r.Seek(3, io.SeekStart); err != nil {
			return
		}
		atyp, err := r.ReadByte()
		if err != nil {
			return
		}
		host, port, err := readSocks5Address(r, atyp)
		if err != nil {
			return
		}
		payload, err := io.ReadAll(r)
		if err != nil {
			return
		}
		targetConn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: port})
		if err != nil {
			return
		}
		if _, err := targetConn.Write(payload); err != nil {
			_ = targetConn.Close()
			return
		}
		_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp := make([]byte, 2048)
		rn, _, err := targetConn.ReadFromUDP(resp)
		_ = targetConn.Close()
		if err != nil {
			return
		}
		frame, err := buildSocks5UDPDatagram(host, port, resp[:rn])
		if err != nil {
			return
		}
		_, _ = udpConn.WriteToUDP(frame, clientAddr)
	}
}

func socks5UDPAssociate(t *testing.T, proxyAddr string) (net.Conn, *net.UDPAddr) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var authResp [2]byte
	if _, err := io.ReadFull(conn, authResp[:]); err != nil {
		t.Fatalf("read auth resp: %v", err)
	}
	if authResp != [2]byte{0x05, 0x00} {
		t.Fatalf("auth resp = %v", authResp)
	}
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write udp associate: %v", err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read reply header: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("udp associate rep = %d", reply[1])
	}
	host, port, err := readSocks5Address(conn, reply[3])
	if err != nil {
		t.Fatalf("read reply addr: %v", err)
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("resolve relay: %v", err)
	}
	return conn, relayAddr
}

func TestSOCKS5UDPAssociateRelay(t *testing.T) {
	echoAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()
	upstream := startFakeUpstreamUDPProxy(t, 0x00)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	promoteTestProxy(p, upstream.tcpAddr)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	proxyAddr, stopProxy := startTCPProxyListener(t, s)
	defer stopProxy()
	controlConn, relayAddr := socks5UDPAssociate(t, proxyAddr)
	defer controlConn.Close()
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
}

func TestSOCKS5UDPAssociateDropsFragments(t *testing.T) {
	echoAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()
	upstream := startFakeUpstreamUDPProxy(t, 0x00)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	promoteTestProxy(p, upstream.tcpAddr)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	proxyAddr, stopProxy := startTCPProxyListener(t, s)
	defer stopProxy()
	controlConn, relayAddr := socks5UDPAssociate(t, proxyAddr)
	defer controlConn.Close()
	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen client udp: %v", err)
	}
	defer clientUDP.Close()
	frame, err := buildSocks5UDPDatagram(echoAddr.IP.String(), echoAddr.Port, []byte("ping"))
	if err != nil {
		t.Fatalf("build datagram: %v", err)
	}
	frame[2] = 0x01
	if _, err := clientUDP.WriteToUDP(frame, relayAddr); err != nil {
		t.Fatalf("write fragment datagram: %v", err)
	}
	_ = clientUDP.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	buf := make([]byte, 2048)
	if _, _, err := clientUDP.ReadFromUDP(buf); err == nil {
		t.Fatalf("expected no response for fragmented packet")
	}
}

func TestSOCKS5UDPAssociateUpstreamFailure(t *testing.T) {
	upstream := startFakeUpstreamUDPProxy(t, 0x07)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	entry := promoteObservedTestProxy(p, upstream.tcpAddr, 50)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	proxyAddr, stopProxy := startTCPProxyListener(t, s)
	defer stopProxy()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var authResp [2]byte
	if _, err := io.ReadFull(conn, authResp[:]); err != nil {
		t.Fatalf("read auth resp: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write udp associate: %v", err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[1] != 0x07 {
		t.Fatalf("reply rep = %d", reply[1])
	}
	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
}

func TestSOCKS5UDPAssociatePinsFirstClientPort(t *testing.T) {
	echoAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()
	upstream := startFakeUpstreamUDPProxy(t, 0x00)
	defer upstream.close()
	p := pool.New(1, 5, 25, 10, time.Hour, 0, 0, 0.3, 3, 1, "")
	promoteTestProxy(p, upstream.tcpAddr)
	s := New(Config{Listen: "127.0.0.1:0", RelayIdleTimeout: time.Second}, p)
	proxyAddr, stopProxy := startTCPProxyListener(t, s)
	defer stopProxy()
	controlConn, relayAddr := socks5UDPAssociate(t, proxyAddr)
	defer controlConn.Close()
	firstClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen first client: %v", err)
	}
	defer firstClient.Close()
	secondClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen second client: %v", err)
	}
	defer secondClient.Close()
	frame, err := buildSocks5UDPDatagram(echoAddr.IP.String(), echoAddr.Port, []byte("ping"))
	if err != nil {
		t.Fatalf("build datagram: %v", err)
	}
	if _, err := firstClient.WriteToUDP(frame, relayAddr); err != nil {
		t.Fatalf("write first datagram: %v", err)
	}
	_ = firstClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	if _, _, err := firstClient.ReadFromUDP(buf); err != nil {
		t.Fatalf("read first response: %v", err)
	}
	if _, err := secondClient.WriteToUDP(frame, relayAddr); err != nil {
		t.Fatalf("write second datagram: %v", err)
	}
	_ = secondClient.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := secondClient.ReadFromUDP(buf); err == nil {
		t.Fatalf("expected second client port to be rejected")
	}
}
