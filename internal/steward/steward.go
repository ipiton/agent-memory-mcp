package steward

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// Service is the main stewardship orchestrator. It manages policy, runs scans,
// applies safe actions, and records audit entries.
type Service struct {
	db       *sql.DB
	store    *memory.Store
	logger   *zap.Logger
	policyMu sync.RWMutex
	policy   Policy
}

// NewService creates a steward service and ensures the required database tables exist.
func NewService(store *memory.Store, logger *zap.Logger) (*Service, error) {
	if logger == nil {
		cfg := zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zap.FatalLevel)
		cfg.OutputPaths = []string{"/dev/null"}
		logger, _ = cfg.Build()
	}

	db := store.DB()

	// Create steward tables.
	for _, fn := range []func(*sql.DB) error{
		ensurePolicyTable,
		ensureReportTable,
		ensureAuditTable,
		ensureInboxTable,
	} {
		if err := fn(db); err != nil {
			return nil, fmt.Errorf("steward: init tables: %w", err)
		}
	}

	policy, err := LoadPolicy(db)
	if err != nil {
		return nil, fmt.Errorf("steward: load policy: %w", err)
	}

	return &Service{
		db:     db,
		store:  store,
		logger: logger,
		policy: policy,
	}, nil
}

// Policy returns a snapshot of the current stewardship policy.
func (s *Service) Policy() Policy {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.policy
}

// SetPolicy updates and persists the stewardship policy.
func (s *Service) SetPolicy(p Policy) error {
	if err := SavePolicy(s.db, p); err != nil {
		return err
	}
	s.policyMu.Lock()
	s.policy = p
	s.policyMu.Unlock()
	return nil
}

// RunParams configures a steward run.
type RunParams struct {
	Scope   RunScope
	DryRun  bool
	Context string
	Service string
}

// Run executes a stewardship cycle: scan → plan → (optionally) apply → report.
func (s *Service) Run(ctx context.Context, params RunParams) (*Report, error) {
	if s.Policy().Mode == PolicyModeOff {
		return nil, fmt.Errorf("steward: stewardship is disabled (mode=off)")
	}

	startedAt := time.Now().UTC()

	scope := params.Scope
	if scope == "" {
		scope = ScopeFull
	}

	s.logger.Info("steward run started",
		zap.String("scope", string(scope)),
		zap.Bool("dry_run", params.DryRun),
		zap.String("context", params.Context),
		zap.String("service", params.Service),
	)

	// Phase 1: Scan
	scanResult := RunScanners(ctx, s.store, s.policy, scope, params.Context, params.Service)

	// Phase 2: Apply (if not dry-run)
	applied := 0
	pendingReview := 0

	for i := range scanResult.Actions {
		a := &scanResult.Actions[i]
		if params.DryRun {
			// In dry-run mode, mark safe actions as "planned" and review as "review_required".
			if a.Handling == HandlingSafeAutoApply {
				a.State = StatePlanned
			} else {
				a.State = StateReviewRequired
			}
			pendingReview++
			continue
		}

		if a.Handling == HandlingSafeAutoApply {
			if err := s.applyAction(ctx, a, ""); err != nil {
				a.State = StateSkipped
				scanResult.Errors = append(scanResult.Errors, RunError{
					Phase:   "apply",
					Message: fmt.Sprintf("failed to apply %s on %v: %v", a.Kind, a.TargetIDs, err),
				})
				continue
			}
			a.State = StateApplied
			applied++
		} else {
			a.State = StateReviewRequired
			pendingReview++
		}
	}

	completedAt := time.Now().UTC()

	// Count findings by type.
	stats := RunStats{Scanned: scanResult.Scanned, ActionsApplied: applied, ActionsPendingReview: pendingReview}
	for _, a := range scanResult.Actions {
		switch a.Kind {
		case ActionMergeDuplicates:
			stats.DuplicatesFound++
		case ActionFlagConflict:
			stats.ConflictsFound++
		case ActionFlagContradiction:
			stats.ContradictionsFound++
		case ActionMarkStale:
			stats.StaleFound++
		case ActionDeleteExpiredWorking:
			stats.ExpiredWorkingFound++
		case ActionPromoteCanonical:
			stats.PromotionCandidates++
		}
	}

	report := &Report{
		ID:          uuid.New().String(),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Scope:       scope,
		DryRun:      params.DryRun,
		Context:     params.Context,
		Service:     params.Service,
		Stats:           stats,
		Actions:         scanResult.Actions,
		Errors:          scanResult.Errors,
		CanonicalHealth: scanResult.CanonicalHealth,
	}

	// Phase 3: Persist report and audit entries.
	if err := SaveReport(s.db, report); err != nil {
		s.logger.Error("steward: failed to save report", zap.Error(err))
	}
	if !params.DryRun {
		for _, a := range report.Actions {
			switch a.State {
			case StateApplied:
				if err := WriteAuditEntry(s.db, &AuditEntry{
					RunID:      report.ID,
					Action:     a.Kind,
					TargetIDs:  a.TargetIDs,
					Handling:   string(a.Handling),
					Rationale:  a.Rationale,
					Evidence:   a.Evidence,
					Confidence: a.Confidence,
					AppliedBy:  "steward_auto",
				}); err != nil {
					s.logger.Error("steward: failed to write audit entry", zap.Error(err))
				}
			case StateReviewRequired:
				if err := CreateInboxItem(s.db, &InboxItem{
					SourceRunID:       report.ID,
					Kind:              actionToInboxKind(a.Kind),
					Title:             a.Title,
					Evidence:          a.Evidence,
					Confidence:        a.Confidence,
					Urgency:           actionToUrgency(&a),
					RecommendedAction: a.Rationale,
					TargetIDs:         a.TargetIDs,
				}); err != nil {
					s.logger.Error("steward: failed to create inbox item", zap.Error(err))
				}
			}
		}
	}

	s.logger.Info("steward run completed",
		zap.String("run_id", report.ID),
		zap.Duration("duration", completedAt.Sub(startedAt)),
		zap.Int("scanned", stats.Scanned),
		zap.Int("applied", applied),
		zap.Int("pending_review", pendingReview),
	)

	return report, nil
}

