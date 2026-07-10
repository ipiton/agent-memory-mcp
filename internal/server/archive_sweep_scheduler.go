package server

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/lifecycle"
	"go.uber.org/zap"
)

// archiveSweepInitialDelay is how long after startup the first background sweep
// runs. Short enough to backfill accumulated archives within a normal session,
// long enough not to compete with server boot. Overridable in tests.
var archiveSweepInitialDelay = 15 * time.Second

// resolveArchiveSweepRoots returns the archive roots to sweep. Explicit config
// wins; otherwise it falls back to the <root>/tasks/archive convention so a
// zero-config install consolidates out of the box (T63). A non-existent
// convention path is a harmless no-op — the sweeper stats each root and skips
// the ones it cannot read.
func (srv *MCPServer) resolveArchiveSweepRoots() []string {
	if roots := srv.config.Lifecycle.TaskArchiveRoots; len(roots) > 0 {
		return append([]string(nil), roots...)
	}
	if srv.config.RootPath == "" {
		return nil
	}
	return []string{filepath.Join(srv.config.RootPath, "tasks", "archive")}
}

// runArchiveSweepOnce performs one background consolidation pass with the
// zero-ops defaults (auto_promote=true, live). Returns nil result when there is
// no memory store or no roots resolve.
func (srv *MCPServer) runArchiveSweepOnce(ctx context.Context) (*lifecycle.SweepResult, error) {
	if srv.memoryStore == nil {
		return nil, nil
	}
	roots := srv.resolveArchiveSweepRoots()
	if len(roots) == 0 {
		return nil, nil
	}
	cfg := lifecycle.ArchiveSweepConfig{
		Roots:              roots,
		SlugPattern:        srv.config.Lifecycle.TaskSlugPattern,
		PromotionThreshold: lifecycle.DefaultPromotionThreshold,
		KeepTag:            lifecycle.KeepAfterArchiveTag,
		AutoPromote:        true,
	}
	sweeper := lifecycle.NewSweeper(srv.memoryStore)
	return sweeper.SweepArchive(ctx, cfg)
}

// archiveSweepScheduler runs runArchiveSweepOnce shortly after startup and then
// on a fixed interval, in a single long-running goroutine (T63 zero-ops). It
// mirrors sedimentScheduler: newArchiveSweepScheduler returns nil when the
// feature is off or no store is present, and Start/Close tolerate a nil
// receiver so callers need no guards.
type archiveSweepScheduler struct {
	srv      *MCPServer
	interval time.Duration

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	done    chan struct{}
}

// newArchiveSweepScheduler returns a scheduler when enabled with a positive
// interval and a memory store, else nil.
func newArchiveSweepScheduler(srv *MCPServer, enabled bool, interval time.Duration) *archiveSweepScheduler {
	if srv == nil || srv.memoryStore == nil || !enabled || interval <= 0 {
		return nil
	}
	return &archiveSweepScheduler{srv: srv, interval: interval}
}

// Start kicks off the background loop. Idempotent.
func (a *archiveSweepScheduler) Start() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.done = make(chan struct{})
	a.running = true

	a.logInfo("archive-sweep scheduler started", zap.Duration("interval", a.interval))
	go a.loop()
}

// Close cancels the loop and waits briefly for it to exit. Idempotent.
func (a *archiveSweepScheduler) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	a.running = false
	cancel := a.cancel
	done := a.done
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// A sweep is mid-flight; give up after a bounded wait so shutdown
			// does not hang.
		}
	}
}

func (a *archiveSweepScheduler) loop() {
	defer close(a.done)

	// Initial backfill pass, delayed so it does not compete with boot.
	select {
	case <-a.ctx.Done():
		a.logInfo("archive-sweep scheduler stopped")
		return
	case <-time.After(archiveSweepInitialDelay):
		a.runOnce()
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			a.logInfo("archive-sweep scheduler stopped")
			return
		case <-ticker.C:
			a.runOnce()
		}
	}
}

func (a *archiveSweepScheduler) runOnce() {
	start := time.Now()
	result, err := a.srv.runArchiveSweepOnce(a.ctx)
	elapsed := time.Since(start)
	if err != nil {
		a.logWarn("archive sweep failed", zap.Error(err), zap.Duration("elapsed", elapsed))
		return
	}
	if result == nil {
		return
	}
	a.logInfo("archive sweep complete",
		zap.Int("outdated", result.TotalOutdated),
		zap.Int("promoted", result.TotalPromoted),
		zap.Int("promotion_candidates", result.TotalPromotionCand),
		zap.Int("skipped", result.TotalSkipped),
		zap.Int("errors", len(result.Errors)),
		zap.Duration("elapsed", elapsed),
	)
}

func (a *archiveSweepScheduler) logInfo(msg string, fields ...zap.Field) {
	if a == nil || a.srv == nil || a.srv.fileLogger == nil {
		return
	}
	a.srv.fileLogger.Info(msg, fields...)
}

func (a *archiveSweepScheduler) logWarn(msg string, fields ...zap.Field) {
	if a == nil || a.srv == nil || a.srv.fileLogger == nil {
		return
	}
	a.srv.fileLogger.Warn(msg, fields...)
}
