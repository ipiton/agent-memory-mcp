package embedder

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestProviderHealthCircuitBreaker pins the generic circuit breaker introduced
// by Round 3 M6: it trips after providerFailureThreshold consecutive failures,
// stays open during the cooldown, re-enables (one retry) once the cooldown
// elapses, and a success resets it.
func TestProviderHealthCircuitBreaker(t *testing.T) {
	h := &providerHealth{}

	if ok, _ := h.available(); !ok {
		t.Fatal("a fresh breaker must be available")
	}

	// Failures below the threshold must not trip it.
	for i := 1; i < providerFailureThreshold; i++ {
		if h.markFailure() {
			t.Fatalf("failure %d should not trip the breaker (threshold %d)", i, providerFailureThreshold)
		}
	}
	if ok, _ := h.available(); !ok {
		t.Fatalf("breaker must stay available before %d failures", providerFailureThreshold)
	}

	// The threshold-th failure trips it exactly once.
	if !h.markFailure() {
		t.Fatal("the threshold failure should trip the breaker")
	}
	if ok, _ := h.available(); ok {
		t.Fatal("a tripped breaker must be unavailable during cooldown")
	}
	if h.markFailure() {
		t.Fatal("an already-open breaker must not report tripping again")
	}

	// Simulate cooldown expiry: one retry is allowed and flagged.
	h.mu.Lock()
	h.disabledUntil = time.Now().Add(-time.Second)
	h.mu.Unlock()
	ok, retried := h.available()
	if !ok || !retried {
		t.Fatalf("after cooldown: available=%v retried=%v, want true,true", ok, retried)
	}

	// A success resets the counter so later failures start from zero again.
	h.markFailure()
	h.markSuccess()
	if ok, _ := h.available(); !ok {
		t.Fatal("markSuccess must reset the breaker to available")
	}
	if h.markFailure() {
		t.Fatal("after reset, a single failure should not trip the breaker")
	}
}

// TestCandidatesSkipsDisabledProvider proves the breaker actually removes a
// provider from the fallback chain — the reliability win of M6 (previously only
// Jina could be skipped; OpenAI/Ollama were retried on every call).
func TestCandidatesSkipsDisabledProvider(t *testing.T) {
	e, err := New(Config{
		OpenAIToken:   "test-token",
		OllamaBaseURL: "http://localhost:11434",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := e.healthFor("openai")
	h.mu.Lock()
	h.disabledUntil = time.Now().Add(time.Hour)
	h.mu.Unlock()

	for _, c := range e.candidates("retrieval.passage") {
		if c.provider.name() == "openai" {
			t.Fatal("a disabled provider (openai) must be skipped in the candidate chain")
		}
	}
}
