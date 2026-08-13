package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The .env loader no longer exports into the process environment (T89 H5/M14),
// so these tests assert on the parsed map and on the precedence rule that
// replaced the old "skip keys already set" behaviour: the real environment
// still wins, the file fills the rest.

func TestParseDotEnvFileReadsValues(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "MCP_ROOT=.\nMCP_DATA_PATH=.agent-memory\nOPENAI_BASE_URL=\"https://example.test/v1\"\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	values, err := parseDotEnvFile(envFile)
	if err != nil {
		t.Fatalf("parseDotEnvFile: %v", err)
	}

	want := map[string]string{
		"MCP_ROOT":        ".",
		"MCP_DATA_PATH":   ".agent-memory",
		"OPENAI_BASE_URL": "https://example.test/v1",
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("values[%q] = %q, want %q", k, values[k], v)
		}
	}
}

func TestParseDotEnvFileMissingFileIsNotAnError(t *testing.T) {
	values, err := parseDotEnvFile(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("parseDotEnvFile on a missing file: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("values = %v, want empty", values)
	}
}

// The real environment outranks the file — this is what keeps `sops exec-env`
// and launchd overrides authoritative.
func TestEnvOverlayPrefersProcessEnvironment(t *testing.T) {
	t.Setenv("MCP_DATA_PATH", "custom-data")

	s := &envScan{dotenv: map[string]string{"MCP_DATA_PATH": ".agent-memory", "MCP_ROOT": "from-file"}}

	if got := s.raw("MCP_DATA_PATH"); got != "custom-data" {
		t.Errorf("raw(MCP_DATA_PATH) = %q, want the process environment value", got)
	}
	if got := s.raw("MCP_ROOT"); got != "from-file" {
		t.Errorf("raw(MCP_ROOT) = %q, want the file value when the environment is silent", got)
	}
	if got := s.String("MCP_ABSENT", "fallback"); got != "fallback" {
		t.Errorf("String(MCP_ABSENT) = %q, want the fallback", got)
	}
}

// T89 H5, the core regression: a second load must observe the file's current
// contents. The old loader exported to the process environment and skipped
// already-set keys, so every key was "already set" by the time a reload ran and
// the reload silently returned the original values.
func TestReloadObservesEditedFile(t *testing.T) {
	t.Setenv("MCP_INDEX_DIRS", "")

	envFile := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(envFile, []byte("MCP_INDEX_DIRS=docs\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	first, err := LoadFromFile(envFile)
	if err != nil {
		t.Fatalf("LoadFromFile (first): %v", err)
	}
	if len(first.RAG.IndexDirs) != 1 || first.RAG.IndexDirs[0] != "docs" {
		t.Fatalf("first load index dirs = %v, want [docs]", first.RAG.IndexDirs)
	}

	if err := os.WriteFile(envFile, []byte("MCP_INDEX_DIRS=docs,memory-bank\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	second, err := LoadFromFile(envFile)
	if err != nil {
		t.Fatalf("LoadFromFile (second): %v", err)
	}
	if len(second.RAG.IndexDirs) != 2 {
		t.Fatalf("reload index dirs = %v, want the edited pair — the reload did not re-read the file", second.RAG.IndexDirs)
	}
}

func TestLoadDotEnvFilesExplicitPath(t *testing.T) {
	t.Cleanup(func() { resolvedConfigPath = "" })

	explicit := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(explicit, []byte("TEST_EXPLICIT_VAR=from-explicit\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	values, err := loadDotEnvFiles(explicit)
	if err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	if got := values["TEST_EXPLICIT_VAR"]; got != "from-explicit" {
		t.Fatalf("TEST_EXPLICIT_VAR = %q, want %q", got, "from-explicit")
	}
	if got := ConfigFilePath(); got != explicit {
		t.Fatalf("ConfigFilePath() = %q, want %q", got, explicit)
	}
}

func TestLoadDotEnvFilesXDGFallback(t *testing.T) {
	t.Cleanup(func() { resolvedConfigPath = "" })

	base := t.TempDir()
	xdgDir := filepath.Join(base, "xdg-config", configAppName)
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	xdgFile := filepath.Join(xdgDir, "config.env")
	if err := os.WriteFile(xdgFile, []byte("TEST_XDG_VAR=from-xdg\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg-config"))

	values, err := loadDotEnvFiles("")
	if err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	if got := values["TEST_XDG_VAR"]; got != "from-xdg" {
		t.Fatalf("TEST_XDG_VAR = %q, want %q", got, "from-xdg")
	}
}

func TestLoadDotEnvFilesChainDoesNotOverride(t *testing.T) {
	t.Cleanup(func() { resolvedConfigPath = "" })

	cwdDir := t.TempDir()
	cwdEnv := filepath.Join(cwdDir, ".env")
	if err := os.WriteFile(cwdEnv, []byte("TEST_CHAIN_VAR=from-cwd\n"), 0o644); err != nil {
		t.Fatalf("write cwd .env: %v", err)
	}

	xdgBase := t.TempDir()
	xdgDir := filepath.Join(xdgBase, "xdg-config", configAppName)
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdgDir, "config.env"), []byte("TEST_CHAIN_VAR=from-xdg\n"), 0o644); err != nil {
		t.Fatalf("write xdg: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdgBase, "xdg-config"))

	origDir, _ := os.Getwd()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	values, err := loadDotEnvFiles("")
	if err != nil {
		t.Fatalf("loadDotEnvFiles: %v", err)
	}

	if got := values["TEST_CHAIN_VAR"]; got != "from-cwd" {
		t.Fatalf("TEST_CHAIN_VAR = %q, want %q (the earlier file in the chain wins)", got, "from-cwd")
	}
}

// T89 H5 side effect: explicitness of MCP_STEWARD_ENABLED must be detected in
// the .env file too, otherwise steward auto-enables in HTTP mode against an
// explicit false set in config.env.
func TestStewardExplicitFalseInFileIsRespected(t *testing.T) {
	t.Setenv("MCP_STEWARD_ENABLED", "")
	t.Setenv("MCP_HTTP_MODE", "")
	t.Setenv("MCP_MEMORY_ENABLED", "")

	envFile := filepath.Join(t.TempDir(), "config.env")
	content := "MCP_STEWARD_ENABLED=false\nMCP_HTTP_MODE=http\nMCP_MEMORY_ENABLED=true\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFromFile(envFile)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if cfg.Steward.Enabled {
		t.Fatal("steward enabled despite MCP_STEWARD_ENABLED=false in the config file")
	}
}
