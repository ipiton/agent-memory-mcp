package steward

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func ensureReportTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS steward_runs (
			id TEXT PRIMARY KEY,
			started_at DATETIME NOT NULL,
			completed_at DATETIME NOT NULL,
			scope TEXT NOT NULL,
			dry_run INTEGER NOT NULL DEFAULT 1,
			context TEXT,
			service TEXT,
			stats TEXT NOT NULL,
			actions TEXT NOT NULL,
			errors TEXT,
			canonical_health TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_steward_runs_started ON steward_runs(started_at);
	`)
	if err != nil {
		return err
	}
	// Migration: add canonical_health column if table already exists without it.
	_, _ = db.Exec(`ALTER TABLE steward_runs ADD COLUMN canonical_health TEXT`)
	return nil
}

// SaveReport persists a completed steward report.
func SaveReport(db *sql.DB, r *Report) error {
	statsJSON, err := json.Marshal(r.Stats)
	if err != nil {
		return fmt.Errorf("steward: marshal stats: %w", err)
	}
	actionsJSON, err := json.Marshal(r.Actions)
	if err != nil {
		return fmt.Errorf("steward: marshal actions: %w", err)
	}
	var errorsJSON []byte
	if len(r.Errors) > 0 {
		errorsJSON, err = json.Marshal(r.Errors)
		if err != nil {
			return fmt.Errorf("steward: marshal errors: %w", err)
		}
	}
	var healthJSON []byte
	if r.CanonicalHealth != nil {
		healthJSON, err = json.Marshal(r.CanonicalHealth)
		if err != nil {
			return fmt.Errorf("steward: marshal canonical_health: %w", err)
		}
	}

	dryRun := 0
	if r.DryRun {
		dryRun = 1
	}

	_, err = db.Exec(`
		INSERT INTO steward_runs (id, started_at, completed_at, scope, dry_run, context, service, stats, actions, errors, canonical_health)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.ID, r.StartedAt, r.CompletedAt, string(r.Scope), dryRun,
		r.Context, r.Service, string(statsJSON), string(actionsJSON), nullableString(errorsJSON), nullableString(healthJSON))
	if err != nil {
		return fmt.Errorf("steward: save report: %w", err)
	}
	return nil
}

// LoadReport retrieves a report by ID.
func LoadReport(db *sql.DB, id string) (*Report, error) {
	row := db.QueryRow(`
		SELECT id, started_at, completed_at, scope, dry_run, context, service, stats, actions, errors, canonical_health
		FROM steward_runs WHERE id = ?
	`, id)
	return scanReport(row)
}

