package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvSetsMissingValues(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Unsetenv("MCP_ROOT")
		_ = os.Unsetenv("MCP_DATA_PATH")
		_ = os.Unsetenv("OPENAI_BASE_URL")
	})

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
