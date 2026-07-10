package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// allToolDefsForTest returns the full flat registry (core + steward), the
// superset the grouping builder folds from.
func allToolDefsForTest() []tool {
	return append(mainToolDefs(), stewardToolDefs()...)
}

// TestGroupedSchemaIntegrity pins the invariants the grouping design relies on:
// every action maps to a real tool, meta names never collide with legacy names,
// actions are unique per group, and no grouped member uses `action` as a real
// argument (which the discriminator would otherwise shadow).
func TestGroupedSchemaIntegrity(t *testing.T) {
	byName := make(map[string]tool)
	for _, td := range allToolDefsForTest() {
		byName[td.Name] = td
	}

	metaNames := make(map[string]bool)
	for _, g := range groupedSpecs {
		metaNames[g.Name] = true
	}

	for _, g := range groupedSpecs {
		if byName[g.Name].Name != "" {
			t.Errorf("meta-tool %q collides with a legacy tool name", g.Name)
		}
		seenAction := make(map[string]bool)
		for _, a := range g.Actions {
			if a.Action == "" {
				t.Errorf("group %q has an empty action value", g.Name)
			}
			if seenAction[a.Action] {
				t.Errorf("group %q has a duplicate action %q", g.Name, a.Action)
			}
			seenAction[a.Action] = true

			member, ok := byName[a.Legacy]
			if !ok {
				t.Errorf("group %q action %q maps to unknown tool %q", g.Name, a.Action, a.Legacy)
				continue
			}
			if metaNames[a.Legacy] {
				t.Errorf("group %q action %q maps to a meta-tool name %q", g.Name, a.Action, a.Legacy)
			}
			if props, _ := member.InputSchema["properties"].(map[string]any); props != nil {
				if _, clash := props["action"]; clash {
					t.Errorf("grouped member %q already defines an \"action\" property — discriminator would shadow it", a.Legacy)
				}
			}
		}
	}
}

// TestBuildGroupedListShape verifies the grouped surface: 6 meta-tools emitted,
// each with the expected action enum, claimed members removed, and unclaimed
// tools (singletons, admin, steward) passed through.
func TestBuildGroupedListShape(t *testing.T) {
	available := allToolDefsForTest()
	grouped := buildGroupedList(available)

	byName := make(map[string]tool)
	for _, td := range grouped {
		byName[td.Name] = td
	}

	// Each spec becomes exactly one meta-tool with the full action enum.
	for _, g := range groupedSpecs {
		mt, ok := byName[g.Name]
		if !ok {
			t.Fatalf("meta-tool %q missing from grouped list", g.Name)
		}
		props, _ := mt.InputSchema["properties"].(map[string]any)
		actionProp, _ := props["action"].(map[string]any)
		enum, _ := actionProp["enum"].([]string)
		if len(enum) != len(g.Actions) {
			t.Errorf("meta-tool %q enum has %d actions, want %d", g.Name, len(enum), len(g.Actions))
		}
		req, _ := mt.InputSchema["required"].([]string)
		if len(req) != 1 || req[0] != "action" {
			t.Errorf("meta-tool %q required = %v, want [action]", g.Name, req)
		}
	}

	// Claimed legacy members must not appear as standalone tools anymore.
	for _, g := range groupedSpecs {
		for _, a := range g.Actions {
			if _, present := byName[a.Legacy]; present {
				t.Errorf("legacy tool %q still present after grouping (should be folded into %q)", a.Legacy, g.Name)
			}
		}
	}

	// Singletons pass through unchanged (never folded into a multi-member group).
	if _, present := byName["index_documents"]; !present {
		t.Error("passthrough singleton index_documents missing from grouped list")
	}
	if _, present := byName["project_bank_view"]; !present {
		t.Error("passthrough singleton project_bank_view missing from grouped list")
	}
	// Steward tools stay individual (steward_inbox_resolve uses a real `action`
	// arg that a group discriminator would shadow — must never be grouped).
	if _, present := byName["steward_inbox_resolve"]; !present {
		t.Error("steward_inbox_resolve must remain an individual passthrough tool")
	}
}

// TestBuildGroupedListDropsUnavailableGroup: when none of a group's members are
// available, the meta-tool is not emitted; a partially-available group keeps only
// the available actions.
func TestBuildGroupedListDropsUnavailableGroup(t *testing.T) {
	// Only repo_list and repo_read available — repo group should keep 2 actions;
	// no other group should appear.
	available := []tool{
		{Name: "repo_list", InputSchema: map[string]any{"properties": map[string]any{}}},
		{Name: "repo_read", InputSchema: map[string]any{"properties": map[string]any{}}},
		{Name: "index_documents", InputSchema: map[string]any{"properties": map[string]any{}}},
	}
	grouped := buildGroupedList(available)

	var repoMeta *tool
	for i := range grouped {
		switch grouped[i].Name {
		case "repo":
			repoMeta = &grouped[i]
		case "memory", "memory_admin", "engineering", "search", "session":
			t.Errorf("group %q emitted with no available members", grouped[i].Name)
		}
	}
	if repoMeta == nil {
		t.Fatal("repo meta-tool not emitted despite available members")
	}
	props, _ := repoMeta.InputSchema["properties"].(map[string]any)
	actionProp, _ := props["action"].(map[string]any)
	enum, _ := actionProp["enum"].([]string)
	if len(enum) != 2 {
		t.Errorf("repo enum = %v, want 2 available actions (list, read)", enum)
	}
}

