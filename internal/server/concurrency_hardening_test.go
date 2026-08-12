package server

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"
)

// T88 H3. The archive-sweep scheduler runs on its own goroutine and used to
// read srv.config field by field while SIGHUP / the config watcher reassigned
// it under ragMu.Lock. Run under -race: red before configSnapshot.
func TestArchiveSweepDoesNotRaceWithConfigReload(t *testing.T) {
	s := newMemoryTestServer(t)
	base := s.config

	var wg sync.WaitGroup
	const rounds = 100

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range rounds {
			next := base
			if i%2 == 0 {
				next.Lifecycle.TaskArchiveRoots = []string{"/tmp/roots-a"}
				next.Lifecycle.TaskSlugPattern = regexp.MustCompile("a-.*")
			} else {
				next.Lifecycle.TaskArchiveRoots = nil
				next.Lifecycle.TaskSlugPattern = regexp.MustCompile("b-.*")
			}
			s.ReloadConfig(next)
		}
	}()

	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				_, _ = s.runArchiveSweepOnce(context.Background())
			}
		}()
	}

	wg.Wait()
}

// configSnapshot must hand back the config that ReloadConfig installed, not a
// stale copy — the lock is a barrier, not a cache.
func TestConfigSnapshotReflectsReload(t *testing.T) {
	s := newTestServer(t, "")

	next := s.config
	next.Lifecycle.TaskSlugPattern = regexp.MustCompile("reloaded-.*")
	s.ReloadConfig(next)

	got := s.configSnapshot().Lifecycle.TaskSlugPattern
	if got == nil || got.String() != "reloaded-.*" {
		t.Fatalf("configSnapshot slug pattern = %v, want %q", got, "reloaded-.*")
	}
}

// T88 M4. A checkpoint registered after Close() drained the WaitGroup would run
// on a cancelled ctx and be lost. Close must either wait for it or the
// checkpoint must decline; neither outcome may leave a goroutine writing into a
// closing tracker.
func TestCheckpointAfterCloseIsNotStranded(t *testing.T) {
	s := newAutoSessionTestServer(t, time.Hour, time.Nanosecond, 1)
	st := s.sessionTracker

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			st.HandleToolCall("store_memory", map[string]any{"content": "checkpoint material"}, nil)
		}
	}()

	time.Sleep(time.Millisecond)
	st.Close()
	wg.Wait()

	// Close() has returned: every checkpoint it accepted is finished, and every
	// one it declined released its worker slot. A leaked slot would make this
	// fill fail.
	for range cap(st.checkpointSem) {
		select {
		case st.checkpointSem <- struct{}{}:
		default:
			t.Fatal("checkpoint worker slot leaked: a declined checkpoint did not release the semaphore")
		}
	}
}
