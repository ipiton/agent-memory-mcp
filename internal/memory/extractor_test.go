package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeExtractorServer stands in for an OpenAI-compatible /chat/completions
// endpoint. The handler returns whatever JSON content the test puts on .body
// — the openaiTripleExtractor will then unmarshal that as the triples
// payload (chat.choices[0].message.content).
type fakeExtractorServer struct {
	t            *testing.T
	server       *httptest.Server
	body         string
	status       int
	calls        atomic.Int32
	authReceived atomic.Value // string
}

func newFakeExtractorServer(t *testing.T) *fakeExtractorServer {
	t.Helper()
	f := &fakeExtractorServer{t: t, status: http.StatusOK}
	f.body = `{"triples":[{"subj":"a","rel":"depends_on","obj":"b"}]}`

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.authReceived.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if f.status/100 != 2 {
			_, _ = io.WriteString(w, f.body)
			return
		}
		// Wrap configured body in a chat-completions envelope.
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+strconvQuote(f.body)+`}}]}`)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// strconvQuote sidesteps importing strconv in tests by inlining a tiny JSON
// string-encoder. Sufficient for our happy-path content; we don't put weird
// characters into the body field.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestOpenAIExtractor_HappyPath(t *testing.T) {
	srv := newFakeExtractorServer(t)
	srv.body = `{"triples":[
		{"subj":"auth_service", "rel":"depends_on", "obj":"postgres"},
		{"subj":"auth_service", "rel":"owns",       "obj":"session_token_table"}
	]}`

	extractor, err := NewOpenAIExtractor(OpenAIExtractorConfig{
		BaseURL: srv.server.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewOpenAIExtractor: %v", err)
	}

	mem := &Memory{
		ID:      "mem-1",
		Title:   "Auth migration",
		Type:    TypeSemantic,
		Content: "auth_service migrated session storage from Redis to PostgreSQL.",
	}
	got, err := extractor.Extract(t.Context(), mem)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d triples, want 2", len(got))
	}
	if got[0].Subject != "auth_service" || got[0].Relation != "depends_on" || got[0].Object != "postgres" {
		t.Errorf("triple[0] = %+v, want auth_service/depends_on/postgres", got[0])
	}
	if got[0].MemoryID != mem.ID {
		t.Errorf("triple[0].MemoryID = %q, want %q", got[0].MemoryID, mem.ID)
	}
	if got[0].LinkType != LinkTypeExtracted {
		t.Errorf("triple[0].LinkType = %q, want extracted", got[0].LinkType)
	}
	if auth, _ := srv.authReceived.Load().(string); !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("expected Bearer auth header, got %q", auth)
	}
}

