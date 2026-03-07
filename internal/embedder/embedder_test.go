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
