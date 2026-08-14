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

// hasMemory reports whether the given id is present in the cache. Used to tell
// a live supersession pointer from a dangling one.
func (ms *Store) hasMemory(id string) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	_, ok := ms.memories[id]
	return ok
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
// TypesFromStrings converts configured type names into Type values, dropping
// blanks. Unknown names are kept as-is: they simply never match a stored type,
// which fails toward not decaying rather than toward decaying everything.
func TypesFromStrings(names []string) []Type {
	out := make([]Type, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, Type(n))
		}
	}
	return out
}

// SetRecallDecayTypes restricts age decay to the given memory types. An empty
// list restores the default, where every type decays.
func (ms *Store) SetRecallDecayTypes(types []Type) {
	if len(types) == 0 {
		ms.decayTypes.Store(nil)
		return
	}
	set := make(map[Type]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	ms.decayTypes.Store(&set)
}

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
	// T121: an optional narrowing of what ages. Events and working state go
	// stale on a calendar; a pattern or a fact does not stop being true
	// because a quarter passed. Nil means every type decays, which is the
	// behaviour T68 shipped and the default here.
	if types := ms.decayTypes.Load(); types != nil {
		if !(*types)[m.Type] {
			return 1.0
		}
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
			// The whole semantic leg is gone here and the caller still gets a
			// result list — scored by text matching alone. Strict mode exists
			// for the reader who would rather know (T99).
			ms.logger.Warn("Failed to embed query, falling back to text search", zap.Error(err))
			if ms.retrievalStrict.Load() {
				return nil, StrictRetrievalError("embedding", "query embedding failed, recall would fall back to text matching: "+err.Error())
			}
		} else {
			if len(result.Fallbacks) > 0 && ms.retrievalStrict.Load() {
				return nil, StrictRetrievalError("embedding", fmt.Sprintf(
					"provider(s) %s failed and the query was embedded by %s instead",
					strings.Join(result.Fallbacks, ", "), result.Provider))
			}
			queryEmbedding = result.Embedding
			queryModelID = result.ModelID
		}
	}

	const (
		// minScore is a floor on the weighted score. Its value was never the
		// problem; the distribution under it was. On raw cosine this corpus
		// puts unrelated pairs at a median of 0.555, and a sampled 68 025
		// candidate comparisons cleared 0.05 at a rate of 100.0% — the gate
		// could not reject anything, which is half of why a limitless sweep
		// marks the whole bank (T113). Under centered scoring the same sample
		// clears it at 34.1%, so the threshold starts doing the job it was
		// written for without being retuned.
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

	// T76a: centering is per-query work — the two scalars below are constant
	// across candidates. When the query does not share the mean's dimension
	// (a mixed-model bank, an unembedded query) centering is skipped for the
	// whole call rather than per candidate, so one Recall never mixes two
	// score scales.
	var (
		center                  *embeddingCenter
		qDotMean, qCenteredNorm float64
		centeringOn             bool
	)
	if ms.recallCentered.Load() && len(queryEmbedding) > 0 {
		center = ms.center.Load()
		qDotMean, qCenteredNorm, centeringOn = center.queryStats(queryEmbedding)
	}

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

		// T84: review-queue items are service pointers ("Promotion candidate:
		// memory <uuid>…"), not knowledge. Exclude them from semantic recall —
		// their vector is built from the target's query text, so they self-poison
		// kNN and surface above the answer. They remain visible via the
		// ProjectBank review view (List-based), which is their only consumer.
		if isReviewQueueCached(m) {
			continue
		}

		// T122: the same argument for a different shape. A body that is nothing
		// but "- Promoted canonical: <uuid>" bullets matches a query about
		// promotion honestly and answers none of it — the record is a journal of
		// what was done, not knowledge about it. 80 such records were already in
		// the bank when the write boundary learned to refuse them (the guard
		// existed since T85 but sat only on the checkpoint path), and they stay:
		// they are the raw material of the unprocessed-summary queue, which reads
		// them through List. Only semantic selection skips them.
		if m.ActivityLog {
			continue
		}

		// Superseded entries (temporal replacement, e.g. after a merge or
		// MarkOutdated) are invisible to semantic recall — the successor
		// carries the current knowledge, while the old vector is unchanged and
		// keeps out-ranking it. They stay visible to List/ListLightweight so
		// maintenance tools still see the temporal history. The successor is
		// looked up rather than trusted: Delete does not clear superseded_by on
		// predecessors, and a dangling pointer would bury the entry forever.
		if m.SupersededBy != "" && ms.hasMemory(m.SupersededBy) {
			continue
		}

		// T48 layer-aware filtering: when the flag is on, surface memories
		// are invisible outside their originating Context. This prevents
		// session scratch state from leaking into unrelated recall calls.
		// T90 L3: normalized once per candidate. The layer was normalized here
		// and again for the boost switch below — two allocations-free but
		// non-trivial calls per candidate on the hottest loop in recall.
		layer := DefaultSedimentLayer
		if sedimentOn {
			layer = NormalizeSedimentLayer(string(m.SedimentLayer))
			if layer == LayerSurface {
				if filters.Context == "" || filters.Context != m.Context {
					continue
				}
			}
		}

		trust := deriveTrustMetadataFromCached(m, now)

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 && m.EmbeddingModel != "" && m.EmbeddingModel == queryModelID {
			if centeringOn {
				score = center.similarity(queryEmbedding, m.Embedding, qDotMean, qCenteredNorm)
			} else {
				score = scoring.CosineSimilarity(queryEmbedding, m.Embedding)
			}
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
			switch layer {
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

	// T113: `limit > 0` is the whole distinction. A bounded recall returns the
	// top N of a question and those N did answer it; a sweep (limit <= 0, the
	// shape RecallCanonical passes) returns everything above minScore and
	// proves nothing about any single entry. Both are recorded; only the first
	// moves the counter the sediment gate reads.
	select {
	case ms.accessCh <- accessEvent{ids: ids, targeted: limit > 0}:
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

// accessEvent is one batch of retrieved IDs plus the kind of retrieval that
// produced them. T113: the two kinds are accounted separately — see
// Memory.TargetedAccessCount — so the flag has to survive the trip through
// the channel rather than being decided at flush time.
type accessEvent struct {
	ids      []string
	targeted bool
}

// accessStatsWorker drains accessCh and flushes batched access stats updates.
// Batches are flushed when 100 IDs accumulate or after 5 seconds of inactivity.
// Targeted and sweep accesses are batched apart: merging them would force the
// flush to pick one meaning for the whole batch.
func (ms *Store) accessStatsWorker() {
	defer ms.accessWG.Done()

	const (
		maxBatch     = 100
		flushTimeout = 5 * time.Second
	)
	batches := map[bool]map[string]struct{}{
		true:  make(map[string]struct{}, maxBatch),
		false: make(map[string]struct{}, maxBatch),
	}
	timer := time.NewTimer(flushTimeout)
	defer timer.Stop()

	flushOne := func(targeted bool) {
		batch := batches[targeted]
		if len(batch) == 0 {
			return
		}
		ids := make([]string, 0, len(batch))
		for id := range batch {
			ids = append(ids, id)
		}
		clear(batch)
		ms.flushAccessStats(ids, targeted)
	}
	flush := func() {
		flushOne(true)
		flushOne(false)
	}

	for {
		select {
		case ev, ok := <-ms.accessCh:
			if !ok {
				flush()
				return
			}
			batch := batches[ev.targeted]
			for _, id := range ev.ids {
				batch[id] = struct{}{}
			}
			if len(batch) >= maxBatch {
				flushOne(ev.targeted)
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
func (ms *Store) flushAccessStats(ids []string, targeted bool) {
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

	update := `UPDATE memories SET accessed_at = ?, access_count = access_count + 1 WHERE id = ?`
	if targeted {
		update = `UPDATE memories SET accessed_at = ?, access_count = access_count + 1,
			targeted_access_count = targeted_access_count + 1 WHERE id = ?`
	}
	stmt, err := tx.Prepare(update)
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

	// T88 H1: copy-on-write. Recall/ListLightweight read these fields off
	// snapshot pointers without the lock, so the published entry is replaced,
	// not edited.
	ms.mu.Lock()
	for _, id := range successIDs {
		if m, exists := ms.memories[id]; exists {
			updated := m.cloneForUpdate()
			updated.AccessedAt = now
			updated.AccessCount++
			if targeted {
				updated.TargetedAccessCount++
			}
			ms.cacheSetLocked(updated)
		}
	}
	ms.mu.Unlock()
}

// memoryColumns is the canonical SELECT list for full *Memory rows. Kept in
// sync with scanMemoryRow. Loader (loadMemoriesToCache) historically used a
// shorter subset that omitted replaces/observed_at — those gaps are
// preserved there for now to avoid widening the cachedMemory shape.
const memoryColumns = `id, content, type, title, tags, context, importance, metadata, embedding_model,
		embedding, created_at, updated_at, accessed_at, access_count, targeted_access_count,
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
	return scanMemoryRowCounting(scanner, nil)
}

// scanMemoryRowCounting is scanMemoryRow with an optional counter for
// non-fatal decode problems — a tags blob that will not unmarshal, an
// embedding that will not decode. Those are swallowed for ad-hoc readers
// (nil counter) but the cache loader passes one, because memory_stats reports
// it and a silently degraded row is precisely what went unnoticed in T87.
//
// T90 D4: the cache loader used to carry its own SELECT list and its own copy
// of this parsing, over a narrower column set. The two had already drifted
// once, and the drift is invisible — the cache simply lacks a field nobody
// notices until a consumer reads zero. One column list, one parser.
func scanMemoryRowCounting(scanner rowScanner, softErrors *int) (*Memory, error) {
	countSoft := func() {
		if softErrors != nil {
			*softErrors++
		}
	}

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
		&createdAt, &updatedAt, &accessedAt, &m.AccessCount, &m.TargetedAccessCount,
		&validFrom, &validUntil, &supersededBy, &replaces, &observedAt, &sedimentLayer,
	); err != nil {
		return nil, err
	}

	if tagsJSON.Valid && tagsJSON.String != "" {
		if err := json.Unmarshal([]byte(tagsJSON.String), &m.Tags); err != nil {
			countSoft()
		}
	}
	m.Metadata, _ = parseMetadataJSON(metadataJSON)
	if len(embeddingBlob) > 0 {
		parsed, err := unmarshalEmbeddingBinary(embeddingBlob)
		if err != nil {
			countSoft()
		} else {
			m.Embedding = parsed
		}
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
