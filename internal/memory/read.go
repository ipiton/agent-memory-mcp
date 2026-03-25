package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/topk"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

// snapshotReadonlyMemories returns pointers to cached cachedMemory objects for read-only iteration.
func (ms *Store) snapshotReadonlyMemories() []*cachedMemory {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	snapshot := make([]*cachedMemory, 0, len(ms.memories))
	for _, m := range ms.memories {
		snapshot = append(snapshot, m)
	}
	return snapshot
}

// snapshotForContext returns a read-only snapshot pre-filtered by context.
func (ms *Store) snapshotForContext(ctx string) []*cachedMemory {
	if ctx == "" {
		return ms.snapshotReadonlyMemories()
	}

	ms.mu.RLock()
	defer ms.mu.RUnlock()

	indexed, ok := ms.contextIndex[ctx]
	if !ok {
		return nil
	}
	snapshot := make([]*cachedMemory, 0, len(indexed))
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

	const (
		minScore = 0.05

		// Recall scoring weights: weightedScore = rawScore * (baseW + importance*importanceW + confidence*confidenceW) + freshness*freshnessW
		baseW       = 0.45
		importanceW = 0.35
		confidenceW = 0.20
		freshnessW  = 0.03
	)

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
		if !ms.matchCachedFilters(m, filters) {
			continue
		}

		trust := deriveTrustMetadataFromCached(m, now)

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel == queryModelID {
			score = vectorstore.CosineSimilarity(queryEmbedding, m.Embedding)
		} else {
			if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel != queryModelID {
				modelMismatchCount++
			}
			score = ms.textMatchScore(query, m)
		}

		weightedScore := score*(baseW+m.Importance*importanceW+trust.Confidence*confidenceW) + trust.FreshnessScore*freshnessW
		if weightedScore < minScore {
			continue
		}

		candidate := &SearchResult{
			// We'll fill the full Memory later for the top results
			Memory: &Memory{ID: m.ID},
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

	// Fetch full Memory objects for the final results
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.Memory.ID
	}

	if len(ids) > 0 {
		memMap, err := ms.getBatch(ids)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			if m, ok := memMap[r.Memory.ID]; ok {
				r.Memory = m
			}
		}
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

func (ms *Store) matchCachedFilters(m *cachedMemory, filters Filters) bool {
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

func (ms *Store) textMatchScore(query string, m *cachedMemory) float64 {
	queryLower := strings.ToLower(query)
	titleLower := strings.ToLower(m.Title)
	contentLower := strings.ToLower(m.Content)

	score := 0.0
	if titleLower != "" && strings.Contains(titleLower, queryLower) {
		score += 0.6
	} else if contentLower != "" && strings.Contains(contentLower, queryLower) {
		score += 0.3
	}

	queryWords := scoring.TokenizeWords(queryLower)
	if len(queryWords) == 0 {
		return score
	}

	// Optimization: build a map of content and title words for O(1) matching
	wordSet := make(map[string]struct{})
	for _, w := range scoring.TokenizeWords(titleLower) {
		wordSet[w] = struct{}{}
	}
	for _, w := range scoring.TokenizeWords(contentLower) {
		wordSet[w] = struct{}{}
	}

	matchCount := 0
	for _, qw := range queryWords {
		if _, ok := wordSet[qw]; ok {
			matchCount++
		}
	}

	score += (float64(matchCount) / float64(len(queryWords))) * 0.4
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

	// Write to DB first — only update cache for IDs that succeed.
	successIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := ms.db.Exec(`
			UPDATE memories SET accessed_at = ?, access_count = access_count + 1
			WHERE id = ?
		`, now, id); err != nil {
			ms.logger.Warn("Failed to update access stats", zap.String("id", id), zap.Error(err))
		} else {
			successIDs = append(successIDs, id)
		}
	}

	if len(successIDs) == 0 {
		return
	}

	ms.mu.Lock()
	for _, id := range successIDs {
		if m, exists := ms.memories[id]; exists {
			m.AccessedAt = now
			m.AccessCount++
		}
	}
	ms.mu.Unlock()
}

// Get retrieves a memory by ID from the database.
func (ms *Store) Get(id string) (*Memory, error) {
	row := ms.db.QueryRow(`
		SELECT id, content, type, title, tags, context, importance, metadata, embedding_model,
		       embedding, created_at, updated_at, accessed_at, access_count,
		       valid_from, valid_until, superseded_by, replaces, observed_at
		FROM memories WHERE id = ?
	`, id)

	var m Memory
	var tagsJSON, metadataJSON, embeddingModel sql.NullString
	var embeddingBlob []byte
	var createdAt, updatedAt, accessedAt sql.NullTime
	var validFrom, validUntil, observedAt sql.NullTime
	var supersededBy, replaces sql.NullString

	err := row.Scan(
		&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
		&m.Importance, &metadataJSON, &embeddingModel, &embeddingBlob,
		&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
		&validFrom, &validUntil, &supersededBy, &replaces, &observedAt,
	)
	if err == sql.ErrNoRows {
		return nil, &ErrNotFound{ID: id}
	}
	if err != nil {
		return nil, err
	}

	if tagsJSON.Valid && tagsJSON.String != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &m.Tags)
	}
	if metadataJSON.Valid && metadataJSON.String != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &m.Metadata)
	}
	if len(embeddingBlob) > 0 {
		m.Embedding, _ = unmarshalEmbeddingBinary(embeddingBlob)
	}
	if embeddingModel.Valid {
		m.EmbeddingModel = embeddingModel.String
	}
	if createdAt.Valid {
		m.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		m.UpdatedAt = updatedAt.Time
	}
	if accessedAt.Valid {
		m.AccessedAt = accessedAt.Time
	}
	if validFrom.Valid {
		m.ValidFrom = &validFrom.Time
	}
	if validUntil.Valid {
		m.ValidUntil = &validUntil.Time
	}
	if supersededBy.Valid {
		m.SupersededBy = supersededBy.String
	}
	if replaces.Valid {
		m.Replaces = replaces.String
	}
	if observedAt.Valid {
		m.ObservedAt = &observedAt.Time
	}

	return &m, nil
}

