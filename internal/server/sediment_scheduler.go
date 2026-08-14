package server

import (
	"context"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// sedimentScheduler runs RunSedimentCycle on a fixed interval. The lifecycle
// (start/stop/bounded shutdown) lives in intervalScheduler — T90 D1; this type
// is the job plus its logging.
//
// Not a new public API: the parent MCPServer holds it and wires Start() into
// its serve path and Close() into Shutdown().
type sedimentScheduler struct {
	*intervalScheduler
	store *memory.Store
}

// newSedimentScheduler returns a scheduler if both flags are set, else nil.
// A nil receiver is tolerated by Start/Close; callers don't need guards.
func newSedimentScheduler(store *memory.Store, fileLogger *logger.FileLogger, enabled bool, interval time.Duration) *sedimentScheduler {
	if store == nil || !enabled || interval <= 0 {
		return nil
	}
	s := &sedimentScheduler{store: store}
	s.intervalScheduler = &intervalScheduler{
		name:       "sediment cycle",
		interval:   interval,
		fileLogger: fileLogger,
		run:        s.runOnce,
	}
	return s
}

// Start and Close are declared explicitly rather than promoted from the
// embedded lifecycle: the constructor returns a typed nil for "feature
// disabled", and a promoted method would dereference that nil to reach the
// embedded pointer before its own nil check could run.
func (s *sedimentScheduler) Start() {
	if s == nil {
		return
	}
	s.intervalScheduler.Start()
}

func (s *sedimentScheduler) Close() {
	if s == nil {
		return
	}
	s.intervalScheduler.Close()
}

func (s *sedimentScheduler) runOnce(ctx context.Context) {
	start := time.Now()
	result, err := s.store.RunSedimentCycle(ctx, memory.SedimentCycleConfig{})
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
