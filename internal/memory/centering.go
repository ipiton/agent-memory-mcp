package memory

import "math"

// T76(a): mean-centering of the embedding space.
//
// Raw cosine on this corpus is inflated: over 1995 random pairs the median
// similarity is 0.555, and pairs from the same task sit at 0.786 — a
// separation of 0.231 on a scale that never comes near zero. Subtracting the
// corpus mean before comparing puts unrelated pairs where they belong (median
// −0.033) and widens the separation to 0.527.
//
// The practical consequence is not only ranking. Recall drops candidates below
// minScore = 0.05; against a distribution whose floor is 0.55 that gate cannot
// reject anything, which is the second half of why a limitless sweep marks the
// whole bank (T113).
//
// Cost: the centered score needs three dot products per candidate instead of
// one cosine's three accumulations — the same order, roughly double the
// arithmetic, and no extra memory. A precomputed per-record scalar would save
// a third of that and would have to be invalidated on every write; not worth
// it at this corpus size.
type embeddingCenter struct {
	mean    []float32
	selfDot float64 // mean·mean
}

// SetRecallCentered toggles mean-centered scoring for Recall's semantic leg.
// Set once at startup from config (MCP_RECALL_CENTERED); retrieval reads the
// atomic without a lock.
func (ms *Store) SetRecallCentered(centered bool) {
	ms.recallCentered.Store(centered)
}

// minCenterVectors is the corpus size below which centering is not applied.
// The mean has to represent the bulk of the space in order to cancel out of a
// comparison; with a handful of vectors it is dominated by the very vectors it
// is subtracted from, and the result is noise rather than a sharper signal. A
// fresh install and every unit-test store live below this line and keep raw
// cosine, which is also why the switch does not perturb existing tests.
const minCenterVectors = 100

// newEmbeddingCenter computes the mean of the given vectors, ignoring any
// whose dimension differs from the first usable one (a mixed-model bank has
// both, and averaging across models would produce a vector belonging to
// neither space). Returns nil when there is nothing to average or when the
// corpus is too small to have a meaningful mean.
func newEmbeddingCenter(vectors [][]float32) *embeddingCenter {
	dim := 0
	for _, v := range vectors {
		if len(v) > 0 {
			dim = len(v)
			break
		}
	}
	if dim == 0 {
		return nil
	}

	sum := make([]float64, dim)
	count := 0
	for _, v := range vectors {
		if len(v) != dim {
			continue
		}
		for i, x := range v {
			sum[i] += float64(x)
		}
		count++
	}
	if count < minCenterVectors {
		return nil
	}

	mean := make([]float32, dim)
	var selfDot float64
	for i := range sum {
		m := sum[i] / float64(count)
		mean[i] = float32(m)
		selfDot += m * m
	}
	return &embeddingCenter{mean: mean, selfDot: selfDot}
}

// queryStats precomputes the two query-side scalars that are constant across
// candidates: q·mean and |q − mean|. ok is false when the query does not live
// in the same space as the mean, in which case the caller must fall back to
// the raw cosine rather than compare vectors of different dimensions.
func (c *embeddingCenter) queryStats(q []float32) (qDotMean, qCenteredNorm float64, ok bool) {
	if c == nil || len(q) != len(c.mean) {
		return 0, 0, false
	}
	var qq float64
	for i, x := range q {
		qDotMean += float64(x) * float64(c.mean[i])
		qq += float64(x) * float64(x)
	}
	norm := qq - 2*qDotMean + c.selfDot
	if norm <= 0 {
		return 0, 0, false
	}
	return qDotMean, math.Sqrt(norm), true
}

// similarity returns cos(q − mean, a − mean), expanded so neither vector has
// to be materialised in centered form.
func (c *embeddingCenter) similarity(q, a []float32, qDotMean, qCenteredNorm float64) float64 {
	if c == nil || len(a) != len(c.mean) || len(q) != len(a) || qCenteredNorm == 0 {
		return 0
	}
	var qa, aMean, aa float64
	for i, x := range a {
		xf := float64(x)
		qa += float64(q[i]) * xf
		aMean += xf * float64(c.mean[i])
		aa += xf * xf
	}
	aNormSq := aa - 2*aMean + c.selfDot
	if aNormSq <= 0 {
		return 0
	}
	return (qa - qDotMean - aMean + c.selfDot) / (qCenteredNorm * math.Sqrt(aNormSq))
}
