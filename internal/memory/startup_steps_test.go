package memory

import (
	"context"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// T175: a start that took 11 minutes under memory pressure left no trace of
// which part was slow. NewStore names each step it finishes, in order, with
// its duration, and the cache step says how many records it loaded.
func TestNewStoreLogsStartupSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")

	seed, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := seed.Store(context.Background(), &Memory{Content: "one", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	_ = seed.Close()

	core, logs := observer.New(zapcore.InfoLevel)
	store, err := NewStore(path, nil, zap.New(core))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var steps []string
	for _, entry := range logs.FilterMessage("Memory store startup step").All() {
		fields := entry.ContextMap()
		if _, ok := fields["elapsed"]; !ok {
			t.Errorf("step %v has no elapsed field", fields["step"])
		}
		steps = append(steps, fields["step"].(string))
		if fields["step"] == "cache_load" && fields["memories"] != int64(1) {
			t.Errorf("cache_load memories = %v, want 1", fields["memories"])
		}
	}
	want := []string{"open", "schema", "utf8_repair", "merged_duplicates_backfill", "cache_load"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
}
