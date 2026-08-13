package memory

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// T89 H2. MarkOutdated and PromoteToCanonical read the memory outside any lock
// and wrote it back under one, so a writer that committed in between had its
// change silently overwritten. Both now hold writeMu across the whole
// read-modify-write, so a concurrent Update either lands before the read or
// after the write — never in the gap.
func TestMarkOutdatedDoesNotLoseConcurrentUpdate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	const rounds = 40
	for i := range rounds {
		m := &Memory{Content: "routing decision under contention", Type: TypeSemantic}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = store.MarkOutdated(ctx, m.ID, "retired", "")
		}()
		go func() {
			defer wg.Done()
			_ = store.Update(ctx, m.ID, Update{Title: "titled by the concurrent writer"})
		}()
		wg.Wait()

		got, err := store.Get(m.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Whichever order the two operations took, both effects must survive:
		// the title is never written by MarkOutdated, and the archived flag is
		// never written by Update, so a lost update shows up as one of them
		// missing.
		if got.Title != "titled by the concurrent writer" {
			t.Fatalf("round %d: title lost — MarkOutdated overwrote a committed update", i)
		}
		if got.Metadata["archived"] != "true" {
			t.Fatalf("round %d: archived flag lost — Update overwrote MarkOutdated", i)
		}
	}
}

// The supersession pair is written in one transaction, so both rows agree or
// neither moves.
func TestMarkOutdatedWritesSupersessionPairAtomically(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	old := &Memory{Content: "ingress via nginx", Type: TypeSemantic}
	if err := store.Store(ctx, old); err != nil {
		t.Fatalf("Store old: %v", err)
	}
	successor := &Memory{Content: "ingress via traefik", Type: TypeSemantic}
	if err := store.Store(ctx, successor); err != nil {
		t.Fatalf("Store successor: %v", err)
	}

	if _, err := store.MarkOutdated(ctx, old.ID, "replaced", successor.ID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}

	gotOld, err := store.Get(old.ID)
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if gotOld.SupersededBy != successor.ID {
		t.Errorf("retired.SupersededBy = %q, want %q", gotOld.SupersededBy, successor.ID)
	}
	if gotOld.ValidUntil == nil {
		t.Error("retired.ValidUntil is nil, want the retirement instant")
	}
	if gotOld.Metadata["status"] != "superseded" {
		t.Errorf("retired status = %q, want superseded", gotOld.Metadata["status"])
	}

	gotNew, err := store.Get(successor.ID)
	if err != nil {
		t.Fatalf("Get successor: %v", err)
	}
	if gotNew.Replaces != old.ID {
		t.Errorf("successor.Replaces = %q, want %q — the back-link is the other half of the pair", gotNew.Replaces, old.ID)
	}
	if gotNew.ValidFrom == nil {
		t.Error("successor.ValidFrom is nil, want the supersession instant")
	}
	if got := referencedByCountFromMetadata(gotNew.Metadata); got != 1 {
		t.Errorf("successor referenced_by_count = %d, want 1", got)
	}
}

// A superseded_by pointing at nothing is still recorded — the retirement is
// real even when the named successor is not in the store.
func TestMarkOutdatedToleratesDanglingSuccessor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &Memory{Content: "retired with a dangling pointer", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	res, err := store.MarkOutdated(ctx, m.ID, "replaced", "no-such-id")
	if err != nil {
		t.Fatalf("MarkOutdated with dangling successor: %v", err)
	}
	if res.Status != "superseded" {
		t.Fatalf("status = %q, want superseded", res.Status)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SupersededBy != "no-such-id" {
		t.Fatalf("SupersededBy = %q, want the recorded pointer", got.SupersededBy)
	}
}

func TestPromoteToCanonicalDoesNotLoseConcurrentUpdate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	const rounds = 40
	for i := range rounds {
		m := &Memory{Content: "runbook worth canonicalising", Type: TypeProcedural}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = store.PromoteToCanonical(ctx, m.ID, "operator", true)
		}()
		go func() {
			defer wg.Done()
			_ = store.Update(ctx, m.ID, Update{Title: "titled by the concurrent writer"})
		}()
		wg.Wait()

		got, err := store.Get(m.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Title != "titled by the concurrent writer" {
			t.Fatalf("round %d: title lost — PromoteToCanonical overwrote a committed update", i)
		}
		if got.Metadata["canonical"] != "true" {
			t.Fatalf("round %d: canonical flag lost — Update overwrote the promotion", i)
		}
	}
}

// T89 M3: promotion moves both axes. An entry canonical on the metadata axis
// but still surface-level on the sediment axis is evictable knowledge that
// claims to be load-bearing.
func TestPromoteToCanonicalRaisesSedimentLayer(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &Memory{Content: "load-bearing rollback procedure", Type: TypeProcedural}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := NormalizeSedimentLayer(m.SedimentLayer); got != DefaultSedimentLayer {
		t.Fatalf("precondition: layer = %q, want %q", got, DefaultSedimentLayer)
	}

	if _, err := store.PromoteToCanonical(ctx, m.ID, "operator", true); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if layer := NormalizeSedimentLayer(got.SedimentLayer); layer != LayerCharacter {
		t.Fatalf("sediment layer = %q, want %q", layer, LayerCharacter)
	}
	if !strings.EqualFold(got.Metadata["knowledge_layer"], "canonical") {
		t.Fatalf("knowledge_layer = %q, want canonical", got.Metadata["knowledge_layer"])
	}
}
