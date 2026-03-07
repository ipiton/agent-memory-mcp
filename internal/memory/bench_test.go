package memory

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func benchStore(b *testing.B, n int) *Store {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "bench-mem.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	types := []Type{TypeSemantic, TypeEpisodic, TypeProcedural, TypeWorking}
	for i := range n {
		mem := &Memory{
			Content:    fmt.Sprintf("Memory about deployment process step %d with details on infrastructure and monitoring setup", i),
			Title:      fmt.Sprintf("Deployment step %d", i),
			Type:       types[i%len(types)],
			Importance: 0.3 + rand.Float64()*0.7,
			Tags:       []string{"deployment", fmt.Sprintf("tag-%d", i%20)},
		}
		if i%3 == 0 {
			mem.Context = "project-alpha"
		}
		if err := store.Store(context.Background(), mem); err != nil {
			b.Fatalf("Store: %v", err)
		}
	}
	return store
}

func BenchmarkRecall(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			store := benchStore(b, n)
			b.ResetTimer()
			for range b.N {
				if _, err := store.Recall(context.Background(), "deployment infrastructure monitoring", Filters{}, 10); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRecallWithContext(b *testing.B) {
	store := benchStore(b, 5000)
	b.ResetTimer()
	for range b.N {
		if _, err := store.Recall(context.Background(), "deployment", Filters{Context: "project-alpha"}, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkList(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			store := benchStore(b, n)
			b.ResetTimer()
			for range b.N {
				if _, err := store.List(context.Background(), Filters{}, 20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStore(b *testing.B) {
	dbPath := filepath.Join(b.TempDir(), "bench-store.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	b.ResetTimer()
	for n := range b.N {
		mem := &Memory{
			Content:    fmt.Sprintf("Benchmark memory entry %d", n),
			Title:      fmt.Sprintf("Entry %d", n),
			Type:       TypeSemantic,
			Importance: 0.5,
		}
		if err := store.Store(context.Background(), mem); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTextMatchScore(b *testing.B) {
	dbPath := filepath.Join(b.TempDir(), "bench-text.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	mem := &Memory{
		Content: "This is a detailed document about Kubernetes deployment strategies including blue-green and canary deployments with monitoring setup",
		Title:   "Kubernetes deployment strategies",
		Type:    TypeSemantic,
	}

	b.ResetTimer()
	for range b.N {
		store.textMatchScore("kubernetes deployment monitoring", mem)
	}
}
