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
	defaultLlamaCPPBaseURL    = "http://127.0.0.1:8080/v1"
	defaultLlamaCPPModel      = "bge-m3"
)

// Config holds provider credentials and tuning parameters for the Embedder.
type Config struct {
	JinaToken     string
	OpenAIToken   string
	OpenAIBaseURL string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel   string // Embedding model (default: text-embedding-3-small)
	OllamaBaseURL string
	// llama.cpp (llama-server --embedding) exposes an OpenAI-compatible
	// /v1/embeddings endpoint. Opt-in: only joins the provider chain when
	// LlamaCPPBaseURL is non-empty. Works in local-only mode.
	LlamaCPPBaseURL string
	LlamaCPPModel   string // llama.cpp embedding model (default: bge-m3)
	Dimension       int    // Required embedding dimension (default: 1024)
	Mode            string // auto or local-only
	MaxRetries      int
	Timeout         time.Duration
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

// Service is the embedding surface consumed by the memory store, the RAG
// vector service, and the MCP server. *Embedder is the production
// implementation; accepting the interface (Round 3 M23) lets tests inject a
// lightweight fake instead of standing up an httptest server per provider.
type Service interface {
	EmbedDetailed(ctx context.Context, text string) (*EmbeddingResult, error)
	EmbedQueryDetailed(ctx context.Context, text string) (*EmbeddingResult, error)
	BatchEmbedDetailed(ctx context.Context, texts []string) (*BatchEmbeddingResult, error)
	Dimensions() int
	Close()
}

var _ Service = (*Embedder)(nil)

// AsService adapts a concrete *Embedder to the Service interface, returning a
// true nil interface when e is nil. Passing a nil *Embedder directly to a
// Service parameter would yield a non-nil interface (Go typed-nil trap), so
// downstream `svc != nil` guards would wrongly succeed and dereference nil.
// Use this at call sites where the embedder may be absent (memory-only mode).
func AsService(e *Embedder) Service {
	if e == nil {
		return nil
	}
	return e
}

// Embedder generates vector embeddings using Jina AI as primary with OpenAI and Ollama fallback.
type Embedder struct {
	config      Config
	logger      *zap.Logger
	client      *http.Client
	Dimension   int // Embedding dimension
	health      map[string]*providerHealth
	healthMu    sync.Mutex // guards the health map (values self-synchronize)
	lastModelMu sync.RWMutex
	lastModelID string
}

// New creates a new Embedder with the given configuration and logger.
func New(config Config, logger *zap.Logger) (*Embedder, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	// Default to Ollama only when no other local backend is configured. With
	// llama.cpp wired up, an empty OLLAMA_BASE_URL means "don't use Ollama" —
	// forcing the default left a dead provider in the fallback chain, so every
	// llama.cpp failure was followed by two connection-refused Ollama retries
	// (bge-m3 + mxbai), tripling the failures and adding retry latency.
	if config.OllamaBaseURL == "" && config.LlamaCPPBaseURL == "" {
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
		Dimension:   config.Dimension,
		health:      map[string]*providerHealth{},
		lastModelMu: sync.RWMutex{},
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

	for _, candidate := range e.candidates(task) {
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

	for _, candidate := range e.candidates(task) {
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

// candidates builds the ordered provider fallback chain, skipping any provider
// whose circuit breaker is currently open. The single- and batch-embedding
// paths share this list (Round 3 M6) — previously only the single path wired a
// breaker, and only for Jina, so a hard-failing OpenAI/Ollama backend was
// retried on every call.
func (e *Embedder) candidates(task string) []providerCandidate {
	out := make([]providerCandidate, 0, 5)
	add := func(p providerAdapter) {
		name := p.name()
		h := e.healthFor(name)
		ok, retried := h.available()
		if !ok {
			return
		}
		if retried {
			fields := []zap.Field{zap.String("provider", name)}
			if strings.TrimSpace(task) != "" {
				fields = append(fields, zap.String("task", task))
			}
			e.logger.Info("Retrying embedding provider after cooldown", fields...)
		}
		out = append(out, providerCandidate{
			provider:  p,
			onSuccess: h.markSuccess,
			onFailure: func(error) {
				if h.markFailure() {
					fields := []zap.Field{
						zap.String("provider", name),
						zap.String("hint", "check the provider credentials/endpoint or remove it from the config to skip"),
					}
					if strings.TrimSpace(task) != "" {
						fields = append(fields, zap.String("task", task))
					}
					e.logger.Error("Embedding provider disabled after repeated failures, using fallback providers", fields...)
				}
			},
		})
	}

	if !e.localOnlyMode() && e.config.JinaToken != "" {
		add(jinaAdapter{embedder: e})
	}
	if !e.localOnlyMode() && e.config.OpenAIToken != "" {
		add(openAIAdapter{embedder: e})
	}
	if e.config.LlamaCPPBaseURL != "" {
		add(llamaCPPAdapter{embedder: e})
	}
	if e.config.OllamaBaseURL != "" {
		add(ollamaAdapter{embedder: e, model: defaultOllamaPrimaryModel})
		add(ollamaAdapter{embedder: e, model: defaultOllamaBackupModel})
	}
	return out
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

const (
	// providerFailureThreshold is the number of consecutive failures that trips
	// a provider's circuit breaker; providerDisableCooldown is how long it stays
	// open before one retry is allowed.
	providerFailureThreshold = 3
	providerDisableCooldown  = time.Hour
)

// providerHealth is a per-provider circuit breaker shared by every embedding
// backend (Round 3 M6). Values are stored in Embedder.health keyed by
// provider.name(); each value synchronizes its own state.
type providerHealth struct {
	mu            sync.Mutex
	errorCount    int
	disabledUntil time.Time
}

// available reports whether the provider may be tried now. When the cooldown has
// elapsed it re-enables the provider (allowing one retry) and returns
// retried=true so the caller can log the recovery attempt once.
func (h *providerHealth) available() (ok bool, retried bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.disabledUntil.IsZero() {
		return true, false
	}
	if time.Now().After(h.disabledUntil) {
		h.disabledUntil = time.Time{}
		h.errorCount = 0
		return true, true
	}
	return false, false
}

func (h *providerHealth) markSuccess() {
	h.mu.Lock()
	h.errorCount = 0
	h.disabledUntil = time.Time{}
	h.mu.Unlock()
}

// markFailure records a failure and returns true when it trips the breaker
// (crosses the threshold for the first time), so the caller logs exactly once.
func (h *providerHealth) markFailure() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errorCount++
	if h.errorCount >= providerFailureThreshold && h.disabledUntil.IsZero() {
		h.disabledUntil = time.Now().Add(providerDisableCooldown)
		return true
	}
	return false
}

// healthFor returns the circuit breaker for a provider, creating it on first use.
func (e *Embedder) healthFor(name string) *providerHealth {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	h := e.health[name]
	if h == nil {
		h = &providerHealth{}
		e.health[name] = h
	}
	return h
}

// Dimensions returns the embedding vector dimension this Embedder produces.
func (e *Embedder) Dimensions() int {
	return e.Dimension
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

func (e *Embedder) llamaCPPModelID() string {
	baseURL := e.config.LlamaCPPBaseURL
	if baseURL == "" {
		baseURL = defaultLlamaCPPBaseURL
	}
	model := e.config.LlamaCPPModel
	if model == "" {
		model = defaultLlamaCPPModel
	}
	return fmt.Sprintf("llamacpp:%s:%s:%d", strings.TrimRight(baseURL, "/"), model, e.Dimension)
}
