//go:build eval

package memory

// T119, second instrument. The first one — document retrieval over the task
// archive — turned out to have no headroom to give: Hit@5 was 0.9933 with long
// queries and 0.9680 with short ones, and every miss in the short run was a
// spec whose H1 is boilerplate ("Спецификация (Spec.md)"), i.e. a generator
// artifact rather than a retrieval failure. Engineering tasks are topically
// disjoint and each one's documents repeat each other, so finding *any* of a
// task's four files is easy no matter how the corpus is sized.
//
// The questions in the backlog are about memory recall, not document search:
// whether a different encoder retrieves better (T74), whether centering the
// vector space changes ranking (T76a), whether the graph walk earns its cost
// (T94). This harness measures that path directly — same machine-derived
// labels (a record's `context` is a task slug), the live bank as the corpus,
// and no indexing step because the embeddings are already there.
//
// First run: Hit@5 = 0.6232, MRR = 0.4922 over 345 queries — 130 misses, and
// the misses are ordinary tasks with ordinary titles, not generator artifacts.
// That is the headroom the document arm could not produce.
//
//	MCP_EVAL_MEMORY_DB     copy of memories.db — never the live file
//	MCP_EVAL_TASK_ARCHIVE  directory of <slug>/ task dirs, for query text
//	LLAMACPP_BASE_URL      encoder for the query side
//	MCP_EVAL_CENTERED      1 to score with mean-centered cosine (T76a)

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

type recallCase struct {
	slug     string
	question string
	expected map[string]struct{}
	// ageDays is how old the freshest expected record is. Age decay is a
	// multiplier on relevance, so a grid over half-lives measured on one pool
	// only re-derives "decay hurts old material" — which is its definition,
	// not evidence. Bucketing by this lets the same run answer the question
	// that actually decides a default: is there a bucket where decay helps?
	ageDays float64
}

// ageBucket labels a case for the per-bucket breakdown. Boundaries follow the
// half-life being questioned: inside one half-life, inside six, beyond.
func ageBucket(days float64) string {
	switch {
	case days <= 30:
		return "fresh(<=30d)"
	case days <= 180:
		return "mid(30-180d)"
	default:
		return "old(>180d)"
	}
}

