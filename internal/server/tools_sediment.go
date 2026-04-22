package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// callPromoteSediment moves a memory to the requested sediment layer.
// target_layer must be one of surface/episodic/semantic/character.
func (s *MCPServer) callPromoteSediment(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(mustString(args, "id"))
	if id == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}
	target := strings.TrimSpace(mustString(args, "target_layer"))
	if !memory.IsValidSedimentLayer(target) {
		return nil, &rpcError{
			Code:    rpcErrInvalidParams,
			Message: fmt.Sprintf("invalid target_layer %q (expected surface, episodic, semantic, or character)", target),
		}
	}
	res, err := s.memoryStore.PromoteSediment(context.Background(), id, memory.SedimentLayer(target))
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to promote sediment", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	if format == "text" {
		text := fmt.Sprintf("Sediment layer updated:\n- ID: %s\n- From: %s\n- To: %s\n- Affected: %v",
			res.ID, res.From, res.To, res.Affected)
		return toolResultText(text), nil
	}
	return toolResultJSON(res), nil
}

// callDemoteSediment moves a memory one layer closer to surface. No-op at surface.
func (s *MCPServer) callDemoteSediment(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(mustString(args, "id"))
	if id == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}
	res, err := s.memoryStore.DemoteSediment(context.Background(), id)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to demote sediment", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	if format == "text" {
		msg := "Sediment layer demoted"
		if !res.Affected {
			msg = "Sediment layer unchanged (already at surface)"
		}
		text := fmt.Sprintf("%s:\n- ID: %s\n- From: %s\n- To: %s",
			msg, res.ID, res.From, res.To)
		return toolResultText(text), nil
	}
	return toolResultJSON(res), nil
}

// callSedimentCycle runs the sediment-cycle job (T48). Non-auto transitions
// land in the review_queue_item pipeline; trivial ones are auto-applied
// (unless dry_run=true).
func (s *MCPServer) callSedimentCycle(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	dryRun, _ := getBool(args, "dry_run")
	sinceDays, _ := getInt(args, "since_days")
	limit, _ := getInt(args, "limit")

	cfg := memory.SedimentCycleConfig{
		DryRun:    dryRun,
		SinceDays: sinceDays,
		Limit:     limit,
	}
	result, err := s.memoryStore.RunSedimentCycle(context.Background(), cfg)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "sediment cycle failed", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	if format == "text" {
		mode := "live"
		if result.DryRun {
			mode = "dry-run"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Sediment cycle (%s):\n", mode)
		fmt.Fprintf(&b, "- Auto applied: %d\n", result.AutoApplied)
		fmt.Fprintf(&b, "- Review queued: %d\n", result.ReviewQueued)
		fmt.Fprintf(&b, "- Skipped: %d\n", result.Skipped)
		if len(result.Errors) > 0 {
			b.WriteString("\nErrors:\n")
			for _, e := range result.Errors {
				fmt.Fprintf(&b, "- %s\n", e)
			}
		}
		return toolResultText(b.String()), nil
	}
	return toolResultJSON(result), nil
}
