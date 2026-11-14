package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// incidentRecall is a read-only call that still carries knowledge, so a
// one-call session is enough to produce a summary (see T122 in the idle test).
func incidentRecall(t *testing.T, query, ctxLabel string) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name": "recall_similar_incidents",
		"arguments": map[string]any{
			"query":   query,
			"context": ctxLabel,
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return params
}

func sessionSummaries(t *testing.T, s *MCPServer, ctxLabel string) []*memory.Memory {
	t.Helper()
	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: ctxLabel}, 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []*memory.Memory
	for _, m := range memories {
		if memory.IsSessionSummaryMemory(m) {
			out = append(out, m)
		}
	}
	return out
}

// T187: two clients of one HTTP server interleave calls inside one idle
// window. Before the fix both landed in a single session under the first
// context seen, so one summary carried the other client's queries.
func TestSessionTrackerKeepsClientsApart(t *testing.T) {
	s := newAutoSessionTestServer(t, 30*time.Millisecond, 0, 1)

	calls := []struct {
		client, query, ctx string
	}{
		{"http:a", "latency spike on the payments api", "alpha"},
		{"http:b", "lease renewal for the warehouse contract", "beta"},
		{"http:a", "payments api error rate after deploy", "alpha"},
		{"http:b", "warehouse contract signature deadline", "beta"},
	}
	for _, c := range calls {
		if _, rErr := s.handleToolsCall(incidentRecall(t, c.query, c.ctx), c.client); rErr != nil {
			t.Fatalf("handleToolsCall: %+v", rErr)
		}
	}
	s.sessionTracker.waitForBackground()

	for _, tc := range []struct {
		ctx      string
		own      string
		foreign  string
		wantSize int
	}{
		{"alpha", "payments api", "warehouse", 1},
		{"beta", "warehouse", "payments", 1},
	} {
		summaries := sessionSummaries(t, s, tc.ctx)
		if len(summaries) != tc.wantSize {
			t.Fatalf("context %s: %d summaries, want %d", tc.ctx, len(summaries), tc.wantSize)
		}
		content := summaries[0].Content
		if !strings.Contains(content, tc.own) {
			t.Errorf("context %s: summary lacks its own calls: %q", tc.ctx, content)
		}
		if strings.Contains(content, tc.foreign) {
			t.Errorf("context %s: summary carries the other client's calls: %q", tc.ctx, content)
		}
	}
}

// T187: an explicit change of context closes the session instead of
// summarising the new project's calls under the first one.
func TestSessionTrackerSplitsOnContextSwitch(t *testing.T) {
	s := newAutoSessionTestServer(t, 30*time.Millisecond, 0, 1)

	for _, c := range []struct{ query, ctx string }{
		{"latency spike on the payments api", "alpha"},
		{"lease renewal for the warehouse contract", "beta"},
	} {
		if _, rErr := s.handleToolsCall(incidentRecall(t, c.query, c.ctx), stdioClientKey); rErr != nil {
			t.Fatalf("handleToolsCall: %+v", rErr)
		}
	}
	s.sessionTracker.waitForBackground()

	alpha := sessionSummaries(t, s, "alpha")
	if len(alpha) != 1 || alpha[0].Metadata[memory.MetadataSessionBoundary] != "context_switch" {
		t.Fatalf("alpha summaries = %d, want 1 closed by context_switch", len(alpha))
	}
	if strings.Contains(alpha[0].Content, "warehouse") {
		t.Errorf("alpha summary carries the beta call: %q", alpha[0].Content)
	}
	if beta := sessionSummaries(t, s, "beta"); len(beta) != 1 {
		t.Fatalf("beta summaries = %d, want 1", len(beta))
	}
}

func postMCPRequest(t *testing.T, mux http.Handler, sessionID string, req rpcRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		r.Header.Set(mcpSessionIDHeader, sessionID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s: status %d", req.Method, rec.Code)
	}
	return rec
}

// T187: initialize hands out an Mcp-Session-Id; calls that echo it are tracked,
// calls without it are not — a lost summary beats a merged one.
func TestHTTPSessionIDKeysTracking(t *testing.T) {
	s := newAutoSessionTestServer(t, 30*time.Millisecond, 0, 1)
	mux := buildMux(s)

	rec := postMCPRequest(t, mux, "", rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"})
	sessionID := rec.Header().Get(mcpSessionIDHeader)
	if !validMCPSessionID(sessionID) {
		t.Fatalf("initialize returned session id %q", sessionID)
	}

	postMCPRequest(t, mux, "", rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call",
		Params: incidentRecall(t, "anonymous caller query", "anon")})
	postMCPRequest(t, mux, sessionID, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call",
		Params: incidentRecall(t, "latency spike on the payments api", "keyed")})
	s.sessionTracker.waitForBackground()

	if got := sessionSummaries(t, s, "anon"); len(got) != 0 {
		t.Fatalf("call without Mcp-Session-Id produced %d summaries, want 0", len(got))
	}
	if got := sessionSummaries(t, s, "keyed"); len(got) != 1 {
		t.Fatalf("call with Mcp-Session-Id produced %d summaries, want 1", len(got))
	}
}

func TestValidMCPSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"":                       false,
		"abc123":                 true,
		"has space":              false,
		"tab\tinside":            false,
		strings.Repeat("x", 129): false,
		"0f9e-ABCD_~!":           true,
		"кирил":                  false,
	} {
		if got := validMCPSessionID(id); got != want {
			t.Errorf("validMCPSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}
