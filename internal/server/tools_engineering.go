package server

// store_decision / store_runbook / store_incident / store_postmortem /
// store_dead_end and the shared write path behind them. T91: split out of
// tools_workflow.go, which had grown to 1068 lines spanning session analysis,
// engineering writes, the review queue and the archive sweep. Moved verbatim.

import (
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
	"go.uber.org/zap"
)

func (s *MCPServer) storeEngineeringMemory(args map[string]any, entityType memory.EngineeringType, entityLabel string, content string, titleFallback string, defaultImportance float64, extraTags []string, extraMeta map[string]string) (any, *rpcError) {
	title, _ := getString(args, "title")
	service := mustString(args, "service")
	severity := mustString(args, "severity")
	status := mustString(args, "status")
	importance := defaultImportance
	v, ok, impErr := getImportance(args)
	if impErr != nil {
		return nil, impErr
	}
	if ok {
		importance = v
	}
	mem := &memory.Memory{
		Title:      defaultTitle(title, titleFallback),
		Content:    content,
		Type:       memory.DefaultStorageTypeForEngineeringType(entityType),
		Context:    mustString(args, "context"),
		Importance: importance,
		Tags:       memory.BuildEngineeringTags(entityType, service, severity, status, false, append(extraTags, getStringSlice(args, "tags")...)),
		Metadata:   memory.BuildEngineeringMetadata(entityType, service, severity, status, false, extraMeta),
	}
	return s.storeWorkflowMemory(entityLabel, mem)
}

func (s *MCPServer) callStoreDecision(args map[string]any) (any, *rpcError) {
	decision, rsErr := requiredString(args, "decision")
	if rsErr != nil {
		return nil, rsErr
	}
	owner := mustString(args, "owner")
	content := joinContentLines(
		prefixedLine("Decision", decision), prefixedLine("Rationale", mustString(args, "rationale")),
		prefixedLine("Consequences", mustString(args, "consequences")), prefixedLine("Service", mustString(args, "service")),
		prefixedLine("Owner", owner), prefixedLine("Status", mustString(args, "status")),
		prefixedLine("Avoided dead end", mustString(args, "avoided_dead_end_id")),
	)
	extraMeta := map[string]string{"owner": owner}
	avoidedID := strings.TrimSpace(mustString(args, "avoided_dead_end_id"))
	if avoidedID != "" {
		extraMeta["avoided_dead_end_id"] = avoidedID
	}
	result, rpcErr := s.storeEngineeringMemory(args, memory.EngineeringTypeDecision, "Decision", content, decision, 0.85, nil, extraMeta)
	if rpcErr == nil && avoidedID != "" && s.memoryStore != nil {
		// Best-effort observability counter: never fail the originating
		// Store call if the increment errors. Feeds the T48 semantic→
		// character "by refs" promotion rule.
		if err := s.memoryStore.IncrementReferencedByCount(context.Background(), avoidedID); err != nil && s.fileLogger != nil {
			s.fileLogger.Warn("failed to increment referenced_by_count on avoided dead end",
				zap.String("target", avoidedID), zap.Error(err))
		}
	}
	return result, rpcErr
}

// callStoreDeadEnd persists an abandoned approach with its failure rationale.
// Stores as TypeSemantic (knowledge, not event) with metadata.entity=dead_end.
func (s *MCPServer) callStoreDeadEnd(args map[string]any) (any, *rpcError) {
	attempted, rsErr := requiredString(args, "attempted_approach")
	if rsErr != nil {
		return nil, rsErr
	}
	whyFailed, rsErr := requiredString(args, "why_failed")
	if rsErr != nil {
		return nil, rsErr
	}
	content := joinContentLines(
		prefixedLine("Attempted approach", attempted),
		prefixedLine("Why failed", whyFailed),
		prefixedLine("Alternative used", mustString(args, "alternative_used")),
		prefixedLine("Related task", mustString(args, "related_task_slug")),
		prefixedLine("Service", mustString(args, "service")),
	)
	extraMeta := map[string]string{}
	if slug := strings.TrimSpace(mustString(args, "related_task_slug")); slug != "" {
		extraMeta["related_task_slug"] = slug
	}
	if alt := strings.TrimSpace(mustString(args, "alternative_used")); alt != "" {
		extraMeta["alternative_used"] = alt
	}
	return s.storeEngineeringMemory(args, memory.EngineeringTypeDeadEnd, "Dead End", content, attempted, 0.80, nil, extraMeta)
}

func (s *MCPServer) callStoreIncident(args map[string]any) (any, *rpcError) {
	summary, rsErr := requiredString(args, "summary")
	if rsErr != nil {
		return nil, rsErr
	}
	content := joinContentLines(
		prefixedLine("Incident", summary), prefixedLine("Impact", mustString(args, "impact")),
		prefixedLine("Root cause", mustString(args, "root_cause")), prefixedLine("Resolution", mustString(args, "resolution")),
		prefixedLine("Service", mustString(args, "service")), prefixedLine("Severity", mustString(args, "severity")),
	)
	return s.storeEngineeringMemory(args, memory.EngineeringTypeIncident, "Incident", content, summary, 0.90, nil, nil)
}

func (s *MCPServer) callStoreRunbook(args map[string]any) (any, *rpcError) {
	procedure, rsErr := requiredString(args, "procedure")
	if rsErr != nil {
		return nil, rsErr
	}
	content := joinContentLines(
		prefixedLine("Procedure", procedure), prefixedLine("Trigger", mustString(args, "trigger")),
		prefixedLine("Verification", mustString(args, "verification")), prefixedLine("Rollback", mustString(args, "rollback")),
		prefixedLine("Service", mustString(args, "service")),
	)
	return s.storeEngineeringMemory(args, memory.EngineeringTypeRunbook, "Runbook", content, procedure, 0.85, nil, nil)
}

func (s *MCPServer) callStorePostmortem(args map[string]any) (any, *rpcError) {
	summary, rsErr := requiredString(args, "summary")
	if rsErr != nil {
		return nil, rsErr
	}
	content := joinContentLines(
		prefixedLine("Postmortem", summary), prefixedLine("Impact", mustString(args, "impact")),
		prefixedLine("Root cause", mustString(args, "root_cause")), prefixedLine("Action items", mustString(args, "action_items")),
		prefixedLine("Follow-up", mustString(args, "follow_up")), prefixedLine("Service", mustString(args, "service")),
		prefixedLine("Severity", mustString(args, "severity")),
	)
	return s.storeEngineeringMemory(args, memory.EngineeringTypePostmortem, "Postmortem", content, summary, 0.85, []string{"incident"}, nil)
}

func (s *MCPServer) storeWorkflowMemory(entityLabel string, mem *memory.Memory) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}
	mem.Content = strings.TrimSpace(mem.Content)
	mem.Title = strings.TrimSpace(mem.Title)
	mem.Context = strings.TrimSpace(mem.Context)
	mem.Tags = userio.NormalizeTags(mem.Tags)
	if err := userio.ValidateMemoryContent(mem.Content); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to store memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("%s stored:\n- ID: %s\n- Type: %s\n- Title: %s\n- Tags: %v",
		entityLabel, mem.ID, formatMemoryType(mem.Type), mem.Title, mem.Tags)), nil
}
