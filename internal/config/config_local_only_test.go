package config

import (
	"testing"
	"time"
)

func TestLoadFromEnvEmbeddingModeLocalOnly(t *testing.T) {
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

func TestLoadFromEnvEmbeddingTimeoutDefaults(t *testing.T) {
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

func TestLoadFromEnvEmbeddingTimeoutOverrides(t *testing.T) {
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
