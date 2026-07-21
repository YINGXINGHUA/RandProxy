package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/user/randproxy/internal/allocation"
)

type udpDatagram struct {
	Frag byte
	Data []byte
}

func readSocks5Address(r io.Reader, atyp byte) (string, int, error) {
	switch atyp {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", 0, err
		}
		port, err := readSocks5Port(r)
		return net.IP(ip).String(), port, err
	case 0x03:
		lenb := make([]byte, 1)
		if _, err := io.ReadFull(r, lenb); err != nil {
			return "", 0, err
		}
		domain := make([]byte, lenb[0])
		if _, err := io.ReadFull(r, domain); err != nil {
			return "", 0, err
		}
		port, err := readSocks5Port(r)
		return string(domain), port, err
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", 0, err
		}
		port, err := readSocks5Port(r)
		return net.IP(ip).String(), port, err
	default:
		return "", 0, fmt.Errorf("unsupported atyp %d", atyp)
	}
}

func readSocks5Port(r io.Reader) (int, error) {
	var portb [2]byte
	if _, err := io.ReadFull(r, portb[:]); err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(portb[:])), nil
}

func encodeSocks5Address(host string, port int) ([]byte, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	buf := bytes.NewBuffer(nil)
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			buf.WriteByte(0x01)
			buf.Write(ip4)
		} else {
			buf.WriteByte(0x04)
			buf.Write(ip.To16())
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("domain too long")
		}
		buf.WriteByte(0x03)
		buf.WriteByte(byte(len(host)))
		buf.WriteString(host)
	}
	var portb [2]byte
	binary.BigEndian.PutUint16(portb[:], uint16(port))
	buf.Write(portb[:])
	return buf.Bytes(), nil
}

func writeSocks5Reply(w io.Writer, rep byte, host string, port int) error {
	addr, err := encodeSocks5Address(host, port)
	if err != nil {
		addr = []byte{0x01, 0, 0, 0, 0, 0, 0}
	}
	_, err = w.Write(append([]byte{0x05, rep, 0x00}, addr...))
	return err
}

func parseSocks5UDPDatagram(data []byte) (udpDatagram, error) {
	if len(data) < 4 {
		return udpDatagram{}, fmt.Errorf("datagram too short")
	}
	r := bytes.NewReader(data)
	var rsv [2]byte
	if _, err := io.ReadFull(r, rsv[:]); err != nil {
		return udpDatagram{}, err
	}
	frag, err := r.ReadByte()
	if err != nil {
		return udpDatagram{}, err
	}
	atyp, err := r.ReadByte()
	if err != nil {
		return udpDatagram{}, err
	}
	if _, _, err := readSocks5Address(r, atyp); err != nil {
		return udpDatagram{}, err
	}
	payload, err := io.ReadAll(r)
	if err != nil {
		return udpDatagram{}, err
	}
	return udpDatagram{Frag: frag, Data: payload}, nil
}

func buildSocks5UDPDatagram(host string, port int, payload []byte) ([]byte, error) {
	addr, err := encodeSocks5Address(host, port)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 3+len(addr)+len(payload))
	buf = append(buf, 0x00, 0x00, 0x00)
	buf = append(buf, addr...)
	buf = append(buf, payload...)
	return buf, nil
}

func udpBindHost(conn net.Conn, udpAddr *net.UDPAddr) string {
	if udpAddr != nil && udpAddr.IP != nil && !udpAddr.IP.IsUnspecified() {
		return udpAddr.IP.String()
	}
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok && tcpAddr.IP != nil && !tcpAddr.IP.IsUnspecified() {
		return tcpAddr.IP.String()
	}
	return "0.0.0.0"
}

func establishUpstreamUDPAssociation(ctx context.Context, upstream string, timeout time.Duration, bindAddr *net.UDPAddr) (net.Conn, *net.UDPAddr, byte, error) {
	dialer := &net.Dialer{Timeout: timeout}
	controlConn, err := dialer.DialContext(ctx, "tcp", upstream)
	if err != nil {
		return nil, nil, 0x01, err
	}
	if err := controlConn.SetDeadline(time.Now().Add(timeout)); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	if _, err := controlConn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	var authResp [2]byte
	if _, err := io.ReadFull(controlConn, authResp[:]); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	if authResp[0] != 0x05 || authResp[1] != 0x00 {
		controlConn.Close()
		return nil, nil, authResp[1], fmt.Errorf("unexpected auth response %v", authResp)
	}
	bindHost := "0.0.0.0"
	bindPort := 0
	if bindAddr != nil {
		if bindAddr.IP != nil && !bindAddr.IP.IsUnspecified() {
			bindHost = bindAddr.IP.String()
		}
		bindPort = bindAddr.Port
	}
	reqAddr, err := encodeSocks5Address(bindHost, bindPort)
	if err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	req := append([]byte{0x05, 0x03, 0x00}, reqAddr...)
	if _, err := controlConn.Write(req); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	var reply [4]byte
	if _, err := io.ReadFull(controlConn, reply[:]); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	if reply[0] != 0x05 || reply[2] != 0x00 {
		controlConn.Close()
		return nil, nil, 0x01, fmt.Errorf("invalid udp associate reply header %v", reply)
	}
	if reply[1] != 0x00 {
		controlConn.Close()
		return nil, nil, reply[1], fmt.Errorf("udp associate failed with %d", reply[1])
	}
	host, port, err := readSocks5Address(controlConn, reply[3])
	if err != nil {
		controlConn.Close()
		return nil, nil, reply[1], err
	}
	if err := controlConn.SetDeadline(time.Time{}); err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		controlConn.Close()
		return nil, nil, 0x01, err
	}
	return controlConn, relayAddr, 0x00, nil
}

