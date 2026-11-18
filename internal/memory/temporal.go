package memory

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// RecallAsOf searches for memories that were valid at a specific point in time.
// A memory is considered valid at `asOf` if:
//   - valid_from is nil or valid_from <= asOf
//   - valid_until is nil or valid_until > asOf
//
// T191: the validity window alone decides, so a record superseded after asOf is
// found: it was the current one then. MarkOutdated closes its window at the
// moment of supersession, which keeps it out of later dates.
func (ms *Store) RecallAsOf(ctx context.Context, query string, asOf time.Time, filters Filters, limit int) ([]*SearchResult, error) {
	validThen := func(m *cachedMemory) bool { return isValidAt(m.ValidFrom, m.ValidUntil, asOf) }
	filtered, err := ms.recall(ctx, query, filters, limit, validThen, false)
	if err != nil {
		return nil, err
	}

	// T155: a record rewritten after asOf answers with the text it carried
	// then, not today's. The swap happens on a copy — the result must not
	// write the old text back into the cache.
	for i, r := range filtered {
		rev, err := ms.revisionAsOf(ctx, r.Memory.ID, asOf)
		if err != nil {
			return nil, err
		}
		if rev == nil {
			continue
		}
		m := copyMemory(r.Memory)
		m.Content = rev.Content
		m.Title = rev.Title
		if m.Metadata == nil {
			m.Metadata = make(map[string]string)
		}
		m.Metadata[MetadataContentAsOfRevision] = strconv.Itoa(rev.Revision)
		swapped := *r
		swapped.Memory = m
		filtered[i] = &swapped
	}
	return filtered, nil
}

// isValidAt checks if a validity window covers the given timestamp.
func isValidAt(validFrom, validUntil *time.Time, asOf time.Time) bool {
	if validFrom != nil && validFrom.After(asOf) {
		return false
	}
	if validUntil != nil && !validUntil.After(asOf) {
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
	// Rank matching memories, superseded ones included, with a safety cap.
	anyRecord := func(*cachedMemory) bool { return true }
	results, err := ms.recall(ctx, query, Filters{Context: memContext}, 500, anyRecord, false)
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

		title := DisplayTitle(m, 60)

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

	m.UpdatedAt = ms.now()

	if err := updateMemoryRow(ms.db, m); err != nil {
		return err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(m))
	ms.mu.Unlock()

	return nil
}
