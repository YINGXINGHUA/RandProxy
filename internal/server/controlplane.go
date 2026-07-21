package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/config"
	"github.com/user/randproxy/internal/controlplane"
	"github.com/user/randproxy/internal/pool"
)

var (
	controlPlaneLiveFields = []string{
		"policy.mode",
		"policy.random_subset_size",
		"policy.stable_subset_size",
		"pool.max_use",
		"pool.blacklist_ttl",
		"sources.enabled.*",
	}
	controlPlaneRestartRequiredFields = []string{
		"server.*",
		"validator.*",
		"health.*",
		"log.*",
		"pool.min_ready",
		"pool.max_ready",
		"pool.buffer_max",
		"pool.state_file",
	}
	poolMutationActions = []string{"blacklist", "revalidate", "release"}
)

const (
	controlPlaneMutationHeader      = "X-RandProxy-Control"
	controlPlaneMutationHeaderValue = "1"
)

type controlPlaneState struct {
	mu               sync.RWMutex
	manager          *controlplane.Manager
	lastApplyReceipt *applyReceiptSnapshot
}

type configMetadataResponse struct {
	BasePath              string         `json:"base_path"`
	OverridePath          string         `json:"override_path"`
	EffectiveConfig       *config.Config `json:"effective_config"`
	LiveFields            []string       `json:"live_fields"`
	RestartRequiredFields []string       `json:"restart_required_fields"`
}

type applyResponse struct {
	OK                    bool                `json:"ok"`
	EffectiveConfig       *config.Config      `json:"effective_config"`
	AppliedLiveFields     []string            `json:"applied_live_fields"`
	RestartRequiredFields []string            `json:"restart_required_fields"`
	Receipt               applyReceiptPayload `json:"receipt"`
}

type applyReceiptPayload struct {
	Operation   controlplane.OperationReceipt  `json:"operation"`
	Persistence controlplane.PersistenceStatus `json:"persistence"`
}

type applyReceiptSnapshot struct {
	AppliedLiveFields     []string            `json:"applied_live_fields"`
	RestartRequiredFields []string            `json:"restart_required_fields"`
	Receipt               applyReceiptPayload `json:"receipt"`
}

type overviewResponse struct {
	Overview            map[string]any        `json:"overview"`
	EffectiveConfigMeta overviewConfigMeta    `json:"effective_config_meta"`
	LastApplyReceipt    *applyReceiptSnapshot `json:"last_apply_receipt"`
	RestartRequired     []string              `json:"restart_required"`
}

type overviewConfigMeta struct {
	BasePath     string `json:"base_path"`
	OverridePath string `json:"override_path"`
}

type sourceMutationResponse struct {
	OK      bool                `json:"ok"`
	Source  string              `json:"source"`
	Enabled bool                `json:"enabled"`
	Receipt applyReceiptPayload `json:"receipt"`
}

type poolCountsResponse struct {
	Ready     int `json:"ready"`
	Buffer    int `json:"buffer"`
	Blacklist int `json:"blacklist"`
}

type proxyActionResponse struct {
	OK      bool               `json:"ok"`
	ProxyID string             `json:"proxy_id"`
	Action  string             `json:"action"`
	Receipt string             `json:"receipt"`
	Counts  poolCountsResponse `json:"counts"`
}

type proxyActionErrorResponse struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Action  string `json:"action"`
	ProxyID string `json:"proxy_id"`
	Message string `json:"message"`
}

type poolInventoryResponse struct {
	Ready     []proxyInventoryEntryResponse `json:"ready"`
	Buffer    []proxyInventoryEntryResponse `json:"buffer"`
	Blacklist []proxyInventoryEntryResponse `json:"blacklist"`
}

type proxyInventoryEntryResponse struct {
	ProxyID          string `json:"proxy_id"`
	IP               string `json:"ip"`
	Port             int    `json:"port"`
	Protocol         string `json:"protocol"`
	Source           string `json:"source"`
	Status           string `json:"status"`
	UseCount         int    `json:"use_count"`
	MaxUse           int    `json:"max_use"`
	AddedAt          string `json:"added_at,omitempty"`
	LastUsed         string `json:"last_used,omitempty"`
	BlacklistedUntil string `json:"blacklisted_until,omitempty"`
	ActiveLeases     int    `json:"active_leases"`
}

