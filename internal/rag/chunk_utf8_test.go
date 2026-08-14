package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// T118: chunk windows advance by arithmetic (chunkSize - overlap), so on
// two-byte Cyrillic text the offset lands mid-codepoint roughly half the time.
// The live index carried 2622 such chunks (4.3%), every one of them broken at
// the first byte of the body — exactly where `start` lands.
func TestSplitTextByBudgetKeepsRunesWhole(t *testing.T) {
	// An odd-length ASCII prefix guarantees the byte grid falls out of step with
	// the two-byte runes that follow, which is what the live documents did.
	body := "x" + strings.Repeat("абвгдеёжзийклмноп", 200)

	for _, budget := range []int{101, 128, 257, 512} {
		for _, overlap := range []int{0, 17, 64} {
			if overlap >= budget {
				continue
			}
			chunks := splitTextByBudget(body, budget, overlap)
			if len(chunks) < 2 {
				t.Fatalf("budget=%d overlap=%d produced %d chunks, expected a real split",
					budget, overlap, len(chunks))
			}
			for i, chunk := range chunks {
				if !utf8.ValidString(chunk) {
					t.Errorf("budget=%d overlap=%d chunk %d is not valid UTF-8: %q",
						budget, overlap, i, chunk)
				}
			}
		}
	}
}

// Whitespace-free text defeats the break-point back-scan, so `end` is left on
// the raw arithmetic boundary — the case the back-scan silently covered before.
func TestSplitTextByBudgetKeepsRunesWholeWithoutWhitespace(t *testing.T) {
	body := "y" + strings.Repeat("значение", 400) // no spaces at all

	chunks := splitTextByBudget(body, 200, 20)
	if len(chunks) < 2 {
		t.Fatalf("expected a real split, got %d chunks", len(chunks))
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, chunk)
		}
	}
}

// The rune fix must not change what the splitter produces for ASCII, which is
// what the existing chunking tests and the indexed corpus assume.
func TestSplitTextByBudgetASCIIUnchanged(t *testing.T) {
	body := strings.Repeat("alpha beta gamma delta ", 100)

	chunks := splitTextByBudget(body, 128, 16)
	if len(chunks) < 2 {
		t.Fatalf("expected a real split, got %d chunks", len(chunks))
	}
	joined := strings.Join(chunks, "")
	for _, word := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(joined, word) {
			t.Errorf("word %q vanished from the split", word)
		}
	}
}
