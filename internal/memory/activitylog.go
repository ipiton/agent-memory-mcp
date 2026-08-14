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
// Two labels are deliberately absent, and both absences cost something:
//
//   - "incident investigation" — a human bullet "- Incident investigation: root
//     cause was X, fixed by Y" reads as real knowledge (T85).
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

// IsActivityLogOnly reports whether body consists solely of activity bullets
// whose labels are all in activityLogLabels. Blank lines are ignored; a single
// unrecognised line makes the body real content and returns false.
func IsActivityLogOnly(body string) bool {
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
		if _, known := activityLogLabels[label]; !known {
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
