package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/config"
	"github.com/user/randproxy/internal/controlplane"
	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/proxy"
)

const controlPlaneTestBaseConfigJSONC = `{
  "server": {"host": "127.0.0.1", "port": 18080, "web_host": "127.0.0.1", "web_port": 18081, "relay_idle_timeout": "30s"},
  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false},
  "policy": {"mode": "balanced", "random_subset_size": 3, "stable_subset_size": 3},
  "control_plane": {"trusted_local_only": true},
  "sources": {"enabled": {"alpha": true, "beta": true}}
}`

func newControlPlaneTestServer(t *testing.T) (*ProxyServer, *controlplane.Manager, string) {
	t.Helper()

	proxyPool := pool.New(1, 10, 2, 10, 0, 0, 0, 0.3, 1, 1, "")
	srv := New(Config{Listen: ":0", WebListen: ":0"}, proxyPool)
	srv.SetStatsProvider(func() []proxy.Provider { return nil })

	basePath := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(basePath, []byte(controlPlaneTestBaseConfigJSONC), 0o644); err != nil {
		t.Fatalf("write base config: %v", err)
	}

	manager, err := controlplane.NewManager(basePath, controlplane.ManagerOptions{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	srv.SetControlPlaneManager(manager)

	return srv, manager, basePath
}

func disableTrustedLocalOnly(t *testing.T, manager *controlplane.Manager) {
	t.Helper()

	desired := manager.Export().EffectiveConfig
	desired.ControlPlane.TrustedLocalOnly = false
	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("disable trusted-local-only: %v", err)
	}
	if !result.Receipt.OK {
		t.Fatalf("disable trusted-local-only receipt: %#v", result.Receipt)
	}
	if manager.Export().EffectiveConfig.ControlPlane.TrustedLocalOnly {
		t.Fatal("trusted-local-only remained enabled")
	}
}

func performLoopbackJSONRequest(t *testing.T, handler http.Handler, method string, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4242"
	req.Host = "127.0.0.1:18081"
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func decodeJSONResponse[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()

	var decoded T
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json response: %v\nbody=%s", err, recorder.Body.String())
	}
	return decoded
}

func TestControlPlaneAPIOverview_returnsBootstrapModel_whenManagerConfigured(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	recorder := performLoopbackJSONRequest(t, handler, http.MethodGet, "/api/v1/overview", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Overview            map[string]any `json:"overview"`
		EffectiveConfigMeta struct {
			BasePath     string `json:"base_path"`
			OverridePath string `json:"override_path"`
		} `json:"effective_config_meta"`
		LastApplyReceipt any      `json:"last_apply_receipt"`
		RestartRequired  []string `json:"restart_required"`
	}
	body = decodeJSONResponse[struct {
		Overview            map[string]any `json:"overview"`
		EffectiveConfigMeta struct {
			BasePath     string `json:"base_path"`
			OverridePath string `json:"override_path"`
		} `json:"effective_config_meta"`
		LastApplyReceipt any      `json:"last_apply_receipt"`
		RestartRequired  []string `json:"restart_required"`
	}](t, recorder)

	if body.Overview == nil {
		t.Fatal("overview missing from response")
	}
	if body.EffectiveConfigMeta.BasePath == "" {
		t.Fatal("effective config base_path missing from response")
	}
	if body.EffectiveConfigMeta.OverridePath == "" {
		t.Fatal("effective config override_path missing from response")
	}
	if body.LastApplyReceipt != nil {
		t.Fatalf("last_apply_receipt = %#v, want nil before first apply", body.LastApplyReceipt)
	}
	if len(body.RestartRequired) != 0 {
		t.Fatalf("restart_required = %#v, want empty before first apply", body.RestartRequired)
	}
	if _, ok := body.Overview["pool"]; !ok {
		t.Fatalf("overview missing pool summary: %#v", body.Overview)
	}
}

func TestControlPlaneAPIConfig_returnsEffectiveConfigMetadata_whenRequested(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	recorder := performLoopbackJSONRequest(t, handler, http.MethodGet, "/api/v1/config", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	exported := manager.Export()
	body := decodeJSONResponse[struct {
		BasePath              string         `json:"base_path"`
		OverridePath          string         `json:"override_path"`
		EffectiveConfig       *config.Config `json:"effective_config"`
		LiveFields            []string       `json:"live_fields"`
		RestartRequiredFields []string       `json:"restart_required_fields"`
	}](t, recorder)

	if body.BasePath != exported.BasePath {
		t.Fatalf("base_path = %q, want %q", body.BasePath, exported.BasePath)
	}
	if body.OverridePath != exported.OverridePath {
		t.Fatalf("override_path = %q, want %q", body.OverridePath, exported.OverridePath)
	}
	if body.EffectiveConfig == nil {
		t.Fatal("effective_config missing")
	}
	if body.EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatalf("effective_config.policy.mode = %q", body.EffectiveConfig.Policy.Mode)
	}
	if len(body.LiveFields) == 0 {
		t.Fatal("live_fields missing")
	}
	if len(body.RestartRequiredFields) == 0 {
		t.Fatal("restart_required_fields missing")
	}
}

func TestControlPlaneAPIConfigApply_returnsMixedReceipt_whenLiveAndRestartFieldsChange(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.Policy.Mode = "single_best"
	desired.Pool.MaxUse = 7
	desired.Server.WebPort = 19091

	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	recorder := performLoopbackJSONRequest(t, handler, http.MethodPut, "/api/v1/config", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config apply status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	response := decodeJSONResponse[struct {
		OK                    bool           `json:"ok"`
		EffectiveConfig       *config.Config `json:"effective_config"`
		AppliedLiveFields     []string       `json:"applied_live_fields"`
		RestartRequiredFields []string       `json:"restart_required_fields"`
		Receipt               struct {
			Operation struct {
				OK      bool   `json:"ok"`
				Noop    bool   `json:"noop"`
				Message string `json:"message"`
			} `json:"operation"`
			Persistence struct {
				Attempted    bool   `json:"attempted"`
				Succeeded    bool   `json:"succeeded"`
				OverridePath string `json:"override_path"`
				Error        string `json:"error"`
			} `json:"persistence"`
		} `json:"receipt"`
	}](t, recorder)

	if !response.OK {
		t.Fatalf("apply ok = false body=%s", recorder.Body.String())
	}
	if response.EffectiveConfig == nil {
		t.Fatal("effective_config missing after apply")
	}
	if response.EffectiveConfig.Policy.Mode != "single_best" {
		t.Fatalf("effective policy mode = %q", response.EffectiveConfig.Policy.Mode)
	}
	if response.EffectiveConfig.Pool.MaxUse != 7 {
		t.Fatalf("effective pool max_use = %d", response.EffectiveConfig.Pool.MaxUse)
	}
	if response.EffectiveConfig.Server.WebPort != 19091 {
		t.Fatalf("effective server.web_port = %d", response.EffectiveConfig.Server.WebPort)
	}
	if len(response.AppliedLiveFields) == 0 {
		t.Fatal("applied_live_fields missing from mixed apply")
	}
	if len(response.RestartRequiredFields) == 0 {
		t.Fatal("restart_required_fields missing from mixed apply")
	}
	if !response.Receipt.Operation.OK {
		t.Fatalf("receipt.operation.ok = false: %#v", response.Receipt.Operation)
	}
	if !response.Receipt.Persistence.Attempted || !response.Receipt.Persistence.Succeeded {
		t.Fatalf("persistence receipt = %#v", response.Receipt.Persistence)
	}
}

func TestControlPlaneAPIConfigApply_canPersistLANControlPlaneMode(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.ControlPlane.TrustedLocalOnly = false
	desired.Server.WebHost = "0.0.0.0"
	desired.Server.WebPort = 19091

	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	recorder := performLoopbackJSONRequest(t, handler, http.MethodPut, "/api/v1/config", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config apply status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	response := decodeJSONResponse[struct {
		OK              bool           `json:"ok"`
		EffectiveConfig *config.Config `json:"effective_config"`
	}](t, recorder)
	if !response.OK || response.EffectiveConfig == nil {
		t.Fatalf("unexpected apply response: %#v", response)
	}
	if response.EffectiveConfig.ControlPlane.TrustedLocalOnly {
		t.Fatal("effective config did not persist trusted-local-only=false")
	}
	if got := response.EffectiveConfig.Server.WebHost; got != "0.0.0.0" {
		t.Fatalf("effective config web_host = %q", got)
	}
	if manager.Export().EffectiveConfig.ControlPlane.TrustedLocalOnly {
		t.Fatal("persisted effective config did not preserve trusted-local-only=false")
	}
}

func TestControlPlaneAPIPool_returnsInventory_whenPoolContainsMixedEntries(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	readyEntry := promoteObservedTestProxy(srv.pool, "203.0.113.10:1080", 2)
	secondReadyEntry := promoteObservedTestProxy(srv.pool, "203.0.113.11:1080", 2)
	if _, err := srv.pool.ManualBlacklist(secondReadyEntry.Proxy.Addr()); err != nil {
		t.Fatalf("prepare blacklist inventory: %v", err)
	}
	srv.pool.Feed([]*proxy.Proxy{{IP: "203.0.113.12", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-src"}})

	recorder := performLoopbackJSONRequest(t, handler, http.MethodGet, "/api/v1/pool", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pool status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	response := decodeJSONResponse[struct {
		Ready     []proxyInventoryEntryResponse `json:"ready"`
		Buffer    []proxyInventoryEntryResponse `json:"buffer"`
		Blacklist []proxyInventoryEntryResponse `json:"blacklist"`
	}](t, recorder)

	if len(response.Ready) != 1 || response.Ready[0].ProxyID != readyEntry.Proxy.Addr() {
		t.Fatalf("ready inventory = %#v", response.Ready)
	}
	if len(response.Buffer) != 1 || response.Buffer[0].ProxyID != "203.0.113.12:1080" {
		t.Fatalf("buffer inventory = %#v", response.Buffer)
	}
	if len(response.Blacklist) != 1 || response.Blacklist[0].ProxyID != secondReadyEntry.Proxy.Addr() {
		t.Fatalf("blacklist inventory = %#v", response.Blacklist)
	}
}

func TestControlPlaneAPIPoolIncludesReadyActiveLeases_whenReadyProxyIsLeased(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()
	readyEntry := promoteObservedTestProxy(srv.pool, "203.0.113.20:1080", 10)
	lease, err := srv.allocator.Acquire(context.Background(), allocation.AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	defer lease.Finish(allocation.Result{Success: false})

	// When
	recorder := performLoopbackJSONRequest(t, handler, http.MethodGet, "/api/v1/pool", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pool status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Then
	response := decodeJSONResponse[struct {
		Ready []proxyInventoryEntryResponse `json:"ready"`
	}](t, recorder)
	if len(response.Ready) != 1 || response.Ready[0].ProxyID != readyEntry.Proxy.Addr() {
		t.Fatalf("ready inventory = %#v", response.Ready)
	}
	if response.Ready[0].ActiveLeases != 1 {
		t.Fatalf("ready active_leases = %d, want 1", response.Ready[0].ActiveLeases)
	}
}

func TestControlPlaneAPISourceEnableDisable_updatesEffectiveConfig_whenLoopbackRequest(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	disableRecorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	if disableRecorder.Code != http.StatusOK {
		t.Fatalf("disable status = %d body=%s", disableRecorder.Code, disableRecorder.Body.String())
	}
	disableResponse := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
		Receipt struct {
			Operation struct {
				OK bool `json:"ok"`
			} `json:"operation"`
		} `json:"receipt"`
	}](t, disableRecorder)
	if !disableResponse.OK || disableResponse.Source != "alpha" || disableResponse.Enabled {
		t.Fatalf("unexpected disable response: %#v", disableResponse)
	}
	if manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("expected alpha source to be disabled in effective config")
	}

	enableRecorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/sources/alpha/enable", nil)
	if enableRecorder.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", enableRecorder.Code, enableRecorder.Body.String())
	}
	enableResponse := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
	}](t, enableRecorder)
	if !enableResponse.OK || enableResponse.Source != "alpha" || !enableResponse.Enabled {
		t.Fatalf("unexpected enable response: %#v", enableResponse)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("expected alpha source to be enabled in effective config")
	}
}

func TestControlPlaneAPIProxyActions_returnPoolReceipts_whenLoopbackRequest(t *testing.T) {
	t.Run("blacklist", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()
		entry := promoteObservedTestProxy(srv.pool, "203.0.113.20:1080", 2)

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.20:1080/blacklist", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("blacklist status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			ProxyID string `json:"proxy_id"`
			Action  string `json:"action"`
			Receipt string `json:"receipt"`
			Counts  struct {
				Ready     int `json:"ready"`
				Buffer    int `json:"buffer"`
				Blacklist int `json:"blacklist"`
			} `json:"counts"`
		}](t, recorder)
		if !response.OK || response.ProxyID != entry.Proxy.Addr() || response.Action != "blacklist" || response.Receipt != "blacklisted" {
			t.Fatalf("unexpected blacklist response: %#v", response)
		}
		if response.Counts.Ready != 0 || response.Counts.Buffer != 0 || response.Counts.Blacklist != 1 {
			t.Fatalf("unexpected blacklist counts: %#v", response.Counts)
		}
	})

	t.Run("revalidate", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()
		entry := promoteObservedTestProxy(srv.pool, "203.0.113.21:1080", 2)

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.21:1080/revalidate", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("revalidate status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			ProxyID string `json:"proxy_id"`
			Action  string `json:"action"`
			Receipt string `json:"receipt"`
			Counts  struct {
				Ready     int `json:"ready"`
				Buffer    int `json:"buffer"`
				Blacklist int `json:"blacklist"`
			} `json:"counts"`
		}](t, recorder)
		if !response.OK || response.ProxyID != entry.Proxy.Addr() || response.Action != "revalidate" || response.Receipt != "accepted_for_revalidation" {
			t.Fatalf("unexpected revalidate response: %#v", response)
		}
		if response.Counts.Ready != 0 || response.Counts.Buffer != 1 || response.Counts.Blacklist != 0 {
			t.Fatalf("unexpected revalidate counts: %#v", response.Counts)
		}
	})

	t.Run("release", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()
		entry := promoteObservedTestProxy(srv.pool, "203.0.113.22:1080", 2)
		if _, err := srv.pool.ManualBlacklist(entry.Proxy.Addr()); err != nil {
			t.Fatalf("prepare release action: %v", err)
		}

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.22:1080/release", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("release status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			ProxyID string `json:"proxy_id"`
			Action  string `json:"action"`
			Receipt string `json:"receipt"`
			Counts  struct {
				Ready     int `json:"ready"`
				Buffer    int `json:"buffer"`
				Blacklist int `json:"blacklist"`
			} `json:"counts"`
		}](t, recorder)
		if !response.OK || response.ProxyID != entry.Proxy.Addr() || response.Action != "release" || response.Receipt != "released_from_blacklist" {
			t.Fatalf("unexpected release response: %#v", response)
		}
		if response.Counts.Ready != 1 || response.Counts.Buffer != 0 || response.Counts.Blacklist != 0 {
			t.Fatalf("unexpected release counts: %#v", response.Counts)
		}
	})

	t.Run("not found", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.99:1080/blacklist", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("not found status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			Code    string `json:"code"`
			Action  string `json:"action"`
			ProxyID string `json:"proxy_id"`
			Message string `json:"message"`
		}](t, recorder)
		if response.OK || response.Code != "proxy_not_found" || response.Action != "blacklist" || response.ProxyID != "203.0.113.99:1080" || response.Message == "" {
			t.Fatalf("unexpected not-found response: %#v", response)
		}
	})

	t.Run("release capacity conflict", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()
		blockedEntry := promoteObservedTestProxy(srv.pool, "203.0.113.30:1080", 2)
		if _, err := srv.pool.ManualBlacklist(blockedEntry.Proxy.Addr()); err != nil {
			t.Fatalf("prepare release conflict blacklist: %v", err)
		}
		for i := 0; i < 10; i++ {
			promoteObservedTestProxy(srv.pool, "203.0.113.4"+string(rune('0'+i))+":1080", 2)
		}

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.30:1080/release", nil)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("release conflict status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			Code    string `json:"code"`
			Action  string `json:"action"`
			ProxyID string `json:"proxy_id"`
			Message string `json:"message"`
		}](t, recorder)
		if response.OK || response.Code != "ready_pool_full" || response.Action != "release" || response.ProxyID != "203.0.113.30:1080" || response.Message == "" {
			t.Fatalf("unexpected release conflict response: %#v", response)
		}
	})

	t.Run("revalidate capacity conflict", func(t *testing.T) {
		srv, _, _ := newControlPlaneTestServer(t)
		handler := srv.webHandler()
		entry := promoteObservedTestProxy(srv.pool, "203.0.113.31:1080", 2)
		srv.pool.Feed([]*proxy.Proxy{
			{IP: "203.0.113.40", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-0"},
			{IP: "203.0.113.41", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-1"},
			{IP: "203.0.113.42", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-2"},
			{IP: "203.0.113.43", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-3"},
			{IP: "203.0.113.44", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-4"},
			{IP: "203.0.113.45", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-5"},
			{IP: "203.0.113.46", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-6"},
			{IP: "203.0.113.47", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-7"},
			{IP: "203.0.113.48", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-8"},
			{IP: "203.0.113.49", Port: 1080, Protocol: proxy.ProtocolSOCKS5, Source: "buffer-9"},
		})

		recorder := performLoopbackJSONRequest(t, handler, http.MethodPost, "/api/v1/pool/proxies/203.0.113.31:1080/revalidate", nil)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("revalidate conflict status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		response := decodeJSONResponse[struct {
			OK      bool   `json:"ok"`
			Code    string `json:"code"`
			Action  string `json:"action"`
			ProxyID string `json:"proxy_id"`
			Message string `json:"message"`
		}](t, recorder)
		if response.OK || response.Code != "buffer_pool_full" || response.Action != "revalidate" || response.ProxyID != entry.Proxy.Addr() || response.Message == "" {
			t.Fatalf("unexpected revalidate conflict response: %#v", response)
		}
	})
}

func TestControlPlaneAPIMutation_rejectsNonLoopbackRequest_whenTrustedLocalOnlyEnabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.Policy.Mode = "single_best"
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.200:4242"
	req.Host = "127.0.0.1:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("trusted-local rejection status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}](t, recorder)
	if response.OK {
		t.Fatalf("trusted-local rejection unexpectedly ok: %#v", response)
	}
	if response.Code == "" || response.Message == "" {
		t.Fatalf("trusted-local rejection missing structured error: %#v", response)
	}
	if manager.Export().EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatal("remote mutation should not change effective config")
	}
}

func TestControlPlaneAPIMutation_rejectsNonLoopbackRequest_whenForwardedHeadersSpoofLoopback(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.Policy.Mode = "single_best"
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.201:4242"
	req.Host = "127.0.0.1:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("Forwarded", "for=127.0.0.1;proto=http;host=127.0.0.1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed trusted-local rejection status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}](t, recorder)
	if response.OK {
		t.Fatalf("spoofed trusted-local rejection unexpectedly ok: %#v", response)
	}
	if response.Code != "trusted_local_only" {
		t.Fatalf("spoofed trusted-local rejection code = %q, want trusted_local_only", response.Code)
	}
	if response.Message == "" {
		t.Fatalf("spoofed trusted-local rejection missing message: %#v", response)
	}
	if manager.Export().EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatal("forwarded headers must not bypass trusted-local mode")
	}
}

