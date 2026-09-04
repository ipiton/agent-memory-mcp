package config

import (
	"bytes"
	"flag"
	"os"
	"testing"
)

// withCommandLine swaps the global FlagSet and os.Args for one Load() call, so
// a test can drive the flag surface without the go-test flags (-test.v and
// friends) reaching it. Returns the FlagSet Load registered into, so the test
// can inspect the help text it would print.
func withCommandLine(t *testing.T, argv ...string) *flag.FlagSet {
	t.Helper()
	origFS, origArgs := flag.CommandLine, os.Args
	fs := flag.NewFlagSet(argv[0], flag.ContinueOnError)
	fs.SetOutput(new(bytes.Buffer))
	flag.CommandLine = fs
	os.Args = argv
	t.Cleanup(func() {
		flag.CommandLine = origFS
		os.Args = origArgs
	})
	return fs
}

// TestLoadHelpDoesNotLeakThisMachinesEnv (T129) is the defect verbatim: the
// published `--help` advertised `-root string … (default "/Users/vit/Sema")`,
// a path that came from the installed instance's config.env through the flag's
// own default. General help must describe the flag, not this machine.
func TestLoadHelpDoesNotLeakThisMachinesEnv(t *testing.T) {
	hermeticDotEnv(t)
	envRoot := t.TempDir()
	t.Setenv("MCP_ROOT", envRoot)
	t.Setenv("MCP_STATS_PATH", "/private/stats/from/this/machine.jsonl")

	fs := withCommandLine(t, "agent-memory-mcp")
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var help bytes.Buffer
	fs.SetOutput(&help)
	fs.PrintDefaults()
	for _, leaked := range []string{envRoot, "/private/stats/from/this/machine.jsonl"} {
		if bytes.Contains(help.Bytes(), []byte(leaked)) {
			t.Fatalf("help text carries a value from this machine's environment (%q):\n%s", leaked, help.String())
		}
	}
}

// TestLoadFlagPrecedence (T129) pins the ordering the fix had to preserve while
// emptying the defaults: an unset flag must not overwrite the env value with
// its own zero, and a passed flag must still win.
func TestLoadFlagPrecedence(t *testing.T) {
	hermeticDotEnv(t)
	envRoot := t.TempDir()
	flagRoot := t.TempDir()
	t.Setenv("MCP_ROOT", envRoot)
	t.Setenv("MCP_MAX_SEARCH_RESULTS", "42")

	t.Run("env survives when no flag is passed", func(t *testing.T) {
		withCommandLine(t, "agent-memory-mcp")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RootPath != envRoot {
			t.Errorf("RootPath = %q, want the env value %q", cfg.RootPath, envRoot)
		}
		if cfg.MaxSearchResults != 42 {
			t.Errorf("MaxSearchResults = %d, want the env value 42", cfg.MaxSearchResults)
		}
	})

	t.Run("flag wins when passed", func(t *testing.T) {
		withCommandLine(t, "agent-memory-mcp", "-root", flagRoot, "-max-search-results", "7")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RootPath != flagRoot {
			t.Errorf("RootPath = %q, want the flag value %q", cfg.RootPath, flagRoot)
		}
		if cfg.MaxSearchResults != 7 {
			t.Errorf("MaxSearchResults = %d, want the flag value 7", cfg.MaxSearchResults)
		}
	})
}