// applyAction executes a single safe action against the memory store.
func (s *Service) applyAction(ctx context.Context, a *Action, runID string) error {
	switch a.Kind {
	case ActionMarkStale:
		if len(a.TargetIDs) == 0 {
			return fmt.Errorf("no target IDs")
		}
		_, err := s.store.MarkOutdated(ctx, a.TargetIDs[0], a.Rationale, "")
		return err

	case ActionRefreshFreshness:
		// Freshness scores are computed on read, nothing to persist.
		return nil

	case ActionMergeDuplicates:
		if len(a.TargetIDs) < 2 {
			return fmt.Errorf("merge requires at least 2 IDs")
		}
		_, err := s.store.MergeDuplicates(ctx, a.TargetIDs[0], a.TargetIDs[1:])
		return err

	case ActionPromoteCanonical:
		if len(a.TargetIDs) == 0 {
			return fmt.Errorf("no target IDs")
		}
		_, err := s.store.PromoteToCanonical(ctx, a.TargetIDs[0], "steward")
		return err

	case ActionDeleteExpiredWorking:
		if len(a.TargetIDs) == 0 {
			return fmt.Errorf("no target IDs")
		}
		return s.store.Delete(ctx, a.TargetIDs[0])

	default:
		return fmt.Errorf("unsupported action kind: %s", a.Kind)
	}
}

// GetReport returns a specific report by ID, or the latest if id is empty.
func (s *Service) GetReport(id string) (*Report, error) {
	if id == "" {
		return LoadLatestReport(s.db)
	}
	return LoadReport(s.db, id)
}

// Status returns the current stewardship status.
func (s *Service) Status() (*Status, error) {
	pendingReview, err := CountPendingInbox(s.db)
	if err != nil {
		// Fallback to report-based count.
		pendingReview, _ = CountPendingReview(s.db)
	}

	status := &Status{
		PolicyMode:    s.Policy().Mode,
		PendingReview: pendingReview,
	}

	latest, err := LoadLatestReport(s.db)
	if err != nil {
		return nil, err
	}
	if latest != nil {
		status.LastRun = &RunBrief{
			RunID:     latest.ID,
			StartedAt: latest.StartedAt,
			Duration:  latest.CompletedAt.Sub(latest.StartedAt).Round(time.Millisecond).String(),
			Stats:     latest.Stats,
		}
	}

	return status, nil
}

// ListInbox returns inbox items matching the query.
func (s *Service) ListInbox(q InboxQuery) ([]InboxItem, error) {
	return ListInboxItems(s.db, q)
}

// ResolveInbox resolves an inbox item with the given action.
func (s *Service) ResolveInbox(id, action, note, resolvedBy string) error {
	if action == "defer" {
		return DeferInboxItem(s.db, id, note)
	}
	return ResolveInboxItem(s.db, id, action, note, resolvedBy)
}

// DB exposes the database for use by tools that need direct inbox operations.
func (s *Service) DB() *sql.DB {
	return s.db
}
