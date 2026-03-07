package memory

import (
	"testing"
	"time"
)

func TestNormalizeMemoryForStoreAlignsEngineeringMetadataAndTags(t *testing.T) {
	mem := &Memory{
		Content: "rollback ingress deployment",
		Type:    TypeProcedural,
		Tags:    []string{" runbook ", "service:api"},
		Metadata: map[string]string{
			MetadataStatus:         "accepted",
			MetadataReviewRequired: "YES",
		},
	}

	if err := NormalizeMemoryForStore(mem); err != nil {
		t.Fatalf("NormalizeMemoryForStore: %v", err)
	}

	if got := mem.Metadata[MetadataEntity]; got != string(EngineeringTypeRunbook) {
		t.Fatalf("metadata entity = %q, want %q", got, EngineeringTypeRunbook)
	}
	if got := mem.Metadata[MetadataLifecycleStatus]; got != string(LifecycleActive) {
		t.Fatalf("lifecycle_status = %q, want %q", got, LifecycleActive)
	}
	if got := mem.Metadata[MetadataReviewRequired]; got != "true" {
		t.Fatalf("review_required = %q, want true", got)
	}
	for _, wanted := range []string{"runbook", "service:api", "status:accepted", "review:required"} {
		if !containsTag(mem.Tags, wanted) {
			t.Fatalf("expected tag %q in %v", wanted, mem.Tags)
		}
	}
}

func TestLifecycleStatusOfPreservesCanonicalAndCompatibilityMappings(t *testing.T) {
	t.Run("canonical markers win", func(t *testing.T) {
		mem := &Memory{
			Type: TypeProcedural,
			Metadata: map[string]string{
				MetadataKnowledgeLayer: "canonical",
				MetadataStatus:         "confirmed",
			},
		}
		if got := LifecycleStatusOf(mem); got != LifecycleCanonical {
			t.Fatalf("LifecycleStatusOf = %q, want %q", got, LifecycleCanonical)
		}
	})

	t.Run("merged becomes superseded", func(t *testing.T) {
		mem := &Memory{
			Type: TypeSemantic,
			Metadata: map[string]string{
				MetadataStatus: "merged",
			},
		}
		if got := LifecycleStatusOf(mem); got != LifecycleSuperseded {
			t.Fatalf("LifecycleStatusOf = %q, want %q", got, LifecycleSuperseded)
		}
	})

	t.Run("working defaults to draft", func(t *testing.T) {
		mem := &Memory{Type: TypeWorking}
		if got := LifecycleStatusOf(mem); got != LifecycleDraft {
			t.Fatalf("LifecycleStatusOf = %q, want %q", got, LifecycleDraft)
		}
	})
}

func TestBuildEngineeringMetadataKeepsDetailedStatusAndDerivedLifecycle(t *testing.T) {
	metadata := BuildEngineeringMetadata(EngineeringTypeDecision, "payments-api", "", "accepted", false, map[string]string{
		MetadataOwner: "platform",
	})

	if got := metadata[MetadataEntity]; got != string(EngineeringTypeDecision) {
		t.Fatalf("entity = %q, want %q", got, EngineeringTypeDecision)
	}
	if got := metadata[MetadataStatus]; got != "accepted" {
		t.Fatalf("status = %q, want accepted", got)
	}
	if got := metadata[MetadataLifecycleStatus]; got != string(LifecycleActive) {
		t.Fatalf("lifecycle_status = %q, want %q", got, LifecycleActive)
	}
	if got := metadata[MetadataService]; got != "payments-api" {
		t.Fatalf("service = %q, want payments-api", got)
	}
	if got := metadata[MetadataOwner]; got != "platform" {
		t.Fatalf("owner = %q, want platform", got)
	}
}

func TestReviewRequiredReducesTrustConfidence(t *testing.T) {
	now := time.Now()
	base := &Memory{
		Content:    "disable hpa for api during migration",
		Type:       TypeSemantic,
		Importance: 0.8,
		Metadata: map[string]string{
			MetadataEntity:         string(EngineeringTypeDecision),
			MetadataStatus:         "accepted",
			MetadataLastVerifiedAt: now.UTC().Format(time.RFC3339),
		},
	}
	review := copyMemory(base)
	review.Metadata = copyMetadata(base.Metadata)
	review.Metadata[MetadataReviewRequired] = "true"

	if err := NormalizeMemoryForStore(base); err != nil {
		t.Fatalf("NormalizeMemoryForStore base: %v", err)
	}
	if err := NormalizeMemoryForStore(review); err != nil {
		t.Fatalf("NormalizeMemoryForStore review: %v", err)
	}

	baseTrust := deriveTrustMetadata(base, now)
	reviewTrust := deriveTrustMetadata(review, now)
	if reviewTrust.Confidence >= baseTrust.Confidence {
		t.Fatalf("expected review-required confidence %.2f to be below base %.2f", reviewTrust.Confidence, baseTrust.Confidence)
	}
	if !RequiresReview(review) {
		t.Fatal("RequiresReview(review) = false, want true")
	}
}
