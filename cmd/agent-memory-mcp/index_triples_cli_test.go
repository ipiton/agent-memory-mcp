package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// fakeIndexTriplesStore is a minimal in-memory stand-in for *memory.Store
// used by indexTriplesLoop tests. It tracks per-memory triples and lets
// individual operations be made to fail.
type fakeIndexTriplesStore struct {
	mu          sync.Mutex
	triples     map[string][]memory.Triple
	failLookup  map[string]bool
	failDelete  map[string]bool
	failAddFor  map[string]bool
	deleteCalls map[string]int
}

func newFakeStore() *fakeIndexTriplesStore {
	return &fakeIndexTriplesStore{
		triples:     map[string][]memory.Triple{},
		failLookup:  map[string]bool{},
		failDelete:  map[string]bool{},
		failAddFor:  map[string]bool{},
		deleteCalls: map[string]int{},
	}
}

func (f *fakeIndexTriplesStore) TriplesForMemory(_ context.Context, id string) ([]memory.Triple, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLookup[id] {
		return nil, errors.New("forced lookup failure")
	}
	out := make([]memory.Triple, len(f.triples[id]))
	copy(out, f.triples[id])
	return out, nil
}

func (f *fakeIndexTriplesStore) DeleteTriplesForMemory(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls[id]++
	if f.failDelete[id] {
		return 0, errors.New("forced delete failure")
	}
	n := len(f.triples[id])
	delete(f.triples, id)
	return n, nil
}

func (f *fakeIndexTriplesStore) AddTriples(_ context.Context, triples []*memory.Triple) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(triples) == 0 {
		return nil
	}
	id := triples[0].MemoryID
	if f.failAddFor[id] {
		return errors.New("forced add failure")
	}
	for _, t := range triples {
		f.triples[t.MemoryID] = append(f.triples[t.MemoryID], *t)
	}
	return nil
}

// fakeExtractor returns the configured set of triples per memory ID. When
// returnErr is set for a memory ID, Extract returns that error.
type fakeExtractor struct {
	mu        sync.Mutex
	calls     int
	results   map[string][]*memory.Triple
	returnErr map[string]error
}

func newFakeExtractor() *fakeExtractor {
	return &fakeExtractor{
		results:   map[string][]*memory.Triple{},
		returnErr: map[string]error{},
	}
}

func (f *fakeExtractor) Extract(_ context.Context, mem *memory.Memory) ([]*memory.Triple, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.returnErr[mem.ID]; ok {
		return nil, err
	}
	return f.results[mem.ID], nil
}

func mkMemory(id, title, content string) *memory.Memory {
	return &memory.Memory{ID: id, Title: title, Content: content, Type: memory.TypeSemantic}
}

func mkTriple(memID, subj, rel, obj string) *memory.Triple {
	return &memory.Triple{MemoryID: memID, Subject: subj, Relation: rel, Object: obj}
}

func TestIndexTriplesLoop_HappyPath_ProcessesEverything(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "first", "content one"),
		mkMemory("m2", "second", "content two"),
	}
	extractor.results["m1"] = []*memory.Triple{mkTriple("m1", "a", "r", "b")}
	extractor.results["m2"] = []*memory.Triple{
		mkTriple("m2", "x", "r1", "y"),
		mkTriple("m2", "x", "r2", "z"),
	}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{Resume: true})
	if stats.Processed != 2 {
		t.Fatalf("Processed = %d, want 2", stats.Processed)
	}
	if stats.TripleCount != 3 {
		t.Fatalf("TripleCount = %d, want 3", stats.TripleCount)
	}
	if extractor.calls != 2 {
		t.Fatalf("extractor calls = %d, want 2", extractor.calls)
	}
}

func TestIndexTriplesLoop_Resume_SkipsMemoriesWithExistingTriples(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "already", "x"),
		mkMemory("m2", "fresh", "y"),
	}
	// m1 already has triples — must be skipped under --resume.
	store.triples["m1"] = []memory.Triple{{MemoryID: "m1", Subject: "old", Relation: "r", Object: "thing"}}
	extractor.results["m2"] = []*memory.Triple{mkTriple("m2", "a", "r", "b")}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{Resume: true})
	if stats.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Processed != 1 {
		t.Fatalf("Processed = %d, want 1", stats.Processed)
	}
	if extractor.calls != 1 {
		t.Fatalf("extractor must be called only for the un-extracted memory; got %d calls", extractor.calls)
	}
	if store.deleteCalls["m1"] != 0 {
		t.Fatalf("Resume should not delete existing triples; deleteCalls[m1]=%d", store.deleteCalls["m1"])
	}
}

