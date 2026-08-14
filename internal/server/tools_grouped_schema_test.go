package server

import (
	"strings"
	"testing"
)

func groupedToolByName(t *testing.T, name string) tool {
	t.Helper()
	for _, tl := range buildGroupedList(mainToolDefs()) {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("grouped tool %q not found", name)
	return tool{}
}

func actionEnumDescription(t *testing.T, tl tool) string {
	t.Helper()
	props, _ := tl.InputSchema["properties"].(map[string]any)
	action, _ := props["action"].(map[string]any)
	desc, _ := action["description"].(string)
	if desc == "" {
		t.Fatalf("tool %q has no action description", tl.Name)
	}
	return desc
}

// T91 M8. The grouped schema can only require `action`, so each member's own
// required list was dropped and the model had to guess which arguments an
// action needs. They are stated in the enum help instead.
func TestGroupedActionEnumStatesRequiredFields(t *testing.T) {
	desc := actionEnumDescription(t, groupedToolByName(t, "memory"))

	if !strings.Contains(desc, "- store:") {
		t.Fatalf("action description does not list the store action:\n%s", desc)
	}
	if !strings.Contains(desc, "[req: content]") {
		t.Fatalf("store action does not state its required field:\n%s", desc)
	}

	// An action with no required arguments must not carry an empty marker.
	for _, line := range strings.Split(desc, "\n") {
		if strings.Contains(line, "[req: ]") {
			t.Fatalf("empty required marker on line: %q", line)
		}
	}
}

// A property whose binding constraints differ between actions must be called
// out. Merging first-writer-wins and saying nothing presents one action's
// constraint as if it held for all — the model has no reason to doubt it.
func TestGroupedToolNamesDivergentProperties(t *testing.T) {
	memory := groupedToolByName(t, "memory")

	if !strings.Contains(memory.Description, "Varies by action:") {
		t.Fatalf("memory tool does not flag its divergent properties:\n%s", memory.Description)
	}
	// `type` genuinely differs: store takes the memory-type enum, list also
	// accepts "all".
	if !strings.Contains(memory.Description, "type") {
		t.Errorf("memory tool does not name `type` as divergent:\n%s", memory.Description)
	}
}

// Prose differences are not constraint differences. Flagging every argument
// whose wording varies would warn about nearly all of them, which is the same
// as warning about none.
func TestGroupedDivergenceIgnoresDescriptionOnlyDifferences(t *testing.T) {
	variants := map[string][]propertyVariant{}
	recordPropertyVariant(variants, "context", "a", map[string]any{"type": "string", "description": "one wording"})
	recordPropertyVariant(variants, "context", "b", map[string]any{"type": "string", "description": "another wording"})

	if got := divergentPropertyNames(variants); len(got) != 0 {
		t.Fatalf("divergent = %v, want none for a description-only difference", got)
	}

	recordPropertyVariant(variants, "limit", "a", map[string]any{"type": "integer", "maximum": 50})
	recordPropertyVariant(variants, "limit", "b", map[string]any{"type": "integer", "maximum": 200})

	got := divergentPropertyNames(variants)
	if len(got) != 1 || got[0] != "limit" {
		t.Fatalf("divergent = %v, want [limit] — a differing maximum is a real conflict", got)
	}
}

// Annotating the merged view must not rewrite the flat tool's own schema: the
// grouped properties alias the member maps.
func TestGroupedAnnotationDoesNotMutateFlatSchemas(t *testing.T) {
	flat := mainToolDefs()

	var before string
	for _, tl := range flat {
		if tl.Name == "store_memory" {
			props, _ := tl.InputSchema["properties"].(map[string]any)
			typ, _ := props["type"].(map[string]any)
			before, _ = typ["description"].(string)
		}
	}

	_ = buildGroupedList(flat)

	for _, tl := range mainToolDefs() {
		if tl.Name == "store_memory" {
			props, _ := tl.InputSchema["properties"].(map[string]any)
			typ, _ := props["type"].(map[string]any)
			after, _ := typ["description"].(string)
			if after != before {
				t.Fatalf("flat store_memory type description changed after grouping:\n before: %q\n after:  %q", before, after)
			}
		}
	}
}
