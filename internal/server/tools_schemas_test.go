package server

import "testing"

// TestToolDefsCountStable guards the Round 3 H16 decomposition of the tool list
// into per-category builders: the total definition count and uniqueness of tool
// names must not drift as builders are edited. 41 static tools + 9 steward = 50.
func TestToolDefsCountStable(t *testing.T) {
	all := append(mainToolDefs(), stewardToolDefs()...)
	const want = 50
	if len(all) != want {
		t.Fatalf("total tool defs = %d, want %d", len(all), want)
	}
	seen := make(map[string]bool, len(all))
	for _, d := range all {
		if d.Name == "" {
			t.Error("tool definition with empty Name")
		}
		if seen[d.Name] {
			t.Errorf("duplicate tool name %q", d.Name)
		}
		seen[d.Name] = true
	}
	if len(mainToolDefs()) != 41 {
		t.Fatalf("main (non-steward) tool defs = %d, want 41", len(mainToolDefs()))
	}
}
