// Package textfmt provides rune-aware text formatting helpers shared
// across the codebase. New callers should prefer textfmt.Truncate over
// the package-local truncate(s, max) helpers that several packages
// historically reinvented.
package textfmt

import (
	"strings"
	"unicode/utf8"
)

// TruncateSuffix shortens s to at most maxRunes Unicode code points and appends
// suffix when content was cut. The suffix is added beyond the budget, not
// inside it — use Truncate when the ellipsis must fit within maxRunes.
//
// This is the rune-aware replacement for the `if len(s) > n { s = s[:n] + "…" }`
// idiom, which is wrong twice over for non-ASCII text: `len` counts bytes, so a
// Cyrillic string is cut at roughly half the intended length, and the cut lands
// mid-codepoint, producing invalid UTF-8. That is the mechanism which left 69
// unreadable rows in the store (T87); the idiom then came back in a dozen
// places (T90 D6), so it lives here where it can be tested once.
//
// A negative maxRunes returns s unchanged.
func TruncateSuffix(s string, maxRunes int, suffix string) string {
	if maxRunes < 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + suffix
}

// Truncate returns s trimmed of leading/trailing whitespace and shortened
// to at most maxRunes Unicode code points, appending "..." when content
// was cut. Guarantees:
//   - rune-aware: never produces invalid UTF-8 for multibyte input (Cyrillic,
//     CJK, emoji). The byte-based `s[:max]` pattern that some legacy
//     truncators used can break a multi-byte sequence mid-codepoint.
//   - non-positive maxRunes returns "".
//   - maxRunes < 3 returns a hard cut (no room for the ellipsis).
//   - if the trimmed string already fits, the original (trimmed) value
//     is returned without ellipsis.
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes < 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// AlignRuneStart moves i back to the nearest byte index that begins a UTF-8
// rune, so s[i:] and s[:i] are both valid UTF-8. Indices at or past len(s)
// return len(s); negative indices return 0.
//
// This is for splitters that advance on a byte grid — a chunker stepping by
// chunkSize-overlap, a windowed scanner — where the offset is arithmetic rather
// than a scan result and therefore lands mid-codepoint on any non-ASCII text.
// The RAG index carried 2622 chunks (4.3%) whose body began mid-rune for
// exactly that reason (T118); T87 was the same defect on the memory side, and
// TruncateSuffix above is its fix for the truncation case.
func AlignRuneStart(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
