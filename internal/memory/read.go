package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/topk"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

// snapshotReadonlyMemories returns pointers to cached Memory objects for read-only iteration.
// Callers MUST NOT mutate the returned items. Use copyMemory before returning to external callers.
// The snapshot is safe to iterate after RLock release because write operations use copy-on-write
// (they replace map entries with new objects, never mutating existing ones in place).
func (ms *Store) snapshotReadonlyMemories() []*Memory {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	snapshot := make([]*Memory, 0, len(ms.memories))
	for _, m := range ms.memories {
		snapshot = append(snapshot, m)
	}
	return snapshot
}

// snapshotForContext returns a read-only snapshot pre-filtered by context.
// If context is empty, returns all memories (same as snapshotReadonlyMemories).
// Uses contextIndex for O(1) lookup instead of full scan when context is specified.
func (ms *Store) snapshotForContext(context string) []*Memory {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if context == "" {
		snapshot := make([]*Memory, 0, len(ms.memories))
		for _, m := range ms.memories {
			snapshot = append(snapshot, m)
		}
		return snapshot
	}

	indexed, ok := ms.contextIndex[context]
	if !ok {
		return nil
	}
	snapshot := make([]*Memory, 0, len(indexed))
	for _, m := range indexed {
		snapshot = append(snapshot, m)
	}
	return snapshot
}

