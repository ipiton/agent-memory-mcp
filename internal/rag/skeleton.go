package rag

import (
	"regexp"
	"strings"
)

// Skeleton-aware chunking for Markdown documents (T49 slice 1).
//
// The naive splitter (splitIntoChunks) cuts a document by character count and
// loses the section it belongs to. For typed engineering artifacts (runbooks,
// ADRs, postmortems, CLAUDE.md, SPEC docs) we preserve hierarchy so retrieval
// returns chunks an LLM can place: each chunk gains a breadcrumb prefix
// "[doc title > section > subsection] ..." derived from the Markdown header
// tree, and chunks never cross section boundaries within a single source.

// markdownHeaderPattern matches an ATX-style header at the start of a line:
// 1-6 leading hashes, at least one space, the title, and an optional trailing
// hash sequence which is part of the standard but rarely used in our corpus.
var markdownHeaderPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// noisyTitlePatterns lists section titles whose contents almost never carry
// retrieval-useful information for our engineering corpus: tables of contents,
// reference lists, indexes, and changelog/acknowledgement boilerplate. The
// patterns are matched case-insensitively against the trimmed section title;
// any leading "Appendix N:" or trailing colon is normalised away first.
//
// Slice 3 of T49 implements heuristic-only noise filtering. A future LLM
// classifier (per the original spec) can extend this list at ingest time
// without changing the splitter API.
var noisyTitlePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^table\s+of\s+contents$`),
	regexp.MustCompile(`(?i)^toc$`),
	regexp.MustCompile(`(?i)^contents$`),
	regexp.MustCompile(`(?i)^references$`),
	regexp.MustCompile(`(?i)^bibliography$`),
	regexp.MustCompile(`(?i)^index$`),
	regexp.MustCompile(`(?i)^changelog$`),
	regexp.MustCompile(`(?i)^change\s+log$`),
	regexp.MustCompile(`(?i)^release\s+notes$`),
	regexp.MustCompile(`(?i)^acknowledg(e)?ments$`),
	regexp.MustCompile(`(?i)^see\s+also$`),
	regexp.MustCompile(`(?i)^further\s+reading$`),
	regexp.MustCompile(`(?i)^external\s+links$`),
}

// isNoisyTitle reports whether a single section title matches one of the
// noisyTitlePatterns. Punctuation noise (trailing colons, "Appendix A: " prefix)
// is stripped so common variations still match.
func isNoisyTitle(title string) bool {
	t := strings.TrimSpace(title)
	t = strings.TrimRight(t, ":")
	if idx := strings.Index(strings.ToLower(t), ": "); idx >= 0 && strings.HasPrefix(strings.ToLower(t), "appendix") {
		t = strings.TrimSpace(t[idx+2:])
	}
	for _, pattern := range noisyTitlePatterns {
		if pattern.MatchString(t) {
			return true
		}
	}
	return false
}

// isNoisyPath reports whether any breadcrumb segment (excluding the root
// docTitle at index 0) is a noisy section. A child of "## References" is
// itself noise — we drop the entire subtree.
func isNoisyPath(path []string) bool {
	// Skip path[0] — that is the root docTitle, not a section heading. A
	// document literally named "References" should not be filtered out
	// wholesale; only header-bound subtrees are.
	for i := 1; i < len(path); i++ {
		if isNoisyTitle(path[i]) {
			return true
		}
	}
	return false
}

// section is one node in the skeleton tree of a Markdown document.
// Path holds the breadcrumb from doc title down to this section (inclusive).
type section struct {
	level   int
	title   string
	path    []string
	content string
}

// splitMarkdownWithBreadcrumbs returns the chunks for a Markdown document with
// each chunk prefixed by its hierarchical breadcrumb. It splits content by
// header structure first, then applies the existing length-based splitter
// inside each section so oversize sections still fit chunkSize. Sections
// shorter than the chunk budget yield exactly one chunk regardless of
// chunkSize, preserving locality.
//
// docTitle is the root breadcrumb element (typically the H1 or filename).
// Pre-header content (anything before the first header) is emitted under the
// docTitle alone.
//
// When keepNoise is false (default), sections whose breadcrumb contains a
// noisy header title (Table of Contents / References / Index / Changelog /
// etc.) are dropped; their children inherit the noise flag through breadcrumb
// ancestry. Set keepNoise=true to disable filtering (used by the
// MCP_RAG_KEEP_NOISE escape hatch).
func splitMarkdownWithBreadcrumbs(content, docTitle string, chunkSize, overlap int, keepNoise bool) []string {
	sections := parseMarkdownSkeleton(content, docTitle)
	if len(sections) == 0 {
		return nil
	}

	var chunks []string
	for _, sec := range sections {
		body := strings.TrimSpace(sec.content)
		if body == "" {
			continue
		}
		if !keepNoise && isNoisyPath(sec.path) {
			continue
		}
		breadcrumb := formatBreadcrumb(sec.path)
		// Compute the budget left for the actual section text after the
		// breadcrumb prefix is accounted for.
		budget := chunkSize - len(breadcrumb)
		if budget < 200 {
			// Pathological config: chunkSize too small to fit a breadcrumb
			// plus useful context. Fall back to no-prefix splitting so we
			// don't emit chunks that are 90% breadcrumb.
			budget = chunkSize
			breadcrumb = ""
		}
		for _, piece := range splitTextByBudget(body, budget, overlap) {
			if breadcrumb == "" {
				chunks = append(chunks, piece)
			} else {
				chunks = append(chunks, breadcrumb+"\n\n"+piece)
			}
		}
	}
	return chunks
}

// parseMarkdownSkeleton walks the document line by line, tracks fenced code
// blocks (``` and ~~~), and emits one section per heading the parser has
// finished accumulating body for. Pre-header text becomes a synthetic root
// section with path=[docTitle]. Ancestor sections stay on the stack so a
// child heading inherits the full breadcrumb chain.
func parseMarkdownSkeleton(content, docTitle string) []section {
	docTitle = strings.TrimSpace(docTitle)
	if docTitle == "" {
		docTitle = "document"
	}

	lines := strings.Split(content, "\n")
	var (
		sections    []section
		stack       []section
		currentBody strings.Builder
		fence       string
	)

	// closeCurrent finalises the body that has been accumulating since the
	// most recent section transition. It emits the top-of-stack section
	// (with its body) — or a preamble section under [docTitle] when the
	// stack is empty — without mutating the stack itself.
	closeCurrent := func() {
		body := currentBody.String()
		currentBody.Reset()
		if strings.TrimSpace(body) == "" {
			return
		}
		if len(stack) == 0 {
			sections = append(sections, section{
				level:   0,
				title:   docTitle,
				path:    []string{docTitle},
				content: body,
			})
			return
		}
		top := stack[len(stack)-1]
		top.content = body
		sections = append(sections, top)
	}

	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)

		// Track fenced code blocks so a "# heading" inside a code fence is
		// treated as content, not a section header.
		if fence == "" {
			if strings.HasPrefix(trimmed, "```") {
				fence = "```"
			} else if strings.HasPrefix(trimmed, "~~~") {
				fence = "~~~"
			}
		} else if strings.HasPrefix(trimmed, fence) {
			fence = ""
			currentBody.WriteString(raw)
			currentBody.WriteByte('\n')
			continue
		}

		if fence != "" {
			currentBody.WriteString(raw)
			currentBody.WriteByte('\n')
			continue
		}

		match := markdownHeaderPattern.FindStringSubmatch(raw)
		if match == nil {
			currentBody.WriteString(raw)
			currentBody.WriteByte('\n')
			continue
		}

		level := len(match[1])
		title := strings.TrimSpace(match[2])

		// Body accumulated so far belongs to the previous section (or to
		// the doc preamble when no section is open yet). Emit it now.
		closeCurrent()

		// Pop stack frames whose level is at or below the new header's:
		// they are siblings or shallower, not ancestors of this heading.
		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			stack = stack[:len(stack)-1]
		}

		// Build the breadcrumb path: docTitle plus every ancestor still on
		// the stack plus the current title.
		path := make([]string, 0, len(stack)+2)
		path = append(path, docTitle)
		for _, ancestor := range stack {
			path = append(path, ancestor.title)
		}
		path = append(path, title)

		stack = append(stack, section{level: level, title: title, path: path})
	}

	// Flush the final body to whatever section is currently on top of the
	// stack (or as a preamble for header-less docs).
	closeCurrent()
	return sections
}

