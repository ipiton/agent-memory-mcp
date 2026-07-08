package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestConsolePageServed(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, expected := range []string{"Retrieval Console", "/console/api/query", "Compare normal vs debug mode"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("console page missing %q", expected)
		}
	}
}

func TestConsoleAPIRequiresAuth(t *testing.T) {
	s := newTestServer(t, "secret-token-123")
	mux := buildMux(s)

	body, _ := json.Marshal(map[string]any{
		"mode":  "documents",
		"query": "ingress rollback",
	})
	req := httptest.NewRequest(http.MethodPost, "/console/api/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestConsoleAPIMemoryQuery(t *testing.T) {
	s := newMemoryTestServer(t)
	mux := buildMux(s)

	mem := &memory.Memory{
		Title:      "Rollback api",
		Content:    "rollback api deployment and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata:   map[string]string{"entity": "runbook", "service": "api"},
	}
	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"mode":        "memory",
		"query":       "rollback api deployment",
		"context":     "payments",
		"service":     "api",
		"memory_type": "procedural",
		"limit":       5,
	})
	req := httptest.NewRequest(http.MethodPost, "/console/api/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["mode"] != "memory" {
		t.Fatalf("mode = %v, want memory", result["mode"])
	}
	if int(result["result_count"].(float64)) != 1 {
		t.Fatalf("result_count = %v, want 1", result["result_count"])
	}
}
