package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// TripleExtractor turns a stored memory into a small set of knowledge-graph
// triples. Implementations must be safe for concurrent use across goroutines
// because the Store fires extraction in fan-out goroutines, one per write.
//
// Implementations must NOT panic and must respect ctx cancellation: a slow
// LLM call must abort cleanly when the caller's context expires.
type TripleExtractor interface {
	Extract(ctx context.Context, mem *Memory) ([]*Triple, error)
}

// SetTripleExtractor wires the optional LLM-backed extractor used by Store
// and Update to populate memory_triples asynchronously. Pass nil to disable
// extraction. Safe to call at any time, including from goroutines other than
// the constructor.
func (ms *Store) SetTripleExtractor(e TripleExtractor) {
	ms.tripleExtractorMu.Lock()
	defer ms.tripleExtractorMu.Unlock()
	ms.tripleExtractor = e
}

// activeTripleExtractor returns the currently-installed extractor under a
// read lock. The pointer is stable for the duration of the call site since
// callers immediately use it; we don't hold the lock through Extract because
// extraction is an external HTTP call that can take seconds.
func (ms *Store) activeTripleExtractor() TripleExtractor {
	ms.tripleExtractorMu.RLock()
	defer ms.tripleExtractorMu.RUnlock()
	return ms.tripleExtractor
}

// fanoutTripleExtraction launches the extractor in a goroutine when one is
// configured. It is fire-and-forget by design: a successful Store/Update
// must NEVER fail because the LLM was slow or unreachable. Errors are logged
// and swallowed. Memory IDs are looked up at extract time (the caller passes
// a copy) so concurrent Update on the same memory is fine.
func (ms *Store) fanoutTripleExtraction(mem *Memory) {
	extractor := ms.activeTripleExtractor()
	if extractor == nil || mem == nil || strings.TrimSpace(mem.Content) == "" {
		return
	}

	memCopy := copyMemory(mem)
	ms.extractionWG.Add(1)
	go func() {
		defer ms.extractionWG.Done()
		defer func() {
			if r := recover(); r != nil {
				ms.logger.Error("triple extraction panic recovered",
					zap.String("memory_id", memCopy.ID),
					zap.Any("panic", r),
				)
			}
		}()
		// Bound the per-memory extraction so a hung provider cannot leak
		// goroutines indefinitely. 60s is generous for chat completions
		// even on slow models like Sonnet/GPT-4 class.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		triples, err := extractor.Extract(ctx, memCopy)
		if err != nil {
			ms.logger.Warn("triple extraction failed",
				zap.String("memory_id", memCopy.ID), zap.Error(err))
			return
		}
		if len(triples) == 0 {
			return
		}
		// Replace-all on update: drop any prior triples for this memory
		// before inserting the fresh batch. Slice 1 confirmed cascade
		// works on Delete; here we take the same approach explicitly so
		// repeat extractions don't pile up duplicates.
		if _, err := ms.DeleteTriplesForMemory(ctx, memCopy.ID); err != nil {
			ms.logger.Warn("triple replace failed at delete step",
				zap.String("memory_id", memCopy.ID), zap.Error(err))
			return
		}
		if err := ms.AddTriples(ctx, triples); err != nil {
			ms.logger.Warn("triple persist failed",
				zap.String("memory_id", memCopy.ID), zap.Error(err))
			return
		}
		ms.logger.Debug("triples extracted",
			zap.String("memory_id", memCopy.ID), zap.Int("count", len(triples)))
	}()
}

// OpenAIExtractorConfig is the runtime configuration for an
// OpenAI-compatible chat-completions extractor (DeepSeek, Together, Groq,
// any /v1/chat/completions endpoint).
type OpenAIExtractorConfig struct {
	BaseURL string        // e.g. https://api.deepseek.com/v1
	APIKey  string        // bearer token
	Model   string        // e.g. deepseek-chat / qwen2.5-72b-instruct
	Timeout time.Duration // per-request timeout, default 30s
}