// Recall searches memories by semantic similarity, applying filters and importance weighting.
func (ms *Store) Recall(ctx context.Context, query string, filters Filters, limit int) ([]*SearchResult, error) {
	snapshot := ms.snapshotForContext(filters.Context)
	if len(snapshot) == 0 {
		return nil, nil
	}

	var queryEmbedding []float32
	var queryModelID string
	if ms.embedder != nil {
		result, err := ms.embedder.EmbedQueryDetailed(ctx, query)
		if err != nil {
			ms.logger.Warn("Failed to embed query, falling back to text search", zap.Error(err))
		} else {
			queryEmbedding = result.Embedding
			queryModelID = result.ModelID
		}
	}

	const minScore = 0.05

	var results []*SearchResult
	useHeap := limit > 0
	var topResults *topk.MinHeap[*SearchResult]
	if useHeap {
		topResults = topk.NewMinHeap(limit, func(a, b *SearchResult) bool {
			return a.Score < b.Score
		})
	}
	modelMismatchCount := 0
	now := time.Now()

	for _, m := range snapshot {
		if !ms.matchFilters(m, filters) {
			continue
		}

		trust := deriveTrustMetadata(m, now)

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel == queryModelID {
			score = vectorstore.CosineSimilarity(queryEmbedding, m.Embedding)
		} else {
			if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel != queryModelID {
				modelMismatchCount++
			}
			score = ms.textMatchScore(query, m)
		}

		weightedScore := score*(0.45+m.Importance*0.35+trust.Confidence*0.20) + trust.FreshnessScore*0.03
		if weightedScore < minScore {
			continue
		}

		candidate := &SearchResult{
			Memory: m,
			Score:  weightedScore,
			Trust:  trust,
		}
		if !useHeap {
			results = append(results, candidate)
			continue
		}
		if topResults.Len() < limit {
			topResults.PushItem(candidate)
			continue
		}
		if topResults.PeekMin().Score < candidate.Score {
			topResults.ReplaceMin(candidate)
		}
	}

	if useHeap {
		results = make([]*SearchResult, 0, topResults.Len())
		for topResults.Len() > 0 {
			results = append(results, topResults.PopItem())
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	ids := make([]string, len(results))
	for i, r := range results {
		r.Memory = copyMemory(r.Memory)
		ids[i] = r.Memory.ID
	}

	select {
	case ms.accessCh <- ids:
	default:
		ms.logger.Debug("Access stats channel full, dropping update")
	}

	if modelMismatchCount > 0 {
		ms.logger.Info("Recall fell back to text matching for model-mismatched memories",
			zap.Int("count", modelMismatchCount),
			zap.String("query_model", queryModelID),
		)
	}

	return results, nil
}

// matchFilters checks if a memory matches the filters.
func (ms *Store) matchFilters(m *Memory, filters Filters) bool {
	if filters.Type != "" && m.Type != filters.Type {
		return false
	}
	if filters.Context != "" && m.Context != filters.Context {
		return false
	}
	if filters.MinImportance > 0 && m.Importance < filters.MinImportance {
		return false
	}
	if !filters.Since.IsZero() && m.CreatedAt.Before(filters.Since) {
		return false
	}

	if len(filters.Tags) > 0 {
		tagSet := make(map[string]struct{}, len(m.Tags))
		for _, t := range m.Tags {
			tagSet[t] = struct{}{}
		}
		hasTag := false
		for _, filterTag := range filters.Tags {
			if _, ok := tagSet[filterTag]; ok {
				hasTag = true
				break
			}
		}
		if !hasTag {
			return false
		}
	}

	return true
}

// textMatchScore calculates a simple text matching score.
func (ms *Store) textMatchScore(query string, m *Memory) float64 {
	queryLower := strings.ToLower(query)
	contentLower := strings.ToLower(m.Content)
	titleLower := strings.ToLower(m.Title)

	score := 0.0
	if strings.Contains(contentLower, queryLower) {
		score += 0.5
	}
	if strings.Contains(titleLower, queryLower) {
		score += 0.7
	}

	queryWords := scoring.TokenizeWords(queryLower)
	contentWords := scoring.TokenizeWords(contentLower)

	matchCount := 0
	for _, qw := range queryWords {
		for _, cw := range contentWords {
			if qw == cw {
				matchCount++
				break
			}
		}
	}

	if len(queryWords) > 0 {
		score += float64(matchCount) / float64(len(queryWords)) * 0.3
	}

	return score
}

// accessStatsWorker drains accessCh and flushes batched access stats updates.
// Batches are flushed when 100 IDs accumulate or after 5 seconds of inactivity.
func (ms *Store) accessStatsWorker() {
	defer ms.accessWG.Done()

	const (
		maxBatch     = 100
		flushTimeout = 5 * time.Second
	)
	batch := make(map[string]struct{}, maxBatch)
	timer := time.NewTimer(flushTimeout)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ids := make([]string, 0, len(batch))
		for id := range batch {
			ids = append(ids, id)
		}
		clear(batch)
		ms.flushAccessStats(ids)
	}

	for {
		select {
		case ids, ok := <-ms.accessCh:
			if !ok {
				flush()
				return
			}
			for _, id := range ids {
				batch[id] = struct{}{}
			}
			if len(batch) >= maxBatch {
				flush()
				timer.Reset(flushTimeout)
			}
		case <-timer.C:
			flush()
			timer.Reset(flushTimeout)
		}
	}
}

// flushAccessStats persists access statistics for a batch of memory IDs.
func (ms *Store) flushAccessStats(ids []string) {
	if len(ids) == 0 {
		return
	}

	now := time.Now()

	ms.mu.Lock()
	for _, id := range ids {
		if m, exists := ms.memories[id]; exists {
			updated := copyMemory(m)
			updated.AccessedAt = now
			updated.AccessCount++
			ms.cacheSetLocked(updated)
		}
	}
	ms.mu.Unlock()

	for _, id := range ids {
		if _, err := ms.db.Exec(`
			UPDATE memories SET accessed_at = ?, access_count = access_count + 1
			WHERE id = ?
		`, now, id); err != nil {
			ms.logger.Warn("Failed to update access stats", zap.String("id", id), zap.Error(err))
		}
	}
}

// Get retrieves a memory by ID from the in-memory cache (returns a copy).
func (ms *Store) Get(id string) (*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	m, exists := ms.memories[id]
	if !exists {
		return nil, fmt.Errorf("memory not found: %s", id)
	}

	return copyMemory(m), nil
}

// List returns memories matching the given filters, sorted by update time descending.
func (ms *Store) List(ctx context.Context, filters Filters, limit int) ([]*Memory, error) {
	snapshot := ms.snapshotForContext(filters.Context)

	var results []*Memory
	for _, m := range snapshot {
		if ms.matchFilters(m, filters) {
			results = append(results, m)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	copies := make([]*Memory, 0, len(results))
	for _, m := range results {
		copies = append(copies, copyMemory(m))
	}

	return copies, nil
}

// ExportAll returns all memories sorted by CreatedAt ascending.
func (ms *Store) ExportAll(ctx context.Context) ([]*Memory, error) {
	result := ms.snapshotReadonlyMemories()

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	copies := make([]*Memory, 0, len(result))
	for _, m := range result {
		copies = append(copies, copyMemory(m))
	}
	return copies, nil
}

// Count returns the total number of stored memories.
func (ms *Store) Count() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.memories)
}

// CountByType returns the number of memories grouped by Type.
func (ms *Store) CountByType() map[Type]int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	counts := make(map[Type]int)
	for _, m := range ms.memories {
		counts[m.Type]++
	}
	return counts
}

// CountByEmbeddingModel returns the number of memories grouped by embedding model.
func (ms *Store) CountByEmbeddingModel() map[string]int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	counts := make(map[string]int)
	for _, m := range ms.memories {
		model := m.EmbeddingModel
		if model == "" {
			model = "(none)"
		}
		counts[model]++
	}
	return counts
}

// Close shuts down the access stats worker and closes the database connection.
func (ms *Store) Close() error {
	close(ms.accessCh)
	ms.accessWG.Wait()
	return ms.db.Close()
}
