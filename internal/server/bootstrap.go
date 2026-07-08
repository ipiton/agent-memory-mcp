package server

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/steward"
	"go.uber.org/zap"
)

// This file decomposes the MCPServer constructor into cohesive init steps
// (Round 3 H17). New (in server.go) orchestrates them; each helper owns one
// subsystem's construction, error handling, and logging. Behaviour and order
// are identical to the previous inline constructor.

// initFileLogger builds the optional file logger from cfg.LogPath, returning nil
// (and warning to stderr) when it cannot be created or is not configured.
func initFileLogger(cfg config.Config) *logger.FileLogger {
	if cfg.LogPath == "" {
		return nil
	}
	fileLogger, err := logger.New(cfg.LogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create file logger: %v\n", err)
		return nil
	}
	fileLogger.Info("MCP server initializing",
		zap.String("root_path", cfg.RootPath),
		zap.Bool("rag_enabled", cfg.RAGEnabled),
		zap.Bool("memory_enabled", cfg.MemoryEnabled),
		zap.String("rag_index_path", cfg.RAGIndexPath),
	)
	return fileLogger
}

// initRAGEngine builds the RAG engine when enabled, returning nil when disabled
// or when initialization fails (both degrade gracefully).
func initRAGEngine(cfg config.Config, fileLogger *logger.FileLogger) *rag.Engine {
	if !cfg.RAGEnabled {
		if fileLogger != nil {
			fileLogger.Info("RAG engine disabled by configuration")
		}
		return nil
	}

	ragEngine := rag.NewEngine(cfg, fileLogger)
	if ragEngine == nil {
		if fileLogger != nil {
			fileLogger.Warn("RAG engine initialization failed - RAG features will be unavailable",
				zap.String("rag_index_path", cfg.RAGIndexPath),
				zap.String("jina_api_key_set", config.BoolToString(cfg.JinaAPIKey != "")),
				zap.String("ollama_url", cfg.OllamaBaseURL),
			)
		}
		return nil
	}
	if fileLogger != nil {
		fileLogger.Info("RAG engine initialized successfully",
			zap.String("rag_index_path", cfg.RAGIndexPath),
		)
	}
	return ragEngine
}

// initMemoryStore builds the memory store and its embedder when memory is
// enabled. Returns (nil, nil) when disabled or on failure. The returned
// embedder may be nil (text-only matching) even when the store is non-nil; it is
// returned so the caller can close it / adapt it to embedder.Service.
func initMemoryStore(cfg config.Config, fileLogger *logger.FileLogger) (*memory.Store, *embedder.Embedder) {
	if !cfg.MemoryEnabled {
		return nil, nil
	}

	if err := os.MkdirAll(filepath.Dir(cfg.MemoryDBPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create memory store directory: %v\n", err)
	}

	emb, err := embedder.New(cfg.EmbedderConfig(), zap.NewNop())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: embedder unavailable, memory will use text-only matching: %v\n", err)
		if fileLogger != nil {
			fileLogger.Warn("Embedder initialization failed, memory will use text-only matching", zap.Error(err))
		}
		emb = nil
	}

	memoryStore, err := memory.NewStore(cfg.MemoryDBPath, embedder.AsService(emb), zap.NewNop())
	if err != nil {
		if fileLogger != nil {
			fileLogger.Warn("Memory store initialization failed - memory features will be unavailable",
				zap.String("memory_db_path", cfg.MemoryDBPath),
				zap.Error(err),
			)
		}
		if emb != nil {
			emb.Close()
		}
		return nil, nil
	}

	if fileLogger != nil {
		fileLogger.Info("Memory store initialized successfully",
			zap.String("memory_db_path", cfg.MemoryDBPath),
		)
	}
	// T48: propagate the sediment feature flag into the store so
	// Recall knows whether to apply layer-aware scoring.
	memoryStore.SetSedimentEnabled(cfg.SedimentEnabled)

	// T68: configure exponential age decay for recall scoring.
	memoryStore.SetRecallHalfLife(cfg.RecallHalfLifeDays)

	// T50 slice 2: optional knowledge-graph triple extractor.
	// Only wired when MCP_TRIPLE_EXTRACTOR_ENABLED=true. If
	// configuration is incomplete we log a warning and proceed
	// without extraction — ingest must keep working regardless.
	if cfg.TripleExtractorEnabled {
		wireTripleExtractor(cfg, memoryStore, fileLogger)
	}

	return memoryStore, emb
}

