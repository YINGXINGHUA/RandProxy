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

const goodipsURL = "http://api.goodips.com/?format=json"

// Goodips fetches proxies from api.goodips.com.
// Returns mix of HTTP and SOCKS5 proxies.
type Goodips struct {
	Client *http.Client
	status statusField
}

func (p *Goodips) Name() string { return "goodips" }

func (p *Goodips) Status() proxy.ProviderStatus { return p.status.get() }

func (p *Goodips) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, goodipsURL, nil)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("goodips: build request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("goodips: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.status.set(proxy.StatusOffline)
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("goodips: status %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		Total int    `json:"total"`
		Data  []struct {
			IP       string `json:"ip"`
			Port     string `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("goodips: decode: %w", err)
	}
	if r.Code != 0 {
		p.status.set(proxy.StatusOffline)
		return nil, fmt.Errorf("goodips: api code %d: %s", r.Code, r.Msg)
	}

	proxies := make([]*proxy.Proxy, 0, len(r.Data))
	for _, item := range r.Data {
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

func (p *Goodips) client() *http.Client {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return p.Client
}
