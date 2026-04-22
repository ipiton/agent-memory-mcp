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
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/stats"
	"github.com/ipiton/agent-memory-mcp/internal/steward"
	"go.uber.org/zap"
)

const maxRequestBodyBytes = 10 * 1024 * 1024 // 10 MB

const protocolVersion = "2025-11-25"

// Version is set via ldflags at build time: -ldflags "-X ...server.Version=..."
// Falls back to "dev" if not set.
var Version = "dev"

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
	config           config.Config
	pathGuard        *paths.Guard
	outputMode       string
	stats            *stats.Logger
	ragEngine        *rag.Engine
	ragMu            sync.RWMutex // protects ragEngine during hot-reload
	memoryStore      *memory.Store
	embedder         *embedder.Embedder
	fileLogger       *logger.FileLogger
	sessionTracker   *sessionTracker
	stewardService   *steward.Service
	stewardScheduler *steward.Scheduler
	toolHandlers     map[string]toolHandler
}

// New creates a new MCPServer with the given configuration and path guard.
func New(cfg config.Config, guard *paths.Guard) *MCPServer {
	var ragEngine *rag.Engine
	var memoryStore *memory.Store
	var emb *embedder.Embedder
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

		var err error
		emb, err = embedder.New(cfg.EmbedderConfig(), zap.NewNop())
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: embedder unavailable, memory will use text-only matching: %v\n", err)
			if fileLogger != nil {
				fileLogger.Warn("Embedder initialization failed, memory will use text-only matching", zap.Error(err))
			}
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
			if emb != nil {
				emb.Close()
			}
			emb = nil
		} else {
			if fileLogger != nil {
				fileLogger.Info("Memory store initialized successfully",
					zap.String("memory_db_path", cfg.MemoryDBPath),
				)
			}
			// T48: propagate the sediment feature flag into the store so
			// Recall knows whether to apply layer-aware scoring.
			memoryStore.SetSedimentEnabled(cfg.SedimentEnabled)
		}
	}

	if fileLogger != nil {
		fileLogger.Info("MCP server initialized",
			zap.Bool("rag_available", ragEngine != nil),
			zap.Bool("memory_available", memoryStore != nil),
		)
	}

	var stewardSvc *steward.Service
	var stewardSched *steward.Scheduler
	if cfg.StewardEnabled && memoryStore != nil {
		var sLogger *zap.Logger
		if fileLogger != nil {
			sLogger = fileLogger.Logger
		}
		var err error
		stewardSvc, err = steward.NewService(memoryStore, sLogger)
		if err != nil {
			if fileLogger != nil {
				fileLogger.Warn("Steward service initialization failed", zap.Error(err))
			}
		} else {
			// Override policy from config if mode differs.
			p := stewardSvc.Policy()
			p.Mode = steward.PolicyMode(cfg.StewardMode)
			p.ScheduleInterval = cfg.StewardScheduleInterval
			p.DuplicateSimilarity = cfg.StewardDuplicateThreshold
			p.StaleDays = cfg.StewardStaleDays
			p.CanonicalMinConfidence = cfg.StewardCanonicalMinConf
			if err := stewardSvc.SetPolicy(p); err != nil && fileLogger != nil {
				fileLogger.Warn("Failed to set steward policy from config", zap.Error(err))
			}

			stewardSched = steward.NewScheduler(stewardSvc, sLogger)
			stewardSched.Start()

			if fileLogger != nil {
				fileLogger.Info("Steward service initialized",
					zap.String("mode", cfg.StewardMode),
				)
			}
		}
	}

	srv := &MCPServer{
		config:           cfg,
		pathGuard:        guard,
		outputMode:       cfg.OutputMode,
		stats:            stats.NewLogger(cfg),
		ragEngine:        ragEngine,
		memoryStore:      memoryStore,
		embedder:         emb,
		fileLogger:       fileLogger,
		sessionTracker:   newSessionTracker(cfg, memoryStore, fileLogger),
		stewardService:   stewardSvc,
		stewardScheduler: stewardSched,
	}
	// Wire session close event to steward scheduler.
	if srv.sessionTracker != nil && stewardSched != nil {
		srv.sessionTracker.onSessionClose = func() {
			stewardSched.TriggerEvent("session_close")
		}
	}

	srv.toolHandlers = srv.buildToolHandlers()
	return srv
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
	if err := validateHTTPExposure(server.config); err != nil {
		return err
	}

	mux := buildHTTPMux(server)
	addr := httpListenAddr(server.config)
	logHTTPExposurePolicy(server)

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
	// Notifications (e.g. "initialized") require no response.
	if s.sessionTracker != nil {
		s.sessionTracker.HandleNotification(req.Method, req.Params)
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
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "tools/list":
		return s.handleToolsList(req.Params)
	case "tools/call":
		return s.handleToolsCall(req.Params)
	default:
		return nil, &rpcError{Code: rpcErrMethodNotFound, Message: "method not found"}
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
			"version": Version,
		},
	}, nil
}

// getRagEngine returns the current RAG engine under read lock.
func (s *MCPServer) getRagEngine() *rag.Engine {
	s.ragMu.RLock()
	defer s.ragMu.RUnlock()
	return s.ragEngine
}

// ReloadRAG stops the current RAG engine and creates a new one from newCfg.
// If RAG is disabled in newCfg, the engine is stopped and set to nil.
func (s *MCPServer) ReloadRAG(newCfg config.Config) {
	s.ragMu.Lock()
	defer s.ragMu.Unlock()

	if s.ragEngine != nil {
		s.ragEngine.Stop()
		s.ragEngine = nil
	}

	if !newCfg.RAGEnabled {
		if s.fileLogger != nil {
			s.fileLogger.Info("Config reload: RAG disabled")
		}
		return
	}

	engine := rag.NewEngine(newCfg, s.fileLogger)
	if engine == nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("Config reload: RAG engine initialization failed")
		}
		return
	}

	s.ragEngine = engine
	s.config = newCfg
	if s.fileLogger != nil {
		s.fileLogger.Info("Config reload: RAG engine restarted",
			zap.String("root_path", newCfg.RootPath),
			zap.Bool("rag_enabled", newCfg.RAGEnabled),
			zap.Strings("index_dirs", newCfg.IndexDirs),
		)
	}
}

// Shutdown gracefully shuts down the server, closing all resources.
func (s *MCPServer) Shutdown() {
	if s.stewardScheduler != nil {
		s.stewardScheduler.Stop()
	}
	if s.sessionTracker != nil {
		s.sessionTracker.Close()
	}
	if s.ragEngine != nil {
		s.ragEngine.Stop()
	}
	if s.memoryStore != nil {
		if err := s.memoryStore.Close(); err != nil && s.fileLogger != nil {
			s.fileLogger.Warn("Failed to close memory store", zap.Error(err))
		}
	}
	if s.embedder != nil {
		s.embedder.Close()
	}
	if s.fileLogger != nil {
		if err := s.fileLogger.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to sync file logger: %v\n", err)
		}
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
