package rag

import (
	"context"
	"strings"
	"testing"
)

func TestIndexMarkdownWithManifestNamesRemainsReadable(t *testing.T) {
	for _, path := range []string{
		"docs/deployment.md",
		"docs/service.md",
		"docs/ingress.md",
		"docs/k8s/notes.md",
		"docs/kubernetes/notes.md",
	} {
		t.Run(path, func(t *testing.T) {
			server := newBatchEmbeddingServer(t)
			t.Cleanup(server.Close)
			root := t.TempDir()
			writeCorpusFile(t, root, path, "# Operations\n\nRestart the service after checking its readiness.\n")
			engine := newRootEngine(t, root, t.TempDir(), server.URL)
			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatal(err)
			}
			result, err := engine.DocumentContext(context.Background(), path, ContextOptions{})
			if err != nil {
				t.Fatalf("read indexed Markdown: %v", err)
			}
			if len(result.Sections) != 1 || !strings.Contains(result.Sections[0].Text, "Restart the service after checking its readiness.") {
				t.Fatalf("indexed context = %+v", result)
			}
		})
	}
}
