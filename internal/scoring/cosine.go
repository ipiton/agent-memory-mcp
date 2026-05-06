package scoring

import "math"

// CosineSimilarity returns the cosine similarity of two equal-length
// float32 vectors. Returns 0 for length mismatch, empty vectors, or when
// either vector is all zeros (degenerate norm). Result is in [-1, 1] for
// proper inputs.
//
// Lives in scoring/ rather than vectorstore/ because it is consumed across
// memory recall, vectorstore search, and steward conflict scanning — a pure
// math primitive that has nothing specific to vector storage.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
