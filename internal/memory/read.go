package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/topk"
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

// LastInContext returns the most recent session-checkpoint memory stored in the
// given context whose CreatedAt is >= since. It scans the cached context index
// under mu.RLock (via snapshotForContext), then fetches the full Memory via Get
// so that callers receive Content/Metadata — needed for similarity scoring.
//
// Returns (nil, nil) if no matching memory exists.
func (ms *Store) LastInContext(ctx context.Context, contextName string, since time.Time) (*Memory, error) {
	snapshot := ms.snapshotForContext(contextName)
	if len(snapshot) == 0 {
		return nil, nil
	}

	var latest *cachedMemory
	for _, m := range snapshot {
		if m == nil {
			continue
		}
		if !since.IsZero() && m.CreatedAt.Before(since) {
			continue
		}
		// Session-checkpoint filter: cachedMemory intentionally does not
		// retain the full metadata map, so we use the "session-checkpoint"
		// tag — set unconditionally by the checkpoint CLI — as a cheap
		// pre-filter, then confirm record_kind via the full Get below.
		hasCheckpointTag := false
		for _, tag := range m.Tags {
			if tag == "session-checkpoint" {
				hasCheckpointTag = true
				break
			}
		}
		if !hasCheckpointTag {
			continue
		}
		if latest == nil || m.CreatedAt.After(latest.CreatedAt) {
			latest = m
		}
	}
	if latest == nil {
		return nil, nil
	}

	full, err := ms.Get(latest.ID)
	if err != nil {
		return nil, err
	}
	// Confirm via record_kind metadata — the tag alone is a hint.
	if full.Metadata[MetadataRecordKind] != RecordKindSessionCheckpoint {
		return nil, nil
	}
	return full, nil
}

// SetRecallHalfLife configures exponential age decay for Recall scoring (T68).
// days <= 0 disables decay; otherwise λ = ln(2)/days so a memory exactly one
// half-life old scores at half its undecayed weight. Set once at startup
// (idempotent); retrieval reads the atomic without a lock.
func (ms *Store) SetRecallHalfLife(days float64) {
	var lambda float64
	if days > 0 {
		lambda = math.Ln2 / days
	}
	ms.recallDecayLambda.Store(math.Float64bits(lambda))
}

