package steward

import (
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func mem(title, context string, meta map[string]string) *memory.Memory {
	// Distinct non-empty IDs: real stored memories always have one, and the
	// supersession check compares SupersededBy against the other's ID — empty
	// strings would spuriously match.
	return &memory.Memory{ID: "id:" + title, Title: title, Context: context, Content: title, Type: memory.TypeSemantic, Metadata: meta}
}

// TestHasContradictionSignals_SuppressesFPClasses pins the T82 suppressions: the
// three non-terminal kinship/time-series/lifecycle classes must NOT be flagged
// as contradictions, while a genuine invalidation still is.
func TestHasContradictionSignals_SuppressesFPClasses(t *testing.T) {
	cases := []struct {
		name string
		a, b *memory.Memory
		want bool
	}{
		{
			name: "(a) terminal vs Pattern kin, same context → suppress",
			a:    mem("Task complete: refactor auth", "auth-refactor", nil),
			b:    mem("Pattern: prefer middleware for auth", "auth-refactor", nil),
			want: false,
		},
		{
			name: "(a) terminal vs Pattern, different context → not suppressed by this class",
			a:    mem("Task complete: refactor auth", "auth-refactor", nil),
			b:    mem("Pattern: prefer middleware for auth", "other-task", nil),
			want: false, // no other signal either, but proves (a) requires same context
		},
		{
			name: "(b) two Strategy review snapshots → suppress",
			a:    mem("Strategy review 2026-05-04", "strategy", nil),
			b:    mem("Strategy review 2026-05-11", "strategy", nil),
			want: false,
		},
		{
			name: "(c) Task started ↔ Task complete same subject → suppress",
			a:    mem("Task started: migrate db", "db-migrate", nil),
			b:    mem("Task complete: migrate db", "db-migrate", nil),
			want: false,
		},
		{
			name: "control: genuine lifecycle invalidation still flagged",
			a:    mem("API rate limit is 100/s", "api", map[string]string{memory.MetadataStatus: "outdated", "archived": "true"}),
			b:    mem("API rate limit is 500/s", "api", map[string]string{memory.MetadataStatus: "active"}),
			want: true,
		},
		{
			name: "control: contradiction keyword still flagged",
			a:    mem("We use Postgres", "db", nil),
			b:    mem("Migrated to MySQL, Postgres removed", "db", nil),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasContradictionSignals(tc.a, tc.b); got != tc.want {
				t.Fatalf("hasContradictionSignals = %v, want %v", got, tc.want)
			}
		})
	}
}
