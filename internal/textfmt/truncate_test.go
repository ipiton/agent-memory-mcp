package textfmt

import "testing"

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{name: "short ascii unchanged", input: "hello", maxRunes: 10, want: "hello"},
		{name: "exact fit", input: "hello", maxRunes: 5, want: "hello"},
		{name: "long ascii ellipsis", input: "hello world", maxRunes: 8, want: "hello..."},
		{name: "trims whitespace", input: "  hi  ", maxRunes: 5, want: "hi"},
		{name: "non-positive max", input: "hello", maxRunes: 0, want: ""},
		{name: "negative max", input: "hello", maxRunes: -3, want: ""},
		{name: "max < 3 hard cut", input: "hello", maxRunes: 2, want: "he"},
		// Cyrillic: each character is 2 bytes in UTF-8, 1 rune. Byte-based
		// truncation would split a multi-byte sequence and produce invalid
		// UTF-8 — the legacy lifecycle.truncate had this bug (Round 3 L17).
		{name: "cyrillic rune-aware", input: "Привет мир!", maxRunes: 7, want: "Прив..."},
		{name: "cyrillic exact fit", input: "Привет", maxRunes: 6, want: "Привет"},
		// Emoji: the running joke for naive truncators.
		{name: "emoji rune-aware", input: "👋🌍🚀✨🎉", maxRunes: 4, want: "👋..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.input, tc.maxRunes)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.input, tc.maxRunes, got, tc.want)
			}
		})
	}
}
