package embedder

import (
	"context"
	"fmt"
	"strings"
)

type jinaAdapter struct {
	embedder *Embedder
}

func (a jinaAdapter) name() string {
	return "jina"
}

func (a jinaAdapter) modelID() string {
	return a.embedder.jinaModelID()
}

func (a jinaAdapter) embed(ctx context.Context, text, task string) ([]float32, error) {
	payload := map[string]any{
		"input":           []string{text},
		"model":           "jina-embeddings-v3",
		"encoding_format": "float",
		"dimensions":      a.embedder.Dimension,
		"task":            task,
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, jinaEmbeddingsURL, map[string]string{"Authorization": "Bearer " + a.embedder.config.JinaToken}, payload, &response); err != nil {
		return nil, fmt.Errorf("jina AI request failed: %w", err)
	}
	return singleEmbeddingFromCompatibleResponse("jina AI", response)
}

func (a jinaAdapter) batchEmbed(ctx context.Context, texts []string, task string) ([][]float32, error) {
	payload := map[string]any{
		"input":           texts,
		"model":           "jina-embeddings-v3",
		"encoding_format": "float",
		"dimensions":      a.embedder.Dimension,
		"task":            task,
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, jinaEmbeddingsURL, map[string]string{"Authorization": "Bearer " + a.embedder.config.JinaToken}, payload, &response); err != nil {
		return nil, fmt.Errorf("jina batch request failed: %w", err)
	}
	return batchEmbeddingsFromCompatibleResponse("jina", response, len(texts))
}

type openAIAdapter struct {
	embedder *Embedder
}

func (a openAIAdapter) name() string {
	return "openai"
}

func (a openAIAdapter) modelID() string {
	return a.embedder.openAIModelID()
}

func (a openAIAdapter) embed(ctx context.Context, text, _ string) ([]float32, error) {
	payload := map[string]any{
		"input":      text,
		"model":      a.model(),
		"dimensions": a.embedder.Dimension,
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, a.endpoint(), map[string]string{"Authorization": "Bearer " + a.embedder.config.OpenAIToken}, payload, &response); err != nil {
		return nil, fmt.Errorf("OpenAI API request failed: %w", err)
	}
	return singleEmbeddingFromCompatibleResponse("OpenAI", response)
}

func (a openAIAdapter) batchEmbed(ctx context.Context, texts []string, _ string) ([][]float32, error) {
	payload := map[string]any{
		"input":      texts,
		"model":      a.model(),
		"dimensions": a.embedder.Dimension,
	}
	var response compatibleEmbeddingsResponse
	if err := a.embedder.postJSON(ctx, a.endpoint(), map[string]string{"Authorization": "Bearer " + a.embedder.config.OpenAIToken}, payload, &response); err != nil {
		return nil, fmt.Errorf("OpenAI batch request failed: %w", err)
	}
	return batchEmbeddingsFromCompatibleResponse("OpenAI", response, len(texts))
}

func (a openAIAdapter) model() string {
	if strings.TrimSpace(a.embedder.config.OpenAIModel) == "" {
		return defaultOpenAIModel
	}
	return a.embedder.config.OpenAIModel
}

func (a openAIAdapter) endpoint() string {
	baseURL := a.embedder.config.OpenAIBaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/embeddings"
}
