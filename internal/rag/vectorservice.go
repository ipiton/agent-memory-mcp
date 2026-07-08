package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/reranker"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

type vectorService struct {
	config vecServiceConfig
	logger *zap.Logger
	store  vectorstore.Store
}

func newVectorService(cfg vecServiceConfig, logger *zap.Logger) (*vectorService, error) {
	if err := os.MkdirAll(cfg.IndexPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create index directory: %w", err)
	}

	dbPath := filepath.Join(cfg.IndexPath, "vectors.db")
	logger.Info("Using SQLite vector store", zap.String("db_path", dbPath))

	store, err := vectorstore.NewSQLiteStore(dbPath, cfg.Embedder.Dimensions(), logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	return &vectorService{
		config: cfg,
		logger: logger,
		store:  store,
	}, nil
}

func (vs *vectorService) search(ctx context.Context, query searchQuery) (*SearchResponse, error) {
	startTime := time.Now()

	queryResult, err := vs.config.Embedder.EmbedQueryDetailed(ctx, query.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	storedModel, err := vs.store.GetMetadata("embedding_model")
	if err == nil && storedModel != "" && storedModel != queryResult.ModelID {
		return nil, fmt.Errorf("embedding model mismatch: index was built with %s but current query model is %s. Run index_documents to rebuild the index", storedModel, queryResult.ModelID)
	}

	semanticLimit := max(query.Limit*8, 50)
	keywordLimit := max(query.Limit*12, 100)

	semanticResults, err := vs.store.Search(queryResult.Embedding, semanticLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to load semantic candidates: %w", err)
	}

	keywordResults, err := vs.store.KeywordSearch(query.Query, keywordLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to load keyword candidates: %w", err)
	}

	searchResults, contents, debugInfo := buildHybridSearchResults(query.Query, query.SourceType, semanticResults, keywordResults, vs.store.Count(), query.Limit, query.Debug)

	// Neural reranker pass — strictly opt-in. Any error or timeout falls
	// back to hybrid ordering without bubbling an error to the caller.
	if vs.config.Reranker != nil && len(searchResults) > 1 {
		searchResults = vs.applyReranker(ctx, query.Query, searchResults, contents, debugInfo)
	}

	return &SearchResponse{
		Query:      query.Query,
		Results:    searchResults,
		TotalFound: len(searchResults),
		SearchTime: time.Since(startTime).Milliseconds(),
		Debug:      debugInfo,
	}, nil
}

// applyReranker runs the cross-encoder over the top-N hybrid candidates,
// reorders them by the returned relevance scores, and decorates debug output.
//
// Fallback rules (CRITICAL — must stay intact so search stays alive under
// provider outages):
//   - If Reranker.Rerank returns an error or the context deadline fires, we
//     keep the original hybrid ordering and append a "rerank_failed:<reason>"
//     signal to the debug trace.
//   - Tail items beyond top-N are never touched; they keep their hybrid
//     final_score and their relative order.
//   - For the reranked head, FinalScore is replaced with the rerank score
//     (0..1). The tail keeps its hybrid FinalScore. ScoreBreakdown still
//     carries the original hybrid signals for downstream comparisons.
//
// contents[i] is the full chunk text for results[i] — we pass the full
// content (not the 200-char display snippet) to the reranker so the
// cross-encoder's ~8k token window is actually used.
func (vs *vectorService) applyReranker(ctx context.Context, query string, results []SearchResult, contents []string, debugInfo *SearchDebug) []SearchResult {
	topN := vs.config.RerankTopN
	if topN <= 0 {
		topN = 40
	}
	if topN > len(results) {
		topN = len(results)
	}
	if topN > maxRerankTopN {
		vs.logger.Warn("rerank top_n clamped",
			zap.Int("requested", topN),
			zap.Int("applied", maxRerankTopN),
		)
		topN = maxRerankTopN
	}

	candidates := make([]reranker.Candidate, topN)
	for i, r := range results[:topN] {
		content := r.Snippet
		if i < len(contents) && contents[i] != "" {
			content = contents[i]
		}
		candidates[i] = reranker.Candidate{
			ID:      r.ID,
			Title:   r.Title,
			Content: content,
		}
	}

	timeout := vs.config.RerankTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	scored, err := vs.config.Reranker.Rerank(rctx, query, candidates)
	elapsed := time.Since(started).Milliseconds()

	if err != nil {
		vs.logger.Warn("Rerank failed, falling back to hybrid ordering",
			zap.Error(err),
			zap.Int64("elapsed_ms", elapsed),
			zap.Int("top_n", topN),
		)
		if debugInfo != nil {
			debugInfo.RankingSignals = append(debugInfo.RankingSignals, "rerank_failed:"+rerankErrorReason(err))
		}
		return results
	}

	reordered := applyRerankScores(results, scored, topN, elapsed)
	if debugInfo != nil {
		debugInfo.RankingSignals = append(debugInfo.RankingSignals, "+ neural_reranker")
	}
	return reordered
}

// rerankErrorReason compresses an error into a short, lowercase token that
// can be safely embedded in the debug trace (e.g. "timeout", "http_error",
// "decode"). Used for observability, not for logic — callers must never
// branch on this string.
func rerankErrorReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "canceled"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "deadline"), strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "canceled"), strings.Contains(msg, "cancelled"):
		return "canceled"
	case strings.Contains(msg, "decode"):
		return "decode"
	case strings.Contains(msg, "bad_index"), strings.Contains(msg, "out of range"):
		return "bad_index"
	case strings.Contains(msg, "http") && strings.Contains(msg, "status"):
		return "http_error"
	case strings.Contains(msg, "error"):
		return "error"
	}
	return "unknown"
}

