package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Event is the JSON object Claude Code writes to a hook's stdin.
//
// The CLI used to take that stdin as the session summary verbatim, which is
// why 1102 SessionEnd records in a live bank held
// `{"session_id":…,"hook_event_name":"SessionEnd"}` instead of anything that
// happened in the session. The transcript is not on stdin — only a path to it.
type Event struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Name           string `json:"hook_event_name"`
	Reason         string `json:"reason"`  // SessionEnd: clear, logout, other…
	Trigger        string `json:"trigger"` // PreCompact: manual or auto
}

// ParseEvent reads a hook event. It rejects anything that is not a JSON object
// rather than guessing: a caller that passes prose here has the older contract
// in mind, and silently storing that prose is the bug this type exists to fix.
func ParseEvent(data []byte) (Event, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Event{}, errors.New("empty hook event on stdin")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return Event{}, fmt.Errorf("hook event must be a JSON object, got %.40q — drop --hook-event to pass a summary as text", trimmed)
	}
	var event Event
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return Event{}, fmt.Errorf("parsing hook event: %w", err)
	}
	return event, nil
}

// ContextLabel returns the context to file the record under, leaving an
// explicit --context untouched.
//
// The session id is part of the generated label on purpose: sessionclose folds
// records that share a context within six hours into the first one, and
// shouldReplaceContent needs a 0.95 lexical overlap to update the text. Two
// different sessions never reach that, so a project-level label would keep the
// first session's content and quietly drop the second one's.
func (e Event) ContextLabel(explicit string) string {
	if label := strings.TrimSpace(explicit); label != "" {
		return label
	}
	slug := "session"
	if cwd := strings.TrimSpace(e.CWD); cwd != "" {
		if base := filepath.Base(filepath.Clean(cwd)); base != "." && base != string(filepath.Separator) {
			slug = base
		}
	}
	id := strings.TrimSpace(e.SessionID)
	if id == "" {
		return slug
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return slug + "-" + id
}

// Transcript summarisation limits. The transcript of a long session runs to
// megabytes of tool calls and thinking blocks; what belongs in the bank is the
// conversation, recent-first, bounded so a single record stays readable.
const (
	maxTranscriptMessages   = 40
	maxTranscriptPerMessage = 1500
	maxTranscriptTotal      = 12000
	maxTranscriptLine       = 16 << 20 // one JSONL record, buffered
)

type transcriptRecord struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Attachment  json.RawMessage `json:"attachment"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// SummarizeTranscript turns a Claude Code JSONL transcript into the text a
// session record should carry: the last messages of the conversation, without
// tool calls, thinking blocks or injected context.
//
// A missing or unreadable path is an error the caller decides about — for a
// hook, "no transcript" means "nothing to capture", not "fail the session".
func SummarizeTranscript(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLine)

	messages := make([]string, 0, maxTranscriptMessages)
	for scanner.Scan() {
		var record transcriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue // a partially written line is not a reason to lose the rest
		}
		label, ok := speakerLabel(record.Type)
		if !ok || record.IsSidechain || len(record.Attachment) > 0 {
			continue
		}
		body := strings.TrimSpace(textBlocks(record.Message.Content))
		if body == "" || isInjectedContext(body) {
			continue
		}
		if len(body) > maxTranscriptPerMessage {
			body = body[:maxTranscriptPerMessage]
		}
		messages = append(messages, label+": "+body)
	}
	// A truncated read still carries what it managed to parse; the caller sees
	// the error and can decide whether a partial capture is worth keeping.
	err = scanner.Err()

	if len(messages) > maxTranscriptMessages {
		messages = messages[len(messages)-maxTranscriptMessages:]
	}
	summary := strings.Join(messages, "\n\n")
	if len(summary) > maxTranscriptTotal {
		summary = summary[len(summary)-maxTranscriptTotal:]
	}
	return summary, err
}

func speakerLabel(recordType string) (string, bool) {
	switch recordType {
	case "user":
		return "User", true
	case "assistant":
		return "Assistant", true
	default:
		return "", false
	}
}

// textBlocks keeps only the text a human would recognise as the conversation.
// A list-shaped content field also carries tool_use payloads and thinking
// blocks with base64 signatures — storing those would make the record larger
// than the session and no more informative.
func textBlocks(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// isInjectedContext drops the blocks the harness adds to a turn — they are not
// something the user or the agent said, and they repeat verbatim across
// sessions, which is exactly the shape the dedup filter exists to reject.
func isInjectedContext(body string) bool {
	return strings.HasPrefix(body, "<corpus-search>") ||
		strings.HasPrefix(body, "<system-reminder>") ||
		strings.HasPrefix(body, "<command-name>") ||
		strings.HasPrefix(body, "<local-command-stdout>")
}
