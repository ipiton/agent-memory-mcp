package memory

import (
	"strings"
	"unicode/utf8"
)

// sanitizeUTF8 replaces invalid UTF-8 byte sequences with the Unicode
// replacement character (U+FFFD), yielding a string safe to store and
// re-encode. It is a no-op for already-valid input. Used to repair rows whose
// content/title were byte-truncated mid-rune before the rune-aware fix (T87).
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// truncateRunesSuffix truncates s to at most maxRunes runes on a rune boundary
// (never splitting a multibyte character) and appends suffix when truncation
// occurred. Unlike a byte slice (s[:n]), this cannot produce invalid UTF-8 —
// the root-cause fix for corrupted Cyrillic/CJK content (T87).
func truncateRunesSuffix(s string, maxRunes int, suffix string) string {
	if maxRunes < 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + suffix
}
