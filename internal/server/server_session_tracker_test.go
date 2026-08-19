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

	// T122: the activity has to be one that carries knowledge. This test used
	// project_bank_view, whose whole session body is "- Project bank review:
	// canonical_overview" — a chore-log line that the write boundary now
	// refuses, so the test was asserting that a no-content record gets stored.
	// "Incident investigation" is deliberately outside the chore whitelist (a
	// human writes real root-cause prose under it), which makes it the right
	// read-only stand-in here.
	params, _ := json.Marshal(map[string]any{
		"name": "recall_similar_incidents",
		"arguments": map[string]any{
			"query":   "latency spike on the payments api",
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
	// The incident-recall activity also queues a review item, so pick the
	// summary out rather than asserting on the whole context.
	var summary *memory.Memory
	for _, m := range memories {
		if memory.IsSessionSummaryMemory(m) {
			if summary != nil {
				t.Fatalf("two session summaries for one flush")
			}
			summary = m
		}
	}
	if summary == nil {
		t.Fatalf("no session summary among %d memories", len(memories))
	}
	if summary.Metadata[memory.MetadataSessionBoundary] != "idle_timeout" {
		t.Fatalf("session_boundary = %q, want idle_timeout", summary.Metadata[memory.MetadataSessionBoundary])
	}
	if summary.Metadata[memory.MetadataSessionOrigin] != autoSessionOrigin {
		t.Fatalf("session_origin = %q, want %q", summary.Metadata[memory.MetadataSessionOrigin], autoSessionOrigin)
	}
}

// T122: the auto-capture idle flush goes Analyze → executeActions →
// SaveRawSummary and never touched hooks.Check, so a session that only ran
// maintenance views persisted a body of pure activity bullets. On the live bank
// that produced 75 such records against 0 on the guarded checkpoint path.
func TestBackgroundSessionTrackerSkipsChoreOnlySession(t *testing.T) {
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
	for _, m := range memories {
		if memory.IsSessionSummaryMemory(m) {
			t.Fatalf("a chore-only session was persisted as a summary: %q", m.Content)
		}
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

	// No sleep: waitForBackground drains the armed idle timer and then the
	// flush goroutine it starts. Sleeping "just past" a 20ms timeout is what
	// made this test race on a loaded runner.
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

	// A knowledge-bearing call so the checkpoint summary is not chore-log-only —
	// the T85 guard suppresses checkpoints whose every line is a maintenance
	// bullet (a session that only browsed project banks yields no memory).
	first, _ := json.Marshal(map[string]any{
		"name": "store_incident",
		"arguments": map[string]any{
			"summary": "Traced the latency spike to a missing index and added it.",
			"context": "payments",
			"service": "api",
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