func TestControlPlaneAPIMutation_rejectsDNSRebindingHost_whenTrustedLocalOnlyEnabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "127.0.0.1:4242"
	request.Host = "evil.example:18081"
	request.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	request.Header.Set("Origin", "http://evil.example:18081")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("trusted-local dns rebinding status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "control_host_required" {
		t.Fatalf("unexpected trusted-local dns rebinding response: %#v", response)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("trusted-local dns rebinding request should not change source state")
	}
}

func TestControlPlaneAPIMutation_allowsNonLoopbackRequest_whenTrustedLocalOnlyDisabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()
	disableTrustedLocalOnly(t, manager)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "192.168.1.20:4242"
	request.Host = "192.168.1.10:18081"
	request.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("lan mutation status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
	}](t, recorder)
	if !response.OK || response.Source != "alpha" || response.Enabled {
		t.Fatalf("unexpected lan mutation response: %#v", response)
	}
	if manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("lan mutation did not update source state")
	}
}

func TestControlPlaneAPIMutation_rejectsNonLoopbackWithoutControlHeader_whenTrustedLocalOnlyDisabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()
	disableTrustedLocalOnly(t, manager)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "192.168.1.21:4242"
	request.Host = "192.168.1.10:18081"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing control header LAN status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "control_header_required" {
		t.Fatalf("unexpected missing control header LAN response: %#v", response)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("missing control header LAN request should not change source state")
	}
}

