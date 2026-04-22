package server

import (
	"context"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// sedimentScheduler runs RunSedimentCycle on a fixed interval in a single
// long-running goroutine. Start() is a no-op if the feature flag is off or
// the interval is zero, so production defaults (SedimentEnabled=false,
// SedimentScheduleInterval=0) leave this component dormant.
//
// Not a new public API: the parent MCPServer holds it and wires Start()
// into its serve path and Close() into Shutdown().
type sedimentScheduler struct {
	store      *memory.Store
	fileLogger *logger.FileLogger
	interval   time.Duration

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	done    chan struct{}
}

// newSedimentScheduler returns a scheduler if both flags are set, else nil.
// A nil receiver is tolerated by Start/Close; callers don't need guards.
func newSedimentScheduler(store *memory.Store, fileLogger *logger.FileLogger, enabled bool, interval time.Duration) *sedimentScheduler {
	if store == nil || !enabled || interval <= 0 {
		return nil
	}
	return &sedimentScheduler{
		store:      store,
		fileLogger: fileLogger,
		interval:   interval,
	}
}

// Start kicks off the background loop. Safe to call multiple times —
// subsequent calls after the first are no-ops.
func (s *sedimentScheduler) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	s.running = true

	s.logInfo("sediment cycle scheduler started", zap.Duration("interval", s.interval))
	go s.loop()
}

// Close cancels the loop and waits briefly for it to exit. Idempotent.
func (s *sedimentScheduler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Loop blocked on RunSedimentCycle; give up after a bounded wait
			// so shutdown doesn't hang indefinitely.
		}
	}
}

func (s *sedimentScheduler) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.logInfo("sediment cycle scheduler stopped")
			return
		case <-ticker.C:
			s.runOnce()
		}
	}
}

func (s *sedimentScheduler) runOnce() {
	start := time.Now()
	result, err := s.store.RunSedimentCycle(s.ctx, memory.SedimentCycleConfig{})
	elapsed := time.Since(start)
	if err != nil {
		s.logWarn("sediment cycle failed",
			zap.Error(err),
			zap.Duration("elapsed", elapsed),
		)
		return
	}
	if result == nil {
		return
	}
	s.logInfo("sediment cycle complete",
		zap.Int("auto_applied", result.AutoApplied),
		zap.Int("review_queued", result.ReviewQueued),
		zap.Int("errors", len(result.Errors)),
		zap.Duration("elapsed", elapsed),
	)
}

func (s *sedimentScheduler) logInfo(msg string, fields ...zap.Field) {
	if s == nil || s.fileLogger == nil {
		return
	}
	s.fileLogger.Info(msg, fields...)
}

func (s *sedimentScheduler) logWarn(msg string, fields ...zap.Field) {
	if s == nil || s.fileLogger == nil {
		return
	}
	s.fileLogger.Warn(msg, fields...)
}
