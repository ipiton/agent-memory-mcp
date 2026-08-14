package server

import "testing"

// T96: an empty value is how a metadata key is REMOVED (updateLocked deletes on
// it). The shared getStringMap drops empty values, so routing the patch through
// it would turn every removal into a silent no-op — which is what left canonical
// records stamped as verified by their own promotion, uncorrectable.
func TestMetadataPatchKeepsEmptyValues(t *testing.T) {
	patch := metadataPatchFromArgs(map[string]any{
		"metadata": map[string]any{
			"last_verified_at": "",
			"owner":            "engineering",
			"  spaced  ":       "  value  ",
			"":                 "ignored",
			"explicit_null":    nil,
		},
	})

	if got, ok := patch["last_verified_at"]; !ok || got != "" {
		t.Errorf(`patch["last_verified_at"] = %q, present=%v — want an empty value, kept`, got, ok)
	}
	if got := patch["explicit_null"]; got != "" {
		t.Errorf(`patch["explicit_null"] = %q, want "" (JSON null is a removal too)`, got)
	}
	if got := patch["owner"]; got != "engineering" {
		t.Errorf(`patch["owner"] = %q, want "engineering"`, got)
	}
	if got := patch["spaced"]; got != "value" {
		t.Errorf(`patch["spaced"] = %q, want "value" — keys and values are trimmed`, got)
	}
	if _, blank := patch[""]; blank {
		t.Error("a blank key made it into the patch")
	}
}

func TestMetadataPatchAbsentIsNil(t *testing.T) {
	if patch := metadataPatchFromArgs(map[string]any{"id": "x"}); patch != nil {
		t.Errorf("metadataPatchFromArgs(no metadata) = %v, want nil", patch)
	}
	if patch := metadataPatchFromArgs(map[string]any{"metadata": "not an object"}); patch != nil {
		t.Errorf("metadataPatchFromArgs(wrong type) = %v, want nil", patch)
	}
}
