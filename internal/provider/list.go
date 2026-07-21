package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

// ParseFunc decodes raw bytes from a source into proxy entries.
type ParseFunc func([]byte) ([]proxy.Proxy, error)

// ListProvider fetches a proxy list from a static URL and parses it.
type ListProvider struct {
	name   string
	url    string
	parse  ParseFunc
	cli    *http.Client
	status proxy.ProviderStatus
	statusMu sync.RWMutex
}

func NewListProvider(name, url string, parse ParseFunc) *ListProvider {
	return &ListProvider{
		name:   name,
		url:    url,
		parse:  parse,
		cli:    &http.Client{Timeout: 15 * time.Second},
		status: proxy.StatusUnknown,
	}
}

func (p *ListProvider) Name() string { return p.name }

func (p *ListProvider) Status() proxy.ProviderStatus {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	return p.status
}

func (p *ListProvider) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		p.setStatus(proxy.StatusOffline)
		return nil, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	req.Header.Set("User-Agent", "RandProxy/1.0")

	resp, err := p.cli.Do(req)
	if err != nil {
		p.setStatus(proxy.StatusOffline)
		return nil, fmt.Errorf("%s: request: %w", p.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.setStatus(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status %d: %s", p.name, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.setStatus(proxy.StatusOffline)
		return nil, fmt.Errorf("%s: read: %w", p.name, err)
	}

	parsed, err := p.parse(body)
	if err != nil {
		p.setStatus(proxy.StatusOffline)
		return nil, fmt.Errorf("%s: parse: %w", p.name, err)
	}

	p.setStatus(proxy.StatusOnline)
	proxies := make([]*proxy.Proxy, len(parsed))
	for i := range parsed {
		proxies[i] = &parsed[i]
		proxies[i].Source = p.name
	}
	return proxies, nil
}

func (p *ListProvider) setStatus(s proxy.ProviderStatus) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.status = s
}

// --- Parsers ---

// ParseProxyScrape parses the ProxyScrape JSON format.
// {"protocol":"socks5","ip":"...","port":1080,...}
func ParseProxyScrape(body []byte) ([]proxy.Proxy, error) {
	var raw []struct {
		Protocol string `json:"protocol"`
		IP       string `json:"ip"`
		Port     int    `json:"port"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]proxy.Proxy, 0, len(raw))
	for _, r := range raw {
		proto := parseProtocol(r.Protocol)
		if proto != proxy.ProtocolSOCKS5 {
			continue
		}
		out = append(out, proxy.Proxy{
			IP:       r.IP,
			Port:     r.Port,
			Protocol: proto,
		})
	}
	return out, nil
}

// ParseProxifly parses the Proxifly JSON format.
// {"proxy":"socks5://ip:port","protocol":"socks5","ip":"...","port":58208}
func ParseProxifly(body []byte) ([]proxy.Proxy, error) {
	var raw []struct {
		Protocol string `json:"protocol"`
		IP       string `json:"ip"`
		Port     int    `json:"port"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]proxy.Proxy, 0, len(raw))
	for _, r := range raw {
		proto := parseProtocol(r.Protocol)
		if proto != proxy.ProtocolSOCKS5 {
			continue
		}
		out = append(out, proxy.Proxy{
			IP:       r.IP,
			Port:     r.Port,
			Protocol: proto,
		})
	}
	return out, nil
}

// ParseTXT parses plain text with one ip:port per line.
func ParseTXT(body []byte) ([]proxy.Proxy, error) {
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	out := make([]proxy.Proxy, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		port := 0
		fmt.Sscanf(parts[1], "%d", &port)
		if port == 0 {
			continue
		}
		out = append(out, proxy.Proxy{
			IP:       parts[0],
			Port:     port,
			Protocol: proxy.ProtocolSOCKS5,
		})
	}
	return out, nil
}

func parseProtocol(s string) proxy.Protocol {
	switch strings.ToLower(s) {
	case "socks5":
		return proxy.ProtocolSOCKS5
	case "socks4":
		return proxy.ProtocolSOCKS5 // treat SOCKS4 as SOCKS5 for our purposes
	case "http", "https":
		return proxy.ProtocolHTTP
	default:
		return proxy.ProtocolHTTP
	}
}