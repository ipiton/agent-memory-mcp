package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestOllamaModelSelection(t *testing.T) {
	for _, method := range []string{"document", "query", "batch"} {
		for _, tc := range []struct {
			name        string
			model       string
			unavailable bool
		}{
			{name: "configured", model: " embeddinggemma:300m "},
			{name: "configured unavailable", model: "embeddinggemma:300m", unavailable: true},
			{name: "default fallback"},
			{name: "blank keeps default fallback", model: " \t "},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				var mu sync.Mutex
				var requested []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode request: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					mu.Lock()
					requested = append(requested, request.Model)
					mu.Unlock()
					if request.Model == defaultOllamaPrimaryModel || (tc.unavailable && request.Model == "embeddinggemma:300m") {
						http.Error(w, "model not found", http.StatusNotFound)
						return
					}
					switch r.URL.Path {
					case "/api/embeddings":
						_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.1, 0.2, 0.3, 0.4}})
					case "/api/embed":
						_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float64{{0.1, 0.2, 0.3, 0.4}}})
					default:
						http.NotFound(w, r)
					}
				}))
				t.Cleanup(server.Close)
				e, err := New(Config{OllamaBaseURL: server.URL, OllamaModel: tc.model, Dimension: 4, Mode: "local-only"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(e.Close)
				var modelID string
				switch method {
				case "document":
					var result *EmbeddingResult
					result, err = e.EmbedDetailed(context.Background(), "document")
					if result != nil {
						modelID = result.ModelID
					}
				case "query":
					var result *EmbeddingResult
					result, err = e.EmbedQueryDetailed(context.Background(), "query")
					if result != nil {
						modelID = result.ModelID
					}
				case "batch":
					var result *BatchEmbeddingResult
					result, err = e.BatchEmbedDetailed(context.Background(), []string{"document"})
					if result != nil {
						modelID = result.ModelID
					}
				}
				selected := strings.TrimSpace(tc.model)
				wantRequests := []string{selected}
				if selected == "" {
					wantRequests = []string{defaultOllamaPrimaryModel, defaultOllamaBackupModel}
					selected = defaultOllamaBackupModel
				}
				mu.Lock()
				gotRequests := append([]string(nil), requested...)
				mu.Unlock()
				if !slices.Equal(gotRequests, wantRequests) {
					t.Fatalf("requested models = %v, want %v", gotRequests, wantRequests)
				}
				if tc.unavailable {
					if err == nil || !strings.Contains(err.Error(), selected) {
						t.Fatalf("error = %v, want failure naming %q without default fallback", err, selected)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantID := "ollama:" + server.URL + ":" + selected + ":4"
				if modelID != wantID || e.LastModelID() != wantID {
					t.Fatalf("model identity = %q, last = %q, want %q", modelID, e.LastModelID(), wantID)
				}
			})
		}
	}
}