// TestResolveGroupedToolCall covers the (meta, action) → legacy translation and
// its error paths.
func TestResolveGroupedToolCall(t *testing.T) {
	t.Run("valid action resolves and strips discriminator", func(t *testing.T) {
		args := map[string]any{"action": "store", "content": "x"}
		legacy, rErr, matched := resolveGroupedToolCall("memory", args)
		if !matched || rErr != nil {
			t.Fatalf("matched=%v rErr=%v, want matched=true rErr=nil", matched, rErr)
		}
		if legacy != "store_memory" {
			t.Errorf("legacy = %q, want store_memory", legacy)
		}
		if _, still := args["action"]; still {
			t.Error("action key not stripped from args")
		}
		if args["content"] != "x" {
			t.Error("non-action args must be preserved")
		}
	})

	t.Run("missing action errors", func(t *testing.T) {
		_, rErr, matched := resolveGroupedToolCall("memory", map[string]any{"content": "x"})
		if !matched || rErr == nil || rErr.Code != rpcErrInvalidParams {
			t.Fatalf("want invalid-params error, got matched=%v rErr=%+v", matched, rErr)
		}
	})

	t.Run("unknown action errors", func(t *testing.T) {
		_, rErr, matched := resolveGroupedToolCall("memory", map[string]any{"action": "nope"})
		if !matched || rErr == nil || rErr.Code != rpcErrInvalidParams {
			t.Fatalf("want invalid-params error, got matched=%v rErr=%+v", matched, rErr)
		}
	})

	t.Run("legacy name is not a meta-tool", func(t *testing.T) {
		_, _, matched := resolveGroupedToolCall("store_memory", map[string]any{})
		if matched {
			t.Error("legacy tool name must not match a group")
		}
	})
}

// TestGroupedDispatchEquivalence proves that a grouped call and its legacy
// equivalent hit the same handler and return identical results.
func TestGroupedDispatchEquivalence(t *testing.T) {
	s := newMemoryTestServer(t)

	call := func(name string, args map[string]any) (any, *rpcError) {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		return s.handleToolsCall(json.RawMessage(params))
	}

	// list_memories on an empty store, both call forms.
	legacyRes, legacyErr := call("list_memories", map[string]any{})
	groupedRes, groupedErr := call("memory", map[string]any{"action": "list"})

	if (legacyErr == nil) != (groupedErr == nil) {
		t.Fatalf("error mismatch: legacy=%+v grouped=%+v", legacyErr, groupedErr)
	}
	legacyJSON, _ := json.Marshal(legacyRes)
	groupedJSON, _ := json.Marshal(groupedRes)
	if !reflect.DeepEqual(legacyJSON, groupedJSON) {
		t.Fatalf("result mismatch:\n legacy=%s\ngrouped=%s", legacyJSON, groupedJSON)
	}

	// A grouped store must be retrievable via the legacy path — proves the write
	// went through the real store_memory handler.
	if _, rErr := call("memory", map[string]any{"action": "store", "content": "grouped-dispatch-probe", "type": "semantic"}); rErr != nil {
		t.Fatalf("grouped store failed: %+v", rErr)
	}
	listRes, rErr := call("list_memories", map[string]any{})
	if rErr != nil {
		t.Fatalf("legacy list after grouped store failed: %+v", rErr)
	}
	if listJSON, _ := json.Marshal(listRes); !strings.Contains(string(listJSON), "grouped-dispatch-probe") {
		t.Errorf("grouped-stored memory not found via legacy list: %s", listJSON)
	}
}

// TestGroupedListTokenSavings is the T67 acceptance bench. It measures the
// default initial payload — the core surface a typical agent loads at
// initialize (steward is opt-in and off by default) — and asserts the grouped
// form is at least 40% smaller. It also pins the "8 meta-tools" outcome: the 41
// default tools collapse to 6 groups + 2 singletons (index_documents,
// project_bank_view).
func TestGroupedListTokenSavings(t *testing.T) {
	available := mainToolDefs() // default surface: steward off
	grouped := buildGroupedList(available)

	flatJSON, _ := json.Marshal(map[string]any{"tools": available})
	groupedJSON, _ := json.Marshal(map[string]any{"tools": grouped})

	flat, groupedBytes := len(flatJSON), len(groupedJSON)
	saved := 1 - float64(groupedBytes)/float64(flat)
	t.Logf("default surface: %d tools -> %d, flat=%d bytes, grouped=%d bytes, saved=%.1f%%",
		len(available), len(grouped), flat, groupedBytes, saved*100)

	if saved < 0.40 {
		t.Errorf("token savings %.1f%% below 40%% target (flat=%d grouped=%d)", saved*100, flat, groupedBytes)
	}
	if len(grouped) != 8 {
		t.Errorf("default grouped surface = %d tools, want 8 (6 groups + 2 singletons)", len(grouped))
	}
}
