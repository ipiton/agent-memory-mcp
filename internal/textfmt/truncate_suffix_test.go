package textfmt

import (
	"testing"
	"unicode/utf8"
)

// T90 D6. The idiom this replaces — `if len(s) > n { s = s[:n] + "…" }` — is
// wrong twice for non-ASCII: len counts bytes, so a Cyrillic string is cut at
// roughly half the intended length, and the cut lands mid-codepoint. The second
// half is what left 69 unreadable rows in the store (T87).
func TestTruncateSuffixKeepsCyrillicValid(t *testing.T) {
	// 40 Cyrillic runes, 80 bytes.
	s := "переиндексация корпуса документов диска"
	for utf8.RuneCountInString(s) < 40 {
		s += "я"
	}

	got := TruncateSuffix(s, 20, "...")

	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if want := 20 + utf8.RuneCountInString("..."); utf8.RuneCountInString(got) != want {
		t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(got), want)
	}
	// The byte-based idiom would have cut at byte 20, i.e. 10 runes.
	if runes := []rune(got); string(runes[:20]) != string([]rune(s)[:20]) {
		t.Fatalf("prefix = %q, want the first 20 runes of the input", string(runes[:20]))
	}
}

// The cut must land on a rune boundary at every offset, not just convenient ones.
func TestTruncateSuffixNeverSplitsARune(t *testing.T) {
	inputs := []string{
		"переиндексация корпуса",
		"日本語のテキストを切り詰める",
		"emoji: 🚀🔥🧊 and more 🎯",
		"mixed кириллица and ascii",
	}
	for _, in := range inputs {
		for n := range utf8.RuneCountInString(in) + 2 {
			got := TruncateSuffix(in, n, "…")
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateSuffix(%q, %d) produced invalid UTF-8: %q", in, n, got)
			}
		}
	}
}

func TestTruncateSuffixBoundaries(t *testing.T) {
	if got := TruncateSuffix("abc", 3, "..."); got != "abc" {
		t.Errorf("exact fit = %q, want %q (no suffix)", got, "abc")
	}
	if got := TruncateSuffix("abcd", 3, "..."); got != "abc..." {
		t.Errorf("over budget = %q, want %q", got, "abc...")
	}
	if got := TruncateSuffix("abc", 0, "..."); got != "..." {
		t.Errorf("zero budget = %q, want %q", got, "...")
	}
	if got := TruncateSuffix("abc", -1, "..."); got != "abc" {
		t.Errorf("negative budget = %q, want the input unchanged", got)
	}
	if got := TruncateSuffix("", 5, "..."); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}
