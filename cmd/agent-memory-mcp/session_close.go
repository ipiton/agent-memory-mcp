package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/hooks"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

type sessionCommandBehavior struct {
	dryRun           bool
	saveRaw          bool
	autoApplyLowRisk bool
}

func runCloseSession(args []string) error {
	return runSessionCommand("close-session", args, sessionCommandBehavior{
		dryRun: true,
	})
}

func runReviewSession(args []string) error {
	return runSessionCommand("review-session", args, sessionCommandBehavior{
		dryRun: true,
	})
}

func runAcceptSession(args []string) error {
	return runSessionCommand("accept-session", args, sessionCommandBehavior{
		dryRun:           false,
		saveRaw:          true,
		autoApplyLowRisk: true,
	})
}

func runSessionCommand(name string, args []string, behavior sessionCommandBehavior) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	summary := fs.String("summary", "", "Session summary text")
	stdin := fs.Bool("stdin", false, "Read session summary from stdin")
	mode := fs.String("mode", "", "Optional session mode: coding, incident, migration, research, cleanup")
	ctx := fs.String("context", "", "Project or task context")
	service := fs.String("service", "", "Service or component name")
	tags := fs.String("tags", "", "Comma-separated tags")
	metadata := fs.String("metadata", "", "Comma-separated key=value metadata")
	startedAt := fs.String("started-at", "", "Optional RFC3339 session start time")
	endedAt := fs.String("ended-at", "", "Optional RFC3339 session end time")
	rawOnly := fs.Bool("raw-only", false, "Only save the raw session summary")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	summaryText, err := readSessionSummary(*summary, *stdin, fs.Args())
	if err != nil {
		return err
	}

	modeValue, err := parseSessionModeValue(*mode)
	if err != nil {
		return err
	}
	metadataMap, err := parseSessionMetadataFlag(*metadata)
	if err != nil {
		return err
	}
	started, err := parseSessionTimeFlag(*startedAt, "started-at")
	if err != nil {
		return err
	}
	ended, err := parseSessionTimeFlag(*endedAt, "ended-at")
	if err != nil {
		return err
	}

	serviceLayer := sessionclose.New(store)
	sessionSummary := memory.SessionSummary{
		Mode:      modeValue,
		Context:   strings.TrimSpace(*ctx),
		Service:   strings.TrimSpace(*service),
		Summary:   summaryText,
		StartedAt: started,
		EndedAt:   ended,
		Tags:      parseCSVTags(*tags),
		Metadata:  metadataMap,
	}

	if *rawOnly {
		rawID, err := serviceLayer.SaveRawSummary(context.Background(), sessionSummary)
		// T122: the write boundary refuses bodies with no knowledge. Report it as
		// a skip with the reason, the way the checkpoint path already does — an
		// exit code would read as "the store is broken".
		if errors.Is(err, sessionclose.ErrNoKnowledge) {
			if *jsonOut {
				return printJSON(map[string]any{
					"raw_only": true,
					"skipped":  "no_knowledge",
					"mode":     modeValue,
				})
			}
			fmt.Println("Raw session summary skipped: body is an activity log, not knowledge")
			return nil
		}
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(map[string]any{
				"raw_only":          true,
				"raw_summary_saved": rawID,
				"mode":              modeValue,
			})
		}
		fmt.Printf("Raw session summary saved as memory %s\n", rawID)
		return nil
	}

	result, err := serviceLayer.Analyze(context.Background(), sessionclose.AnalyzeRequest{
		Summary:          sessionSummary,
		DryRun:           behavior.dryRun,
		SaveRaw:          behavior.saveRaw,
		AutoApplyLowRisk: behavior.autoApplyLowRisk,
	})
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(result)
	}
	fmt.Println(sessionclose.FormatAnalysis(result))
	return nil
}

// sessionInput is what a capture command needs from its invocation: the text
// to store and the context to file it under. Captured is false when a hook
// event arrived but pointed at nothing readable — a session with no transcript
// is nothing to save, not a failure to report to the hook runner.
type sessionInput struct {
	Summary  string
	Context  string
	Event    hooks.Event
	Captured bool
}

// resolveSessionInput reads either a hook event or a summary, depending on
// --hook-event.
//
// The flag exists because the two are not the same thing and used to be
// treated as one: Claude Code writes an event object to a hook's stdin, and
// `--stdin` stored that object as the session text. Every SessionEnd record
// written that way holds the event's metadata and nothing about the session.
// Under --hook-event the transcript named by the event is read instead, and
// the context label carries the session id so two sessions in one evening do
// not consolidate into one record.
func resolveSessionInput(hookEvent bool, summary string, useStdin bool, contextFlag string, positional []string) (sessionInput, error) {
	if !hookEvent {
		text, err := readSessionSummary(summary, useStdin, positional)
		if err != nil {
			return sessionInput{}, err
		}
		return sessionInput{Summary: text, Context: strings.TrimSpace(contextFlag), Captured: true}, nil
	}

	data, err := readStdin()
	if err != nil {
		return sessionInput{}, fmt.Errorf("reading hook event from stdin: %w", err)
	}
	event, err := hooks.ParseEvent(data)
	if err != nil {
		return sessionInput{}, err
	}

	input := sessionInput{Context: event.ContextLabel(contextFlag), Event: event}
	if strings.TrimSpace(event.TranscriptPath) == "" {
		return input, nil
	}
	text, err := hooks.SummarizeTranscript(event.TranscriptPath)
	if err != nil {
		// A transcript that cannot be read is worth saying out loud — the hook
		// still exits 0, because failing the session over it helps nobody.
		fmt.Fprintf(os.Stderr, "hook event: %v\n", err)
		if strings.TrimSpace(text) == "" {
			return input, nil
		}
	}
	if strings.TrimSpace(text) == "" {
		return input, nil
	}
	input.Summary = text
	input.Captured = true
	return input, nil
}

func readSessionSummary(summary string, useStdin bool, positional []string) (string, error) {
	summary = strings.TrimSpace(summary)
	if summary != "" {
		return summary, nil
	}
	if useStdin {
		data, err := readStdin()
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		summary = strings.TrimSpace(string(data))
		if summary != "" {
			return summary, nil
		}
	}
	if len(positional) > 0 {
		summary = strings.TrimSpace(strings.Join(positional, " "))
		if summary != "" {
			return summary, nil
		}
	}
	return "", errors.New("session summary is required via -summary, -stdin, or positional text")
}

func parseSessionModeValue(value string) (memory.SessionMode, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	return memory.ValidateSessionMode(value, "")
}

func parseSessionMetadataFlag(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	result := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("invalid metadata entry %q (expected key=value)", part)
		}
		result[key] = value
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func parseSessionTimeFlag(value string, flagName string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", flagName, err)
	}
	return parsed, nil
}

func parseCSVTags(raw string) []string {
	return userio.ParseCSVTags(raw)
}
