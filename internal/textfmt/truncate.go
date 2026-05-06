// Package textfmt provides rune-aware text formatting helpers shared
// across the codebase. New callers should prefer textfmt.Truncate over
// the package-local truncate(s, max) helpers that several packages
// historically reinvented.
package textfmt

import "strings"

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
