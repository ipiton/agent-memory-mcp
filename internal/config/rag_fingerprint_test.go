package config

import (
	"reflect"
	"slices"
	"testing"
)

// T91 M13: the fingerprint is the reload contract in one place — it decides
// whether an edit rebuilds the RAG engine. It is also hand-maintained, so a new
// RAG-affecting field added to Config without a matching line here produces a
// silent no-op reload: the operator's change is read from the file (since T89
// H5, it genuinely is) and then discarded because the fingerprint compared
// equal. This golden list makes such a change fail here instead.
//
// When you legitimately add or remove a field, update the list in the same
// commit — that is the point at which someone has to consider the reload
// semantics.
func TestRAGFingerprintFieldSetIsPinned(t *testing.T) {
	want := []string{
		"AutoIndex",
		"ChunkOverlap",
		"ChunkSize",
		"EmbeddingMode",
		"Enabled",
		"FileWatcher",
		"IndexDirs",
		"IndexExcludeDirs",
		"IndexExcludeGlobs",
		"IndexPath",
		"JinaAPIKey",
		"OllamaBaseURL",
		"OpenAIAPIKey",
		"OpenAIBaseURL",
		"OpenAIModel",
		"RedactSecrets",
		"RootPath",
	}

	typ := reflect.TypeOf(ragFingerprint{})
	got := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("ragFingerprint fields changed.\n got: %v\nwant: %v\n\nIf this is intentional, update the list here and confirm the reload semantics are what you want.", got, want)
	}
}

// Every field must actually be read from the Config — a field that is declared
// but left at its zero value in ragFingerprintOf never triggers a rebuild, and
// the omission is invisible.
func TestRAGFingerprintReadsEveryField(t *testing.T) {
	base := Config{}
	baseline := ragFingerprintOf(base)

	// A config where every fingerprinted source field differs from the zero value.
	full := Config{
		RootPath: "/tmp/root",
		RAG: RAGConfig{
			Enabled:           true,
			IndexPath:         "/tmp/index",
			ChunkSize:         1234,
			ChunkOverlap:      56,
			AutoIndex:         true,
			FileWatcher:       true,
			RedactSecrets:     true,
			IndexDirs:         []string{"docs"},
			IndexExcludeDirs:  []string{"vendor"},
			IndexExcludeGlobs: []string{"*.min.js"},
		},
		Embeddings: EmbeddingsConfig{
			Mode:          "local",
			JinaAPIKey:    "jina",
			OpenAIAPIKey:  "openai",
			OpenAIBaseURL: "https://example.test/v1",
			OpenAIModel:   "model",
			OllamaBaseURL: "http://localhost:11434",
		},
	}

	fp := reflect.ValueOf(ragFingerprintOf(full))
	zero := reflect.ValueOf(baseline)
	typ := fp.Type()
	for i := range typ.NumField() {
		if reflect.DeepEqual(fp.Field(i).Interface(), zero.Field(i).Interface()) {
			t.Errorf("ragFingerprint.%s is identical for an empty and a fully-populated config — it is probably not wired to a Config field", typ.Field(i).Name)
		}
	}
}

// The payoff, stated as behaviour: changing a fingerprinted field is seen as a
// RAG change, and changing something outside the set is not.
func TestRAGConfigChangedTracksTheFingerprint(t *testing.T) {
	base := Config{RAG: RAGConfig{Enabled: true, ChunkSize: 2000}}

	rag := base
	rag.RAG.ChunkSize = 4000
	if !ragConfigChanged(base, rag) {
		t.Error("a chunk-size change was not seen as a RAG change")
	}

	unrelated := base
	unrelated.HTTP.Port = 9999
	if ragConfigChanged(base, unrelated) {
		t.Error("an HTTP port change was treated as a RAG change")
	}
}
