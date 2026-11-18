package vectorstore

import (
	"reflect"
	"testing"
)

func TestKeywordQueryTermsIgnoreEnglishFunctionWords(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"How is a canary deployment rolled back?", []string{"canary", "deployment", "rolled", "back"}},
		{"Which gateway routes have already been approved?", []string{"gateway", "routes", "already", "approved"}},
		{"What was decided about GRPC_KEEPALIVE_TIME_MS?", []string{"what", "was", "decided", "about", "grpc", "keepalive", "time", "ms"}},
		{"Как откатываются миграции?", stemTokens([]string{"как", "откатываются", "миграции"})},
		{"the who", []string{"the", "who"}},
		{"to", []string{"to"}},
		{"DO_NOT_RETRY", []string{"do", "not", "retry"}},
		{"CAN-RETRY", []string{"can", "retry"}},
		{"api.is_ready", []string{"api", "is", "ready"}},
		{"", []string{}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := keywordQueryTerms(tc.query)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("terms = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeywordSearchDoesNotRewardEnglishQuestionGrammar(t *testing.T) {
	store := newTestStore(t)
	if err := store.Upsert([]Chunk{
		{ID: "guide", DocPath: "docs/procedure.md", Content: "canary deployment rollback"},
		{ID: "grammar", DocPath: "docs/other.md", Content: "what is the and which was were been with from should must"},
	}); err != nil {
		t.Fatal(err)
	}
	ids := keywordIDs(t, store, "What is the canary deployment rollback and which should have been used?")
	if !reflect.DeepEqual(ids, []string{"guide"}) {
		t.Fatalf("keyword matches = %v, want only the procedure", ids)
	}
}

func TestKeywordSearchRetainsExplicitPhraseAndIdentifier(t *testing.T) {
	store := newTestStore(t)
	if err := store.Upsert([]Chunk{
		{ID: "phrase", DocPath: "docs/phrase.md", Title: "Configuration", Content: "This is the way"},
		{ID: "generic", DocPath: "docs/way.md", Title: "Way", Content: "Way guidance"},
		{ID: "identifier", DocPath: "docs/client.md", Content: "GRPC_KEEPALIVE_TIME_MS sets the keepalive interval."},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		id    string
	}{
		{"this is the way", "phrase"},
		{"GRPC_KEEPALIVE_TIME_MS", "identifier"},
	} {
		ids := keywordIDs(t, store, tc.query)
		if len(ids) == 0 || ids[0] != tc.id {
			t.Fatalf("search %q = %v, want %q first", tc.query, ids, tc.id)
		}
	}
}

func TestKeywordSearchKeepsFunctionWordsInsideIdentifiers(t *testing.T) {
	for _, identifier := range []string{"DO_NOT_RETRY", "CAN_RETRY", "CAN-RETRY", "can.retry"} {
		t.Run(identifier, func(t *testing.T) {
			store := newTestStore(t)
			if err := store.Upsert([]Chunk{
				{ID: "exact", DocPath: "docs/errors.md", Title: "Errors", Content: identifier},
				{ID: "general", DocPath: "docs/retry.md", Title: "Retry", Content: "Retry guidance"},
			}); err != nil {
				t.Fatal(err)
			}
			ids := keywordIDs(t, store, identifier)
			if len(ids) == 0 || ids[0] != "exact" {
				t.Fatalf("search %q = %v, want the exact identifier first", identifier, ids)
			}
		})
	}
}
