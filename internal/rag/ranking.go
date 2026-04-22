package rag

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// sourceBoostSecondary is the retrieval boost applied to secondary knowledge
// artifacts (dead_end, changelog, adr, etc.) when the query matches their
// keyword profile. Runbooks/postmortems use the stronger primary boost (0.08).
const sourceBoostSecondary = 0.07

type hybridCandidate struct {
	chunk         vectorstore.Chunk
	sourceType    string
	semanticScore float64
	keywordScore  float64
	recencyScore  float64
	sourceBoost   float64
	trust         *trust.Metadata
}

func normalizeSourceType(value string) string {
	val := strings.ToLower(strings.TrimSpace(value))
	switch val {
	case "", "any", "all":
		return ""
	case "doc", "docs", "readme":
		return "docs"
	case "adr":
		return "adr"
	case "rfc":
		return "rfc"
	case "change", "changelog", "release", "release_notes":
		return "changelog"
	case "runbook", "runbooks":
		return "runbook"
	case "postmortem", "postmortems", "incident":
		return "postmortem"
	case "dead_end", "dead-end", "deadend", "dead_ends":
		return "dead_end"
	case "ci", "ci_config", "workflow", "pipeline":
		return "ci_config"
	case "helm":
		return "helm"
	case "terraform":
		return "terraform"
	case "k8s", "kubernetes":
		return "k8s"
	default:
		return val
	}
}

func sourceAwareBoost(query string, sourceType string) float64 {
	queryLower := strings.ToLower(query)
	if queryLower == "" || sourceType == "" {
		return 0
	}

	switch sourceType {
	case "runbook":
		if scoring.ContainsAny(queryLower, "runbook", "rollback", "recover", "troubleshoot", "fix", "restart") {
			return 0.08
		}
	case "postmortem":
		if scoring.ContainsAny(queryLower, "incident", "outage", "postmortem", "root cause", "regression") {
			return 0.08
		}
	case "adr", "rfc":
		if scoring.ContainsAny(queryLower, "adr", "rfc", "architecture", "decision", "design") {
			return 0.07
		}
	case "changelog":
		if scoring.ContainsAny(queryLower, "change", "release", "migration", "regression", "what changed") {
			return 0.07
		}
	case "ci_config":
		if scoring.ContainsAny(queryLower, "ci", "pipeline", "workflow", "github actions", "gitlab") {
			return 0.07
		}
	case "helm":
		if scoring.ContainsAny(queryLower, "helm", "chart", "values") {
			return 0.07
		}
	case "terraform":
		if scoring.ContainsAny(queryLower, "terraform", "tf", "plan", "state", "module") {
			return 0.07
		}
	case "k8s":
		if scoring.ContainsAny(queryLower, "k8s", "kubernetes", "deployment", "ingress", "service") {
			return 0.07
		}
	case "dead_end":
		// Pass the raw query — IsPitfallQuery handles case-insensitivity and
		// uses word-boundary matching so "retry storm" won't fire "try".
		if scoring.IsPitfallQuery(query) {
			return sourceBoostSecondary
		}
	}

	return 0
}

func recencyBoost(lastModified time.Time, now time.Time) float64 {
	if lastModified.IsZero() {
		return 0
	}

	age := now.Sub(lastModified)
	switch {
	case age <= 7*24*time.Hour:
		return 0.06
	case age <= 30*24*time.Hour:
		return 0.04
	case age <= 90*24*time.Hour:
		return 0.025
	case age <= 180*24*time.Hour:
		return 0.01
	default:
		return 0
	}
}

func trustMetadataForDocument(sourceType string, lastModified time.Time, now time.Time) *trust.Metadata {
	normalizedType := normalizeSourceType(sourceType)
	if normalizedType == "" {
		normalizedType = "docs"
	}
	if lastModified.IsZero() {
		lastModified = now
	}

	return &trust.Metadata{
		KnowledgeLayer: "document",
		SourceType:     normalizedType,
		Confidence:     documentConfidence(normalizedType),
		LastVerifiedAt: lastModified,
		Owner:          documentOwner(normalizedType),
		FreshnessScore: scoring.FreshnessScore(lastModified, now),
	}
}

