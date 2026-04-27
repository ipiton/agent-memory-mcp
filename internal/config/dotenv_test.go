package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvSetsMissingValues(t *testing.T) {
	// Hermetic setup: clear vars that may be set in the developer's shell
	// (homebrew install of agent-memory-mcp pre-sets MCP_DATA_PATH, for example).
	// loadDotEnv only fills missing values, so a pre-set var would block .env.
	t.Setenv("MCP_ROOT", "")
	t.Setenv("MCP_DATA_PATH", "")
	t.Setenv("OPENAI_BASE_URL", "")

	envFile := filepath.Join(t.TempDir(), ".env")
	content := "MCP_ROOT=.\nMCP_DATA_PATH=.agent-memory\nOPENAI_BASE_URL=\"https://example.test/v1\"\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if err := loadDotEnv(envFile); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}

	if got := os.Getenv("MCP_ROOT"); got != "." {
		t.Fatalf("MCP_ROOT = %q, want %q", got, ".")
	}
	if got := os.Getenv("MCP_DATA_PATH"); got != ".agent-memory" {
		t.Fatalf("MCP_DATA_PATH = %q, want %q", got, ".agent-memory")
	}
	if got := os.Getenv("OPENAI_BASE_URL"); got != "https://example.test/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q, want %q", got, "https://example.test/v1")
	}
}

func TestLoadDotEnvPreservesExistingEnv(t *testing.T) {
	t.Setenv("MCP_DATA_PATH", "custom-data")

	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("MCP_DATA_PATH=.agent-memory\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if err := loadDotEnv(envFile); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}

	if got := os.Getenv("MCP_DATA_PATH"); got != "custom-data" {
		t.Fatalf("MCP_DATA_PATH = %q, want %q", got, "custom-data")
	}
}

func TestLoadDotEnvFilesExplicitPath(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Unsetenv("TEST_EXPLICIT_VAR")
		resolvedConfigPath = ""
	})

	explicit := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(explicit, []byte("TEST_EXPLICIT_VAR=from-explicit\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := loadDotEnvFiles(explicit); err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	if got := os.Getenv("TEST_EXPLICIT_VAR"); got != "from-explicit" {
		t.Fatalf("TEST_EXPLICIT_VAR = %q, want %q", got, "from-explicit")
	}
	if got := ConfigFilePath(); got != explicit {
		t.Fatalf("ConfigFilePath() = %q, want %q", got, explicit)
	}
}

func TestLoadDotEnvFilesXDGFallback(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Unsetenv("TEST_XDG_VAR")
		resolvedConfigPath = ""
	})

	base := t.TempDir()

	// Create XDG config dir with config.env
	xdgDir := filepath.Join(base, "xdg-config", configAppName)
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	xdgFile := filepath.Join(xdgDir, "config.env")
	if err := os.WriteFile(xdgFile, []byte("TEST_XDG_VAR=from-xdg\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg-config"))

	if err := loadDotEnvFiles(""); err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	if got := os.Getenv("TEST_XDG_VAR"); got != "from-xdg" {
		t.Fatalf("TEST_XDG_VAR = %q, want %q", got, "from-xdg")
	}
}

func TestLoadDotEnvFilesChainDoesNotOverride(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Unsetenv("TEST_CHAIN_VAR")
		resolvedConfigPath = ""
	})

	// CWD .env sets the value
	cwdDir := t.TempDir()
	cwdEnv := filepath.Join(cwdDir, ".env")
	if err := os.WriteFile(cwdEnv, []byte("TEST_CHAIN_VAR=from-cwd\n"), 0o644); err != nil {
		t.Fatalf("write cwd .env: %v", err)
	}

	// XDG config tries to set a different value
	xdgDir := filepath.Join(t.TempDir(), "xdg-config", configAppName)
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdgDir, "config.env"), []byte("TEST_CHAIN_VAR=from-xdg\n"), 0o644); err != nil {
		t.Fatalf("write xdg: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))

	// Change to CWD so the chain picks up CWD/.env first
	origDir, _ := os.Getwd()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if err := loadDotEnvFiles(""); err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	// CWD value wins — XDG does not override
	if got := os.Getenv("TEST_CHAIN_VAR"); got != "from-cwd" {
		t.Fatalf("TEST_CHAIN_VAR = %q, want %q (chain should not override)", got, "from-cwd")
	}
}
