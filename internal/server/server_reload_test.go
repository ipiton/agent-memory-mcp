package server

import (
	"sync"
	"testing"
)

// TestApplyReloadConcurrent exercises the Round 3 M15 reload barrier: the config
// watcher and the SIGHUP handler can both trigger a reload at the same time.
// Firing many reloads concurrently must serialize cleanly (no data race, no torn
// config). Run under -race to catch regressions.
func TestApplyReloadConcurrent(t *testing.T) {
	s := newTestServer(t, "")
	base := s.config

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := base
			cfg.MaxSearchResults = 10 + i
			s.ApplyReload(cfg)
		}(i)
	}
	wg.Wait()

	// After all reloads a consistent snapshot must be readable (one applied
	// value, not a torn write).
	s.ragMu.RLock()
	got := s.config.MaxSearchResults
	s.ragMu.RUnlock()
	if got < 10 || got > 29 {
		t.Fatalf("config.MaxSearchResults = %d, want a single applied value in [10,29]", got)
	}
}