func TestOpenAIExtractor_SkipsInvalidTriples(t *testing.T) {
	srv := newFakeExtractorServer(t)
	srv.body = `{"triples":[
		{"subj":"auth_service", "rel":"depends_on", "obj":"postgres"},
		{"subj":"",             "rel":"orphan",     "obj":"missing_subj"},
		{"subj":"x",            "rel":"",           "obj":"missing_rel"},
		{"subj":"valid",        "rel":"emits",      "obj":"event"}
	]}`

	extractor, err := NewOpenAIExtractor(OpenAIExtractorConfig{
		BaseURL: srv.server.URL,
		APIKey:  "k",
		Model:   "m",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewOpenAIExtractor: %v", err)
	}
	got, err := extractor.Extract(t.Context(), &Memory{ID: "m1", Content: "anything"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 valid triples (invalids skipped), got %d", len(got))
	}
}

func TestOpenAIExtractor_HandlesMalformedJSON(t *testing.T) {
	srv := newFakeExtractorServer(t)
	srv.body = `not json at all`
	extractor, err := NewOpenAIExtractor(OpenAIExtractorConfig{
		BaseURL: srv.server.URL,
		APIKey:  "k",
		Model:   "m",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewOpenAIExtractor: %v", err)
	}
	if _, err := extractor.Extract(t.Context(), &Memory{ID: "m1", Content: "x"}); err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}
}

func TestOpenAIExtractor_HTTPErrorPropagates(t *testing.T) {
	srv := newFakeExtractorServer(t)
	srv.status = http.StatusInternalServerError
	srv.body = `{"error":"upstream broke"}`

	extractor, err := NewOpenAIExtractor(OpenAIExtractorConfig{
		BaseURL: srv.server.URL,
		APIKey:  "k",
		Model:   "m",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewOpenAIExtractor: %v", err)
	}
	if _, err := extractor.Extract(t.Context(), &Memory{ID: "m1", Content: "x"}); err == nil {
		t.Fatalf("expected error for 500 response, got nil")
	}
}

func TestNewOpenAIExtractor_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  OpenAIExtractorConfig
	}{
		{"missing_base_url", OpenAIExtractorConfig{APIKey: "k", Model: "m"}},
		{"missing_api_key", OpenAIExtractorConfig{BaseURL: "https://x", Model: "m"}},
		{"missing_model", OpenAIExtractorConfig{BaseURL: "https://x", APIKey: "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewOpenAIExtractor(tc.cfg, zap.NewNop()); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// stubExtractor is a deterministic in-memory TripleExtractor for tests of
// the async fan-out path. It records the memories it saw and lets tests
// override the returned triples or simulate failure.
type stubExtractor struct {
	calls    atomic.Int32
	returnFn func(mem *Memory) ([]*Triple, error)
}

func (s *stubExtractor) Extract(_ context.Context, mem *Memory) ([]*Triple, error) {
	s.calls.Add(1)
	if s.returnFn != nil {
		return s.returnFn(mem)
	}
	return nil, nil
}

func TestStore_FiresExtractorAsynchronouslyOnStore(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	stub := &stubExtractor{
		returnFn: func(mem *Memory) ([]*Triple, error) {
			return []*Triple{
				{Subject: "a", Relation: "r", Object: "b", MemoryID: mem.ID},
				{Subject: "a", Relation: "r", Object: "c", MemoryID: mem.ID},
			}, nil
		},
	}
	store.SetTripleExtractor(stub)

	mem := &Memory{Content: "hello world"}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Fan-out is async; wait briefly for the goroutine to land. A short
	// poll loop avoids flaky absolute sleeps under loaded CI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.TriplesForMemory(ctx, mem.ID)
		if len(got) == 2 {
			return // ok
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, _ := store.TriplesForMemory(ctx, mem.ID)
	t.Fatalf("expected 2 triples persisted within 2s, got %d (extractor calls=%d)", len(got), stub.calls.Load())
}

func TestStore_ExtractorFailureDoesNotBlockIngest(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	stub := &stubExtractor{
		returnFn: func(mem *Memory) ([]*Triple, error) {
			return nil, io.ErrUnexpectedEOF
		},
	}
	store.SetTripleExtractor(stub)

	mem := &Memory{Content: "ingest must succeed even when extractor breaks"}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if mem.ID == "" {
		t.Fatalf("Store should still assign ID on extractor failure")
	}
}

func TestStore_NoExtractor_NoFanout(t *testing.T) {
	store := newTestStore(t)
	if store.activeTripleExtractor() != nil {
		t.Fatalf("default extractor should be nil")
	}
	// Storing a memory without an extractor must not panic and must not
	// persist any triples.
	mem := &Memory{Content: "no extractor wired"}
	if err := store.Store(t.Context(), mem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, _ := store.TriplesForMemory(t.Context(), mem.ID)
	if len(got) != 0 {
		t.Fatalf("expected zero triples without extractor, got %d", len(got))
	}
}

func TestStore_UpdateContent_ReExtractsTriples(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	currentContent := atomic.Value{}
	currentContent.Store("first content")

	stub := &stubExtractor{
		returnFn: func(mem *Memory) ([]*Triple, error) {
			// Snapshot the content so the test can verify the extractor
			// saw the up-to-date version.
			content, _ := mem.Content, mem.Content
			tag := "first"
			if strings.Contains(content, "second") {
				tag = "second"
			}
			return []*Triple{{Subject: tag, Relation: "based_on", Object: "memory", MemoryID: mem.ID}}, nil
		},
	}
	store.SetTripleExtractor(stub)

	mem := &Memory{Content: "first content describing service"}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	waitForTripleCount(t, store, mem.ID, 1)

	// Update the content; extractor must fire again with new content and
	// the prior triples must be replaced (not accumulated).
	if err := store.Update(ctx, mem.ID, Update{Content: "second content describing service"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.TriplesForMemory(ctx, mem.ID)
		if len(got) == 1 && got[0].Subject == "second" {
			return // success: replace-all happened
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := store.TriplesForMemory(ctx, mem.ID)
	t.Fatalf("expected single triple with subj=second, got %v", got)
}

func waitForTripleCount(t *testing.T, store *Store, memoryID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.TriplesForMemory(t.Context(), memoryID)
		if len(got) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := store.TriplesForMemory(t.Context(), memoryID)
	t.Fatalf("waited 2s, expected %d triples, got %d", want, len(got))
}
