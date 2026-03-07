package main

import (
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestParseSessionMetadataFlag(t *testing.T) {
	got, err := parseSessionMetadataFlag("owner=platform, ticket = INC-42 ")
	if err != nil {
		t.Fatalf("parseSessionMetadataFlag: %v", err)
	}
	if got["owner"] != "platform" {
		t.Fatalf("owner = %q, want platform", got["owner"])
	}
	if got["ticket"] != "INC-42" {
		t.Fatalf("ticket = %q, want INC-42", got["ticket"])
	}
}

func TestParseSessionMetadataFlagRejectsInvalidEntry(t *testing.T) {
	if _, err := parseSessionMetadataFlag("owner"); err == nil {
		t.Fatal("expected invalid metadata entry error")
	}
}

func TestReadSessionSummaryPrefersExplicitSummary(t *testing.T) {
	got, err := readSessionSummary("summary from flag", false, []string{"positional", "text"})
	if err != nil {
		t.Fatalf("readSessionSummary: %v", err)
	}
	if got != "summary from flag" {
		t.Fatalf("summary = %q, want %q", got, "summary from flag")
	}
}

func TestReadSessionSummaryFallsBackToPositional(t *testing.T) {
	got, err := readSessionSummary("", false, []string{"first", "second"})
	if err != nil {
		t.Fatalf("readSessionSummary: %v", err)
	}
	if got != "first second" {
		t.Fatalf("summary = %q, want %q", got, "first second")
	}
}

func TestParseSessionModeValueAllowsEmptyAndValidModes(t *testing.T) {
	got, err := parseSessionModeValue("")
	if err != nil {
		t.Fatalf("parseSessionModeValue(empty): %v", err)
	}
	if got != "" {
		t.Fatalf("mode = %q, want empty", got)
	}

	got, err = parseSessionModeValue("incident")
	if err != nil {
		t.Fatalf("parseSessionModeValue(incident): %v", err)
	}
	if got != memory.SessionModeIncident {
		t.Fatalf("mode = %q, want %q", got, memory.SessionModeIncident)
	}
}

func TestParseSessionTimeFlag(t *testing.T) {
	got, err := parseSessionTimeFlag("2026-03-06T12:00:00Z", "started-at")
	if err != nil {
		t.Fatalf("parseSessionTimeFlag: %v", err)
	}
	want := time.Date(2026, time.March, 6, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("time = %s, want %s", got, want)
	}
}