func TestControlPlaneAPIMutation_rejectsDNSRebindingHost_whenTrustedLocalOnlyDisabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()
	disableTrustedLocalOnly(t, manager)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "192.168.1.23:4242"
	request.Host = "evil.example:18081"
	request.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	request.Header.Set("Origin", "http://evil.example:18081")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("dns rebinding host status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "control_host_required" {
		t.Fatalf("unexpected dns rebinding host response: %#v", response)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("dns rebinding host request should not change source state")
	}
}

func TestControlPlaneAPIMutation_rejectsCrossSiteOrigin_whenTrustedLocalOnlyDisabled(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()
	disableTrustedLocalOnly(t, manager)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "192.168.1.22:4242"
	request.Host = "192.168.1.10:18081"
	request.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site LAN status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "cross_site_mutation" {
		t.Fatalf("unexpected cross-site LAN response: %#v", response)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("cross-site LAN request should not change source state")
	}
}

func TestControlPlaneAPIMutation_rejectsLoopbackRequestWithoutControlHeader(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.Policy.Mode = "single_best"
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4242"
	req.Host = "127.0.0.1:18081"
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing control header status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "control_header_required" {
		t.Fatalf("unexpected missing control header response: %#v", response)
	}
	if manager.Export().EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatal("missing control header request should not change effective config")
	}
}

