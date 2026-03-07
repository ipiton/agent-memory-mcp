package embedder

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

type ollamaAdapter struct {
	embedder *Embedder
	model    string
}

func (a ollamaAdapter) name() string {
	return "ollama/" + strings.TrimSuffix(a.model, ":latest")
}

func (a ollamaAdapter) modelID() string {
	return a.embedder.ollamaModelID(a.model)
}

func (a ollamaAdapter) embed(text, _ string) ([]float32, error) {
	payload := map[string]any{
		"model":  a.model,
		"prompt": text,
	}

	for attempt := 0; attempt <= a.embedder.config.MaxRetries; attempt++ {
		if attempt > 0 {
			a.logRetry("Retrying Ollama embed, model may be loading", attempt)
		}

		var response ollamaEmbeddingResponse
		err := a.embedder.postJSON(a.singleEndpoint(), nil, payload, &response)
		if err != nil {
			if attempt < a.embedder.config.MaxRetries {
				continue
			}
			return nil, fmt.Errorf("ollama %s request failed: %w", a.model, err)
		}
		if len(response.Embedding) == 0 {
			if attempt < a.embedder.config.MaxRetries {
				a.embedder.logger.Warn("Ollama returned empty embedding, model may be loading", zap.String("model", a.model))
				continue
			}
			return nil, fmt.Errorf("ollama %s returned empty embedding after retries", a.model)
		}
		return float64SliceToFloat32(response.Embedding), nil
	}

	return nil, fmt.Errorf("ollama %s: all retries exhausted", a.model)
}

func (a ollamaAdapter) batchEmbed(texts []string, _ string) ([][]float32, error) {
	const subBatchSize = 10

	allEmbeddings := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += subBatchSize {
		end := start + subBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		embeddings, err := a.embedSubBatch(texts[start:end])
		if err != nil {
			return nil, err
		}
		copy(allEmbeddings[start:end], embeddings)
	}
	return allEmbeddings, nil
}

func (a ollamaAdapter) embedSubBatch(texts []string) ([][]float32, error) {
	payload := map[string]any{
		"model": a.model,
		"input": texts,
	}

	for attempt := 0; attempt <= a.embedder.config.MaxRetries; attempt++ {
		if attempt > 0 {
			a.logRetry("Retrying Ollama batch embed, model may be loading", attempt)
		}

		var response ollamaBatchEmbeddingResponse
		err := a.embedder.postJSON(a.batchEndpoint(), nil, payload, &response)
		if err != nil {
			if attempt < a.embedder.config.MaxRetries {
				continue
			}
			return nil, fmt.Errorf("ollama %s batch request failed: %w", a.model, err)
		}
		if len(response.Embeddings) == 0 || len(response.Embeddings[0]) == 0 {
			if attempt < a.embedder.config.MaxRetries {
				a.embedder.logger.Warn("Ollama batch returned empty embeddings, model may be loading", zap.String("model", a.model))
				continue
			}
			return nil, fmt.Errorf("ollama %s batch returned empty embeddings after retries", a.model)
		}
		if len(response.Embeddings) != len(texts) {
			return nil, fmt.Errorf("ollama %s batch: got %d embeddings for %d texts", a.model, len(response.Embeddings), len(texts))
		}

		embeddings := make([][]float32, len(response.Embeddings))
		for i, embedding := range response.Embeddings {
			if len(embedding) != a.embedder.Dimension {
				return nil, fmt.Errorf("ollama %s dimension mismatch at index %d: got %d, expected %d", a.model, i, len(embedding), a.embedder.Dimension)
			}
			embeddings[i] = float64SliceToFloat32(embedding)
		}
		return embeddings, nil
	}

	return nil, fmt.Errorf("ollama %s batch: all retries exhausted", a.model)
}

func (a ollamaAdapter) singleEndpoint() string {
	return strings.TrimRight(a.embedder.config.OllamaBaseURL, "/") + "/api/embeddings"
}

func (a ollamaAdapter) batchEndpoint() string {
	return strings.TrimRight(a.embedder.config.OllamaBaseURL, "/") + "/api/embed"
}

func (a ollamaAdapter) logRetry(message string, attempt int) {
	delay := time.Duration(attempt*2) * time.Second
	a.embedder.logger.Info(message,
		zap.String("model", a.model),
		zap.Int("attempt", attempt+1),
		zap.Duration("delay", delay),
	)
	time.Sleep(delay)
}
