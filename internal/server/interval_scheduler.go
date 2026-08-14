package server

import (
	"context"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"go.uber.org/zap"
)

// intervalScheduler runs a job on a fixed interval in one long-running
// goroutine.
//
// T90 D1: the sediment and archive-sweep schedulers were byte-for-byte copies
// of the same mu/ctx/cancel/running/done state machine, down to the two-second
// bounded wait in Close. Two copies meant every fix to the shutdown or
// cancellation behaviour had to be made twice, and the copies had already begun
// to drift (only one of them delayed its first run). The lifecycle lives here
// once; callers supply a name, an interval and the job.
//
// A nil receiver is tolerated by Start and Close, so a construction helper can
// return nil for "feature disabled" and callers need no guards.
type intervalScheduler struct {
	name       string
	interval   time.Duration
	fileLogger *logger.FileLogger

	// initialDelay, when positive, runs the job once that long after Start
	// before the ticker takes over. Used to backfill shortly after boot
	// without competing with startup.
	initialDelay func() time.Duration

	run func(context.Context)

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	done    chan struct{}
}

// Start kicks off the background loop. Idempotent.
func (s *intervalScheduler) Start() {
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

	s.logInfo(s.name+" scheduler started", zap.Duration("interval", s.interval))
	go s.loop()
}

// Close cancels the loop and waits briefly for it to exit. Idempotent.
func (s *intervalScheduler) Close() {
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
			// The job is mid-flight; give up after a bounded wait so shutdown
			// does not hang. The job itself is expected to honour ctx.
		}
	}
}

func (s *intervalScheduler) loop() {
	defer close(s.done)

	if s.initialDelay != nil {
		if d := s.initialDelay(); d > 0 {
			select {
			case <-s.ctx.Done():
				s.logInfo(s.name + " scheduler stopped")
				return
			case <-time.After(d):
				s.run(s.ctx)
			}
		}
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.logInfo(s.name + " scheduler stopped")
			return
		case <-ticker.C:
			s.run(s.ctx)
		}
	}
}

func (s *intervalScheduler) logInfo(msg string, fields ...zap.Field) {
	if s == nil || s.fileLogger == nil {
		return
	}
	s.fileLogger.Info(msg, fields...)
}

func (s *intervalScheduler) logWarn(msg string, fields ...zap.Field) {
	if s == nil || s.fileLogger == nil {
		return
	}
	s.fileLogger.Warn(msg, fields...)
}
