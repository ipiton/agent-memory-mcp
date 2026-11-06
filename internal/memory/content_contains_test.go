package memory

import (
	"strings"
	"testing"
)

// The steward calls a merge safe when ContentContains holds for every
// duplicate; that is only true if mergeContent then leaves the primary's text
// exactly as it was.
func TestContentContainsMeansMergeAddsNothing(t *testing.T) {
	primary := "  User: start.\n\nAssistant: Checked the indexer.\n\nAssistant: re-ran the gates.  "
	cases := []struct {
		dup      string
		contains bool
	}{
		{"Assistant: Checked the indexer.", true},
		{"assistant: checked the INDEXER.", true}, // case is ignored, as in the merge
		{"   ", true}, // empty adds nothing
		{"Assistant: Checked the indexer twice.", false},
	}
	for _, c := range cases {
		if got := ContentContains(primary, c.dup); got != c.contains {
			t.Errorf("ContentContains(%q) = %v, want %v", c.dup, got, c.contains)
			continue
		}
		merged := mergeContent(primary, []*Memory{{Title: "dup", Content: c.dup}})
		unchanged := merged == strings.TrimSpace(primary)
		if unchanged != c.contains {
			t.Errorf("dup %q: merge unchanged = %v, but ContentContains = %v", c.dup, unchanged, c.contains)
		}
	}
}
