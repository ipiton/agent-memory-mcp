package vectorstore

import (
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestVectorFootprint measures what loading the vectors costs the process on a
// real index (T164/T177). It runs only when VECTORSTORE_FOOTPRINT_DB names a
// COPY of a vectors.db: opening the store sweeps orphans and may write. The
// dimension comes from VECTORSTORE_FOOTPRINT_DIM (default 768).
//
//	VECTORSTORE_FOOTPRINT_DB=/tmp/copy/vectors.db go test -run TestVectorFootprint -v -count=1 ./internal/vectorstore
func TestVectorFootprint(t *testing.T) {
	dbPath := os.Getenv("VECTORSTORE_FOOTPRINT_DB")
	if dbPath == "" {
		t.Skip("VECTORSTORE_FOOTPRINT_DB not set")
	}
	dim := 768
	if v := os.Getenv("VECTORSTORE_FOOTPRINT_DIM"); v != "" {
		dim, _ = strconv.Atoi(v)
	}

	store, err := NewSQLiteStore(dbPath, dim, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	report := func(stage string) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("%-22s heap_inuse=%4d MB heap_sys=%4d MB heap_released=%4d MB next_gc=%4d MB footprint=%s",
			stage, ms.HeapInuse>>20, ms.HeapSys>>20, ms.HeapReleased>>20, ms.NextGC>>20, physFootprint(t))
	}
	report("opened")

	query := randomEmbedding(dim)
	began := time.Now()
	if _, _, err := store.Search(query, 10); err != nil {
		t.Fatalf("Search: %v", err)
	}
	t.Logf("first search (load) took %s", time.Since(began))
	report("after load")

	runtime.GC()
	debug.FreeOSMemory()
	report("after GC+FreeOSMemory")

	var ranks []time.Duration
	for range 30 {
		_, timing, err := store.Search(randomEmbedding(dim), 50)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		ranks = append(ranks, timing.Rank)
	}
	sort.Slice(ranks, func(i, j int) bool { return ranks[i] < ranks[j] })
	t.Logf("rank over %d searches: p50=%s p95=%s", len(ranks), ranks[len(ranks)/2], ranks[len(ranks)*95/100])
	report("after 30 searches")
}

// physFootprint asks macOS footprint(1) for the process's physical footprint,
// the figure Activity Monitor shows as Memory. Elsewhere it reports RSS from ps.
func physFootprint(t *testing.T) string {
	pid := strconv.Itoa(os.Getpid())
	if out, err := exec.Command("footprint", pid).Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			if strings.Contains(line, "Footprint:") {
				return strings.TrimSpace(line)
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", pid).Output()
	if err != nil {
		t.Logf("ps: %v", err)
		return "?"
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return strconv.Itoa(kb>>10) + " MB rss"
}