func documentConfidence(sourceType string) float64 {
	switch sourceType {
	case "adr":
		return 0.98
	case "runbook":
		return 0.94
	case "postmortem":
		return 0.92
	case "dead_end":
		return 0.90
	case "changelog":
		return 0.90
	case "ci_config":
		return 0.89
	case "rfc":
		return 0.88
	case "helm", "terraform", "k8s":
		return 0.87
	case "docs":
		return 0.75
	default:
		return 0.70
	}
}

func documentOwner(sourceType string) string {
	switch sourceType {
	case "adr", "rfc", "docs":
		return "engineering"
	case "runbook", "postmortem", "dead_end":
		return "operations"
	case "changelog":
		return "release"
	case "ci_config", "helm", "terraform", "k8s":
		return "platform"
	default:
		return "unknown"
	}
}

func confidenceBoost(confidence float64) float64 {
	return math.Max(0, (confidence-0.50)*0.05)
}

// buildHybridSearchResults fuses semantic+keyword candidates into ordered
// SearchResult rows and returns a parallel slice of full chunk content keyed
// by index. The parallel content slice is consumed by the neural reranker
// path (see vectorService.applyReranker) which needs the full chunk text —
// the snippet in SearchResult is already truncated to 200 chars for display.
//
// The content slice always has the same length and ordering as the returned
// []SearchResult, so content[i] is the full text for results[i].
func buildHybridSearchResults(query string, sourceTypeFilter string, semanticResults []vectorstore.SearchResult, keywordResults []vectorstore.SearchResult, indexedChunks int, limit int, debug bool) ([]SearchResult, []string, *SearchDebug) {
	now := time.Now()
	normalizedFilter := normalizeSourceType(sourceTypeFilter)

	candidateMap := make(map[string]*hybridCandidate, len(semanticResults)+len(keywordResults))
	filteredOut := 0
	discardedAsNoise := 0
	maxKeywordScore := 0.0

	for _, result := range semanticResults {
		candidate := candidateMap[result.ID]
		if candidate == nil {
			candidate = &hybridCandidate{
				chunk:      result.Chunk,
				sourceType: classifySourceType(result.DocPath, result.Title, result.Content),
			}
			candidateMap[result.ID] = candidate
		}
		if result.Score > candidate.semanticScore {
			candidate.semanticScore = result.Score
		}
	}

	for _, result := range keywordResults {
		candidate := candidateMap[result.ID]
		if candidate == nil {
			candidate = &hybridCandidate{
				chunk:      result.Chunk,
				sourceType: classifySourceType(result.DocPath, result.Title, result.Content),
			}
			candidateMap[result.ID] = candidate
		}
		if result.Score > candidate.keywordScore {
			candidate.keywordScore = result.Score
		}
		if result.Score > maxKeywordScore {
			maxKeywordScore = result.Score
		}
	}

	candidates := make([]hybridCandidate, 0, len(candidateMap))
	for _, candidate := range candidateMap {
		if normalizedFilter != "" && candidate.sourceType != normalizedFilter {
			filteredOut++
			continue
		}
		if candidate.semanticScore < 0.1 && candidate.keywordScore <= 0 {
			discardedAsNoise++
			continue
		}

		candidate.recencyScore = recencyBoost(candidate.chunk.LastModified, now)
		candidate.sourceBoost = sourceAwareBoost(query, candidate.sourceType)
		candidate.trust = trustMetadataForDocument(candidate.sourceType, candidate.chunk.LastModified, now)
		candidates = append(candidates, *candidate)
	}

	// We keep full chunk content in a parallel slice to searchResults so the
	// public SearchResult shape stays snippet-only (no JSON drift for API
	// consumers) while the internal reranker path still gets the full text.
	type resultWithContent struct {
		result  SearchResult
		content string
	}
	rows := make([]resultWithContent, 0, len(candidates))
	for _, candidate := range candidates {
		keywordComponent := 0.0
		if maxKeywordScore > 0 {
			keywordComponent = candidate.keywordScore / maxKeywordScore
		}

		confidenceComponent := confidenceBoost(candidate.trust.Confidence)
		score := candidate.semanticScore*0.60 + keywordComponent*0.30 + candidate.recencyScore + candidate.sourceBoost + confidenceComponent
		if keywordComponent > 0 && candidate.semanticScore < 0.1 {
			score += 0.05
		}

		fullContent := candidate.chunk.Content
		snippet := fullContent
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}

		result := SearchResult{
			ID:           candidate.chunk.ID,
			Title:        candidate.chunk.Title,
			Path:         candidate.chunk.DocPath,
			SourceType:   candidate.sourceType,
			Score:        score,
			Snippet:      snippet,
			LastModified: candidate.chunk.LastModified,
			Trust:        candidate.trust,
		}
		if debug {
			result.Debug = &ResultDebug{
				Breakdown: ScoreBreakdown{
					Semantic:          candidate.semanticScore,
					KeywordRaw:        candidate.keywordScore,
					KeywordNormalized: keywordComponent,
					RecencyBoost:      candidate.recencyScore,
					SourceBoost:       candidate.sourceBoost,
					ConfidenceBoost:   confidenceComponent,
					FinalScore:        score,
				},
				AppliedBoosts: appliedBoosts(candidate, keywordComponent, confidenceComponent),
			}
		}
		rows = append(rows, resultWithContent{result: result, content: fullContent})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].result.Score == rows[j].result.Score {
			return rows[i].result.LastModified.After(rows[j].result.LastModified)
		}
		return rows[i].result.Score > rows[j].result.Score
	})

	if len(rows) > limit {
		rows = rows[:limit]
	}

	searchResults := make([]SearchResult, len(rows))
	contents := make([]string, len(rows))
	for i, row := range rows {
		searchResults[i] = row.result
		contents[i] = row.content
	}

	if !debug {
		return searchResults, contents, nil
	}

	debugInfo := &SearchDebug{
		RankingSignals: []string{
			"semantic_similarity",
			"keyword_bm25_like",
			"source_type_filter",
			"recency_boost",
			"source_type_weighting",
			"trust_confidence",
			"freshness_score",
		},
		IndexedChunks:    indexedChunks,
		FilteredOut:      filteredOut,
		DiscardedAsNoise: discardedAsNoise,
		CandidateCount:   len(candidates),
		ReturnedCount:    len(searchResults),
	}
	if normalizedFilter != "" {
		debugInfo.AppliedFilters = []string{fmt.Sprintf("source_type=%s", normalizedFilter)}
	}

	return searchResults, contents, debugInfo
}

func appliedBoosts(candidate hybridCandidate, keywordComponent float64, confidenceComponent float64) []string {
	boosts := make([]string, 0, 4)
	if candidate.semanticScore >= 0.1 {
		boosts = append(boosts, "semantic_similarity")
	}
	if keywordComponent > 0 {
		boosts = append(boosts, "keyword_match")
	}
	if candidate.recencyScore > 0 {
		boosts = append(boosts, fmt.Sprintf("recency(+%.3f)", candidate.recencyScore))
	}
	if candidate.sourceBoost > 0 {
		boosts = append(boosts, fmt.Sprintf("source_type:%s(+%.3f)", candidate.sourceType, candidate.sourceBoost))
	}
	if confidenceComponent > 0 {
		boosts = append(boosts, fmt.Sprintf("trust_confidence(+%.3f)", confidenceComponent))
	}
	return boosts
}
