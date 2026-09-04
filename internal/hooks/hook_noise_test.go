package hooks

import (
	"context"
	"testing"
)

// A realistic SessionEnd payload (as emitted on /clear or a background session)
// — long enough to clear the MinContentChars empty gate, so it exercises the
// T80 hook-noise path rather than ReasonEmpty.
const sessionEndPayload = `{"session_id":"9544ff45-11fb-404d-bc11-c4165bedc656","transcript_path":"/Users/vit/.claude/projects/-Users-vit-Documents-Moving/9544ff45.jsonl","cwd":"/Users/vit/Documents/Moving","hook_event_name":"SessionEnd","reason":"clear"}`

// preCompactPayload is the PreCompact event as Claude Code emitted it on
// 2026-09-04, copied from a record it produced in the live brew bank. Unlike
// the SessionEnd payload above it carries custom_instructions and
// scratchpad_dir — keys outside the T80 whitelist, which is exactly why that
// whitelist matched none of the 78 stored payloads (T132).
const preCompactPayload = `{"session_id":"7e913ee6-8fb6-4305-abd4-0f6d5d7cf396","transcript_path":"/Users/vit/.claude/projects/-Users-vit-Sema/7e913ee6.jsonl","cwd":"/Users/vit/Sema/croqui-frontend","scratchpad_dir":"/private/tmp/claude-501/7e913ee6/scratchpad","prompt_id":"5a801064-6dc0-44c5-8226-a530f537fe31","hook_event_name":"PreCompact","trigger":"auto","custom_instructions":null}`

// TestCheck_PreCompactPayload_SkipHookNoise is T132: the shape verdict must not
// depend on knowing every field the harness puts in the event.
func TestCheck_PreCompactPayload_SkipHookNoise(t *testing.T) {
	store := newTestStore(t)

	result, err := Check(context.Background(), store, newSummary("proj-x", preCompactPayload), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonHookNoise {
		t.Fatalf("expected Skip=true reason=hook_noise, got %+v", result)
	}
}

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
		{"PreCompact payload with fields outside the whitelist", preCompactPayload, true},
		{"unknown future hook field alongside the signature", `{"session_id":"x","hook_event_name":"Stop","invented_field":1}`, true},
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

// TestIsHookEventPayload covers the exported signature test on its own: the CLI
// calls it before the store is even opened, so it has to stand without the
// whitelist arm behind it.
func TestIsHookEventPayload(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"PreCompact", preCompactPayload, true},
		{"SessionEnd", sessionEndPayload, true},
		{"signature plus unknown fields", `{"session_id":"x","hook_event_name":"Stop","whatever":[1,2]}`, true},
		{"event carrying a summary is not a bare event", `{"session_id":"x","hook_event_name":"Stop","summary":"fixed the deploy bug"}`, false},
		{"event carrying content is not a bare event", `{"session_id":"x","hook_event_name":"Stop","content":"fixed the deploy bug"}`, false},
		{"no hook_event_name", `{"session_id":"x","reason":"other"}`, false},
		{"no session_id", `{"hook_event_name":"Stop","trigger":"auto"}`, false},
		{"prose mentioning both field names", "The session_id and hook_event_name were both logged.", false},
		{"json array", `["session_id","hook_event_name"]`, false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsHookEventPayload(tc.in); got != tc.want {
				t.Errorf("IsHookEventPayload(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