type apiErrorResponse struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type controlPlaneRequestError struct {
	status  int
	payload any
}

func (e *controlPlaneRequestError) Error() string {
	return http.StatusText(e.status)
}

func (s *ProxyServer) SetControlPlaneManager(manager *controlplane.Manager) {
	state := s.ensureControlPlaneState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.manager = manager
	state.lastApplyReceipt = nil
}

func (s *ProxyServer) ensureControlPlaneState() *controlPlaneState {
	if s.controlPlane != nil {
		return s.controlPlane
	}
	s.controlPlane = &controlPlaneState{}
	return s.controlPlane
}

func (s *ProxyServer) controlPlaneManager() *controlplane.Manager {
	if s.controlPlane == nil {
		return nil
	}
	s.controlPlane.mu.RLock()
	defer s.controlPlane.mu.RUnlock()
	return s.controlPlane.manager
}

func (s *ProxyServer) lastApplyReceipt() *applyReceiptSnapshot {
	if s.controlPlane == nil {
		return nil
	}
	s.controlPlane.mu.RLock()
	defer s.controlPlane.mu.RUnlock()
	if s.controlPlane.lastApplyReceipt == nil {
		return nil
	}
	copyValue := *s.controlPlane.lastApplyReceipt
	copyValue.AppliedLiveFields = append([]string(nil), copyValue.AppliedLiveFields...)
	copyValue.RestartRequiredFields = append([]string(nil), copyValue.RestartRequiredFields...)
	return &copyValue
}

func (s *ProxyServer) storeApplyReceipt(result controlplane.ApplyResult) {
	state := s.ensureControlPlaneState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastApplyReceipt = &applyReceiptSnapshot{
		AppliedLiveFields:     append([]string(nil), result.AppliedLiveFields...),
		RestartRequiredFields: append([]string(nil), result.RestartRequiredFields...),
		Receipt:               newApplyReceiptPayload(result),
	}
}

func (s *ProxyServer) overviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	exported, err := s.requireControlPlaneExport()
	if err != nil {
		writeControlPlaneError(w, err)
		return
	}

	lastReceipt := s.lastApplyReceipt()
	restartRequired := []string{}
	if lastReceipt != nil {
		restartRequired = append(restartRequired, lastReceipt.RestartRequiredFields...)
	}

	writeJSON(w, http.StatusOK, overviewResponse{
		Overview: s.currentOverview(exported.EffectiveConfig),
		EffectiveConfigMeta: overviewConfigMeta{
			BasePath:     exported.BasePath,
			OverridePath: exported.OverridePath,
		},
		LastApplyReceipt: lastReceipt,
		RestartRequired:  restartRequired,
	})
}

func (s *ProxyServer) configHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetConfig(w)
	case http.MethodPut:
		s.handlePutConfig(w, r)
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *ProxyServer) poolHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, s.poolInventorySnapshot())
}

