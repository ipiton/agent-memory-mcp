package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

func TestEmbedLocalOnlySkipsHostedProviders(t *testing.T) {
	var openAIHits atomic.Int32

	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIHits.Add(1)
		http.Error(w, "should not be called", http.StatusTeapot)
	}))
	defer openAIServer.Close()

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": []float64{0.1, 0.2, 0.3, 0.4},
		})
	}))
	defer ollamaServer.Close()

	e, err := New(Config{
		OpenAIToken:   "should-not-be-used",
		OpenAIBaseURL: openAIServer.URL,
		OllamaBaseURL: ollamaServer.URL,
		Dimension:     4,
		Mode:          "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	embedding, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embedding) != 4 {
		t.Fatalf("embedding length = %d, want 4", len(embedding))
	}
	if openAIHits.Load() != 0 {
		t.Fatalf("OpenAI server was called %d times, want 0", openAIHits.Load())
	}
}

func TestEmbedLocalOnlyReturnsSpecificError(t *testing.T) {
	e, err := New(Config{
		OllamaBaseURL: "http://127.0.0.1:1",
		Dimension:     4,
		Mode:          "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "local-only embedding mode failed") {
		t.Fatalf("error = %q, want local-only specific message", err.Error())
	}
}

func TestEmbedQueryDetailedUsesOpenAIAdapter(t *testing.T) {
	var authHeader string
	var requestBody struct {
		Input      string `json:"input"`
		Model      string `json:"model"`
		Dimensions int    `json:"dimensions"`
	}

	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.11, 0.22, 0.33, 0.44}, "index": 0},
			},
		})
	}))
	defer openAIServer.Close()

	e, err := New(Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: openAIServer.URL,
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := e.EmbedQueryDetailed(context.Background(), "hello adapters")
	if err != nil {
		t.Fatalf("EmbedQueryDetailed: %v", err)
	}

	if authHeader != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer token", authHeader)
	}
	if requestBody.Input != "hello adapters" {
		t.Fatalf("input = %q, want %q", requestBody.Input, "hello adapters")
	}
	if requestBody.Model != defaultOpenAIModel {
		t.Fatalf("model = %q, want %q", requestBody.Model, defaultOpenAIModel)
	}
	if requestBody.Dimensions != 4 {
		t.Fatalf("dimensions = %d, want 4", requestBody.Dimensions)
	}
	if got := len(result.Embedding); got != 4 {
		t.Fatalf("embedding length = %d, want 4", got)
	}
	wantModelID := "openai:" + openAIServer.URL + ":" + defaultOpenAIModel + ":4"
	if result.ModelID != wantModelID {
		t.Fatalf("ModelID = %q, want %q", result.ModelID, wantModelID)
	}
	if e.LastModelID() != wantModelID {
		t.Fatalf("LastModelID = %q, want %q", e.LastModelID(), wantModelID)
	}
}

func TestBatchEmbedDetailedUsesOllamaAdapter(t *testing.T) {
	var batchHits atomic.Int32

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		batchHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{
				{0.1, 0.2, 0.3, 0.4},
				{0.5, 0.6, 0.7, 0.8},
			},
		})
	}))
	defer ollamaServer.Close()

	e, err := New(Config{
		OllamaBaseURL: ollamaServer.URL,
		Dimension:     4,
		Mode:          "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := e.BatchEmbedDetailed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("BatchEmbedDetailed: %v", err)
	}

	if batchHits.Load() != 1 {
		t.Fatalf("batch hits = %d, want 1", batchHits.Load())
	}
	if len(result.Embeddings) != 2 {
		t.Fatalf("embeddings count = %d, want 2", len(result.Embeddings))
	}
	if got := len(result.Embeddings[0]); got != 4 {
		t.Fatalf("first embedding length = %d, want 4", got)
	}
	wantModelID := "ollama:" + ollamaServer.URL + ":" + defaultOllamaPrimaryModel + ":4"
	if result.ModelID != wantModelID {
		t.Fatalf("ModelID = %q, want %q", result.ModelID, wantModelID)
	}
	if e.LastModelID() != wantModelID {
		t.Fatalf("LastModelID = %q, want %q", e.LastModelID(), wantModelID)
	}
}

func TestBatchEmbedDetailedFallsBackAfterHostedDimensionMismatch(t *testing.T) {
	var openAIHits atomic.Int32
	var ollamaHits atomic.Int32

	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		openAIHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
				{"embedding": []float64{0.4, 0.5, 0.6}, "index": 1},
			},
		})
	}))
	defer openAIServer.Close()

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		ollamaHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{
				{0.1, 0.2, 0.3, 0.4},
				{0.5, 0.6, 0.7, 0.8},
			},
		})
	}))
	defer ollamaServer.Close()

	e, err := New(Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: openAIServer.URL,
		OllamaBaseURL: ollamaServer.URL,
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := e.BatchEmbedDetailed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("BatchEmbedDetailed: %v", err)
	}
	if openAIHits.Load() != 1 {
		t.Fatalf("OpenAI hits = %d, want 1", openAIHits.Load())
	}
	if ollamaHits.Load() != 1 {
		t.Fatalf("Ollama hits = %d, want 1", ollamaHits.Load())
	}
	if got := len(result.Embeddings[0]); got != 4 {
		t.Fatalf("first embedding length = %d, want 4", got)
	}
	wantModelID := "ollama:" + ollamaServer.URL + ":" + defaultOllamaPrimaryModel + ":4"
	if result.ModelID != wantModelID {
		t.Fatalf("ModelID = %q, want %q", result.ModelID, wantModelID)
	}
}

