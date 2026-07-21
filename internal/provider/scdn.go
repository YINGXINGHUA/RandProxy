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

const scdnURL = "https://proxy.scdn.io/api/get_proxy.php"

// Scdn fetches SOCKS5 proxies from proxy.scdn.io.
// protocol=socks5 filter empirically yields ~45% HTTPS pass rate.
type Scdn struct {
	Client *http.Client
	status statusField
}

func (p *Scdn) Name() string { return "scdn-socks5" }

func (p *Scdn) Status() proxy.ProviderStatus { return p.status.get() }

func (p *Scdn) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	url := scdnURL + "?protocol=socks5&count=20"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("scdn: build request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("scdn: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.status.set(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("scdn: status %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Proxies []string `json:"proxies"`
			Count   int      `json:"count"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("scdn: decode: %w", err)
	}
	if r.Code != 200 {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("scdn: api code %d: %s", r.Code, r.Message)
	}

	proxies := make([]*proxy.Proxy, 0, len(r.Data.Proxies))
	for _, entry := range r.Data.Proxies {
		parts := strings.Split(entry, ":")
		if len(parts) != 2 {
			continue
		}
		port := 0
		fmt.Sscanf(parts[1], "%d", &port)
		if port == 0 {
			continue
		}
		proxies = append(proxies, &proxy.Proxy{
			IP:       parts[0],
			Port:     port,
			Protocol: proxy.ProtocolSOCKS5,
			Source:   p.Name(),
		})
	}
	p.status.set(proxy.StatusOnline)
	return proxies, nil
}

func (p *Scdn) client() *http.Client {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return p.Client
}
