package server

import (
	"encoding/json"
	"testing"
)

// The tools that remove, merge, archive or overwrite entries (T183). A client
// that asks for confirmation on destructiveHint must stop on each of them.
var wantDestructive = []string{
	"delete_memory",
	"update_memory",
	"merge_duplicates",
	"close_session",
	"analyze_session",
	"accept_session_changes",
	"end_task",
	"sweep_archive",
	"steward_run",
	"steward_inbox_resolve",
}

// TestEveryToolIsAnnotated is the drift guard between the tool registry and
// toolSafety: a new tool without a safety decision fails here, and so does a
// classification left behind for a tool that no longer exists.
func TestEveryToolIsAnnotated(t *testing.T) {
	defs := append(mainToolDefs(), stewardToolDefs()...)
	registered := make(map[string]bool, len(defs))
	for _, d := range defs {
		registered[d.Name] = true
		if _, ok := toolSafety[d.Name]; !ok {
			t.Errorf("tool %q has no entry in toolSafety", d.Name)
		}
	}
	for name := range toolSafety {
		if !registered[name] {
			t.Errorf("toolSafety classifies %q, which is not a registered tool", name)
		}
	}
	handlers := newTestServer(t, "").toolHandlers
	if len(handlers) != len(toolSafety) {
		t.Errorf("toolSafety has %d entries, dispatch table %d handlers", len(toolSafety), len(handlers))
	}

	for _, name := range wantDestructive {
		a := toolSafety[name]
		if !a.DestructiveHint || a.ReadOnlyHint {
			t.Errorf("%s: %+v, want destructive and not read-only", name, a)
		}
	}
	for name, a := range toolSafety {
		if a.ReadOnlyHint && a.DestructiveHint {
			t.Errorf("%s is both read-only and destructive", name)
		}
		if a.OpenWorldHint {
			t.Errorf("%s claims an open world; every tool works on local state", name)
		}
	}
}

// listedAnnotations calls tools/list and returns each tool's raw annotations
// object as the client sees it on the wire.
func listedAnnotations(t *testing.T, s *MCPServer) map[string]map[string]any {
	t.Helper()
	res, rErr := s.handleToolsList(nil)
	if rErr != nil {
		t.Fatalf("tools/list: %v", rErr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]map[string]any, len(list.Tools))
	for _, tl := range list.Tools {
		out[tl.Name] = tl.Annotations
	}
	return out
}

// TestToolsListCarriesExplicitHints checks the wire: every tool in tools/list
// states all four hints, none left to the spec defaults (destructiveHint
// defaults to true, which would mark every recall as dangerous).
func TestToolsListCarriesExplicitHints(t *testing.T) {
	for _, grouping := range []bool{false, true} {
		s := newMemoryTestServer(t)
		s.config.ToolGrouping = grouping
		listed := listedAnnotations(t, s)
		if len(listed) == 0 {
			t.Fatal("tools/list is empty")
		}
		for name, a := range listed {
			for _, hint := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
				if _, ok := a[hint].(bool); !ok {
					t.Errorf("grouping=%v %s: annotations lack %s: %v", grouping, name, hint, a)
				}
			}
		}
		if grouping {
			// A meta-tool is gated as a whole: one destructive action makes it
			// destructive, one writing action takes away read-only.
			if listed["memory"]["destructiveHint"] != true {
				t.Errorf("grouped memory (has delete): %v", listed["memory"])
			}
			if listed["search"]["readOnlyHint"] != true || listed["search"]["destructiveHint"] != false {
				t.Errorf("grouped search (retrieval only): %v", listed["search"])
			}
			if listed["engineering"]["readOnlyHint"] != false || listed["engineering"]["destructiveHint"] != false {
				t.Errorf("grouped engineering (stores only): %v", listed["engineering"])
			}
			continue
		}
		for _, name := range []string{"delete_memory", "merge_duplicates", "sweep_archive"} {
			if listed[name]["destructiveHint"] != true {
				t.Errorf("%s: %v, want destructiveHint=true", name, listed[name])
			}
		}
		if listed["recall_memory"]["readOnlyHint"] != true || listed["recall_memory"]["destructiveHint"] != false {
			t.Errorf("recall_memory: %v, want read-only", listed["recall_memory"])
		}
	}
}