func (s *ProxyServer) poolProxyActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if err := s.enforceTrustedLocalMutation(r); err != nil {
		writeControlPlaneError(w, err)
		return
	}

	proxyID, action, err := parsePoolProxyActionPath(r.URL.Path)
	if err != nil {
		writeControlPlaneError(w, err)
		return
	}

	var (
		result    pool.ProxyActionResult
		actionErr error
	)
	switch action {
	case "blacklist":
		result, actionErr = s.pool.ManualBlacklist(proxyID)
	case "revalidate":
		result, actionErr = s.pool.ManualRevalidate(proxyID)
	case "release":
		result, actionErr = s.pool.ManualRelease(proxyID)
	default:
		writeJSON(w, http.StatusNotFound, apiErrorResponse{OK: false, Code: "route_not_found", Message: "proxy action route was not found"})
		return
	}
	if actionErr != nil {
		var proxyActionErr *pool.ProxyActionNotFoundError
		if errors.As(actionErr, &proxyActionErr) {
			writeJSON(w, http.StatusNotFound, proxyActionErrorResponse{
				OK:      false,
				Code:    "proxy_not_found",
				Action:  proxyActionErr.Action,
				ProxyID: proxyActionErr.ProxyID,
				Message: proxyActionErr.ErrorMessage(),
			})
			return
		}
		var proxyCapacityErr *pool.ProxyActionCapacityError
		if errors.As(actionErr, &proxyCapacityErr) {
			writeJSON(w, http.StatusConflict, proxyActionErrorResponse{
				OK:      false,
				Code:    proxyCapacityErr.ErrorCode(),
				Action:  proxyCapacityErr.Action,
				ProxyID: proxyCapacityErr.ProxyID,
				Message: proxyCapacityErr.ErrorMessage(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{OK: false, Code: "proxy_action_failed", Message: actionErr.Error()})
		return
	}

	writeJSON(w, http.StatusOK, newProxyActionResponse(result))
}

func (s *ProxyServer) sourceMutationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if err := s.enforceTrustedLocalMutation(r); err != nil {
		writeControlPlaneError(w, err)
		return
	}

	sourceName, enabled, err := parseSourceMutationPath(r.URL.Path)
	if err != nil {
		writeControlPlaneError(w, err)
		return
	}

	manager := s.controlPlaneManager()
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{OK: false, Code: "control_plane_unavailable", Message: "control-plane manager is not configured"})
		return
	}

	desired := manager.Export().EffectiveConfig
	if desired.Sources.Enabled == nil {
		desired.Sources.Enabled = map[string]bool{}
	}
	desired.Sources.Enabled[sourceName] = enabled

	result, applyErr := manager.Apply(r.Context(), desired)
	if applyErr != nil {
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{OK: false, Code: "config_apply_failed", Message: applyErr.Error()})
		return
	}
	s.storeApplyReceipt(result)

	writeJSON(w, http.StatusOK, sourceMutationResponse{
		OK:      result.Receipt.OK,
		Source:  sourceName,
		Enabled: enabled,
		Receipt: newApplyReceiptPayload(result),
	})
}

func (s *ProxyServer) handleGetConfig(w http.ResponseWriter) {
	exported, err := s.requireControlPlaneExport()
	if err != nil {
		writeControlPlaneError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, configMetadataResponse{
		BasePath:              exported.BasePath,
		OverridePath:          exported.OverridePath,
		EffectiveConfig:       exported.EffectiveConfig,
		LiveFields:            append([]string(nil), controlPlaneLiveFields...),
		RestartRequiredFields: append([]string(nil), controlPlaneRestartRequiredFields...),
	})
}

func (s *ProxyServer) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.enforceTrustedLocalMutation(r); err != nil {
		writeControlPlaneError(w, err)
		return
	}

	manager := s.controlPlaneManager()
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{OK: false, Code: "control_plane_unavailable", Message: "control-plane manager is not configured"})
		return
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var desired config.Config
	if err := decoder.Decode(&desired); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{OK: false, Code: "invalid_config_payload", Message: err.Error()})
		return
	}
	if err := ensureRequestBodyFullyConsumed(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{OK: false, Code: "invalid_config_payload", Message: err.Error()})
		return
	}

	result, applyErr := manager.Apply(r.Context(), &desired)
	if applyErr != nil {
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{OK: false, Code: "config_apply_failed", Message: applyErr.Error()})
		return
	}
	s.storeApplyReceipt(result)

	writeJSON(w, http.StatusOK, applyResponse{
		OK:                    result.Receipt.OK,
		EffectiveConfig:       result.EffectiveConfig,
		AppliedLiveFields:     append([]string(nil), result.AppliedLiveFields...),
		RestartRequiredFields: append([]string(nil), result.RestartRequiredFields...),
		Receipt:               newApplyReceiptPayload(result),
	})
}

func (s *ProxyServer) requireControlPlaneExport() (controlplane.ExportedConfig, error) {
	manager := s.controlPlaneManager()
	if manager == nil {
		return controlplane.ExportedConfig{}, &controlPlaneRequestError{
			status:  http.StatusServiceUnavailable,
			payload: apiErrorResponse{OK: false, Code: "control_plane_unavailable", Message: "control-plane manager is not configured"},
		}
	}
	return manager.Export(), nil
}