// breadcrumbSeparator is the visible delimiter between breadcrumb segments.
// Centralised so ExtractBreadcrumb / formatBreadcrumb stay in lock-step and
// downstream callers (vectorstore section expansion, retrieval grouping)
// share the same key shape.
const breadcrumbSeparator = " > "

// ExtractBreadcrumb parses the leading "[doc > section > subsection]" prefix
// emitted by splitMarkdownWithBreadcrumbs and returns the path segments. It
// returns ok=false when the content has no breadcrumb (legacy non-Markdown
// chunks, content rewritten by an LLM, etc.); callers should treat the
// chunk as pathless. The returned body is the chunk text with the breadcrumb
// header (and the immediately-following blank separator line) stripped.
func ExtractBreadcrumb(content string) (path []string, body string, ok bool) {
	if len(content) < 2 || content[0] != '[' {
		return nil, content, false
	}
	end := strings.IndexByte(content, ']')
	if end < 0 {
		return nil, content, false
	}
	inner := content[1:end]
	if strings.TrimSpace(inner) == "" {
		return nil, content, false
	}
	parts := strings.Split(inner, breadcrumbSeparator)
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return nil, content, false
	}
	rest := content[end+1:]
	rest = strings.TrimLeft(rest, "\n")
	return cleaned, rest, true
}

