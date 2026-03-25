package steward

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Scheduler runs stewardship cycles on a configured interval.
type Scheduler struct {
	service *Service
	logger  *zap.Logger

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	interval time.Duration
	lastRun  time.Time
	nextRun  time.Time
}

// NewScheduler creates a scheduler for the given service.
// It does not start automatically — call Start() when ready.
func NewScheduler(service *Service, logger *zap.Logger) *Scheduler {
	if logger == nil {
		cfg := zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zap.FatalLevel)
		cfg.OutputPaths = []string{"/dev/null"}
		logger, _ = cfg.Build()
	}
	return &Scheduler{
		service: service,
		logger:  logger,
	}
}

// Start begins the scheduler loop if the policy mode requires it.
// Safe to call multiple times — only the first call starts the loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	policy := s.service.Policy()
	if policy.Mode != PolicyModeScheduled && policy.Mode != PolicyModeEventDriven {
		s.logger.Info("steward scheduler not started", zap.String("mode", string(policy.Mode)))
		return
	}

	interval, err := time.ParseDuration(policy.ScheduleInterval)
	if err != nil || interval < time.Minute {
		interval = 24 * time.Hour
	}
	s.interval = interval

	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.running = true
	s.nextRun = time.Now().Add(interval)

	go s.loop()

	s.logger.Info("steward scheduler started",
		zap.Duration("interval", interval),
		zap.String("mode", string(policy.Mode)),
	)
}

// Stop halts the scheduler loop.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}
	s.cancel()
	s.running = false
	s.logger.Info("steward scheduler stopped")
}

// TriggerEvent fires a steward run if the event matches a configured trigger.
// Safe to call from any goroutine.
func (s *Scheduler) TriggerEvent(event string) {
	policy := s.service.Policy()
	if policy.Mode != PolicyModeEventDriven && policy.Mode != PolicyModeScheduled {
		return
	}

	for _, trigger := range policy.EventTriggers {
		if trigger == event {
			go s.runOnce("event:" + event)
			return
		}
	}
}

// NextRun returns the next scheduled run time, if any.
func (s *Scheduler) NextRun() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.nextRun.IsZero() {
		return nil
	}
	t := s.nextRun
	return &t
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runOnce("scheduled")
			s.mu.Lock()
			s.nextRun = time.Now().Add(s.interval)
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) runOnce(trigger string) {
	s.logger.Info("steward scheduled run starting", zap.String("trigger", trigger))

	report, err := s.service.Run(s.ctx, RunParams{
		Scope:  ScopeFull,
		DryRun: false,
	})
	if err != nil {
		s.logger.Error("steward scheduled run failed", zap.Error(err))
		return
	}

	s.mu.Lock()
	s.lastRun = time.Now()
	s.mu.Unlock()

	s.logger.Info("steward scheduled run completed",
		zap.String("run_id", report.ID),
		zap.Int("applied", report.Stats.ActionsApplied),
		zap.Int("pending", report.Stats.ActionsPendingReview),
	)
}
