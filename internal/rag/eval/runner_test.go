//go:build eval

package eval_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/rag/eval"
	"github.com/ipiton/agent-memory-mcp/internal/reranker"
)

var updateBaseline = flag.Bool("update-baseline", false, "Write current metrics to baseline.json")

// regressionTolerance is the maximum allowed drop from baseline for either
// HitRateAtK or MRR before the test fails.
const regressionTolerance = 0.05

func TestRetrievalEval(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cfg := eval.HarnessConfig{
		CorpusDir:    filepath.Join(wd, "testdata", "corpus"),
		QAPath:       filepath.Join(wd, "testdata", "qa.json"),
		BaselinePath: filepath.Join(wd, "testdata", "baseline.json"),
		K:            5,
	}

	h := eval.NewHarness(t, cfg)
	results, metrics, err := h.RunAll(context.Background())
	if err != nil {
		t.Fatalf("run all: %v", err)
	}

	// Always log the raw metrics and any misses to help diagnose regressions.
	t.Logf("HitRateAtK@%d=%.4f MRR=%.4f (N=%d)", cfg.K, metrics.HitRateAtK, metrics.MRR, metrics.TotalQueries)
	for t_, tm := range metrics.PerType {
		t.Logf("  type=%s count=%d hitrate=%.4f mrr=%.4f", t_, tm.Count, tm.HitRateAtK, tm.MRR)
	}
	for _, r := range results {
		if !r.Hit {
			t.Logf("  MISS id=%s type=%s q=%q top=%v expected=%v", r.Query.ID, r.Query.Type, r.Query.Question, r.TopK, r.Query.ExpectedDocIDs)
		}
	}

	if *updateBaseline {
		if err := eval.WriteBaseline(cfg.BaselinePath, metrics); err != nil {
			t.Fatalf("write baseline: %v", err)
		}
		t.Logf("baseline updated at %s", cfg.BaselinePath)
		return
	}

	baseline, err := eval.LoadBaseline(cfg.BaselinePath)
	if err != nil {
		t.Fatalf("load baseline: %v (hint: first run with -args -update-baseline)", err)
	}

	if metrics.HitRateAtK+regressionTolerance < baseline.HitRateAtK {
		t.Errorf("HitRateAtK regression: got %.4f, baseline %.4f, tolerance %.2f",
			metrics.HitRateAtK, baseline.HitRateAtK, regressionTolerance)
	}
	if metrics.MRR+regressionTolerance < baseline.MRR {
		t.Errorf("MRR regression: got %.4f, baseline %.4f, tolerance %.2f",
			metrics.MRR, baseline.MRR, regressionTolerance)
	}
}

// oracleReranker knows the expected-doc-path set for each question-hash and
// returns scores that strictly prefer those paths. It is used to establish
// the upper-bound MRR delta for T44 — if even an oracle reranker doesn't
// improve MRR, the rerank wiring itself is broken.
type oracleReranker struct {
	// expectedByQuery[question] = set of expected doc paths. The rerank
	// Candidate.ID corresponds to SearchResult.ID (chunk ID) in production,
	// but the harness already de-duplicates by Path before computing
	// metrics; to keep the oracle grounded on what's actually measured, we
	// match by ID-prefix against expected paths using the Content field
	// (which the test engine wires to the chunk body) too.
	expectedByQuery map[string]map[string]struct{}
	// lastQuery is captured per-Rerank call so we can look up the expected
	// set from the query text.
	lastQuery string
}

func newOracleReranker(qa []eval.QAQuery) *oracleReranker {
	m := make(map[string]map[string]struct{}, len(qa))
	for _, q := range qa {
		set := make(map[string]struct{}, len(q.ExpectedDocIDs))
		for _, id := range q.ExpectedDocIDs {
			set[id] = struct{}{}
		}
		m[q.Question] = set
	}
	return &oracleReranker{expectedByQuery: m}
}