func TestControlPlaneAPIMutation_rejectsCrossSiteOrigin_whenLoopbackRequestHasControlHeader(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	desired := manager.Export().EffectiveConfig
	desired.Policy.Mode = "single_best"
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatalf("marshal desired config: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4242"
	req.Host = "127.0.0.1:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	req.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site origin status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "cross_site_mutation" {
		t.Fatalf("unexpected cross-site origin response: %#v", response)
	}
	if manager.Export().EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatal("cross-site origin request should not change effective config")
	}
}

func TestControlPlaneAPIMutation_rejectsCrossSiteFetchMetadata_whenLoopbackRequestHasControlHeader(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/alpha/disable", nil)
	request.RemoteAddr = "127.0.0.1:4242"
	request.Host = "127.0.0.1:18081"
	request.Header.Set(controlPlaneMutationHeader, controlPlaneMutationHeaderValue)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site fetch metadata status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}](t, recorder)
	if response.OK || response.Code != "cross_site_mutation" {
		t.Fatalf("unexpected cross-site fetch metadata response: %#v", response)
	}
	if !manager.Export().EffectiveConfig.Sources.Enabled["alpha"] {
		t.Fatal("cross-site fetch metadata request should not change source state")
	}
}

func TestControlPlaneMutationHostValidation(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		localWant bool
		lanWant   bool
	}{
		{name: "localhost", host: "localhost:18081", localWant: true, lanWant: false},
		{name: "loopback ipv4", host: "127.0.0.1:18081", localWant: true, lanWant: true},
		{name: "loopback ipv6", host: "[::1]:18081", localWant: true, lanWant: true},
		{name: "lan ipv4", host: "192.168.1.10:18081", localWant: false, lanWant: true},
		{name: "unique local ipv6", host: "[fd00::1]:18081", localWant: false, lanWant: true},
		{name: "public literal", host: "203.0.113.10:18081", localWant: false, lanWant: true},
		{name: "unspecified ipv4", host: "0.0.0.0:18081", localWant: false, lanWant: false},
		{name: "unspecified ipv6", host: "[::]:18081", localWant: false, lanWant: false},
		{name: "dns host", host: "evil.example:18081", localWant: false, lanWant: false},
		{name: "malformed port", host: "127.0.0.1:bad", localWant: false, lanWant: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTrustedLocalMutationHost(tt.host); got != tt.localWant {
				t.Fatalf("isTrustedLocalMutationHost(%q) = %v, want %v", tt.host, got, tt.localWant)
			}
			if got := isTrustedLANMutationHost(tt.host); got != tt.lanWant {
				t.Fatalf("isTrustedLANMutationHost(%q) = %v, want %v", tt.host, got, tt.lanWant)
			}
		})
	}
}

func TestControlPlaneAPIConfigApply_rejectsMalformedJSON_whenMutationRequested(t *testing.T) {
	srv, manager, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	recorder := performLoopbackJSONRequest(t, handler, http.MethodPut, "/api/v1/config", []byte(`{"policy":`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed config status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeJSONResponse[struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}](t, recorder)
	if response.OK || response.Code != "invalid_config_payload" || response.Message == "" {
		t.Fatalf("unexpected malformed config response: %#v", response)
	}
	if manager.Export().EffectiveConfig.Policy.Mode != "balanced" {
		t.Fatal("malformed config payload should not change effective config")
	}
}

func TestControlPlaneAPIDoesNotBreakLegacyStatsRoute_whenManagerConfigured(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	ts := httptest.NewServer(srv.webHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("stats content-type = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stats body: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode stats body: %v body=%s", err, string(body))
	}
	requiredKeys := []string{"pool", "server", "sources", "ready_history", "recent_requests"}
	for _, key := range requiredKeys {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("stats response missing %q field: %#v", key, decoded)
		}
	}
}
