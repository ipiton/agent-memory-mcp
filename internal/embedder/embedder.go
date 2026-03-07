// Package embedder provides text embedding with Jina AI, OpenAI, and Ollama providers.
package embedder

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultDimension is the default vector dimension for embedding providers.
// Jina v3 and bge-m3 produce 1024 natively; OpenAI supports Matryoshka truncation to any size.
// Changing requires re-indexing.
const DefaultDimension = 1024

const (
	defaultOpenAIBaseURL      = "https://api.openai.com/v1"
	defaultOpenAIModel        = "text-embedding-3-small"
	defaultOllamaBaseURL      = "http://localhost:11434"
	defaultOllamaPrimaryModel = "bge-m3:latest"
	defaultOllamaBackupModel  = "mxbai-embed-large:latest"
)

// Config holds provider credentials and tuning parameters for the Embedder.
type Config struct {
	JinaToken     string
	OpenAIToken   string
	OpenAIBaseURL string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel   string // Embedding model (default: text-embedding-3-small)
	OllamaBaseURL string
	Dimension     int    // Required embedding dimension (default: 1024)
	Mode          string // auto or local-only
	MaxRetries    int
	Timeout       time.Duration
}

type EmbeddingResult struct {
	Embedding []float32
	ModelID   string
}

type BatchEmbeddingResult struct {
	Embeddings [][]float32
	ModelID    string
}

type providerAdapter interface {
	name() string
	modelID() string
	embed(ctx context.Context, text, task string) ([]float32, error)
	batchEmbed(ctx context.Context, texts []string, task string) ([][]float32, error)
}

type providerCandidate struct {
	provider  providerAdapter
	onSuccess func()
	onFailure func(error)
}

// Embedder generates vector embeddings using Jina AI as primary with OpenAI and Ollama fallback.
type Embedder struct {
	config            Config
	logger            *zap.Logger
	client            *http.Client
	Dimension         int        // Embedding dimension
	jinaDisabled      bool       // Flag to disable Jina after auth errors
	jinaDisabledUntil time.Time  // Time when Jina can be retried again (for auth errors)
	jinaErrorCount    int        // Count of consecutive Jina errors
	jinaDisabledMu    sync.Mutex // Mutex for jinaDisabled flag
	lastModelMu       sync.RWMutex
	lastModelID       string
}

// New creates a new Embedder with the given configuration and logger.
func New(config Config, logger *zap.Logger) (*Embedder, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if config.OllamaBaseURL == "" {
		config.OllamaBaseURL = defaultOllamaBaseURL
	}
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 2
	}
	if config.Dimension == 0 {
		config.Dimension = DefaultDimension
	}
	if config.Mode == "" {
		config.Mode = "auto"
	}

	return &Embedder{
		config: config,
		logger: logger,
		client: &http.Client{
			Timeout: config.Timeout,
		},
		Dimension:         config.Dimension,
		jinaDisabled:      false,
		jinaDisabledUntil: time.Time{},
		jinaErrorCount:    0,
		jinaDisabledMu:    sync.Mutex{},
		lastModelMu:       sync.RWMutex{},
	}, nil
}

// Close releases resources held by the Embedder, including idle HTTP connections.
func (e *Embedder) Close() {
	if e != nil && e.client != nil {
		e.client.CloseIdleConnections()
	}
}

func (e *Embedder) localOnlyMode() bool {
	return strings.EqualFold(e.config.Mode, "local-only")
}

// Embed generates a vector embedding for the given text, optimized for document passages.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	result, err := e.EmbedDetailed(ctx, text)
	if err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

// EmbedQuery generates a vector embedding for a search query, optimized for retrieval.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	result, err := e.EmbedQueryDetailed(ctx, text)
	if err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