func TestRecallEval(t *testing.T) {
	dbPath := os.Getenv("MCP_EVAL_MEMORY_DB")
	archive := os.Getenv("MCP_EVAL_TASK_ARCHIVE")
	baseURL := os.Getenv("LLAMACPP_BASE_URL")
	if dbPath == "" || archive == "" || baseURL == "" {
		t.Skip("set MCP_EVAL_MEMORY_DB (a copy!), MCP_EVAL_TASK_ARCHIVE and LLAMACPP_BASE_URL")
	}

	emb, err := embedder.New(embedder.Config{
		LlamaCPPBaseURL: baseURL,
		LlamaCPPModel:   envOr("LLAMACPP_EMBEDDING_MODEL", "embeddinggemma-300m"),
		Dimension:       768,
		Mode:            "local-only",
		Timeout:         30 * time.Second,
		MaxRetries:      2,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}

	store, err := NewStore(dbPath, embedder.AsService(emb), zap.NewNop())
	if err != nil {
		t.Fatalf("open store copy: %v", err)
	}
	defer func() { _ = store.Close() }()

	centered := envOr("MCP_EVAL_CENTERED", "0") == "1"
	store.SetRecallCentered(centered)

	// The harness exists to predict production, so it has to score the way
	// production scores. The first version of this test left age decay off
	// while the service runs it at 30 days by default — and the two interact:
	// decay is a multiplier on a score whose spread centering deliberately
	// narrows, so a measurement without decay cannot see centering trading
	// relevance for recency. Defaults follow config.RecallHalfLifeDays.
	halfLife := 30.0
	if v := envOr("MCP_EVAL_HALFLIFE_DAYS", ""); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("MCP_EVAL_HALFLIFE_DAYS=%q: %v", v, err)
		}
		halfLife = parsed
	}
	store.SetRecallHalfLife(halfLife)

	// T121 sub-hypothesis: events and working state age on a calendar, a
	// pattern or a fact does not. Empty means every type decays.
	var decayTypes []Type
	decaySpec := envOr("MCP_EVAL_DECAY_TYPES", "")
	for _, part := range strings.Split(decaySpec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			decayTypes = append(decayTypes, Type(part))
		}
	}
	store.SetRecallDecayTypes(decayTypes)

	cases := buildRecallCases(t, store, archive)
	if len(cases) == 0 {
		t.Fatal("no cases: does the bank's `context` match the archive's directory names?")
	}

	const k = 5
	hits, mrrSum, misses := 0, 0.0, 0
	type bucket struct{ n, hits int }
	buckets := map[string]*bucket{}
	for _, c := range cases {
		results, err := store.Recall(context.Background(), c.question, Filters{}, k)
		if err != nil {
			t.Fatalf("recall %s: %v", c.slug, err)
		}
		rank := -1
		for i, r := range results {
			if r == nil || r.Memory == nil {
				continue
			}
			if _, ok := c.expected[r.Memory.ID]; ok {
				rank = i
				break
			}
		}
		b := buckets[ageBucket(c.ageDays)]
		if b == nil {
			b = &bucket{}
			buckets[ageBucket(c.ageDays)] = b
		}
		b.n++
		if rank >= 0 {
			b.hits++
			hits++
			mrrSum += 1.0 / float64(rank+1)
			continue
		}
		misses++
		if misses <= 5 {
			t.Logf("  MISS %s q=%.70q", c.slug, c.question)
		}
	}

	n := float64(len(cases))
	t.Logf("recall eval: Hit@%d=%.4f MRR=%.4f (N=%d, centered=%v, halflife=%.0fd, decays=%s)",
		k, float64(hits)/n, mrrSum/n, len(cases), centered, halfLife, envOr("MCP_EVAL_DECAY_TYPES", "all"))
	for _, name := range []string{"fresh(<=30d)", "mid(30-180d)", "old(>180d)"} {
		if b := buckets[name]; b != nil && b.n > 0 {
			t.Logf("  bucket %-13s n=%3d Hit@%d=%.4f", name, b.n, k, float64(b.hits)/float64(b.n))
		}
	}
	t.Logf("misses: %d of %d (%.1f%% headroom)", misses, len(cases), 100*float64(misses)/n)
}

// buildRecallCases pairs every task slug that is both a memory `context` and a
// directory in the archive. Queries come from the spec title, with the slug
// scrubbed — a query carrying its own label would be answered by the label.
func buildRecallCases(t *testing.T, store *Store, archive string) []recallCase {
	t.Helper()

	byContext := map[string]map[string]struct{}{}
	freshest := map[string]time.Time{}
	for _, m := range store.ListLightweight(Filters{}) {
		if m.Context == "" {
			continue
		}
		if byContext[m.Context] == nil {
			byContext[m.Context] = map[string]struct{}{}
		}
		byContext[m.Context][m.ID] = struct{}{}
		if m.CreatedAt.After(freshest[m.Context]) {
			freshest[m.Context] = m.CreatedAt
		}
	}
	now := time.Now()

	slugs := make([]string, 0, len(byContext))
	for slug := range byContext {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	titleRe := regexp.MustCompile(`(?m)^#\s+(.*)$`)
	fmRe := regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)

	var cases []recallCase
	for _, slug := range slugs {
		ids := byContext[slug]
		if len(ids) < 3 {
			continue
		}
		spec, err := os.ReadFile(filepath.Join(archive, slug, "Spec.md"))
		if err != nil {
			continue
		}
		m := titleRe.FindStringSubmatch(fmRe.ReplaceAllString(string(spec), ""))
		if m == nil {
			continue
		}
		q := strings.TrimSpace(m[1])
		for _, prefix := range []string{"Specification:", "Спецификация:", "Spec:"} {
			q = strings.TrimSpace(strings.TrimPrefix(q, prefix))
		}
		for _, form := range []string{slug, strings.ToUpper(slug), strings.ReplaceAll(slug, "-", " ")} {
			q = strings.ReplaceAll(q, form, " ")
		}
		q = strings.Join(strings.Fields(q), " ")
		if len([]rune(q)) < 15 {
			continue
		}
		cases = append(cases, recallCase{
			slug:     slug,
			question: q,
			expected: ids,
			ageDays:  now.Sub(freshest[slug]).Hours() / 24,
		})
	}
	return cases
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
