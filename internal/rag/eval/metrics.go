package eval

// HitRateAtK returns the fraction of results whose FirstHit rank falls inside
// [0, k). Non-hit entries (FirstHit < 0) are counted as misses.
func HitRateAtK(results []QAResult, k int) float64 {
	if len(results) == 0 || k <= 0 {
		return 0
	}
	hits := 0
	for _, r := range results {
		if r.Hit && r.FirstHit >= 0 && r.FirstHit < k {
			hits++
		}
	}
	return float64(hits) / float64(len(results))
}

// MRR computes Mean Reciprocal Rank over all results. Misses contribute 0 and
// are included in the denominator (standard MRR convention).
func MRR(results []QAResult) float64 {
	if len(results) == 0 {
		return 0
	}
	total := 0.0
	for _, r := range results {
		if r.Hit && r.FirstHit >= 0 {
			total += 1.0 / float64(r.FirstHit+1)
		}
	}
	return total / float64(len(results))
}

// AggregateMetrics groups results by query type and computes per-type and
// overall metrics. Results are matched to queries by position in the input
// slices, and the Query field on each QAResult is used to determine type.
func AggregateMetrics(queries []QAQuery, results []QAResult, k int) *EvalMetrics {
	metrics := &EvalMetrics{
		TotalQueries: len(results),
		HitRateAtK:   HitRateAtK(results, k),
		MRR:          MRR(results),
		PerType:      map[string]TypeMetrics{},
	}
	if len(results) == 0 {
		return metrics
	}

	groups := map[string][]QAResult{}
	for _, r := range results {
		t := r.Query.Type
		if t == "" {
			t = "unknown"
		}
		groups[t] = append(groups[t], r)
	}
	for t, g := range groups {
		metrics.PerType[t] = TypeMetrics{
			Count:      len(g),
			HitRateAtK: HitRateAtK(g, k),
			MRR:        MRR(g),
		}
	}
	return metrics
}
