package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T188: a session of a client that is not a coding agent (a claude.ai chat) was
// summarised in coding mode, and the mode fallback turned its store_memory
// lines into engineering cards for hard_review. The client is known by the
// clientInfo.name it sent in initialize.
func TestChatClientSessionIsNotCodingMode(t *testing.T) {
	for _, tc := range []struct {
		clientName   string
		ctx          string
		wantMode     memory.SessionMode
		wantFallback bool
	}{
		{"some-chat-app", "chat-ctx", memory.SessionModeChat, false},
		// The coding agent keeps the old behaviour: it is what proves the
		// stored line does reach the coding fallback.
		{"claude-code", "code-ctx", memory.SessionModeCoding, true},
	} {
		t.Run(tc.clientName, func(t *testing.T) {
			s := newAutoSessionTestServer(t, 30*time.Millisecond, 0, 1)
			mux := buildMux(s)

			initParams, _ := json.Marshal(map[string]any{
				"protocolVersion": protocolVersion,
				"clientInfo":      map[string]any{"name": tc.clientName, "version": "1.0"},
			})
			rec := postMCPRequest(t, mux, "", rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: initParams})
			sessionID := rec.Header().Get(mcpSessionIDHeader)

			// No keyword match, but "service" and "helm" are engineering
			// signals, so in coding mode the line falls back to a caveat.
			callParams, _ := json.Marshal(map[string]any{
				"name": "store_memory",
				"arguments": map[string]any{
					"content": "The payments service helm chart values now live in the shared repo",
					"title":   "Payments service helm chart values now live in the shared repo",
					"context": tc.ctx,
				},
			})
			postMCPRequest(t, mux, sessionID, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call", Params: callParams})
			s.sessionTracker.waitForBackground()

			summaries := sessionSummaries(t, s, tc.ctx)
			if len(summaries) != 1 {
				t.Fatalf("%d session summaries, want 1", len(summaries))
			}
			if got := summaries[0].Metadata[memory.MetadataSessionMode]; got != string(tc.wantMode) {
				t.Errorf("session mode = %q, want %q", got, tc.wantMode)
			}

			all, err := s.memoryStore.List(context.Background(), memory.Filters{Context: tc.ctx}, 50)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			fallback := false
			for _, m := range all {
				if memory.IsReviewQueueMemory(m) && strings.Contains(m.Content, "session_mode_fallback:coding") {
					fallback = true
				}
			}
			if fallback != tc.wantFallback {
				t.Errorf("review card with session_mode_fallback:coding = %v, want %v", fallback, tc.wantFallback)
			}
		})
	}
}
