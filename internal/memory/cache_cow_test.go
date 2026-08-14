package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// T88 H1. Recall and ListLightweight walk *cachedMemory pointers handed out by
// snapshotForContext and read their fields without holding ms.mu — the snapshot
// copies pointers, not values. Cheap-update paths used to edit those very
// structs in place under mu.Lock, which excludes other writers but not these
// readers. Run under -race: this is red before the copy-on-write fix.
func TestCacheUpdatesDoNotRaceWithLockFreeReaders(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ids := make([]string, 0, 20)
	for i := range 20 {
		m := &Memory{
			Content: fmt.Sprintf("ingress rollback runbook step %d", i),
			Type:    TypeSemantic,
			Context: "shared-context",
		}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
		ids = append(ids, m.ID)
	}

	const rounds = 50
	var wg sync.WaitGroup

	// Lock-free readers.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				results, err := store.Recall(ctx, "ingress rollback", Filters{Context: "shared-context"}, 10)
				if err != nil {
					continue
				}
				for _, r := range results {
					_ = r.Memory.AccessCount
					_ = r.Memory.SedimentLayer
				}
				for _, m := range store.ListLightweight(Filters{Context: "shared-context"}) {
					_ = m.AccessCount
					_ = m.SedimentLayer
				}
			}
		}()
	}

	// flushAccessStats path: AccessedAt / AccessCount.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			store.flushAccessStats(ids, true)
		}
	}()

	// updateCachedField path: PromoteSediment / DemoteSediment.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range rounds {
			target := LayerEpisodic
			if i%2 == 0 {
				target = LayerSurface
			}
			for _, id := range ids[:5] {
				_, _ = store.PromoteSediment(ctx, id, target)
			}
		}
	}()

	wg.Wait()
}

// The copy-on-write swap must remain visible: a snapshot taken before the
// update keeps the old value, the cache serves the new one.
func TestUpdateCachedFieldPublishesReplacement(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &Memory{Content: "traefik ingress notes", Type: TypeSemantic, Context: "cow"}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	before := store.snapshotForContext("cow")
	if len(before) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(before))
	}
	stale := before[0]
	staleCount := stale.AccessCount

	store.updateCachedField(m.ID, func(cm *cachedMemory) { cm.AccessCount = staleCount + 7 })

	if stale.AccessCount != staleCount {
		t.Fatalf("pre-existing snapshot entry was mutated: AccessCount = %d, want %d", stale.AccessCount, staleCount)
	}

	after := store.snapshotForContext("cow")
	if len(after) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(after))
	}
	if after[0].AccessCount != staleCount+7 {
		t.Fatalf("AccessCount = %d, want %d", after[0].AccessCount, staleCount+7)
	}
	if after[0] == stale {
		t.Fatal("cache still holds the original pointer; the update was applied in place")
	}
}
