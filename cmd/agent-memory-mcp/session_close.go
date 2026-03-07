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
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

type sessionCommandBehavior struct {
	dryRun           bool
	saveRaw          bool
	autoApplyLowRisk bool
}

func runCloseSession(args []string) {
	runSessionCommand("close-session", args, sessionCommandBehavior{
		dryRun: true,
	})
}

func runReviewSession(args []string) {
	runSessionCommand("review-session", args, sessionCommandBehavior{
		dryRun: true,
	})
}

func runAcceptSession(args []string) {
	runSessionCommand("accept-session", args, sessionCommandBehavior{
		dryRun:           false,
		saveRaw:          true,
		autoApplyLowRisk: true,
	})
}

func runSessionCommand(name string, args []string, behavior sessionCommandBehavior) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
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
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		failSessionCommand(err)
	}

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		failSessionCommand(err)
	}
	defer cleanup()

	summaryText, err := readSessionSummary(*summary, *stdin, fs.Args())
	if err != nil {
		failSessionCommand(err)
	}

	modeValue, err := parseSessionModeValue(*mode)
	if err != nil {
		failSessionCommand(err)
	}
	metadataMap, err := parseSessionMetadataFlag(*metadata)
	if err != nil {
		failSessionCommand(err)
	}
	started, err := parseSessionTimeFlag(*startedAt, "started-at")
	if err != nil {
		failSessionCommand(err)
	}
	ended, err := parseSessionTimeFlag(*endedAt, "ended-at")
	if err != nil {
		failSessionCommand(err)
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
		if err != nil {
			failSessionCommand(err)
		}
		if *jsonOut {
			mustPrintJSON(map[string]any{
				"raw_only":          true,
				"raw_summary_saved": rawID,
				"mode":              modeValue,
			})
			return
		}
		fmt.Printf("Raw session summary saved as memory %s\n", rawID)
		return
	}

	result, err := serviceLayer.Analyze(context.Background(), sessionclose.AnalyzeRequest{
		Summary:          sessionSummary,
		DryRun:           behavior.dryRun,
		SaveRaw:          behavior.saveRaw,
		AutoApplyLowRisk: behavior.autoApplyLowRisk,
	})
	if err != nil {
		failSessionCommand(err)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}
	fmt.Println(sessionclose.FormatAnalysis(result))
}

func failSessionCommand(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
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