// SectionKey serialises a breadcrumb path back into the same canonical string
// format used inside the [...] prefix. It is the value callers should use to
// match sections across chunks (e.g., "Deploy Runbook > Rollback > Network").
func SectionKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, breadcrumbSeparator)
}

// formatBreadcrumb produces the prefix string injected at the top of each
// chunk. Format: "[doc > section > subsection]". Whitespace in titles is
// kept; breadcrumbSeparator is the path separator. Adjacent duplicate
// segments collapse — this triggers when the docTitle matches an explicit H1
// of the same name, which would otherwise emit "[Doc > Doc > Section]".
func formatBreadcrumb(path []string) string {
	if len(path) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(path))
	var prev string
	for _, part := range path {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == prev {
			continue
		}
		cleaned = append(cleaned, part)
		prev = part
	}
	if len(cleaned) == 0 {
		return ""
	}
	return "[" + strings.Join(cleaned, breadcrumbSeparator) + "]"
}

// splitTextByBudget reproduces the naive splitter's behaviour but operates on
// an arbitrary budget. It is intentionally a near-copy of splitIntoChunks so
// per-section splitting matches the tested non-Markdown path. When body fits
// in budget, the original body is returned as a single chunk (no whitespace
// reflow), which matches the original splitIntoChunks contract.
func splitTextByBudget(body string, budget, overlap int) []string {
	if budget <= 0 {
		return []string{body}
	}
	if len(body) <= budget {
		return []string{body}
	}
	step := budget - overlap
	if step <= 0 {
		step = budget
	}

	var chunks []string
	bodyLen := len(body)
	for start := 0; start < bodyLen; start += step {
		end := start + budget
		if end > bodyLen {
			end = bodyLen
		}
		if end < bodyLen {
			breakPoint := end
			for i := end; i > end-100 && i > start; i-- {
				if body[i] == ' ' || body[i] == '\n' {
					breakPoint = i
					break
				}
			}
			end = breakPoint
		}
		chunk := strings.TrimSpace(body[start:end])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end >= bodyLen {
			break
		}
	}
	return chunks
}
