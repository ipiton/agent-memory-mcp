package memory

import (
	"strings"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/textfmt"
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

// truncateRunesSuffix is retained as the package-local spelling of
// textfmt.TruncateSuffix. T90 D6: the implementation moved to textfmt so the
// dozen call sites across packages share one tested version instead of each
// package growing its own (and several regressing to a byte slice).
func truncateRunesSuffix(s string, maxRunes int, suffix string) string {
	return textfmt.TruncateSuffix(s, maxRunes, suffix)
}
