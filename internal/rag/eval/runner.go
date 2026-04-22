//go:build eval

package eval

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
)

// HarnessConfig controls where the harness sources corpus/QA data and how it
// runs queries.
type HarnessConfig struct {
	CorpusDir    string
	QAPath       string
	BaselinePath string
	K            int
}

// Harness wires up a temporary RAG engine against a test corpus and exposes
// a convenient RunAll method for evaluating a whole QA set.
type Harness struct {
	engine  *rag.Engine
	queries []QAQuery
	k       int
}

// NewHarness builds a Harness that can be used in the enclosing test.
// Bootstrap failures call t.Fatalf; on success, cleanup is scheduled via t.Cleanup.
func NewHarness(t *testing.T, cfg HarnessConfig) *Harness {
	t.Helper()

	if cfg.K <= 0 {
		cfg.K = 5
	}

	corpusAbs, err := filepath.Abs(cfg.CorpusDir)
	if err != nil {
		t.Fatalf("abs corpus dir: %v", err)
	}
	if info, err := os.Stat(corpusAbs); err != nil || !info.IsDir() {
		t.Fatalf("corpus dir not a directory: %q (%v)", corpusAbs, err)
	}

	queries, err := LoadQASet(cfg.QAPath)
	if err != nil {
		t.Fatalf("load qa set: %v", err)
	}
	if len(queries) == 0 {
		t.Fatalf("qa set is empty: %s", cfg.QAPath)
	}

	const dim = 64
	srv := newDeterministicEmbeddingServer(dim)
	t.Cleanup(srv.Close)

	indexDir := t.TempDir()

	ragCfg := config.Config{
		RootPath:           corpusAbs,
		RAGIndexPath:       indexDir,
		RAGEnabled:         true,
		RAGMaxResults:      50,
		IndexDirs:          []string{corpusAbs},
		ChunkSize:          2000,
		ChunkOverlap:       200,
		OllamaBaseURL:      srv.URL,
		EmbeddingDimension: dim,
		EmbeddingMode:      "local-only",
		AutoIndex:          false,
		FileWatcher:        false,
	}

	engine := rag.NewEngine(ragCfg, nil)
	if engine == nil {
		t.Fatalf("rag.NewEngine returned nil")
	}
	t.Cleanup(engine.Stop)

	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index documents: %v", err)
	}

	return &Harness{
		engine:  engine,
		queries: queries,
		k:       cfg.K,
	}
}

// RunAll executes every loaded QAQuery and returns both raw results and the
// aggregate metrics.
func (h *Harness) RunAll(ctx context.Context) ([]QAResult, *EvalMetrics, error) {
	results := make([]QAResult, 0, len(h.queries))
	for _, q := range h.queries {
		r, err := h.RunQuery(ctx, q)
		if err != nil {
			return nil, nil, fmt.Errorf("query %s: %w", q.ID, err)
		}
		results = append(results, r)
	}
	metrics := AggregateMetrics(h.queries, results, h.k)
	return results, metrics, nil
}

// RunQuery runs a single QAQuery through the engine and returns the matched
// QAResult including rank of the first expected doc ID.
func (h *Harness) RunQuery(ctx context.Context, q QAQuery) (QAResult, error) {
	resp, err := h.engine.Search(ctx, q.Question, h.k, q.SourceType, true)
	if err != nil {
		return QAResult{Query: q, FirstHit: -1}, err
	}

	topIDs := make([]string, 0, len(resp.Results))
	seen := map[string]struct{}{}
	for _, r := range resp.Results {
		if _, dup := seen[r.Path]; dup {
			continue
		}
		seen[r.Path] = struct{}{}
		topIDs = append(topIDs, r.Path)
		if len(topIDs) >= h.k {
			break
		}
	}

	firstHit := -1
	for rank, id := range topIDs {
		if containsString(q.ExpectedDocIDs, id) {
			firstHit = rank
			break
		}
	}
	return QAResult{
		Query:    q,
		TopK:     topIDs,
		Hit:      firstHit >= 0,
		FirstHit: firstHit,
	}, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- deterministic embedding server ---

// newDeterministicEmbeddingServer serves Ollama-compatible embedding endpoints
// where the returned vector is a stable function of the text content. This is
// NOT a good semantic encoder — it is a reproducible fixture so the evaluation
// does not depend on an external model. The RAG engine's keyword-based
// signals are expected to carry most of the retrieval quality here.
func newDeterministicEmbeddingServer(dim int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		payload, err := readJSONPayload(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text, _ := payload["prompt"].(string)
		vec := deterministicEmbedding(text, dim)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": vec,
		})
	})
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		payload, err := readJSONPayload(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var inputs []string
		switch v := payload["input"].(type) {
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					inputs = append(inputs, s)
				}
			}
		case string:
			inputs = append(inputs, v)
		}
		out := make([][]float64, len(inputs))
		for i, text := range inputs {
			out[i] = deterministicEmbedding(text, dim)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": out,
		})
	})
	return httptest.NewServer(mux)
}

func readJSONPayload(r io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// deterministicEmbedding produces a vector that is stable across runs and
// correlates with shared tokens between texts. The vector is built as a sum
// of per-token hash-derived unit vectors, then L2-normalized. Overlapping
// vocabularies produce higher cosine similarity, which is enough signal for
// the harness to sanity-check ranking; the hybrid search layer then adds
// keyword BM25-like scoring on top.
func deterministicEmbedding(text string, dim int) []float64 {
	vec := make([]float64, dim)
	tokens := tokenize(text)
	if len(tokens) == 0 {
		// avoid the all-zero embedding; seed with the text hash itself.
		seedVector(vec, []byte(text))
		return normalize(vec)
	}
	tmp := make([]float64, dim)
	for _, tok := range tokens {
		seedVector(tmp, []byte(tok))
		for i := range vec {
			vec[i] += tmp[i]
		}
	}
	return normalize(vec)
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= '0' && r <= '9':
			return false
		}
		return true
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) >= 3 {
			out = append(out, f)
		}
	}
	return out
}

func seedVector(vec []float64, seed []byte) {
	// Use sha256(seed) as a deterministic source. Expand the 32-byte digest
	// across the vector by repeatedly hashing the digest with a counter.
	counter := make([]byte, 8)
	for i := range vec {
		vec[i] = 0
	}
	written := 0
	for counter[0] = 0; written < len(vec); counter[0]++ {
		h := sha256.New()
		h.Write(seed)
		h.Write(counter)
		digest := h.Sum(nil)
		for j := 0; j+8 <= len(digest) && written < len(vec); j += 8 {
			u := binary.BigEndian.Uint64(digest[j : j+8])
			// Map to [-1, 1].
			vec[written] = (float64(u)/float64(math.MaxUint64))*2.0 - 1.0
			written++
		}
	}
}

func normalize(vec []float64) []float64 {
	var sum float64
	for _, v := range vec {
		sum += v * v
	}
	if sum == 0 {
		return vec
	}
	inv := 1.0 / math.Sqrt(sum)
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}
