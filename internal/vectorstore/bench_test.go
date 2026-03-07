package vectorstore

import (
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func randomEmbedding(dim int) []float32 {
	emb := make([]float32, dim)
	for i := range emb {
		emb[i] = rand.Float32()*2 - 1
	}
	return emb
}

func benchStoreWithChunks(b *testing.B, n, dim int) *SQLiteStore {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "bench-vec.db")
	store, err := NewSQLiteStore(dbPath, dim, zap.NewNop())
	if err != nil {
		b.Fatalf("NewSQLiteStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	const batchSize = 500
	for i := 0; i < n; i += batchSize {
		end := min(i+batchSize, n)
		chunks := make([]Chunk, 0, end-i)
		for j := i; j < end; j++ {
			chunks = append(chunks, Chunk{
				ID:        fmt.Sprintf("chunk-%d", j),
				DocPath:   fmt.Sprintf("docs/file-%d.md", j%100),
				Content:   fmt.Sprintf("This is document chunk number %d with some searchable content about deployment and infrastructure", j),
				Title:     fmt.Sprintf("Document %d", j),
				Embedding: randomEmbedding(dim),
			})
		}
		if err := store.Upsert(chunks); err != nil {
			b.Fatalf("Upsert batch: %v", err)
		}
	}
	return store
}

func BenchmarkCosineSimilarity(b *testing.B) {
	for _, dim := range []int{384, 768, 1536} {
		b.Run(fmt.Sprintf("dim=%d", dim), func(b *testing.B) {
			a := randomEmbedding(dim)
			v := randomEmbedding(dim)
			b.ResetTimer()
			for range b.N {
				CosineSimilarity(a, v)
			}
		})
	}
}

func BenchmarkSearch(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			store := benchStoreWithChunks(b, n, 128)
			query := randomEmbedding(128)
			b.ResetTimer()
			for range b.N {
				if _, err := store.Search(query, 10); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkKeywordSearchScaled(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			store := benchStoreWithChunks(b, n, 128)
			b.ResetTimer()
			for range b.N {
				if _, err := store.KeywordSearch("deployment infrastructure", 10); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeDecodeEmbedding(b *testing.B) {
	emb := randomEmbedding(768)
	b.Run("encode", func(b *testing.B) {
		for range b.N {
			encodeEmbedding(emb)
		}
	})
	blob := encodeEmbedding(emb)
	b.Run("decode", func(b *testing.B) {
		for range b.N {
			if _, err := decodeEmbedding(blob); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkUpsert(b *testing.B) {
	dim := 128
	dbPath := filepath.Join(b.TempDir(), "bench-upsert.db")
	store, err := NewSQLiteStore(dbPath, dim, zap.NewNop())
	if err != nil {
		b.Fatalf("NewSQLiteStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	chunks := make([]Chunk, 50)
	for i := range chunks {
		chunks[i] = Chunk{
			ID:        fmt.Sprintf("chunk-%d", i),
			DocPath:   "docs/bench.md",
			Content:   "benchmark content for upsert testing",
			Embedding: randomEmbedding(dim),
		}
	}

	b.ResetTimer()
	for n := range b.N {
		for i := range chunks {
			chunks[i].ID = fmt.Sprintf("chunk-%d-%d", n, i)
		}
		if err := store.Upsert(chunks); err != nil {
			b.Fatal(err)
		}
	}
}

// Verify CosineSimilarity correctness with known vectors.
func BenchmarkCosineSimilarityAccuracy(b *testing.B) {
	// Identical vectors → similarity = 1.0
	v := []float32{1, 2, 3, 4, 5}
	sim := CosineSimilarity(v, v)
	if math.Abs(sim-1.0) > 1e-6 {
		b.Fatalf("identical vectors: got %.6f, want 1.0", sim)
	}

	// Orthogonal vectors → similarity = 0.0
	a := []float32{1, 0, 0}
	o := []float32{0, 1, 0}
	sim = CosineSimilarity(a, o)
	if math.Abs(sim) > 1e-6 {
		b.Fatalf("orthogonal vectors: got %.6f, want 0.0", sim)
	}
}
