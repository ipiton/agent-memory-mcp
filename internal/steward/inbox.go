package steward

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// InboxItemKind classifies the type of inbox entry.
type InboxItemKind string

const (
	InboxDuplicateCandidate    InboxItemKind = "duplicate_candidate"
	InboxContradictionCandidate InboxItemKind = "contradiction_candidate"
	InboxStaleCanonical        InboxItemKind = "stale_canonical"
	InboxStaleEntry            InboxItemKind = "stale_entry"
	InboxOutdatedProcedural    InboxItemKind = "outdated_procedural"
	InboxUnverifiedRunbook     InboxItemKind = "unverified_runbook"
	InboxSourceMismatch        InboxItemKind = "source_mismatch"
	InboxMissingSourceLink     InboxItemKind = "missing_source_link"
	InboxSupersededCandidate   InboxItemKind = "superseded_candidate"
	InboxPromotionCandidate    InboxItemKind = "promotion_candidate"
	InboxDriftDetected         InboxItemKind = "drift_detected"
)

// InboxItemState tracks the lifecycle of an inbox item.
type InboxItemState string

const (
	InboxPending  InboxItemState = "pending"
	InboxResolved InboxItemState = "resolved"
	InboxDeferred InboxItemState = "deferred"
)

// InboxItem represents a review-required action in the stewardship inbox.
type InboxItem struct {
	ID                string         `json:"id"`
	SourceRunID       string         `json:"source_run_id,omitempty"`
	Kind              InboxItemKind  `json:"kind"`
	State             InboxItemState `json:"state"`
	Title             string         `json:"title"`
	Evidence          []string       `json:"evidence,omitempty"`
	Confidence        float64        `json:"confidence"`
	Urgency           string         `json:"urgency"` // high, medium, low
	RecommendedAction string         `json:"recommended_action"`
	TargetIDs         []string       `json:"target_ids,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	ResolvedAt        *time.Time     `json:"resolved_at,omitempty"`
	ResolvedBy        string         `json:"resolved_by,omitempty"`
	Resolution        string         `json:"resolution,omitempty"`
	ResolutionNote    string         `json:"resolution_note,omitempty"`
}

func ensureInboxTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS steward_inbox (
			id TEXT PRIMARY KEY,
			source_run_id TEXT,
			kind TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'pending',
			title TEXT NOT NULL,
			evidence TEXT,
			confidence REAL,
			urgency TEXT,
			recommended_action TEXT,
			target_ids TEXT,
			created_at DATETIME NOT NULL,
			resolved_at DATETIME,
			resolved_by TEXT,
			resolution TEXT,
			resolution_note TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_steward_inbox_state ON steward_inbox(state);
		CREATE INDEX IF NOT EXISTS idx_steward_inbox_kind ON steward_inbox(kind);
		CREATE INDEX IF NOT EXISTS idx_steward_inbox_created ON steward_inbox(created_at);
	`)
	return err
}