// EmbedWithTask generates a vector embedding with the specified Jina task type.
func (e *Embedder) EmbedWithTask(ctx context.Context, text string, task string) ([]float32, error) {
	result, err := e.embedWithTaskDetailed(ctx, text, task)
	if err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

func (e *Embedder) EmbedDetailed(ctx context.Context, text string) (*EmbeddingResult, error) {
	return e.embedWithTaskDetailed(ctx, text, "retrieval.passage")
}

func (e *Embedder) EmbedQueryDetailed(ctx context.Context, text string) (*EmbeddingResult, error) {
	return e.embedWithTaskDetailed(ctx, text, "retrieval.query")
}

func (e *Embedder) embedWithTaskDetailed(ctx context.Context, text string, task string) (*EmbeddingResult, error) {
	if err := e.ensureReady(); err != nil {
		return nil, err
	}

	for _, candidate := range e.singleCandidates(task) {
		embedding, err := candidate.provider.embed(ctx, text, task)
		if err != nil {
			e.logger.Warn("Embedding provider failed", zap.String("provider", candidate.provider.name()), zap.Error(err))
			if candidate.onFailure != nil {
				candidate.onFailure(err)
			}
			continue
		}
		if !e.validateEmbedding(candidate.provider.name(), embedding) {
			if candidate.onFailure != nil {
				candidate.onFailure(fmt.Errorf("dimension mismatch"))
			}
			continue
		}
		if candidate.onSuccess != nil {
			candidate.onSuccess()
		}
		modelID := candidate.provider.modelID()
		e.recordModel(modelID)
		return &EmbeddingResult{Embedding: embedding, ModelID: modelID}, nil
	}

	if e.localOnlyMode() {
		return nil, fmt.Errorf("local-only embedding mode failed: start Ollama at %s and pull bge-m3 or mxbai-embed-large, or disable MCP_EMBEDDING_MODE=local-only", e.config.OllamaBaseURL)
	}
	return nil, fmt.Errorf("all embedding providers failed: configure at least one of JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL")
}

// BatchEmbed generates vector embeddings for multiple texts using native batch APIs.
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	result, err := e.BatchEmbedDetailed(ctx, texts)
	if err != nil {
		return nil, err
	}
	return result.Embeddings, nil
}

// BatchEmbedWithTask generates batch embeddings with the specified task type.
// Uses native batch APIs for each provider (Jina, OpenAI, Ollama).
func (e *Embedder) BatchEmbedWithTask(ctx context.Context, texts []string, task string) ([][]float32, error) {
	result, err := e.batchEmbedWithTaskDetailed(ctx, texts, task)
	if err != nil {
		return nil, err
	}
	return result.Embeddings, nil
}

func (e *Embedder) BatchEmbedDetailed(ctx context.Context, texts []string) (*BatchEmbeddingResult, error) {
	return e.batchEmbedWithTaskDetailed(ctx, texts, "retrieval.passage")
}

func (e *Embedder) batchEmbedWithTaskDetailed(ctx context.Context, texts []string, task string) (*BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := e.ensureReady(); err != nil {
		return nil, err
	}

	for _, candidate := range e.batchCandidates(task) {
		embeddings, err := candidate.provider.batchEmbed(ctx, texts, task)
		if err != nil {
			e.logger.Warn("Batch embedding provider failed", zap.String("provider", candidate.provider.name()), zap.Error(err))
			if candidate.onFailure != nil {
				candidate.onFailure(err)
			}
			continue
		}
		if err := e.validateBatchEmbeddings(candidate.provider.name(), embeddings, len(texts)); err != nil {
			e.logger.Warn("Batch embedding validation failed", zap.String("provider", candidate.provider.name()), zap.Error(err))
			if candidate.onFailure != nil {
				candidate.onFailure(err)
			}
			continue
		}
		if candidate.onSuccess != nil {
			candidate.onSuccess()
		}
		modelID := candidate.provider.modelID()
		e.recordModel(modelID)
		return &BatchEmbeddingResult{Embeddings: embeddings, ModelID: modelID}, nil
	}

	if e.localOnlyMode() {
		return nil, fmt.Errorf("local-only batch embedding failed: start Ollama at %s and pull bge-m3 or mxbai-embed-large, or disable MCP_EMBEDDING_MODE=local-only", e.config.OllamaBaseURL)
	}
	return nil, fmt.Errorf("all batch embedding providers failed: configure at least one of JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL")
}

func (e *Embedder) ensureReady() error {
	if e == nil {
		return fmt.Errorf("embedder is nil")
	}
	if e.logger == nil {
		e.logger = zap.NewNop()
	}
	return nil
}

func (e *Embedder) singleCandidates(task string) []providerCandidate {
	candidates := make([]providerCandidate, 0, 4)
	if !e.localOnlyMode() && e.config.JinaToken != "" && e.jinaAvailable(task) {
		candidates = append(candidates, providerCandidate{
			provider:  jinaAdapter{embedder: e},
			onSuccess: e.markJinaSuccess,
			onFailure: func(error) { e.markJinaFailure(task) },
		})
	}
	if !e.localOnlyMode() && e.config.OpenAIToken != "" {
		candidates = append(candidates, providerCandidate{provider: openAIAdapter{embedder: e}})
	}
	if e.config.OllamaBaseURL != "" {
		candidates = append(candidates,
			providerCandidate{provider: ollamaAdapter{embedder: e, model: defaultOllamaPrimaryModel}},
			providerCandidate{provider: ollamaAdapter{embedder: e, model: defaultOllamaBackupModel}},
		)
	}
	return candidates
}

