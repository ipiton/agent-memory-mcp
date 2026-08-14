package memory

import (
	"math"
	"math/rand"
	"testing"
)

func randomVectors(t *testing.T, n, dim int) [][]float32 {
	t.Helper()
	rng := rand.New(rand.NewSource(11))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		out[i] = v
	}
	return out
}

// The expanded form in similarity() is an optimisation — it must agree with
// the definition it stands for, or the whole measurement behind T76a is a
// measurement of a typo.
func TestCenteredSimilarityMatchesExplicitCentering(t *testing.T) {
	const dim = 32
	vectors := randomVectors(t, minCenterVectors+20, dim)
	center := newEmbeddingCenter(vectors)
	if center == nil {
		t.Fatal("newEmbeddingCenter returned nil for a large enough corpus")
	}

	explicit := func(a, b []float32) float64 {
		ca := make([]float32, dim)
		cb := make([]float32, dim)
		for i := range a {
			ca[i] = a[i] - center.mean[i]
			cb[i] = b[i] - center.mean[i]
		}
		var dot, na, nb float64
		for i := range ca {
			dot += float64(ca[i]) * float64(cb[i])
			na += float64(ca[i]) * float64(ca[i])
			nb += float64(cb[i]) * float64(cb[i])
		}
		return dot / (math.Sqrt(na) * math.Sqrt(nb))
	}

	q := vectors[0]
	qDotMean, qCenteredNorm, ok := center.queryStats(q)
	if !ok {
		t.Fatal("queryStats reported the query as unusable")
	}
	for _, a := range vectors[1:20] {
		got := center.similarity(q, a, qDotMean, qCenteredNorm)
		want := explicit(q, a)
		if math.Abs(got-want) > 1e-4 {
			t.Fatalf("similarity = %.6f, explicit centering = %.6f", got, want)
		}
	}
}

// A mean over a handful of vectors is dominated by the vectors it is meant to
// cancel, so below the threshold there is no center at all and Recall keeps
// raw cosine. This is what keeps a fresh install — and every store in the unit
// tests — on the pre-T76a scoring.
func TestEmbeddingCenterRequiresEnoughVectors(t *testing.T) {
	if c := newEmbeddingCenter(randomVectors(t, minCenterVectors-1, 8)); c != nil {
		t.Errorf("center built from %d vectors, want nil below %d", minCenterVectors-1, minCenterVectors)
	}
	if c := newEmbeddingCenter(randomVectors(t, minCenterVectors, 8)); c == nil {
		t.Errorf("no center built from exactly %d vectors", minCenterVectors)
	}
	if c := newEmbeddingCenter(nil); c != nil {
		t.Error("center built from no vectors at all")
	}
}

// A bank mid-re-embed holds two dimensions at once. Averaging across them
// would produce a vector belonging to neither space, and comparing against it
// would be worse than not centering.
func TestEmbeddingCenterIgnoresForeignDimensions(t *testing.T) {
	vectors := randomVectors(t, minCenterVectors+5, 16)
	vectors = append(vectors, randomVectors(t, 30, 8)...)

	center := newEmbeddingCenter(vectors)
	if center == nil {
		t.Fatal("center is nil despite enough same-dimension vectors")
	}
	if len(center.mean) != 16 {
		t.Fatalf("mean dimension = %d, want 16", len(center.mean))
	}

	if _, _, ok := center.queryStats(make([]float32, 8)); ok {
		t.Error("queryStats accepted a query from the other embedding space")
	}
}

// Below the threshold there is no center, so Recall must not silently score
// against nil — the nil receiver is part of the contract, not an accident.
func TestNilCenterIsInert(t *testing.T) {
	var center *embeddingCenter
	if _, _, ok := center.queryStats([]float32{1, 2, 3}); ok {
		t.Error("nil center reported usable query stats")
	}
	if got := center.similarity([]float32{1}, []float32{1}, 0, 1); got != 0 {
		t.Errorf("nil center scored %.4f, want 0", got)
	}
}
