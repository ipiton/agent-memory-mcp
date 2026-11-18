package rag

import (
	"context"
	"testing"
)

func TestSearchEnglishQuestionUsesSubjectWords(t *testing.T) {
	server := newBatchEmbeddingServer(t)
	t.Cleanup(server.Close)
	root := t.TempDir()
	writeCorpusFile(t, root, "docs/procedure.md", "# Procedure\n\ncanary deployment rollback\n")
	writeCorpusFile(t, root, "docs/other.md", "# Other\n\nwhat is the and which was were been with from should must\n")
	engine := newRootEngine(t, root, t.TempDir(), server.URL)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Search(context.Background(), "What is the canary deployment rollback and which should have been used?", 2, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 || result.Results[0].Path != "docs/procedure.md" {
		t.Fatalf("ranked results = %+v, want the procedure first", result.Results)
	}
	for _, row := range result.Results {
		if row.Path == "docs/other.md" && row.Debug.Breakdown.KeywordRaw != 0 {
			t.Fatalf("question grammar contributed a keyword score: %+v", row.Debug.Breakdown)
		}
	}
}
