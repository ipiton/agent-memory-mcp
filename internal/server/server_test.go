package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T, authToken string) *MCPServer {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		RootPath:      root,
		HTTPHost:      "127.0.0.1",
		HTTPAuthToken: authToken,
		OutputMode:    "line",
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return New(cfg, guard)
}

func buildMux(s *MCPServer) http.Handler {
	return buildHTTPMux(s)
}

func newMemoryTestServer(t *testing.T) *MCPServer {
	t.Helper()
	s := newTestServer(t, "")
	store, err := memory.NewStore(filepath.Join(t.TempDir(), "memory.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.memoryStore = store
	return s
}

func newAutoSessionTestServer(t *testing.T, idleTimeout time.Duration, checkpointInterval time.Duration, minEvents int) *MCPServer {
	t.Helper()
	s := newMemoryTestServer(t)
	s.config.SessionTrackingEnabled = true
	s.config.SessionIdleTimeout = idleTimeout
	s.config.SessionCheckpointInterval = checkpointInterval
	s.config.SessionMinEvents = minEvents
	s.sessionTracker = newSessionTracker(s.config, s.memoryStore, nil)
	return s
}

func TestHTTPAuthRequired(t *testing.T) {
	s := newTestServer(t, "secret-token-123")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	// No auth header → 401
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Wrong token → 401
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", rec.Code)
	}

	// Correct token → 200
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-123")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", rec.Code)
	}
}

func TestHTTPNoAuthConfigured(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth config, got %d", rec.Code)
	}
}

func TestValidateHTTPExposureAllowsLoopbackWithoutAuth(t *testing.T) {
	cfg := config.Config{
		HTTPMode: "http",
		HTTPHost: "127.0.0.1",
		HTTPPort: 18080,
	}

	if err := validateHTTPExposure(cfg); err != nil {
		t.Fatalf("validateHTTPExposure returned error: %v", err)
	}
}

func TestValidateHTTPExposureRejectsNonLoopbackWithoutAuth(t *testing.T) {
	cfg := config.Config{
		HTTPMode: "http",
		HTTPHost: "0.0.0.0",
		HTTPPort: 18080,
	}

	err := validateHTTPExposure(cfg)
	if err == nil {
		t.Fatal("expected validation error for non-loopback host without auth")
	}
	if !strings.Contains(err.Error(), "MCP_HTTP_AUTH_TOKEN") {
		t.Fatalf("error = %q, want MCP_HTTP_AUTH_TOKEN hint", err.Error())
	}
}

func TestValidateHTTPExposureAllowsExplicitInsecureOverride(t *testing.T) {
	cfg := config.Config{
		HTTPMode:                         "http",
		HTTPHost:                         "0.0.0.0",
		HTTPPort:                         18080,
		HTTPInsecureAllowUnauthenticated: true,
	}

	if err := validateHTTPExposure(cfg); err != nil {
		t.Fatalf("validateHTTPExposure returned error: %v", err)
	}
}

func TestHTTPMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPOptions(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", rec.Code)
	}

	// CORS header should deny by default
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "" {
		t.Fatalf("expected empty CORS origin, got %q", origin)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := buildMux(s)

	// Health should be accessible without auth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", result["status"])
	}
}

func TestConsolePageServed(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, expected := range []string{"Retrieval Console", "/console/api/query", "Compare normal vs debug mode"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("console page missing %q", expected)
		}
	}
}

func TestConsoleAPIRequiresAuth(t *testing.T) {
	s := newTestServer(t, "secret-token-123")
	mux := buildMux(s)

	body, _ := json.Marshal(map[string]any{
		"mode":  "documents",
		"query": "ingress rollback",
	})
	req := httptest.NewRequest(http.MethodPost, "/console/api/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestConsoleAPIMemoryQuery(t *testing.T) {
	s := newMemoryTestServer(t)
	mux := buildMux(s)

	mem := &memory.Memory{
		Title:      "Rollback api",
		Content:    "rollback api deployment and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata:   map[string]string{"entity": "runbook", "service": "api"},
	}
	if err := s.memoryStore.Store(context.Background(),mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"mode":        "memory",
		"query":       "rollback api deployment",
		"context":     "payments",
		"service":     "api",
		"memory_type": "procedural",
		"limit":       5,
	})
	req := httptest.NewRequest(http.MethodPost, "/console/api/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["mode"] != "memory" {
		t.Fatalf("mode = %v, want memory", result["mode"])
	}
	if int(result["result_count"].(float64)) != 1 {
		t.Fatalf("result_count = %v, want 1", result["result_count"])
	}
}

