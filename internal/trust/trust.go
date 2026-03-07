// Package trust provides shared trust metadata types used across memory and RAG subsystems.
package trust

import "time"

// Metadata describes the reliability and freshness of a knowledge result.
type Metadata struct {
	KnowledgeLayer string    `json:"knowledge_layer,omitempty"`
	SourceType     string    `json:"source_type"`
	Confidence     float64   `json:"confidence"`
	LastVerifiedAt time.Time `json:"last_verified_at"`
	Owner          string    `json:"owner"`
	FreshnessScore float64   `json:"freshness_score"`
}
