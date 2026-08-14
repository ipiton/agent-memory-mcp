//go:build eval

package eval_test

// T119: the same harness, pointed at the generated corpus and a real encoder.
// No baseline is committed for this run and no regression gate is attached —
// the corpus is local, private and rebuildable, so a threshold derived from it
// would be a number nobody else can reproduce. What this test produces is the
// measurement the backlog needs: how much room the pipeline actually has.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/rag/eval"
)

func TestRetrievalEvalGenerated(t *testing.T) {
	outDir := os.Getenv(envOutDir)
	if outDir == "" {
		t.Skipf("set %s (see `make eval-corpus`) to run against the generated corpus", envOutDir)
	}
	qaPath := filepath.Join(outDir, "qa.json")
	if _, err := os.Stat(qaPath); err != nil {
		t.Fatalf("no QA set at %s — run `make eval-corpus` first (%v)", qaPath, err)
	}

	cfg := eval.HarnessConfig{
		CorpusDir: filepath.Join(outDir, "corpus"),
		QAPath:    qaPath,
		K:         5,
	}
	if emb := liveEmbeddings(); emb != nil {
		cfg.Embeddings = emb
		t.Logf("encoder: llama.cpp %s model=%s dim=%d", emb.LlamaCPPBaseURL, emb.LlamaCPPModel, emb.Dimension)
	} else {
		t.Logf("encoder: deterministic fixture — semantic scores are noise, keyword signals carry the result")
	}

	h := eval.NewHarness(t, cfg)
	results, metrics, err := h.RunAll(context.Background())
	if err != nil {
		t.Fatalf("run all: %v", err)
	}

	t.Logf("HitRateAtK@%d=%.4f MRR=%.4f (N=%d)", cfg.K, metrics.HitRateAtK, metrics.MRR, metrics.TotalQueries)
	misses := 0
	for _, r := range results {
		if !r.Hit {
			misses++
			if misses <= 10 {
				t.Logf("  MISS id=%s q=%.80q top=%v", r.Query.ID, r.Query.Question, r.TopK)
			}
		}
	}
	t.Logf("misses: %d of %d (%.1f%% headroom on Hit@%d)",
		misses, metrics.TotalQueries, 100*float64(misses)/float64(metrics.TotalQueries), cfg.K)

	if metrics.TotalQueries == 0 {
		t.Fatal("empty QA set")
	}
}

// liveEmbeddings returns a real encoder config when LLAMACPP_BASE_URL is set,
// and nil otherwise. Deliberately env-driven rather than hardcoded: the
// endpoint, the model and the dimension are properties of the machine the
// measurement runs on, and pinning them here would make a local accident look
// like a project constant.
func liveEmbeddings() *config.EmbeddingsConfig {
	baseURL := os.Getenv("LLAMACPP_BASE_URL")
	if baseURL == "" {
		return nil
	}
	dim := 768
	if v := os.Getenv("MCP_EMBEDDING_DIMENSION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dim = n
		}
	}
	model := os.Getenv("LLAMACPP_EMBEDDING_MODEL")
	if model == "" {
		model = "embeddinggemma-300m"
	}
	return &config.EmbeddingsConfig{
		LlamaCPPBaseURL: baseURL,
		LlamaCPPModel:   model,
		Dimension:       dim,
		Mode:            "local-only",
		Timeout:         30 * time.Second,
		MaxRetries:      2,
	}
}
