//go:build corpus

// Corpus harness for the contradiction detector (T105).
//
// The detector's acceptance criterion is a number on a real memory bank, not a
// property of a synthetic pair: "a run against a snapshot of the Sema memory
// yields ≤2 findings, and no single record appears in more than one pair". The
// 2026-08-12 measurement that opened T105 produced 17 findings, 17 of them
// false — with one record standing in 3 pairs and another in 5. A unit test
// cannot show that, because the failure is a property of the corpus (patterns
// describing fixes naturally contain "superseded", "replaced by", "removed").
//
// Excluded from the default build because it needs a database that is not in
// the repository. Run against a read-only copy — never the live file:
//
//	cp /opt/homebrew/var/agent-memory-mcp/memory-store/memories.db /tmp/snap.db
//	go test -tags=corpus ./internal/steward/ -run TestContradictionCorpus -v \
//	    -corpus-db /tmp/snap.db
package steward

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

var corpusDB = flag.String("corpus-db", "", "path to a read-only copy of a memories.db snapshot")

func TestContradictionCorpus(t *testing.T) {
	if *corpusDB == "" {
		t.Skip("no -corpus-db given")
	}

	store, err := memory.NewStore(*corpusDB, nil, nil)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = store.Close() }()

	loaded, err := store.List(context.Background(), memory.Filters{}, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Same input the live scan sees: loadActiveMemories drops archived records
	// before any scanner runs. Comparing against the unfiltered corpus would
	// inflate the baseline with pairs the scanner never considers.
	var all []*memory.Memory
	withEmbeddings := 0
	for _, m := range loaded {
		if m == nil || memory.IsArchivedMemory(m) {
			continue
		}
		all = append(all, m)
		if len(m.Embedding) > 0 {
			withEmbeddings++
		}
	}

	result := &ScanResult{}
	scanSemanticConflicts(all, DefaultPolicy(), result)

	// How often each record is named. A record in many pairs is the hub
	// signature: one keyword in one body dragging in everything semantically
	// nearby.
	appearances := map[string]int{}
	for _, a := range result.Actions {
		for _, id := range a.TargetIDs {
			appearances[id]++
		}
	}
	type hub struct {
		id string
		n  int
	}
	hubs := make([]hub, 0, len(appearances))
	for id, n := range appearances {
		hubs = append(hubs, hub{id, n})
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].n > hubs[j].n })

	t.Logf("corpus: %d records, %d with embeddings", len(all), withEmbeddings)
	t.Logf("findings: %d", len(result.Actions))
	for _, a := range result.Actions {
		t.Logf("  %s | %s", a.Title, a.Rationale)
	}
	for i, h := range hubs {
		if i >= 5 || h.n < 2 {
			break
		}
		t.Logf("  hub %s appears in %d pairs", h.id, h.n)
	}

	if len(result.Actions) > 2 {
		t.Errorf("findings = %d, want ≤2 (T105 acceptance)", len(result.Actions))
	}
	for _, h := range hubs {
		if h.n > 1 {
			t.Errorf("record %s appears in %d pairs, want ≤1 (T105 acceptance)", h.id, h.n)
		}
	}
	fmt.Print("")
}
