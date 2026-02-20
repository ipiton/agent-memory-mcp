package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxErrorLength = 200

type StatsEvent struct {
	Timestamp   string `json:"timestamp"`
	Event       string `json:"event"`
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

type StatsLogger struct {
	enabled    bool
	path       string
	sampleRate float64
	mu         sync.Mutex
	rng        *rand.Rand
}

func NewStatsLogger(cfg Config) *StatsLogger {
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
	return &StatsLogger{
		enabled:    true,
		path:       cfg.StatsPath,
		sampleRate: cfg.StatsSampleRate,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *StatsLogger) Log(event StatsEvent) {
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
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = file.Write(append(data, '\n'))
	_ = file.Close()
}

func trimError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxErrorLength {
		return value
	}
	return value[:maxErrorLength] + "..."
}