func TestDispatchTableCompleteness(t *testing.T) {
	s := newTestServer(t, "")
	handlers := s.toolHandlers

	expectedTools := []string{
		"repo_list",
		"repo_read",
		"repo_search",
		"semantic_search",
		"index_documents",
		"store_memory",
		"recall_memory",
		"update_memory",
		"delete_memory",
		"list_memories",
		"memory_stats",
		"merge_duplicates",
		"mark_outdated",
		"promote_to_canonical",
		"conflicts_report",
		"list_canonical_knowledge",
		"recall_canonical_knowledge",
		"close_session",
		"analyze_session",
		"review_session_changes",
		"accept_session_changes",
		"resolve_review_item",
		"resolve_review_queue",
		"store_decision",
		"store_incident",
		"store_runbook",
		"store_postmortem",
		"search_runbooks",
		"recall_similar_incidents",
		"summarize_project_context",
		"project_bank_view",
	}

	for _, tool := range expectedTools {
		if _, ok := handlers[tool]; !ok {
			t.Errorf("missing handler for tool %q", tool)
		}
	}

	if len(handlers) != len(expectedTools) {
		t.Errorf("handler count mismatch: got %d, want %d", len(handlers), len(expectedTools))
	}
}

func TestHandleUnknownTool(t *testing.T) {
	s := newTestServer(t, "")

	params, _ := json.Marshal(map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})

	result, rErr := s.handleToolsCall(json.RawMessage(params))
	if rErr == nil {
		t.Fatal("expected error for unknown tool")
	}
	if rErr.Code != -32601 {
		t.Fatalf("expected -32601, got %d", rErr.Code)
	}
	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestReadMessageSizeLimit(t *testing.T) {
	// Test that readMessage rejects oversized content-length
	input := "Content-Length: 999999999\r\n\r\n"
	reader := bufio.NewReader(bytes.NewBufferString(input))

	_, _, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for oversized content-length")
	}
}

func TestFormatSearchResultsIncludesDebug(t *testing.T) {
	s := newTestServer(t, "")

	formatted := s.formatSearchResults(&rag.SearchResponse{
		Query: "ingress rollback",
		Results: []rag.SearchResult{
			{
				Title:      "Ingress rollback",
				Path:       "runbooks/ingress-rollback.md",
				SourceType: "runbook",
				Score:      0.91,
				Snippet:    "Rollback steps",
				Trust: &trust.Metadata{
					KnowledgeLayer: "document",
					SourceType:     "runbook",
					Confidence:     0.94,
					FreshnessScore: 0.80,
					Owner:          "operations",
					LastVerifiedAt: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC),
				},
				Debug: &rag.ResultDebug{
					Breakdown: rag.ScoreBreakdown{
						Semantic:          0.62,
						KeywordRaw:        2.10,
						KeywordNormalized: 1.00,
						RecencyBoost:      0.04,
						SourceBoost:       0.08,
						ConfidenceBoost:   0.02,
						FinalScore:        0.91,
					},
					AppliedBoosts: []string{"semantic_similarity", "keyword_match", "source_type:runbook(+0.080)", "trust_confidence(+0.020)"},
				},
			},
		},
		SearchTime: 12,
		Debug: &rag.SearchDebug{
			AppliedFilters:   []string{"source_type=runbook"},
			RankingSignals:   []string{"semantic_similarity", "keyword_bm25_like", "recency_boost", "trust_confidence", "freshness_score"},
			IndexedChunks:    10,
			FilteredOut:      4,
			DiscardedAsNoise: 2,
			CandidateCount:   4,
			ReturnedCount:    1,
		},
	})

	for _, expected := range []string{
		"Applied filters: source_type=runbook",
		"Ranking signals: semantic_similarity, keyword_bm25_like, recency_boost, trust_confidence, freshness_score",
		"Trust: layer=document source=runbook confidence=0.94 freshness=0.80 owner=operations verified=2026-03-01T12:00:00Z",
		"Score breakdown: semantic=0.620",
		"confidence=0.020",
		"Applied boosts: semantic_similarity, keyword_match, source_type:runbook(+0.080), trust_confidence(+0.020)",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("formatted output missing %q:\n%s", expected, formatted)
		}
	}
}

