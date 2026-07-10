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
