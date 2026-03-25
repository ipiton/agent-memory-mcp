package steward

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// VerificationMethod describes how a memory was verified.
type VerificationMethod string

const (
	VerifyManual       VerificationMethod = "manual"
	VerifySourceCheck  VerificationMethod = "source_check"
	VerifyRepoScan     VerificationMethod = "repo_scan"
	VerifyAgentVerified VerificationMethod = "agent_verified"
)

// VerificationStatus tracks the outcome of a verification.
type VerificationStatus string

const (
	StatusVerified          VerificationStatus = "verified"
	StatusVerificationFailed VerificationStatus = "verification_failed"
	StatusNeedsUpdate       VerificationStatus = "needs_update"
	StatusUnverified        VerificationStatus = "unverified"
)

// VerifyParams holds parameters for verify_entry.
type VerifyParams struct {
	MemoryID string
	Method   VerificationMethod
	Status   VerificationStatus
	Note     string
}

// VerifyEntry marks a memory as verified (or failed verification) by updating its metadata.
func (s *Service) VerifyEntry(ctx context.Context, params VerifyParams) error {
	if params.MemoryID == "" {
		return fmt.Errorf("memory_id is required")
	}

	now := time.Now().UTC()
	meta := map[string]string{
		memory.MetadataLastVerifiedAt:     now.Format(time.RFC3339),
		memory.MetadataVerifiedBy:         string(params.Method),
		memory.MetadataVerificationMethod: string(params.Method),
		memory.MetadataVerificationStatus: string(params.Status),
	}
	if params.Note != "" {
		meta["verification_note"] = params.Note
	}

	return s.store.Update(ctx, params.MemoryID, memory.Update{Metadata: meta})
}

// Urgency levels for verification candidates.
type Urgency string

const (
	UrgencyHigh   Urgency = "high"
	UrgencyMedium Urgency = "medium"
	UrgencyLow    Urgency = "low"
)

// VerificationCandidate is a memory that needs verification.
type VerificationCandidate struct {
	MemoryID       string    `json:"memory_id"`
	Title          string    `json:"title"`
	Type           string    `json:"type"`
	Entity         string    `json:"entity,omitempty"`
	LastVerifiedAt time.Time `json:"last_verified_at"`
	AgeDays        int       `json:"age_days"`
	Reason         string    `json:"reason"`
	Urgency        Urgency   `json:"urgency"`
	SuggestedAction string  `json:"suggested_action"`
}

// VerificationCandidatesParams configures the verification candidates query.
type VerificationCandidatesParams struct {
	Limit      int
	Scope      string // "all", "canonical", "decisions", "runbooks"
	MinAgeDays int
	Context    string
	Service    string
}

// VerificationCandidates returns memories that need verification, ranked by urgency.
func (s *Service) VerificationCandidates(ctx context.Context, params VerificationCandidatesParams) ([]VerificationCandidate, error) {
	if params.Limit <= 0 {
		params.Limit = 20
	}

	active, _, err := loadActiveMemories(ctx, s.store, params.Context, params.Service)
	if err != nil {
		return nil, fmt.Errorf("steward: list memories: %w", err)
	}

	now := time.Now().UTC()
	staleDays := s.policy.EffectiveStaleDays()

	var candidates []VerificationCandidate

	for _, m := range active {
		// Scope filter.
		entity := memory.EngineeringTypeOf(m)
		isCanonical := memory.IsCanonicalMemory(m)
		switch params.Scope {
		case "canonical":
			if !isCanonical {
				continue
			}
		case "decisions":
			if entity != memory.EngineeringTypeDecision {
				continue
			}
		case "runbooks":
			if entity != memory.EngineeringTypeRunbook {
				continue
			}
		}

		verified := memory.LastVerifiedAt(m)
		ageDays := int(now.Sub(verified).Hours() / 24)
		if params.MinAgeDays > 0 && ageDays < params.MinAgeDays {
			continue
		}

		// Determine reason and urgency.
		vStatus := verificationStatusOf(m)
		reason, urgency, action := classifyVerificationNeed(m, vStatus, ageDays, staleDays, isCanonical)
		if reason == "" {
			continue
		}

		title := displayTitle(m, 60)

		candidates = append(candidates, VerificationCandidate{
			MemoryID:        m.ID,
			Title:           title,
			Type:            string(m.Type),
			Entity:          string(entity),
			LastVerifiedAt:  verified,
			AgeDays:         ageDays,
			Reason:          reason,
			Urgency:         urgency,
			SuggestedAction: action,
		})
	}

	// Sort by urgency (high first), then by age (oldest first).
	sort.Slice(candidates, func(i, j int) bool {
		if urgencyRank(candidates[i].Urgency) != urgencyRank(candidates[j].Urgency) {
			return urgencyRank(candidates[i].Urgency) < urgencyRank(candidates[j].Urgency)
		}
		return candidates[i].AgeDays > candidates[j].AgeDays
	})

	if len(candidates) > params.Limit {
		candidates = candidates[:params.Limit]
	}
	return candidates, nil
}

func verificationStatusOf(m *memory.Memory) VerificationStatus {
	if m == nil || len(m.Metadata) == 0 {
		return StatusUnverified
	}
	if s := m.Metadata[memory.MetadataVerificationStatus]; s != "" {
		return VerificationStatus(s)
	}
	return StatusUnverified
}

func classifyVerificationNeed(m *memory.Memory, vStatus VerificationStatus, ageDays, staleDays int, isCanonical bool) (reason string, urgency Urgency, action string) {
	switch {
	case vStatus == StatusVerificationFailed:
		return "verification_failed", UrgencyHigh, "investigate"
	case vStatus == StatusNeedsUpdate:
		return "needs_update", UrgencyHigh, "update"
	case isCanonical && ageDays > staleDays*2:
		return "stale_canonical", UrgencyHigh, "verify"
	case vStatus == StatusUnverified && isCanonical:
		return "unverified_canonical", UrgencyHigh, "verify"
	case ageDays > staleDays:
		return "stale", UrgencyMedium, "verify"
	case vStatus == StatusUnverified && m.Importance >= 0.8:
		return "never_verified_important", UrgencyMedium, "verify"
	}
	return "", "", ""
}

func urgencyRank(u Urgency) int {
	switch u {
	case UrgencyHigh:
		return 0
	case UrgencyMedium:
		return 1
	case UrgencyLow:
		return 2
	default:
		return 3
	}
}
