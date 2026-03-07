package server

import (
	"context"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) callProjectBankView(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	view, err := memory.ValidateProjectBankView(mustString(args, "view"))
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	result, err := s.memoryStore.ProjectBankView(context.Background(), view, memory.ProjectBankOptions{
		Filters: memory.Filters{
			Context: strings.TrimSpace(mustString(args, "context")),
		},
		Service: strings.TrimSpace(mustString(args, "service")),
		Status:  strings.TrimSpace(mustString(args, "status")),
		Owner:   strings.TrimSpace(mustString(args, "owner")),
		Tags:    userio.NormalizeTags(getStringSlice(args, "tags")),
		Limit:   boundedLimit(args, 10, 50),
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to build project bank view", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		return toolResultText(s.formatProjectBankView(result)), nil
	default:
		return toolResultJSON(result), nil
	}
}
