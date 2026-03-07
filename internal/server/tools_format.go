package server

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) formatWorkflowSearch(title string, query string, context string, service string, memoryResults []*memory.SearchResult, docResults *rag.SearchResponse, memoryHeading string, docHeading string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s for '%s'\n", title, query)
	if context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", context)
	}
	if service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", service)
	}
	buf.WriteString("\n")

	sections := 0
	if len(memoryResults) > 0 {
		buf.WriteString(s.formatWorkflowMemoryResults(memoryHeading, memoryResults))
		sections++
	}
	if docResults != nil && len(docResults.Results) > 0 {
		if sections > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(s.formatWorkflowDocResults(docHeading, docResults))
		sections++
	}

	if sections == 0 {
		buf.WriteString("No matching operational context found.")
	}

	return buf.String()
}

func formatConflictReport(items []memory.ConflictReportItem, context string, service string) string {
	var buf bytes.Buffer
	buf.WriteString("Memory conflicts report\n")
	if context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", context)
	}
	if service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", service)
	}
	buf.WriteString("\n")

	if len(items) == 0 {
		buf.WriteString("No conflict groups found.")
		return buf.String()
	}

	for i, item := range items {
		fmt.Fprintf(&buf, "%d. %s (%s)\n", i+1, item.Subject, item.Reason)
		if item.Entity != "" {
			fmt.Fprintf(&buf, "   Entity: %s\n", item.Entity)
		}
		if item.Service != "" {
			fmt.Fprintf(&buf, "   Service: %s\n", item.Service)
		}
		if item.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", item.Context)
		}
		if len(item.Statuses) > 0 {
			fmt.Fprintf(&buf, "   Statuses: %s\n", strings.Join(item.Statuses, ", "))
		}
		if len(item.Tags) > 0 {
			fmt.Fprintf(&buf, "   Tags: %s\n", strings.Join(item.Tags, ", "))
		}
		if len(item.MemoryIDs) > 0 {
			fmt.Fprintf(&buf, "   Memory IDs: %s\n", strings.Join(item.MemoryIDs, ", "))
		}
		if len(item.Titles) > 0 {
			fmt.Fprintf(&buf, "   Titles: %s\n", strings.Join(item.Titles, " | "))
		}
		fmt.Fprintf(&buf, "   Suggested action: %s\n", item.SuggestedAction)
	}

	return buf.String()
}

func (s *MCPServer) formatProjectContextSummary(context string, focus string, service string, canonicalEntries []*memory.CanonicalKnowledge, decisions []*memory.Memory, runbooks []*memory.Memory, incidents []*memory.Memory, relatedDocs *rag.SearchResponse) string {
	var buf bytes.Buffer
	buf.WriteString("Project context summary\n")
	if context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", context)
	}
	if service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", service)
	}
	if focus != "" {
		fmt.Fprintf(&buf, "Focus: %s\n", focus)
	}
	buf.WriteString("\n")

	sections := 0
	if len(canonicalEntries) > 0 {
		buf.WriteString(formatCanonicalKnowledgeList(canonicalEntries, context, service))
		sections++
	}
	if len(decisions) > 0 {
		if sections > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(s.formatWorkflowMemoryList("Recent decisions", decisions))
		sections++
	}
	if len(runbooks) > 0 {
		if sections > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(s.formatWorkflowMemoryList("Runbooks", runbooks))
		sections++
	}
	if len(incidents) > 0 {
		if sections > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(s.formatWorkflowMemoryList("Incidents and postmortems", incidents))
		sections++
	}
	if relatedDocs != nil && len(relatedDocs.Results) > 0 {
		if sections > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(s.formatWorkflowDocResults("Related docs", relatedDocs))
		sections++
	}

	if sections == 0 {
		buf.WriteString("No stored project context found.")
	}

	return buf.String()
}

