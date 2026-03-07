package userio

import (
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

func TestParseMemoryType(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		defaultType memory.Type
		allowAll    bool
		want        memory.Type
		wantErr     string
	}{
		{name: "default", raw: "", defaultType: memory.TypeSemantic, want: memory.TypeSemantic},
		{name: "all", raw: "all", allowAll: true, want: ""},
		{name: "valid", raw: "procedural", want: memory.TypeProcedural},
		{name: "invalid", raw: "broken", wantErr: "invalid memory type"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMemoryType(tc.raw, tc.defaultType, tc.allowAll)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseMemoryType error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMemoryType error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseMemoryType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeTags(t *testing.T) {
	got := NormalizeTags([]string{" api ", "incident", "api", "", "incident", " rollback "})
	want := []string{"api", "incident", "rollback"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeTags[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestNormalizeImportancePreservesExplicitZero(t *testing.T) {
	got, err := NormalizeImportance(0, memory.DefaultImportance)
	if err != nil {
		t.Fatalf("NormalizeImportance error = %v", err)
	}
	if got != 0 {
		t.Fatalf("NormalizeImportance = %f, want 0", got)
	}
}

func TestFormatTrustSummarySkipsZeroVerified(t *testing.T) {
	formatted := FormatTrustSummary("canonical", "decision", 0.91, 0.85, "", time.Time{})
	if strings.Contains(formatted, "verified=") {
		t.Fatalf("formatted trust unexpectedly contains verified timestamp: %q", formatted)
	}
	if !strings.Contains(formatted, "owner=unknown") {
		t.Fatalf("formatted trust = %q, want owner=unknown", formatted)
	}
}

func TestFormatTrustWrappers(t *testing.T) {
	verifiedAt := time.Date(2026, time.March, 2, 10, 30, 0, 0, time.UTC)

	memTrust := FormatMemoryTrust(&trust.Metadata{
		KnowledgeLayer: "canonical",
		SourceType:     "decision",
		Confidence:     0.93,
		FreshnessScore: 0.76,
		Owner:          "platform",
		LastVerifiedAt: verifiedAt,
	})
	docTrust := FormatDocumentTrust(&trust.Metadata{
		KnowledgeLayer: "document",
		SourceType:     "runbook",
		Confidence:     0.88,
		FreshnessScore: 0.72,
		Owner:          "operations",
		LastVerifiedAt: verifiedAt,
	})

	for _, formatted := range []string{memTrust, docTrust} {
		if !strings.Contains(formatted, "verified=2026-03-02T10:30:00Z") {
			t.Fatalf("formatted trust missing verified timestamp: %q", formatted)
		}
	}
}