func (ms *Store) getBatch(ids []string) (map[string]*Memory, error) {
	if len(ids) == 0 {
		return make(map[string]*Memory), nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT id, content, type, title, tags, context, importance, metadata, embedding_model,
		       embedding, created_at, updated_at, accessed_at, access_count,
		       valid_from, valid_until, superseded_by, replaces, observed_at
		FROM memories WHERE id IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := ms.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*Memory)
	for rows.Next() {
		var m Memory
		var tagsJSON, metadataJSON, embeddingModel sql.NullString
		var embeddingBlob []byte
		var createdAt, updatedAt, accessedAt sql.NullTime
		var validFrom, validUntil, observedAt sql.NullTime
		var supersededBy, replaces sql.NullString

		err := rows.Scan(
			&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
			&m.Importance, &metadataJSON, &embeddingModel, &embeddingBlob,
			&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
			&validFrom, &validUntil, &supersededBy, &replaces, &observedAt,
		)
		if err != nil {
			continue
		}
		if tagsJSON.Valid && tagsJSON.String != "" {
			_ = json.Unmarshal([]byte(tagsJSON.String), &m.Tags)
		}
		if metadataJSON.Valid && metadataJSON.String != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &m.Metadata)
		}
		if len(embeddingBlob) > 0 {
			m.Embedding, _ = unmarshalEmbeddingBinary(embeddingBlob)
		}
		if embeddingModel.Valid {
			m.EmbeddingModel = embeddingModel.String
		}
		if createdAt.Valid {
			m.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			m.UpdatedAt = updatedAt.Time
		}
		if accessedAt.Valid {
			m.AccessedAt = accessedAt.Time
		}
		if validFrom.Valid {
			m.ValidFrom = &validFrom.Time
		}
		if validUntil.Valid {
			m.ValidUntil = &validUntil.Time
		}
		if supersededBy.Valid {
			m.SupersededBy = supersededBy.String
		}
		if replaces.Valid {
			m.Replaces = replaces.String
		}
		if observedAt.Valid {
			m.ObservedAt = &observedAt.Time
		}
		result[m.ID] = &m
	}
	return result, nil
}

// List returns memories matching the given filters, sorted by update time descending.
func (ms *Store) List(ctx context.Context, filters Filters, limit int) ([]*Memory, error) {
	snapshot := ms.snapshotForContext(filters.Context)

	var filteredIDs []string
	idToCached := make(map[string]*cachedMemory)
	for _, m := range snapshot {
		if ms.matchCachedFilters(m, filters) {
			filteredIDs = append(filteredIDs, m.ID)
			idToCached[m.ID] = m
		}
	}

	sort.Slice(filteredIDs, func(i, j int) bool {
		return idToCached[filteredIDs[i]].UpdatedAt.After(idToCached[filteredIDs[j]].UpdatedAt)
	})

	if limit > 0 && len(filteredIDs) > limit {
		filteredIDs = filteredIDs[:limit]
	}

	memMap, err := ms.getBatch(filteredIDs)
	if err != nil {
		return nil, err
	}

	results := make([]*Memory, 0, len(filteredIDs))
	for _, id := range filteredIDs {
		if m, ok := memMap[id]; ok {
			results = append(results, m)
		}
	}

	return results, nil
}

// ExportAll returns all memories sorted by CreatedAt ascending.
func (ms *Store) ExportAll(ctx context.Context) ([]*Memory, error) {
	snapshot := ms.snapshotReadonlyMemories()

	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i].CreatedAt.Before(snapshot[j].CreatedAt)
	})

	ids := make([]string, len(snapshot))
	for i, m := range snapshot {
		ids[i] = m.ID
	}

	// For large exports, we might want to stream or batch this.
	// But let's keep it simple for now.
	memMap, err := ms.getBatch(ids)
	if err != nil {
		return nil, err
	}

	results := make([]*Memory, 0, len(ids))
	for _, id := range ids {
		if m, ok := memMap[id]; ok {
			results = append(results, m)
		}
	}
	return results, nil
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

// DB returns the underlying *sql.DB for use by subsystems that need to manage
// their own tables within the same database (e.g. steward policy, audit trail).
// Callers must not close the returned connection.
func (ms *Store) DB() *sql.DB {
	return ms.db
}

// Close shuts down the access stats worker and closes the database connection.
func (ms *Store) Close() error {
	close(ms.accessCh)
	ms.accessWG.Wait()
	return ms.db.Close()
}
