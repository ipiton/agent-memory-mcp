package sessionclose

import (
	"context"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/textfmt"
)

// T186: one Claude Code session left dozens of "Session close" records. A
// session in the app lives for days and fires SessionEnd every time it is left,
// so a summary arriving later than consolidationWindow started a new record;
// every PreCompact added a checkpoint of its own. With the session id on the
// record the window protects nothing for the same session, so records that
// carry an id are folded by id alone — one summary and one checkpoint per
// session.
//
// The text cannot simply be replaced: the hook hands over the transcript tail
// (40 messages, 12 KB), not the whole conversation, so the latest capture does
// not cover the earlier ones. Replacing would drop the start of a long session
// and keeping the first would drop everything after it. Instead the new tail is
// appended from the point where the stored text ends, and the record is capped
// by trimming its middle: the opening and the latest part stay.

const (
	// maxSessionRecordBytes bounds a folded session record. The encoder embeds
	// only the opening of a long body anyway; past this the record stops being
	// readable as one note.
	maxSessionRecordBytes = 32000
	// sessionRecordHeadBytes is how much of the opening survives a trim.
	sessionRecordHeadBytes = 8000
	// overlapParagraphs is how many trailing paragraphs of the stored text must
	// reappear, in order, in the new tail to count as the overlap. One is not
	// enough: a short closing line ("Assistant: Готово.") repeats.
	overlapParagraphs = 3

	sessionGapMarker  = "[…]"
	sessionTrimMarker = "[… middle of the session trimmed …]"
)

// findSessionRecord returns the most recent record of the same agent session
// and kind (terminal summary or checkpoint) in mem.Context, at any age.
func (s *Service) findSessionRecord(ctx context.Context, mem *memory.Memory, sessionID string, checkpoint bool) (*memory.Memory, error) {
	slug := strings.TrimSpace(mem.Context)
	if slug == "" {
		return nil, nil
	}
	existing, err := s.store.List(ctx, memory.Filters{Context: slug, Type: memory.TypeEpisodic}, 0)
	if err != nil {
		return nil, err
	}
	var target *memory.Memory
	for _, m := range existing {
		if m == nil || strings.TrimSpace(m.Metadata[memory.MetadataAgentSessionID]) != sessionID {
			continue
		}
		if checkpoint && !memory.IsSessionCheckpointMemory(m) || !checkpoint && !isTerminalRecord(m) {
			continue
		}
		if target == nil || m.CreatedAt.After(target.CreatedAt) {
			target = m
		}
	}
	return target, nil
}

// foldSessionTail appends to current the part of tail that current does not
// already end with. When no overlap is found the whole tail is appended after
// a gap marker: a duplicated paragraph is cheaper than a lost one.
func foldSessionTail(current, tail string) string {
	current = strings.TrimSpace(current)
	tail = strings.TrimSpace(tail)
	switch {
	case tail == "" || strings.Contains(current, tail):
		return current
	case current == "":
		return capSessionRecord(tail)
	}

	have := splitParagraphs(current)
	incoming := splitParagraphs(tail)
	n := min(overlapParagraphs, len(have))
	want := have[len(have)-n:]
	for i := 0; i+n <= len(incoming); i++ {
		if equalParagraphs(incoming[i:i+n], want) {
			rest := incoming[i+n:]
			if len(rest) == 0 {
				return current
			}
			return capSessionRecord(current + "\n\n" + strings.Join(rest, "\n\n"))
		}
	}
	// The tail may start inside the last few stored paragraphs: its opening is
	// then a suffix of the stored text. Take the longest such overlap.
	for j := min(len(have), len(incoming)); j > 0; j-- {
		if equalParagraphs(incoming[:j], have[len(have)-j:]) {
			if j == len(incoming) {
				return current
			}
			return capSessionRecord(current + "\n\n" + strings.Join(incoming[j:], "\n\n"))
		}
	}
	return capSessionRecord(current + "\n\n" + sessionGapMarker + "\n\n" + tail)
}

// capSessionRecord keeps the opening and the latest part of an over-long
// record, cutting the middle on paragraph boundaries where it can. The head
// stops before a previous trim marker, so markers do not pile up.
func capSessionRecord(s string) string {
	if len(s) <= maxSessionRecordBytes {
		return s
	}
	head := s[:textfmt.AlignRuneStart(s, sessionRecordHeadBytes)]
	if i := strings.Index(head, sessionTrimMarker); i >= 0 {
		head = strings.TrimSpace(head[:i])
	} else if i := strings.LastIndex(head, "\n\n"); i > 0 {
		head = head[:i]
	}
	budget := maxSessionRecordBytes - len(head) - len(sessionTrimMarker) - 4
	tail := s[textfmt.AlignRuneStart(s, len(s)-budget):]
	if i := strings.Index(tail, "\n\n"); i >= 0 && i < len(tail)-2 {
		tail = tail[i+2:]
	}
	return head + "\n\n" + sessionTrimMarker + "\n\n" + tail
}

func splitParagraphs(s string) []string {
	parts := strings.Split(s, "\n\n")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func equalParagraphs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