func (e *Embedder) batchCandidates(task string) []providerCandidate {
	candidates := make([]providerCandidate, 0, 4)
	if !e.localOnlyMode() && e.config.JinaToken != "" && e.jinaAvailable(task) {
		candidates = append(candidates, providerCandidate{provider: jinaAdapter{embedder: e}})
	}
	if !e.localOnlyMode() && e.config.OpenAIToken != "" {
		candidates = append(candidates, providerCandidate{provider: openAIAdapter{embedder: e}})
	}
	if e.config.OllamaBaseURL != "" {
		candidates = append(candidates,
			providerCandidate{provider: ollamaAdapter{embedder: e, model: defaultOllamaPrimaryModel}},
			providerCandidate{provider: ollamaAdapter{embedder: e, model: defaultOllamaBackupModel}},
		)
	}
	return candidates
}

func (e *Embedder) validateEmbedding(providerName string, embedding []float32) bool {
	if len(embedding) == e.Dimension {
		return true
	}
	e.logger.Error("Embedding dimension mismatch — check model configuration",
		zap.String("provider", providerName),
		zap.Int("got", len(embedding)),
		zap.Int("expected", e.Dimension),
		zap.String("hint", fmt.Sprintf("The model returned %d dimensions but %d are required. Set MCP_EMBEDDING_DIMENSION=%d or use a model that supports %d-dimensional output.", len(embedding), e.Dimension, len(embedding), e.Dimension)),
	)
	return false
}

func (e *Embedder) validateBatchEmbeddings(providerName string, embeddings [][]float32, expected int) error {
	if len(embeddings) != expected {
		return fmt.Errorf("%s returned %d embeddings for %d texts", providerName, len(embeddings), expected)
	}
	for i, embedding := range embeddings {
		if embedding == nil {
			return fmt.Errorf("%s returned nil embedding at index %d", providerName, i)
		}
		if !e.validateEmbedding(fmt.Sprintf("%s[%d]", providerName, i), embedding) {
			return fmt.Errorf("%s returned invalid embedding dimension at index %d", providerName, i)
		}
	}
	return nil
}

func (e *Embedder) jinaAvailable(task string) bool {
	e.jinaDisabledMu.Lock()
	wasDisabled := e.jinaDisabled
	canRetry := wasDisabled && !e.jinaDisabledUntil.IsZero() && time.Now().After(e.jinaDisabledUntil)
	if canRetry {
		e.jinaDisabled = false
		e.jinaDisabledUntil = time.Time{}
		e.jinaErrorCount = 0
	}
	stillDisabled := e.jinaDisabled
	e.jinaDisabledMu.Unlock()

	if canRetry {
		fields := []zap.Field{}
		if strings.TrimSpace(task) != "" {
			fields = append(fields, zap.String("task", task))
		}
		e.logger.Info("Retrying Jina AI after timeout period", fields...)
	}

	return !stillDisabled
}

func (e *Embedder) markJinaSuccess() {
	e.jinaDisabledMu.Lock()
	e.jinaErrorCount = 0
	e.jinaDisabledMu.Unlock()
}

func (e *Embedder) markJinaFailure(task string) {
	e.jinaDisabledMu.Lock()
	e.jinaErrorCount++
	errorCount := e.jinaErrorCount
	shouldDisable := errorCount >= 3 && !e.jinaDisabled
	if shouldDisable {
		e.jinaDisabled = true
		e.jinaDisabledUntil = time.Now().Add(1 * time.Hour)
	}
	e.jinaDisabledMu.Unlock()

	if shouldDisable {
		fields := []zap.Field{zap.String("hint", "Check JINA_API_KEY or remove it to skip Jina")}
		if strings.TrimSpace(task) != "" {
			fields = append(fields, zap.String("task", task))
		}
		e.logger.Error("Jina AI disabled after repeated failures, using fallback providers", fields...)
	}
}

func (e *Embedder) LastModelID() string {
	e.lastModelMu.RLock()
	defer e.lastModelMu.RUnlock()
	return e.lastModelID
}

func (e *Embedder) recordModel(modelID string) {
	e.lastModelMu.Lock()
	e.lastModelID = modelID
	e.lastModelMu.Unlock()
}

func (e *Embedder) jinaModelID() string {
	return fmt.Sprintf("jina:jina-embeddings-v3:%d", e.Dimension)
}

func (e *Embedder) openAIModelID() string {
	model := e.config.OpenAIModel
	if model == "" {
		model = defaultOpenAIModel
	}
	baseURL := e.config.OpenAIBaseURL
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return fmt.Sprintf("openai:%s:%s:%d", strings.TrimRight(baseURL, "/"), model, e.Dimension)
}

func (e *Embedder) ollamaModelID(model string) string {
	baseURL := e.config.OllamaBaseURL
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	return fmt.Sprintf("ollama:%s:%s:%d", strings.TrimRight(baseURL, "/"), model, e.Dimension)
}