func (s *ProxyServer) currentOverview(effectiveConfig *config.Config) map[string]any {
	overview := map[string]any{
		"pool": map[string]int{
			"buffer":    s.pool.BufferCount(),
			"ready":     s.pool.ReadyCount(),
			"blacklist": s.pool.BlacklistCount(),
		},
	}
	if s.stats != nil {
		_ = json.Unmarshal(s.stats.collect(s.pool, s.activeLeaseSnapshot().Total), &overview)
	}
	if effectiveConfig != nil {
		overview["policy"] = effectiveConfig.Policy
		overview["control_plane"] = effectiveConfig.ControlPlane
	}
	return overview
}

func (s *ProxyServer) activeLeaseSnapshot() allocation.ActiveLeaseSnapshot {
	snapshotter, ok := s.allocator.(allocation.ActiveLeaseSnapshotter)
	if !ok {
		return allocation.ActiveLeaseSnapshot{}
	}
	return snapshotter.ActiveLeaseSnapshot()
}

func (s *ProxyServer) poolInventorySnapshot() poolInventoryResponse {
	snapshot := s.pool.InventorySnapshot()
	leaseSnapshot := s.activeLeaseSnapshot()
	return poolInventoryResponse{
		Ready:     newProxyInventoryEntryResponses(snapshot.Ready, leaseSnapshot.ByProxy),
		Buffer:    newProxyInventoryEntryResponses(snapshot.Buffer, leaseSnapshot.ByProxy),
		Blacklist: newProxyInventoryEntryResponses(snapshot.Blacklist, leaseSnapshot.ByProxy),
	}
}

func newProxyInventoryEntryResponses(entries []pool.InventoryEntry, activeLeases map[string]int) []proxyInventoryEntryResponse {
	response := make([]proxyInventoryEntryResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, proxyInventoryEntryResponse{
			ProxyID:          entry.ProxyID,
			IP:               entry.IP,
			Port:             entry.Port,
			Protocol:         entry.Protocol,
			Source:           entry.Source,
			Status:           entry.Status,
			UseCount:         entry.UseCount,
			MaxUse:           entry.MaxUse,
			AddedAt:          entry.AddedAt,
			LastUsed:         entry.LastUsed,
			BlacklistedUntil: entry.BlacklistedUntil,
			ActiveLeases:     activeLeases[entry.ProxyID],
		})
	}
	return response
}

func (s *ProxyServer) enforceTrustedLocalMutation(r *http.Request) error {
	exported, err := s.requireControlPlaneExport()
	if err != nil {
		return err
	}
	if exported.EffectiveConfig.ControlPlane.TrustedLocalOnly {
		if !isLoopbackRemoteAddr(r.Context(), r.RemoteAddr) {
			return &controlPlaneRequestError{
				status:  http.StatusForbidden,
				payload: apiErrorResponse{OK: false, Code: "trusted_local_only", Message: "mutation endpoints only accept loopback clients in trusted-local mode"},
			}
		}
		if !isTrustedLocalMutationHost(r.Host) {
			return &controlPlaneRequestError{
				status:  http.StatusForbidden,
				payload: apiErrorResponse{OK: false, Code: "control_host_required", Message: "trusted-local mutation endpoints require localhost or loopback IP Host header"},
			}
		}
	} else if !isTrustedLANMutationHost(r.Host) {
		return &controlPlaneRequestError{
			status:  http.StatusForbidden,
			payload: apiErrorResponse{OK: false, Code: "control_host_required", Message: "LAN mutation endpoints require an IP-literal Host header"},
		}
	}
	if err := rejectCrossSiteMutation(r); err != nil {
		return err
	}
	if r.Header.Get(controlPlaneMutationHeader) != controlPlaneMutationHeaderValue {
		return &controlPlaneRequestError{
			status:  http.StatusForbidden,
			payload: apiErrorResponse{OK: false, Code: "control_header_required", Message: "mutation endpoints require the RandProxy control header"},
		}
	}
	return nil
}

