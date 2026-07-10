package memory

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func newGateTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "m.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestPromoteToCanonicalGateBlocksConversationalAuto is the T77 core: an
// automated promotion (verified=false) of a conversational-origin memory is
// refused, so an auto-pipeline cannot canonicalize planted records.
func TestPromoteToCanonicalGateBlocksConversationalAuto(t *testing.T) {
	store := newGateTestStore(t)
	ctx := context.Background()

	m := &Memory{Content: "planted record", Type: TypeSemantic, Importance: 0.8}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	_, err := store.PromoteToCanonical(ctx, m.ID, "auto", false)
	if !errors.Is(err, ErrPromotionRequiresVerification) {
		t.Fatalf("auto-promote of conversational record: got %v, want ErrPromotionRequiresVerification", err)
	}

	got, _ := store.Get(m.ID)
	if LifecycleStatusOf(got) == LifecycleCanonical {
		t.Fatal("record must not have been canonicalized by a blocked auto-promotion")
	}
}

// TestPromoteToCanonicalVerifiedAllowed: a human/verify promotion (verified=true)
// proceeds and stamps provenance=verified (T77).
func TestPromoteToCanonicalVerifiedAllowed(t *testing.T) {
	store := newGateTestStore(t)
	ctx := context.Background()

	m := &Memory{Content: "reviewed decision", Type: TypeSemantic, Importance: 0.8}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	if _, err := store.PromoteToCanonical(ctx, m.ID, "reviewer", true); err != nil {
		t.Fatalf("verified promote: %v", err)
	}

	got, _ := store.Get(m.ID)
	if LifecycleStatusOf(got) != LifecycleCanonical {
		t.Fatalf("verified promote must canonicalize, got %q", LifecycleStatusOf(got))
	}
	if ProvenanceOf(got) != ProvenanceVerified {
		t.Fatalf("verified promote must stamp provenance=verified, got %q", ProvenanceOf(got))
	}
}

// TestPromoteToCanonicalTrustedProvenanceAutoAllowed: a record that already
// carries trusted provenance (external) may be auto-promoted without a human
// gate (T77).
func TestPromoteToCanonicalTrustedProvenanceAutoAllowed(t *testing.T) {
	store := newGateTestStore(t)
	ctx := context.Background()

	m := &Memory{
		Content:    "ingested from trusted docs",
		Type:       TypeSemantic,
		Importance: 0.8,
		Metadata:   map[string]string{MetadataProvenance: ProvenanceExternal},
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	if _, err := store.PromoteToCanonical(ctx, m.ID, "auto", false); err != nil {
		t.Fatalf("auto-promote of external-provenance record must be allowed: %v", err)
	}
	got, _ := store.Get(m.ID)
	if LifecycleStatusOf(got) != LifecycleCanonical {
		t.Fatalf("trusted record must canonicalize, got %q", LifecycleStatusOf(got))
	}
}
