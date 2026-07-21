package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
	proxycore "github.com/user/randproxy/internal/proxy"
)

func promoteObservedTestProxy(p *pool.Pool, upstreamAddr string, maxUse int) *pool.Entry {
	host, portStr, _ := net.SplitHostPort(upstreamAddr)
	port, _ := strconv.Atoi(portStr)
	entry := &pool.Entry{
		Proxy:           &proxycore.Proxy{IP: host, Port: port, Protocol: proxycore.ProtocolSOCKS5, Source: "test"},
		MaxUse:          maxUse,
		LatencyEWMA:     50 * time.Millisecond,
		LatencyVariance: time.Millisecond,
		LatencyCount:    2,
	}
	p.Promote(entry)
	return entry
}

func startTCPEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() {
		close(stop)
		_ = ln.Close()
	}
}

func startFakeUpstreamTCPProxy(t *testing.T, rep byte) *fakeUpstream {
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
			go handleFakeUpstreamTCPConnectConn(conn, rep)
		}
	}()
	return &fakeUpstream{tcpAddr: ln.Addr().String(), close: func() { close(stop); _ = ln.Close() }}
}

func handleFakeUpstreamTCPConnectConn(conn net.Conn, rep byte) {
	defer conn.Close()
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var reqHdr [4]byte
	if _, err := io.ReadFull(conn, reqHdr[:]); err != nil {
		return
	}
	host, port, err := readSocks5Address(conn, reqHdr[3])
	if err != nil {
		return
	}
	if rep != 0x00 {
		_ = writeSocks5Reply(conn, rep, "0.0.0.0", 0)
		return
	}
	targetConn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		_ = writeSocks5Reply(conn, 0x04, "0.0.0.0", 0)
		return
	}
	defer targetConn.Close()
	localAddr := targetConn.LocalAddr().(*net.TCPAddr)
	if err := writeSocks5Reply(conn, 0x00, localAddr.IP.String(), localAddr.Port); err != nil {
		return
	}
	relay(conn, targetConn, time.Second)
}

func runServerConnHandler(t *testing.T, handler func(net.Conn), clientAction func(net.Conn)) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverConn.Close()
		handler(serverConn)
	}()
	clientAction(clientConn)
	_ = clientConn.Close()
	<-done
}

func readSOCKS5Reply(t *testing.T, conn net.Conn) byte {
	t.Helper()
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read socks5 reply header: %v", err)
	}
	if _, _, err := readSocks5Address(conn, reply[3]); err != nil {
		t.Fatalf("read socks5 reply address: %v", err)
	}
	return reply[1]
}

func readHTTPProxyResponse(t *testing.T, conn net.Conn, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read http response: %v", err)
	}
	return resp
}
