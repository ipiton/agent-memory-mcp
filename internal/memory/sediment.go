// Package memory — sedimentation layer (T48).
//
// Introduces a sediment-layer dimension on memories: surface → episodic →
// semantic → character. This is ADDITIVE and does not replace the `type`
// field. Layer governs retrieval priority: `character` always surfaces,
// `semantic` by match, `episodic` top-K, `surface` only when filters.Context
// matches the memory's Context.
//
// Transition rules in this file are pure functions — they emit proposals
// (SedimentTransition) without performing any I/O. Application is done by
// PromoteSediment/DemoteSediment (see write.go) and the sediment-cycle job.
//
// Degraded-mode caveat: T48 lacks the production access-pattern history needed
// to tune thresholds (7d / 30d / access_count). Defaults are conservative;
// future tuning should instrument cycle outputs over 2+ months.
package memory

import (
	"strconv"
	"strings"
	"time"
)

// SedimentLayer is the four-level ladder that governs retrieval priority.
type SedimentLayer string

const (
	// LayerSurface holds short-lived session/task scratch state. It surfaces
	// only when filters.Context matches the memory's Context (i.e. the agent
	// is inside the originating session/task).
	LayerSurface SedimentLayer = "surface"

	// LayerEpisodic holds events/actions that have aged past the session but
	// have not consolidated into generalized knowledge.
	LayerEpisodic SedimentLayer = "episodic"

	// LayerSemantic holds consolidated, generalized knowledge.
	LayerSemantic SedimentLayer = "semantic"

	// LayerCharacter holds load-bearing canonical knowledge that should
	// always be available regardless of query match (like identity facts).
	LayerCharacter SedimentLayer = "character"
)

// Default sediment layer applied to new memories before any transition.
const DefaultSedimentLayer = LayerSurface

// MetadataReferencedByCount is the optional metadata key that counts how many
// other memories reference this one. When it crosses SedimentPolicy.SemanticToCharacterRefs
// the memory becomes a non-auto promotion candidate to character.
const MetadataReferencedByCount = "referenced_by_count"

// Sediment metadata keys for review-queue items produced by the cycle.
const (
	MetadataReviewSource         = "review_source"
	MetadataReviewTargetMemoryID = "review_target_memory_id"
	MetadataReviewTargetLayer    = "review_target_layer"

	ReviewSourceSedimentCycle = "sediment_cycle"
)

// Default sediment policy thresholds. Chosen conservatively given the T48
// degraded-mode caveat — no production access-pattern history was available
// at the time of implementation. Operators should revisit these once 2+
// months of cycle outputs are instrumented.
const (
	DefaultSurfaceToEpisodicAge    = 7 * 24 * time.Hour   // 7d
	DefaultEpisodicToSemanticAge   = 30 * 24 * time.Hour  // 30d
	DefaultSemanticToCharacterRefs = 20                   // referenced_by count
	DefaultCharacterDemotionAge    = 90 * 24 * time.Hour  // 90d
	DefaultEpisodicToSemanticMin   = 3                    // min access_count for episodic→semantic
	DefaultSurfaceToEpisodicMin    = 1                    // min access_count for surface→episodic
)

// IsValidSedimentLayer reports whether s is one of the four canonical layers.
// Exact match after NormalizeSedimentLayer.
func IsValidSedimentLayer(s string) bool {
	switch NormalizeSedimentLayer(s) {
	case LayerSurface, LayerEpisodic, LayerSemantic, LayerCharacter:
		return true
	default:
		return false
	}
}

// NormalizeSedimentLayer maps a stored string into the canonical enum.
// Empty/whitespace input returns the empty SedimentLayer (not a default) —
// callers decide whether to coerce to LayerSurface. Unknown values return "".
func NormalizeSedimentLayer(s string) SedimentLayer {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return ""
	case string(LayerSurface):
		return LayerSurface
	case string(LayerEpisodic):
		return LayerEpisodic
	case string(LayerSemantic):
		return LayerSemantic
	case string(LayerCharacter):
		return LayerCharacter
	default:
		return ""
	}
}

