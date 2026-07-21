package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

const daili66URL = "http://api.66daili.com/?format=json"

// Daili66 fetches proxies from api.66daili.com.
// Rate-limited to ~1 request per 10 seconds.
type Daili66 struct {
	Client *http.Client
	status statusField
}

func (p *Daili66) Name() string { return "66daili" }

func (p *Daili66) Status() proxy.ProviderStatus { return p.status.get() }

func (p *Daili66) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, daili66URL, nil)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("66daili: build request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("66daili: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.status.set(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("66daili: status %d: %s", resp.StatusCode, string(body))
	}

	var dailiResp struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		Total int    `json:"total"`
		Data  []struct {
			IP       string `json:"ip"`
			Port     string `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&dailiResp); err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("66daili: decode: %w", err)
	}

	if dailiResp.Code != 0 {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("66daili: api code %d: %s", dailiResp.Code, dailiResp.Msg)
	}

	proxies := make([]*proxy.Proxy, 0, len(dailiResp.Data))
	for _, item := range dailiResp.Data {
		port, err := strconv.Atoi(item.Port)
		if err != nil {
			continue
		}
		proxies = append(proxies, &proxy.Proxy{
			IP:       item.IP,
			Port:     port,
			Protocol: parseDailiProtocol(item.Protocol),
			Source:   p.Name(),
		})
	}
	p.status.set(proxy.StatusOnline)
	return proxies, nil
}

func (p *Daili66) client() *http.Client {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return p.Client
}

func parseDailiProtocol(s string) proxy.Protocol {
	switch s {
	case "HTTP", "http":
		return proxy.ProtocolHTTP
	case "HTTPS", "https":
		return proxy.ProtocolHTTPS
	case "SOCKS5", "Socks5", "socks5":
		return proxy.ProtocolSOCKS5
	default:
		return proxy.ProtocolHTTP
	}
}
