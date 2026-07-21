package pool

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

func newTestPoolForRuntimeControl() *Pool {
	return New(1, 10, 25, 10, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
}

func newRuntimeControlEntry(ip string, port int, source string, status Status) *Entry {
	baseTime := time.Date(2026, time.July, 6, 12, 0, 0, 0, time.UTC)
	entry := &Entry{
		Proxy: &proxy.Proxy{
			IP:       ip,
			Port:     port,
			Protocol: proxy.ProtocolSOCKS5,
			Source:   source,
		},
		Status:          status,
		UseCount:        3,
		MaxUse:          25,
		AddedAt:         baseTime,
		LastUsed:        baseTime.Add(15 * time.Minute),
		LatencyEWMA:     120 * time.Millisecond,
		LatencyVariance: 10 * time.Millisecond,
		LatencyCount:    3,
	}
	if status == StatusBlacklisted {
		entry.BlacklistedUntil = baseTime.Add(2 * time.Hour)
	}
	return entry
}

func TestPoolInventorySnapshotReturnsStableCategorizedEntries_whenPoolsContainMixedEntries(t *testing.T) {
	pool := newTestPoolForRuntimeControl()
	pool.ready = []*Entry{
		newRuntimeControlEntry("203.0.113.20", 1080, "beta", StatusReady),
		newRuntimeControlEntry("203.0.113.10", 1080, "alpha", StatusReady),
	}
	pool.buffer = []*Entry{
		newRuntimeControlEntry("203.0.113.40", 1080, "delta", StatusBuffer),
		newRuntimeControlEntry("203.0.113.30", 1080, "charlie", StatusBuffer),
	}
	pool.blacklist = []*Entry{
		newRuntimeControlEntry("203.0.113.60", 1080, "foxtrot", StatusBlacklisted),
		newRuntimeControlEntry("203.0.113.50", 1080, "echo", StatusBlacklisted),
	}

	first := pool.InventorySnapshot()
	second := pool.InventorySnapshot()

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected stable inventory snapshot across reads")
	}

	if got, want := inventoryProxyIDs(first.Ready), []string{"203.0.113.10:1080", "203.0.113.20:1080"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected ready ids: got %v want %v", got, want)
	}
	if got, want := inventoryProxyIDs(first.Buffer), []string{"203.0.113.30:1080", "203.0.113.40:1080"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected buffer ids: got %v want %v", got, want)
	}
	if got, want := inventoryProxyIDs(first.Blacklist), []string{"203.0.113.50:1080", "203.0.113.60:1080"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected blacklist ids: got %v want %v", got, want)
	}

	if first.Ready[0].Status != "ready" {
		t.Fatalf("expected ready status, got %q", first.Ready[0].Status)
	}
	if first.Buffer[0].Status != "buffer" {
		t.Fatalf("expected buffer status, got %q", first.Buffer[0].Status)
	}
	if first.Blacklist[0].Status != "blacklisted" {
		t.Fatalf("expected blacklisted status, got %q", first.Blacklist[0].Status)
	}
	if first.Blacklist[0].BlacklistedUntil == "" {
		t.Fatal("expected blacklist entry to include blacklist deadline")
	}
}

func TestPoolManualBlacklistMovesReadyEntryToBlacklist_whenProxyIDExists(t *testing.T) {
	pool := newTestPoolForRuntimeControl()
	entry := newRuntimeControlEntry("203.0.113.10", 1080, "alpha", StatusReady)
	pool.ready = []*Entry{entry}
	pool.known[entry.Proxy.Addr()] = true

	result, err := pool.ManualBlacklist(entry.Proxy.Addr())
	if err != nil {
		t.Fatalf("expected blacklist action to succeed: %v", err)
	}

	if result.ProxyID != entry.Proxy.Addr() {
		t.Fatalf("unexpected proxy id %q", result.ProxyID)
	}
	if result.Action != "blacklist" {
		t.Fatalf("unexpected action %q", result.Action)
	}
	if result.Outcome != "blacklisted" {
		t.Fatalf("unexpected outcome %q", result.Outcome)
	}
	if entry.Status != StatusBlacklisted {
		t.Fatalf("expected entry to become blacklisted, got %v", entry.Status)
	}
	if entry.BlacklistedUntil.IsZero() {
		t.Fatal("expected blacklist deadline to be set")
	}
	if got := result.Counts; got != (PoolCounts{Ready: 0, Buffer: 0, Blacklist: 1}) {
		t.Fatalf("unexpected counts after blacklist: %+v", got)
	}
}