func (s *MCPServer) formatProjectBankView(result *memory.ProjectBankViewResult) string {
	if result == nil {
		return "Project bank view unavailable."
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Project bank view: %s\n", result.Title)
	if result.Context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", result.Context)
	}
	if result.Service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", result.Service)
	}
	if result.Status != "" {
		fmt.Fprintf(&buf, "Status filter: %s\n", result.Status)
	}
	if result.Owner != "" {
		fmt.Fprintf(&buf, "Owner filter: %s\n", result.Owner)
	}
	if len(result.Tags) > 0 {
		fmt.Fprintf(&buf, "Tags filter: %s\n", strings.Join(result.Tags, ", "))
	}
	buf.WriteString("\n")

	if result.TotalCount > 0 {
		fmt.Fprintf(&buf, "Visible items: %d\n", result.TotalCount)
	}
	if len(result.EntityCounts) > 0 {
		buf.WriteString("Entity counts:\n")
		for _, key := range []string{"decisions", "runbooks", "incidents", "caveats", "migrations"} {
			if count := result.EntityCounts[key]; count > 0 {
				fmt.Fprintf(&buf, "- %s: %d\n", key, count)
			}
		}
		buf.WriteString("\n")
	}

	for idx, section := range result.Sections {
		if idx > 0 {
			buf.WriteString("\n")
		}
		fmt.Fprintf(&buf, "%s (%d):\n", section.Title, len(section.Items))
		if section.Description != "" {
			fmt.Fprintf(&buf, "%s\n", section.Description)
		}
		if len(section.Items) == 0 {
			buf.WriteString("No items found.\n")
			continue
		}
		for i, item := range section.Items {
			fmt.Fprintf(&buf, "%d. %s\n", i+1, item.Title)
			if item.Entity != "" {
				fmt.Fprintf(&buf, "   Entity: %s\n", item.Entity)
			}
			if item.Service != "" {
				fmt.Fprintf(&buf, "   Service: %s\n", item.Service)
			}
			if item.Context != "" {
				fmt.Fprintf(&buf, "   Context: %s\n", item.Context)
			}
			if item.Lifecycle != "" {
				fmt.Fprintf(&buf, "   Lifecycle: %s\n", item.Lifecycle)
			}
			if item.KnowledgeLayer != "" {
				fmt.Fprintf(&buf, "   Layer: %s\n", item.KnowledgeLayer)
			}
			if item.Status != "" {
				fmt.Fprintf(&buf, "   Status: %s\n", item.Status)
			}
			if item.Owner != "" {
				fmt.Fprintf(&buf, "   Owner: %s\n", item.Owner)
			}
			if item.SessionMode != "" {
				fmt.Fprintf(&buf, "   Session mode: %s\n", item.SessionMode)
			}
			if item.ReviewRequired {
				buf.WriteString("   Review: required\n")
			}
			if len(item.Tags) > 0 {
				fmt.Fprintf(&buf, "   Tags: %s\n", strings.Join(item.Tags, ", "))
			}
			if item.Trust != nil {
				fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(item.Trust))
			}
			if !item.LastVerifiedAt.IsZero() {
				fmt.Fprintf(&buf, "   Last verified: %s\n", item.LastVerifiedAt.UTC().Format(time.RFC3339))
			}
			if !item.UpdatedAt.IsZero() {
				fmt.Fprintf(&buf, "   Updated: %s\n", item.UpdatedAt.UTC().Format(time.RFC3339))
			}
			if item.Summary != "" {
				fmt.Fprintf(&buf, "   %s\n", truncateText(item.Summary, 220))
			}
		}
	}

	return strings.TrimSpace(buf.String())
}

func formatCanonicalKnowledgeList(entries []*memory.CanonicalKnowledge, context string, service string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Canonical knowledge (%d):\n", len(entries))
	for i, entry := range entries {
		fmt.Fprintf(&buf, "%d. %s\n", i+1, entry.Title)
		if entry.Entity != "" {
			fmt.Fprintf(&buf, "   Entity: %s\n", entry.Entity)
		}
		if entry.Service != "" {
			fmt.Fprintf(&buf, "   Service: %s\n", entry.Service)
		}
		if entry.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", entry.Context)
		}
		if len(entry.Tags) > 0 {
			fmt.Fprintf(&buf, "   Tags: %v\n", entry.Tags)
		}
		if entry.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(entry.Trust))
		}
		fmt.Fprintf(&buf, "   %s\n", truncateText(entry.Summary, 220))
	}
	return buf.String()
}

func formatCanonicalKnowledgeRecall(query string, results []*memory.CanonicalSearchResult, context string, service string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Canonical knowledge for '%s'\n", query)
	if context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", context)
	}
	if service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", service)
	}
	buf.WriteString("\n")
	if len(results) == 0 {
		buf.WriteString("No canonical knowledge found.")
		return buf.String()
	}
	for i, result := range results {
		entry := result.Entry
		fmt.Fprintf(&buf, "%d. %s (relevance: %.2f)\n", i+1, entry.Title, result.Score)
		if entry.Entity != "" {
			fmt.Fprintf(&buf, "   Entity: %s\n", entry.Entity)
		}
		if entry.Service != "" {
			fmt.Fprintf(&buf, "   Service: %s\n", entry.Service)
		}
		if entry.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(entry.Trust))
		}
		fmt.Fprintf(&buf, "   %s\n", truncateText(entry.Summary, 220))
	}
	return buf.String()
}

