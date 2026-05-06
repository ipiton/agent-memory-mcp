package main

import "testing"

func TestIsOurHookCommand(t *testing.T) {
	const current = "/usr/local/bin/agent-memory-mcp"

	tests := []struct {
		name        string
		cmd         string
		current     string
		wantOurs    bool
		wantCurrent bool
	}{
		{
			name:        "exact match — current binary",
			cmd:         "/usr/local/bin/agent-memory-mcp hook session-start",
			current:     current,
			wantOurs:    true,
			wantCurrent: true,
		},
		{
			name:        "ours but different path",
			cmd:         "/Users/vit/.local/bin/agent-memory-mcp hook pre-compact",
			current:     current,
			wantOurs:    true,
			wantCurrent: false,
		},
		{
			name:        "not ours — agent-memory-mcp-old false positive must be rejected",
			cmd:         "/usr/local/bin/agent-memory-mcp-old hook session-start",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "not ours — name appears mid-string in argv",
			cmd:         "/bin/echo agent-memory-mcp foo",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "empty",
			cmd:         "",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "bare binary name",
			cmd:         "agent-memory-mcp hook session-end",
			current:     "agent-memory-mcp",
			wantOurs:    true,
			wantCurrent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ours, current := isOurHookCommand(tc.cmd, tc.current)
			if ours != tc.wantOurs || current != tc.wantCurrent {
				t.Errorf("isOurHookCommand(%q, %q) = (ours=%v, current=%v), want (%v, %v)",
					tc.cmd, tc.current, ours, current, tc.wantOurs, tc.wantCurrent)
			}
		})
	}
}