func TestEmbedUsesLlamaCPPAdapter(t *testing.T) {
	var path string
	var requestBody struct {
		Input string `json:"input"`
		Model string `json:"model"`
		// llama.cpp does not accept dimensions; assert absence below.
		Dimensions *int `json:"dimensions"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.5, 0.6, 0.7, 0.8}, "index": 0},
			},
		})
	}))
	defer server.Close()

	e, err := New(Config{
		LlamaCPPBaseURL: server.URL + "/v1",
		Dimension:       4,
		Mode:            "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := e.EmbedDetailed(context.Background(), "hello llama")
	if err != nil {
		t.Fatalf("EmbedDetailed: %v", err)
	}
	if path != "/v1/embeddings" {
		t.Fatalf("path = %q, want /v1/embeddings", path)
	}
	if requestBody.Input != "hello llama" {
		t.Fatalf("input = %q, want %q", requestBody.Input, "hello llama")
	}
	if requestBody.Model != defaultLlamaCPPModel {
		t.Fatalf("model = %q, want %q", requestBody.Model, defaultLlamaCPPModel)
	}
	if requestBody.Dimensions != nil {
		t.Fatalf("dimensions = %v, want absent (llama.cpp ignores it)", *requestBody.Dimensions)
	}
	if len(result.Embedding) != 4 {
		t.Fatalf("embedding length = %d, want 4", len(result.Embedding))
	}
	wantModelID := "llamacpp:" + server.URL + "/v1:" + defaultLlamaCPPModel + ":4"
	if result.ModelID != wantModelID {
		t.Fatalf("ModelID = %q, want %q", result.ModelID, wantModelID)
	}
}

func TestBatchEmbedUsesLlamaCPPBeforeOllama(t *testing.T) {
	var llamaHits, ollamaHits atomic.Int32

	llamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		llamaHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0},
				{"embedding": []float64{0.5, 0.6, 0.7, 0.8}, "index": 1},
			},
		})
	}))
	defer llamaServer.Close()

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ollamaHits.Add(1)
		http.Error(w, "should not be reached", http.StatusTeapot)
	}))
	defer ollamaServer.Close()

	e, err := New(Config{
		LlamaCPPBaseURL: llamaServer.URL + "/v1",
		OllamaBaseURL:   ollamaServer.URL,
		Dimension:       4,
		Mode:            "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := e.BatchEmbedDetailed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("BatchEmbedDetailed: %v", err)
	}
	if llamaHits.Load() != 1 {
		t.Fatalf("llama.cpp hits = %d, want 1", llamaHits.Load())
	}
	if ollamaHits.Load() != 0 {
		t.Fatalf("Ollama hits = %d, want 0 (llama.cpp should win)", ollamaHits.Load())
	}
	if len(result.Embeddings) != 2 {
		t.Fatalf("embeddings count = %d, want 2", len(result.Embeddings))
	}
}

func TestLlamaCPPDisabledWhenBaseURLEmpty(t *testing.T) {
	e, err := New(Config{
		LlamaCPPModel: "bge-m3", // model set but no base URL → must stay disabled
		Dimension:     4,
		Mode:          "local-only",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, c := range e.singleCandidates("retrieval.passage") {
		if c.provider.name() == "llamacpp" {
			t.Fatal("llama.cpp must be opt-in via LLAMACPP_BASE_URL, but it joined the provider chain")
		}
	}
}

// TestOllamaNotDefaultedWhenLlamaCPPConfigured guards the fix for the dead
// Ollama fallback: with llama.cpp wired up and OLLAMA_BASE_URL empty, Ollama
// must NOT be force-defaulted into the chain (otherwise every llama.cpp failure
// triggered connection-refused retries against a host that was removed).
func TestOllamaNotDefaultedWhenLlamaCPPConfigured(t *testing.T) {
	e, err := New(Config{
		LlamaCPPBaseURL: "http://127.0.0.1:8090/v1",
		LlamaCPPModel:   "bge-m3",
		Dimension:       4,
		Mode:            "local-only",
		// OllamaBaseURL intentionally empty
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.config.OllamaBaseURL != "" {
		t.Fatalf("OllamaBaseURL was force-defaulted to %q despite llama.cpp being configured", e.config.OllamaBaseURL)
	}
	for _, c := range e.batchCandidates("retrieval.passage") {
		if strings.HasPrefix(c.provider.name(), "ollama/") {
			t.Fatalf("Ollama (%s) joined the batch chain despite empty OLLAMA_BASE_URL + configured llama.cpp", c.provider.name())
		}
	}

	// Conversely, with no local backend at all, Ollama still defaults (back-compat).
	e2, err := New(Config{Dimension: 4, Mode: "local-only"}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e2.config.OllamaBaseURL != defaultOllamaBaseURL {
		t.Fatalf("Ollama should default when no other backend is set, got %q", e2.config.OllamaBaseURL)
	}
}
