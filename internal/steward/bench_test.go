package steward

import (
	"context"
	"fmt"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// seedSyntheticCorpus populates store with n synthetic memories spread
// across types/services/contexts to exercise the steward scanners
// realistically. ~10% canonical, ~10% archived (superseded), ~10% with
// embeddings to stress scanSemanticConflicts.
func seedSyntheticCorpus(b *testing.B, store *memory.Store, n int) {
	b.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		m := &memory.Memory{
			Title:      fmt.Sprintf("Item %d", i),
			Content:    fmt.Sprintf("Synthetic memory body %d describing service interaction", i),
			Type:       memory.TypeSemantic,
			Context:    fmt.Sprintf("ctx-%d", i%50),
			Importance: 0.5 + float64(i%5)*0.1,
			Tags:       []string{fmt.Sprintf("service:svc-%d", i%20)},
			Metadata: map[string]string{
				"service": fmt.Sprintf("svc-%d", i%20),
			},
		}
		if i%10 == 0 {
			// Mark as canonical via metadata.
			m.Metadata["knowledge_layer"] = "canonical"
		}
		if i%10 == 1 {
			// Drift dummy embedding for ~10% to exercise scanSemanticConflicts.
			emb := make([]float32, 8)
			for k := range emb {
				emb[k] = float32(i%7) / 10.0
			}
			m.Embedding = emb
		}
		if err := store.Store(ctx, m); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
}

func benchmarkRunScannersN(b *testing.B, n int) {
	store := newBenchStore(b)
	seedSyntheticCorpus(b, store, n)
	policy := DefaultPolicy()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RunScanners(ctx, store, policy, ScopeFull, "", "")
	}
}

func newBenchStore(b *testing.B) *memory.Store {
	b.Helper()
	dir := b.TempDir()
	store, err := memory.NewStore(dir+"/bench.db", nil, nil)
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

func BenchmarkRunScanners_500(b *testing.B)  { benchmarkRunScannersN(b, 500) }
func BenchmarkRunScanners_2000(b *testing.B) { benchmarkRunScannersN(b, 2000) }
