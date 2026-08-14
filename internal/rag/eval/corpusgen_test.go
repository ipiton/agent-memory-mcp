//go:build eval

package eval_test

// T119: the committed QA set (25 queries over a curated toy corpus) reports
// Hit@5 = 1.0000 and MRR = 0.9680, with an oracle reranker worth +0.032. Three
// backlog records — an embedding upgrade, a scoring change, a third retrieval
// leg — all say "decide by measured win", and none of them can be decided on a
// set with no room to win. This file builds a set that has room.
//
// The material is a by-product of how the system is already used: a memory
// entry's `context` is the slug of a task, and the same slug names a directory
// of that task's documents. So "query → the documents that answer it" is
// already labelled, by machine, without a single hour of annotation.
//
// The generated corpus is NOT committed. It is private product documentation
// and this repository is public; the generator writes into a gitignored
// directory and the committed toy corpus stays the CI gate.
//
//	make eval-corpus   # generate from a local task archive
//	make eval-real     # run against it with a real embedder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/rag/eval"
)

const (
	envTaskArchive = "MCP_EVAL_TASK_ARCHIVE" // directory of <slug>/ task dirs
	envOutDir      = "MCP_EVAL_OUT_DIR"      // where corpus/ and qa.json land
	envMaxTasks    = "MCP_EVAL_MAX_TASKS"    // cap on generated queries
	envQueryMode   = "MCP_EVAL_QUERY_MODE"   // title (default) or summary
)

// TestGenerateEvalCorpus is a generator, not an assertion: it is skipped
// unless MCP_EVAL_TASK_ARCHIVE points at a task archive. Kept as a test rather
// than a main package so it stays inside the tagged package that `make vet`
// checks — the T112 lesson was that untagged-invisible code rots.
func TestGenerateEvalCorpus(t *testing.T) {
	archive := os.Getenv(envTaskArchive)
	if archive == "" {
		t.Skipf("set %s to a directory of <slug>/ task dirs to generate", envTaskArchive)
	}
	outDir := os.Getenv(envOutDir)
	if outDir == "" {
		t.Fatalf("%s is required alongside %s", envOutDir, envTaskArchive)
	}
	maxTasks := 150
	if v := os.Getenv(envMaxTasks); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("%s=%q: want a positive integer", envMaxTasks, v)
		}
		maxTasks = n
	}
	queryMode := os.Getenv(envQueryMode)
	if queryMode == "" {
		queryMode = "title"
	}
	if queryMode != "title" && queryMode != "summary" {
		t.Fatalf("%s=%q: want title or summary", envQueryMode, queryMode)
	}

	entries, err := os.ReadDir(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	slugs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			slugs = append(slugs, e.Name())
		}
	}
	sort.Strings(slugs) // deterministic selection, not "whatever the FS returned"

	corpusDir := filepath.Join(outDir, "corpus")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatalf("mkdir corpus: %v", err)
	}

	var (
		queries   []eval.QAQuery
		copied    int
		skipped   int
		noSummary int
	)
	for _, slug := range slugs {
		if len(queries) >= maxTasks {
			break
		}
		taskDir := filepath.Join(archive, slug)
		spec, err := os.ReadFile(filepath.Join(taskDir, "Spec.md"))
		if err != nil {
			skipped++
			continue
		}
		question, ok := extractQuestion(string(spec), slug, queryMode)
		if !ok {
			noSummary++
			continue
		}

		docs, n, err := copyTaskDocs(taskDir, filepath.Join(corpusDir, slug), slug)
		if err != nil {
			t.Fatalf("copy %s: %v", slug, err)
		}
		if len(docs) == 0 {
			skipped++
			continue
		}
		copied += n
		queries = append(queries, eval.QAQuery{
			ID:             "task-" + slug,
			Type:           "task",
			Question:       question,
			ExpectedDocIDs: docs,
		})
	}

	if len(queries) == 0 {
		t.Fatalf("no usable task dirs under %s", archive)
	}

	qaPath := filepath.Join(outDir, "qa.json")
	data, err := json.MarshalIndent(queries, "", "  ")
	if err != nil {
		t.Fatalf("marshal qa: %v", err)
	}
	if err := os.WriteFile(qaPath, data, 0o644); err != nil {
		t.Fatalf("write qa: %v", err)
	}

	t.Logf("generated %d queries (mode=%s) over %d files → %s", len(queries), queryMode, copied, outDir)
	t.Logf("skipped: %d without Spec.md or docs, %d without a usable question", skipped, noSummary)
}