func TestPoolManualRevalidateEnqueuesEntryForValidation_whenProxyIDExists(t *testing.T) {
	t.Run("from ready", func(t *testing.T) {
		pool := newTestPoolForRuntimeControl()
		entry := newRuntimeControlEntry("203.0.113.11", 1080, "alpha", StatusReady)
		pool.ready = []*Entry{entry}
		pool.known[entry.Proxy.Addr()] = true

		result, err := pool.ManualRevalidate(entry.Proxy.Addr())
		if err != nil {
			t.Fatalf("expected revalidate action to succeed: %v", err)
		}

		if result.ProxyID != entry.Proxy.Addr() {
			t.Fatalf("unexpected proxy id %q", result.ProxyID)
		}
		if result.Outcome != "accepted_for_revalidation" {
			t.Fatalf("unexpected outcome %q", result.Outcome)
		}
		if entry.Status != StatusBuffer {
			t.Fatalf("expected entry to move to buffer, got %v", entry.Status)
		}
		if entry.UseCount != 0 {
			t.Fatalf("expected revalidated entry use count reset, got %d", entry.UseCount)
		}
		if entry.BlacklistedUntil.IsZero() == false {
			t.Fatal("expected revalidated entry blacklist deadline cleared")
		}
		if got := result.Counts; got != (PoolCounts{Ready: 0, Buffer: 1, Blacklist: 0}) {
			t.Fatalf("unexpected counts after revalidate: %+v", got)
		}

		queued := pool.NextBuffer()
		if queued != entry {
			t.Fatalf("expected NextBuffer to return revalidated entry")
		}
		t.Logf("manual QA outcome=%s proxy=%s counts=%+v", result.Outcome, result.ProxyID, result.Counts)
	})

	t.Run("from blacklist", func(t *testing.T) {
		pool := newTestPoolForRuntimeControl()
		entry := newRuntimeControlEntry("203.0.113.12", 1080, "beta", StatusBlacklisted)
		pool.blacklist = []*Entry{entry}
		pool.known[entry.Proxy.Addr()] = true

		result, err := pool.ManualRevalidate(entry.Proxy.Addr())
		if err != nil {
			t.Fatalf("expected revalidate action to succeed: %v", err)
		}

		if result.ProxyID != entry.Proxy.Addr() {
			t.Fatalf("unexpected proxy id %q", result.ProxyID)
		}
		if result.Outcome != "accepted_for_revalidation" {
			t.Fatalf("unexpected outcome %q", result.Outcome)
		}
		if entry.Status != StatusBuffer {
			t.Fatalf("expected blacklisted entry to move to buffer, got %v", entry.Status)
		}
		if got := result.Counts; got != (PoolCounts{Ready: 0, Buffer: 1, Blacklist: 0}) {
			t.Fatalf("unexpected counts after blacklist revalidate: %+v", got)
		}
	})
}

func TestPoolManualReleaseMovesBlacklistedEntryToReady_whenProxyIDExists(t *testing.T) {
	pool := newTestPoolForRuntimeControl()
	entry := newRuntimeControlEntry("203.0.113.13", 1080, "alpha", StatusBlacklisted)
	pool.blacklist = []*Entry{entry}
	pool.known[entry.Proxy.Addr()] = true

	result, err := pool.ManualRelease(entry.Proxy.Addr())
	if err != nil {
		t.Fatalf("expected release action to succeed: %v", err)
	}

	if result.ProxyID != entry.Proxy.Addr() {
		t.Fatalf("unexpected proxy id %q", result.ProxyID)
	}
	if result.Action != "release" {
		t.Fatalf("unexpected action %q", result.Action)
	}
	if result.Outcome != "released_from_blacklist" {
		t.Fatalf("unexpected outcome %q", result.Outcome)
	}
	if entry.Status != StatusReady {
		t.Fatalf("expected entry to become ready, got %v", entry.Status)
	}
	if entry.UseCount != 0 {
		t.Fatalf("expected released entry use count reset, got %d", entry.UseCount)
	}
	if !entry.BlacklistedUntil.IsZero() {
		t.Fatal("expected blacklist deadline to be cleared")
	}
	if got := result.Counts; got != (PoolCounts{Ready: 1, Buffer: 0, Blacklist: 0}) {
		t.Fatalf("unexpected counts after release: %+v", got)
	}
	if !pool.known[entry.Proxy.Addr()] {
		t.Fatal("expected release to preserve known-map membership")
	}
}

