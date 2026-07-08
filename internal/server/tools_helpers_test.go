package server

import (
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		want      string
		wantError bool
	}{
		{name: "empty defaults to text", args: map[string]any{}, want: "text"},
		{name: "explicit text", args: map[string]any{"format": "text"}, want: "text"},
		{name: "explicit json", args: map[string]any{"format": "json"}, want: "json"},
		{name: "uppercase normalized", args: map[string]any{"format": "JSON"}, want: "json"},
		{name: "with whitespace", args: map[string]any{"format": "  text  "}, want: "text"},
		{name: "yaml rejected", args: map[string]any{"format": "yaml"}, wantError: true},
		{name: "garbage rejected", args: map[string]any{"format": "xml"}, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFormat(tc.args)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseFormat(%v): expected error, got %q", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFormat(%v): unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("parseFormat(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestRequiredString(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		key       string
		want      string
		wantError bool
	}{
		{name: "present", args: map[string]any{"id": "abc"}, key: "id", want: "abc"},
		{name: "trimmed", args: map[string]any{"id": "  abc  "}, key: "id", want: "abc"},
		{name: "missing", args: map[string]any{}, key: "id", wantError: true},
		{name: "blank", args: map[string]any{"id": ""}, key: "id", wantError: true},
		{name: "whitespace only", args: map[string]any{"id": "   "}, key: "id", wantError: true},
		// Round 3 M33: getString is strict — a non-string value (JSON number,
		// bool, object) is treated as absent, so requiredString rejects it
		// instead of coercing it to "5"/"true".
		{name: "non-string number rejected", args: map[string]any{"id": float64(5)}, key: "id", wantError: true},
		{name: "non-string bool rejected", args: map[string]any{"id": true}, key: "id", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requiredString(tc.args, tc.key)
			if tc.wantError {
				if err == nil {
					t.Fatalf("requiredString(%v, %q): expected error, got %q", tc.args, tc.key, got)
				}
				if !strings.Contains(err.Message, tc.key) {
					t.Errorf("error message %q should mention key %q", err.Message, tc.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("requiredString(%v, %q): unexpected error: %v", tc.args, tc.key, err)
			}
			if got != tc.want {
				t.Errorf("requiredString(%v, %q) = %q, want %q", tc.args, tc.key, got, tc.want)
			}
		})
	}
}

func TestRenderFormatted_DispatchesByFormat(t *testing.T) {
	jsonValue := map[string]any{"a": 1}
	textCalls := 0
	textFn := func() string {
		textCalls++
		return "hello"
	}

	t.Run("text invokes textFn", func(t *testing.T) {
		textCalls = 0
		result := renderFormatted("text", jsonValue, textFn)
		tr, ok := result.(toolResult)
		if !ok {
			t.Fatalf("expected toolResult, got %T", result)
		}
		if len(tr.Content) != 1 || tr.Content[0].Text != "hello" {
			t.Errorf("text path: expected hello, got %+v", tr.Content)
		}
		if textCalls != 1 {
			t.Errorf("textFn called %d times, want 1", textCalls)
		}
	})

	t.Run("json bypasses textFn", func(t *testing.T) {
		textCalls = 0
		result := renderFormatted("json", jsonValue, textFn)
		tr, ok := result.(toolResult)
		if !ok {
			t.Fatalf("expected toolResult, got %T", result)
		}
		if !strings.Contains(tr.Content[0].Text, "\"a\"") {
			t.Errorf("json path: expected JSON with \"a\", got %q", tr.Content[0].Text)
		}
		if textCalls != 0 {
			t.Errorf("textFn called %d times in json path, want 0 (lazy)", textCalls)
		}
	})
}

// TestBuildSessionSchema_ProducesValidSchema pins down P1-2: review and accept
// must share the base shape with close_session, but allow per-tool summary
// description and extras.
func TestBuildSessionSchema_ProducesValidSchema(t *testing.T) {
	base := buildSessionSchema("base", nil)
	if base["type"] != "object" {
		t.Errorf("type = %v, want object", base["type"])
	}
	required, ok := base["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "summary" {
		t.Errorf("required = %v, want [summary]", base["required"])
	}
	props, _ := base["properties"].(map[string]any)
	for _, key := range []string{"summary", "mode", "context", "service", "started_at", "ended_at", "tags", "metadata", "format"} {
		if _, ok := props[key]; !ok {
			t.Errorf("base schema missing property %q", key)
		}
	}
	if summary, _ := props["summary"].(map[string]any); summary["description"] != "base" {
		t.Errorf("summary description = %v, want base", summary["description"])
	}

	withExtras := buildSessionSchema("", map[string]any{
		"dry_run": map[string]any{"type": "boolean"},
	})
	props, _ = withExtras["properties"].(map[string]any)
	if _, ok := props["dry_run"]; !ok {
		t.Error("extras not merged")
	}
	// Empty summaryDesc gets the default.
	if summary, _ := props["summary"].(map[string]any); summary["description"] != "Raw session summary text" {
		t.Errorf("default summary description = %v", summary["description"])
	}
}

// TestGetStringStrict pins the Round 3 M33 contract: only an actual JSON string
// is accepted; any other type reads as absent (ok=false) rather than being
// coerced via fmt.Sprintf.
func TestGetStringStrict(t *testing.T) {
	if got, ok := getString(map[string]any{"k": "hello"}, "k"); !ok || got != "hello" {
		t.Errorf("string value: got (%q, %v), want (hello, true)", got, ok)
	}
	if _, ok := getString(map[string]any{}, "k"); ok {
		t.Error("missing key: ok=true, want false")
	}
	for name, val := range map[string]any{
		"number": float64(5),
		"bool":   true,
		"slice":  []any{"a"},
		"object": map[string]any{"a": 1},
	} {
		if got, ok := getString(map[string]any{"k": val}, "k"); ok || got != "" {
			t.Errorf("%s value: got (%q, %v), want (\"\", false)", name, got, ok)
		}
	}
}

// TestGetImportanceHonestContract pins Round 3 L29: a valid in-range float
// returns (v, true); missing/wrong-type/out-of-range returns (0, false) so the
// caller applies its own default instead of the value being silently swallowed.
func TestGetImportanceHonestContract(t *testing.T) {
	if v, ok := getImportance(map[string]any{"importance": 0.7}); !ok || v != 0.7 {
		t.Errorf("valid: got (%v, %v), want (0.7, true)", v, ok)
	}
	cases := map[string]map[string]any{
		"missing":      {},
		"out of range": {"importance": 1.5},
		"negative":     {"importance": -0.1},
		"wrong type":   {"importance": "0.7"},
	}
	for name, args := range cases {
		if v, ok := getImportance(args); ok || v != 0 {
			t.Errorf("%s: got (%v, %v), want (0, false)", name, v, ok)
		}
	}
}
