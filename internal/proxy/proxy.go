// Package proxy defines core types and interfaces for the proxy pool.
package proxy

// Protocol is the type of proxy protocol.
type Protocol string

const (
	ProtocolHTTP   Protocol = "http"
	ProtocolHTTPS  Protocol = "https"
	ProtocolSOCKS5 Protocol = "socks5"
)

// Proxy represents a single proxy fetched from any provider.
type Proxy struct {
	IP       string   `json:"ip"`
	Port     int      `json:"port"`
	Protocol Protocol `json:"protocol"`
	Source   string   `json:"source"` // provider name, e.g. "66daili"
}

// Addr returns the "ip:port" string.
func (p *Proxy) Addr() string {
	return p.IP + ":" + itoa(p.Port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
