package server

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// TestPreviewText covers the three branches of the MCP_MEMORY_PREVIEW_RUNES
// policy plus the UTF-8 guarantee that motivated replacing the legacy
// byte-slice truncation (s[:300]) with rune-aware textfmt.Truncate.
func TestPreviewText(t *testing.T) {
	const defaultLimit = 300
	long := strings.Repeat("a", 400)
	cyr := strings.Repeat("я", 400) // 2 bytes/rune — a byte cut would split a codepoint

	cases := []struct {
		name          string
		override      int
		in            string
		wantRunes     int  // upper bound on rune count of the result
		wantFull      bool // result must equal the input verbatim (no truncation)
		wantTruncated bool // result must carry the "..." ellipsis
	}{
		{name: "default uses per-surface limit", override: 0, in: long, wantRunes: defaultLimit, wantTruncated: true},
		{name: "positive override forces cap", override: 50, in: long, wantRunes: 50, wantTruncated: true},
		{name: "negative disables truncation", override: -1, in: long, wantRunes: 400, wantFull: true},
		{name: "cyrillic cut on rune boundary", override: 100, in: cyr, wantRunes: 100, wantTruncated: true},
		{name: "short input under limit unchanged", override: 0, in: "short", wantRunes: 5, wantFull: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &MCPServer{config: config.Config{Memory: config.MemoryConfig{PreviewRunes: tc.override}}}
			got := s.previewText(tc.in, defaultLimit)

			if !utf8.ValidString(got) {
				t.Fatalf("previewText produced invalid UTF-8: %q", got)
			}
			if n := utf8.RuneCountInString(got); n > tc.wantRunes {
				t.Fatalf("rune count = %d, want <= %d", n, tc.wantRunes)
			}
			if tc.wantFull && got != tc.in {
				t.Fatalf("expected full input back, got %d runes (want %d)",
					utf8.RuneCountInString(got), utf8.RuneCountInString(tc.in))
			}
			if tc.wantTruncated && !strings.HasSuffix(got, "...") {
				t.Fatalf("expected truncated result to end with ellipsis, got %q", got)
			}
		})
	}
}
