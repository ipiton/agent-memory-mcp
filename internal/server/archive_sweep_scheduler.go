package server

import (
	"context"
	"path/filepath"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
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
// The config is passed in rather than read off srv so that a caller running on
// a background goroutine takes one consistent snapshot (T88 H3) instead of
// racing ReloadConfig field by field.
func resolveArchiveSweepRoots(cfg config.Config) []string {
	if roots := cfg.Lifecycle.TaskArchiveRoots; len(roots) > 0 {
		return append([]string(nil), roots...)
	}
	if cfg.RootPath == "" {
		return nil
	}
	return []string{filepath.Join(cfg.RootPath, "tasks", "archive")}
}

// runArchiveSweepOnce performs one background consolidation pass with the
// zero-ops defaults (auto_promote=true, live). Returns nil result when there is
// no memory store or no roots resolve.
func (srv *MCPServer) runArchiveSweepOnce(ctx context.Context) (*lifecycle.SweepResult, error) {
	if srv.memoryStore == nil {
		return nil, nil
	}
	snapshot := srv.configSnapshot()
	roots := resolveArchiveSweepRoots(snapshot)
	if len(roots) == 0 {
		return nil, nil
	}
	cfg := lifecycle.ArchiveSweepConfig{
		Roots:              roots,
		SlugPattern:        snapshot.Lifecycle.TaskSlugPattern,
		PromotionThreshold: lifecycle.DefaultPromotionThreshold,
		KeepTag:            lifecycle.KeepAfterArchiveTag,
		AutoPromote:        true,
	}
	sweeper := lifecycle.NewSweeper(srv.memoryStore)
	return sweeper.SweepArchive(ctx, cfg)
}

// archiveSweepScheduler runs runArchiveSweepOnce shortly after startup and then
// on a fixed interval (T63 zero-ops). The lifecycle lives in intervalScheduler
// — T90 D1; this type is the job plus its logging.
//
// newArchiveSweepScheduler returns nil when the feature is off or no store is
// present, and Start/Close tolerate a nil receiver so callers need no guards.
type archiveSweepScheduler struct {
	*intervalScheduler
	srv *MCPServer
}

// newArchiveSweepScheduler returns a scheduler when enabled with a positive
// interval and a memory store, else nil.
func newArchiveSweepScheduler(srv *MCPServer, enabled bool, interval time.Duration) *archiveSweepScheduler {
	if srv == nil || srv.memoryStore == nil || !enabled || interval <= 0 {
		return nil
	}
	a := &archiveSweepScheduler{srv: srv}
	a.intervalScheduler = &intervalScheduler{
		name:       "archive-sweep",
		interval:   interval,
		fileLogger: srv.fileLogger,
		// Read through a func so tests can shorten the delay after construction.
		initialDelay: func() time.Duration { return archiveSweepInitialDelay },
		run:          a.runOnce,
	}
	return a
}

// See sedimentScheduler: explicit, so a typed-nil receiver stays safe.
func (a *archiveSweepScheduler) Start() {
	if a == nil {
		return
	}
	a.intervalScheduler.Start()
}

func (a *archiveSweepScheduler) Close() {
	if a == nil {
		return
	}
	a.intervalScheduler.Close()
}

func (a *archiveSweepScheduler) runOnce(ctx context.Context) {
	start := time.Now()
	result, err := a.srv.runArchiveSweepOnce(ctx)
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