func TestCallStoreDecisionStoresWorkflowMemory(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callStoreDecision(map[string]any{
		"decision":   "Disable HPA for api during migration",
		"rationale":  "Pods were thrashing during rollout",
		"service":    "api",
		"status":     "accepted",
		"context":    "payments",
		"tags":       []any{"migration"},
		"importance": 0.9,
	})
	if rErr != nil {
		t.Fatalf("callStoreDecision returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "Decision stored:") {
		t.Fatalf("unexpected result text: %s", toolRes.Content[0].Text)
	}

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}

	mem := memories[0]
	if mem.Type != memory.TypeSemantic {
		t.Fatalf("mem.Type = %s, want %s", mem.Type, memory.TypeSemantic)
	}
	if mem.Metadata["last_verified_at"] == "" {
		t.Fatalf("expected last_verified_at metadata, got %#v", mem.Metadata)
	}
	for _, tag := range []string{"decision", "service:api", "status:accepted", "migration"} {
		if !containsTag(mem.Tags, tag) {
			t.Fatalf("missing tag %q in %v", tag, mem.Tags)
		}
	}
	if mem.Metadata["entity"] != "decision" {
		t.Fatalf("metadata entity = %q, want decision", mem.Metadata["entity"])
	}
	if !strings.Contains(mem.Content, "Rationale: Pods were thrashing during rollout") {
		t.Fatalf("unexpected content: %s", mem.Content)
	}
}