// wireTripleExtractor attaches the optional OpenAI triple extractor to the
// store, logging and skipping on misconfiguration.
func wireTripleExtractor(cfg config.Config, memoryStore *memory.Store, fileLogger *logger.FileLogger) {
	apiKey := cfg.TripleExtractorAPIKey
	if apiKey == "" {
		apiKey = cfg.OpenAIAPIKey
	}
	baseURL := cfg.TripleExtractorBaseURL
	if baseURL == "" {
		baseURL = cfg.OpenAIBaseURL
	}
	extractor, exErr := memory.NewOpenAIExtractor(memory.OpenAIExtractorConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   cfg.TripleExtractorModel,
		Timeout: cfg.TripleExtractorTimeout,
	}, zap.NewNop())
	if exErr != nil {
		if fileLogger != nil {
			fileLogger.Warn("Triple extractor disabled: misconfiguration",
				zap.Error(exErr))
		} else {
			fmt.Fprintf(os.Stderr, "warning: triple extractor disabled: %v\n", exErr)
		}
		return
	}
	memoryStore.SetTripleExtractor(extractor)
}

// initStewardService builds the steward service and scheduler when enabled and a
// memory store exists. Applies env-driven policy overrides and starts the
// scheduler. Returns (nil, nil) when disabled or on failure.
func initStewardService(cfg config.Config, memoryStore *memory.Store, fileLogger *logger.FileLogger) (*steward.Service, *steward.Scheduler) {
	if !cfg.StewardEnabled || memoryStore == nil {
		return nil, nil
	}

	var sLogger *zap.Logger
	if fileLogger != nil {
		sLogger = fileLogger.Logger
	}
	stewardSvc, err := steward.NewService(memoryStore, sLogger)
	if err != nil {
		if fileLogger != nil {
			fileLogger.Warn("Steward service initialization failed", zap.Error(err))
		}
		return nil, nil
	}

	// Apply env-driven config overrides ONLY for fields whose env
	// var is explicitly set. Without this guard (Round 3 / T53),
	// every restart clobbered a user-set `steward_policy mode=auto`
	// with the config default "manual", silently halting auto-runs
	// after `brew upgrade`.
	p := stewardSvc.Policy()
	if _, ok := os.LookupEnv("MCP_STEWARD_MODE"); ok {
		p.Mode = steward.PolicyMode(cfg.StewardMode)
	}
	if _, ok := os.LookupEnv("MCP_STEWARD_SCHEDULE_INTERVAL"); ok {
		p.ScheduleInterval = cfg.StewardScheduleInterval
	}
	if _, ok := os.LookupEnv("MCP_STEWARD_DUPLICATE_THRESHOLD"); ok {
		p.DuplicateSimilarity = cfg.StewardDuplicateThreshold
	}
	if _, ok := os.LookupEnv("MCP_STEWARD_STALE_DAYS"); ok {
		p.StaleDays = cfg.StewardStaleDays
	}
	if _, ok := os.LookupEnv("MCP_STEWARD_CANONICAL_MIN_CONFIDENCE"); ok {
		p.CanonicalMinConfidence = cfg.StewardCanonicalMinConf
	}
	if err := stewardSvc.SetPolicy(p); err != nil && fileLogger != nil {
		fileLogger.Warn("Failed to set steward policy from config", zap.Error(err))
	}

	stewardSched := steward.NewScheduler(stewardSvc, sLogger)
	stewardSched.Start()

	// T53 observability: surface a persistent inbox backlog when
	// running in non-auto mode so operators notice that previously
	// queued items aren't being processed.
	if fileLogger != nil {
		fileLogger.Info("Steward service initialized",
			zap.String("mode", string(p.Mode)),
		)
		if p.Mode == steward.PolicyModeManual {
			if status, err := stewardSvc.Status(); err == nil && status.PendingReview > 100 {
				fileLogger.Warn("Steward in manual mode with large pending review queue",
					zap.Int("pending_review", status.PendingReview),
					zap.String("hint", "run 'steward_policy mode=scheduled' to resume auto-resolution"),
				)
			}
		}
	}

	return stewardSvc, stewardSched
}
