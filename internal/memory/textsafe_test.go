package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// brokenUTF8 is valid text with a truncated em-dash (U+2014 = e2 80 94) cut
// after two of its three bytes — exactly the mid-rune byte truncation T87 fixes.
var brokenUTF8 = "Forge Phase 2 " + string([]byte{0xe2, 0x80}) + " Adapter Layer"

func TestTruncateRunesSuffixNeverSplitsRune(t *testing.T) {
	// 10 Cyrillic runes (2 bytes each). A byte slice [:11] would split rune 6.
	s := strings.Repeat("я", 10)
	got := truncateRunesSuffix(s, 5, "…")
	if !utf8.ValidString(got) {
		t.Fatalf("produced invalid UTF-8: %q", got)
	}
	if got != strings.Repeat("я", 5)+"…" {
		t.Fatalf("got %q", got)
	}
	// No truncation when within budget.
	if truncateRunesSuffix(s, 10, "…") != s {
		t.Fatal("should not truncate when within budget")
	}
}

func TestSanitizeUTF8(t *testing.T) {
	if utf8.ValidString(brokenUTF8) {
		t.Fatal("fixture should be invalid UTF-8")
	}
	got := sanitizeUTF8(brokenUTF8)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitize left invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "Forge Phase 2 ") || !strings.HasSuffix(got, " Adapter Layer") {
		t.Fatalf("sanitize mangled surrounding text: %q", got)
	}
	// Valid input is returned unchanged (no allocation of replacement chars).
	if sanitizeUTF8("чистый текст") != "чистый текст" {
		t.Fatal("valid input must pass through unchanged")
	}
}

func TestValidateSanitizesInvalidUTF8(t *testing.T) {
	// T87: the write boundary sanitizes rather than rejects, so a caller that
	// log-and-continues never silently drops a record; stored bytes are valid.
	m := &Memory{Content: brokenUTF8, Title: brokenUTF8}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate should sanitize, not reject: %v", err)
	}
	if !utf8.ValidString(m.Content) || !utf8.ValidString(m.Title) {
		t.Fatalf("content/title still invalid after Validate: %q / %q", m.Content, m.Title)
	}
	if !strings.HasPrefix(m.Content, "Forge Phase 2 ") {
		t.Fatalf("sanitize mangled surrounding content: %q", m.Content)
	}
	// Valid UTF-8 passes through unchanged.
	clean := &Memory{Content: "чистый контент", Title: "нормальный заголовок"}
	if err := clean.Validate(); err != nil {
		t.Fatalf("valid UTF-8 should pass: %v", err)
	}
	if clean.Content != "чистый контент" || clean.Title != "нормальный заголовок" {
		t.Fatal("valid content must be unchanged")
	}
}

// TestStartupRepairsInvalidUTF8 inserts a corrupt row directly (bypassing the
// write-boundary guard), reopens the store, and verifies the repair migration
// rewrote it to valid UTF-8 while preserving the surrounding text.
func TestStartupRepairsInvalidUTF8(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "repair.db")

	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()
	_, err = store.db.Exec(
		`INSERT INTO memories (id, content, type, title, context, importance, access_count, created_at, updated_at, accessed_at, sediment_layer)
		 VALUES (?, ?, 'semantic', ?, '', 0.5, 0, ?, ?, ?, 'surface')`,
		"broken-1", brokenUTF8, brokenUTF8, now, now, now,
	)
	if err != nil {
		t.Fatalf("insert corrupt row: %v", err)
	}
	// Simulate a legacy store predating the T87 repair: reset the migration gate
	// so the reopen actually runs the repair (the first NewStore already stamped
	// user_version, which in production would still be 0 for the corrupt rows).
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	_ = store.Close()

	// Reopen — NewStore runs repairInvalidUTF8Memories.
	store2, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	got, err := store2.Get("broken-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !utf8.ValidString(got.Content) || !utf8.ValidString(got.Title) {
		t.Fatalf("row still invalid after repair: content=%q title=%q", got.Content, got.Title)
	}
	if !strings.HasPrefix(got.Content, "Forge Phase 2 ") {
		t.Fatalf("repair mangled content: %q", got.Content)
	}

	// Idempotent: a third open repairs nothing.
	var invalid int
	rows, err := store2.db.Query(`SELECT content, title FROM memories`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c, ti string
		if err := rows.Scan(&c, &ti); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !utf8.ValidString(c) || !utf8.ValidString(ti) {
			invalid++
		}
	}
	if invalid != 0 {
		t.Fatalf("expected 0 invalid rows after repair, got %d", invalid)
	}
}

// TestStoreRoundTripPreservesCyrillic guards the end-to-end write→read path for
// multibyte content (no corruption introduced by the store itself).
func TestStoreRoundTripPreservesCyrillic(t *testing.T) {
	store := newTestStore(t)
	content := "Исправил баг — обновил конфиг, закоммитил изменения"
	if err := store.Store(context.Background(), &Memory{ID: "cyr-1", Content: content, Type: TypeSemantic}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := store.Get("cyr-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != content {
		t.Fatalf("round-trip mismatch: %q", got.Content)
	}
}
