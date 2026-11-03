package sessionclose

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T145: a transcript nobody is watching must survive a pasted token, with the
// token gone from what is stored. A refusal here would lose the whole session.
func TestSaveRawSummaryMasksProviderTokens(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	token := "gh" + "p_" + strings.Repeat("a1B2", 9)

	id, err := svc.SaveRawSummary(ctx, memory.SessionSummary{
		Context: "t145",
		Summary: "Rotated the deploy token " + token + " and redeployed the service.",
	})
	if err != nil {
		t.Fatalf("SaveRawSummary: %v", err)
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(got.Content, token) || !strings.Contains(got.Content, "[REDACTED GITHUB TOKEN]") {
		t.Errorf("content = %q", got.Content)
	}
	if !strings.Contains(got.Content, "redeployed the service") {
		t.Errorf("the rest of the session was lost: %q", got.Content)
	}
}
