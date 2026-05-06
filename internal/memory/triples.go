package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TripleLinkType distinguishes how a (subj, rel, obj) edge entered the
// graph. Hard edges have provenance (a source memory_id) and are persisted;
// soft edges are inference computed at query time and not stored. T50 slice 1
// stores only "extracted" edges; "inferred" is reserved for a future slice
// that materialises PPR walk results when caching makes sense.
type TripleLinkType string

const (
	// LinkTypeExtracted edges are produced by an LLM (or manual input) at
	// ingest time and tied to a specific source memory. Always persisted.
	LinkTypeExtracted TripleLinkType = "extracted"

	// LinkTypeInferred edges are derived at query time via PPR / graph
	// traversal. Reserved name — slice 1 does not write these. When/if
	// materialisation is added later, callers must set link_type explicitly
	// so a future cleanup can distinguish persisted-but-stale inferences
	// from durable extracted facts.
	LinkTypeInferred TripleLinkType = "inferred"
)

// Triple is a single (subj, rel, obj) edge in the memory knowledge graph,
// with provenance (MemoryID) and a relevance Weight in [0, 1].
type Triple struct {
	ID        string         `json:"id"`
	Subject   string         `json:"subj"`
	Relation  string         `json:"rel"`
	Object    string         `json:"obj"`
	MemoryID  string         `json:"memory_id"`
	LinkType  TripleLinkType `json:"link_type"`
	Weight    float64        `json:"weight"`
	CreatedAt time.Time      `json:"created_at"`
}

// validate normalises and checks a Triple before persistence. It trims all
// string fields and rejects empty subj/rel/obj/memory_id; weight is clamped
// to [0, 1] (negative or >1 weights are nonsense for our scoring path).
func (t *Triple) validate() error {
	t.Subject = strings.TrimSpace(t.Subject)
	t.Relation = strings.TrimSpace(t.Relation)
	t.Object = strings.TrimSpace(t.Object)
	t.MemoryID = strings.TrimSpace(t.MemoryID)
	if t.Subject == "" || t.Relation == "" || t.Object == "" {
		return fmt.Errorf("triple subj/rel/obj must all be non-empty")
	}
	if t.MemoryID == "" {
		return fmt.Errorf("triple memory_id is required for provenance")
	}
	if t.LinkType == "" {
		t.LinkType = LinkTypeExtracted
	}
	if t.Weight < 0 {
		t.Weight = 0
	}
	if t.Weight > 1 {
		t.Weight = 1
	}
	if t.Weight == 0 {
		t.Weight = 1
	}
	return nil
}

// AddTriple persists a single triple. ID and CreatedAt are filled when zero.
func (ms *Store) AddTriple(ctx context.Context, t *Triple) error {
	if t == nil {
		return fmt.Errorf("triple is nil")
	}
	if err := t.validate(); err != nil {
		return err
	}
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = ms.now().UTC()
	}

	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	_, err := ms.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO memory_triples
		(id, subj, rel, obj, memory_id, link_type, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, t.ID, t.Subject, t.Relation, t.Object, t.MemoryID, string(t.LinkType), t.Weight, t.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("failed to insert triple: %w", err)
	}
	return nil
}

// AddTriples persists a batch of triples in a single transaction. On any
// per-row error the whole batch rolls back so callers see all-or-nothing
// behaviour. ID/CreatedAt are filled per row.
func (ms *Store) AddTriples(ctx context.Context, triples []*Triple) error {
	if len(triples) == 0 {
		return nil
	}

	for _, t := range triples {
		if err := t.validate(); err != nil {
			return err
		}
		if t.ID == "" {
			t.ID = uuid.New().String()
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = ms.now().UTC()
		}
	}

	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	tx, err := ms.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO memory_triples
		(id, subj, rel, obj, memory_id, link_type, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, t := range triples {
		if _, err := stmt.ExecContext(ctx,
			t.ID, t.Subject, t.Relation, t.Object,
			t.MemoryID, string(t.LinkType), t.Weight, t.CreatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("failed to insert triple %s: %w", t.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	return nil
}

// TriplesForMemory returns every triple whose provenance is the given memory.
// Order: created_at ASC then id ASC for stable iteration.
func (ms *Store) TriplesForMemory(ctx context.Context, memoryID string) ([]Triple, error) {
	return ms.queryTriples(ctx, `
		SELECT id, subj, rel, obj, memory_id, link_type, weight, created_at
		FROM memory_triples
		WHERE memory_id = ?
		ORDER BY created_at ASC, id ASC
	`, memoryID)
}

// TriplesBySubject returns every triple whose subj matches exactly. Used by
// the PPR walk seed expansion (slice 4) — exact-match is intentional: fuzzy
// expansion is the LLM's responsibility before hitting the graph.
func (ms *Store) TriplesBySubject(ctx context.Context, subj string) ([]Triple, error) {
	return ms.queryTriples(ctx, `
		SELECT id, subj, rel, obj, memory_id, link_type, weight, created_at
		FROM memory_triples
		WHERE subj = ?
		ORDER BY weight DESC, created_at ASC
	`, strings.TrimSpace(subj))
}

// TriplesByObject is the symmetric lookup used when walking the graph
// backwards (e.g., "what facts target this entity?"). Same ordering as
// TriplesBySubject for consistency.
func (ms *Store) TriplesByObject(ctx context.Context, obj string) ([]Triple, error) {
	return ms.queryTriples(ctx, `
		SELECT id, subj, rel, obj, memory_id, link_type, weight, created_at
		FROM memory_triples
		WHERE obj = ?
		ORDER BY weight DESC, created_at ASC
	`, strings.TrimSpace(obj))
}

// DeleteTriplesForMemory removes every triple tied to the given memory and
// returns the deleted count. Called from Store.Delete to maintain cascade
// semantics in Go (the SQLite FK pragma is not relied upon here so behaviour
// is portable across drivers / pragma states).
func (ms *Store) DeleteTriplesForMemory(ctx context.Context, memoryID string) (int, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return 0, nil
	}
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	res, err := ms.db.ExecContext(ctx, `DELETE FROM memory_triples WHERE memory_id = ?`, memoryID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete triples for memory %s: %w", memoryID, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (ms *Store) queryTriples(ctx context.Context, query string, args ...any) ([]Triple, error) {
	rows, err := ms.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("triple query failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Triple
	for rows.Next() {
		var t Triple
		var linkType string
		var createdAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.Subject, &t.Relation, &t.Object,
			&t.MemoryID, &linkType, &t.Weight, &createdAt); err != nil {
			return nil, fmt.Errorf("triple scan failed: %w", err)
		}
		t.LinkType = TripleLinkType(linkType)
		if createdAt.Valid {
			t.CreatedAt = createdAt.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
