// Package server implements the MCP protocol server with stdio and HTTP transports.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/stats"
	"go.uber.org/zap"
)

const maxRequestBodyBytes = 10 * 1024 * 1024 // 10 MB

const protocolVersion = "2024-11-05"

// JSON-RPC structures

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCPServer implements the MCP protocol server with RAG and memory capabilities.
type MCPServer struct {
	config      config.Config
	pathGuard   *paths.Guard
	outputMode  string
	stats       *stats.Logger
	ragEngine   *rag.Engine
	memoryStore *memory.Store
	fileLogger  *logger.FileLogger
}

// New creates a new MCPServer with the given configuration and path guard.
func New(cfg config.Config, guard *paths.Guard) *MCPServer {
	var ragEngine *rag.Engine
	var memoryStore *memory.Store
	var fileLogger *logger.FileLogger

	if cfg.LogPath != "" {
		var err error
		fileLogger, err = logger.New(cfg.LogPath)
		if err != nil {
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

	if cfg.RAGEnabled {
		ragEngine = rag.NewEngine(cfg, fileLogger)
		if ragEngine == nil {
			if fileLogger != nil {
				fileLogger.Warn("RAG engine initialization failed - RAG features will be unavailable",
					zap.String("rag_index_path", cfg.RAGIndexPath),
					zap.String("jina_api_key_set", config.BoolToString(cfg.JinaAPIKey != "")),
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

	if cfg.MemoryEnabled {
		if err := os.MkdirAll(filepath.Dir(cfg.MemoryDBPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create memory store directory: %v\n", err)
		}

		var emb *embedder.Embedder
		// Create a minimal embedder for memory store
		var err error
		emb, err = embedder.New(embedder.Config{
			JinaToken:     cfg.JinaAPIKey,
			OpenAIToken:   cfg.OpenAIAPIKey,
			OpenAIBaseURL: cfg.OpenAIBaseURL,
			OpenAIModel:   cfg.OpenAIModel,
			OllamaBaseURL: cfg.OllamaBaseURL,
			Dimension:     cfg.EmbeddingDimension,
			MaxRetries:    1,
			Timeout:       5 * time.Second,
		}, zap.NewNop())
		if err != nil {
			emb = nil
		}

		memoryStore, err = memory.NewStore(cfg.MemoryDBPath, emb, zap.NewNop())
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
		stats:       stats.NewLogger(cfg),
		ragEngine:   ragEngine,
		memoryStore: memoryStore,
		fileLogger:  fileLogger,
	}
}

// RunStdio runs the server in stdio mode, reading JSON-RPC requests from stdin.
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

		var req rpcRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			resp := errorResponse(nil, -32600, "invalid request", err.Error())
			if err := writeResponse(writer, resp, server.outputMode); err != nil {
				return err
			}
			continue
		}

		resp := server.handle(req)
		if resp == nil {
			continue
		}
		if err := writeResponse(writer, resp, server.outputMode); err != nil {
			return err
		}
	}
}

// RunHTTP runs the server in HTTP mode, blocking until ctx is cancelled
// and then gracefully shutting down.
func RunHTTP(ctx context.Context, server *MCPServer) error {
	mux := http.NewServeMux()

	authToken := server.config.HTTPAuthToken

	if authToken == "" && server.fileLogger != nil {
		server.fileLogger.Warn("HTTP server starting without authentication — set MCP_HTTP_AUTH_TOKEN for security")
	}

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// CORS: deny cross-origin by default
		w.Header().Set("Access-Control-Allow-Origin", "")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Auth check
		if authToken != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+authToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		ct := r.Header.Get("Content-Type")
		if ct != "" && !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := errorResponse(nil, -32600, "Parse error", err.Error())
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := server.handle(req)
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":           "ok",
			"rag_available":    server.ragEngine != nil,
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

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown failed: %w", err)
		}
		return nil
	}
}

func (s *MCPServer) handle(req rpcRequest) *rpcResponse {
	if isNotification(req.ID) {
		s.handleNotification(req)
		return nil
	}

	result, rpcErr := s.dispatch(req)
	if rpcErr != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcErr,
		}
	}

	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func (s *MCPServer) handleNotification(req rpcRequest) {
	switch req.Method {
	case "initialized":
		return
	default:
		return
	}
}

func (s *MCPServer) dispatch(req rpcRequest) (any, *rpcError) {
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
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *MCPServer) handleInitialize(_ json.RawMessage) (any, *rpcError) {
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
			"version": "0.2.0",
		},
	}, nil
}

// Shutdown gracefully shuts down the server, closing all resources.
func (s *MCPServer) Shutdown() {
	if s.ragEngine != nil {
		s.ragEngine.Stop()
	}
	if s.memoryStore != nil {
		s.memoryStore.Close()
	}
	if s.fileLogger != nil {
		s.fileLogger.Sync()
	}
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

func errorResponse(id json.RawMessage, code int, message string, data any) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
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
	if int64(contentLength) > maxRequestBodyBytes {
		return nil, "content-length", fmt.Errorf("request too large: %d bytes (max %d)", contentLength, maxRequestBodyBytes)
	}
	payload := make([]byte, contentLength)
	_, err = io.ReadFull(r, payload)
	if err != nil {
		return nil, "content-length", err
	}
	return payload, "content-length", nil
}

func writeResponse(w *bufio.Writer, resp *rpcResponse, mode string) error {
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