// SedimentTransition is a proposal, not an applied mutation. Score is rule-
// internal and used by the cycle to order work (e.g. oldest first).
type SedimentTransition struct {
	MemoryID string        `json:"memory_id"`
	From     SedimentLayer `json:"from"`
	To       SedimentLayer `json:"to"`
	Reason   string        `json:"reason"`
	// Auto==true: trivial transition applied directly by the cycle.
	// Auto==false: non-trivial; cycle routes to the review_queue_item pipeline.
	Auto  bool    `json:"auto"`
	Score float64 `json:"score,omitempty"`
}

// SedimentPolicy wires knobs for Decide. Zero-value fields are replaced by
// DefaultsApplied to keep call sites concise.
type SedimentPolicy struct {
	Now                     func() time.Time
	SurfaceToEpisodicAge    time.Duration
	EpisodicToSemanticAge   time.Duration
	SemanticToCharacterRefs int
	CharacterDemotionAge    time.Duration
	// EpisodicToSemanticMin is the minimum access_count required before an
	// aged episodic memory is proposed for semantic promotion.
	EpisodicToSemanticMin int
	// SurfaceToEpisodicMin is the minimum access_count required before an
	// aged surface memory is auto-promoted to episodic.
	SurfaceToEpisodicMin int
}

// DefaultsApplied returns a copy of policy with zero-value fields filled in.
func (p SedimentPolicy) DefaultsApplied() SedimentPolicy {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.SurfaceToEpisodicAge <= 0 {
		p.SurfaceToEpisodicAge = DefaultSurfaceToEpisodicAge
	}
	if p.EpisodicToSemanticAge <= 0 {
		p.EpisodicToSemanticAge = DefaultEpisodicToSemanticAge
	}
	if p.SemanticToCharacterRefs <= 0 {
		p.SemanticToCharacterRefs = DefaultSemanticToCharacterRefs
	}
	if p.CharacterDemotionAge <= 0 {
		p.CharacterDemotionAge = DefaultCharacterDemotionAge
	}
	if p.EpisodicToSemanticMin <= 0 {
		p.EpisodicToSemanticMin = DefaultEpisodicToSemanticMin
	}
	if p.SurfaceToEpisodicMin <= 0 {
		p.SurfaceToEpisodicMin = DefaultSurfaceToEpisodicMin
	}
	return p
}

