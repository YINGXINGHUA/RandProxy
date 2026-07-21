package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/user/randproxy/internal/config"
)

type LiveChangeSet struct {
	Fields          []string
	EffectiveConfig *config.Config
}

type LiveApplier interface {
	Apply(ctx context.Context, change LiveChangeSet) error
}

type ManagerOptions struct {
	LiveApplier LiveApplier
}

type PersistenceStatus struct {
	Attempted    bool
	Succeeded    bool
	OverridePath string
	Error        string
}

type OperationReceipt struct {
	OK      bool
	Noop    bool
	Message string
}

type ApplyResult struct {
	EffectiveConfig       *config.Config
	AppliedLiveFields     []string
	RestartRequiredFields []string
	Persistence           PersistenceStatus
	Receipt               OperationReceipt
}

type ExportedConfig struct {
	BasePath        string
	OverridePath    string
	EffectiveConfig *config.Config
}

type Manager struct {
	mu           sync.RWMutex
	basePath     string
	overridePath string
	effective    *config.Config
	liveApplier  LiveApplier
}

func NewManager(basePath string, options ManagerOptions) (*Manager, error) {
	effective, err := config.LoadEffective(basePath)
	if err != nil {
		return nil, err
	}
	return &Manager{
		basePath:     basePath,
		overridePath: config.DefaultOverridePath(basePath),
		effective:    effective,
		liveApplier:  options.LiveApplier,
	}, nil
}

func (m *Manager) Export() ExportedConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return ExportedConfig{
		BasePath:        m.basePath,
		OverridePath:    m.overridePath,
		EffectiveConfig: cloneConfigOrPanic(m.effective),
	}
}

func (m *Manager) Apply(ctx context.Context, desired *config.Config) (ApplyResult, error) {
	if desired == nil {
		return ApplyResult{}, fmt.Errorf("controlplane: desired config is required")
	}
	desiredCopy, err := cloneConfig(desired)
	if err != nil {
		return ApplyResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	currentCopy, err := cloneConfig(m.effective)
	if err != nil {
		return ApplyResult{}, err
	}
	changedFields, err := diffConfigPaths(currentCopy, desiredCopy)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(changedFields) == 0 {
		return ApplyResult{
			EffectiveConfig:       currentCopy,
			AppliedLiveFields:     []string{},
			RestartRequiredFields: []string{},
			Persistence: PersistenceStatus{
				Attempted:    false,
				Succeeded:    false,
				OverridePath: m.overridePath,
			},
			Receipt: OperationReceipt{
				OK:      true,
				Noop:    true,
				Message: "no config changes detected",
			},
		}, nil
	}

	appliedLiveFields, restartRequiredFields := classifyChangedFields(changedFields)
	persistence := PersistenceStatus{
		Attempted:    true,
		Succeeded:    false,
		OverridePath: m.overridePath,
	}
	if _, err := config.SaveOverride(m.basePath, desiredCopy); err != nil {
		persistence.Error = err.Error()
		return ApplyResult{
			EffectiveConfig:       currentCopy,
			AppliedLiveFields:     []string{},
			RestartRequiredFields: []string{},
			Persistence:           persistence,
			Receipt: OperationReceipt{
				OK:      false,
				Message: "override persistence failed; live apply skipped",
			},
		}, nil
	}
	persistence.Succeeded = true

	persistedConfig, err := config.LoadEffective(m.basePath)
	if err != nil {
		return ApplyResult{}, err
	}
	m.effective = persistedConfig

	if len(appliedLiveFields) > 0 && m.liveApplier != nil {
		if err := m.liveApplier.Apply(ctx, LiveChangeSet{
			Fields:          append([]string(nil), appliedLiveFields...),
			EffectiveConfig: cloneConfigOrPanic(persistedConfig),
		}); err != nil {
			return ApplyResult{
				EffectiveConfig:       cloneConfigOrPanic(persistedConfig),
				AppliedLiveFields:     append([]string(nil), appliedLiveFields...),
				RestartRequiredFields: append([]string(nil), restartRequiredFields...),
				Persistence:           persistence,
				Receipt: OperationReceipt{
					OK:      false,
					Message: fmt.Sprintf("override persisted but live apply failed: %v", err),
				},
			}, nil
		}
	}

	return ApplyResult{
		EffectiveConfig:       cloneConfigOrPanic(persistedConfig),
		AppliedLiveFields:     append([]string(nil), appliedLiveFields...),
		RestartRequiredFields: append([]string{}, restartRequiredFields...),
		Persistence:           persistence,
		Receipt: OperationReceipt{
			OK:      true,
			Message: buildApplyReceiptMessage(appliedLiveFields, restartRequiredFields),
		},
	}, nil
}

func classifyChangedFields(paths []string) ([]string, []string) {
	liveFields := make([]string, 0, len(paths))
	restartRequiredFields := make([]string, 0, len(paths))
	for _, path := range paths {
		if isLiveField(path) {
			liveFields = append(liveFields, path)
			continue
		}
		restartRequiredFields = append(restartRequiredFields, path)
	}
	sort.Strings(liveFields)
	sort.Strings(restartRequiredFields)
	return liveFields, restartRequiredFields
}

func isLiveField(path string) bool {
	switch path {
	case "policy.mode", "policy.random_subset_size", "policy.stable_subset_size", "pool.max_use", "pool.blacklist_ttl":
		return true
	default:
		return strings.HasPrefix(path, "sources.enabled.")
	}
}

func buildApplyReceiptMessage(appliedLiveFields []string, restartRequiredFields []string) string {
	parts := make([]string, 0, 2)
	if len(appliedLiveFields) > 0 {
		parts = append(parts, fmt.Sprintf("live-applied %d field(s)", len(appliedLiveFields)))
	}
	if len(restartRequiredFields) > 0 {
		parts = append(parts, fmt.Sprintf("persisted %d restart-required field(s)", len(restartRequiredFields)))
	}
	if len(parts) == 0 {
		return "config persisted"
	}
	return strings.Join(parts, "; ")
}
