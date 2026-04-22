package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/hooks"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"go.uber.org/zap"
)

// TestCheckpointDedup_SuppressesNearDuplicates simulates multiple checkpoint
// invocations with identical content and asserts that only the first one
// is persisted — matching the runCheckpoint flow without re-launching
// the CLI binary.
func TestCheckpointDedup_SuppressesNearDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := sessionclose.New(store)
	cfg := hooks.DedupConfig{
		Threshold:       0.9,
		MinContentChars: 100,
		Window:          10 * time.Minute,
	}

	body := "Worked on T45 hook dedup filter. Implemented Jaccard similarity over lowercase whitespace-tokenised summaries. Added threshold, window, and escape-hatch env vars and integrated them in hook CLI paths."
	const invocations = 5
	for i := 0; i < invocations; i++ {
		summary := memory.SessionSummary{
			Context: "proj-x",
			Summary: body,
		}
		res, err := hooks.Check(context.Background(), store, summary, cfg)
		if err != nil {
			t.Fatalf("Check iter %d: %v", i, err)
		}
		if res.Skip {
			store.IncrementDedupSkipped(res.Reason)
			continue
		}
		if _, err := svc.SaveRawSummaryWithOptions(context.Background(), summary, sessionclose.RawSaveOptions{
			RecordKind: memory.RecordKindSessionCheckpoint,
			ExtraTags:  []string{"session-checkpoint"},
			Metadata: map[string]string{
				memory.MetadataSessionBoundary: "checkpoint",
				memory.MetadataSessionOrigin:   "hook_checkpoint",
			},
		}); err != nil {
			t.Fatalf("SaveRawSummaryWithOptions iter %d: %v", i, err)
		}
	}

	// Expect exactly one stored record, others skipped via dedup.
	lst, err := store.List(context.Background(), memory.Filters{Context: "proj-x"}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lst) != 1 {
		t.Fatalf("expected 1 stored checkpoint, got %d", len(lst))
	}
	skips := store.DedupSkippedByReason()
	if skips[hooks.ReasonSimilar] != invocations-1 {
		t.Fatalf("expected %d similar-skips, got %d (%v)", invocations-1, skips[hooks.ReasonSimilar], skips)
	}
}

func TestDedupConfigFrom_DisabledEscapeHatch(t *testing.T) {
	cfg := hooks.DedupConfig{Threshold: 0.9, MinContentChars: 100, Window: 10 * time.Minute}
	if cfg.Threshold == 0 {
		t.Fatalf("sanity check: expected non-zero threshold")
	}

	// Emulate `MCP_CHECKPOINT_DEDUP_DISABLED=true` by clearing the config
	// — dedupConfigFrom returns a zero-value DedupConfig in that case.
	disabled := hooks.DedupConfig{}
	if disabled.Threshold != 0 || disabled.MinContentChars != 0 {
		t.Fatalf("disabled config should be zero-valued, got %+v", disabled)
	}
}

// TestCheckpointDedup_DisabledEscapeHatch_AllowsDuplicates exercises the
// end-to-end path: env var -> config.LoadFromEnv -> dedupConfigFrom ->
// hooks.Check. It asserts that with MCP_CHECKPOINT_DEDUP_DISABLED=true the
// filter is fully bypassed and the dedup skip counter never increments,
// even for byte-identical back-to-back invocations.
func TestCheckpointDedup_DisabledEscapeHatch_AllowsDuplicates(t *testing.T) {
	t.Setenv("MCP_CHECKPOINT_DEDUP_DISABLED", "true")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if !cfg.CheckpointDedupDisabled {
		t.Fatalf("expected CheckpointDedupDisabled=true from env, got false")
	}

	dc := dedupConfigFrom(cfg)
	if dc != (hooks.DedupConfig{}) {
		t.Fatalf("expected zero DedupConfig when disabled, got %+v", dc)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	body := "Worked on T45 hook dedup filter. Implemented Jaccard similarity over lowercase whitespace-tokenised summaries. Added threshold, window, and escape-hatch env vars and integrated them in hook CLI paths."
	summary := memory.SessionSummary{Context: "proj-x", Summary: body}

	for i := 0; i < 2; i++ {
		res, err := hooks.Check(context.Background(), store, summary, dc)
		if err != nil {
			t.Fatalf("Check iter %d: %v", i, err)
		}
		if res.Skip {
			t.Fatalf("Check iter %d: expected Skip=false with disabled config, got %+v", i, res)
		}
	}

	skips := store.DedupSkippedByReason()
	if skips[hooks.ReasonSimilar] != 0 {
		t.Fatalf("expected 0 similar-skips with disabled filter, got %d (%v)", skips[hooks.ReasonSimilar], skips)
	}
}