// CreateInboxItem inserts a new inbox item.
func CreateInboxItem(db *sql.DB, item *InboxItem) error {
	if item.ID == "" {
		item.ID = uuid.New().String()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.State == "" {
		item.State = InboxPending
	}

	evidenceJSON, _ := json.Marshal(item.Evidence)
	targetJSON, _ := json.Marshal(item.TargetIDs)

	_, err := db.Exec(`
		INSERT INTO steward_inbox (id, source_run_id, kind, state, title, evidence, confidence, urgency, recommended_action, target_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.SourceRunID, string(item.Kind), string(item.State),
		item.Title, string(evidenceJSON), item.Confidence, item.Urgency,
		item.RecommendedAction, string(targetJSON), item.CreatedAt)
	if err != nil {
		return fmt.Errorf("steward: create inbox item: %w", err)
	}
	return nil
}

// InboxQuery configures an inbox list query.
type InboxQuery struct {
	Status string // "pending", "resolved", "deferred", "all"
	Kind   string // filter by kind
	Limit  int
	SortBy string // "urgency", "created_at", "confidence"
}

// ListInboxItems returns inbox items matching the query.
func ListInboxItems(db *sql.DB, q InboxQuery) ([]InboxItem, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}

	var conditions []string
	var args []any

	if q.Status != "" && q.Status != "all" {
		conditions = append(conditions, "state = ?")
		args = append(args, q.Status)
	}
	if q.Kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, q.Kind)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	orderBy := "created_at DESC"
	switch q.SortBy {
	case "urgency":
		orderBy = "CASE urgency WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END, created_at DESC"
	case "confidence":
		orderBy = "confidence DESC, created_at DESC"
	}

	query := fmt.Sprintf(`
		SELECT id, source_run_id, kind, state, title, evidence, confidence, urgency, recommended_action, target_ids, created_at, resolved_at, resolved_by, resolution, resolution_note
		FROM steward_inbox %s ORDER BY %s LIMIT ?
	`, where, orderBy)
	args = append(args, q.Limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("steward: list inbox: %w", err)
	}
	defer rows.Close()

	var items []InboxItem
	for rows.Next() {
		item, err := scanInboxRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

// ResolveInboxItem marks an inbox item as resolved.
func ResolveInboxItem(db *sql.DB, id, action, note, resolvedBy string) error {
	now := time.Now().UTC()
	result, err := db.Exec(`
		UPDATE steward_inbox
		SET state = 'resolved', resolved_at = ?, resolved_by = ?, resolution = ?, resolution_note = ?
		WHERE id = ? AND state = 'pending'
	`, now, resolvedBy, action, note, id)
	if err != nil {
		return fmt.Errorf("steward: resolve inbox item: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("steward: inbox item %s not found or already resolved", id)
	}
	return nil
}

// DeferInboxItem marks an inbox item as deferred.
func DeferInboxItem(db *sql.DB, id, note string) error {
	now := time.Now().UTC()
	result, err := db.Exec(`
		UPDATE steward_inbox SET state = 'deferred', resolution_note = ?, resolved_at = ? WHERE id = ? AND state = 'pending'
	`, note, now, id)
	if err != nil {
		return fmt.Errorf("steward: defer inbox item: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("steward: inbox item %s not found or already resolved", id)
	}
	return nil
}

// CountPendingInbox returns the count of pending inbox items.
func CountPendingInbox(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM steward_inbox WHERE state = 'pending'`).Scan(&count)
	return count, err
}

type inboxScannable interface {
	Scan(dest ...any) error
}

func scanInboxRow(row inboxScannable) (*InboxItem, error) {
	var (
		item         InboxItem
		sourceRunID  sql.NullString
		evidenceJSON sql.NullString
		targetJSON   sql.NullString
		resolvedAt   sql.NullTime
		resolvedBy   sql.NullString
		resolution   sql.NullString
		resNote      sql.NullString
		kind, state  string
	)

	err := row.Scan(
		&item.ID, &sourceRunID, &kind, &state, &item.Title,
		&evidenceJSON, &item.Confidence, &item.Urgency, &item.RecommendedAction,
		&targetJSON, &item.CreatedAt, &resolvedAt, &resolvedBy, &resolution, &resNote,
	)
	if err != nil {
		return nil, fmt.Errorf("steward: scan inbox row: %w", err)
	}

	item.Kind = InboxItemKind(kind)
	item.State = InboxItemState(state)
	if sourceRunID.Valid {
		item.SourceRunID = sourceRunID.String
	}
	if evidenceJSON.Valid && evidenceJSON.String != "" {
		_ = json.Unmarshal([]byte(evidenceJSON.String), &item.Evidence)
	}
	if targetJSON.Valid && targetJSON.String != "" {
		_ = json.Unmarshal([]byte(targetJSON.String), &item.TargetIDs)
	}
	if resolvedAt.Valid {
		item.ResolvedAt = &resolvedAt.Time
	}
	if resolvedBy.Valid {
		item.ResolvedBy = resolvedBy.String
	}
	if resolution.Valid {
		item.Resolution = resolution.String
	}
	if resNote.Valid {
		item.ResolutionNote = resNote.String
	}

	return &item, nil
}

// actionToInboxKind maps steward action kinds to inbox item kinds.
func actionToInboxKind(kind ActionKind) InboxItemKind {
	switch kind {
	case ActionMergeDuplicates:
		return InboxDuplicateCandidate
	case ActionFlagConflict:
		return InboxContradictionCandidate
	case ActionMarkStale:
		return InboxStaleEntry
	case ActionPromoteCanonical:
		return InboxPromotionCandidate
	default:
		return InboxItemKind(string(kind))
	}
}

// actionToUrgency maps action handling to urgency string.
func actionToUrgency(a *Action) string {
	if a.Confidence >= 0.8 {
		return "high"
	}
	if a.Confidence >= 0.6 {
		return "medium"
	}
	return "low"
}
