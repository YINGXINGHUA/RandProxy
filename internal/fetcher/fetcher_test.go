package fetcher

import (
	"context"
	"testing"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

const testWaitTimeout = 250 * time.Millisecond

type fakeProvider struct {
	name      string
	fetches   chan int
	responses []*proxy.Proxy
	count     int
}

func newFakeProvider(name string) *fakeProvider {
	return &fakeProvider{
		name:    name,
		fetches: make(chan int, 16),
		responses: []*proxy.Proxy{{
			IP:       "203.0.113.10",
			Port:     1080,
			Protocol: proxy.ProtocolSOCKS5,
			Source:   name,
		}},
	}
}

func (p *fakeProvider) Name() string {
	return p.name
}

func (p *fakeProvider) Status() proxy.ProviderStatus {
	return proxy.StatusOnline
}

func (p *fakeProvider) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	p.count++
	select {
	case p.fetches <- p.count:
	default:
	}
	return p.responses, nil
}

func TestFetcher_skipsFutureFetchTicks_whenSourceDisabled(t *testing.T) {
	// Given
	fetcher := New()
	provider := newFakeProvider("alpha")
	fetcher.Add(provider, 40*time.Millisecond, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fetcher.Run(ctx)
	defer fetcher.Stop()

	waitForFetchCall(t, provider.fetches)
	waitForBatch(t, fetcher.Out())

	// When
	fetcher.SetSourceEnabled(provider.Name(), false)

	// Then
	assertNoFetchCall(t, provider.fetches, 150*time.Millisecond)
	assertNoBatch(t, fetcher.Out(), 150*time.Millisecond)
}

func TestFetcher_resumesFetchTicks_whenSourceReEnabled(t *testing.T) {
	// Given
	fetcher := New()
	provider := newFakeProvider("alpha")
	fetcher.Add(provider, 40*time.Millisecond, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fetcher.Run(ctx)
	defer fetcher.Stop()

	waitForFetchCall(t, provider.fetches)
	waitForBatch(t, fetcher.Out())
	fetcher.SetSourceEnabled(provider.Name(), false)
	assertNoFetchCall(t, provider.fetches, 150*time.Millisecond)
	assertNoBatch(t, fetcher.Out(), 150*time.Millisecond)

	// When
	fetcher.SetSourceEnabled(provider.Name(), true)

	// Then
	waitForFetchCall(t, provider.fetches)
	waitForBatch(t, fetcher.Out())
	stats := fetcher.SourceStats()
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	if !stats[0].Enabled {
		t.Fatalf("stats enabled = %v, want true", stats[0].Enabled)
	}
	t.Logf("manual QA receipt: source=%s disabled_window_batches=0 resumed_batches=1", provider.Name())
}

func TestFetcher_SourceStats_remainReadable_whenSourceDisabled(t *testing.T) {
	// Given
	fetcher := New()
	provider := newFakeProvider("alpha")
	fetcher.Add(provider, 40*time.Millisecond, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fetcher.Run(ctx)
	defer fetcher.Stop()

	waitForFetchCall(t, provider.fetches)
	waitForBatch(t, fetcher.Out())

	// When
	fetcher.SetSourceEnabled(provider.Name(), false)
	assertNoFetchCall(t, provider.fetches, 150*time.Millisecond)
	stats := fetcher.SourceStats()

	// Then
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	if stats[0].Name != provider.Name() {
		t.Fatalf("stats name = %q, want %q", stats[0].Name, provider.Name())
	}
	if stats[0].Fetched == 0 {
		t.Fatalf("stats fetched = %d, want > 0", stats[0].Fetched)
	}
	if stats[0].LastFetch == "" {
		t.Fatalf("stats last fetch is empty")
	}
	if stats[0].Enabled {
		t.Fatalf("stats enabled = %v, want false", stats[0].Enabled)
	}
}

func waitForFetchCall(t *testing.T, calls <-chan int) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for fetch call")
	}
}

func waitForBatch(t *testing.T, out <-chan []*proxy.Proxy) {
	t.Helper()
	select {
	case batch := <-out:
		if len(batch) == 0 {
			t.Fatal("batch len = 0, want > 0")
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for fetch batch")
	}
}

func assertNoFetchCall(t *testing.T, calls <-chan int, duration time.Duration) {
	t.Helper()
	select {
	case count := <-calls:
		t.Fatalf("unexpected fetch call #%d while source disabled", count)
	case <-time.After(duration):
	}
}

func assertNoBatch(t *testing.T, out <-chan []*proxy.Proxy, duration time.Duration) {
	t.Helper()
	select {
	case batch := <-out:
		t.Fatalf("unexpected batch while source disabled: len=%d", len(batch))
	case <-time.After(duration):
	}
}
