package memory

import (
	"context"
	"testing"
)

// T122: the record that triggered the task — a body of nothing but promotion
// pointers, which out-ranked the answer to a query about promotion.
const promotionJournal = `- Promoted canonical: 68d40320-1111-4c11-9c11-111111111111
- Promoted canonical: aa0bfc8f-2222-4c22-9c22-222222222222
- Promoted canonical: 180bd48a-3333-4c33-9c33-333333333333`

func TestIsActivityLogOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"promotion journal", promotionJournal, true},
		{"single pointer bullet", "- Document search: провенанс канона", true},
		{"star marker", "* Memory recall: как чинили гонку", true},
		{"blank lines ignored", "\n- Repo search: read.go\n\n- Inspected file: x.go\n", true},
		{
			// The whole point of the whitelist: one line of knowledge and the
			// record is knowledge, whatever surrounds it.
			"one prose line among bullets",
			"- Document search: провенанс\nПромоушен требует доверенного провенанса, иначе запись уходит в review.",
			false,
		},
		{
			// Deliberately not in the whitelist: the value is another record's
			// content, so the line carries the knowledge it copied.
			"stored memory bullet",
			"- Stored memory: Task PERF-001 complete. Refactored 12 sequential SQL into 2 queries.",
			false,
		},
		{
			// Stays out of the write verdict: T85's caution about a hand-written
			// root-cause bullet is right, and refusing a write is irreversible.
			// It is excluded from *selection* instead — see the test below.
			"incident investigation stays writable",
			"- Incident investigation: ratchet порог поднят метрика quality gate регрессия",
			false,
		},
		{"prose with colon", "Fixed the search: it now works", false},
		{"empty", "", false},
		{"whitespace", "  \n\t\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsActivityLogOnly(tc.in); got != tc.want {
				t.Errorf("IsActivityLogOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The exclusion has to reach Recall, not just the predicate: before T122 the
// journal record won the very query it carried no answer to.
func TestRecallSkipsActivityJournal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	journal := &Memory{
		Title:      "Session close / canon-promotion",
		Content:    promotionJournal,
		Type:       TypeEpisodic,
		Context:    "canon-promotion",
		Importance: 0.9, // deliberately outranks the answer on every other axis
	}
	if err := store.Store(ctx, journal); err != nil {
		t.Fatalf("store journal: %v", err)
	}

	answer := &Memory{
		Title:      "Гейт промоушена в канон",
		Content:    "Promoted canonical записи требуют доверенного провенанса; иначе промоушен уходит в review.",
		Type:       TypeSemantic,
		Context:    "canon-promotion",
		Importance: 0.2,
	}
	if err := store.Store(ctx, answer); err != nil {
		t.Fatalf("store answer: %v", err)
	}

	results, err := store.Recall(ctx, "promoted canonical", Filters{}, 10)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.ID == journal.ID {
			t.Fatalf("journal record surfaced in semantic recall (%d results)", len(results))
		}
	}
	if len(results) == 0 {
		t.Fatal("recall returned nothing — the answer must still be reachable")
	}

	// It must remain reachable to the maintenance/queue path, which is what
	// keeps the unprocessed-summary backlog (T78) intact.
	listed, err := store.List(ctx, Filters{Context: "canon-promotion"}, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, m := range listed {
		if m.ID == journal.ID {
			found = true
		}
	}
	if !found {
		t.Error("journal record disappeared from List — exclusion must apply to selection only")
	}
}

// The write verdict and the selection verdict are different questions with
// different costs, and "incident investigation" is the label that forced them
// apart: refusing the write could destroy a real root-cause report, while
// skipping it in ranking costs nothing — the record stays in the bank and in
// List. Auto-capture puts the search *query* on that line, so its vector is
// built from the question and out-ranks the answer (the T84 mechanism).
func TestIncidentInvestigationIsSelectionOnly(t *testing.T) {
	const autoCaptured = "- Incident investigation: ratchet порог поднят метрика quality gate регрессия"

	if IsActivityLogOnly(autoCaptured) {
		t.Error("the write boundary must still accept it — T85's caution stands")
	}
	if !IsActivityLogForSelection(autoCaptured) {
		t.Error("semantic selection must skip it — this is the record that out-ranked the answer")
	}

	// One line of real content and it competes again, on both verdicts.
	withFinding := autoCaptured + "\nПервопричина: порог снапнут по ложному улучшению метрики."
	if IsActivityLogForSelection(withFinding) {
		t.Error("a bullet next to a finding must stay in selection")
	}
}

func TestRecallSkipsAutoCapturedIncidentQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	log := &Memory{
		Title:      "Session close",
		Content:    "- Incident investigation: провенанс гейт промоушена канон",
		Type:       TypeEpisodic,
		Importance: 0.9,
	}
	if err := store.Store(ctx, log); err != nil {
		t.Fatalf("store log: %v", err)
	}
	answer := &Memory{
		Title:      "Гейт промоушена",
		Content:    "Промоушен в канон требует доверенного провенанса, иначе запись уходит в review.",
		Type:       TypeSemantic,
		Importance: 0.2,
	}
	if err := store.Store(ctx, answer); err != nil {
		t.Fatalf("store answer: %v", err)
	}

	results, err := store.Recall(ctx, "провенанс гейт промоушена канон", Filters{}, 10)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.ID == log.ID {
			t.Fatal("the auto-captured incident query surfaced in semantic recall")
		}
	}
	if len(results) == 0 {
		t.Fatal("the answer must still be reachable")
	}
}
