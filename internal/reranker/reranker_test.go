package reranker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNew_Disabled(t *testing.T) {
	for _, provider := range []string{"", "disabled", "DISABLED", "  "} {
		r, err := New(Config{Provider: provider}, zap.NewNop())
		if r != nil {
			t.Fatalf("provider=%q: Reranker = %v, want nil", provider, r)
		}
		if !errors.Is(err, ErrDisabled) {
			t.Fatalf("provider=%q: err = %v, want ErrDisabled", provider, err)
		}
	}
}

func TestNew_Unknown(t *testing.T) {
	r, err := New(Config{Provider: "cohere-fancy"}, zap.NewNop())
	if r != nil {
		t.Fatalf("Reranker = %v, want nil", r)
	}
	if !errors.Is(err, ErrProviderUnknown) {
		t.Fatalf("err = %v, want ErrProviderUnknown", err)
	}
}

func TestNew_Jina(t *testing.T) {
	r, err := New(Config{Provider: "jina", APIKey: "x"}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r == nil {
		t.Fatal("Reranker = nil, want non-nil jina adapter")
	}
}

func TestJina_ReordersByScore(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		TopN      int      `json:"top_n"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Return results reversed: last candidate ranked highest.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 2, "relevance_score": 0.9},
				{"index": 0, "relevance_score": 0.5},
				{"index": 1, "relevance_score": 0.1},
			},
		})
	}))
	defer srv.Close()

	r, err := New(Config{
		Provider: "jina",
		APIKey:   "tok-123",
		Endpoint: srv.URL,
		Model:    "jina-reranker-v2-base-multilingual",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	candidates := []Candidate{
		{ID: "a", Title: "A", Content: "alpha"},
		{ID: "b", Title: "B", Content: "beta"},
		{ID: "c", Title: "C", Content: "gamma"},
	}

	scored, err := r.Rerank(context.Background(), "search query", candidates)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}

	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	if gotBody.Model != "jina-reranker-v2-base-multilingual" {
		t.Fatalf("model = %q, want jina-reranker-v2-base-multilingual", gotBody.Model)
	}
	if gotBody.Query != "search query" {
		t.Fatalf("query = %q, want 'search query'", gotBody.Query)
	}
	if len(gotBody.Documents) != 3 {
		t.Fatalf("documents len = %d, want 3", len(gotBody.Documents))
	}
	if !strings.Contains(gotBody.Documents[0], "alpha") || !strings.Contains(gotBody.Documents[0], "A") {
		t.Fatalf("documents[0] = %q, want title+content", gotBody.Documents[0])
	}

	if len(scored) != 3 {
		t.Fatalf("scored len = %d, want 3", len(scored))
	}
	if scored[0].ID != "c" || scored[1].ID != "a" || scored[2].ID != "b" {
		t.Fatalf("scored order = [%s, %s, %s], want [c, a, b]", scored[0].ID, scored[1].ID, scored[2].ID)
	}
	if scored[0].Score != 0.9 {
		t.Fatalf("top score = %v, want 0.9", scored[0].Score)
	}
}

func TestJina_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r, err := New(Config{Provider: "jina", APIKey: "x", Endpoint: srv.URL}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.Rerank(context.Background(), "q", []Candidate{{ID: "a", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want to mention status 500", err)
	}
}

func TestJina_Timeout(t *testing.T) {
	// Handler stays in flight until either the client cancels (ctx done) or
	// the test closes the release channel. Closing release before srv.Close
	// keeps Close from blocking on hung goroutines if the client hadn't
	// cancelled yet.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	r, err := New(Config{Provider: "jina", APIKey: "x", Endpoint: srv.URL}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = r.Rerank(ctx, "q", []Candidate{{ID: "a", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestJina_EmptyCandidates(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	r, err := New(Config{Provider: "jina", APIKey: "x", Endpoint: srv.URL}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scored, err := r.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scored) != 0 {
		t.Fatalf("scored = %v, want empty", scored)
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits = %d, want 0 (no HTTP call for empty input)", hits.Load())
	}
}

func TestJina_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	r, err := New(Config{Provider: "jina", APIKey: "x", Endpoint: srv.URL}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = r.Rerank(context.Background(), "q", []Candidate{{ID: "a", Content: "hi"}})
	if err == nil {
		t.Fatal("expected decode error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want to mention 'decode'", err)
	}
}

func TestJina_IndexOutOfBounds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 42, "relevance_score": 0.9},
			},
		})
	}))
	defer srv.Close()

	r, err := New(Config{Provider: "jina", APIKey: "x", Endpoint: srv.URL}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Should not panic; must return an error.
	_, err = r.Rerank(context.Background(), "q", []Candidate{{ID: "a", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error on out-of-bounds index, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v, want to mention 'out of range'", err)
	}
}

func TestFormatDocument(t *testing.T) {
	cases := []struct {
		name string
		c    Candidate
		want string
	}{
		{"title+content", Candidate{Title: "T", Content: "C"}, "T\nC"},
		{"content only", Candidate{Content: "C"}, "C"},
		{"title only", Candidate{Title: "T"}, "T"},
		{"both empty", Candidate{}, ""},
		{"strips whitespace", Candidate{Title: "  T  ", Content: "\nC\n"}, "T\nC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatDocument(tc.c); got != tc.want {
				t.Fatalf("formatDocument(%+v) = %q, want %q", tc.c, got, tc.want)
			}
		})
	}
}
