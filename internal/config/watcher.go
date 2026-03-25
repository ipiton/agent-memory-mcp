package config

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Watcher monitors a config file for changes and calls onChange when the file is modified.
type Watcher struct {
	path     string
	interval time.Duration
	onChange func(old, new Config)
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewWatcher creates a config file watcher that polls the file at the given interval.
// The onChange callback is called only when the file changes and produces a different config.
func NewWatcher(path string, interval time.Duration, onChange func(old, new Config)) *Watcher {
	return &Watcher{
		path:     path,
		interval: interval,
		onChange: onChange,
		stop:     make(chan struct{}),
	}
}

// Start begins watching in a background goroutine.
func (w *Watcher) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run()
	}()
}

// Stop signals the watcher to stop and waits for the goroutine to exit.
func (w *Watcher) Stop() {
	close(w.stop)
	w.wg.Wait()
}

func (w *Watcher) run() {
	var lastMtime time.Time
	if info, err := os.Stat(w.path); err == nil {
		lastMtime = info.ModTime()
	}

	// Load initial config snapshot for comparison.
	current, _ := LoadFromFile(w.path)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			info, err := os.Stat(w.path)
			if err != nil {
				continue
			}
			mtime := info.ModTime()
			if !mtime.After(lastMtime) {
				continue
			}
			lastMtime = mtime

			newCfg, err := LoadFromFile(w.path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "config watcher: failed to reload %s: %v\n", w.path, err)
				continue
			}

			if ragConfigChanged(current, newCfg) {
				w.onChange(current, newCfg)
				current = newCfg
			}
		}
	}
}

// LoadFromFile loads config from a specific .env file in isolation.
// It temporarily clears MCP_* env vars, loads the file, reads config, then restores.
func LoadFromFile(path string) (Config, error) {
	if err := loadDotEnv(path); err != nil {
		return Config{}, err
	}
	ev, err := readEnvValues()
	if err != nil {
		return Config{}, err
	}
	return resolvePaths(ev)
}

// ragConfigChanged returns true if any RAG-related config fields differ between old and new.
func ragConfigChanged(old, new Config) bool {
	if old.RAGEnabled != new.RAGEnabled {
		return true
	}
	if old.RootPath != new.RootPath {
		return true
	}
	if old.RAGIndexPath != new.RAGIndexPath {
		return true
	}
	if old.EmbeddingMode != new.EmbeddingMode {
		return true
	}
	if old.JinaAPIKey != new.JinaAPIKey {
		return true
	}
	if old.OpenAIAPIKey != new.OpenAIAPIKey {
		return true
	}
	if old.OpenAIBaseURL != new.OpenAIBaseURL {
		return true
	}
	if old.OpenAIModel != new.OpenAIModel {
		return true
	}
	if old.OllamaBaseURL != new.OllamaBaseURL {
		return true
	}
	if old.ChunkSize != new.ChunkSize {
		return true
	}
	if old.ChunkOverlap != new.ChunkOverlap {
		return true
	}
	if old.AutoIndex != new.AutoIndex {
		return true
	}
	if old.FileWatcher != new.FileWatcher {
		return true
	}
	if old.RedactSecrets != new.RedactSecrets {
		return true
	}
	if !stringSlicesEqual(old.IndexDirs, new.IndexDirs) {
		return true
	}
	if !stringSlicesEqual(old.IndexExcludeDirs, new.IndexExcludeDirs) {
		return true
	}
	if !stringSlicesEqual(old.IndexExcludeGlobs, new.IndexExcludeGlobs) {
		return true
	}
	return false
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
