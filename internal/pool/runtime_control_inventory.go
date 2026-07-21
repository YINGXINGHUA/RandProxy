package pool

import (
	"sort"
	"time"
)

type InventoryEntry struct {
	ProxyID          string
	IP               string
	Port             int
	Protocol         string
	Source           string
	Status           string
	UseCount         int
	MaxUse           int
	AddedAt          string
	LastUsed         string
	BlacklistedUntil string
}

type InventorySnapshot struct {
	Ready     []InventoryEntry
	Buffer    []InventoryEntry
	Blacklist []InventoryEntry
}

type EntryAccountingSnapshot struct {
	UseCount     int
	LastUsed     time.Time
	LatencyCount int
}

func (p *Pool) EntryAccountingSnapshot(entry *Entry) EntryAccountingSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return EntryAccountingSnapshot{
		UseCount:     entry.UseCount,
		LastUsed:     entry.LastUsed,
		LatencyCount: entry.LatencyCount,
	}
}

func (p *Pool) InventorySnapshot() InventorySnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return InventorySnapshot{
		Ready:     snapshotEntries(p.ready, "ready"),
		Buffer:    snapshotEntries(p.buffer, "buffer"),
		Blacklist: snapshotEntries(p.blacklist, "blacklisted"),
	}
}

func snapshotEntries(entries []*Entry, status string) []InventoryEntry {
	snapshot := make([]InventoryEntry, 0, len(entries))
	for _, entry := range entries {
		snapshot = append(snapshot, inventoryEntryFromPoolEntry(entry, status))
	}
	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i].ProxyID < snapshot[j].ProxyID
	})
	return snapshot
}

func inventoryEntryFromPoolEntry(entry *Entry, status string) InventoryEntry {
	inventory := InventoryEntry{
		ProxyID:  entry.Proxy.Addr(),
		IP:       entry.Proxy.IP,
		Port:     entry.Proxy.Port,
		Protocol: string(entry.Proxy.Protocol),
		Source:   entry.Proxy.Source,
		Status:   status,
		UseCount: entry.UseCount,
		MaxUse:   entry.MaxUse,
	}
	if !entry.AddedAt.IsZero() {
		inventory.AddedAt = entry.AddedAt.UTC().Format(time.RFC3339)
	}
	if !entry.LastUsed.IsZero() {
		inventory.LastUsed = entry.LastUsed.UTC().Format(time.RFC3339)
	}
	if !entry.BlacklistedUntil.IsZero() {
		inventory.BlacklistedUntil = entry.BlacklistedUntil.UTC().Format(time.RFC3339)
	}
	return inventory
}
