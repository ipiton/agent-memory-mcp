package config

import (
	"strings"
	"testing"
	"time"
)

// hermeticDotEnv isolates the dotenv search chain from any config.env present
// on the developer machine. LoadFromEnv walks CWD/.env → XDG → Homebrew prefix,
// and a real /opt/homebrew/etc/agent-memory-mcp/config.env leaks values such as
// LLAMACPP_BASE_URL into these tests — making opt-in/default assertions fail
// locally while passing in the clean CI environment. Pointing every chain
// source at an empty temp dir makes the tests deterministic anywhere.
func hermeticDotEnv(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	t.Chdir(empty)                     // no CWD/.env
	t.Setenv("XDG_CONFIG_HOME", empty) // no {xdg}/agent-memory-mcp/config.env
	t.Setenv("HOMEBREW_PREFIX", empty) // no {prefix}/etc/...; blocks /opt/homebrew fallback
}

func TestLoadFromEnvEmbeddingModeLocalOnly(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_EMBEDDING_MODE", "local-only")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.EmbeddingMode != "local-only" {
		t.Fatalf("EmbeddingMode = %q, want %q", cfg.EmbeddingMode, "local-only")
	}
}

func TestLoadFromEnvEmbeddingTuningDefaults(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.EmbeddingTimeout != 5*time.Second {
		t.Fatalf("EmbeddingTimeout = %s, want 5s", cfg.EmbeddingTimeout)
	}
	if cfg.EmbeddingMaxRetries != 1 {
		t.Fatalf("EmbeddingMaxRetries = %d, want 1", cfg.EmbeddingMaxRetries)
	}

	ec := cfg.EmbedderConfig()
	if ec.Timeout != 5*time.Second {
		t.Fatalf("EmbedderConfig().Timeout = %s, want 5s", ec.Timeout)
	}
	if ec.MaxRetries != 1 {
		t.Fatalf("EmbedderConfig().MaxRetries = %d, want 1", ec.MaxRetries)
	}
}

func TestLoadFromEnvEmbeddingTuningOverrides(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_EMBEDDING_TIMEOUT", "30s")
	t.Setenv("MCP_EMBEDDING_MAX_RETRIES", "3")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.EmbeddingTimeout != 30*time.Second {
		t.Fatalf("EmbeddingTimeout = %s, want 30s", cfg.EmbeddingTimeout)
	}
	if cfg.EmbeddingMaxRetries != 3 {
		t.Fatalf("EmbeddingMaxRetries = %d, want 3", cfg.EmbeddingMaxRetries)
	}

	ec := cfg.EmbedderConfig()
	if ec.Timeout != 30*time.Second {
		t.Fatalf("EmbedderConfig().Timeout = %s, want 30s", ec.Timeout)
	}
	if ec.MaxRetries != 3 {
		t.Fatalf("EmbedderConfig().MaxRetries = %d, want 3", ec.MaxRetries)
	}
}

func TestLoadFromEnvEmbeddingTimeoutGarbageFallsBack(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")
	// Durations are read as strings (EnvOrDefault) and parsed later, so garbage
	// still falls back — M13 fail-fast covers only numeric/bool env vars.
	t.Setenv("MCP_EMBEDDING_TIMEOUT", "garbage")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.EmbeddingTimeout != 5*time.Second {
		t.Fatalf("EmbeddingTimeout = %s, want fallback 5s", cfg.EmbeddingTimeout)
	}
}

// TestLoadFromEnvRejectsUnparseableInt is the M13 payoff: a numeric env var set
// to garbage (typo) fails config load instead of silently defaulting, which
// used to mask e.g. a mistyped MCP_HTTP_PORT and bind the wrong port.
func TestLoadFromEnvRejectsUnparseableInt(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_EMBEDDING_MAX_RETRIES", "not-a-number")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv: expected error for unparseable MCP_EMBEDDING_MAX_RETRIES, got nil")
	}
	if !strings.Contains(err.Error(), "MCP_EMBEDDING_MAX_RETRIES") {
		t.Fatalf("error should name the offending key, got: %v", err)
	}
}

// TestLoadFromEnvRejectsUnparseableBool covers the bool branch of the same
// fail-fast contract (M13).
func TestLoadFromEnvRejectsUnparseableBool(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_STEWARD_ENABLED", "treu") // typo for "true"

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv: expected error for unparseable MCP_STEWARD_ENABLED, got nil")
	}
	if !strings.Contains(err.Error(), "MCP_STEWARD_ENABLED") {
		t.Fatalf("error should name the offending key, got: %v", err)
	}
}