// applyRerankScores reorders the top-N slice of results by the rerank scores
// (higher first), appends the untouched tail, and populates per-result
// RerankScore / RerankTimeMs in debug breakdowns.
//
// Candidates present in the rerank response but missing from results (should
// never happen) are ignored; candidates present in results but missing from
// the rerank response are placed after the scored ones in their original
// order so no result is ever dropped.
//
// We explicitly sort the head by rerank score (descending) rather than
// trusting the response order. The Jina API documents a score-sorted
// response, but a stable sort keyed on score keeps us robust against
// provider drift and simplifies in-process mock rerankers that return
// candidates in input order.
func applyRerankScores(results []SearchResult, scored []reranker.Scored, topN int, elapsedMs int64) []SearchResult {
	if topN <= 0 || len(results) == 0 {
		return results
	}
	if topN > len(results) {
		topN = len(results)
	}

	head := results[:topN]
	tail := results[topN:]

	byID := make(map[string]int, len(head))
	for i, r := range head {
		byID[r.ID] = i
	}

	type scoredItem struct {
		id    string
		score float64
		pos   int // original head position, used as tiebreaker
	}

	items := make([]scoredItem, 0, len(scored))
	scoreByID := make(map[string]float64, len(scored))
	for _, s := range scored {
		idx, ok := byID[s.ID]
		if !ok {
			continue
		}
		if _, dup := scoreByID[s.ID]; dup {
			continue
		}
		scoreByID[s.ID] = s.Score
		items = append(items, scoredItem{id: s.ID, score: s.Score, pos: idx})
	}

	// Stable sort: higher score first; ties keep original head order.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].pos < items[j].pos
		}
		return items[i].score > items[j].score
	})

	// Rebuild head in rerank order, then append any head items the reranker
	// skipped (in their original order).
	seen := make(map[string]struct{}, len(items))
	newHead := make([]SearchResult, 0, topN)
	for _, it := range items {
		item := head[it.pos]
		item.Score = it.score
		if item.Debug != nil {
			item.Debug.Breakdown.RerankScore = it.score
			item.Debug.Breakdown.RerankTimeMs = elapsedMs
		}
		newHead = append(newHead, item)
		seen[it.id] = struct{}{}
	}
	for _, r := range head {
		if _, done := seen[r.ID]; done {
			continue
		}
		newHead = append(newHead, r)
	}

	final := make([]SearchResult, 0, len(results))
	final = append(final, newHead...)
	final = append(final, tail...)
	return final
}