func (o *oracleReranker) Rerank(ctx context.Context, query string, candidates []reranker.Candidate) ([]reranker.Scored, error) {
	expected := o.expectedByQuery[query]
	out := make([]reranker.Scored, len(candidates))
	for i, c := range candidates {
		score := 0.0
		// Chunk IDs look like "<path>::<index>"; match by path prefix so we
		// promote every chunk that belongs to an expected document.
		for exp := range expected {
			if strings.HasPrefix(c.ID, exp) {
				score = 1.0
				break
			}
		}
		out[i] = reranker.Scored{ID: c.ID, Score: score}
	}
	return out, nil
}

// reversingReranker assigns decreasing scores in input order so the last
// hybrid candidate ends up first. This is the worst-case for retrieval
// quality and should strictly degrade MRR.
type reversingReranker struct{}

func (reversingReranker) Rerank(ctx context.Context, query string, candidates []reranker.Candidate) ([]reranker.Scored, error) {
	out := make([]reranker.Scored, len(candidates))
	n := len(candidates)
	for i, c := range candidates {
		// score: first gets 1.0/n, last gets 1.0
		out[i] = reranker.Scored{ID: c.ID, Score: float64(n-i) / float64(n+1)}
	}
	// But we want reverse: so last-input → highest. Flip.
	for i := range out {
		out[i].Score = 1.0 - out[i].Score
	}
	return out, nil
}

// TestRetrievalEval_WithRerankMock measures the MRR delta from injecting
// trivial best-case and worst-case rerankers, verifying that the rerank
// wiring is observable end-to-end on the eval corpus.
func TestRetrievalEval_WithRerankMock(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	baseCfg := eval.HarnessConfig{
		CorpusDir:    filepath.Join(wd, "testdata", "corpus"),
		QAPath:       filepath.Join(wd, "testdata", "qa.json"),
		BaselinePath: filepath.Join(wd, "testdata", "baseline.json"),
		K:            5,
	}

	qaSet, err := eval.LoadQASet(baseCfg.QAPath)
	if err != nil {
		t.Fatalf("LoadQASet: %v", err)
	}

	// Baseline — no reranker.
	hNone := eval.NewHarness(t, baseCfg)
	_, mNone, err := hNone.RunAll(context.Background())
	if err != nil {
		t.Fatalf("baseline run: %v", err)
	}

	// Oracle — strictly preferring expected docs.
	bestCfg := baseCfg
	bestCfg.Reranker = newOracleReranker(qaSet)
	hBest := eval.NewHarness(t, bestCfg)
	_, mBest, err := hBest.RunAll(context.Background())
	if err != nil {
		t.Fatalf("oracle run: %v", err)
	}

	// Reversing — worst-case reorder.
	worstCfg := baseCfg
	worstCfg.Reranker = reversingReranker{}
	hWorst := eval.NewHarness(t, worstCfg)
	_, mWorst, err := hWorst.RunAll(context.Background())
	if err != nil {
		t.Fatalf("reversing run: %v", err)
	}

	t.Logf("MRR: none=%.4f oracle=%.4f reversing=%.4f (N=%d)",
		mNone.MRR, mBest.MRR, mWorst.MRR, mNone.TotalQueries)
	t.Logf("HitRateAtK@%d: none=%.4f oracle=%.4f reversing=%.4f",
		baseCfg.K, mNone.HitRateAtK, mBest.HitRateAtK, mWorst.HitRateAtK)
	t.Logf("delta_oracle_mrr = %+.4f", mBest.MRR-mNone.MRR)
	t.Logf("delta_reversing_mrr = %+.4f", mWorst.MRR-mNone.MRR)

	if mBest.MRR < mNone.MRR {
		t.Errorf("oracle reranker should strictly improve MRR: oracle=%.4f < none=%.4f", mBest.MRR, mNone.MRR)
	}
	if mWorst.MRR >= mNone.MRR {
		t.Errorf("reversing reranker should strictly degrade MRR: reversing=%.4f >= none=%.4f", mWorst.MRR, mNone.MRR)
	}
}
