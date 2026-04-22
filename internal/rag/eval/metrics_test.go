package eval

import (
	"math"
	"testing"
)

func floatEquals(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestHitRateAtK_AllHit(t *testing.T) {
	results := []QAResult{
		{Hit: true, FirstHit: 0},
		{Hit: true, FirstHit: 2},
		{Hit: true, FirstHit: 4},
	}
	got := HitRateAtK(results, 5)
	if !floatEquals(got, 1.0, 1e-9) {
		t.Fatalf("HitRateAtK all-hit = %f, want 1.0", got)
	}
}

func TestHitRateAtK_NoHit(t *testing.T) {
	results := []QAResult{
		{Hit: false, FirstHit: -1},
		{Hit: false, FirstHit: -1},
	}
	got := HitRateAtK(results, 5)
	if !floatEquals(got, 0.0, 1e-9) {
		t.Fatalf("HitRateAtK no-hit = %f, want 0.0", got)
	}
}

func TestHitRateAtK_PartialHit(t *testing.T) {
	results := []QAResult{
		{Hit: true, FirstHit: 0},
		{Hit: false, FirstHit: -1},
		{Hit: true, FirstHit: 3},
		{Hit: false, FirstHit: -1},
	}
	got := HitRateAtK(results, 5)
	if !floatEquals(got, 0.5, 1e-9) {
		t.Fatalf("HitRateAtK partial = %f, want 0.5", got)
	}
}

func TestHitRateAtK_RankOutsideK(t *testing.T) {
	// FirstHit at position 5 (0-indexed) should not count for k=5 (positions 0..4).
	results := []QAResult{
		{Hit: true, FirstHit: 5},
		{Hit: true, FirstHit: 4},
	}
	got := HitRateAtK(results, 5)
	if !floatEquals(got, 0.5, 1e-9) {
		t.Fatalf("HitRateAtK rank-outside-k = %f, want 0.5", got)
	}
}

func TestHitRateAtK_EmptyResults(t *testing.T) {
	got := HitRateAtK(nil, 5)
	if !floatEquals(got, 0.0, 1e-9) {
		t.Fatalf("HitRateAtK empty = %f, want 0.0", got)
	}
}

func TestMRR_AllRank1(t *testing.T) {
	results := []QAResult{
		{Hit: true, FirstHit: 0},
		{Hit: true, FirstHit: 0},
	}
	got := MRR(results)
	if !floatEquals(got, 1.0, 1e-9) {
		t.Fatalf("MRR all-rank-1 = %f, want 1.0", got)
	}
}

func TestMRR_MixedRanks(t *testing.T) {
	// 1/(0+1) + 1/(1+1) + 1/(3+1) = 1 + 0.5 + 0.25 = 1.75 / 3 = 0.58333...
	results := []QAResult{
		{Hit: true, FirstHit: 0},
		{Hit: true, FirstHit: 1},
		{Hit: true, FirstHit: 3},
	}
	got := MRR(results)
	want := (1.0 + 0.5 + 0.25) / 3.0
	if !floatEquals(got, want, 1e-9) {
		t.Fatalf("MRR mixed = %f, want %f", got, want)
	}
}

func TestMRR_NoHitsContribute(t *testing.T) {
	// No-hit (FirstHit=-1) contributes 0 but is still in the denominator.
	results := []QAResult{
		{Hit: true, FirstHit: 0},
		{Hit: false, FirstHit: -1},
	}
	got := MRR(results)
	want := 1.0 / 2.0
	if !floatEquals(got, want, 1e-9) {
		t.Fatalf("MRR with no-hit = %f, want %f", got, want)
	}
}

func TestMRR_EmptyResults(t *testing.T) {
	got := MRR(nil)
	if !floatEquals(got, 0.0, 1e-9) {
		t.Fatalf("MRR empty = %f, want 0.0", got)
	}
}

func TestMRR_AllNoHit(t *testing.T) {
	results := []QAResult{
		{Hit: false, FirstHit: -1},
		{Hit: false, FirstHit: -1},
	}
	got := MRR(results)
	if !floatEquals(got, 0.0, 1e-9) {
		t.Fatalf("MRR all-no-hit = %f, want 0.0", got)
	}
}

func TestAggregateMetrics_PerType(t *testing.T) {
	queries := []QAQuery{
		{ID: "s1", Type: "simple"},
		{ID: "s2", Type: "simple"},
		{ID: "m1", Type: "multi-hop"},
	}
	results := []QAResult{
		{Query: queries[0], Hit: true, FirstHit: 0},
		{Query: queries[1], Hit: false, FirstHit: -1},
		{Query: queries[2], Hit: true, FirstHit: 1},
	}
	m := AggregateMetrics(queries, results, 5)
	if m.TotalQueries != 3 {
		t.Fatalf("TotalQueries = %d, want 3", m.TotalQueries)
	}
	// Overall HitRate = 2/3
	if !floatEquals(m.HitRateAtK, 2.0/3.0, 1e-9) {
		t.Fatalf("HitRateAtK = %f, want %f", m.HitRateAtK, 2.0/3.0)
	}
	// Overall MRR = (1 + 0 + 0.5)/3 = 0.5
	if !floatEquals(m.MRR, 0.5, 1e-9) {
		t.Fatalf("MRR = %f, want 0.5", m.MRR)
	}

	simple, ok := m.PerType["simple"]
	if !ok {
		t.Fatal("PerType missing 'simple'")
	}
	if simple.Count != 2 {
		t.Fatalf("simple.Count = %d, want 2", simple.Count)
	}
	if !floatEquals(simple.HitRateAtK, 0.5, 1e-9) {
		t.Fatalf("simple.HitRateAtK = %f, want 0.5", simple.HitRateAtK)
	}
	if !floatEquals(simple.MRR, 0.5, 1e-9) {
		t.Fatalf("simple.MRR = %f, want 0.5", simple.MRR)
	}

	multi, ok := m.PerType["multi-hop"]
	if !ok {
		t.Fatal("PerType missing 'multi-hop'")
	}
	if multi.Count != 1 {
		t.Fatalf("multi.Count = %d, want 1", multi.Count)
	}
	if !floatEquals(multi.HitRateAtK, 1.0, 1e-9) {
		t.Fatalf("multi.HitRateAtK = %f, want 1.0", multi.HitRateAtK)
	}
	if !floatEquals(multi.MRR, 0.5, 1e-9) {
		t.Fatalf("multi.MRR = %f, want 0.5", multi.MRR)
	}
}

func TestAggregateMetrics_Empty(t *testing.T) {
	m := AggregateMetrics(nil, nil, 5)
	if m.TotalQueries != 0 {
		t.Fatalf("TotalQueries = %d, want 0", m.TotalQueries)
	}
	if !floatEquals(m.HitRateAtK, 0.0, 1e-9) {
		t.Fatalf("HitRateAtK = %f, want 0.0", m.HitRateAtK)
	}
	if !floatEquals(m.MRR, 0.0, 1e-9) {
		t.Fatalf("MRR = %f, want 0.0", m.MRR)
	}
	if len(m.PerType) != 0 {
		t.Fatalf("PerType size = %d, want 0", len(m.PerType))
	}
}
