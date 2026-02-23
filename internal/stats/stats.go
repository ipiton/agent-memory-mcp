// Package stats provides sampled usage statistics logging in JSONL format.
package stats

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

const maxErrorLength = 200

// Event represents a single MCP usage event for statistics tracking.
type Event struct {
	Timestamp   string `json:"timestamp"`
	EventName   string `json:"event"`
	Tool        string `json:"tool,omitempty"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	QueryLength int    `json:"query_length,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
	MaxDepth    int    `json:"max_depth,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
}

// Logger writes sampled usage events to a JSONL file.
type Logger struct {
	enabled    bool
	sampleRate float64
	mu         sync.Mutex
	rng        *rand.Rand
	file       *os.File
}

// NewLogger creates a stats Logger from the given config, or returns nil if disabled.
func NewLogger(cfg config.Config) *Logger {
	if !cfg.StatsEnabled || cfg.StatsPath == "" {
		return nil
	}
	if cfg.StatsSampleRate <= 0 {
		return nil
	}
	if cfg.StatsSampleRate > 1 {
		cfg.StatsSampleRate = 1
	}
	dir := filepath.Dir(cfg.StatsPath)
	if dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	file, err := os.OpenFile(cfg.StatsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return &Logger{
		enabled:    true,
		sampleRate: cfg.StatsSampleRate,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		file:       file,
	}
}

// Log writes an event to the stats file, subject to sampling rate.
func (s *Logger) Log(event Event) {
	if s == nil || !s.enabled {
		return
	}
	if s.sampleRate < 1 && s.rng.Float64() > s.sampleRate {
		return
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Error != "" {
		event.Error = trimError(event.Error)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.file.Write(append(data, '\n'))
}

// Close closes the underlying stats file.
func (s *Logger) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}

func trimError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxErrorLength {
		return value
	}
	return value[:maxErrorLength] + "..."
}