// NewOpenAIExtractor builds a TripleExtractor that calls an
// OpenAI-compatible /chat/completions endpoint. Returns an error when
// required fields are missing — the caller should treat a nil-extractor
// case as "extraction disabled" and proceed without it.
func NewOpenAIExtractor(cfg OpenAIExtractorConfig, logger *zap.Logger) (TripleExtractor, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("triple extractor: base_url required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("triple extractor: api_key required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("triple extractor: model required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &openaiTripleExtractor{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}, nil
}

type openaiTripleExtractor struct {
	cfg    OpenAIExtractorConfig
	client *http.Client
	logger *zap.Logger
}

// extractorPrompt is the system prompt sent to the LLM. Kept minimal because
// chat-completion providers charge per token: every word beyond the contract
// shows up in monthly invoices over thousands of memories. We ask for a
// JSON array directly so json_object response_format is enough to enforce
// the shape.
const extractorPrompt = `You extract knowledge graph triples from technical engineering memories (decisions, runbooks, postmortems, code-change rationale).

Output rules:
- Return ONLY a JSON object {"triples": [{"subj": "...", "rel": "...", "obj": "..."}, ...]}.
- Emit 3-7 triples per memory. Skip noisy or trivial ones.
- subj/obj should be specific identifiers (service names, modules, files, commit shas, decision slugs). Prefer snake_case.
- rel is a short verb phrase (e.g. depends_on, blocks, supersedes, mitigates, owns, affects).
- Never invent facts not stated in the memory.`

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	ResponseFormat *responseFormat   `json:"response_format,omitempty"`
	Temperature    float64           `json:"temperature"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type extractedPayload struct {
	Triples []struct {
		Subj string `json:"subj"`
		Rel  string `json:"rel"`
		Obj  string `json:"obj"`
	} `json:"triples"`
}

func (e *openaiTripleExtractor) Extract(ctx context.Context, mem *Memory) ([]*Triple, error) {
	if mem == nil || strings.TrimSpace(mem.Content) == "" {
		return nil, nil
	}

	body := chatRequest{
		Model: e.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: extractorPrompt},
			{Role: "user", Content: buildExtractorUserMessage(mem)},
		},
		ResponseFormat: &responseFormat{Type: "json_object"},
		Temperature:    0,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call extractor: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("extractor http %d: %s", resp.StatusCode, truncateError(string(respBody)))
	}

	var chat chatResponse
	if err := json.Unmarshal(respBody, &chat); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if chat.Error != nil && chat.Error.Message != "" {
		return nil, fmt.Errorf("extractor error: %s", chat.Error.Message)
	}
	if len(chat.Choices) == 0 || chat.Choices[0].Message.Content == "" {
		return nil, fmt.Errorf("extractor: empty completion")
	}

	var parsed extractedPayload
	if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &parsed); err != nil {
		return nil, fmt.Errorf("decode triples: %w", err)
	}
	if len(parsed.Triples) == 0 {
		return nil, nil
	}

	out := make([]*Triple, 0, len(parsed.Triples))
	for _, raw := range parsed.Triples {
		t := &Triple{
			Subject:  strings.TrimSpace(raw.Subj),
			Relation: strings.TrimSpace(raw.Rel),
			Object:   strings.TrimSpace(raw.Obj),
			MemoryID: mem.ID,
			LinkType: LinkTypeExtracted,
			Weight:   1,
		}
		// Reuse Triple.validate() so partial/empty rows from the LLM are
		// skipped silently rather than failing the whole batch — the
		// model occasionally drops a field, and one bad triple should
		// not invalidate the rest of the response.
		if err := t.validate(); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func buildExtractorUserMessage(mem *Memory) string {
	var b strings.Builder
	if mem.Title != "" {
		b.WriteString("Title: ")
		b.WriteString(mem.Title)
		b.WriteByte('\n')
	}
	if mem.Type != "" {
		b.WriteString("Type: ")
		b.WriteString(string(mem.Type))
		b.WriteByte('\n')
	}
	if mem.Context != "" {
		b.WriteString("Context: ")
		b.WriteString(mem.Context)
		b.WriteByte('\n')
	}
	if len(mem.Tags) > 0 {
		b.WriteString("Tags: ")
		b.WriteString(strings.Join(mem.Tags, ", "))
		b.WriteByte('\n')
	}
	b.WriteString("Content:\n")
	b.WriteString(mem.Content)
	return b.String()
}

func truncateError(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
