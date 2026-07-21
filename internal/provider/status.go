package provider

import (
	"sync"

	"github.com/user/randproxy/internal/proxy"
)

// statusField provides thread-safe access to a provider status.
// Embeds sync.Mutex; use get/set instead of direct field access.
type statusField struct {
	mu     sync.Mutex
	status proxy.ProviderStatus
}

func (s *statusField) get() proxy.ProviderStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *statusField) set(v proxy.ProviderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = v
}