// Decide returns a SedimentTransition proposal for m, or nil if no transition
// applies. Pure function — no I/O, no mutation. Uses policy.Now() for age
// calculations so tests can inject a fixed clock.
//
// Rules:
//   - surface → episodic : age >= SurfaceToEpisodicAge AND access_count >= SurfaceToEpisodicMin. AUTO.
//   - episodic → semantic: age >= EpisodicToSemanticAge AND access_count >= EpisodicToSemanticMin. NON-AUTO.
//   - semantic → character: referenced_by_count >= SemanticToCharacterRefs OR lifecycle_status == canonical. NON-AUTO.
//   - character → semantic: last access >= CharacterDemotionAge ago. NON-AUTO (demotion).
//   - otherwise: nil.
//
// Note: review-queue items themselves are skipped — they are workflow
// records, not candidates for layer transitions.
func Decide(m *Memory, policy SedimentPolicy) *SedimentTransition {
	if m == nil {
		return nil
	}
	if IsReviewQueueMemory(m) {
		return nil
	}
	policy = policy.DefaultsApplied()
	now := policy.Now()

	// Coerce stored layer. An empty layer means the memory pre-dates T48 and
	// the backfill hasn't run on it yet — treat as the default (surface) for
	// the purposes of proposing a transition, rather than blocking progress.
	current := NormalizeSedimentLayer(m.SedimentLayer)
	if current == "" {
		current = LayerSurface
	}

	switch current {
	case LayerSurface:
		age := now.Sub(m.CreatedAt)
		if age < policy.SurfaceToEpisodicAge {
			return nil
		}
		if m.AccessCount < policy.SurfaceToEpisodicMin {
			return nil
		}
		return &SedimentTransition{
			MemoryID: m.ID,
			From:     LayerSurface,
			To:       LayerEpisodic,
			Reason:   "aged-surface",
			Auto:     true,
			Score:    age.Hours(),
		}

	case LayerEpisodic:
		age := now.Sub(m.CreatedAt)
		if age < policy.EpisodicToSemanticAge {
			return nil
		}
		if m.AccessCount < policy.EpisodicToSemanticMin {
			return nil
		}
		return &SedimentTransition{
			MemoryID: m.ID,
			From:     LayerEpisodic,
			To:       LayerSemantic,
			Reason:   "aged-episodic",
			Auto:     false,
			Score:    age.Hours(),
		}

	case LayerSemantic:
		if LifecycleStatusOf(m) == LifecycleCanonical {
			return &SedimentTransition{
				MemoryID: m.ID,
				From:     LayerSemantic,
				To:       LayerCharacter,
				Reason:   "canonical-promotion",
				Auto:     false,
				Score:    1.0,
			}
		}
		refs := referencedByCount(m)
		if refs >= policy.SemanticToCharacterRefs {
			return &SedimentTransition{
				MemoryID: m.ID,
				From:     LayerSemantic,
				To:       LayerCharacter,
				Reason:   "canonical-promotion",
				Auto:     false,
				Score:    float64(refs),
			}
		}
		return nil

	case LayerCharacter:
		// Demotion only when the memory has *accessed_at* older than the
		// threshold. A never-accessed character entry retains AccessedAt from
		// CreatedAt (Store sets all three to Now()), so this is well-defined.
		if m.AccessedAt.IsZero() {
			return nil
		}
		age := now.Sub(m.AccessedAt)
		if age < policy.CharacterDemotionAge {
			return nil
		}
		return &SedimentTransition{
			MemoryID: m.ID,
			From:     LayerCharacter,
			To:       LayerSemantic,
			Reason:   "character-decay",
			Auto:     false,
			Score:    age.Hours(),
		}

	default:
		return nil
	}
}

// referencedByCount reads the optional referenced_by_count metadata as int.
// Missing/invalid values produce 0 (no-op for the rule).
func referencedByCount(m *Memory) int {
	if m == nil || len(m.Metadata) == 0 {
		return 0
	}
	raw := strings.TrimSpace(m.Metadata[MetadataReferencedByCount])
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// DemoteOneStep returns the layer immediately closer to surface. Used by
// DemoteSediment. Returns "" for LayerSurface (no further demotion).
func DemoteOneStep(current SedimentLayer) SedimentLayer {
	switch NormalizeSedimentLayer(string(current)) {
	case LayerCharacter:
		return LayerSemantic
	case LayerSemantic:
		return LayerEpisodic
	case LayerEpisodic:
		return LayerSurface
	default:
		return ""
	}
}

// BackfillSedimentLayer computes the initial sediment_layer value for an
// existing memory during schema migration. Mirrors the decision table in the
// T48 spec: working → surface, episodic → episodic, canonical → character,
// else → semantic.
func BackfillSedimentLayer(memType Type, metadata map[string]string) SedimentLayer {
	// Canonical knowledge goes straight to character.
	if len(metadata) > 0 {
		if metadataBool(metadata, "canonical") || strings.EqualFold(strings.TrimSpace(metadata[MetadataKnowledgeLayer]), "canonical") {
			return LayerCharacter
		}
		if strings.EqualFold(strings.TrimSpace(metadata[MetadataLifecycleStatus]), string(LifecycleCanonical)) {
			return LayerCharacter
		}
	}
	switch memType {
	case TypeWorking:
		return LayerSurface
	case TypeEpisodic:
		return LayerEpisodic
	default:
		return LayerSemantic
	}
}
