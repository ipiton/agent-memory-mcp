package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const jinaEmbeddingsURL = "https://api.jina.ai/v1/embeddings"

type indexedEmbeddingItem struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type compatibleEmbeddingsResponse struct {
	Data []indexedEmbeddingItem `json:"data"`
}

type ollamaEmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

type ollamaBatchEmbeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

func (e *Embedder) postJSON(ctx context.Context, url string, headers map[string]string, payload any, out any) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("returned status %d: %s", resp.StatusCode, sanitizeErrorBody(body))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

func singleEmbeddingFromCompatibleResponse(provider string, response compatibleEmbeddingsResponse) ([]float32, error) {
	if len(response.Data) == 0 {
		return nil, fmt.Errorf("%s returned no embeddings", provider)
	}
	return float64SliceToFloat32(response.Data[0].Embedding), nil
}

func batchEmbeddingsFromCompatibleResponse(provider string, response compatibleEmbeddingsResponse, expected int) ([][]float32, error) {
	if len(response.Data) != expected {
		return nil, fmt.Errorf("%s batch: got %d embeddings for %d texts", provider, len(response.Data), expected)
	}

	embeddings := make([][]float32, expected)
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= expected {
			return nil, fmt.Errorf("%s batch: invalid index %d", provider, item.Index)
		}
		embeddings[item.Index] = float64SliceToFloat32(item.Embedding)
	}
	return embeddings, nil
}

func float64SliceToFloat32(values []float64) []float32 {
	result := make([]float32, len(values))
	for i, value := range values {
		result[i] = float32(value)
	}
	return result
}

func sanitizeErrorBody(body []byte) string {
	const maxLen = 200
	s := strings.TrimSpace(string(body))
	if len(s) > maxLen {
		return s[:maxLen] + "... (truncated)"
	}
	return s
}
