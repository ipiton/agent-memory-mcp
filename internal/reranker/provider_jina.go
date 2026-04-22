package reranker

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

// defaultJinaRerankURL is the production Jina rerank endpoint. Config.Endpoint
// overrides this for tests.
const defaultJinaRerankURL = "https://api.jina.ai/v1/rerank"

// defaultJinaModel is the multilingual v2 base model that was benchmarked
// well for technical/code retrieval and supports the same language mix as
// jina-embeddings-v3.
const defaultJinaModel = "jina-reranker-v2-base-multilingual"

// jinaReranker is a thin HTTP adapter over Jina's rerank API. It holds a
// reusable *http.Client for connection keepalive; timeouts are enforced by
// the caller via context.WithTimeout on Rerank(ctx, ...).
type jinaReranker struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
	logger   *zap.Logger
	topN     int
}

func newJinaReranker(cfg Config, logger *zap.Logger) *jinaReranker {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultJinaRerankURL
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultJinaModel
	}
	// The http.Client.Timeout acts as a safety belt if the caller forgot to
	// wrap ctx; it's set to something larger than the documented 5s caller
	// timeout so it doesn't pre-empt a context deadline.
	clientTimeout := 30 * time.Second
	if cfg.Timeout > 0 && cfg.Timeout < clientTimeout {
		clientTimeout = cfg.Timeout + 5*time.Second
	}
	return &jinaReranker{
		endpoint: endpoint,
		model:    model,
		apiKey:   cfg.APIKey,
		client:   &http.Client{Timeout: clientTimeout},
		logger:   logger,
		topN:     cfg.TopN,
	}
}

type jinaRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

type jinaRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type jinaRerankResponse struct {
	Results []jinaRerankResult `json:"results"`
}

// Rerank sends the candidates to Jina and maps the returned indices back to
// candidate IDs.
//
// Guarantees:
//   - Returns nil, nil for empty candidates (no HTTP call).
//   - Returns ctx.Err() when the context is canceled or deadline exceeded.
//   - Returns a non-nil error on non-2xx HTTP status, malformed JSON, or
//     out-of-range indices.
//   - Never panics — index-out-of-bounds is surfaced as an error so the
//     caller can fall back to hybrid ordering cleanly.
func (j *jinaReranker) Rerank(ctx context.Context, query string, candidates []Candidate) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	documents := make([]string, len(candidates))
	for i, c := range candidates {
		documents[i] = formatDocument(c)
	}

	reqBody := jinaRerankRequest{
		Model:     j.model,
		Query:     query,
		Documents: documents,
		TopN:      len(candidates), // ask for all — caller already sliced to TopN
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("reranker: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("reranker: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(j.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+j.apiKey)
	}

	resp, err := j.client.Do(req)
	if err != nil {
		// Propagate context errors verbatim so callers can recognize them.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("reranker: http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("reranker: jina returned status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 256))
	}

	var decoded jinaRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("reranker: decode response: %w", err)
	}

	scored := make([]Scored, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		if r.Index < 0 || r.Index >= len(candidates) {
			return nil, fmt.Errorf("reranker: index %d out of range [0,%d)", r.Index, len(candidates))
		}
		scored = append(scored, Scored{
			ID:    candidates[r.Index].ID,
			Score: r.RelevanceScore,
		})
	}
	return scored, nil
}

// formatDocument renders a candidate as a single document string for the
// cross-encoder. Title + content is the format Jina's examples use and it
// keeps the section heading visible to the model.
func formatDocument(c Candidate) string {
	title := strings.TrimSpace(c.Title)
	content := strings.TrimSpace(c.Content)
	if title == "" {
		return content
	}
	if content == "" {
		return title
	}
	return title + "\n" + content
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
