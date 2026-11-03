package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// countingEmbedder proves the refusal comes before the provider call.
type countingEmbedder struct {
	slowEmbedder
}

func TestStoreRefusesProviderTokenBeforeEmbedding(t *testing.T) {
	emb := &countingEmbedder{}
	store := newTestStoreWithEmbedder(t, emb)
	ctx := context.Background()
	token := "gh" + "p_" + strings.Repeat("a1B2", 9)

	for name, m := range map[string]*Memory{
		"content": {Content: "deploy with " + token, Type: TypeSemantic},
		"title":   {Content: "note", Title: token, Type: TypeSemantic},
		"context": {Content: "note", Context: token, Type: TypeSemantic},
	} {
		err := store.Store(ctx, m)
		if !errors.Is(err, ErrSecretDetected) {
			t.Fatalf("%s: err = %v, want ErrSecretDetected", name, err)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("%s: the error repeats the token: %v", name, err)
		}
	}
	if emb.peak.Load() != 0 {
		t.Error("the embedder saw a refused write")
	}
	if got, err := store.List(ctx, Filters{}, 10); err != nil || len(got) != 0 {
		t.Errorf("stored %d rows after refusal, err %v", len(got), err)
	}
}

func TestStoreMasksAssignmentAndKeepsProse(t *testing.T) {
	store := newTestStoreWithEmbedder(t, &countingEmbedder{})
	ctx := context.Background()
	m := &Memory{Content: "API_TOKEN=abcdef123456\nтокен: строка в заголовке", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(got.Content, "abcdef123456") || !strings.Contains(got.Content, "API_TOKEN=[REDACTED]") {
		t.Errorf("assignment not masked: %q", got.Content)
	}
	if !strings.Contains(got.Content, "токен: строка в заголовке") {
		t.Errorf("prose was changed: %q", got.Content)
	}
}

func TestUpdateRefusesProviderToken(t *testing.T) {
	store := newTestStoreWithEmbedder(t, &countingEmbedder{})
	ctx := context.Background()
	m := &Memory{Content: "clean note", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	token := "AK" + "IA" + "ABCDEFGHIJKLMNOP"
	if err := store.Update(ctx, m.ID, Update{Content: "now with " + token}); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("err = %v, want ErrSecretDetected", err)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "clean note" {
		t.Errorf("content = %q", got.Content)
	}
}
