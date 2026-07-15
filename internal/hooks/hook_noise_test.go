package hooks

import (
	"context"
	"testing"
)

// A realistic SessionEnd payload (as emitted on /clear or a background session)
// — long enough to clear the MinContentChars empty gate, so it exercises the
// T80 hook-noise path rather than ReasonEmpty.
const sessionEndPayload = `{"session_id":"9544ff45-11fb-404d-bc11-c4165bedc656","transcript_path":"/Users/vit/.claude/projects/-Users-vit-Documents-Moving/9544ff45.jsonl","cwd":"/Users/vit/Documents/Moving","hook_event_name":"SessionEnd","reason":"clear"}`

// TestCheck_HookMetadataPayload_SkipHookNoise pins the T80 fix: a raw
// session-hook JSON payload (no session content) is skipped with
// ReasonHookNoise instead of being persisted as a no-content stub.
func TestCheck_HookMetadataPayload_SkipHookNoise(t *testing.T) {
	store := newTestStore(t)

	result, err := Check(context.Background(), store, newSummary("proj-x", sessionEndPayload), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonHookNoise {
		t.Fatalf("expected Skip=true reason=hook_noise, got %+v", result)
	}
}

// TestCheck_HookNoise_SkipsEvenWhenDedupDisabled: hook-noise filtering is
// unconditional — a content-free payload must not be stored even when the
// similarity/min-chars dedup is disabled.
func TestCheck_HookNoise_SkipsEvenWhenDedupDisabled(t *testing.T) {
	store := newTestStore(t)

	cfg := NewDedupConfig(true, 0, 0, 0) // disabled
	result, err := Check(context.Background(), store, newSummary("proj-x", sessionEndPayload), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonHookNoise {
		t.Fatalf("expected hook-noise skip even when dedup disabled, got %+v", result)
	}
}

// choreLog is a search-only session summary: every line is a maintenance
// bullet, no reusable knowledge. Its embedding would be built from the query
// text and rank above the answer — exactly the self-poisoning T85 prevents.
const choreLog = `- Document search: how does clientip handle IPv6
- Memory recall: rate limit shared bucket
- Merged duplicates: 1a2b3c4d-0000-0000-0000-000000000000
- Marked outdated: 9f8e7d6c-0000-0000-0000-000000000000`

// TestCheck_ChoreLog_SkipHookNoise pins the T85 fix: a summary made only of
// maintenance-action bullets is skipped with ReasonHookNoise.
func TestCheck_ChoreLog_SkipHookNoise(t *testing.T) {
	store := newTestStore(t)

	result, err := Check(context.Background(), store, newSummary("proj-x", choreLog), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonHookNoise {
		t.Fatalf("expected Skip=true reason=hook_noise, got %+v", result)
	}
}

// TestCheck_ChoreLog_SkipsEvenWhenDedupDisabled: like T80, the chore-log guard
// is unconditional.
func TestCheck_ChoreLog_SkipsEvenWhenDedupDisabled(t *testing.T) {
	store := newTestStore(t)

	cfg := NewDedupConfig(true, 0, 0, 0) // disabled
	result, err := Check(context.Background(), store, newSummary("proj-x", choreLog), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonHookNoise {
		t.Fatalf("expected chore-log skip even when dedup disabled, got %+v", result)
	}
}

// TestCheck_RealReportWithoutStored_NotSkipped: whitelist not blacklist — a
// genuine closure report is preserved even when it never says "Stored memory".
func TestCheck_RealReportWithoutStored_NotSkipped(t *testing.T) {
	store := newTestStore(t)

	report := "- Document search: prior art on busy_timeout\n- Fixed SQLite busy: added _busy_timeout=5000 and verified WAL pragma survives restart"
	result, err := Check(context.Background(), store, newSummary("proj-x", report), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected real report to be kept, got %+v", result)
	}
}

func TestIsChoreLogOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"all chore bullets", choreLog, true},
		{"single chore bullet", "- Repo search: scanner.go", true},
		{"chore with blank lines", "\n- Memory recall: x\n\n- Inspected file: y\n", true},
		{"star bullet marker", "* Project bank review: sema-prod", true},
		{"real report line present", "- Memory recall: x\n- Stored memory: root cause was byte truncation", false},
		{"incident investigation is not chore (real knowledge risk)", "- Incident investigation: root cause was the missing DB index; added it and latency recovered", false},
		{"unlabelled bullet", "- just did some things", false},
		{"unknown label", "- Refactored: the retrieval pipeline", false},
		{"prose with colon", "Fixed the search: it now works", false},
		{"empty", "", false},
		{"whitespace only", "   \n  \n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChoreLogOnly(tc.in); got != tc.want {
				t.Errorf("isChoreLogOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsHookMetadataOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"full SessionEnd payload", sessionEndPayload, true},
		{"minimal session_id+reason", `{"session_id":"x","reason":"other"}`, true},
		{"json with content field", `{"session_id":"x","reason":"clear","summary":"fixed the deploy bug"}`, false},
		{"prose summary", "Fixed the rollback runbook and verified the deploy on staging.", false},
		{"prose that mentions session_id", "The session_id was logged; reason unknown.", false},
		{"empty object", `{}`, false},
		{"not json", `not json at all`, false},
		{"json array", `["session_id","reason"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHookMetadataOnly(tc.in); got != tc.want {
				t.Errorf("isHookMetadataOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
