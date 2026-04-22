// Package eval provides a retrieval evaluation harness for the RAG engine.
//
// The harness indexes a curated test corpus, runs a set of QA-pair queries,
// and computes retrieval quality metrics (Hit Rate@K, MRR). It is intended to
// be invoked under the //go:build eval tag so it stays out of the default
// test suite while still being runnable in CI as a regression gate.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// QAQuery describes a single evaluation query and its expected relevant docs.
type QAQuery struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Question       string   `json:"question"`
	ExpectedDocIDs []string `json:"expected_doc_ids"`
	SourceType     string   `json:"source_type,omitempty"`
}

// QAResult captures what the harness observed when running one QAQuery.
type QAResult struct {
	Query    QAQuery  `json:"query"`
	TopK     []string `json:"top_k"`
	Hit      bool     `json:"hit"`
	FirstHit int      `json:"first_hit"`
}

// TypeMetrics aggregates retrieval metrics over all queries of a given type.
type TypeMetrics struct {
	Count      int     `json:"count"`
	HitRateAtK float64 `json:"hit_rate_at_5"`
	MRR        float64 `json:"mrr"`
}

// EvalMetrics aggregates retrieval metrics across the whole evaluation set.
type EvalMetrics struct {
	TotalQueries int                    `json:"total_queries"`
	HitRateAtK   float64                `json:"hit_rate_at_5"`
	MRR          float64                `json:"mrr"`
	PerType      map[string]TypeMetrics `json:"per_type"`
}

// LoadQASet reads a JSON file of QAQuery entries.
func LoadQASet(path string) ([]QAQuery, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read qa set: %w", err)
	}
	var queries []QAQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, fmt.Errorf("unmarshal qa set: %w", err)
	}
	return queries, nil
}

// LoadBaseline reads a JSON baseline metrics file.
func LoadBaseline(path string) (*EvalMetrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	var metrics EvalMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return nil, fmt.Errorf("unmarshal baseline: %w", err)
	}
	return &metrics, nil
}

// WriteBaseline writes EvalMetrics as pretty-printed JSON.
func WriteBaseline(path string, metrics *EvalMetrics) error {
	if metrics == nil {
		return fmt.Errorf("nil metrics")
	}
	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	return nil
}
