package sessionclose

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T139: review_session_changes and accept_session_changes recompute the plan
// from the summary on every call. The plan fingerprint lets accept apply
// exactly the plan the reviewer saw: it covers every knowledge action and the
// state of each action's target, so a bank that changed between the two calls
// (parallel sessions, steward, Forge share it) is refused instead of applied.

// ErrPlanChanged reports that the plan recomputed at accept time differs from
// the reviewed one. Nothing is written when it is returned.
var ErrPlanChanged = errors.New("session plan changed since review")

// ErrInvalidDecision reports a per-action decision the plan cannot honour.
// Nothing is written when it is returned.
var ErrInvalidDecision = errors.New("invalid session action decision")

// Decision is the reviewer's verdict on one planned knowledge action.
type Decision string

const (
	DecisionUpdate       Decision = "update"
	DecisionMerge        Decision = "merge"
	DecisionKeepExisting Decision = "keep_existing"
	DecisionReject       Decision = "reject"
)

// assignActionIDs gives every knowledge action an id derived from its
// candidate and position, so the same summary yields the same ids on review
// and on accept. raw_only has no id: it is not a knowledge decision.
func assignActionIDs(actions []CandidateAction) {
	for i := range actions {
		action := &actions[i]
		if action.Kind == ActionRawOnly || action.Candidate == nil {
			continue
		}
		action.ID = "act_" + shortHash(fmt.Sprintf("%d\x00%s\x00%s\x00%s",
			i, action.EngineeringType, action.Candidate.Title, action.Candidate.Content))
	}
}

// planFingerprint hashes what accept would act on: each knowledge action's id,
// kind, handling and target with the target's version. Session timestamps and
// the raw summary are left out — they default to "now" and differ per call.
func planFingerprint(actions []CandidateAction) string {
	var b strings.Builder
	for _, action := range actions {
		if action.Kind == ActionRawOnly {
			continue
		}
		fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%s\n",
			action.ID, action.Kind, action.Handling, action.TargetMemoryID, action.TargetVersion)
	}
	return "plan_" + shortHash(b.String())
}

// memoryVersion hashes the fields a session-close update or merge reads and
// rewrites. It is content-based rather than UpdatedAt-based so it does not
// depend on timestamp precision across the cache and SQLite.
func memoryVersion(m *memory.Memory) string {
	if m == nil {
		return ""
	}
	tags := append([]string(nil), m.Tags...)
	sort.Strings(tags)
	keys := make([]string, 0, len(m.Metadata))
	for key := range m.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%.6f\x00%s\x00", m.Title, m.Content, m.Type, m.Context, m.Importance, strings.Join(tags, "\x01"))
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%s\x01", key, m.Metadata[key])
	}
	return shortHash(b.String())
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// validateDecisions checks every decision against the plan before anything is
// written: the id must name a knowledge action, the verdict must be known, and
// update/merge need a target to write into.
func validateDecisions(actions []CandidateAction, decisions map[string]Decision) error {
	byID := make(map[string]*CandidateAction, len(actions))
	for i := range actions {
		if actions[i].ID != "" {
			byID[actions[i].ID] = &actions[i]
		}
	}
	ids := make([]string, 0, len(decisions))
	for id := range decisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		action, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: unknown action id %q", ErrInvalidDecision, id)
		}
		switch decisions[id] {
		case DecisionUpdate, DecisionMerge:
			if strings.TrimSpace(action.TargetMemoryID) == "" {
				return fmt.Errorf("%w: action %s has no target memory for %q", ErrInvalidDecision, id, decisions[id])
			}
		case DecisionKeepExisting, DecisionReject:
		default:
			return fmt.Errorf("%w: action %s: unknown decision %q (want update, merge, keep_existing or reject)", ErrInvalidDecision, id, decisions[id])
		}
	}
	return nil
}
