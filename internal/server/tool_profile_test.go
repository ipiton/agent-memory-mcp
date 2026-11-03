package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func callTool(t *testing.T, s *MCPServer, name string, args map[string]any) *rpcError {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, rErr := s.handleToolsCall(params)
	return rErr
}

func listedTools(t *testing.T, s *MCPServer) map[string]bool {
	t.Helper()
	result, rErr := s.handleToolsList(nil)
	if rErr != nil {
		t.Fatalf("tools/list: %v", rErr)
	}
	listed := map[string]bool{}
	for _, tool := range result.(map[string]any)["tools"].([]tool) {
		listed[tool.Name] = true
	}
	return listed
}

func isProfileRefusal(rErr *rpcError) bool {
	return rErr != nil && strings.Contains(rErr.Message, "profile")
}

func TestCoreProfileRefusesOnTheServer(t *testing.T) {
	s := newTestServer(t, "")
	s.config.ToolProfile = config.ToolProfileCore

	if listed := listedTools(t, s); listed["repo_read"] {
		t.Error("repo_read is listed in the core profile")
	}
	if rErr := callTool(t, s, "repo_read", map[string]any{"path": "x"}); !isProfileRefusal(rErr) {
		t.Errorf("direct call: %+v, want a profile refusal", rErr)
	}
	if rErr := callTool(t, s, "memory_stats", nil); isProfileRefusal(rErr) {
		t.Errorf("core tool refused: %+v", rErr)
	}
}

func TestCoreProfileCoversGroupedCalls(t *testing.T) {
	s := newTestServer(t, "")
	s.config.ToolProfile = config.ToolProfileCore

	if rErr := callTool(t, s, "session", map[string]any{"action": "sweep_archive"}); !isProfileRefusal(rErr) {
		t.Errorf("grouped call past the profile: %+v", rErr)
	}
	if rErr := callTool(t, s, "session", map[string]any{"action": "close"}); isProfileRefusal(rErr) {
		t.Errorf("grouped call to a core tool refused: %+v", rErr)
	}
}

func TestFullProfileIsUnrestricted(t *testing.T) {
	s := newTestServer(t, "")
	s.config.ToolProfile = config.ToolProfileFull

	if !listedTools(t, s)["repo_read"] {
		t.Error("repo_read not listed in the full profile")
	}
	if rErr := callTool(t, s, "repo_read", map[string]any{"path": "x"}); isProfileRefusal(rErr) {
		t.Errorf("refused in the full profile: %+v", rErr)
	}
}

func TestCoreToolsExist(t *testing.T) {
	s := newTestServer(t, "")
	defined := map[string]bool{}
	for _, tool := range mainToolDefs() {
		defined[tool.Name] = true
	}
	for name := range coreTools {
		if !defined[name] {
			t.Errorf("core tool %s has no definition", name)
		}
		if _, ok := s.toolHandlers[name]; !ok {
			t.Errorf("core tool %s has no handler", name)
		}
	}
}
