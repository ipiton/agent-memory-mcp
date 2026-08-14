package config

import (
	"errors"
	"strings"
	"testing"
)

// T100. The extractor's key used to fall back to OPENAI_API_KEY regardless of
// where MCP_TRIPLE_EXTRACTOR_BASE_URL pointed, so a third-party endpoint
// received the OpenAI key and nothing looked wrong: the request succeeds and
// extraction works. These cases fix which pairings are allowed.
func TestTripleExtractorCredentials(t *testing.T) {
	const openAIURL = "https://api.openai.com/v1"

	cfg := func(extractorURL, extractorKey, openAIKey string) Config {
		return Config{
			TripleExtractor: TripleExtractorConfig{BaseURL: extractorURL, APIKey: extractorKey},
			Embeddings:      EmbeddingsConfig{OpenAIBaseURL: openAIURL, OpenAIAPIKey: openAIKey},
		}
	}

	t.Run("no extractor URL: fallback is the convenience it was written for", func(t *testing.T) {
		url, key, err := cfg("", "", "sk-openai").TripleExtractorCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != openAIURL || key != "sk-openai" {
			t.Fatalf("got (%q, %q), want (%q, %q)", url, key, openAIURL, "sk-openai")
		}
	})

	t.Run("same endpoint: fallback still applies", func(t *testing.T) {
		// Trailing slash and case must not defeat the comparison — otherwise the
		// narrowing would break a working install over punctuation.
		url, key, err := cfg("https://API.openai.com/v1/", "", "sk-openai").TripleExtractorCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "sk-openai" {
			t.Fatalf("key = %q, want the OpenAI key", key)
		}
		if url != "https://API.openai.com/v1/" {
			t.Fatalf("url = %q, want the configured extractor URL verbatim", url)
		}
	})

	t.Run("third-party endpoint without its own key is refused", func(t *testing.T) {
		_, _, err := cfg("https://openrouter.ai/api/v1", "", "sk-openai").TripleExtractorCredentials()
		if !errors.Is(err, ErrCrossFamilyCredential) {
			t.Fatalf("err = %v, want ErrCrossFamilyCredential", err)
		}
		// The message has to name both variables: the operator set one of them
		// and has no way to guess which pairing was rejected.
		for _, want := range []string{"MCP_TRIPLE_EXTRACTOR_BASE_URL", "MCP_TRIPLE_EXTRACTOR_API_KEY", "OPENAI_API_KEY"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error message does not mention %s:\n%v", want, err)
			}
		}
	})

	t.Run("third-party endpoint with its own key is fine", func(t *testing.T) {
		url, key, err := cfg("https://openrouter.ai/api/v1", "sk-or", "sk-openai").TripleExtractorCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://openrouter.ai/api/v1" || key != "sk-or" {
			t.Fatalf("got (%q, %q), want the extractor's own pair", url, key)
		}
	})

	t.Run("no OpenAI key either: nothing to leak, nothing to refuse", func(t *testing.T) {
		// The extractor will fail to construct downstream; that is not this
		// function's call to make. It must not invent an error of its own here.
		_, key, err := cfg("", "", "").TripleExtractorCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "" {
			t.Fatalf("key = %q, want empty", key)
		}
	})
}
