package steward

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const policyKey = "default"

func ensurePolicyTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS steward_policy (
			key TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`)
	return err
}

// LoadPolicy reads the current policy from the database. Returns DefaultPolicy if none is stored.
func LoadPolicy(db *sql.DB) (Policy, error) {
	row := db.QueryRow(`SELECT data FROM steward_policy WHERE key = ?`, policyKey)

	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return DefaultPolicy(), nil
		}
		return Policy{}, fmt.Errorf("steward: load policy: %w", err)
	}

	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Policy{}, fmt.Errorf("steward: unmarshal policy: %w", err)
	}
	return p, nil
}

// SavePolicy writes the policy to the database and returns it with the
// persisted UpdatedAt stamp.
//
// T108: the stamp used to be applied to this function's own copy, so callers
// stored the un-stamped original in memory and `steward_policy get` reported an
// UpdatedAt older than the row on disk until the service restarted. That is the
// one field an operator reads to answer "when was this policy last touched" —
// it was the evidence that diagnosed T103 — so a stale value there is worse
// than none. Measured live on 2026-08-13: get said 08-12T13:22, the row said
// 08-13T19:53.
func SavePolicy(db *sql.DB, p Policy) (Policy, error) {
	p.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(p)
	if err != nil {
		return Policy{}, fmt.Errorf("steward: marshal policy: %w", err)
	}

	_, err = db.Exec(`
		INSERT INTO steward_policy (key, data, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at
	`, policyKey, string(data), p.UpdatedAt)
	if err != nil {
		return Policy{}, fmt.Errorf("steward: save policy: %w", err)
	}
	return p, nil
}
