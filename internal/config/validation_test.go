package config

import (
	"strings"
	"testing"
)

func TestResolvePathsRejectsInvalidChunkConfig(t *testing.T) {
	_, err := resolvePaths(envValues{
		root:         ".",
		chunkSize:    200,
		chunkOverlap: 200,
	})
	if err == nil || !strings.Contains(err.Error(), "MCP_CHUNK_OVERLAP") {
		t.Fatalf("resolvePaths error = %v, want chunk overlap validation", err)
	}
}

func TestResolvePathsRejectsAllowlistOutsideRoot(t *testing.T) {
	_, err := resolvePaths(envValues{
		root:         ".",
		allow:        "../secrets",
		chunkSize:    2000,
		chunkOverlap: 200,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes MCP_ROOT") {
		t.Fatalf("resolvePaths error = %v, want allowlist validation", err)
	}
}

func TestResolvePathsRejectsAbsoluteAllowlist(t *testing.T) {
	_, err := resolvePaths(envValues{
		root:         ".",
		allow:        "/tmp",
		chunkSize:    2000,
		chunkOverlap: 200,
	})
	if err == nil || !strings.Contains(err.Error(), "repo-relative") {
		t.Fatalf("resolvePaths error = %v, want absolute allowlist validation", err)
	}
}
