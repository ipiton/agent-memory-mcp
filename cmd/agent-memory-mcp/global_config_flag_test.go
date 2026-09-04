package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// TestGlobalConfigFlagReachesSubcommands (T129) covers the half of the defect
// that turned out to be worse than reported: --config was stripped inside
// runServe only, so every other subcommand died on "flag provided but not
// defined: -config" — on a machine that runs three banks, that is the flag
// that says which bank to talk to.
func TestGlobalConfigFlagReachesSubcommands(t *testing.T) {
	t.Cleanup(func() { config.SetExplicitConfigPath("") })

	dir := t.TempDir()
	bank := filepath.Join(dir, "bank", "memories.db")
	cfgPath := filepath.Join(dir, "test.env")
	body := "MCP_ROOT=" + dir + "\nMCP_MEMORY_DB_PATH=" + bank + "\nMCP_RAG_ENABLED=false\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// A bank path that exists nowhere else: if --config were ignored, the
	// subcommand would open the discovery-chain bank and this file would
	// never appear.
	var err error
	silenceStdout(t, func() {
		err = run([]string{"list", "--config", cfgPath, "-json"})
	})
	if err != nil {
		t.Fatalf("run list --config: %v", err)
	}
	if _, statErr := os.Stat(bank); statErr != nil {
		t.Fatalf("subcommand did not use the bank from --config (%s): %v", bank, statErr)
	}
	if got := config.ConfigFilePath(); !strings.HasSuffix(got, "test.env") {
		t.Errorf("ConfigFilePath() = %q, want the file passed via --config", got)
	}
}
