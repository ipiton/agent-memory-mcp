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
	envCorpusName  = "MCP_EVAL_CORPUS_NAME"  // corpus (default) or corpus-permuted (T102)
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

// TestPermuteEvalCorpusHeadings writes a copy of the generated corpus with the
// heading *texts* permuted across the whole corpus, levels untouched.
//
// T102 asks whether a tree walk with summaries at the nodes could retrieve as
// well as the vector index. The donor (SegTreeMem) settles the analogous
// question with a permutation: destroy the structure's information and see
// whether quality moves. Ours is executable before a single LLM call, because
// the hierarchy is already extracted — every chunk carries a
// "[doc > section > subsection]" breadcrumb (T49).
//
// Permuting the texts while keeping the levels holds everything else fixed: the
// same number of headings, the same depth, the same amount of heading-shaped
// text in the corpus. What it removes is the association between a section's
// label and the content under it — which is the only thing a descent navigates
// by. If Hit@5 and MRR do not move, the labels carry no navigational signal on
// this corpus, and a tree leg has nothing to add that the flat index lacks.
//
// A ceiling does not block this test. T119 measured the document arm at
// Hit@5 0.97–0.99, which prevents detecting an improvement, not a degradation.
func TestPermuteEvalCorpusHeadings(t *testing.T) {
	outDir := os.Getenv(envOutDir)
	if outDir == "" {
		t.Skipf("set %s (see `make eval-corpus`) to build the permuted variant", envOutDir)
	}
	src := filepath.Join(outDir, "corpus")
	dst := filepath.Join(outDir, "corpus-permuted")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("no corpus at %s — run `make eval-corpus` first (%v)", src, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatalf("clear %s: %v", dst, err)
	}

	type headingRef struct {
		file  string
		line  int
		level string
	}
	var refs []headingRef
	var texts []string
	bodies := map[string][]string{}

	headingRe := regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(raw), "\n")
		bodies[path] = lines
		for i, line := range lines {
			if m := headingRe.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[2]) != "" {
				refs = append(refs, headingRef{file: path, line: i, level: m[1]})
				texts = append(texts, strings.TrimSpace(m[2]))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk corpus: %v", walkErr)
	}
	if len(refs) < 2 {
		t.Fatalf("corpus has %d headings — nothing to permute", len(refs))
	}

	// Deterministic derangement-ish shuffle: a fixed seed keeps the two arms
	// comparable across runs, and rotating by a stride coprime with the length
	// guarantees no heading keeps its own text.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].file != refs[j].file {
			return refs[i].file < refs[j].file
		}
		return refs[i].line < refs[j].line
	})
	sorted := append([]string(nil), texts...)
	sort.Strings(sorted)
	stride := len(sorted)/2 + 1
	for gcd(stride, len(sorted)) != 1 {
		stride++
	}
	for i, ref := range refs {
		lines := bodies[ref.file]
		lines[ref.line] = ref.level + " " + sorted[(i*stride)%len(sorted)]
	}

	written := 0
	for path, lines := range bodies {
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			t.Fatalf("rel %s: %v", path, relErr)
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
		written++
	}
	t.Logf("permuted corpus: %d documents, %d headings relabelled -> %s", written, len(refs), dst)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// TestFlattenEvalCorpusHeadings writes a copy of the corpus with every heading
// demoted to bold text: "## Гейт промоушена" becomes "**Гейт промоушена**".
//
// T102, discriminating arm. The permutation arm above moves heading *text*
// around, and a heading is two things at once — the structural label a descent
// would navigate by, and ordinary content whose words match a query. Its drop
// therefore cannot say which of the two carried the loss.
//
// Flattening separates them: the words stay exactly where they were, in the
// same document, in the same position, so lexical and semantic matching is
// untouched. What disappears is the section tree — the markdown parser sees no
// headings, so no chunk gets a meaningful breadcrumb. Baseline minus this arm
// is the structure's own contribution.
func TestFlattenEvalCorpusHeadings(t *testing.T) {
	outDir := os.Getenv(envOutDir)
	if outDir == "" {
		t.Skipf("set %s (see `make eval-corpus`) to build the flattened variant", envOutDir)
	}
	src := filepath.Join(outDir, "corpus")
	dst := filepath.Join(outDir, "corpus-flat")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("no corpus at %s — run `make eval-corpus` first (%v)", src, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatalf("clear %s: %v", dst, err)
	}

	headingRe := regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	docs, flattened := 0, 0
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if m := headingRe.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[2]) != "" {
				lines[i] = "**" + strings.TrimSpace(m[2]) + "**"
				flattened++
			}
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		docs++
		return os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644)
	})
	if walkErr != nil {
		t.Fatalf("walk corpus: %v", walkErr)
	}
	t.Logf("flattened corpus: %d documents, %d headings demoted to bold text -> %s", docs, flattened, dst)
}
