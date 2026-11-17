//go:build eval

package eval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/rag/eval"
)

func TestHarnessCompareRankings(t *testing.T) {
	var documentEmbeddings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input  any    `json:"input"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Input == nil {
			req.Input = req.Prompt
		}
		var inputs []string
		switch input := req.Input.(type) {
		case string:
			inputs = []string{input}
		case []any:
			for _, text := range input {
				inputs = append(inputs, text.(string))
			}
		}
		vectors := make([][]float64, len(inputs))
		for i, input := range inputs {
			if input == "rollback ingress" {
				vectors[i] = []float64{1, 0}
				continue
			}
			documentEmbeddings.Add(1)
			score := 0.15
			if strings.Contains(input, "previous deployment revision") {
				score = 0.62
			}
			vectors[i] = []float64{score, math.Sqrt(1 - score*score)}
		}
		response := map[string]any{"embeddings": vectors}
		if r.URL.Path == "/api/embeddings" && len(vectors) > 0 {
			response = map[string]any{"embedding": vectors[0]}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	corpus := filepath.Join(root, "docs")
	if err := os.Mkdir(corpus, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"meaning.md": "# Meaning\n\nRestore the previous deployment revision.\n",
		"words.md":   "# Words\n\nrollback ingress rollback ingress\n",
	} {
		if err := os.WriteFile(filepath.Join(corpus, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	qa, err := json.Marshal([]eval.QAQuery{{ID: "comparison", Type: "simple", Question: "rollback ingress", ExpectedDocIDs: []string{"meaning.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	qaPath := filepath.Join(root, "qa.json")
	if err := os.WriteFile(qaPath, qa, 0o600); err != nil {
		t.Fatal(err)
	}
	h := eval.NewHarness(t, eval.HarnessConfig{CorpusDir: corpus, QAPath: qaPath, K: 1, MaxChunksPerDoc: 1, Embeddings: &config.EmbeddingsConfig{OllamaBaseURL: server.URL, Dimension: 2, Mode: "local-only", MaxRetries: 1}})
	indexed := documentEmbeddings.Load()
	comparison, err := h.CompareRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if comparison.HybridFusion != "weighted" || comparison.K != 1 || comparison.MaxChunksPerDoc != 1 {
		t.Fatalf("comparison settings = %+v", comparison)
	}
	if comparison.Hybrid.TotalQueries != 1 || comparison.Semantic.TotalQueries != 1 {
		t.Fatalf("query counts = %+v", comparison)
	}
	if comparison.Hybrid.HitRateAtK != 0 || comparison.Semantic.HitRateAtK != 1 {
		t.Fatalf("expected lexical distraction only in hybrid arm: %+v / %+v", comparison.Hybrid, comparison.Semantic)
	}
	if documentEmbeddings.Load() != indexed {
		t.Fatal("comparison rebuilt the index")
	}
	_, after, err := h.RunAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.HitRateAtK != comparison.Hybrid.HitRateAtK {
		t.Fatal("comparison changed the default harness mode")
	}
}

// This comparison has no quality threshold: a worse semantic score is still a
// valid measurement. TestRetrievalEval remains the hybrid regression gate.
func TestRetrievalEvalCompareRankings(t *testing.T) {
	for _, maxPerDoc := range []int{0, 1} {
		t.Run(fmt.Sprintf("max_chunks_per_doc=%d", maxPerDoc), func(t *testing.T) {
			h := eval.NewHarness(t, eval.HarnessConfig{CorpusDir: "testdata/corpus", QAPath: "testdata/qa.json", K: 5, MaxChunksPerDoc: maxPerDoc})
			comparison, err := h.CompareRankings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Hybrid.TotalQueries == 0 || comparison.Hybrid.TotalQueries != comparison.Semantic.TotalQueries {
				t.Fatalf("comparison query counts differ: %+v", comparison)
			}
			payload, err := json.Marshal(comparison)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("comparison: %s", payload)
		})
	}
}

func TestHarnessCompareRankingsPropagatesCancellation(t *testing.T) {
	h := eval.NewHarness(t, eval.HarnessConfig{CorpusDir: "testdata/corpus", QAPath: "testdata/qa.json", K: 5})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	comparison, err := h.CompareRankings(ctx)
	if err == nil || comparison != nil {
		t.Fatalf("cancelled comparison = %+v, %v", comparison, err)
	}
}
