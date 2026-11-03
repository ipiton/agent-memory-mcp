package vectorstore

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/kljensen/snowball"
	"go.uber.org/zap"
)

// Keyword search lives in an FTS5 table on disk (T163). The in-memory inverted
// index it replaces was 72-78% of the live heap and made every process rebuild
// it on start, which also held up the MCP handshake.
//
// Tokens are prepared here rather than by an FTS5 tokenizer: FTS5 has no Russian
// stemmer, and a query has to go through the same steps as the text. The table
// is contentless, so the text is stored once, in chunks.
const (
	ftsTable = "chunks_fts"

	// keywordIndexVersion names what produced the tokens. Bumping it refills the
	// table from chunks on the next open; no re-embedding is involved.
	keywordIndexVersion    = "1"
	keywordIndexVersionKey = "keyword_index_version"

	// bm25 is only as good as the candidates it gets: title and path boosts can
	// lift a chunk it ranks low, so a second probe looks at those columns alone.
	minKeywordCandidates = 300
	titlePathCandidates  = 100

	keywordBackfillBatch   = 2000
	keywordBackfillRetries = 10
)

const ftsSchema = `CREATE VIRTUAL TABLE IF NOT EXISTS ` + ftsTable + ` USING fts5(
	title, doc_path, content,
	content='', contentless_delete=1,
	tokenize='unicode61 remove_diacritics 0'
)`

const keywordCountsQuery = `SELECT (SELECT COUNT(*) FROM chunks), (SELECT COUNT(*) FROM ` + ftsTable + `_docsize)`

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// KeywordSearch finds keyword-relevant chunks through the FTS5 index.
func (s *SQLiteStore) KeywordSearch(query string, limit int) ([]SearchResult, error) {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	terms := uniqueKeywordTerms(stemTokens(tokenizeKeywordText(queryLower)))
	if len(terms) == 0 {
		return []SearchResult{}, nil
	}
	if err := s.waitKeywordIndex(); err != nil {
		return nil, err
	}

	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	match := strings.Join(quoted, " OR ")

	candidates, err := s.keywordCandidates(match, max(limit*3, minKeywordCandidates))
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	titled, err := s.keywordCandidates("{title doc_path} : ("+match+")", titlePathCandidates)
	if err != nil {
		return nil, fmt.Errorf("keyword search in titles: %w", err)
	}
	for id, result := range titled {
		if _, ok := candidates[id]; !ok {
			candidates[id] = result
		}
	}

	results := make([]SearchResult, 0, len(candidates))
	for _, result := range candidates {
		result.Score += keywordBoost(queryLower, terms, result.Chunk)
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			if results[i].LastModified.Equal(results[j].LastModified) {
				return results[i].ID < results[j].ID
			}
			return results[i].LastModified.After(results[j].LastModified)
		}
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *SQLiteStore) keywordCandidates(match string, limit int) (map[string]SearchResult, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.doc_path, c.content, c.title, c.last_modified, m.score
		FROM (
			SELECT rowid AS rid, -bm25(`+ftsTable+`) AS score
			FROM `+ftsTable+` WHERE `+ftsTable+` MATCH ?
			ORDER BY bm25(`+ftsTable+`) LIMIT ?
		) m
		JOIN chunks c ON c.rowid = m.rid`, match, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]SearchResult, limit)
	for rows.Next() {
		var result SearchResult
		var title sql.NullString
		var lastModified sql.NullTime
		if err := rows.Scan(&result.ID, &result.DocPath, &result.Content, &title, &lastModified, &result.Score); err != nil {
			return nil, err
		}
		result.Content = sanitizeUTF8(result.Content)
		result.Title = sanitizeUTF8(title.String)
		if lastModified.Valid {
			result.LastModified = lastModified.Time
		}
		out[result.ID] = result
	}
	return out, rows.Err()
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

// stemTokens reduces Cyrillic words to their stem and leaves everything else
// alone: identifiers and English terms are searched as written (T148).
func stemTokens(tokens []string) []string {
	for i, token := range tokens {
		if !isCyrillicWord(token) {
			continue
		}
		if stem, err := snowball.Stem(token, "russian", false); err == nil && stem != "" {
			tokens[i] = stem
		}
	}
	return tokens
}

func isCyrillicWord(word string) bool {
	for _, r := range word {
		if !unicode.Is(unicode.Cyrillic, r) {
			return false
		}
	}
	return word != ""
}

func normalizeForKeywordIndex(text string) string {
	return strings.Join(stemTokens(tokenizeKeywordText(text)), " ")
}

func insertKeywordRow(q sqlExecer, rowid int64, title, docPath, content string) error {
	_, err := q.Exec(`INSERT INTO `+ftsTable+`(rowid, title, doc_path, content) VALUES (?, ?, ?, ?)`,
		rowid, normalizeForKeywordIndex(title), normalizeForKeywordIndex(docPath), normalizeForKeywordIndex(content))
	return err
}

// deleteKeywordRowByChunkID must run before the row leaves chunks: the rowid is
// the only link between the two tables.
func deleteKeywordRowByChunkID(q sqlExecer, id string) error {
	_, err := q.Exec(`DELETE FROM `+ftsTable+` WHERE rowid IN (SELECT rowid FROM chunks WHERE id = ?)`, id)
	return err
}

// prepareKeywordIndex creates the FTS table and returns false when it still has
// to be filled from chunks: on the first open after an upgrade, after a change of
// tokenization, or when the row counts disagree because a build that predates
// the table wrote to chunks.
func (s *SQLiteStore) prepareKeywordIndex() (bool, error) {
	if _, err := s.db.Exec(ftsSchema); err != nil {
		return false, fmt.Errorf("create keyword index: %w", err)
	}
	stored, err := s.GetMetadata(keywordIndexVersionKey)
	if err != nil && !errors.Is(err, ErrMetadataNotFound) {
		return false, err
	}
	var chunks, indexed int
	if err := s.db.QueryRow(keywordCountsQuery).Scan(&chunks, &indexed); err != nil {
		return false, err
	}
	if chunks == 0 && indexed == 0 {
		return true, s.SetMetadata(keywordIndexVersionKey, keywordIndexVersion)
	}
	return stored == keywordIndexVersion && chunks == indexed, nil
}

// backfillKeywordIndex fills the table from chunks in one transaction, so a crash
// leaves no half-built index and a second process waiting for the lock finds the
// work done. The transaction holds the write lock for the whole build (about 20s
// on 110k chunks, once per upgrade); writers in this process wait on ftsReady,
// and another process's write can hit busy_timeout and retry on its next run.
func (s *SQLiteStore) backfillKeywordIndex() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	err = tx.QueryRow(`SELECT value FROM index_metadata WHERE key = ?`, keywordIndexVersionKey).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var chunks, indexed int
	if err := tx.QueryRow(keywordCountsQuery).Scan(&chunks, &indexed); err != nil {
		return err
	}
	if version == keywordIndexVersion && chunks == indexed {
		return nil // another process finished it while this one waited for the lock
	}
	if _, err := tx.Exec(`DELETE FROM ` + ftsTable); err != nil {
		return err
	}

	type row struct {
		rowid                int64
		title, path, content string
	}
	var cursor int64
	for {
		// A page at a time: the corpus does not fit comfortably in memory, and
		// modernc SQLite cannot write while a cursor is open on the connection.
		rows, err := tx.Query(`SELECT rowid, title, doc_path, content FROM chunks WHERE rowid > ? ORDER BY rowid LIMIT ?`, cursor, keywordBackfillBatch)
		if err != nil {
			return err
		}
		page := make([]row, 0, keywordBackfillBatch)
		for rows.Next() {
			var r row
			var title sql.NullString
			if err := rows.Scan(&r.rowid, &title, &r.path, &r.content); err != nil {
				_ = rows.Close()
				return err
			}
			r.title = title.String
			page = append(page, r)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			if err := insertKeywordRow(tx, r.rowid, r.title, r.path, r.content); err != nil {
				return err
			}
		}
		cursor = page[len(page)-1].rowid
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)`, keywordIndexVersionKey, keywordIndexVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) runKeywordBackfill() {
	defer s.backfill.Done()
	defer close(s.ftsReady)

	began := time.Now()
	var err error
	// Another process may hold the write lock for its own build for longer than
	// busy_timeout. It will finish, so keep waiting rather than fail the search.
	for range keywordBackfillRetries {
		if err = s.backfillKeywordIndex(); err == nil {
			s.logger.Info("Keyword index built", zap.Duration("took", time.Since(began)))
			return
		}
		s.logger.Warn("Keyword index build failed, retrying", zap.Error(err))
	}
	s.ftsErr = fmt.Errorf("keyword index unavailable: %w", err)
	s.logger.Error("Keyword index build gave up", zap.Error(err))
}

