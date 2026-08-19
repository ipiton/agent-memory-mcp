package rag

import "testing"

// T124. The weighted blend adds a raw cosine to a keyword score normalised
// against the current result set; RRF adds neither, only 1/(k+rank). This
// pins the arithmetic, because the arms are chosen by measurement and the
// measurement is worthless if the modes do not compute what they claim.
func TestFusionScoreByMode(t *testing.T) {
	candidate := hybridCandidate{
		semanticScore: 0.8,
		semanticRank:  1,
		keywordScore:  4,
		keywordRank:   3,
		recencyScore:  0.06,
		sourceBoost:   0.08,
	}
	const keywordComponent, confidenceComponent = 0.5, 0.02

	cases := []struct {
		mode string
		want float64
	}{
		{"weighted", 0.8*0.60 + 0.5*0.30 + 0.06 + 0.08 + 0.02},
		{"rrf", 1.0/61 + 1.0/63},
		{"rrf-boosted", 1.0/61 + 1.0/63 + (0.06+0.08+0.02)/61},
	}
	for _, tc := range cases {
		got := newFusionSettings(tc.mode, 60).score(candidate, keywordComponent, confidenceComponent)
		if diff := got - tc.want; diff > 1e-12 || diff < -1e-12 {
			t.Errorf("%s: score = %.12f, want %.12f", tc.mode, got, tc.want)
		}
	}
}

// An arm that did not return the candidate contributes nothing — the absent
// rank must not be read as rank 0, which would be the best position there is.
func TestFusionRRFIgnoresAbsentArm(t *testing.T) {
	semanticOnly := hybridCandidate{semanticScore: 0.7, semanticRank: 2}
	f := newFusionSettings("rrf", 60)

	got := f.score(semanticOnly, 0, 0)
	if want := 1.0 / 62; got != want {
		t.Fatalf("score = %.12f, want %.12f", got, want)
	}
	both := semanticOnly
	both.keywordRank = 50
	if f.score(both, 0, 0) <= got {
		t.Error("a candidate found by both arms must outrank the same candidate found by one")
	}
}

// The whole point of RRF here is that scale differences between the arms stop
// mattering: a candidate ranked first by both arms wins over one ranked lower,
// whatever the raw scores say.
func TestFusionRRFRanksByPositionNotScale(t *testing.T) {
	f := newFusionSettings("rrf", 60)
	topOfBoth := hybridCandidate{semanticScore: 0.51, semanticRank: 1, keywordScore: 0.1, keywordRank: 1}
	highCosine := hybridCandidate{semanticScore: 0.99, semanticRank: 4, keywordScore: 99, keywordRank: 9}

	if f.score(topOfBoth, 0, 0) <= f.score(highCosine, 0, 0) {
		t.Error("rrf ranked the higher raw scores above the better ranks")
	}
	weighted := newFusionSettings("weighted", 60)
	if weighted.score(topOfBoth, 0.001, 0) < weighted.score(highCosine, 1, 0) {
		return // the blend puts the raw scores first, which is the difference under test
	}
	t.Error("setup no longer distinguishes the two modes")
}

// An unset or unknown mode must behave exactly like the shipped default,
// so a typo in the env var cannot silently change how a deployment ranks.
func TestFusionUnknownModeFallsBackToWeighted(t *testing.T) {
	candidate := hybridCandidate{semanticScore: 0.4, semanticRank: 1, keywordRank: 1}
	want := newFusionSettings("weighted", 60).score(candidate, 0.2, 0)
	for _, mode := range []string{"", "RRF ", "nonsense"} {
		if got := newFusionSettings(mode, 60).score(candidate, 0.2, 0); got != want {
			t.Errorf("mode %q: score = %v, want the weighted %v", mode, got, want)
		}
	}
}

// k <= 0 would divide by the rank alone and make rank 1 worth 1.0 — far past
// any boost. Guard it at the constructor, since it arrives from an env var.
func TestFusionRejectsNonPositiveK(t *testing.T) {
	if got := newFusionSettings("rrf", 0).k; got != 60 {
		t.Errorf("k = %d, want the canonical 60", got)
	}
	if got := newFusionSettings("rrf", -5).k; got != 60 {
		t.Errorf("k = %d, want the canonical 60", got)
	}
}
