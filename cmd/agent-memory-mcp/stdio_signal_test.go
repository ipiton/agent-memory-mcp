package main

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// T172: a client that signals without closing stdin must still get a clean
// exit. Exit code 0 is the witness that run() returned through its deferred
// srv.Shutdown: a process killed by the signal reports no exit code at all.
func TestServeStdioExitsOnSignals(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-memory-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	start := func(t *testing.T) (*exec.Cmd, io.WriteCloser) {
		t.Helper()
		cmd := exec.Command(bin, "serve")
		cmd.Dir = dir
		cmd.Env = []string{
			"HOME=" + dir,
			"MCP_HTTP_MODE=stdio",
			"MCP_RAG_ENABLED=false",
			"MCP_MEMORY_DB_PATH=" + filepath.Join(dir, "memories.db"),
			"MCP_LOG_PATH=" + filepath.Join(dir, "mcp.log"),
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		// The answer proves the server is in its read loop before the signal.
		if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 256)
		n, err := stdout.Read(buf)
		if err != nil || !strings.Contains(string(buf[:n]), `"id":1`) {
			t.Fatalf("no reply before the signal: %q, %v", buf[:n], err)
		}
		return cmd, stdin
	}
	waitExit := func(t *testing.T, cmd *exec.Cmd) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				t.Fatalf("not a clean exit: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("still running 10s later")
		}
	}

	for name, sig := range map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGINT": syscall.SIGINT} {
		t.Run(name+" with stdin open", func(t *testing.T) {
			cmd, stdin := start(t)
			defer stdin.Close()
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			waitExit(t, cmd)
		})
	}
	t.Run("stdin closed", func(t *testing.T) {
		cmd, stdin := start(t)
		_ = stdin.Close()
		waitExit(t, cmd)
	})
}
