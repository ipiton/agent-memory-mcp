//go:build eval

package eval_test

// T119: the same harness, pointed at the generated corpus and a real encoder.
// No baseline is committed for this run and no regression gate is attached —
// the corpus is local, private and rebuildable, so a threshold derived from it
// would be a number nobody else can reproduce. What this test produces is the
// measurement the backlog needs: how much room the pipeline actually has.

import (
	"context"
	"encoding/json"
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

	// T102: the permuted-headings arm lives in a sibling directory, so the two
	// runs differ in exactly one thing and share the encoder, the QA set and the
	// pipeline.
	corpusName := os.Getenv(envCorpusName)
	if corpusName == "" {
		corpusName = "corpus"
	}
	// T124: the fusion arm under test. Env-driven for the same reason the
	// encoder is — which arm ran is a property of the run, and the three of
	// them have to differ in exactly this one thing.
	fusion := config.NormalizeFusion(os.Getenv("MCP_RAG_FUSION"))
	rrfK := 60
	if raw := os.Getenv("MCP_RAG_RRF_K"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			rrfK = parsed
		}
	}
	cfg := eval.HarnessConfig{
		CorpusDir: filepath.Join(outDir, corpusName),
		QAPath:    qaPath,
		K:         5,
		Fusion:    fusion,
		RRFK:      rrfK,
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

	t.Logf("corpus=%s fusion=%s k=%d HitRateAtK@%d=%.4f MRR=%.4f (N=%d)", corpusName, fusion, rrfK, cfg.K, metrics.HitRateAtK, metrics.MRR, metrics.TotalQueries)
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

	// T124: aggregate metrics can coincide while the orderings differ, and
	// "the arms scored the same" is a different claim from "the arms returned
	// the same thing". Dumping the ranking makes the second one checkable.
	if dump := os.Getenv("MCP_EVAL_DUMP"); dump != "" {
		rankings := make(map[string][]string, len(results))
		for _, r := range results {
			rankings[r.Query.ID] = r.TopK
		}
		payload, err := json.MarshalIndent(rankings, "", "  ")
		if err != nil {
			t.Fatalf("marshal rankings: %v", err)
		}
		if err := os.WriteFile(dump, payload, 0o644); err != nil {
			t.Fatalf("write rankings: %v", err)
		}
		t.Logf("rankings written to %s", dump)
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