// waitKeywordIndex blocks until the first-time build finished. Searching a
// half-filled table would answer "nothing found", which reads as a fact.
func (s *SQLiteStore) waitKeywordIndex() error {
	<-s.ftsReady
	return s.ftsErr
}

// keywordScoreConfig holds the boost coefficients for keyword scoring.
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
	TitleExact    float64
	TitleAllTerms float64
	PathExact     float64
	PathSlugged   float64
	PathAllTerms  float64
	ContentExact  float64
}

var defaultKeywordScoreConfig = keywordScoreConfig{
	TitleExact:    1.4,
	TitleAllTerms: 0.8,
	PathExact:     1.0,
	PathSlugged:   0.9,
	PathAllTerms:  0.6,
	ContentExact:  0.5,
}

// keywordBoost adds the title, path and phrase signals on top of the BM25 score.
func keywordBoost(queryLower string, terms []string, chunk Chunk) float64 {
	cfg := defaultKeywordScoreConfig
	titleLower := strings.ToLower(chunk.Title)
	pathLower := strings.ToLower(chunk.DocPath)

	score := 0.0
	switch {
	case strings.Contains(titleLower, queryLower):
		score += cfg.TitleExact
	case allTermsPresent(titleLower, terms):
		score += cfg.TitleAllTerms
	}

	switch {
	case strings.Contains(pathLower, queryLower):
		score += cfg.PathExact
	case strings.Contains(pathLower, strings.ReplaceAll(queryLower, " ", "-")):
		score += cfg.PathSlugged
	case allTermsPresent(pathLower, terms):
		score += cfg.PathAllTerms
	}

	if strings.Contains(strings.ToLower(chunk.Content), queryLower) {
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
