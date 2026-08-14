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
			name: "(T83) Implementation complete ↔ Task complete same subject → suppress",
			a:    mem("Implementation complete: agent-cap-gate", "agent-cap-gate", nil),
			b:    mem("Task complete: agent-cap-gate", "agent-cap-gate", nil),
			want: false,
		},
		{
			name: "(T83) Task S1 complete ↔ Task S2 complete same subject → suppress",
			a:    mem("Task S1 complete: ml-dead-endpoint", "ml-dead-endpoint", nil),
			b:    mem("Task S2 complete: ml-dead-endpoint", "ml-dead-endpoint", nil),
			want: false,
		},
		{
			name: "(T83) Task started ↔ Epic complete same subject → suppress",
			a:    mem("Task started: model-autoprune", "model-autoprune", nil),
			b:    mem("Epic complete: model-autoprune", "model-autoprune", nil),
			want: false,
		},
		{
			name: "control: genuine lifecycle invalidation still flagged",
			a:    mem("API rate limit is 100/s", "api", map[string]string{memory.MetadataStatus: "outdated", "archived": "true"}),
			b:    mem("API rate limit is 500/s", "api", map[string]string{memory.MetadataStatus: "active"}),
			want: true,
		},
		{
			// T105: this used to be the control proving the keyword layer alive.
			// That layer scored 13 findings and 13 false positives on the live
			// bank and was removed, so a supersession announced only in prose is
			// no longer a signal. The pair below is the same claim made
			// explicit, and it is what the surviving signals read.
			name: "control: prose-only supersession no longer flagged",
			a:    mem("We use Postgres", "db", nil),
			b:    mem("Migrated to MySQL, Postgres removed", "db", nil),
			want: false,
		},
		{
			name: "control: the same change, recorded in the lifecycle field",
			a:    mem("We use Postgres", "db", map[string]string{"lifecycle_status": "outdated"}),
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

// TestContradictionIgnoresProseVocabulary pins the T105 removal by the shape
// that caused it, not by the absence of a function: a record whose own advice
// is to mark things superseded must not become a hub for everything
// semantically near it.
//
// The live measurement behind this: 1565 active records, 13 findings, 13 false,
// one record in 3 pairs and another in 3. Reintroducing a substring scan over
// prose — in either direction, disjunctive or conjunctive — fails here.
func TestContradictionIgnoresProseVocabulary(t *testing.T) {
	hub := mem("Pattern: put up a SUPERSEDED banner when routing changes", "marketing", nil)

	neighbours := []*memory.Memory{
		mem("Marketing artifacts carry external links", "marketing", nil),
		mem("Routing was migrated to the new gateway", "marketing", nil),
		mem("The old approach is no longer used here", "marketing", nil),
	}
	for _, n := range neighbours {
		if hasContradictionSignals(hub, n) {
			t.Errorf("hub paired with %q — prose vocabulary must not be a contradiction signal", n.Title)
		}
	}

	// Both sides carrying the vocabulary is agreement about a change, not a
	// disagreement — the reason a conjunctive rule was rejected too.
	agreeA := mem("We migrated to gRPC", "transport", nil)
	agreeB := mem("The transport migrated to gRPC last quarter", "transport", nil)
	if hasContradictionSignals(agreeA, agreeB) {
		t.Error("two records agreeing about a migration must not be a contradiction")
	}
}
