package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

// Proxy43 fetches from 43.135.31.113:8777 (pagination API).
type Proxy43 struct {
	Client *http.Client
	Page   int
	Limit  int
	status statusField
}

func (p *Proxy43) Name() string { return "43.135.31.113" }

func (p *Proxy43) Status() proxy.ProviderStatus { return p.status.get() }

func (p *Proxy43) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	url := fmt.Sprintf("http://43.135.31.113:8777/proxyList?limit=%d&page=%d&language=zh-hans",
		p.getLimit(), p.getPage())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("43.135.31.113: build request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("43.135.31.113: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.status.set(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("43.135.31.113: status %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		Data []struct {
			IP        string   `json:"ip"`
			Port      string   `json:"port"`
			Protocols []string `json:"protocols"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("43.135.31.113: decode: %w", err)
	}

	proxies := make([]*proxy.Proxy, 0, len(r.Data))
	for _, item := range r.Data {
		port := 0
		fmt.Sscanf(item.Port, "%d", &port)
		if port == 0 {
			continue
		}
		proto := pickProtocol(item.Protocols)
		if proto == proxy.ProtocolHTTP {
			continue
		}
		proxies = append(proxies, &proxy.Proxy{
			IP:       item.IP,
			Port:     port,
			Protocol: proto,
			Source:   p.Name(),
		})
	}
	p.status.set(proxy.StatusOnline)
	return proxies, nil
}

func (p *Proxy43) client() *http.Client {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return p.Client
}

func (p *Proxy43) getPage() int {
	if p.Page <= 0 {
		return 1
	}
	return p.Page
}

func (p *Proxy43) getLimit() int {
	if p.Limit <= 0 || p.Limit > 100 {
		return 50
	}
	return p.Limit
}

func pickProtocol(protos []string) proxy.Protocol {
	for _, s := range protos {
		switch strings.ToLower(s) {
		case "socks5":
			return proxy.ProtocolSOCKS5
		case "socks4":
			return proxy.ProtocolSOCKS5 // treat as SOCKS5 for our purposes
		case "http", "https":
			return proxy.ProtocolHTTP
		}
	}
	return proxy.ProtocolHTTP
}
