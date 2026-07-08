package config

import (
	"fmt"
	"sync"
	"testing"
)

// TestConfigPathConcurrentAccess covers Round 3 M14: the config-path globals are
// written at startup but read at runtime from the SIGHUP and watcher goroutines.
// Hammering the setters and getters concurrently must be race-free — run under
// -race to catch a regression that drops the guarding mutex.
func TestConfigPathConcurrentAccess(t *testing.T) {
	t.Cleanup(func() { SetExplicitConfigPath("") })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			SetExplicitConfigPath(fmt.Sprintf("/tmp/cfg-%d.env", i))
			_ = ConfigFilePath()
			_ = getExplicitConfigPath()
			setResolvedConfigPath(fmt.Sprintf("/tmp/resolved-%d.env", i))
		}(i)
	}
	wg.Wait()
}