// recallDecayMultiplier returns e^(-λ·ageDays) for m (T68), or 1.0 when decay is
// disabled (λ<=0) or m is evergreen. Evergreen = canonical knowledge
// (lifecycle/knowledge-layer canonical) or character-layer identity, both of
// which are stable by design and must not lose rank purely with age.
func (ms *Store) recallDecayMultiplier(m *cachedMemory, now time.Time) float64 {
	lambda := math.Float64frombits(ms.recallDecayLambda.Load())
	if lambda <= 0 {
		return 1.0
	}
	if m.Lifecycle == LifecycleCanonical || m.KnowledgeLayer == "canonical" || m.SedimentLayer == LayerCharacter {
		return 1.0
	}
	ageDays := now.Sub(m.CreatedAt).Hours() / 24
	if ageDays <= 0 {
		return 1.0
	}
	return math.Exp(-lambda * ageDays)
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

		// T48 layer boosts — applied ONLY when ms.sedimentEnabled is true.
		// Character is always-surfaced (+0.15 and no minScore cutoff below).
		// Episodic pays a small demotion (-0.05). Surface is excluded unless
		// filters.Context matches m.Context.
		layerCharacterBoost = 0.15
		layerEpisodicBoost  = -0.05
	)

	sedimentOn := ms.sedimentEnabled.Load()

	var results []*SearchResult
	useHeap := limit > 0
	var topResults *topk.MinHeap[*SearchResult]
	if useHeap {
		topResults = topk.NewMinHeap(limit, func(a, b *SearchResult) bool {
			return a.Score < b.Score
		})
	}
	modelMismatchCount := 0
	now := ms.now()

	// Round 3 M18: build the filter tag-set ONCE outside the per-memory loop.
	filterTagSet := buildFilterTagSet(filters)

	for _, m := range snapshot {
		if !ms.matchCachedFiltersWithTagSet(m, filters, filterTagSet) {
			continue
		}

		// T48 layer-aware filtering: when the flag is on, surface memories
		// are invisible outside their originating Context. This prevents
		// session scratch state from leaking into unrelated recall calls.
		if sedimentOn {
			layer := NormalizeSedimentLayer(string(m.SedimentLayer))
			if layer == LayerSurface {
				if filters.Context == "" || filters.Context != m.Context {
					continue
				}
			}
		}

		trust := deriveTrustMetadataFromCached(m, now)

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel == queryModelID {
			score = scoring.CosineSimilarity(queryEmbedding, m.Embedding)
		} else {
			if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel != queryModelID {
				modelMismatchCount++
			}
			score = ms.textMatchScore(query, m)
		}

		weightedScore := score*(baseW+m.Importance*importanceW+trust.Confidence*confidenceW) + trust.FreshnessScore*freshnessW

		// T68: exponential age decay — a MULTIPLIER on the relevance/trust
		// score (decision variant (a)). This is a distinct axis from
		// trust.FreshnessScore (source-verification recency); decay reflects
		// calendar age since created_at. Applied before the additive layer
		// boosts below so character's always-surface boost is never eroded by
		// age, and so a stale non-evergreen card can fall under minScore.
		weightedScore *= ms.recallDecayMultiplier(m, now)

		// T48 layer boost. Character memories are always-surfaced — they
		// skip the minScore cutoff below so even unrelated queries see
		// them. Episodic pays a small tax; semantic/surface are neutral
		// (surface already got the context-gate above).
		isCharacter := false
		if sedimentOn {
			switch NormalizeSedimentLayer(string(m.SedimentLayer)) {
			case LayerCharacter:
				weightedScore += layerCharacterBoost
				isCharacter = true
			case LayerEpisodic:
				weightedScore += layerEpisodicBoost
			}
		}
		if weightedScore < minScore && !isCharacter {
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
	return ms.matchCachedFiltersWithTagSet(m, filters, nil)
}

// matchCachedFiltersWithTagSet is the hot-path variant called by Recall/List
// loops. The caller pre-builds the filter tag-set once outside the loop;
// this avoids the O(M) per-memory allocation of m.Tags into a set that the
// naive matchCachedFilters performed (Round 3 M18: ~100k allocations on a
// 100k-memory recall). For one-off calls with no filter tags, pass nil and
// the function falls back to a linear membership scan.
func (ms *Store) matchCachedFiltersWithTagSet(m *cachedMemory, filters Filters, filterTagSet map[string]struct{}) bool {
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

	if len(filters.Tags) == 0 {
		return true
	}
	if filterTagSet == nil {
		// Fallback: linear scan when caller didn't pre-build the set.
		for _, t := range m.Tags {
			for _, filterTag := range filters.Tags {
				if t == filterTag {
					return true
				}
			}
		}
		return false
	}
	for _, t := range m.Tags {
		if _, ok := filterTagSet[t]; ok {
			return true
		}
	}
	return false
}

// buildFilterTagSet returns a set of filter.Tags for use with
// matchCachedFiltersWithTagSet. Returns nil when there are no tag filters
// (skip allocation entirely).
func buildFilterTagSet(filters Filters) map[string]struct{} {
	if len(filters.Tags) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(filters.Tags))
	for _, t := range filters.Tags {
		set[t] = struct{}{}
	}
	return set
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
// Round 3 M3: all per-id UPDATEs run inside a single transaction so the
// WAL fsync count is one per batch instead of one per id (was N fsyncs
// per Recall under bursty traffic).
func (ms *Store) flushAccessStats(ids []string) {
	if len(ids) == 0 {
		return
	}

	now := ms.now()

	// Write to DB first inside a single transaction so success/failure is
	// all-or-nothing per batch and the WAL only fsyncs once. defer Rollback
	// is a no-op once Commit succeeds.
	tx, err := ms.db.Begin()
	if err != nil {
		ms.logger.Warn("Failed to begin access stats tx", zap.Error(err))
		return
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`UPDATE memories SET accessed_at = ?, access_count = access_count + 1 WHERE id = ?`)
	if err != nil {
		ms.logger.Warn("Failed to prepare access stats stmt", zap.Error(err))
		return
	}
	defer func() { _ = stmt.Close() }()

	successIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := stmt.Exec(now, id); err != nil {
			ms.logger.Warn("Failed to update access stats", zap.String("id", id), zap.Error(err))
			continue
		}
		successIDs = append(successIDs, id)
	}

	if err := tx.Commit(); err != nil {
		ms.logger.Warn("Failed to commit access stats tx", zap.Error(err))
		return
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

// memoryColumns is the canonical SELECT list for full *Memory rows. Kept in
// sync with scanMemoryRow. Loader (loadMemoriesToCache) historically used a
// shorter subset that omitted replaces/observed_at — those gaps are
// preserved there for now to avoid widening the cachedMemory shape.
const memoryColumns = `id, content, type, title, tags, context, importance, metadata, embedding_model,
		embedding, created_at, updated_at, accessed_at, access_count,
		valid_from, valid_until, superseded_by, replaces, observed_at, sediment_layer`

// rowScanner abstracts *sql.Row and *sql.Rows so scanMemoryRow can serve
// both QueryRow callers (Get) and Next-loop callers (getBatch).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMemoryRow scans memoryColumns into a fresh *Memory and applies the
// post-Scan hydration (tags JSON, metadata JSON, embedding blob, time
// fields, sediment layer normalization). Centralises ~50 LOC that Get and
// getBatch previously duplicated and which had already started drifting
// (Round 3 H18: loader missed replaces/observed_at).
func scanMemoryRow(scanner rowScanner) (*Memory, error) {
	var m Memory
	var tagsJSON, metadataJSON, embeddingModel sql.NullString
	var embeddingBlob []byte
	var createdAt, updatedAt, accessedAt sql.NullTime
	var validFrom, validUntil, observedAt sql.NullTime
	var supersededBy, replaces sql.NullString
	var sedimentLayer sql.NullString

	if err := scanner.Scan(
		&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
		&m.Importance, &metadataJSON, &embeddingModel, &embeddingBlob,
		&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
		&validFrom, &validUntil, &supersededBy, &replaces, &observedAt, &sedimentLayer,
	); err != nil {
		return nil, err
	}

	if tagsJSON.Valid && tagsJSON.String != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &m.Tags)
	}
	m.Metadata, _ = parseMetadataJSON(metadataJSON)
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
	if sedimentLayer.Valid {
		m.SedimentLayer = string(NormalizeSedimentLayer(sedimentLayer.String))
	}
	if m.SedimentLayer == "" {
		m.SedimentLayer = string(DefaultSedimentLayer)
	}
	return &m, nil
}

