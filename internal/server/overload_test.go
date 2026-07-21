package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHandleConnReturnsHTTP503WhenConnectionLimitIsFull(t *testing.T) {
	srv := &ProxyServer{connSem: make(chan struct{}, 1)}
	fillConnectionSemaphore(t, srv)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan struct{})
	go func() {
		srv.handleConn(serverConn)
		close(done)
	}()

	if _, err := clientConn.Write([]byte("G")); err != nil {
		t.Fatalf("write request prefix: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("read overload response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	waitForHandleConn(t, done)
}

func TestHandleConnReturnsSOCKSFailureWhenConnectionLimitIsFull(t *testing.T) {
	srv := &ProxyServer{connSem: make(chan struct{}, 1)}
	fillConnectionSemaphore(t, srv)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan struct{})
	go func() {
		srv.handleConn(serverConn)
		close(done)
	}()

	if _, err := clientConn.Write([]byte{0x05}); err != nil {
		t.Fatalf("write socks prefix: %v", err)
	}
	var response [2]byte
	if _, err := io.ReadFull(clientConn, response[:]); err != nil {
		t.Fatalf("read overload response: %v", err)
	}
	if response != [2]byte{0x05, 0xFF} {
		t.Fatalf("response = %#v, want SOCKS5 no acceptable methods", response)
	}
	waitForHandleConn(t, done)
}

func TestNewWithAllocatorCapsMaxConnections(t *testing.T) {
	srv := NewWithAllocator(Config{Listen: "127.0.0.1:0", MaxConnections: maxSupportedConns + 1}, nil, nil)
	if cap(srv.connectionSemaphore()) != maxSupportedConns {
		t.Fatalf("conn semaphore cap = %d, want %d", cap(srv.connectionSemaphore()), maxSupportedConns)
	}
}

func fillConnectionSemaphore(t *testing.T, srv *ProxyServer) {
	t.Helper()
	sem := srv.connectionSemaphore()
	for len(sem) > 0 {
		<-sem
	}
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	t.Cleanup(func() {
		for len(sem) > 0 {
			<-sem
		}
	})
}

func waitForHandleConn(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleConn did not return")
	}
}
