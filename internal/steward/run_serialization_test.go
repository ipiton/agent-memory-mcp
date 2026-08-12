package steward

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// concurrencyProbeStore wraps a real store and records how many Run bodies are
// inside the store at the same time. Every Run calls ListLightweight, so the
// peak observed there is the peak concurrency of Run itself.
type concurrencyProbeStore struct {
	storeAPI
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (p *concurrencyProbeStore) ListLightweight(filters memory.Filters) []*memory.Memory {
	cur := p.inFlight.Add(1)
	for {
		prev := p.peak.Load()
		if cur <= prev || p.peak.CompareAndSwap(prev, cur) {
			break
		}
	}
	// Widen the window so an unserialized second Run would reliably overlap.
	time.Sleep(2 * time.Millisecond)
	out := p.storeAPI.ListLightweight(filters)
	p.inFlight.Add(-1)
	return out
}

func (p *concurrencyProbeStore) DB() *sql.DB { return p.storeAPI.DB() }

// T88 H4. The interval loop and every session_close trigger each spawned their
// own Run goroutine with nothing serializing them, so two scans walked the same
// corpus concurrently and each filed its own inbox items — CreateInboxItem does
// not deduplicate.
func TestConcurrentRunsAreSerialized(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for i := range 5 {
		m := &memory.Memory{
			Content: "steward corpus entry for serialization probe",
			Type:    memory.TypeSemantic,
			Context: "probe",
		}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store %d: %v", i, err)
		}
	}

	probe := &concurrencyProbeStore{storeAPI: store}
	svc, err := NewService(probe, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	policy := svc.Policy()
	policy.Mode = PolicyModeScheduled
	svc.SetPolicy(policy)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Run(ctx, RunParams{Scope: ScopeFull}); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak := probe.peak.Load(); peak > 1 {
		t.Fatalf("peak concurrent scans = %d, want 1 — Run is not serialized", peak)
	}
}

// The scheduler drops an overlapping background trigger instead of queueing a
// second identical full scan behind it.
func TestSchedulerCoalescesOverlappingTriggers(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	policy := svc.Policy()
	policy.Mode = PolicyModeEventDriven
	svc.SetPolicy(policy)

	sched := NewScheduler(svc, nil)

	// Occupy the in-flight flag the way a running scan would.
	if !sched.runInFlight.CompareAndSwap(false, true) {
		t.Fatal("runInFlight was already set on a fresh scheduler")
	}

	done := make(chan struct{})
	go func() {
		sched.runOnceWithCtx(context.Background(), "test-overlap")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("overlapping trigger blocked instead of being coalesced")
	}

	// The coalesced call must not clear the flag owned by the scan in flight.
	if !sched.runInFlight.Load() {
		t.Fatal("coalesced trigger cleared the in-flight flag of the running scan")
	}
	sched.runInFlight.Store(false)
}
