package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

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

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 10)
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

	// Idle timeout is 20ms; sleep just past it to let the timer fire,
	// then drain the flush goroutine deterministically (Round 3 M10).
	time.Sleep(40 * time.Millisecond)
	s.sessionTracker.waitForBackground()

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 20)
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

	view, err := s.memoryStore.ProjectBankView(context.Background(), memory.ProjectBankViewReviewQueue, memory.ProjectBankOptions{
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

	// Round 3 M10: checkpoints are now async, so drain before asserting.
	s.sessionTracker.waitForCheckpoints()

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 10)
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

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 20)
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

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 10)
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
