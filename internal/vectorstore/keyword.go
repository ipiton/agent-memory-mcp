package vectorstore

import (
	"math"
	"sort"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
)

type keywordDocStats struct {
	termFreq     map[string]int
	tokenCount   int
	titleLower   string
	pathLower    string
	contentLower string
}

type keywordQueryStats struct {
	totalDocs      int
	avgChunkLength float64
	docFreq        map[string]int
}

// KeywordSearch finds keyword-relevant chunks using a precomputed in-memory inverted index.
func (s *SQLiteStore) KeywordSearch(query string, limit int) ([]SearchResult, error) {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	queryTerms := uniqueKeywordTerms(tokenizeKeywordText(queryLower))
	if len(queryTerms) == 0 {
		return []SearchResult{}, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.keywordDocs) == 0 {
		return []SearchResult{}, nil
	}

	candidateIDs := make(map[string]struct{}, len(queryTerms)*4)
	docFreq := make(map[string]int, len(queryTerms))
	for _, term := range queryTerms {
		postings := s.keywordPostings[term]
		docFreq[term] = len(postings)
		for chunkID := range postings {
			candidateIDs[chunkID] = struct{}{}
		}
	}

	if len(candidateIDs) == 0 {
		return []SearchResult{}, nil
	}

	avgChunkLength := 1.0
	if len(s.keywordDocs) > 0 && s.totalKeywordTokens > 0 {
		avgChunkLength = float64(s.totalKeywordTokens) / float64(len(s.keywordDocs))
		if avgChunkLength == 0 {
			avgChunkLength = 1
		}
	}
	queryStats := keywordQueryStats{
		totalDocs:      len(s.keywordDocs),
		avgChunkLength: avgChunkLength,
		docFreq:        docFreq,
	}

	results := make([]SearchResult, 0, len(candidateIDs))
	for chunkID := range candidateIDs {
		chunk := s.chunks[chunkID]
		if chunk == nil {
			continue
		}
		docStats, ok := s.keywordDocs[chunkID]
		if !ok {
			continue
		}

		score := keywordScore(queryLower, queryTerms, queryStats, docStats)
		if score <= 0 {
			continue
		}

		results = append(results, SearchResult{
			Chunk: *chunk,
			Score: score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].LastModified.After(results[j].LastModified)
		}
		return results[i].Score > results[j].Score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func tokenizeKeywordText(value string) []string {
	fields := scoring.TokenizeWords(value)

	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 || field == "k" {
			continue
		}
		tokens = append(tokens, field)
	}
	return tokens
}

func uniqueKeywordTerms(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

func buildKeywordDocStats(chunk *Chunk) keywordDocStats {
	tokens := tokenizeKeywordText(chunk.Title + " " + chunk.DocPath + " " + chunk.Content)
	stats := keywordDocStats{
		termFreq:     make(map[string]int, len(tokens)),
		tokenCount:   len(tokens),
		titleLower:   strings.ToLower(chunk.Title),
		pathLower:    strings.ToLower(chunk.DocPath),
		contentLower: strings.ToLower(chunk.Content),
	}
	for _, token := range tokens {
		stats.termFreq[token]++
	}
	return stats
}

func (s *SQLiteStore) indexChunkKeywordsLocked(chunk *Chunk) {
	stats := buildKeywordDocStats(chunk)
	s.keywordDocs[chunk.ID] = stats
	s.totalKeywordTokens += stats.tokenCount

	for term, tf := range stats.termFreq {
		postings := s.keywordPostings[term]
		if postings == nil {
			postings = make(map[string]int)
			s.keywordPostings[term] = postings
		}
		postings[chunk.ID] = tf
	}
}

func (s *SQLiteStore) removeChunkKeywordsLocked(chunkID string) {
	stats, ok := s.keywordDocs[chunkID]
	if !ok {
		return
	}

	delete(s.keywordDocs, chunkID)
	s.totalKeywordTokens -= stats.tokenCount
	if s.totalKeywordTokens < 0 {
		s.totalKeywordTokens = 0
	}

	for term := range stats.termFreq {
		postings := s.keywordPostings[term]
		if postings == nil {
			continue
		}
		delete(postings, chunkID)
		if len(postings) == 0 {
			delete(s.keywordPostings, term)
		}
	}
}

// keywordScoreConfig holds the BM25 + boost coefficients for keyword scoring.
// Round 3 L13: extracted from inline magic numbers so they can be tuned in
// one place (rather than chasing six +0.5/+1.4/+0.8/+0.9/+0.6 literals
// across the function body).
//
// Boost rationale:
//   - TitleExact (1.4): exact-substring match in the title is the strongest
//     signal in our retrieval suite. Doubled relative to AllTermsPresent.
//   - TitleAllTerms (0.8): all query terms present somewhere in the title
//     but not as an exact substring (word-order independent).
//   - PathExact (1.0): exact match in the document path. Slightly lower
//     than TitleExact because paths are often deep/noisy.
//   - PathSlugged (0.9): exact-substring after replacing spaces with "-",
//     covers slugified URLs.
//   - PathAllTerms (0.6): weakest path signal.
//   - ContentExact (0.5): floor; content-substring matches exist but
//     correlate weakly with relevance for short queries.
type keywordScoreConfig struct {
	K1            float64 // BM25 saturation
	B             float64 // BM25 length normalization
	TitleExact    float64
	TitleAllTerms float64
	PathExact     float64
	PathSlugged   float64
	PathAllTerms  float64
	ContentExact  float64
}

var defaultKeywordScoreConfig = keywordScoreConfig{
	K1:            1.2,
	B:             0.75,
	TitleExact:    1.4,
	TitleAllTerms: 0.8,
	PathExact:     1.0,
	PathSlugged:   0.9,
	PathAllTerms:  0.6,
	ContentExact:  0.5,
}

func keywordScore(queryLower string, queryTerms []string, queryStats keywordQueryStats, stats keywordDocStats) float64 {
	if len(queryTerms) == 0 || queryStats.totalDocs == 0 || stats.tokenCount == 0 {
		return 0
	}

	titleLower := stats.titleLower
	pathLower := stats.pathLower
	contentLower := stats.contentLower

	cfg := defaultKeywordScoreConfig

	score := 0.0
	chunkLength := float64(stats.tokenCount)
	for _, term := range queryTerms {
		tf := stats.termFreq[term]
		if tf == 0 {
			continue
		}

		df := queryStats.docFreq[term]
		if df == 0 {
			continue
		}

		idf := math.Log(1 + (float64(queryStats.totalDocs)-float64(df)+0.5)/(float64(df)+0.5))
		denom := float64(tf) + cfg.K1*(1-cfg.B+cfg.B*(chunkLength/queryStats.avgChunkLength))
		score += idf * ((float64(tf) * (cfg.K1 + 1)) / denom)
	}

	if score == 0 && !scoring.ContainsAny(titleLower, queryTerms...) && !scoring.ContainsAny(pathLower, queryTerms...) && !scoring.ContainsAny(contentLower, queryTerms...) {
		return 0
	}

	switch {
	case strings.Contains(titleLower, queryLower):
		score += cfg.TitleExact
	case allTermsPresent(titleLower, queryTerms):
		score += cfg.TitleAllTerms
	}

	switch {
	case strings.Contains(pathLower, queryLower):
		score += cfg.PathExact
	case strings.Contains(pathLower, strings.ReplaceAll(queryLower, " ", "-")):
		score += cfg.PathSlugged
	case allTermsPresent(pathLower, queryTerms):
		score += cfg.PathAllTerms
	}

	if strings.Contains(contentLower, queryLower) {
		score += cfg.ContentExact
	}

	return score
}

func allTermsPresent(value string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !strings.Contains(value, term) {
			return false
		}
	}
	return true
}
