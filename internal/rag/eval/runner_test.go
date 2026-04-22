//go:build eval

package eval_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/rag/eval"
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

