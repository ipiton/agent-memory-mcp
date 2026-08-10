package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
)

// newLoggingTestServer builds a server with file logging enabled and returns it
// together with the log path. newTestServer leaves LogPath empty, which makes
// fileLogger nil — any assertion on log output built on that helper would pass
// without executing the logging path at all.
func newLoggingTestServer(t *testing.T) (*MCPServer, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "mcp.log")
	cfg := config.Config{
		RootPath:   t.TempDir(),
		OutputMode: "line",
		LogPath:    logPath,
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	s := New(cfg, guard)
	if s.fileLogger == nil {
		t.Fatal("fileLogger is nil — the logging path would not be exercised")
	}
	return s, logPath
}

func readLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(data)
}

// TestInitializeLogsClientProtocolVersion is the acceptance check for step 1 of
// MCP-PROTOCOL-MIGRATION-2026-07-28: a client asking for a revision we do not
// implement must be visible in telemetry rather than discovered through a
// failure. The server still answers with its own version — this is observation
// only, not negotiation.
func TestInitializeLogsClientProtocolVersion(t *testing.T) {
	s, logPath := newLoggingTestServer(t)

	params := json.RawMessage(`{"protocolVersion":"2026-07-28",` +
		`"clientInfo":{"name":"claude-code","version":"9.9.9"}}`)

	result, rpcErr := s.handleInitialize(params)
	if rpcErr != nil {
		t.Fatalf("handleInitialize returned error: %+v", rpcErr)
	}

	// The response must be unchanged: we report our own revision, not the one
	// the client asked for.
	answered := result.(map[string]any)["protocolVersion"]
	if answered != protocolVersion {
		t.Errorf("response protocolVersion = %v, want %q", answered, protocolVersion)
	}

	logged := readLog(t, logPath)
	for _, want := range []string{
		`"client_protocol_version":"2026-07-28"`,
		`"server_protocol_version":"` + protocolVersion + `"`,
		`"protocol_version_match":false`,
		`"client_name":"claude-code"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log does not contain %s\nlog:\n%s", want, logged)
		}
	}
}

// TestInitializeLogsMatchingProtocolVersion pins the other branch of the match
// flag, so a detector that always reported "false" would not pass.
func TestInitializeLogsMatchingProtocolVersion(t *testing.T) {
	s, logPath := newLoggingTestServer(t)

	params := json.RawMessage(`{"protocolVersion":"` + protocolVersion + `"}`)
	if _, rpcErr := s.handleInitialize(params); rpcErr != nil {
		t.Fatalf("handleInitialize returned error: %+v", rpcErr)
	}

	if logged := readLog(t, logPath); !strings.Contains(logged, `"protocol_version_match":true`) {
		t.Errorf("expected protocol_version_match:true\nlog:\n%s", logged)
	}
}

// TestInitializeSurvivesUnusableParams guards the failure mode that matters:
// logging is diagnostics, and it must never turn a working handshake into a
// broken one. Both a malformed body and an absent one must still initialize.
func TestInitializeSurvivesUnusableParams(t *testing.T) {
	cases := map[string]json.RawMessage{
		"malformed": json.RawMessage(`{"protocolVersion":`),
		"absent":    nil,
		"empty":     json.RawMessage(`{}`),
	}

	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newLoggingTestServer(t)

			result, rpcErr := s.handleInitialize(params)
			if rpcErr != nil {
				t.Fatalf("handleInitialize returned error: %+v", rpcErr)
			}
			if result.(map[string]any)["protocolVersion"] != protocolVersion {
				t.Error("response protocolVersion changed on unusable params")
			}
		})
	}
}