// LoadLatestReport returns the most recent steward report.
func LoadLatestReport(db *sql.DB) (*Report, error) {
	row := db.QueryRow(`
		SELECT id, started_at, completed_at, scope, dry_run, context, service, stats, actions, errors, canonical_health
		FROM steward_runs ORDER BY started_at DESC LIMIT 1
	`)
	return scanReport(row)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanReport(row scannable) (*Report, error) {
	var (
		r          Report
		scope      string
		dryRun     int
		ctx, svc   sql.NullString
		statsJSON  string
		actJSON    string
		errsJSON   sql.NullString
		healthJSON sql.NullString
	)

	err := row.Scan(&r.ID, &r.StartedAt, &r.CompletedAt, &scope, &dryRun,
		&ctx, &svc, &statsJSON, &actJSON, &errsJSON, &healthJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("steward: scan report: %w", err)
	}

	r.Scope = RunScope(scope)
	r.DryRun = dryRun == 1
	if ctx.Valid {
		r.Context = ctx.String
	}
	if svc.Valid {
		r.Service = svc.String
	}
	if err := json.Unmarshal([]byte(statsJSON), &r.Stats); err != nil {
		return nil, fmt.Errorf("steward: unmarshal stats: %w", err)
	}
	if err := json.Unmarshal([]byte(actJSON), &r.Actions); err != nil {
		return nil, fmt.Errorf("steward: unmarshal actions: %w", err)
	}
	if errsJSON.Valid && errsJSON.String != "" {
		if err := json.Unmarshal([]byte(errsJSON.String), &r.Errors); err != nil {
			return nil, fmt.Errorf("steward: unmarshal errors: %w", err)
		}
	}
	if healthJSON.Valid && healthJSON.String != "" {
		var health CanonicalHealth
		if err := json.Unmarshal([]byte(healthJSON.String), &health); err != nil {
			return nil, fmt.Errorf("steward: unmarshal canonical_health: %w", err)
		}
		r.CanonicalHealth = &health
	}
	return &r, nil
}

func nullableString(b []byte) sql.NullString {
	if len(b) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// FormatReport produces a human-readable summary of a steward report.
func FormatReport(r *Report) string {
	if r == nil {
		return "No steward reports found."
	}

	var sb strings.Builder

	mode := "APPLY"
	if r.DryRun {
		mode = "DRY RUN"
	}

	fmt.Fprintf(&sb, "Steward Run [%s] — %s\n", mode, r.ID[:8])
	fmt.Fprintf(&sb, "Scope: %s | Duration: %s\n", r.Scope, r.CompletedAt.Sub(r.StartedAt).Round(time.Millisecond))
	if r.Context != "" {
		fmt.Fprintf(&sb, "Context: %s", r.Context)
		if r.Service != "" {
			fmt.Fprintf(&sb, " | Service: %s", r.Service)
		}
		sb.WriteByte('\n')
	}

	sb.WriteByte('\n')
	fmt.Fprintf(&sb, "Scanned: %d memories\n", r.Stats.Scanned)
	fmt.Fprintf(&sb, "  Duplicates found:       %d\n", r.Stats.DuplicatesFound)
	fmt.Fprintf(&sb, "  Conflicts found:        %d\n", r.Stats.ConflictsFound)
	fmt.Fprintf(&sb, "  Stale found:            %d\n", r.Stats.StaleFound)
	fmt.Fprintf(&sb, "  Promotion candidates:   %d\n", r.Stats.PromotionCandidates)
	fmt.Fprintf(&sb, "  Actions applied:        %d\n", r.Stats.ActionsApplied)
	fmt.Fprintf(&sb, "  Actions pending review: %d\n", r.Stats.ActionsPendingReview)

	if len(r.Actions) > 0 {
		sb.WriteString("\nActions:\n")
		for i, a := range r.Actions {
			stateIcon := "[ ]"
			switch a.State {
			case StateApplied:
				stateIcon = "[x]"
			case StateReviewRequired:
				stateIcon = "[?]"
			case StateSkipped:
				stateIcon = "[-]"
			}
			fmt.Fprintf(&sb, "  %d. %s %s — %s\n", i+1, stateIcon, a.Kind, a.Title)
			fmt.Fprintf(&sb, "     Rationale: %s\n", a.Rationale)
			if a.Confidence > 0 {
				fmt.Fprintf(&sb, "     Confidence: %.2f | Handling: %s\n", a.Confidence, a.Handling)
			}
			if len(a.TargetIDs) > 0 {
				fmt.Fprintf(&sb, "     Targets: %s\n", strings.Join(a.TargetIDs, ", "))
			}
		}
	}

	if r.CanonicalHealth != nil && r.CanonicalHealth.Total > 0 {
		ch := r.CanonicalHealth
		sb.WriteString("\nCanonical Health:\n")
		fmt.Fprintf(&sb, "  Total: %d | Stale: %d | Unverified: %d | Conflicting: %d | Low support: %d\n",
			ch.Total, ch.Stale, ch.Unverified, ch.Conflicting, ch.LowSupport)
		for _, issue := range ch.Issues {
			fmt.Fprintf(&sb, "  - [%s] %s — %s\n", issue.Urgency, issue.Title, issue.Issue)
		}
	}

	if len(r.Errors) > 0 {
		sb.WriteString("\nErrors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&sb, "  - [%s] %s\n", e.Phase, e.Message)
		}
	}

	return sb.String()
}
