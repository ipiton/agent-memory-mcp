package main

import (
	"os"
	"path/filepath"
	"testing"
)

// silenceStdout redirects os.Stdout for the duration of fn so the CLI
// handlers' human-readable output does not pollute test logs.
func silenceStdout(t *testing.T, fn func()) {
	t.Helper()
	orig := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	os.Stdout = devnull
	defer func() {
		os.Stdout = orig
		_ = devnull.Close()
	}()
	fn()
}

// TestCLIHandlersReturnErrorsInsteadOfExiting is the H11 payoff: subcommand
// handlers return errors (so deferred SQLite cleanup runs) instead of calling
// os.Exit, which makes them exercisable end-to-end in-process. Before this
// refactor none of these calls could be driven from a test — os.Exit killed
// the test binary.
func TestCLIHandlersReturnErrorsInsteadOfExiting(t *testing.T) {
	t.Setenv("MCP_MEMORY_DB_PATH", filepath.Join(t.TempDir(), "mem.db"))

	var storeErr, listErr, statsErr error
	silenceStdout(t, func() {
		storeErr = runStore([]string{"-content", "e2e test memory", "-type", "semantic"})
		listErr = runList([]string{"-json"})
		statsErr = runStats([]string{"-json"})
	})
	if storeErr != nil {
		t.Fatalf("runStore: %v", storeErr)
	}
	if listErr != nil {
		t.Fatalf("runList: %v", listErr)
	}
	if statsErr != nil {
		t.Fatalf("runStats: %v", statsErr)
	}
}

// TestRunStoreMissingContentReturnsError covers the negative path: a missing
// -content flag used to os.Exit(1); now it surfaces as a returned error the
// caller (main) can handle at a single exit point.
func TestRunStoreMissingContentReturnsError(t *testing.T) {
	t.Setenv("MCP_MEMORY_DB_PATH", filepath.Join(t.TempDir(), "mem.db"))

	var err error
	silenceStdout(t, func() {
		err = runStore([]string{})
	})
	if err == nil {
		t.Fatal("runStore with no content: expected error, got nil")
	}
}
