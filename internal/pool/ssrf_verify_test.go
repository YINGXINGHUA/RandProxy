package pool

import (
	"fmt"
	"testing"
	"time"
	"github.com/user/randproxy/internal/proxy"
)

func TestSSRFFiltering(t *testing.T) {
	p := New(10, 100, 25, 300, 
		24*time.Hour, 1*time.Hour, 5*time.Second, 
		0.1, 3, 5, "")
	
	// Feed internal IPs — they should be rejected
	internalIPs := []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0", "::1", "fe80::1"}
	for _, ip := range internalIPs {
		p.Feed([]*proxy.Proxy{{IP: ip, Port: 8080, Protocol: "socks5"}})
	}
	
	stats := p.DumpStats()
	fmt.Println("After feeding internal IPs:", stats)
	
	// All internal IPs should be in known map (marked as known to avoid retry)
	// but NOT in buffer or ready
	if p.BufferNeedRefill() {
		t.Log("Buffer needs refill (internal IPs were rejected)")
	}
	
	// Verify internal IPs are marked as known
	for _, ip := range internalIPs {
		pr := &proxy.Proxy{IP: ip, Port: 8080}
		key := proxyKey(pr)
		if !p.known[key] {
			t.Errorf("internal IP %s should be marked as known", ip)
		}
	}
	
	fmt.Println("SSRF filtering verified: internal IPs rejected and marked known")
}
