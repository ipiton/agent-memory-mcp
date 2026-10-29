package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func TestDefaultAllowlistUsesRoot(t *testing.T) {
	root := t.TempDir()
	// Create a file inside root
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// No AllowedPaths configured — should default to root path
	cfg := config.Config{
		RootPath:     root,
		AllowedPaths: nil,
	}
	guard, err := NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	// File inside root should be allowed
	if _, err := guard.Resolve("inside.txt"); err != nil {
		t.Fatalf("resolve inside root should work: %v", err)
	}

	// Path traversal should be blocked
	if _, err := guard.Resolve("../outside.txt"); err == nil {
		t.Fatal("expected traversal to be blocked")
	}

	// Absolute path should be blocked
	if _, err := guard.Resolve("/etc/passwd"); err == nil {
		t.Fatal("expected absolute path to be blocked")
	}
}

func TestGuardResolve(t *testing.T) {
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

	cfg := config.Config{
		RootPath:     root,
		AllowedPaths: []string{"AGENTS.md", "docs"},
	}
	guard, err := NewGuard(cfg)
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

// Корень-симлинк: разрешённые пути раскрываются через EvalSymlinks, поэтому
// и корень должен быть раскрыт, иначе RelPath выдаёт "../<реальный путь>".
func TestGuardSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "docs", "readme.md"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("write docs: %v", err)
	}
	root := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, root); err != nil {
		t.Skipf("symlink: %v", err)
	}

	guard, err := NewGuard(config.Config{RootPath: root, AllowedPaths: []string{"docs"}})
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	abs, err := guard.Resolve("docs/readme.md")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !guard.IsAllowed(abs) {
		t.Fatalf("IsAllowed(%q) = false", abs)
	}
	rel, err := guard.RelPath(abs)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	if rel != "docs/readme.md" {
		t.Fatalf("RelPath = %q, want %q", rel, "docs/readme.md")
	}
	// Путь из выдачи должен снова читаться через Resolve.
	if _, err := guard.Resolve(rel); err != nil {
		t.Fatalf("resolve round-trip: %v", err)
	}
}
