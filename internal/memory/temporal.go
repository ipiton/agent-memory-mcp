package memory

import (
	"context"
	"sort"
	"time"
)

// RecallAsOf searches for memories that were valid at a specific point in time.
// A memory is considered valid at `asOf` if:
//   - valid_from is nil or valid_from <= asOf
//   - valid_until is nil or valid_until > asOf
func (ms *Store) RecallAsOf(ctx context.Context, query string, asOf time.Time, filters Filters, limit int) ([]*SearchResult, error) {
	// Use the standard recall first, then filter by temporal validity.
	results, err := ms.Recall(ctx, query, filters, 0) // get all, we'll filter
	if err != nil {
		return nil, err
	}

	var filtered []*SearchResult
	for _, r := range results {
		m := r.Memory
		if !isValidAt(m, asOf) {
			continue
		}
		filtered = append(filtered, r)
	}

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// isValidAt checks if a memory was valid at the given timestamp.
func isValidAt(m *Memory, asOf time.Time) bool {
	if m.ValidFrom != nil && m.ValidFrom.After(asOf) {
		return false
	}
	if m.ValidUntil != nil && !m.ValidUntil.After(asOf) {
		return false
	}
	return true
}

// IsValidAtCached checks temporal validity using cached memory fields.
func isValidAtCached(m *cachedMemory, asOf time.Time) bool {
	if m.ValidFrom != nil && m.ValidFrom.After(asOf) {
		return false
	}
	if m.ValidUntil != nil && !m.ValidUntil.After(asOf) {
		return false
	}
	return true
}

// TimelineEntry represents one entry in a knowledge timeline.
type TimelineEntry struct {
	MemoryID     string     `json:"memory_id"`
	Title        string     `json:"title"`
	ValidFrom    *time.Time `json:"valid_from,omitempty"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	Replaces     string     `json:"replaces,omitempty"`
	Status       string     `json:"status"`
	Confidence   float64    `json:"confidence,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// KnowledgeTimeline returns the chronological evolution of knowledge matching the query.
// It finds related memories and orders them by valid_from (or created_at as fallback).
func (ms *Store) KnowledgeTimeline(ctx context.Context, query string, memContext string, service string) ([]TimelineEntry, error) {
	// Recall all matching memories (including archived/superseded).
	results, err := ms.Recall(ctx, query, Filters{Context: memContext}, 0)
	if err != nil {
		return nil, err
	}

	var entries []TimelineEntry
	for _, r := range results {
		m := r.Memory

		// Filter by service if specified.
		if service != "" && MemoryService(m) != service {
			continue
		}

		title := m.Title
		if title == "" && len(m.Content) > 60 {
			title = m.Content[:60] + "..."
		} else if title == "" {
			title = m.Content
		}

		entry := TimelineEntry{
			MemoryID:     m.ID,
			Title:        title,
			ValidFrom:    m.ValidFrom,
			ValidUntil:   m.ValidUntil,
			SupersededBy: m.SupersededBy,
			Replaces:     m.Replaces,
			Status:       string(LifecycleStatusOf(m)),
			CreatedAt:    m.CreatedAt,
		}

		if r.Trust != nil {
			entry.Confidence = r.Trust.Confidence
		}

		entries = append(entries, entry)
	}

	// Sort by valid_from (or created_at as fallback), oldest first.
	sort.Slice(entries, func(i, j int) bool {
		ti := effectiveFrom(entries[i])
		tj := effectiveFrom(entries[j])
		return ti.Before(tj)
	})

	return entries, nil
}

func effectiveFrom(e TimelineEntry) time.Time {
	if e.ValidFrom != nil {
		return *e.ValidFrom
	}
	return e.CreatedAt
}

// SetTemporalFields updates the temporal columns on a memory.
// Used by MarkOutdated and steward actions to build supersession chains.
func (ms *Store) SetTemporalFields(ctx context.Context, id string, validFrom, validUntil *time.Time, supersededBy, replaces string) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	m, err := ms.Get(id)
	if err != nil {
		return err
	}

	if validFrom != nil {
		m.ValidFrom = validFrom
	}
	if validUntil != nil {
		m.ValidUntil = validUntil
	}
	if supersededBy != "" {
		m.SupersededBy = supersededBy
	}
	if replaces != "" {
		m.Replaces = replaces
	}

	m.UpdatedAt = time.Now()

	if err := updateMemoryRow(ms.db, m); err != nil {
		return err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(m))
	ms.mu.Unlock()

	return nil
}
