package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathGuardResolve(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.md"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("write docs: %v", err)
	}

	cfg := Config{
		RootPath:     root,
		AllowedPaths: []string{"AGENTS.md", "docs"},
	}
	guard, err := NewPathGuard(cfg)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	if _, err := guard.Resolve("docs/readme.md"); err != nil {
		t.Fatalf("resolve docs: %v", err)
	}

	if _, err := guard.Resolve("../secrets.txt"); err == nil {
		t.Fatalf("expected traversal error")
	}

	if _, err := guard.Resolve("AGENTS.md"); err != nil {
		t.Fatalf("resolve AGENTS: %v", err)
	}

	if _, err := guard.Resolve("AGENTS.md/extra"); err == nil {
		t.Fatalf("expected file path to be rejected")
	}
}
