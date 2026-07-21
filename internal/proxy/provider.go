package proxy

import "context"

// ProviderStatus indicates the current health of a proxy source.
type ProviderStatus int

const (
	StatusUnknown  ProviderStatus = iota // initial state, not yet fetched
	StatusOnline                         // last fetch succeeded
	StatusOffline                        // last fetch failed (network error, 403, etc.)
)

// Provider is the interface for free proxy sources.
// Each provider fetches a batch of proxies from its source on demand.
type Provider interface {
	// Name returns a human-readable identifier for this source.
	Name() string

	// Status returns the current health of this source.
	Status() ProviderStatus

	// Fetch retrieves a batch of proxies from the source.
	// Should respect context cancellation for timeouts.
	Fetch(ctx context.Context) ([]*Proxy, error)
}
