package scoring

import "testing"

func TestIsPitfallQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want bool
	}{
		// Positive: canonical pitfall-signaling queries.
		{"how to phrase", "how to migrate X", true},
		{"lesson keyword", "lesson from Y", true},
		{"pitfall keyword", "pitfall of Z", true},
		{"why not phrase", "why not use W", true},
		{"approach plus try", "what approach should I try?", true},
		{"avoid keyword", "avoid this", true},
		{"failed keyword", "things that failed", true},
		{"try standalone", "should we try this plan", true},

		// Negative: must NOT match — these previously triggered false positives
		// with naive substring search.
		{"retry substring of try", "retry storm diagnosis", false},
		{"country substring of try", "country code validator", false},
		{"entry substring of try", "entry point config", false},
		{"unavoidable substring of avoid", "unavoidable dependency", false},
		{"devoid substring of avoid", "devoid of tests", false},
		{"poultry substring of try", "poultry counting service", false},
		{"how without how-to", "how much memory does X use", false},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsPitfallQuery(c.q)
			if got != c.want {
				t.Fatalf("IsPitfallQuery(%q) = %v, want %v", c.q, got, c.want)
			}
		})
	}
}
