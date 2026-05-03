package rag

import (
	"strings"
	"testing"
)

func TestParseMarkdownSkeleton_Hierarchy(t *testing.T) {
	doc := `Top intro paragraph.

## Alpha
Body of alpha section.

### Alpha child
Child body.

## Beta
Body of beta section.
`
	sections := parseMarkdownSkeleton(doc, "runbook-rollback")
	if len(sections) < 4 {
		t.Fatalf("expected >=4 sections (preamble + 3 named), got %d", len(sections))
	}

	// First emission is the preamble — content before any section is
	// flushed as a synthetic root with path=[docTitle].
	if got := sections[0].path; len(got) != 1 || got[0] != "runbook-rollback" {
		t.Fatalf("preamble path = %v, want [runbook-rollback]", got)
	}
	if !strings.Contains(sections[0].content, "Top intro paragraph") {
		t.Fatalf("preamble missing intro line: %q", sections[0].content)
	}

	// Find the "Alpha child" section and assert breadcrumb is the full
	// chain.
	var got *section
	for i := range sections {
		if sections[i].title == "Alpha child" {
			got = &sections[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("Alpha child section not emitted; got titles=%v", titlesOf(sections))
	}
	if want := []string{"runbook-rollback", "Alpha", "Alpha child"}; !slicesEqual(got.path, want) {
		t.Fatalf("Alpha child path = %v, want %v", got.path, want)
	}

	// Beta is a sibling of Alpha — must drop back to level 2 path.
	var beta *section
	for i := range sections {
		if sections[i].title == "Beta" {
			beta = &sections[i]
			break
		}
	}
	if beta == nil {
		t.Fatalf("Beta section missing")
	}
	if want := []string{"runbook-rollback", "Beta"}; !slicesEqual(beta.path, want) {
		t.Fatalf("Beta path = %v, want %v", beta.path, want)
	}
}

func TestParseMarkdownSkeleton_IgnoresHeadersInsideCodeFence(t *testing.T) {
	doc := "## Real section\n" +
		"intro text\n" +
		"```\n" +
		"# pretend heading inside fenced code\n" +
		"```\n" +
		"trailing text\n"

	sections := parseMarkdownSkeleton(doc, "doc")
	for _, s := range sections {
		if s.title == "pretend heading inside fenced code" {
			t.Fatalf("section %q must NOT be promoted to a header inside a code fence", s.title)
		}
	}
	// The "Real section" must still be picked up as a top-level h2.
	found := false
	for _, s := range sections {
		if s.title == "Real section" {
			found = true
			if !strings.Contains(s.content, "pretend heading inside fenced code") {
				t.Fatalf("fenced text should be part of section content; content=%q", s.content)
			}
		}
	}
	if !found {
		t.Fatalf("Real section was not parsed; titles=%v", titlesOf(sections))
	}
}

func TestSplitMarkdownWithBreadcrumbs_PrefixPerChunk(t *testing.T) {
	doc := `Preamble line.

## Section One
First section content.

## Section Two
Second section content.
`
	chunks := splitMarkdownWithBreadcrumbs(doc, "Deploy Runbook", 2000, 200, false)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (preamble + 2 sections), got %d:\n%v", len(chunks), chunks)
	}

	// Every chunk must begin with the breadcrumb prefix and contain the
	// associated section content.
	wantPrefixes := []string{
		"[Deploy Runbook]",
		"[Deploy Runbook > Section One]",
		"[Deploy Runbook > Section Two]",
	}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(chunks[i], prefix) {
			t.Fatalf("chunk %d missing prefix %q:\n%s", i, prefix, chunks[i])
		}
	}
	if !strings.Contains(chunks[0], "Preamble line") {
		t.Fatalf("preamble chunk missing body; got %q", chunks[0])
	}
	if !strings.Contains(chunks[1], "First section content") {
		t.Fatalf("Section One chunk missing body; got %q", chunks[1])
	}
	if !strings.Contains(chunks[2], "Second section content") {
		t.Fatalf("Section Two chunk missing body; got %q", chunks[2])
	}
}

