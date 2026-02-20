package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Note: strings, time, filepath used in NewMCPServer

const protocolVersion = "2024-11-05"

// JSON-RPC structures

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCP server

type MCPServer struct {
	config      Config
	pathGuard   *PathGuard
	outputMode  string
	stats       *StatsLogger
	ragEngine   *RAGEngine
	memoryStore *MemoryStore
	fileLogger  *FileLogger
}

func NewMCPServer(cfg Config, guard *PathGuard) *MCPServer {
	var ragEngine *RAGEngine
	var memoryStore *MemoryStore
	var fileLogger *FileLogger

	// Initialize file logger for diagnostics
	if cfg.LogPath != "" {
		var err error
		fileLogger, err = NewFileLogger(cfg.LogPath)
		if err != nil {
			// Log to stderr if file logger fails (but don't fail server startup)
			fmt.Fprintf(os.Stderr, "warning: failed to create file logger: %v\n", err)
		} else {
			fileLogger.Info("MCP server initializing",
				zap.String("root_path", cfg.RootPath),
				zap.Bool("rag_enabled", cfg.RAGEnabled),
				zap.Bool("memory_enabled", cfg.MemoryEnabled),
				zap.String("rag_index_path", cfg.RAGIndexPath),
			)
		}
	}

	// Initialize RAG if enabled
	if cfg.RAGEnabled {
		ragEngine = NewRAGEngine(cfg, fileLogger)
		if ragEngine == nil {
			if fileLogger != nil {
				fileLogger.Warn("RAG engine initialization failed - RAG features will be unavailable",
					zap.String("rag_index_path", cfg.RAGIndexPath),
					zap.String("jina_api_key_set", boolToString(cfg.JinaAPIKey != "")),
					zap.String("ollama_url", cfg.OllamaBaseURL),
				)
			}
		} else {
			if fileLogger != nil {
				fileLogger.Info("RAG engine initialized successfully",
					zap.String("rag_index_path", cfg.RAGIndexPath),
				)
			}
		}
	} else {
		if fileLogger != nil {
			fileLogger.Info("RAG engine disabled by configuration")
		}
	}

	// Initialize memory store if enabled (uses config paths)
	if cfg.MemoryEnabled {
		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(cfg.MemoryDBPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create memory store directory: %v\n", err)
		}

		// Get embedder from RAG if available, otherwise create minimal embedder
		var embedder *Embedder
		if ragEngine != nil && ragEngine.vecService != nil {
			embedder = ragEngine.vecService.config.Embedder
		} else {
			// Create a minimal embedder for memory store
			var err error
			embedder, err = NewEmbedder(EmbedderConfig{
				JinaToken:     cfg.JinaAPIKey,
				OllamaBaseURL: cfg.OllamaBaseURL,
				MaxRetries:    1,
				Timeout:       5 * time.Second,
		}, zap.NewNop())
			if err != nil {
				embedder = nil // Memory store will use text search fallback
			}
		}

		// Create memory store using config path
		var err error
		memoryStore, err = NewMemoryStore(cfg.MemoryDBPath, embedder, zap.NewNop())
		if err != nil {
			if fileLogger != nil {
				fileLogger.Warn("Memory store initialization failed - memory features will be unavailable",
					zap.String("memory_db_path", cfg.MemoryDBPath),
					zap.Error(err),
				)
			}
			memoryStore = nil
		} else {
			if fileLogger != nil {
				fileLogger.Info("Memory store initialized successfully",
					zap.String("memory_db_path", cfg.MemoryDBPath),
				)
			}
		}
	}

	if fileLogger != nil {
		fileLogger.Info("MCP server initialized",
			zap.Bool("rag_available", ragEngine != nil),
			zap.Bool("memory_available", memoryStore != nil),
		)
	}

	return &MCPServer{
		config:      cfg,
		pathGuard:   guard,
		outputMode:  cfg.OutputMode,
		stats:       NewStatsLogger(cfg),
		ragEngine:   ragEngine,
		memoryStore: memoryStore,
		fileLogger:  fileLogger,
	}
}