var (
	frontMatter  = regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`)
	summaryStart = regexp.MustCompile(`(?mi)^##\s+(summary|описание|обзор)\s*$`)
	nextHeading  = regexp.MustCompile(`(?m)^#{1,6}\s+`)
)

// extractQuestion builds the query text for one task.
//
// mode picks the difficulty lever, and it is a knob rather than a constant
// because the first run settled the question of which lever matters. With the
// 600-character Summary as the query, Hit@5 was 0.9933 over 150 tasks: a
// paragraph that long names the subsystem, the files and the decision, and no
// other task in the archive is about the same thing. Corpus size does not fix
// that — topical disjointness does not thin out with more tasks. Query length
// does, and short queries are also what anyone actually types.
//
//	summary — the Summary section (easy, kept for comparison)
//	title   — the H1 title alone (short, closer to a real question)
//
// The slug and its uppercase id form are scrubbed in both. Leaving them in
// would let a keyword match on the directory name stand in for retrieval — the
// query would be answered by its own label, and the measurement would say
// nothing about whether the pipeline found the right document.
func extractQuestion(spec, slug, mode string) (string, bool) {
	body := frontMatter.ReplaceAllString(spec, "")

	if mode == "title" {
		m := headingRe.FindStringSubmatch(body)
		if m == nil {
			return "", false
		}
		text := strings.TrimSpace(m[1])
		for _, prefix := range []string{"Specification:", "Спецификация:", "Spec:"} {
			text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
		text = strings.Join(strings.Fields(scrubSlug(text, slug)), " ")
		if len([]rune(text)) < 15 {
			return "", false
		}
		return text, true
	}

	var text string
	if loc := summaryStart.FindStringIndex(body); loc != nil {
		rest := body[loc[1]:]
		if next := nextHeading.FindStringIndex(rest); next != nil {
			rest = rest[:next[0]]
		}
		text = strings.TrimSpace(rest)
	}
	if text == "" {
		if m := headingRe.FindStringSubmatch(body); m != nil {
			text = strings.TrimSpace(m[1])
			text = strings.TrimPrefix(text, "Specification:")
			text = strings.TrimSpace(text)
		}
	}
	text = scrubSlug(text, slug)
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) < 40 {
		return "", false
	}
	if r := []rune(text); len(r) > 600 {
		text = string(r[:600])
	}
	return text, true
}

// scrubSlug removes the slug in every form it appears in — kebab, upper, and
// the space-separated words it is built from.
func scrubSlug(text, slug string) string {
	forms := []string{slug, strings.ToUpper(slug), strings.ReplaceAll(slug, "-", " "),
		strings.ReplaceAll(strings.ToUpper(slug), "-", " "), strings.ReplaceAll(slug, "-", "_")}
	for _, f := range forms {
		text = strings.ReplaceAll(text, f, " ")
	}
	return text
}

// copyTaskDocs copies the markdown of one task into the corpus and returns the
// corpus-relative paths, which are what the harness matches against.
//
// Spec.md is held out on purpose: the query is lifted from its Summary, and a
// corpus containing the query verbatim measures substring matching, not
// retrieval — the first generated set scored on exactly that and would have
// reproduced the saturation this whole exercise exists to escape. What remains
// (requirements, research, tasks, review findings) is about the same work
// without restating the question, which is the retrieval task we actually care
// about.
func copyTaskDocs(srcDir, dstDir, slug string) ([]string, int, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, 0, err
	}
	var docs []string
	copied := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if e.Name() == "Spec.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return nil, 0, err
		}
		if len(data) == 0 {
			continue
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return nil, 0, err
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), data, 0o644); err != nil {
			return nil, 0, err
		}
		docs = append(docs, fmt.Sprintf("%s/%s", slug, e.Name()))
		copied++
	}
	return docs, copied, nil
}