func (s *ProxyServer) handleSOCKS5UDPAssociate(conn net.Conn, target string) {
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := s.acquireLease(ctx)
	var upstream string
	record := func(success bool, latency time.Duration) {
		if s.stats != nil {
			s.stats.record(latency, success)
			s.stats.recordRequest(target, upstream, latency.Milliseconds(), success)
		}
	}
	if err != nil {
		_ = writeSocks5Reply(conn, 0x01, "0.0.0.0", 0)
		record(false, time.Since(t0))
		return
	}
	defer lease.Finish(allocation.Result{Success: false})
	upstream = lease.UpstreamAddr()
	downstreamUDP, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		lease.Finish(allocation.Result{Success: false})
		_ = writeSocks5Reply(conn, 0x01, "0.0.0.0", 0)
		record(false, time.Since(t0))
		return
	}
	defer downstreamUDP.Close()
	upstreamUDP, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		lease.Finish(allocation.Result{Success: false})
		_ = writeSocks5Reply(conn, 0x01, "0.0.0.0", 0)
		record(false, time.Since(t0))
		return
	}
	defer upstreamUDP.Close()
	controlConn, relayAddr, rep, err := establishUpstreamUDPAssociation(ctx, upstream, 10*time.Second, upstreamUDP.LocalAddr().(*net.UDPAddr))
	if err != nil {
		lease.Finish(allocation.Result{Success: false})
		_ = writeSocks5Reply(conn, rep, "0.0.0.0", 0)
		record(false, time.Since(t0))
		return
	}
	defer controlConn.Close()
	bindAddr := downstreamUDP.LocalAddr().(*net.UDPAddr)
	if err := writeSocks5Reply(conn, 0x00, udpBindHost(conn, bindAddr), bindAddr.Port); err != nil {
		record(false, time.Since(t0))
		return
	}
	connTime := time.Since(t0)
	record(true, connTime)
	var (
		mu         sync.RWMutex
		clientAddr *net.UDPAddr
	)
	clientIP := net.IP(nil)
	if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		clientIP = tcpAddr.IP
	}
	errCh := make(chan error, 2)
	go func() {
		errCh <- relayDownstreamUDP(downstreamUDP, upstreamUDP, relayAddr, clientIP, &mu, &clientAddr)
	}()
	go func() {
		errCh <- relayUpstreamUDP(upstreamUDP, downstreamUDP, relayAddr, &mu, &clientAddr)
	}()
	controlDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(controlDone)
	}()
	select {
	case <-controlDone:
	case <-errCh:
	}
	lease.Finish(allocation.Result{Success: true, ConnectLatency: connTime})
}

func relayDownstreamUDP(downstreamUDP, upstreamUDP *net.UDPConn, relayAddr *net.UDPAddr, clientIP net.IP, mu *sync.RWMutex, clientAddr **net.UDPAddr) error {
	buf := make([]byte, 64*1024)
	for {
		n, src, err := downstreamUDP.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if clientIP != nil && !src.IP.Equal(clientIP) {
			continue
		}
		packet, err := parseSocks5UDPDatagram(buf[:n])
		if err != nil || packet.Frag != 0 {
			continue
		}
		mu.Lock()
		if *clientAddr == nil {
			*clientAddr = &net.UDPAddr{IP: append(net.IP(nil), src.IP...), Port: src.Port, Zone: src.Zone}
		} else if !sameUDPAddr(*clientAddr, src) {
			mu.Unlock()
			continue
		}
		mu.Unlock()
		if _, err := upstreamUDP.WriteToUDP(buf[:n], relayAddr); err != nil {
			return err
		}
	}
}

func relayUpstreamUDP(upstreamUDP, downstreamUDP *net.UDPConn, relayAddr *net.UDPAddr, mu *sync.RWMutex, clientAddr **net.UDPAddr) error {
	buf := make([]byte, 64*1024)
	for {
		n, src, err := upstreamUDP.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if relayAddr != nil {
			if relayAddr.Port != 0 && src.Port != relayAddr.Port {
				continue
			}
			if relayAddr.IP != nil && !relayAddr.IP.IsUnspecified() && !src.IP.Equal(relayAddr.IP) {
				continue
			}
		}
		mu.RLock()
		dst := *clientAddr
		mu.RUnlock()
		if dst == nil {
			continue
		}
		if _, err := downstreamUDP.WriteToUDP(buf[:n], dst); err != nil {
			return err
		}
	}
}

func sameUDPAddr(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Port == b.Port && a.IP.Equal(b.IP) && a.Zone == b.Zone
}
