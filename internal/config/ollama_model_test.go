package config

import "testing"

func TestLoadFromEnvOllamaModelSelection(t *testing.T) {
	for _, model := range []string{"", "embeddinggemma:300m", " embeddinggemma:300m "} {
		t.Run(model, func(t *testing.T) {
			hermeticDotEnv(t)
			t.Setenv("MCP_ROOT", ".")
			t.Setenv("OLLAMA_EMBEDDING_MODEL", model)
			cfg, err := LoadFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			want := "embeddinggemma:300m"
			if model == "" {
				want = ""
			}
			if cfg.Embeddings.OllamaModel != want || cfg.EmbedderConfig().OllamaModel != want {
				t.Fatalf("configured model = %q, embedder model = %q, want %q", cfg.Embeddings.OllamaModel, cfg.EmbedderConfig().OllamaModel, want)
			}
		})
	}
}

func TestOllamaModelChangeRequiresRAGReload(t *testing.T) {
	before := Config{Embeddings: EmbeddingsConfig{OllamaModel: "bge-m3:latest"}}
	after := before
	after.Embeddings.OllamaModel = "embeddinggemma:300m"
	if !ragConfigChanged(before, after) {
		t.Fatal("Ollama model change was not detected for RAG reload")
	}
}
