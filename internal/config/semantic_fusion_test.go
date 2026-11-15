package config

import "testing"

func TestNormalizeSemanticFusion(t *testing.T) {
	for _, value := range []string{"semantic", " Semantic "} {
		if got := NormalizeFusion(value); got != "semantic" {
			t.Errorf("NormalizeFusion(%q) = %q, want semantic", value, got)
		}
	}
}