func TestIndexTriplesLoop_Force_ReExtractsAndReplaces(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{mkMemory("m1", "doc", "stuff")}
	store.triples["m1"] = []memory.Triple{{MemoryID: "m1", Subject: "old1", Relation: "r", Object: "x"}}
	extractor.results["m1"] = []*memory.Triple{
		mkTriple("m1", "new1", "r", "x"),
		mkTriple("m1", "new2", "r", "y"),
	}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{Resume: true, Force: true})
	if stats.Processed != 1 {
		t.Fatalf("Processed = %d, want 1", stats.Processed)
	}
	if got := store.triples["m1"]; len(got) != 2 {
		t.Fatalf("expected 2 triples after force re-extract, got %d (%v)", len(got), got)
	}
	if got := store.deleteCalls["m1"]; got != 1 {
		t.Fatalf("deleteCalls[m1] = %d, want 1 (replace-all)", got)
	}
}

func TestIndexTriplesLoop_DryRunDoesNotCallExtractor(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "first", "x"),
		mkMemory("m2", "second", "y"),
	}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{DryRun: true})
	if stats.Planned != 2 {
		t.Fatalf("Planned = %d, want 2", stats.Planned)
	}
	if extractor.calls != 0 {
		t.Fatalf("extractor must not be called in dry-run, got %d calls", extractor.calls)
	}
	if stats.Processed != 0 {
		t.Fatalf("Processed must stay 0 in dry-run, got %d", stats.Processed)
	}
}

func TestIndexTriplesLoop_LimitStopsAfterNProcessed(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "a", "x"),
		mkMemory("m2", "b", "y"),
		mkMemory("m3", "c", "z"),
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		extractor.results[id] = []*memory.Triple{mkTriple(id, "s", "r", "o")}
	}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{Limit: 2})
	if stats.Processed != 2 {
		t.Fatalf("Processed = %d, want 2", stats.Processed)
	}
}

func TestIndexTriplesLoop_ExtractorErrorContinues(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "broken", "x"),
		mkMemory("m2", "fine", "y"),
	}
	extractor.returnErr["m1"] = errors.New("network down")
	extractor.results["m2"] = []*memory.Triple{mkTriple("m2", "s", "r", "o")}

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{})
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", stats.Errors)
	}
	if stats.Processed != 1 {
		t.Fatalf("Processed = %d, want 1 (m2 should still succeed)", stats.Processed)
	}
}

func TestIndexTriplesLoop_EmptyExtractionCountsAsEmpty(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{mkMemory("m1", "trivial", "x")}
	// no results configured → extractor returns nil

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{})
	if stats.Empty != 1 {
		t.Fatalf("Empty = %d, want 1", stats.Empty)
	}
	if stats.Processed != 0 {
		t.Fatalf("Processed = %d, want 0", stats.Processed)
	}
}

func TestIndexTriplesLoop_LookupFailureBumpsErrors(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{mkMemory("m1", "x", "y")}
	store.failLookup["m1"] = true

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{})
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", stats.Errors)
	}
	if extractor.calls != 0 {
		t.Fatalf("extractor must not be called when lookup fails")
	}
}

func TestIndexTriplesLoop_PersistFailureBumpsErrorsButContinues(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{
		mkMemory("m1", "broken-persist", "x"),
		mkMemory("m2", "ok", "y"),
	}
	for _, id := range []string{"m1", "m2"} {
		extractor.results[id] = []*memory.Triple{mkTriple(id, "s", "r", "o")}
	}
	store.failAddFor["m1"] = true

	stats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{})
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", stats.Errors)
	}
	if stats.Processed != 1 {
		t.Fatalf("Processed = %d, want 1 (m2 still ok)", stats.Processed)
	}
}

// Sanity: Verbose flag should not influence the stats outcome, only side
// effects (stdout/stderr lines we don't assert on directly).
func TestIndexTriplesLoop_VerboseDoesNotAffectStats(t *testing.T) {
	store := newFakeStore()
	extractor := newFakeExtractor()
	memories := []*memory.Memory{mkMemory("m1", "v", "x")}
	extractor.results["m1"] = []*memory.Triple{mkTriple("m1", "s", "r", "o")}

	silentStats := indexTriplesLoop(context.Background(), store, extractor, memories, indexTriplesLoopOptions{Verbose: false})
	store2 := newFakeStore()
	extractor2 := newFakeExtractor()
	extractor2.results["m1"] = []*memory.Triple{mkTriple("m1", "s", "r", "o")}
	verboseStats := indexTriplesLoop(context.Background(), store2, extractor2, memories, indexTriplesLoopOptions{Verbose: true, ProgressEvery: 1})
	if silentStats.Processed != verboseStats.Processed || silentStats.TripleCount != verboseStats.TripleCount {
		t.Fatalf("verbose changed counted outcome: %+v vs %+v", silentStats, verboseStats)
	}
	_ = fmt.Sprint("") // keep fmt referenced for visual check; harmless
}