func TestCallAnalyzeSessionDryRunReturnsReviewAwareReport(t *testing.T) {
	s := newMemoryTestServer(t)

	existing := &memory.Memory{
		Title:      "Rollback api deployment",
		Content:    "Runbook: Rollback api deployment with helm rollback and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.85,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeRunbook),
			memory.MetadataService: "api",
		},
	}
	if err := s.memoryStore.Store(context.Background(),existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callAnalyzeSession(map[string]any{
		"summary": "Runbook: Rollback api deployment with helm rollback and verify health.\nDecision: Disable HPA for api during migration accepted.",
		"context": "payments",
		"service": "api",
		"mode":    "migration",
		"dry_run": true,
	})
	if rErr != nil {
		t.Fatalf("callAnalyzeSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (dry-run)",
		"Planned actions:",
		"Handling:",
		"safe_auto_apply",
		"hard_review",
		"RAW_ONLY",
		"MERGE",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 1 {
		t.Fatalf("dry-run should not persist raw summary, count = %d", s.memoryStore.Count())
	}
}

func TestCallAnalyzeSessionSaveRawPersistsSessionSummary(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callAnalyzeSession(map[string]any{
		"summary":    "Decision: Disable HPA for api during migration accepted.",
		"context":    "payments",
		"service":    "api",
		"mode":       "migration",
		"save_raw":   true,
		"format":     "json",
		"started_at": "2026-03-06T10:00:00Z",
		"ended_at":   "2026-03-06T10:30:00Z",
		"metadata": map[string]any{
			memory.MetadataRecordKind: "override-me",
			"owner":                   "platform",
		},
	})
	if rErr != nil {
		t.Fatalf("callAnalyzeSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "\"raw_summary_saved\"") {
		t.Fatalf("expected json output with raw_summary_saved, got:\n%s", toolRes.Content[0].Text)
	}

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	mem := memories[0]
	if !memory.IsSessionSummaryMemory(mem) {
		t.Fatalf("expected session summary memory, got metadata %#v", mem.Metadata)
	}
	if mem.Metadata[memory.MetadataRecordKind] != memory.RecordKindSessionSummary {
		t.Fatalf("record_kind = %q, want %q", mem.Metadata[memory.MetadataRecordKind], memory.RecordKindSessionSummary)
	}
	if mem.Metadata["owner"] != "platform" {
		t.Fatalf("owner metadata = %q, want platform", mem.Metadata["owner"])
	}
	if mem.Metadata[memory.MetadataSessionMode] != string(memory.SessionModeMigration) {
		t.Fatalf("session_mode = %q, want %q", mem.Metadata[memory.MetadataSessionMode], memory.SessionModeMigration)
	}
}

func TestCallAnalyzeSessionAutoApplyLowRiskReturnsAppliedState(t *testing.T) {
	s := newMemoryTestServer(t)

	existing := &memory.Memory{
		Title:      "Disable HPA for api during rollout accepted",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
		},
	}
	if err := s.memoryStore.Store(context.Background(),existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callAnalyzeSession(map[string]any{
		"summary":             "Decision: Disable HPA for api during rollout accepted.",
		"context":             "payments",
		"service":             "api",
		"mode":                "coding",
		"save_raw":            true,
		"auto_apply_low_risk": true,
	})
	if rErr != nil {
		t.Fatalf("callAnalyzeSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (write-enabled)",
		"Execution state:",
		"applied: 2",
		"UPDATE",
		"State: applied",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
}

func TestCallReviewSessionChangesForcesDryRunAndHighlightsNextActions(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callReviewSessionChanges(map[string]any{
		"summary":  "Decision: Disable HPA for api during migration accepted.",
		"context":  "payments",
		"service":  "api",
		"save_raw": true,
	})
	if rErr != nil {
		t.Fatalf("callReviewSessionChanges returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (dry-run)",
		"Next actions:",
		"review_changes via review_session_changes",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("review output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 0 {
		t.Fatalf("review should not persist memories, count = %d", s.memoryStore.Count())
	}
}

func TestCallAcceptSessionChangesUsesWriteEnabledDefaults(t *testing.T) {
	s := newMemoryTestServer(t)

	existing := &memory.Memory{
		Title:      "Disable HPA for api during rollout accepted",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
		},
	}
	if err := s.memoryStore.Store(context.Background(),existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callAcceptSessionChanges(map[string]any{
		"summary": "Decision: Disable HPA for api during rollout accepted.",
		"context": "payments",
		"service": "api",
		"mode":    "coding",
	})
	if rErr != nil {
		t.Fatalf("callAcceptSessionChanges returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (write-enabled)",
		"Raw summary saved as memory",
		"State: applied",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("accept output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 2 {
		t.Fatalf("expected updated memory + raw summary, count = %d", s.memoryStore.Count())
	}
}

func TestCallAnalyzeSessionInfersMigrationModeFromSummary(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callAnalyzeSession(map[string]any{
		"summary": "Completed schema backfill and cutover for payments-db after dual-write verification.",
		"context": "payments",
		"service": "payments-db",
	})
	if rErr != nil {
		t.Fatalf("callAnalyzeSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Mode: migration",
		"migration_mode_priority",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
}

func TestCallStoreMemoryRejectsInvalidType(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreMemory(map[string]any{
		"content": "remember this",
		"type":    "broken",
	})
	if rErr == nil {
		t.Fatal("expected invalid type error")
	}
	if rErr.Code != -32602 {
		t.Fatalf("rErr.Code = %d, want -32602", rErr.Code)
	}
	if !strings.Contains(rErr.Message, "invalid memory type") {
		t.Fatalf("rErr.Message = %q, want invalid memory type", rErr.Message)
	}
}

func TestCallStoreMemoryNormalizesTagsFromString(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreMemory(map[string]any{
		"content": "remember this",
		"tags":    " api,incident,api , rollback ",
	})
	if rErr != nil {
		t.Fatalf("callStoreMemory error = %v", rErr)
	}

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	got := memories[0].Tags
	want := []string{"api", "incident", "rollback"}
	if len(got) != len(want) {
		t.Fatalf("len(tags) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tags[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestFormatSearchResultsOmitsZeroVerifiedTimestamp(t *testing.T) {
	s := newTestServer(t, "")

	formatted := s.formatSearchResults(&rag.SearchResponse{
		Query: "runbook",
		Results: []rag.SearchResult{
			{
				Title:   "Runbook",
				Path:    "runbooks/api.md",
				Score:   0.8,
				Snippet: "Steps",
				Trust: &trust.Metadata{
					KnowledgeLayer: "document",
					SourceType:     "runbook",
					Confidence:     0.8,
					FreshnessScore: 0.7,
				},
			},
		},
	})

	if strings.Contains(formatted, "verified=0001-01-01T00:00:00Z") {
		t.Fatalf("formatted output unexpectedly contains zero verified timestamp:\n%s", formatted)
	}
}

func TestSummarizeProjectContextIncludesWorkflowSections(t *testing.T) {
	s := newMemoryTestServer(t)

	for _, mem := range []*memory.Memory{
		{
			Title:      "Disable HPA",
			Content:    "Decision: Disable HPA\nRationale: rollout stability",
			Type:       memory.TypeSemantic,
			Context:    "payments",
			Importance: 0.8,
			Tags:       []string{"decision", "service:api"},
		},
		{
			Title:      "API rollback",
			Content:    "Procedure: Roll back the deployment",
			Type:       memory.TypeProcedural,
			Context:    "payments",
			Importance: 0.8,
			Tags:       []string{"runbook", "service:api"},
		},
		{
			Title:      "Ingress outage",
			Content:    "Incident: ingress outage\nResolution: restart controller",
			Type:       memory.TypeEpisodic,
			Context:    "payments",
			Importance: 0.9,
			Tags:       []string{"incident", "service:api"},
		},
	} {
		if err := s.memoryStore.Store(context.Background(),mem); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	canonicalID := ""
	for _, mem := range memories {
		if mem.Title == "Disable HPA" {
			canonicalID = mem.ID
			break
		}
	}
	if canonicalID == "" {
		t.Fatal("failed to find decision memory to promote")
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(),canonicalID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	result, rErr := s.callSummarizeProjectContext(map[string]any{
		"context": "payments",
		"service": "api",
		"limit":   5,
	})
	if rErr != nil {
		t.Fatalf("callSummarizeProjectContext returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Project context summary",
		"Canonical knowledge",
		"Recent decisions",
		"Runbooks",
		"Incidents and postmortems",
		"Disable HPA",
		"API rollback",
		"Ingress outage",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
}

func TestCallProjectBankViewOverviewShowsCanonicalSessionAndAttentionSections(t *testing.T) {
	s := newMemoryTestServer(t)

	decision := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.85,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
			memory.MetadataStatus:  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(),decision); err != nil {
		t.Fatalf("Store decision: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(),decision.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	session := &memory.Memory{
		Title:      "Session close / payments / api",
		Content:    "Migration summary for payments api rollout.",
		Type:       memory.TypeEpisodic,
		Context:    "payments",
		Importance: 0.20,
		Tags:       []string{"session-summary", "service:api"},
		Metadata: map[string]string{
			memory.MetadataRecordKind:  memory.RecordKindSessionSummary,
			memory.MetadataService:     "api",
			memory.MetadataSessionMode: string(memory.SessionModeMigration),
		},
	}
	if err := s.memoryStore.Store(context.Background(),session); err != nil {
		t.Fatalf("Store session summary: %v", err)
	}

	stale := &memory.Memory{
		Title:      "Known rollout caveat",
		Content:    "Caveat: verification is still manual after rollout.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.60,
		Tags:       []string{"caveat", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:          string(memory.EngineeringTypeCaveat),
			memory.MetadataService:         "api",
			memory.MetadataLifecycleStatus: string(memory.LifecycleOutdated),
			memory.MetadataReviewRequired:  "true",
		},
	}
	if err := s.memoryStore.Store(context.Background(),stale); err != nil {
		t.Fatalf("Store stale caveat: %v", err)
	}

	result, rErr := s.callProjectBankView(map[string]any{
		"view":    "overview",
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callProjectBankView returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Project bank view: Canonical overview",
		"Canonical knowledge (1):",
		"Recent session deltas (1):",
		"Needs review or refresh (1):",
		"Session mode: migration",
		"Layer: canonical",
		"Review: required",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("project bank overview missing %q:\n%s", expected, text)
		}
	}
}

func TestCallProjectBankViewJSONAppliesStatusOwnerAndServiceFilters(t *testing.T) {
	s := newMemoryTestServer(t)

	canonicalDecision := &memory.Memory{
		Title:      "Use maintenance window",
		Content:    "Decision: Use a maintenance window for payments-db cutover accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.80,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "payments-db",
			memory.MetadataStatus:  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(),canonicalDecision); err != nil {
		t.Fatalf("Store canonical decision: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(),canonicalDecision.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	rawDecision := &memory.Memory{
		Title:      "Temporary rollout note",
		Content:    "Decision: Disable background workers during cutover.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "payments-db",
			memory.MetadataStatus:  "draft",
		},
	}
	if err := s.memoryStore.Store(context.Background(),rawDecision); err != nil {
		t.Fatalf("Store raw decision: %v", err)
	}

	result, rErr := s.callProjectBankView(map[string]any{
		"view":    "decisions",
		"context": "payments",
		"service": "payments-db",
		"status":  "canonical",
		"owner":   "platform",
		"format":  "json",
	})
	if rErr != nil {
		t.Fatalf("callProjectBankView returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	var payload memory.ProjectBankViewResult
	if err := json.Unmarshal([]byte(toolRes.Content[0].Text), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload.View != memory.ProjectBankViewDecisions {
		t.Fatalf("view = %q, want %q", payload.View, memory.ProjectBankViewDecisions)
	}
	if len(payload.Sections) != 1 || len(payload.Sections[0].Items) != 1 {
		t.Fatalf("unexpected sections payload: %#v", payload.Sections)
	}
	item := payload.Sections[0].Items[0]
	if item.Title != canonicalDecision.Title {
		t.Fatalf("title = %q, want %q", item.Title, canonicalDecision.Title)
	}
	if item.Owner != "platform" {
		t.Fatalf("owner = %q, want platform", item.Owner)
	}
	if item.Lifecycle != memory.LifecycleCanonical {
		t.Fatalf("lifecycle = %q, want %q", item.Lifecycle, memory.LifecycleCanonical)
	}
}

func TestBackgroundSessionTrackerFlushesOnIdle(t *testing.T) {
	s := newAutoSessionTestServer(t, 20*time.Millisecond, 0, 1)

	params, _ := json.Marshal(map[string]any{
		"name": "project_bank_view",
		"arguments": map[string]any{
			"view":    "canonical_overview",
			"context": "payments",
			"service": "api",
		},
	})
	if _, rErr := s.handleToolsCall(params); rErr != nil {
		t.Fatalf("handleToolsCall returned error: %+v", rErr)
	}

	time.Sleep(60 * time.Millisecond)

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	if !memory.IsSessionSummaryMemory(memories[0]) {
		t.Fatalf("expected session summary memory, got %#v", memories[0].Metadata)
	}
	if memories[0].Metadata[memory.MetadataSessionBoundary] != "idle_timeout" {
		t.Fatalf("session_boundary = %q, want idle_timeout", memories[0].Metadata[memory.MetadataSessionBoundary])
	}
	if memories[0].Metadata[memory.MetadataSessionOrigin] != autoSessionOrigin {
		t.Fatalf("session_origin = %q, want %q", memories[0].Metadata[memory.MetadataSessionOrigin], autoSessionOrigin)
	}
}

func TestBackgroundSessionTrackerCreatesReviewQueueItems(t *testing.T) {
	s := newAutoSessionTestServer(t, 20*time.Millisecond, 0, 1)

	params, _ := json.Marshal(map[string]any{
		"name": "store_incident",
		"arguments": map[string]any{
			"summary": "Latency spike mitigated with temporary fix, verify later.",
			"context": "payments",
			"service": "api",
		},
	})
	if _, rErr := s.handleToolsCall(params); rErr != nil {
		t.Fatalf("handleToolsCall returned error: %+v", rErr)
	}

	time.Sleep(60 * time.Millisecond)

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	reviewCount := 0
	for _, mem := range memories {
		if memory.IsReviewQueueMemory(mem) {
			reviewCount++
			if mem.Metadata[memory.MetadataSessionOrigin] != autoSessionOrigin {
				t.Fatalf("review queue session_origin = %q, want %q", mem.Metadata[memory.MetadataSessionOrigin], autoSessionOrigin)
			}
		}
	}
	if reviewCount == 0 {
		t.Fatalf("expected review queue item, memories = %d", len(memories))
	}

	view, err := s.memoryStore.ProjectBankView(context.Background(),memory.ProjectBankViewReviewQueue, memory.ProjectBankOptions{
		Filters: memory.Filters{Context: "payments"},
		Service: "api",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView(review_queue): %v", err)
	}
	if view.SectionCounts["review_queue"] == 0 {
		t.Fatalf("review_queue count = %d, want > 0", view.SectionCounts["review_queue"])
	}
}

func TestBackgroundSessionTrackerCreatesCheckpointDuringActiveSession(t *testing.T) {
	s := newAutoSessionTestServer(t, time.Hour, 5*time.Millisecond, 1)

	first, _ := json.Marshal(map[string]any{
		"name": "project_bank_view",
		"arguments": map[string]any{
			"view":    "canonical_overview",
			"context": "payments",
		},
	})
	if _, rErr := s.handleToolsCall(first); rErr != nil {
		t.Fatalf("first handleToolsCall returned error: %+v", rErr)
	}

	time.Sleep(10 * time.Millisecond)

	second, _ := json.Marshal(map[string]any{
		"name": "project_bank_view",
		"arguments": map[string]any{
			"view":    "review_queue",
			"context": "payments",
		},
	})
	if _, rErr := s.handleToolsCall(second); rErr != nil {
		t.Fatalf("second handleToolsCall returned error: %+v", rErr)
	}

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	checkpoints := 0
	for _, mem := range memories {
		if memory.IsSessionCheckpointMemory(mem) {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoint count = %d, want 1", checkpoints)
	}
}

func TestBackgroundSessionTrackerFlushesOnTaskDoneNotification(t *testing.T) {
	s := newAutoSessionTestServer(t, time.Hour, 0, 1)

	storeParams, _ := json.Marshal(map[string]any{
		"name": "store_incident",
		"arguments": map[string]any{
			"summary": "Latency spike mitigated with temporary fix, verify later.",
			"context": "payments",
			"service": "api",
		},
	})
	if _, rErr := s.handleToolsCall(storeParams); rErr != nil {
		t.Fatalf("handleToolsCall returned error: %+v", rErr)
	}

	s.handleNotification(rpcRequest{
		Method: "notifications/session_event",
		Params: json.RawMessage(`{
			"event":"task_done",
			"summary":"Incident stabilized, verification passed, but the workaround still needs review before the next deploy.",
			"context":"payments",
			"service":"api",
			"mode":"incident",
			"tags":["done","verification"]
		}`),
	})

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	foundRaw := false
	foundReview := false
	for _, mem := range memories {
		if memory.IsSessionSummaryMemory(mem) && mem.Metadata[memory.MetadataSessionBoundary] == "task_done" {
			foundRaw = true
			if !strings.Contains(mem.Content, "Incident stabilized") {
				t.Fatalf("task_done summary missing explicit final summary:\n%s", mem.Content)
			}
		}
		if memory.IsReviewQueueMemory(mem) {
			foundReview = true
		}
	}
	if !foundRaw {
		t.Fatal("expected task_done raw summary")
	}
	if !foundReview {
		t.Fatal("expected review queue item after task_done consolidation")
	}
}

func TestBackgroundSessionTrackerCheckpointNotificationPersistsCheckpoint(t *testing.T) {
	s := newAutoSessionTestServer(t, time.Hour, 0, 1)

	s.handleNotification(rpcRequest{
		Method: "notifications/session_event",
		Params: json.RawMessage(`{
			"event":"checkpoint",
			"summary":"Investigated migration sequencing and captured rollback caveat.",
			"context":"payments",
			"service":"api",
			"mode":"migration"
		}`),
	})

	memories, err := s.memoryStore.List(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	if !memory.IsSessionCheckpointMemory(memories[0]) {
		t.Fatalf("expected checkpoint memory, got %#v", memories[0].Metadata)
	}
	if memories[0].Metadata[memory.MetadataSessionBoundary] != "checkpoint" {
		t.Fatalf("session_boundary = %q, want checkpoint", memories[0].Metadata[memory.MetadataSessionBoundary])
	}
}

func TestCallResolveReviewItemRemovesItemFromActiveQueue(t *testing.T) {
	s := newMemoryTestServer(t)

	item := &memory.Memory{
		Title:      "Review queue / Replace rollback runbook / api",
		Content:    "Action: merge\nHandling: hard_review\nWhy: replacement is ambiguous",
		Type:       memory.TypeWorking,
		Context:    "payments",
		Importance: 0.45,
		Tags:       []string{"review-queue", "review:required", "status:review_required", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:         string(memory.EngineeringTypeRunbook),
			memory.MetadataService:        "api",
			memory.MetadataRecordKind:     memory.RecordKindReviewQueueItem,
			memory.MetadataReviewRequired: "true",
			memory.MetadataStatus:         "review_required",
		},
	}
	if err := s.memoryStore.Store(context.Background(),item); err != nil {
		t.Fatalf("Store review item: %v", err)
	}

	result, rErr := s.callResolveReviewItem(map[string]any{
		"id":         item.ID,
		"resolution": "dismissed",
		"owner":      "platform",
		"note":       "Handled manually outside the queue",
	})
	if rErr != nil {
		t.Fatalf("callResolveReviewItem returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "Resolution: dismissed") {
		t.Fatalf("resolve output missing resolution:\n%s", toolRes.Content[0].Text)
	}

	updated, err := s.memoryStore.Get(item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memory.RequiresReview(updated) {
		t.Fatalf("review item should no longer require review: %#v", updated.Metadata)
	}
	if updated.Metadata["review_resolved_by"] != "platform" {
		t.Fatalf("review_resolved_by = %q, want platform", updated.Metadata["review_resolved_by"])
	}

	view, err := s.memoryStore.ProjectBankView(context.Background(),memory.ProjectBankViewReviewQueue, memory.ProjectBankOptions{
		Filters: memory.Filters{Context: "payments"},
		Service: "api",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView(review_queue): %v", err)
	}
	if view.TotalCount != 0 {
		t.Fatalf("review queue should be empty after resolve, total_count = %d", view.TotalCount)
	}
}

func TestCanonicalKnowledgeTools(t *testing.T) {
	s := newMemoryTestServer(t)

	mem := &memory.Memory{
		Title:      "Canonical API rollback",
		Content:    "rollback api deployment and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			"entity":  "runbook",
			"service": "api",
		},
	}
	if err := s.memoryStore.Store(context.Background(),mem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(),mem.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	listed, rErr := s.callListCanonicalKnowledge(map[string]any{
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callListCanonicalKnowledge returned error: %+v", rErr)
	}
	listedText := listed.(toolResult).Content[0].Text
	for _, expected := range []string{
		"Canonical knowledge",
		"Canonical API rollback",
		"layer=canonical",
	} {
		if !strings.Contains(listedText, expected) {
			t.Fatalf("canonical list missing %q:\n%s", expected, listedText)
		}
	}

	recalled, rErr := s.callRecallCanonicalKnowledge(map[string]any{
		"query":   "rollback api deployment",
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callRecallCanonicalKnowledge returned error: %+v", rErr)
	}
	recalledText := recalled.(toolResult).Content[0].Text
	for _, expected := range []string{
		"Canonical knowledge for 'rollback api deployment'",
		"Canonical API rollback",
		"layer=canonical",
	} {
		if !strings.Contains(recalledText, expected) {
			t.Fatalf("canonical recall missing %q:\n%s", expected, recalledText)
		}
	}
}

func TestConsolidationToolsWorkflow(t *testing.T) {
	s := newMemoryTestServer(t)

	primary := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	duplicate := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration because rollout was unstable",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api", "migration"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(),primary); err != nil {
		t.Fatalf("Store primary: %v", err)
	}
	if err := s.memoryStore.Store(context.Background(),duplicate); err != nil {
		t.Fatalf("Store duplicate: %v", err)
	}

	conflicts, rErr := s.callConflictsReport(map[string]any{
		"context": "payments",
		"service": "api",
		"limit":   10,
	})
	if rErr != nil {
		t.Fatalf("callConflictsReport returned error: %+v", rErr)
	}
	conflictsText := conflicts.(toolResult).Content[0].Text
	if !strings.Contains(conflictsText, "duplicate_candidates") {
		t.Fatalf("expected duplicate_candidates in report:\n%s", conflictsText)
	}

	merged, rErr := s.callMergeDuplicates(map[string]any{
		"primary_id":    primary.ID,
		"duplicate_ids": []any{duplicate.ID},
	})
	if rErr != nil {
		t.Fatalf("callMergeDuplicates returned error: %+v", rErr)
	}
	if !strings.Contains(merged.(toolResult).Content[0].Text, "Duplicates merged:") {
		t.Fatalf("unexpected merge result: %s", merged.(toolResult).Content[0].Text)
	}

	promoted, rErr := s.callPromoteToCanonical(map[string]any{
		"id":    primary.ID,
		"owner": "platform",
	})
	if rErr != nil {
		t.Fatalf("callPromoteToCanonical returned error: %+v", rErr)
	}
	if !strings.Contains(promoted.(toolResult).Content[0].Text, "Memory promoted to canonical:") {
		t.Fatalf("unexpected promote result: %s", promoted.(toolResult).Content[0].Text)
	}

	marked, rErr := s.callMarkOutdated(map[string]any{
		"id":            duplicate.ID,
		"reason":        "duplicate runbook note",
		"superseded_by": primary.ID,
	})
	if rErr != nil {
		t.Fatalf("callMarkOutdated returned error: %+v", rErr)
	}
	if !strings.Contains(marked.(toolResult).Content[0].Text, "Memory marked outdated:") {
		t.Fatalf("unexpected outdated result: %s", marked.(toolResult).Content[0].Text)
	}

	storedPrimary, err := s.memoryStore.Get(primary.ID)
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if storedPrimary.Metadata["knowledge_layer"] != "canonical" {
		t.Fatalf("knowledge_layer = %q, want canonical", storedPrimary.Metadata["knowledge_layer"])
	}

	finalReport, err := s.memoryStore.ConflictsReport(context.Background(),memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("ConflictsReport: %v", err)
	}
	if len(finalReport) != 0 {
		t.Fatalf("expected no conflicts after merge, got %#v", finalReport)
	}
}

func containsTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}
