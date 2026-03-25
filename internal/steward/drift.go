package steward

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// DriftType classifies the kind of drift detected.
type DriftType string

const (
	DriftSourceChanged  DriftType = "source_changed"
	DriftSourceMissing  DriftType = "source_missing"
	DriftStaleUnverified DriftType = "stale_unverified"
)

// DriftFinding represents a single drift detected between memory and source.
type DriftFinding struct {
	MemoryID        string    `json:"memory_id"`
	Title           string    `json:"title"`
	DriftType       DriftType `json:"drift_type"`
	Evidence        string    `json:"evidence"`
	Confidence      float64   `json:"confidence"`
	SuggestedAction string    `json:"suggested_action"`
}

// DriftResult is the result of a drift scan.
type DriftResult struct {
	Scanned             int              `json:"scanned"`
	Findings            []DriftFinding   `json:"findings"`
	UnreachableSources  []UnreachableRef `json:"unreachable_sources,omitempty"`
}

// UnreachableRef records a memory that references a path that can't be checked.
type UnreachableRef struct {
	MemoryID   string `json:"memory_id"`
	SourcePath string `json:"source_path"`
	Reason     string `json:"reason"`
}

// DriftScanParams configures a drift scan.
type DriftScanParams struct {
	Scope    string // "all", "canonical", "decisions", "runbooks"
	Context  string
	Service  string
	RootPath string // project root for file existence checks
}

// DriftScan compares memory entries against their sources of truth.
// It checks for:
// - Referenced files that have changed since last verification
// - Referenced paths that no longer exist
// - Entries not verified within the stale threshold
func (s *Service) DriftScan(ctx context.Context, params DriftScanParams) (*DriftResult, error) {
	active, _, err := loadActiveMemories(ctx, s.store, params.Context, params.Service)
	if err != nil {
		return nil, fmt.Errorf("steward: drift scan list: %w", err)
	}

	now := time.Now().UTC()
	staleDays := s.policy.EffectiveStaleDays()
	staleThreshold := now.AddDate(0, 0, -staleDays)

	result := &DriftResult{}

	for _, m := range active {
		entity := memory.EngineeringTypeOf(m)
		isCanonical := memory.IsCanonicalMemory(m)
		if !matchesDriftScope(params.Scope, entity, isCanonical) {
			continue
		}

		result.Scanned++
		title := displayTitle(m, 60)

		verified := memory.LastVerifiedAt(m)

		// Check referenced file paths.
		if params.RootPath != "" {
			refs := extractPathRefs(m.Content)
			for _, ref := range refs {
				absPath := ref
				if !filepath.IsAbs(ref) {
					absPath = filepath.Join(params.RootPath, ref)
				}

				info, err := os.Stat(absPath)
				if err != nil {
					if os.IsNotExist(err) {
						result.Findings = append(result.Findings, DriftFinding{
							MemoryID:        m.ID,
							Title:           title,
							DriftType:       DriftSourceMissing,
							Evidence:        fmt.Sprintf("Referenced path does not exist: %s", ref),
							Confidence:      0.85,
							SuggestedAction: "mark_outdated",
						})
					} else {
						result.UnreachableSources = append(result.UnreachableSources, UnreachableRef{
							MemoryID:   m.ID,
							SourcePath: ref,
							Reason:     err.Error(),
						})
					}
					continue
				}

				// Check if source was modified after last verification.
				if info.ModTime().After(verified) {
					result.Findings = append(result.Findings, DriftFinding{
						MemoryID:        m.ID,
						Title:           title,
						DriftType:       DriftSourceChanged,
						Evidence:        fmt.Sprintf("File %s modified %s (memory verified %s)", ref, info.ModTime().Format(time.DateOnly), verified.Format(time.DateOnly)),
						Confidence:      0.70,
						SuggestedAction: "verify",
					})
				}
			}
		}

		// Check stale unverified.
		if verified.Before(staleThreshold) {
			confidence := 0.60
			action := "verify"
			if isCanonical {
				confidence = 0.80
				action = "verify"
			}
			result.Findings = append(result.Findings, DriftFinding{
				MemoryID:        m.ID,
				Title:           title,
				DriftType:       DriftStaleUnverified,
				Evidence:        fmt.Sprintf("Last verified %d days ago (threshold: %d)", int(now.Sub(verified).Hours()/24), staleDays),
				Confidence:      confidence,
				SuggestedAction: action,
			})
		}
	}

	return result, nil
}

func matchesDriftScope(scope string, entity memory.EngineeringType, isCanonical bool) bool {
	switch scope {
	case "canonical":
		return isCanonical
	case "decisions":
		return entity == memory.EngineeringTypeDecision
	case "runbooks":
		return entity == memory.EngineeringTypeRunbook
	case "", "all":
		return true
	default:
		return true
	}
}

// extractPathRefs extracts file path references from memory content.
// Looks for common patterns like `/path/to/file`, `./path`, `docs/file.md`, etc.
var pathRefPattern = regexp.MustCompile(`(?:^|[\s\x60"'(])([./][\w._/-]+\.(?:go|md|yaml|yml|json|toml|sh|sql|tf|hcl|py|js|ts|conf|cfg|env))`)

func extractPathRefs(content string) []string {
	matches := pathRefPattern.FindAllStringSubmatch(content, -1)
	seen := make(map[string]struct{})
	var refs []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		ref := strings.TrimSpace(m[1])
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
