package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestMemoryReadyError(t *testing.T) {
	if err := memoryReadyError(nil); err != nil {
		t.Errorf("ready store: %v", err)
	}

	loading := memoryReadyError(fmt.Errorf("wait: %w", memory.ErrCacheLoading))
	data, ok := loading.Data.(map[string]any)
	if !ok || data["status"] != "starting" || data["retry_after_seconds"] != 5 {
		t.Errorf("loading store answered %+v, want the retryable starting error", loading)
	}

	failed := memoryReadyError(errors.New("disk I/O error"))
	if failed == nil || failed.Message != "Memory store not available" || failed.Data != "disk I/O error" {
		t.Errorf("failed load answered %+v", failed)
	}
}
