package memory

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Foreign-write visibility (T116).
//
// The store answers everything except Get from an in-memory cache, and until
// this file existed nothing ever invalidated it. Meanwhile the CLI — checkpoint,
// auto-capture, close-session, and every SessionStart/SessionEnd/PreCompact
// hook — opens the same database file with its own Store rather than talking to
// the daemon. A record written that way was durable, confirmed, and invisible:
// not to recall_memory, not to list_memories, not to any steward scan, sediment
// cycle or archive sweep, until someone restarted the service. Measured on the
// live service: the daemon reported 4562 records against the file's 4564, and
// the two missing ones were a PreCompact checkpoint written 40 minutes earlier.
//
// The model chosen is "the file is the source of truth, the cache is a derived
// view that has to converge". It is the one that matches how the system is
// actually used — one long-lived daemon and a stream of short-lived CLI
// processes — and it keeps the CLI working when the daemon is down, which the
// alternative (route every CLI write through the daemon's HTTP endpoint) does
// not without a fallback that reintroduces the same problem.
//
// ⚠️ What this does NOT fix: a lost update. updateMemoryRow writes the whole row
// from an in-memory struct, so a CLI process that read a record before the
// daemon changed it will overwrite that change on save. Convergence makes the
// window visible rather than closing it; closing it needs a single writer.

// foreignWriteInterval is how often the daemon asks the file whether someone
// else has committed.
//
// The check is one PRAGMA on a pinned connection. The reload it can trigger is
// the real cost — 83ms for 4574 records with embeddings, measured — and the
// ticker is what bounds it: at most one reload per interval however many writes
// land, so a 100-record archive sweep pays a few reloads rather than a hundred.
const foreignWriteInterval = 2 * time.Second

// foreignWriteWatcher polls PRAGMA data_version on a connection it holds open.
//
// 🔴 The pinned connection is not an optimisation. data_version is per
// connection, and database/sql hands out whatever is free in the pool, so
// reading it through *sql.DB compares numbers from different connections and
// means nothing.
type foreignWriteWatcher struct {
	conn    *sql.Conn
	stop    chan struct{}
	stopped sync.WaitGroup
	once    sync.Once
}

// WatchForeignWrites starts converging the cache on writes made by other
// processes. It is for the daemon: a CLI process is short-lived and re-reads
// the file on every invocation anyway. Calling it twice is a no-op.
func (ms *Store) WatchForeignWrites(ctx context.Context) error {
	ms.watchMu.Lock()
	defer ms.watchMu.Unlock()
	if ms.watcher != nil {
		return nil
	}

	conn, err := ms.db.Conn(ctx)
	if err != nil {
		return err
	}
	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
		_ = conn.Close()
		return err
	}

	w := &foreignWriteWatcher{conn: conn, stop: make(chan struct{})}
	ms.watcher = w

	w.stopped.Add(1)
	go func() {
		defer w.stopped.Done()
		ticker := time.NewTicker(foreignWriteInterval)
		defer ticker.Stop()
		last := version
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				var current int
				if err := conn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&current); err != nil {
					ms.logger.Warn("Foreign-write check failed", zap.Error(err))
					continue
				}
				if current == last {
					continue
				}
				last = current
				// The daemon's own writes bump this too: the pool serves them
				// from a different connection than the pinned one, so they are
				// indistinguishable from a CLI's. Reloading anyway is the safe
				// direction — a redundant reload costs 83ms, a missed one costs
				// invisibility until restart.
				if err := ms.reloadForForeignWrite(); err != nil {
					ms.logger.Warn("Cache reload after a foreign write failed", zap.Error(err))
					continue
				}
				ms.logger.Info("Cache reloaded after an external write",
					zap.Int("data_version", current),
					zap.Int("records", ms.Count()),
				)
			}
		}
	}()
	return nil
}

// reloadForForeignWrite rebuilds the cache under the write lock.
//
// writeMu, not just mu: the documented order is writeMu → mu, and a reload that
// took only mu could land between a writer's row update and its cache update,
// so the writer would then publish a cache entry built before the reload.
func (ms *Store) reloadForForeignWrite() error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()
	return ms.loadMemoriesToCache()
}

// stopWatching ends the watcher and releases the pinned connection. Safe to
// call when no watcher was ever started.
func (ms *Store) stopWatching() {
	ms.watchMu.Lock()
	w := ms.watcher
	ms.watcher = nil
	ms.watchMu.Unlock()
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.stop) })
	w.stopped.Wait()
	_ = w.conn.Close()
}
