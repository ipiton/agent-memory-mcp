package memory

import "strings"

// Activity-log classification (T85, extended by T122).
//
// A session record whose every line is a "- <Action>: <pointer>" bullet is a
// journal of what the agent did, not knowledge about anything. Two consumers
// need the same verdict on opposite sides of the import graph: the hooks write
// boundary refuses to persist such a body, and recall excludes the ones already
// in the bank from semantic selection. `hooks` imports `memory`, so the shared
// predicate lives here and `hooks` delegates to it.

// activityLogLabels are the bullet labels whose value is a pointer — a memory
// id, a path, a query, a view name — and never a standalone statement. The
// whitelist is deliberate: a blacklist ("body lacks the word Stored")
// over-matched real task reports 12:1 on live data, 117 chore logs against 1362
// genuine closure reports (T85).
//
// "incident investigation" is absent here and present in
// selectionOnlyActivityLabels below. The two decisions are not the same
// decision, and conflating them is what made this take two rounds — see that
// list for the argument.
//
// Deliberately absent from both:
//
//   - "stored memory" / "updated knowledge" — the value is the first 220
//     characters of another record's content (buildActivityLine, see
//     internal/server/session_activity.go). Measured on the live bank: 1019 of
//     1273 such bullets (80.0%) reproduce the opening of a record that exists
//     separately. That is a truncated copy competing with its own original — a
//     different defect with a different fix, and one a whitelist cannot address
//     without dropping genuine reports along the way.
var activityLogLabels = map[string]struct{}{
	"document search":     {},
	"memory recall":       {},
	"repo search":         {},
	"inspected file":      {},
	"merged duplicates":   {},
	"marked outdated":     {},
	"project bank review": {},
	"promoted canonical":  {}, // value is a memory id
	"runbook search":      {}, // value is a query
	"listed repo path":    {}, // value is a path
}

// selectionOnlyActivityLabels are labels that make a body unfit to *rank*, but
// not unfit to *keep*. The two questions have different costs and therefore
// different answers: refusing a write destroys whatever the record held,
// while skipping it in semantic selection costs nothing — the record stays in
// the bank, in List, and in the unprocessed-summary queue.
//
// "incident investigation" is the case that forced the split. T85 kept it out
// of the write guard because a hand-written "- Incident investigation: root
// cause was the missing DB index; added it and latency recovered" is a real
// report, and that caution is right and stays. But the line auto-capture
// produces is trimArg(args, "query") — the search query, not a finding — and
// its vector is therefore built from the question, which is exactly the
// mechanism T84 named: the log out-ranks the answer to it. All 26 summaries in
// the bank built solely from this bullet carry queries ("ratchet порог поднят
// метрика quality gate регрессия"); none is a writeup. They were the records
// still sitting above the answer after T122 shipped.
var selectionOnlyActivityLabels = map[string]struct{}{
	"incident investigation": {},
}

// IsActivityLogOnly reports whether body consists solely of activity bullets
// that carry no knowledge — the verdict the write boundary refuses on.
func IsActivityLogOnly(body string) bool {
	return allBulletsWithin(body, activityLogLabels)
}

// IsActivityLogForSelection is the same verdict widened by
// selectionOnlyActivityLabels: bodies that must not compete in semantic
// ranking, but must still be stored and listed.
func IsActivityLogForSelection(body string) bool {
	return allBulletsWithin(body, activityLogLabels, selectionOnlyActivityLabels)
}

// allBulletsWithin reports whether every non-blank line of body is a labelled
// activity bullet whose label appears in one of the given sets. Blank lines are
// ignored; a single unrecognised line makes the body real content.
func allBulletsWithin(body string, sets ...map[string]struct{}) bool {
	sawBullet := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, ok := activityBulletLabel(line)
		if !ok {
			return false
		}
		known := false
		for _, set := range sets {
			if _, in := set[label]; in {
				known = true
				break
			}
		}
		if !known {
			return false
		}
		sawBullet = true
	}
	return sawBullet
}

// activityBulletLabel extracts the lowercased label of a "- Label: value" bullet
// line. It requires a list-bullet marker ("- " or "* ") so a prose sentence
// containing a colon ("Fixed the search: it now works") is not treated as a
// labelled bullet. Returns ok=false when the line is not such a bullet.
func activityBulletLabel(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "- ")
	if !ok {
		rest, ok = strings.CutPrefix(line, "* ")
	}
	if !ok {
		return "", false
	}
	idx := strings.IndexByte(rest, ':')
	if idx <= 0 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(rest[:idx])), true
}
