package server

import "strings"

import "testing"

// T123: the activity line must point at the record, not copy it. Taking the
// content first made every "- Stored memory:" bullet a 220-character copy of a
// record that exists in the bank in full, and the copy competes with its own
// original — same vocabulary, less text, denser score. Measured on the live
// bank: 1020 such pairs, the copy above the original or displacing it in 6 of
// a 60-pair sample.
func TestStoreMemoryActivityLinePointsRatherThanCopies(t *testing.T) {
	const body = "Класс (ML-GATEWAY-IMAGEGEN-CANARY 2026-07-12, ml-service). СИМПТОМ. " +
		"Отбор предлагает кандидата, которого нечем вызвать — на gateway-пути это терминально."

	line := buildActivityLine("store_memory", map[string]any{
		"title":   "Pattern: канареечная активация без вызываемого кандидата",
		"content": body,
	})

	if strings.Contains(line, "СИМПТОМ") {
		t.Errorf("the line carries the record's body, so it duplicates it:\n  %s", line)
	}
	if !strings.Contains(line, "канареечная активация") {
		t.Errorf("the line lost the title, which is what makes it a pointer:\n  %s", line)
	}
}

// Without a title there is nothing to point at, so the content stays — 2 of the
// 1020 measured originals are in that state.
func TestStoreMemoryActivityLineFallsBackToContent(t *testing.T) {
	line := buildActivityLine("store_memory", map[string]any{
		"content": "Промоушен в канон требует доверенного провенанса.",
	})
	if !strings.Contains(line, "доверенного провенанса") {
		t.Errorf("untitled record lost its line entirely: %q", line)
	}
}

func TestUpdateMemoryActivityLinePrefersTitleThenID(t *testing.T) {
	withTitle := buildActivityLine("update_memory", map[string]any{
		"id":      "68d40320-1111-4c11-9c11-111111111111",
		"title":   "Гейт промоушена",
		"content": "длинное новое содержимое записи, которое не должно уехать в журнал",
	})
	if !strings.Contains(withTitle, "Гейт промоушена") || strings.Contains(withTitle, "длинное новое") {
		t.Errorf("update line = %q, want the title alone", withTitle)
	}

	idOnly := buildActivityLine("update_memory", map[string]any{
		"id":      "68d40320-1111-4c11-9c11-111111111111",
		"content": "длинное новое содержимое записи",
	})
	if !strings.Contains(idOnly, "68d40320") {
		t.Errorf("update line without a title = %q, want the id as the pointer", idOnly)
	}
}
