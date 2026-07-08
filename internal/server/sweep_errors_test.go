package server

import (
	"errors"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/lifecycle"
)

// TestMapSweepError_NoRootsTyped (T61) ensures that the typed
// lifecycle.ErrNoRoots failure surfaces as a JSON-RPC invalid-params error
// with an actionable message, instead of the legacy generic -32000 server
// error that masked the root cause.
func TestMapSweepError_NoRootsTyped(t *testing.T) {
	got := mapSweepError("sweep_archive", lifecycle.ErrNoRoots)
	if got == nil {
		t.Fatal("expected typed rpcError, got nil")
	}
	if got.Code != rpcErrInvalidParams {
		t.Fatalf("code = %d, want %d (rpcErrInvalidParams)", got.Code, rpcErrInvalidParams)
	}
	if got.Message != "sweep_archive: archive roots not configured" {
		t.Fatalf("message = %q, want typed message", got.Message)
	}
	data, _ := got.Data.(string)
	if data == "" {
		t.Fatalf("expected Data hint with remediation, got empty")
	}
}

// TestMapSweepError_UnknownFallsBack: unknown errors keep the legacy generic
// server-error envelope so existing client code paths still work.
func TestMapSweepError_UnknownFallsBack(t *testing.T) {
	got := mapSweepError("end_task", errors.New("oh no"))
	if got == nil {
		t.Fatal("expected rpcError, got nil")
	}
	if got.Code != rpcErrServerError {
		t.Fatalf("code = %d, want rpcErrServerError", got.Code)
	}
	if got.Message != "end_task failed" {
		t.Fatalf("message = %q, want legacy envelope", got.Message)
	}
}

// TestReloadConfig_UpdatesNonRAGFields (T61) verifies that ReloadConfig swaps
// in the new TaskArchiveRoots even when RAG is disabled. The previous
// ReloadRAG implementation only updated s.config inside the RAG-enabled
// branch, so SIGHUP could not pick up a freshly added MCP_TASK_ARCHIVE_ROOTS.
func TestReloadConfig_UpdatesNonRAGFields(t *testing.T) {
	srv := newTestServer(t, "")
	if got := len(srv.config.Lifecycle.TaskArchiveRoots); got != 0 {
		t.Fatalf("baseline: TaskArchiveRoots = %v, want empty", srv.config.Lifecycle.TaskArchiveRoots)
	}

	newCfg := srv.config
	newCfg.RAG.Enabled = false // simulate post-install reload before RAG ever spins up
	newCfg.Lifecycle.TaskArchiveRoots = []string{"/tmp/archive-a", "/tmp/archive-b"}

	srv.ReloadConfig(newCfg)

	if got := srv.config.Lifecycle.TaskArchiveRoots; len(got) != 2 || got[0] != "/tmp/archive-a" {
		t.Fatalf("post-reload TaskArchiveRoots = %v, want [/tmp/archive-a /tmp/archive-b]", got)
	}
}

// TestReloadConfig_LegacyAlias confirms ReloadRAG still delegates to
// ReloadConfig so existing external callers keep working through the
// rename window.
func TestReloadConfig_LegacyAlias(t *testing.T) {
	srv := newTestServer(t, "")
	newCfg := srv.config
	newCfg.Lifecycle.TaskArchiveRoots = []string{"/tmp/legacy"}
	srv.ReloadRAG(newCfg)
	if got := srv.config.Lifecycle.TaskArchiveRoots; len(got) != 1 || got[0] != "/tmp/legacy" {
		t.Fatalf("ReloadRAG (legacy alias) did not update config: got %v", got)
	}
}

// _ keeps the config import live in case the test set above is later trimmed
// of fields that reach into the package.
var _ = config.Config{}