func (s *MCPServer) formatWorkflowMemoryResults(heading string, results []*memory.SearchResult) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s (%d):\n", heading, len(results))
	for i, result := range results {
		mem := result.Memory
		fmt.Fprintf(&buf, "%d. %s (relevance: %.2f)\n", i+1, memory.DisplayTitle(mem, 50), result.Score)
		if mem.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", mem.Context)
		}
		if len(mem.Tags) > 0 {
			fmt.Fprintf(&buf, "   Tags: %v\n", mem.Tags)
		}
		if result.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(result.Trust))
		}
		fmt.Fprintf(&buf, "   %s\n", truncateText(mem.Content, 220))
	}
	return buf.String()
}

func (s *MCPServer) formatWorkflowMemoryList(heading string, memories []*memory.Memory) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s (%d):\n", heading, len(memories))
	for i, mem := range memories {
		fmt.Fprintf(&buf, "%d. %s\n", i+1, memory.DisplayTitle(mem, 50))
		if mem.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", mem.Context)
		}
		if len(mem.Tags) > 0 {
			fmt.Fprintf(&buf, "   Tags: %v\n", mem.Tags)
		}
		fmt.Fprintf(&buf, "   %s\n", truncateText(mem.Content, 220))
	}
	return buf.String()
}

func (s *MCPServer) formatWorkflowDocResults(heading string, results *rag.SearchResponse) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s (%d):\n", heading, len(results.Results))
	for i, result := range results.Results {
		fmt.Fprintf(&buf, "%d. %s (relevance: %.2f)\n", i+1, result.Title, result.Score)
		if result.SourceType != "" {
			fmt.Fprintf(&buf, "   Source type: %s\n", result.SourceType)
		}
		if result.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(result.Trust))
		}
		fmt.Fprintf(&buf, "   Path: %s\n", result.Path)
		fmt.Fprintf(&buf, "   %s\n", result.Snippet)
	}
	if results.Debug != nil {
		fmt.Fprintf(&buf, "   Explain: %s\n", s.formatSearchDebug(results.Debug))
	}
	return buf.String()
}

func truncateText(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}

// Memory result formatting

func (s *MCPServer) formatMemoryResults(query string, results []*memory.SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No memories found for '%s'.", query)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Found %d memories for '%s':\n\n", len(results), query)

	for i, r := range results {
		m := r.Memory
		fmt.Fprintf(&buf, "%d. **%s** (relevance: %.2f)\n", i+1, memory.DisplayTitle(m, 50), r.Score)
		fmt.Fprintf(&buf, "   ID: `%s`\n", m.ID)
		fmt.Fprintf(&buf, "   Type: %s\n", formatMemoryType(m.Type))

		if m.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", m.Context)
		}
		if len(m.Tags) > 0 {
			fmt.Fprintf(&buf, "   Tags: %v\n", m.Tags)
		}
		if r.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(r.Trust))
		}

		snippet := m.Content
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		fmt.Fprintf(&buf, "   Content: %s\n", snippet)
		fmt.Fprintf(&buf, "   Importance: %.1f | Access count: %d\n\n", m.Importance, m.AccessCount)
	}

	return buf.String()
}

func (s *MCPServer) formatMemoryList(memories []*memory.Memory) string {
	if len(memories) == 0 {
		return "No memories found."
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Memories (%d):\n\n", len(memories))

	for i, m := range memories {
		fmt.Fprintf(&buf, "%d. **%s**\n", i+1, memory.DisplayTitle(m, 50))
		fmt.Fprintf(&buf, "   ID: `%s`\n", m.ID)
		fmt.Fprintf(&buf, "   Type: %s | Importance: %.1f\n", formatMemoryType(m.Type), m.Importance)

		if m.Context != "" {
			fmt.Fprintf(&buf, "   Context: %s\n", m.Context)
		}

		snippet := m.Content
		if len(snippet) > 150 {
			snippet = snippet[:150] + "..."
		}
		fmt.Fprintf(&buf, "   %s\n", snippet)
		fmt.Fprintf(&buf, "   Created: %s\n\n", m.CreatedAt.Format("2006-01-02 15:04"))
	}

	return buf.String()
}

func formatMemoryType(t memory.Type) string {
	switch t {
	case memory.TypeEpisodic:
		return "Episodic"
	case memory.TypeSemantic:
		return "Semantic"
	case memory.TypeProcedural:
		return "Procedural"
	case memory.TypeWorking:
		return "Working"
	default:
		return string(t)
	}
}
