package review

import "testing"

func TestNormalizeResolution(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"dismissed", "dismissed", false},
		{"resolved", "resolved", false},
		{"deferred", "deferred", false},
		{"", "resolved", false},
		{"  Resolved  ", "resolved", false},
		{"invalid", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeResolution(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("NormalizeResolution(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeResolution(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolvedTagsRemovesPendingMarkers(t *testing.T) {
	got := ResolvedTags([]string{"review:required", "status:review_required", "service:api", "review-queue"}, "resolved")

	has := map[string]bool{}
	for _, tag := range got {
		has[tag] = true
	}
	if has["review:required"] || has["status:review_required"] {
		t.Fatalf("pending markers should be removed: %#v", got)
	}
	if !has["review:resolved"] || !has["status:resolved"] {
		t.Fatalf("resolved markers missing: %#v", got)
	}
	if !has["service:api"] {
		t.Fatalf("non-review tags should be preserved: %#v", got)
	}
}
