package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateResolvedConfig(cfg Config) error {
	if cfg.RAG.ChunkSize <= 0 {
		return fmt.Errorf("MCP_CHUNK_SIZE must be greater than 0")
	}
	if cfg.RAG.ChunkOverlap < 0 {
		return fmt.Errorf("MCP_CHUNK_OVERLAP must be 0 or greater")
	}
	if cfg.RAG.ChunkOverlap >= cfg.RAG.ChunkSize {
		return fmt.Errorf("MCP_CHUNK_OVERLAP (%d) must be smaller than MCP_CHUNK_SIZE (%d)", cfg.RAG.ChunkOverlap, cfg.RAG.ChunkSize)
	}
	if cfg.Session.MinEvents <= 0 {
		return fmt.Errorf("MCP_SESSION_MIN_EVENTS must be greater than 0")
	}
	if cfg.Session.IdleTimeout < 0 {
		return fmt.Errorf("MCP_SESSION_IDLE_TIMEOUT must be 0 or greater")
	}
	if cfg.Session.CheckpointInterval < 0 {
		return fmt.Errorf("MCP_SESSION_CHECKPOINT_INTERVAL must be 0 or greater")
	}
	if cfg.Sediment.ScheduleInterval < 0 {
		return fmt.Errorf("MCP_SEDIMENT_SCHEDULE_INTERVAL must be 0 or greater")
	}
	if cfg.Embeddings.Timeout < 0 {
		return fmt.Errorf("MCP_EMBEDDING_TIMEOUT must be 0 or greater")
	}
	if cfg.Embeddings.MaxRetries < 0 {
		return fmt.Errorf("MCP_EMBEDDING_MAX_RETRIES must be 0 or greater")
	}
	if err := validateAllowedPaths(cfg.RootPath, cfg.AllowedPaths); err != nil {
		return err
	}
	return nil
}

func validateAllowedPaths(root string, allowed []string) error {
	for _, rel := range allowed {
		if filepath.IsAbs(rel) {
			return fmt.Errorf("MCP_ALLOW_DIRS must contain repo-relative paths only: %s", rel)
		}

		clean := filepath.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("MCP_ALLOW_DIRS path escapes MCP_ROOT: %s", rel)
		}

		abs := filepath.Clean(filepath.Join(root, clean))
		relToRoot, err := filepath.Rel(root, abs)
		if err != nil {
			return fmt.Errorf("failed to validate MCP_ALLOW_DIRS path %q: %w", rel, err)
		}
		if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			return fmt.Errorf("MCP_ALLOW_DIRS path escapes MCP_ROOT: %s", rel)
		}
	}
	return nil
}
