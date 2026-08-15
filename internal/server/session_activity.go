package server

// Tool-call to activity-line mapping. T91 M7: split out of session_tracker.go,
// which mixed five concerns in 881 lines. These functions are pure — argument
// map in, display string out — so they are testable without a store, a clock or
// a running session, which was not true while they lived next to the tracker's
// locking and flush machinery.
//
// No behaviour change: the functions moved verbatim.

import (
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func buildActivityLine(name string, args map[string]any) string {
	switch name {
	case "store_decision":
		return prefixedActivity("Decision", firstNonEmpty(trimArg(args, "decision"), trimArg(args, "title")))
	case "store_incident":
		return prefixedActivity("Incident", firstNonEmpty(trimArg(args, "summary"), trimArg(args, "title")))
	case "store_runbook":
		return prefixedActivity("Runbook", firstNonEmpty(trimArg(args, "procedure"), trimArg(args, "title")))
	case "store_postmortem":
		return prefixedActivity("Postmortem", firstNonEmpty(trimArg(args, "summary"), trimArg(args, "title")))
	case "store_memory":
		return buildGenericMemoryActivity(args)
	case "update_memory":
		// T123: title first — see buildGenericMemoryActivity.
		return prefixedActivity("Updated knowledge", firstNonEmpty(trimArg(args, "title"), trimArg(args, "id"), trimArg(args, "content")))
	case "merge_duplicates":
		return prefixedActivity("Merged duplicates", firstNonEmpty(trimArg(args, "primary_id"), trimArg(args, "duplicate_ids")))
	case "mark_outdated":
		return prefixedActivity("Marked outdated", firstNonEmpty(trimArg(args, "id"), trimArg(args, "reason")))
	case "promote_to_canonical":
		return prefixedActivity("Promoted canonical", trimArg(args, "id"))
	case "search_runbooks":
		return prefixedActivity("Runbook search", trimArg(args, "query"))
	case "recall_similar_incidents":
		return prefixedActivity("Incident investigation", trimArg(args, "query"))
	case "summarize_project_context":
		return prefixedActivity("Project context", firstNonEmpty(trimArg(args, "focus"), trimArg(args, "context"), trimArg(args, "service")))
	case "semantic_search":
		return prefixedActivity("Document search", trimArg(args, "query"))
	case "recall_memory":
		return prefixedActivity("Memory recall", trimArg(args, "query"))
	case "repo_read":
		return prefixedActivity("Inspected file", trimArg(args, "path"))
	case "repo_search":
		return prefixedActivity("Repo search", firstNonEmpty(trimArg(args, "query"), trimArg(args, "path")))
	case "repo_list":
		return prefixedActivity("Listed repo path", trimArg(args, "path"))
	case "project_bank_view":
		return prefixedActivity("Project bank review", firstNonEmpty(trimArg(args, "view"), trimArg(args, "context"), trimArg(args, "service")))
	default:
		return ""
	}
}

// buildGenericMemoryActivity renders the "the session stored a record" line.
//
// T123: the title comes first, and that ordering is the whole point. Taking the
// content first made the line a 220-character copy of a record that exists in
// the bank in full, and the copy competes with its own original — shorter text
// carrying the same vocabulary scores denser. Measured on the live bank: 1020
// such pairs, and querying with the copied text put the copy above the original
// (or displaced it from the top ten entirely) in 6 of a 60-pair sample.
//
// A title is a pointer to the same record and cannot duplicate it. 1018 of the
// 1020 originals have one; content stays as the fallback for the rest.
func buildGenericMemoryActivity(args map[string]any) string {
	content := firstNonEmpty(trimArg(args, "title"), trimArg(args, "content"))
	if content == "" {
		return ""
	}
	entity := ""
	metadata := getStringMap(args, "metadata")
	if len(metadata) > 0 {
		entity = strings.TrimSpace(metadata[memory.MetadataEntity])
	}
	if entity == "" {
		for _, tag := range getStringSlice(args, "tags") {
			if value, err := memory.ValidateEngineeringType(tag, true); err == nil && value != "" {
				entity = string(value)
				break
			}
		}
	}
	switch entity {
	case string(memory.EngineeringTypeDecision):
		return prefixedActivity("Decision", content)
	case string(memory.EngineeringTypeIncident):
		return prefixedActivity("Incident", content)
	case string(memory.EngineeringTypeRunbook), string(memory.EngineeringTypeProcedure):
		return prefixedActivity("Runbook", content)
	case string(memory.EngineeringTypeMigrationNote):
		return prefixedActivity("Migration", content)
	case string(memory.EngineeringTypeCaveat):
		return prefixedActivity("Caveat", content)
	default:
		return prefixedActivity("Stored memory", content)
	}
}

func prefixedActivity(prefix string, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s", prefix, truncateText(body, 220))
}

func activityContext(args map[string]any) string {
	return trimArg(args, "context")
}

func activityService(args map[string]any) string {
	return trimArg(args, "service")
}

func activityTags(name string, args map[string]any) []string {
	tags := memory.NormalizeTags(getStringSlice(args, "tags"))
	switch name {
	case "store_incident", "recall_similar_incidents":
		tags = append(tags, "mode:incident")
	case "store_postmortem":
		tags = append(tags, "mode:incident")
	case "store_runbook", "search_runbooks":
		tags = append(tags, "mode:coding")
	}
	return memory.NormalizeTags(tags)
}

func activitySessionMode(name string, args map[string]any) memory.SessionMode {
	if modeValue := trimArg(args, "mode"); modeValue != "" {
		if mode, err := memory.ValidateSessionMode(modeValue, ""); err == nil {
			return mode
		}
	}
	switch name {
	case "store_incident", "store_postmortem", "recall_similar_incidents":
		return memory.SessionModeIncident
	default:
		return ""
	}
}

func sessionToolWrites(args map[string]any) bool {
	if dryRun, ok := getBool(args, "dry_run"); ok && !dryRun {
		return true
	}
	if saveRaw, ok := getBool(args, "save_raw"); ok && saveRaw {
		return true
	}
	if autoApply, ok := getBool(args, "auto_apply_low_risk"); ok && autoApply {
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func trimArg(args map[string]any, key string) string {
	value, _ := getString(args, key)
	return strings.TrimSpace(value)
}