// boolToString converts bool to string for logging
func boolToString(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func RunStdio(server *MCPServer) error {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	for {
		payload, mode, err := readMessage(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if server.outputMode == "" && mode != "" {
			server.outputMode = mode
		}
		if len(strings.TrimSpace(string(payload))) == 0 {
			continue
		}

		var req RPCRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			resp := errorResponse(nil, -32600, "invalid request", err.Error())
			if err := writeResponse(writer, resp, server.outputMode); err != nil {
				return err
			}
			continue
		}

		resp := server.Handle(req)
		if resp == nil {
			continue
		}
		if err := writeResponse(writer, resp, server.outputMode); err != nil {
			return err
		}
	}
}

func RunHTTP(server *MCPServer) error {
	mux := http.NewServeMux()

	// JSON-RPC endpoint
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := errorResponse(nil, -32600, "Parse error", err.Error())
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := server.Handle(req)
		if resp == nil {
			// Notification - no response
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"rag_available":   server.ragEngine != nil,
			"memory_available": server.memoryStore != nil,
		})
	})

	addr := fmt.Sprintf(":%d", server.config.HTTPPort)
	if server.fileLogger != nil {
		server.fileLogger.Info("Starting HTTP server",
			zap.String("address", addr),
			zap.String("mode", server.config.HTTPMode),
		)
	}

	return http.ListenAndServe(addr, mux)
}

func (s *MCPServer) Handle(req RPCRequest) *RPCResponse {
	if isNotification(req.ID) {
		s.handleNotification(req)
		return nil
	}

	result, rpcErr := s.dispatch(req)
	if rpcErr != nil {
		return &RPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcErr,
		}
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func (s *MCPServer) handleNotification(req RPCRequest) {
	switch req.Method {
	case "initialized":
		return
	default:
		return
	}
}

func (s *MCPServer) dispatch(req RPCRequest) (any, *RPCError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "resources/list":
		return s.handleResourcesList(req.Params)
	case "resources/read":
		return s.handleResourcesRead(req.Params)
	case "tools/list":
		return s.handleToolsList(req.Params)
	case "tools/call":
		return s.handleToolsCall(req.Params)
	default:
		return nil, &RPCError{Code: -32601, Message: "method not found"}
	}
}

func (s *MCPServer) handleInitialize(_ json.RawMessage) (any, *RPCError) {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"resources": map[string]any{
				"listChanged": false,
			},
			"tools": map[string]any{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]any{
			"name":    "agent-memory-mcp",
			"version": "0.1.0",
		},
	}, nil
}

func isNotification(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	if string(id) == "null" {
		return true
	}
	return false
}

func errorResponse(id json.RawMessage, code int, message string, data any) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func readMessage(r *bufio.Reader) ([]byte, string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, "", err
	}
	line = strings.TrimSpace(line)
	for line == "" {
		line, err = r.ReadString('\n')
		if err != nil {
			return nil, "", err
		}
		line = strings.TrimSpace(line)
	}

	if strings.HasPrefix(line, "{") {
		return []byte(line), "line", nil
	}

	contentLength := 0
	for {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "content-length:") {
			_, err := fmt.Sscanf(lower, "content-length: %d", &contentLength)
			if err != nil {
				return nil, "content-length", fmt.Errorf("invalid content-length header: %w", err)
			}
		}
		line, err = r.ReadString('\n')
		if err != nil {
			return nil, "content-length", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
	}
	if contentLength <= 0 {
		return nil, "content-length", fmt.Errorf("missing content-length header")
	}
	payload := make([]byte, contentLength)
	_, err = io.ReadFull(r, payload)
	if err != nil {
		return nil, "content-length", err
	}
	return payload, "content-length", nil
}

func writeResponse(w *bufio.Writer, resp *RPCResponse, mode string) error {
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if mode == "line" {
		if _, err := w.Write(payload); err != nil {
			return err
		}
		if _, err := w.WriteString("\n"); err != nil {
			return err
		}
		return w.Flush()
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if _, err := w.WriteString(header); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return w.Flush()
}
