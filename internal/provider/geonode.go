package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

const geonodeURL = "https://proxylist.geonode.com/api/proxy-list"

// Geonode fetches from proxylist.geonode.com (pagination API).
type Geonode struct {
	Client *http.Client
	Page   int
	Limit  int
	status statusField
}

func (p *Geonode) Name() string { return "geonode" }

func (p *Geonode) Status() proxy.ProviderStatus { return p.status.get() }

func (p *Geonode) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	url := fmt.Sprintf("%s?page=%d&limit=%d&sort_by=responseTime&sort_type=asc",
		geonodeURL, p.getPage(), p.getLimit())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("geonode: build request: %w", err)
	}
	req.Header.Set("User-Agent", "RandProxy/1.0")

	resp, err := p.client().Do(req)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("geonode: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.status.set(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("geonode: status %d: %s", resp.StatusCode, string(body))
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
		return nil, fmt.Errorf("geonode: decode: %w", err)
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

func (p *Geonode) client() *http.Client {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return p.Client
}

func (p *Geonode) getPage() int {
	if p.Page <= 0 {
		return 1
	}
	return p.Page
}

func (p *Geonode) getLimit() int {
	if p.Limit <= 0 || p.Limit > 500 {
		return 50
	}
	return p.Limit
}