func TestLoadFromEnvLlamaCPPDisabledByDefault(t *testing.T) {
	hermeticDotEnv(t)
	t.Setenv("MCP_ROOT", ".")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.LlamaCPPBaseURL != "" {
		t.Fatalf("LlamaCPPBaseURL = %q, want empty (opt-in)", cfg.LlamaCPPBaseURL)
	}
	// Model still has a default so the adapter has a value once enabled.
	if cfg.LlamaCPPModel != "bge-m3" {
		t.Fatalf("LlamaCPPModel = %q, want bge-m3", cfg.LlamaCPPModel)
	}
	if ec := cfg.EmbedderConfig(); ec.LlamaCPPBaseURL != "" {
		t.Fatalf("EmbedderConfig().LlamaCPPBaseURL = %q, want empty", ec.LlamaCPPBaseURL)
	}
}

func TestLoadFromEnvLlamaCPPLocalOnlyOverrides(t *testing.T) {
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_EMBEDDING_MODE", "local-only")
	t.Setenv("LLAMACPP_BASE_URL", "http://127.0.0.1:8080/v1")
	t.Setenv("LLAMACPP_EMBEDDING_MODEL", "nomic-embed-text")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.EmbeddingMode != "local-only" {
		t.Fatalf("EmbeddingMode = %q, want local-only", cfg.EmbeddingMode)
	}
	ec := cfg.EmbedderConfig()
	if ec.LlamaCPPBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("EmbedderConfig().LlamaCPPBaseURL = %q", ec.LlamaCPPBaseURL)
	}
	if ec.LlamaCPPModel != "nomic-embed-text" {
		t.Fatalf("EmbedderConfig().LlamaCPPModel = %q, want nomic-embed-text", ec.LlamaCPPModel)
	}
}

func TestLoadFromEnvHTTPDefaults(t *testing.T) {
	t.Setenv("MCP_ROOT", ".")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.HTTPHost != "127.0.0.1" {
		t.Fatalf("HTTPHost = %q, want %q", cfg.HTTPHost, "127.0.0.1")
	}
	if cfg.HTTPPort != 18080 {
		t.Fatalf("HTTPPort = %d, want %d", cfg.HTTPPort, 18080)
	}
	if cfg.HTTPInsecureAllowUnauthenticated {
		t.Fatal("HTTPInsecureAllowUnauthenticated = true, want false")
	}
	if !cfg.SessionTrackingEnabled {
		t.Fatal("SessionTrackingEnabled = false, want true")
	}
	if cfg.SessionIdleTimeout != 10*time.Minute {
		t.Fatalf("SessionIdleTimeout = %s, want 10m", cfg.SessionIdleTimeout)
	}
	if cfg.SessionCheckpointInterval != 30*time.Minute {
		t.Fatalf("SessionCheckpointInterval = %s, want 30m", cfg.SessionCheckpointInterval)
	}
	if cfg.SessionMinEvents != 2 {
		t.Fatalf("SessionMinEvents = %d, want 2", cfg.SessionMinEvents)
	}
}

func TestLoadFromEnvHTTPOverrides(t *testing.T) {
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_HTTP_HOST", "0.0.0.0")
	t.Setenv("MCP_HTTP_PORT", "28080")
	t.Setenv("MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.HTTPHost != "0.0.0.0" {
		t.Fatalf("HTTPHost = %q, want %q", cfg.HTTPHost, "0.0.0.0")
	}
	if cfg.HTTPPort != 28080 {
		t.Fatalf("HTTPPort = %d, want %d", cfg.HTTPPort, 28080)
	}
	if !cfg.HTTPInsecureAllowUnauthenticated {
		t.Fatal("HTTPInsecureAllowUnauthenticated = false, want true")
	}
}

func TestLoadFromEnvSessionTrackingOverrides(t *testing.T) {
	t.Setenv("MCP_ROOT", ".")
	t.Setenv("MCP_SESSION_TRACKING_ENABLED", "false")
	t.Setenv("MCP_SESSION_IDLE_TIMEOUT", "2m")
	t.Setenv("MCP_SESSION_CHECKPOINT_INTERVAL", "5m")
	t.Setenv("MCP_SESSION_MIN_EVENTS", "4")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.SessionTrackingEnabled {
		t.Fatal("SessionTrackingEnabled = true, want false")
	}
	if cfg.SessionIdleTimeout != 2*time.Minute {
		t.Fatalf("SessionIdleTimeout = %s, want 2m", cfg.SessionIdleTimeout)
	}
	if cfg.SessionCheckpointInterval != 5*time.Minute {
		t.Fatalf("SessionCheckpointInterval = %s, want 5m", cfg.SessionCheckpointInterval)
	}
	if cfg.SessionMinEvents != 4 {
		t.Fatalf("SessionMinEvents = %d, want 4", cfg.SessionMinEvents)
	}
}
