package embedder

import (
	"context"
	"fmt"
	"strings"
)

// llamaCPPAdapter targets a llama.cpp server started with
// `llama-server --embedding`, which exposes an OpenAI-compatible
// /v1/embeddings endpoint. The request shape is identical to OpenAI minus the
// `dimensions` parameter: llama.cpp returns the model's native dimension (e.g.
// 1024 for bge-m3), and dimension consistency is enforced by validateEmbedding.
type llamaCPPAdapter struct {
	embedder *Embedder
}

func (a llamaCPPAdapter) name() string {
	return "llamacpp"
}

func (a llamaCPPAdapter) modelID() string {
	return a.embedder.llamaCPPModelID()
}

func (a llamaCPPAdapter) embed(ctx context.Context, text, _ string) ([]float32, error) {
	payload := map[string]any{
		"input": text,
		"model": a.model(),
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, a.endpoint(), nil, payload, &response); err != nil {
		return nil, fmt.Errorf("llama.cpp request failed: %w", err)
	}
	return singleEmbeddingFromCompatibleResponse("llama.cpp", response)
}

func (a llamaCPPAdapter) batchEmbed(ctx context.Context, texts []string, _ string) ([][]float32, error) {
	payload := map[string]any{
		"input": texts,
		"model": a.model(),
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, a.endpoint(), nil, payload, &response); err != nil {
		return nil, fmt.Errorf("llama.cpp batch request failed: %w", err)
	}
	return batchEmbeddingsFromCompatibleResponse("llama.cpp", response, len(texts))
}

func (a llamaCPPAdapter) model() string {
	if strings.TrimSpace(a.embedder.config.LlamaCPPModel) == "" {
		return defaultLlamaCPPModel
	}
	return a.embedder.config.LlamaCPPModel
}

func (a llamaCPPAdapter) endpoint() string {
	baseURL := a.embedder.config.LlamaCPPBaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultLlamaCPPBaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/embeddings"
}
