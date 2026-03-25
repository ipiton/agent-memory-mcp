package steward

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

func ensureAuditTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS steward_audit (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			action TEXT NOT NULL,
			target_ids TEXT NOT NULL,
			handling TEXT NOT NULL,
			rationale TEXT,
			evidence TEXT,
			confidence REAL,
			applied_at DATETIME NOT NULL,
			applied_by TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_steward_audit_run ON steward_audit(run_id);
		CREATE INDEX IF NOT EXISTS idx_steward_audit_applied ON steward_audit(applied_at);
	`)
	return err
}

// WriteAuditEntry records an applied steward action.
func WriteAuditEntry(db *sql.DB, e *AuditEntry) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.AppliedAt.IsZero() {
		e.AppliedAt = time.Now().UTC()
	}

	targetJSON, err := json.Marshal(e.TargetIDs)
	if err != nil {
		return fmt.Errorf("steward: marshal target_ids: %w", err)
	}
	var evidenceJSON []byte
	if len(e.Evidence) > 0 {
		evidenceJSON, err = json.Marshal(e.Evidence)
		if err != nil {
			return fmt.Errorf("steward: marshal evidence: %w", err)
		}
	}

	_, err = db.Exec(`
		INSERT INTO steward_audit (id, run_id, action, target_ids, handling, rationale, evidence, confidence, applied_at, applied_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ID, e.RunID, string(e.Action), string(targetJSON), e.Handling,
		e.Rationale, nullableString(evidenceJSON), e.Confidence, e.AppliedAt, e.AppliedBy)
	if err != nil {
		return fmt.Errorf("steward: write audit entry: %w", err)
	}
	return nil
}

// ListAuditEntries returns audit entries for a given run, ordered by applied_at.
func ListAuditEntries(db *sql.DB, runID string) ([]AuditEntry, error) {
	rows, err := db.Query(`
		SELECT id, run_id, action, target_ids, handling, rationale, evidence, confidence, applied_at, applied_by
		FROM steward_audit WHERE run_id = ? ORDER BY applied_at
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("steward: list audit entries: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var (
			e            AuditEntry
			action       string
			targetJSON   string
			evidenceJSON sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.RunID, &action, &targetJSON, &e.Handling,
			&e.Rationale, &evidenceJSON, &e.Confidence, &e.AppliedAt, &e.AppliedBy); err != nil {
			return nil, fmt.Errorf("steward: scan audit entry: %w", err)
		}
		e.Action = ActionKind(action)
		if err := json.Unmarshal([]byte(targetJSON), &e.TargetIDs); err != nil {
			return nil, fmt.Errorf("steward: unmarshal target_ids: %w", err)
		}
		if evidenceJSON.Valid && evidenceJSON.String != "" {
			if err := json.Unmarshal([]byte(evidenceJSON.String), &e.Evidence); err != nil {
				return nil, fmt.Errorf("steward: unmarshal evidence: %w", err)
			}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountPendingReview returns the number of actions in review_required state from the latest run.
func CountPendingReview(db *sql.DB) (int, error) {
	report, err := LoadLatestReport(db)
	if err != nil || report == nil {
		return 0, err
	}
	count := 0
	for _, a := range report.Actions {
		if a.State == StateReviewRequired {
			count++
		}
	}
	return count, nil
}
