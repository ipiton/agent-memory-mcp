package main

import "testing"

func TestNormalizeReviewResolutionValue(t *testing.T) {
	got, err := normalizeReviewResolutionValue("dismissed")
	if err != nil {
		t.Fatalf("normalizeReviewResolutionValue: %v", err)
	}
	if got != "dismissed" {
		t.Fatalf("resolution = %q, want dismissed", got)
	}
}

func TestResolvedReviewQueueTagsRemovesPendingMarkers(t *testing.T) {
	got := resolvedReviewQueueTags([]string{"review:required", "status:review_required", "service:api", "review-queue"}, "resolved")

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
}
