package steward

import (
	"context"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// TestExtractPathRefsRejectsNonPaths locks the T86 fix: the regex must not
// promote prose (extension lists, bare dotted tokens) into path candidates,
// otherwise drift_scan emits source_missing (mark_outdated) on healthy memory.
func TestExtractPathRefsRejectsNonPaths(t *testing.T) {
	rejected := []struct {
		name    string
		content string
	}{
		{"extension pair from prose", "Правило: фильтровать `.sh`/`.py` перед запуском"},
		{"extension list", "Скрипты `.sh/.bash/.zsh/.py` игнорируются"},
		{"bare dotted token", "Билд пишет манифест в `.build-manifest.json` при старте"},
		{"bare slashless dir word", "Смотри foo/bar без repo-root"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if refs := extractPathRefs(tc.content); len(refs) != 0 {
				t.Errorf("expected no path refs, got %v", refs)
			}
		})
	}

	accepted := []struct {
		name    string
		content string
		want    string
	}{
		{"relative path", "Config in ./nonexistent/config.yaml sets timeout", "./nonexistent/config.yaml"},
		{"absolute path", "See /etc/app/config.json for defaults", "/etc/app/config.json"},
		{"hidden dir path", "CI lives in .github/workflows/ci.yml here", ".github/workflows/ci.yml"},
		{"bare-ext segment plus real file", "Handler in ./sh/main.go does work", "./sh/main.go"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			refs := extractPathRefs(tc.content)
			if len(refs) != 1 || refs[0] != tc.want {
				t.Errorf("expected [%s], got %v", tc.want, refs)
			}
		})
	}
}

// TestDriftScanNoFalsePositivePaths verifies the end-to-end verdict: content
// whose only "paths" are prose traps produces zero source_missing findings even
// against an empty root.
func TestDriftScanNoFalsePositivePaths(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	if err := store.Store(ctx, &memory.Memory{
		Content:    "Паттерн: фильтровать `.sh`/`.py`, писать `.build-manifest.json`. Живой canonical факт.",
		Type:       memory.TypeSemantic,
		Title:      "Filter extensions",
		Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := svc.DriftScan(ctx, DriftScanParams{RootPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range result.Findings {
		if f.DriftType == DriftSourceMissing {
			t.Errorf("unexpected source_missing FP: %+v", f)
		}
	}
}