func TestSplitMarkdownWithBreadcrumbs_OversizeSectionRepeatsBreadcrumb(t *testing.T) {
	body := strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel ", 200) // ~10000 bytes
	doc := "# Doc Title\n\n## Big Section\n" + body + "\n"

	chunks := splitMarkdownWithBreadcrumbs(doc, "Doc Title", 2000, 200, false)
	if len(chunks) < 2 {
		t.Fatalf("expected oversize section to split into multiple chunks, got %d", len(chunks))
	}

	prefix := "[Doc Title > Big Section]"
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, prefix) {
			t.Fatalf("chunk %d missing repeated breadcrumb %q:\n%s", i, prefix, chunk)
		}
	}
}

func TestSplitMarkdownWithBreadcrumbs_NoHeaders_FallsBackToSingleBreadcrumb(t *testing.T) {
	doc := "Plain prose without any markdown headers.\n\nSome more body text."
	chunks := splitMarkdownWithBreadcrumbs(doc, "Plain Doc", 2000, 200, false)
	if len(chunks) != 1 {
		t.Fatalf("expected exactly 1 chunk, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "[Plain Doc]") {
		t.Fatalf("chunk missing root breadcrumb; got %q", chunks[0])
	}
}

func TestSplitMarkdownWithBreadcrumbs_SkipsEmptyDoc(t *testing.T) {
	if got := splitMarkdownWithBreadcrumbs("", "Empty", 2000, 200, false); len(got) != 0 {
		t.Fatalf("empty doc should yield zero chunks, got %d (%v)", len(got), got)
	}
	if got := splitMarkdownWithBreadcrumbs("   \n\n\n", "Whitespace", 2000, 200, false); len(got) != 0 {
		t.Fatalf("whitespace-only doc should yield zero chunks, got %d (%v)", len(got), got)
	}
}

func TestFormatBreadcrumb(t *testing.T) {
	cases := []struct {
		name string
		path []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"Doc"}, "[Doc]"},
		{"deep", []string{"Doc", "A", "B", "C"}, "[Doc > A > B > C]"},
		{"trims_blank_segments", []string{"Doc", "", "  ", "C"}, "[Doc > C]"},
		{"collapses_adjacent_duplicates", []string{"Doc Title", "Doc Title", "Section"}, "[Doc Title > Section]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBreadcrumb(tc.path); got != tc.want {
				t.Fatalf("formatBreadcrumb(%v) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsNoisyTitle(t *testing.T) {
	noisy := []string{
		"Table of Contents",
		"table of contents",
		"TABLE OF CONTENTS",
		"TOC",
		"Contents",
		"References",
		"Bibliography",
		"Index",
		"Changelog",
		"Change Log",
		"Release Notes",
		"Acknowledgements",
		"Acknowledgments",
		"See also",
		"See Also",
		"Further Reading",
		"External Links",
		"Appendix A: References",
		"References:",
	}
	for _, title := range noisy {
		if !isNoisyTitle(title) {
			t.Errorf("isNoisyTitle(%q) = false, want true", title)
		}
	}

	clean := []string{
		"",
		"Overview",
		"Architecture",
		"Rollback procedure",
		"Reference implementation",   // contains "Reference" but not equal to "References"
		"Indexes (k8s pattern)",      // contains "Index" but not equal
		"Change tracking algorithm",  // contains "Change" but not "Changelog"
		"Release strategy",           // contains "Release" but not "Release Notes"
	}
	for _, title := range clean {
		if isNoisyTitle(title) {
			t.Errorf("isNoisyTitle(%q) = true, want false", title)
		}
	}
}

func TestIsNoisyPath_InheritsFromAncestor(t *testing.T) {
	cases := []struct {
		name string
		path []string
		want bool
	}{
		{"clean", []string{"Doc", "Architecture", "Components"}, false},
		{"direct_match", []string{"Doc", "References"}, true},
		{"child_of_noisy", []string{"Doc", "References", "External Links"}, true},
		{"grandchild_of_noisy", []string{"Doc", "Changelog", "v1.0", "Bug fixes"}, true},
		{"docTitle_named_References_NOT_noisy", []string{"References"}, false}, // path[0] is the docTitle, not a section
		{"sibling_of_noisy_is_not_noisy", []string{"Doc", "Architecture"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNoisyPath(tc.path); got != tc.want {
				t.Fatalf("isNoisyPath(%v) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestSplitMarkdownWithBreadcrumbs_DropsNoisySections(t *testing.T) {
	doc := `Preamble line.

## Architecture
Real architecture content.

## Table of Contents
- [Architecture](#architecture)
- [References](#references)

## Implementation
Real implementation content.

### Internals
Implementation deep dive.

## References
- [Wikipedia](https://example.com)
- [Source](https://example.org)

### External links
Should also be dropped (child of References).
`
	chunks := splitMarkdownWithBreadcrumbs(doc, "Deploy Runbook", 2000, 200, false)

	// Expected to keep: preamble + Architecture + Implementation + Internals = 4
	// Expected to drop: Table of Contents + References + External links = 3
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks (preamble + 3 real sections), got %d:\n%v", len(chunks), chunks)
	}

	body := strings.Join(chunks, "\n---\n")
	for _, mustHave := range []string{"Preamble line", "Real architecture content", "Real implementation content", "Implementation deep dive"} {
		if !strings.Contains(body, mustHave) {
			t.Errorf("output missing required section content %q\nfull:\n%s", mustHave, body)
		}
	}
	for _, mustDrop := range []string{"Table of Contents", "Wikipedia", "Should also be dropped"} {
		if strings.Contains(body, mustDrop) {
			t.Errorf("output contains noisy content %q\nfull:\n%s", mustDrop, body)
		}
	}
}

func TestSplitMarkdownWithBreadcrumbs_KeepNoiseEscapeHatch(t *testing.T) {
	doc := `## References
- [Wikipedia](https://example.com)

## Real
Real content.
`
	chunks := splitMarkdownWithBreadcrumbs(doc, "Doc", 2000, 200, true)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks with keepNoise=true, got %d:\n%v", len(chunks), chunks)
	}
	body := strings.Join(chunks, "\n---\n")
	if !strings.Contains(body, "Wikipedia") {
		t.Errorf("keepNoise=true should preserve References content; got:\n%s", body)
	}
}

func TestExtractBreadcrumb(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantPath []string
		wantBody string
		wantOK   bool
	}{
		{
			"happy_path_three_levels",
			"[Doc > Section > Sub]\n\nbody text here",
			[]string{"Doc", "Section", "Sub"},
			"body text here",
			true,
		},
		{
			"single_segment",
			"[Doc]\n\nplain body",
			[]string{"Doc"},
			"plain body",
			true,
		},
		{
			"no_brackets_returns_false",
			"plain content with no breadcrumb",
			nil,
			"plain content with no breadcrumb",
			false,
		},
		{
			"unclosed_bracket_returns_false",
			"[unclosed bracket only",
			nil,
			"[unclosed bracket only",
			false,
		},
		{
			"empty_brackets_returns_false",
			"[]\n\nbody",
			nil,
			"[]\n\nbody",
			false,
		},
		{
			"trims_blank_segments",
			"[Doc >  > Section]\n\nbody",
			[]string{"Doc", "Section"},
			"body",
			true,
		},
		{
			"only_strips_one_leading_blank_separator",
			"[Doc]\n\n\n\nbody after multiple blank lines",
			[]string{"Doc"},
			"body after multiple blank lines",
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, body, ok := ExtractBreadcrumb(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !slicesEqual(path, tc.wantPath) {
				t.Errorf("path = %v, want %v", path, tc.wantPath)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestSectionKey_RoundTripsThroughExtract(t *testing.T) {
	original := []string{"Deploy Runbook", "Rollback procedure", "Network failure"}
	chunkContent := "[" + strings.Join(original, " > ") + "]\n\nthe body"

	path, _, ok := ExtractBreadcrumb(chunkContent)
	if !ok {
		t.Fatalf("expected breadcrumb to parse, got ok=false")
	}
	if !slicesEqual(path, original) {
		t.Fatalf("round-trip path = %v, want %v", path, original)
	}
	if got, want := SectionKey(path), strings.Join(original, " > "); got != want {
		t.Fatalf("SectionKey = %q, want %q", got, want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func titlesOf(sections []section) []string {
	out := make([]string, len(sections))
	for i, s := range sections {
		out[i] = s.title
	}
	return out
}