func isTrustedLocalMutationHost(hostHeader string) bool {
	host := mutationHostName(hostHeader)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isTrustedLANMutationHost(hostHeader string) bool {
	host := mutationHostName(hostHeader)
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsUnspecified()
}

func mutationHostName(hostHeader string) string {
	host := strings.TrimSpace(hostHeader)
	if parsedHost, port, err := net.SplitHostPort(host); err == nil {
		if !isValidHostPort(port) {
			return ""
		}
		return parsedHost
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return ""
	}
	return host
}

func isValidHostPort(port string) bool {
	value, err := strconv.Atoi(port)
	return err == nil && value > 0 && value <= 65535
}

func rejectCrossSiteMutation(r *http.Request) error {
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site == "cross-site" {
		return crossSiteMutationError()
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !requestOriginMatchesHost(origin, r.Host) {
		return crossSiteMutationError()
	}
	return nil
}

func requestOriginMatchesHost(origin string, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

func crossSiteMutationError() error {
	return &controlPlaneRequestError{
		status:  http.StatusForbidden,
		payload: apiErrorResponse{OK: false, Code: "cross_site_mutation", Message: "mutation endpoints reject cross-site browser requests"},
	}
}

func isLoopbackRemoteAddr(ctx context.Context, remoteAddr string) bool {
	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return false
	}
	for _, ip := range resolved {
		if ip.IsLoopback() {
			return true
		}
	}
	return false
}

func parseSourceMutationPath(path string) (string, bool, error) {
	const prefix = "/api/v1/sources/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, &controlPlaneRequestError{status: http.StatusNotFound, payload: apiErrorResponse{OK: false, Code: "route_not_found", Message: "source route was not found"}}
	}
	remainder := strings.TrimPrefix(path, prefix)
	enabled := false
	var namePart string
	switch {
	case strings.HasSuffix(remainder, "/enable"):
		enabled = true
		namePart = strings.TrimSuffix(remainder, "/enable")
	case strings.HasSuffix(remainder, "/disable"):
		namePart = strings.TrimSuffix(remainder, "/disable")
	default:
		return "", false, &controlPlaneRequestError{status: http.StatusNotFound, payload: apiErrorResponse{OK: false, Code: "route_not_found", Message: "source route was not found"}}
	}
	decoded, err := url.PathUnescape(namePart)
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "", false, &controlPlaneRequestError{status: http.StatusBadRequest, payload: apiErrorResponse{OK: false, Code: "invalid_source_name", Message: "source name is invalid"}}
	}
	return decoded, enabled, nil
}

func parsePoolProxyActionPath(path string) (string, string, error) {
	const prefix = "/api/v1/pool/proxies/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", &controlPlaneRequestError{status: http.StatusNotFound, payload: apiErrorResponse{OK: false, Code: "route_not_found", Message: "proxy route was not found"}}
	}
	remainder := strings.TrimPrefix(path, prefix)
	for _, action := range poolMutationActions {
		suffix := "/" + action
		if !strings.HasSuffix(remainder, suffix) {
			continue
		}
		idPart := strings.TrimSuffix(remainder, suffix)
		decoded, err := url.PathUnescape(idPart)
		if err != nil || strings.TrimSpace(decoded) == "" {
			return "", "", &controlPlaneRequestError{status: http.StatusBadRequest, payload: apiErrorResponse{OK: false, Code: "invalid_proxy_id", Message: "proxy id is invalid"}}
		}
		return decoded, action, nil
	}
	return "", "", &controlPlaneRequestError{status: http.StatusNotFound, payload: apiErrorResponse{OK: false, Code: "route_not_found", Message: "proxy route was not found"}}
}

func ensureRequestBodyFullyConsumed(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("request body must contain exactly one JSON document")
}

func newApplyReceiptPayload(result controlplane.ApplyResult) applyReceiptPayload {
	return applyReceiptPayload{Operation: result.Receipt, Persistence: result.Persistence}
}

func newProxyActionResponse(result pool.ProxyActionResult) proxyActionResponse {
	return proxyActionResponse{
		OK:      true,
		ProxyID: result.ProxyID,
		Action:  result.Action,
		Receipt: result.Outcome,
		Counts: poolCountsResponse{
			Ready:     result.Counts.Ready,
			Buffer:    result.Counts.Buffer,
			Blacklist: result.Counts.Blacklist,
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, apiErrorResponse{OK: false, Code: "method_not_allowed", Message: "request method is not allowed on this endpoint"})
}

func writeControlPlaneError(w http.ResponseWriter, err error) {
	var requestErr *controlPlaneRequestError
	if errors.As(err, &requestErr) {
		writeJSON(w, requestErr.status, requestErr.payload)
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiErrorResponse{OK: false, Code: "internal_error", Message: err.Error()})
}
