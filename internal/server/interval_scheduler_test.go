package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// T90 D1: the constructors return a typed nil for "feature disabled" and the
// call sites invoke Start/Close unguarded. Promoting those methods from the
// embedded lifecycle would dereference the nil receiver before any nil check
// could run — this panicked the whole server package until the wrappers were
// made explicit.
func TestDisabledSchedulersTolerateNilReceiver(t *testing.T) {
	var sediment *sedimentScheduler
	var archive *archiveSweepScheduler

	sediment.Start()
	sediment.Close()
	archive.Start()
	archive.Close()
}

func TestIntervalSchedulerRunsAndStops(t *testing.T) {
	var runs atomic.Int32
	s := &intervalScheduler{
		name:     "test",
		interval: 5 * time.Millisecond,
		run:      func(context.Context) { runs.Add(1) },
	}

	s.Start()
	s.Start() // idempotent

	deadline := time.After(2 * time.Second)
	for runs.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("scheduler produced %d runs, want at least 2", runs.Load())
		case <-time.After(2 * time.Millisecond):
		}
	}

	s.Close()
	s.Close() // idempotent
	after := runs.Load()
	time.Sleep(30 * time.Millisecond)
	if runs.Load() != after {
		t.Fatalf("scheduler kept running after Close: %d -> %d", after, runs.Load())
	}
}

func TestIntervalSchedulerHonoursInitialDelay(t *testing.T) {
	var runs atomic.Int32
	s := &intervalScheduler{
		name:         "test",
		interval:     time.Hour, // ticker must not be the one firing
		initialDelay: func() time.Duration { return 5 * time.Millisecond },
		run:          func(context.Context) { runs.Add(1) },
	}

	s.Start()
	defer s.Close()

	deadline := time.After(2 * time.Second)
	for runs.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("initial-delay run never fired")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// The job receives a context that Close cancels, which is what makes the
// bounded shutdown wait meaningful.
func TestIntervalSchedulerCancelsJobContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	s := &intervalScheduler{
		name:         "test",
		interval:     time.Hour,
		initialDelay: func() time.Duration { return time.Millisecond },
		run: func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			close(cancelled)
		},
	}

	s.Start()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("job never started")
	}

	s.Close()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("job context was not cancelled by Close")
	}
}