// Get retrieves a memory by ID from the database.
func (ms *Store) Get(id string) (*Memory, error) {
	row := ms.db.QueryRow("SELECT "+memoryColumns+" FROM memories WHERE id = ?", id)
	m, err := scanMemoryRow(row)
	if err == sql.ErrNoRows {
		return nil, &ErrNotFound{ID: id}
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// getBatchChunkSize bounds the IN-clause size per SQLite query. SQLite's
// default SQLITE_MAX_VARIABLE_NUMBER is 999 (newer builds raise it to
// 32766, but modernc.org/sqlite ships with the conservative default).
// 500 leaves headroom for the planner and is a safe ceiling. Round 3 H4:
// without this cap ExportAll and any massive getBatch crashed at >999 ids.
const getBatchChunkSize = 500

func (ms *Store) getBatch(ids []string) (map[string]*Memory, error) {
	if len(ids) == 0 {
		return make(map[string]*Memory), nil
	}
	result := make(map[string]*Memory, len(ids))
	for start := 0; start < len(ids); start += getBatchChunkSize {
		end := start + getBatchChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := ms.getBatchChunk(ids[start:end], result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// getBatchChunk loads a single IN-bounded chunk of ids into result.
// Caller is responsible for chunking; see getBatch.
func (ms *Store) getBatchChunk(ids []string, result map[string]*Memory) error {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT "+memoryColumns+" FROM memories WHERE id IN (%s)",
		strings.Join(placeholders, ","))

	rows, err := ms.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		m, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		result[m.ID] = m
	}
	return nil
}

// ListLightweight returns memories matching `filters` built directly from
// the in-RAM cache, skipping the SQLite getBatch round-trip that List
// performs. Returned *Memory objects do NOT include `Replaces` or
// `ObservedAt` (those columns are not cached for RAM reasons); all other
// fields are populated identically to List.
//
// Round 3 T52: steward's RunScanners path called List on the full corpus
// per scan invocation, which made `loadActiveMemories` dominate the
// profile (~50% of cum time in BenchmarkRunScanners_2000). Switching to
// ListLightweight for steward and similar predicate-only consumers cuts
// that to a cache-iteration cost.
//
// Use List when you need replaces/observed_at; otherwise prefer this.
func (ms *Store) ListLightweight(filters Filters) []*Memory {
	snapshot := ms.snapshotForContext(filters.Context)
	filterTagSet := buildFilterTagSet(filters)

	results := make([]*Memory, 0, len(snapshot))
	for _, cm := range snapshot {
		if !ms.matchCachedFiltersWithTagSet(cm, filters, filterTagSet) {
			continue
		}
		results = append(results, cachedMemoryToMemory(cm))
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	return results
}

// List returns memories matching the given filters, sorted by update time descending.
func (ms *Store) List(ctx context.Context, filters Filters, limit int) ([]*Memory, error) {
	snapshot := ms.snapshotForContext(filters.Context)
	filterTagSet := buildFilterTagSet(filters)

	var filteredIDs []string
	idToCached := make(map[string]*cachedMemory)
	for _, m := range snapshot {
		if ms.matchCachedFiltersWithTagSet(m, filters, filterTagSet) {
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

// ReloadCache forces the in-memory cache to resync with the database.
// Intended for test helpers that bypass the normal write path (e.g. direct
// SQL to backdate rows for cycle testing). Safe to call concurrently with
// other reads because loadMemoriesToCache acquires mu.
func (ms *Store) ReloadCache() error {
	return ms.loadMemoriesToCache()
}

// Close shuts down all background workers and closes the database connection.
// Order matters: drain in-flight triple-extraction goroutines first so they
// don't write to a closed DB. Then stop the access-stats worker.
func (ms *Store) Close() error {
	ms.extractionWG.Wait()
	close(ms.accessCh)
	ms.accessWG.Wait()
	return ms.db.Close()
}
