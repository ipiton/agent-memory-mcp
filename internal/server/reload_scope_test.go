package server

import (
	"slices"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// T91 L12. A reload that cannot apply a change must say so. Silence here reads
// as success, which is exactly how an operator ends up debugging a setting they
// believe they already changed.
func TestRestartRequiredChangesNamesUnappliedSettings(t *testing.T) {
	base := config.Config{}

	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"session idle timeout", func(c *config.Config) { c.Session.IdleTimeout = time.Hour }, "MCP_SESSION_IDLE_TIMEOUT"},
		{"sweep interval", func(c *config.Config) { c.Lifecycle.ArchiveSweepInterval = time.Hour }, "MCP_ARCHIVE_SWEEP_INTERVAL"},
		{"sediment toggle", func(c *config.Config) { c.Sediment.Enabled = true }, "MCP_SEDIMENT_ENABLED"},
		{"steward toggle", func(c *config.Config) { c.Steward.Enabled = true }, "MCP_STEWARD_ENABLED"},
		{"http port", func(c *config.Config) { c.HTTP.Port = 9999 }, "MCP_HTTP_PORT"},
		{"db path", func(c *config.Config) { c.Memory.DBPath = "/tmp/other.db" }, "MCP_MEMORY_DB_PATH"},
		{"root path", func(c *config.Config) { c.RootPath = "/tmp/other" }, "MCP_ROOT (path guard)"},
		{"tool grouping", func(c *config.Config) { c.ToolGrouping = true }, "MCP_TOOL_GROUPING"},
		{"review-queue suppression", func(c *config.Config) { c.Session.SuppressReviewQueueWrites = true }, "SEMA_MCP_SUPPRESS_REVIEW_QUEUE_WRITES"},
	}

	for _, tc := range cases {
		next := base
		tc.mutate(&next)
		got := restartRequiredChanges(base, next)
		if !slices.Contains(got, tc.want) {
			t.Errorf("%s: changes = %v, want it to name %q", tc.name, got, tc.want)
		}
	}
}

// Settings the reload does apply must not be reported as needing a restart —
// a warning that cries wolf on every RAG edit would be ignored within a week.
func TestRestartRequiredChangesIgnoresAppliedSettings(t *testing.T) {
	base := config.Config{}

	next := base
	next.RAG.Enabled = true
	next.RAG.IndexDirs = []string{"docs", "memory-bank"}
	next.RAG.ChunkSize = 4000
	next.Lifecycle.TaskArchiveRoots = []string{"/tmp/archive"}

	if got := restartRequiredChanges(base, next); len(got) != 0 {
		t.Fatalf("changes = %v, want none — these are applied by the reload", got)
	}
}

func TestRestartRequiredChangesEmptyForIdenticalConfig(t *testing.T) {
	cfg := config.Config{
		RootPath: "/tmp/x",
		Session:  config.SessionConfig{IdleTimeout: time.Minute},
		HTTP:     config.HTTPConfig{Port: 18080},
	}
	if got := restartRequiredChanges(cfg, cfg); len(got) != 0 {
		t.Fatalf("changes = %v, want none for an unchanged config", got)
	}
}