// indexDocuments embeds and stores a batch of chunks. reuse maps a chunk's
// content hash to an embedding carried over from a previous index of the same
// file (T70 incremental re-index): chunks whose content is unchanged skip the
// embedder entirely and reuse the existing vector, so editing one section of a
// large file no longer re-embeds the whole file. reuse may be nil.
func (vs *vectorService) indexDocuments(ctx context.Context, docs []document, reuse map[string][]float32) (*indexResult, error) {
	result := &indexResult{
		SuccessIDs: make([]string, 0, len(docs)),
		FailedIDs:  make([]string, 0),
		Errors:     make([]error, 0),
	}

	// Partition into chunks we can reuse (content unchanged) and chunks that
	// still need embedding. embedDocIdx maps each embedded text back to its
	// position in docs.
	reused := make(map[int][]float32, len(docs))
	texts := make([]string, 0, len(docs))
	embedDocIdx := make([]int, 0, len(docs))
	for i, doc := range docs {
		if emb, ok := reuse[calculateFileHash(doc.Content)]; ok && len(emb) > 0 {
			reused[i] = emb
			continue
		}
		texts = append(texts, doc.Content)
		embedDocIdx = append(embedDocIdx, i)
	}

	// finalEmb[i] is the embedding for docs[i] — reused or freshly computed.
	finalEmb := make([][]float32, len(docs))
	for i, emb := range reused {
		finalEmb[i] = emb
	}
	if len(texts) > 0 {
		batchResult, err := vs.config.Embedder.BatchEmbedDetailed(ctx, texts)
		if err != nil {
			// Batch failed entirely — mark all as failed
			for _, doc := range docs {
				result.FailedIDs = append(result.FailedIDs, doc.ID)
			}
			result.Errors = append(result.Errors, err)
			vs.logger.Error("Batch embedding failed", zap.Error(err), zap.Int("count", len(docs)))
			return result, nil
		}
		result.ModelID = batchResult.ModelID
		for pos, docIdx := range embedDocIdx {
			if pos < len(batchResult.Embeddings) {
				finalEmb[docIdx] = batchResult.Embeddings[pos]
			}
		}
	}

	var chunks []vectorstore.Chunk
	for i, doc := range docs {
		emb := finalEmb[i]
		if emb == nil {
			vs.logger.Warn("Nil embedding for document", zap.String("id", doc.ID))
			result.FailedIDs = append(result.FailedIDs, doc.ID)
			result.Errors = append(result.Errors, fmt.Errorf("nil embedding for doc %s", doc.ID))
			continue
		}

		chunks = append(chunks, vectorstore.Chunk{
			ID:           doc.ID,
			DocPath:      doc.Path,
			Content:      doc.Content,
			Title:        doc.Title,
			LastModified: doc.LastModified,
			Embedding:    emb,
		})

		result.SuccessIDs = append(result.SuccessIDs, doc.ID)
	}

	if reusedCount := len(reused); reusedCount > 0 {
		vs.logger.Info("Reused embeddings for unchanged chunks",
			zap.Int("reused", reusedCount),
			zap.Int("embedded", len(texts)))
	}

	if len(chunks) > 0 {
		if err := vs.store.Upsert(chunks); err != nil {
			vs.logger.Error("Failed to upsert chunks", zap.Error(err))
			return result, err
		}
	}

	return result, nil
}

func (vs *vectorService) detectModelID(ctx context.Context, text string) (string, error) {
	result, err := vs.config.Embedder.EmbedDetailed(ctx, text)
	if err != nil {
		return "", err
	}
	return result.ModelID, nil
}

func (vs *vectorService) removeDocument(path string) error {
	return vs.store.DeleteByDocPath(path)
}
