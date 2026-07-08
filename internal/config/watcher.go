package config

import (
	"fmt"
	"os"
	"reflect"
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

// ragFingerprint projects exactly the config fields whose change requires the
// RAG engine to be rebuilt on hot-reload. Round 3 H13 replaced ~17 hand-written
// field comparisons (and a bespoke slice-equality helper) with a single
// reflect.DeepEqual over this projection — the field set (and thus the reload
// semantics) is unchanged; adding a RAG-affecting field means adding it here.
type ragFingerprint struct {
	Enabled           bool
	RootPath          string
	IndexPath         string
	EmbeddingMode     string
	JinaAPIKey        string
	OpenAIAPIKey      string
	OpenAIBaseURL     string
	OpenAIModel       string
	OllamaBaseURL     string
	ChunkSize         int
	ChunkOverlap      int
	AutoIndex         bool
	FileWatcher       bool
	RedactSecrets     bool
	IndexDirs         []string
	IndexExcludeDirs  []string
	IndexExcludeGlobs []string
}

func ragFingerprintOf(c Config) ragFingerprint {
	return ragFingerprint{
		Enabled:           c.RAG.Enabled,
		RootPath:          c.RootPath,
		IndexPath:         c.RAG.IndexPath,
		EmbeddingMode:     c.Embeddings.Mode,
		JinaAPIKey:        c.Embeddings.JinaAPIKey,
		OpenAIAPIKey:      c.Embeddings.OpenAIAPIKey,
		OpenAIBaseURL:     c.Embeddings.OpenAIBaseURL,
		OpenAIModel:       c.Embeddings.OpenAIModel,
		OllamaBaseURL:     c.Embeddings.OllamaBaseURL,
		ChunkSize:         c.RAG.ChunkSize,
		ChunkOverlap:      c.RAG.ChunkOverlap,
		AutoIndex:         c.RAG.AutoIndex,
		FileWatcher:       c.RAG.FileWatcher,
		RedactSecrets:     c.RAG.RedactSecrets,
		IndexDirs:         c.RAG.IndexDirs,
		IndexExcludeDirs:  c.RAG.IndexExcludeDirs,
		IndexExcludeGlobs: c.RAG.IndexExcludeGlobs,
	}
}

// ragConfigChanged returns true if any RAG-related config fields differ between old and new.
func ragConfigChanged(old, new Config) bool {
	return !reflect.DeepEqual(ragFingerprintOf(old), ragFingerprintOf(new))
}
