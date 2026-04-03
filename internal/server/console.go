package server

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

//go:embed console.html
var consoleHTML string

type consoleQueryRequest struct {
	Mode       string   `json:"mode"`
	Query      string   `json:"query"`
	SourceType string   `json:"source_type"`
	MemoryType string   `json:"memory_type"`
	Context    string   `json:"context"`
	Service    string   `json:"service"`
	Tags       []string `json:"tags"`
	Limit      int      `json:"limit"`
	Debug      bool     `json:"debug"`
}

type consoleQueryResponse struct {
	Mode        string `json:"mode"`
	Query       string `json:"query"`
	Debug       bool   `json:"debug"`
	ResultCount int    `json:"result_count"`
	Results     any    `json:"results"`
}

func buildHTTPMux(server *MCPServer) *http.ServeMux {
	mux := http.NewServeMux()
	authToken := server.config.HTTPAuthToken

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// CORS: deny cross-origin by default
		w.Header().Set("Access-Control-Allow-Origin", "")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// MCP Streamable HTTP: GET opens an SSE stream for server-initiated messages.
		// Per spec §5.2, servers that don't need server→client notifications keep the
		// stream open with periodic keepalive comments until the client disconnects.
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			if !authorizeHTTPRequest(w, r, authToken) {
				return
			}
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming not supported", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, ": stream open\n\n")
			flusher.Flush()

			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					_, _ = fmt.Fprintf(w, ": keepalive\n\n")
					flusher.Flush()
				}
			}
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !authorizeHTTPRequest(w, r, authToken) {
			return
		}

		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := errorResponse(nil, -32600, "Parse error", err.Error())
			writeJSON(w, http.StatusOK, resp)
			return
		}

		resp := server.handle(req)
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeHTTPRequest(w, r, authToken) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"rag_available":    server.ragEngine != nil,
			"memory_available": server.memoryStore != nil,
		})
	})

	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(consoleHTML))
	})

	mux.HandleFunc("/console/api/query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorizeHTTPRequest(w, r, authToken) {
			return
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

		var req consoleQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if req.Limit <= 0 {
			req.Limit = 10
		}
		if req.Limit > 50 {
			req.Limit = 50
		}

		filters := memory.Filters{Context: strings.TrimSpace(req.Context)}
		if memType := strings.TrimSpace(req.MemoryType); memType != "" && memType != "all" {
			parsedType, err := userio.ParseMemoryType(memType, "", true)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			filters.Type = parsedType
		}
		if len(req.Tags) > 0 {
			filters.Tags = userio.NormalizeTags(req.Tags)
		}

		switch strings.ToLower(strings.TrimSpace(req.Mode)) {
		case "", "documents", "document", "search":
			if server.ragEngine == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "RAG engine not available"})
				return
			}
			if strings.TrimSpace(req.Query) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "query is required"})
				return
			}
			results, err := server.ragEngine.Search(r.Context(), req.Query, req.Limit, req.SourceType, req.Debug)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, consoleQueryResponse{
				Mode:        "documents",
				Query:       req.Query,
				Debug:       req.Debug,
				ResultCount: results.TotalFound,
				Results:     results,
			})
		case "memory", "memories":
			if server.memoryStore == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Memory store not available"})
				return
			}
			if strings.TrimSpace(req.Query) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "query is required"})
				return
			}
			results, err := server.memoryStore.Recall(r.Context(), req.Query, filters, req.Limit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			results = filterMemorySearchResults(results, req.Service, req.Tags, req.Limit)
			writeJSON(w, http.StatusOK, consoleQueryResponse{
				Mode:        "memory",
				Query:       req.Query,
				Debug:       false,
				ResultCount: len(results),
				Results:     results,
			})
		case "canonical":
			if server.memoryStore == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Memory store not available"})
				return
			}
			if strings.TrimSpace(req.Query) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "query is required"})
				return
			}
			results, err := server.memoryStore.RecallCanonical(r.Context(), req.Query, filters, req.Limit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			results = filterCanonicalSearchResults(results, req.Service, req.Tags, req.Limit)
			writeJSON(w, http.StatusOK, consoleQueryResponse{
				Mode:        "canonical",
				Query:       req.Query,
				Debug:       false,
				ResultCount: len(results),
				Results:     results,
			})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported mode"})
		}
	})

	return mux
}

func authorizeHTTPRequest(w http.ResponseWriter, r *http.Request, authToken string) bool {
	if authToken == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + authToken
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		// Connection likely closed; log at debug level would be ideal
		// but we only have the http.ResponseWriter here.
		_ = err
	}
}
