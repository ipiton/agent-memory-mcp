package vectorstore

import (
	"path/filepath"
	"slices"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// T175: opening the index cost more than the memory cache on some slow starts,
// and only the total could be read from the log.
func TestNewSQLiteStoreLogsStartupSteps(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "vectors.db"), 3, zap.New(core))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var steps []string
	for _, entry := range logs.FilterMessage("Vector store startup step").All() {
		fields := entry.ContextMap()
		if _, ok := fields["elapsed"]; !ok {
			t.Errorf("step %v has no elapsed field", fields["step"])
		}
		steps = append(steps, fields["step"].(string))
	}
	want := []string{"open", "schema", "keyword_prepare", "utf8_sweep", "orphan_sweep", "count"}
	if !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
}