func TestPoolManualReleaseReturnsCapacityError_whenReadyPoolIsFull(t *testing.T) {
	pool := New(1, 1, 25, 10, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
	readyEntry := newRuntimeControlEntry("203.0.113.14", 1080, "ready", StatusReady)
	blacklistedEntry := newRuntimeControlEntry("203.0.113.15", 1080, "blacklist", StatusBlacklisted)
	pool.ready = []*Entry{readyEntry}
	pool.blacklist = []*Entry{blacklistedEntry}

	_, err := pool.ManualRelease(blacklistedEntry.Proxy.Addr())
	if err == nil {
		t.Fatal("expected capacity error")
	}

	var capacityErr *ProxyActionCapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("expected ProxyActionCapacityError, got %T", err)
	}
	if capacityErr.Action != "release" {
		t.Fatalf("unexpected action %q", capacityErr.Action)
	}
	if capacityErr.PoolName != "ready" {
		t.Fatalf("unexpected pool name %q", capacityErr.PoolName)
	}
	if len(pool.ready) != 1 || pool.ready[0] != readyEntry {
		t.Fatalf("ready pool mutated on failed release: %#v", pool.ready)
	}
	if len(pool.blacklist) != 1 || pool.blacklist[0] != blacklistedEntry {
		t.Fatalf("blacklist mutated on failed release: %#v", pool.blacklist)
	}
}

func TestPoolManualRevalidateReturnsCapacityError_whenBufferIsFull(t *testing.T) {
	pool := New(1, 10, 25, 1, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
	readyEntry := newRuntimeControlEntry("203.0.113.16", 1080, "ready", StatusReady)
	bufferEntry := newRuntimeControlEntry("203.0.113.17", 1080, "buffer", StatusBuffer)
	pool.ready = []*Entry{readyEntry}
	pool.buffer = []*Entry{bufferEntry}

	_, err := pool.ManualRevalidate(readyEntry.Proxy.Addr())
	if err == nil {
		t.Fatal("expected capacity error")
	}

	var capacityErr *ProxyActionCapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("expected ProxyActionCapacityError, got %T", err)
	}
	if capacityErr.Action != "revalidate" {
		t.Fatalf("unexpected action %q", capacityErr.Action)
	}
	if capacityErr.PoolName != "buffer" {
		t.Fatalf("unexpected pool name %q", capacityErr.PoolName)
	}
	if len(pool.ready) != 1 || pool.ready[0] != readyEntry {
		t.Fatalf("ready pool mutated on failed revalidate: %#v", pool.ready)
	}
	if len(pool.buffer) != 1 || pool.buffer[0] != bufferEntry {
		t.Fatalf("buffer pool mutated on failed revalidate: %#v", pool.buffer)
	}
}

func TestPoolManualActionReturnsStructuredNotFoundError_whenProxyIDUnknown(t *testing.T) {
	pool := newTestPoolForRuntimeControl()

	_, err := pool.ManualBlacklist("203.0.113.99:1080")
	if err == nil {
		t.Fatal("expected not-found error")
	}

	var actionErr *ProxyActionNotFoundError
	if !errors.As(err, &actionErr) {
		t.Fatalf("expected ProxyActionNotFoundError, got %T", err)
	}
	if actionErr.Action != "blacklist" {
		t.Fatalf("unexpected error action %q", actionErr.Action)
	}
	if actionErr.ProxyID != "203.0.113.99:1080" {
		t.Fatalf("unexpected error proxy id %q", actionErr.ProxyID)
	}
	if actionErr.ErrorMessage() == "" {
		t.Fatal("expected structured error message")
	}
}

func inventoryProxyIDs(entries []InventoryEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ProxyID)
	}
	return ids
}
