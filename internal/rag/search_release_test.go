//go:build !eval

package rag_test

import (
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"reflect"
	"testing"
)

func TestReleaseBuildExcludesSemanticEvaluation(t *testing.T) {
	if _, exists := reflect.TypeOf((*rag.Engine)(nil)).MethodByName("SearchSemanticForEval"); exists {
		t.Fatal("release builds must not expose the evaluation search entry point")
	}
}
