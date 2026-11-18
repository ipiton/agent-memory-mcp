package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// stdioClient talks line-delimited JSON-RPC to serveStdio over pipes.
type stdioClient struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Reader
}

func newStdioClient(t *testing.T, start *StdioStart) *stdioClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveStdio(start, inR, outW) }()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
		<-done
	})
	return &stdioClient{t: t, in: inW, out: bufio.NewReader(outR)}
}

func (c *stdioClient) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// recv fails the test if no answer arrives in time: a client that would have
// hung on the start must show up as a failure, not as a stuck test run.
func (c *stdioClient) recv() map[string]any {
	c.t.Helper()
	type result struct {
		line string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		line, err := c.out.ReadString('\n')
		got <- result{line, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			c.t.Fatalf("recv: %v", r.err)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(r.line), &msg); err != nil {
			c.t.Fatalf("decode %q: %v", r.line, err)
		}
		return msg
	case <-time.After(5 * time.Second):
		c.t.Fatal("no answer while the server is starting")
		return nil
	}
}

func toolNames(t *testing.T, msg map[string]any) map[string]bool {
	t.Helper()
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", msg)
	}
	names := map[string]bool{}
	for _, item := range result["tools"].([]any) {
		names[item.(map[string]any)["name"].(string)] = true
	}
	return names
}

func stdioStartConfig() config.Config {
	var cfg config.Config
	cfg.OutputMode = "line"
	cfg.Memory.Enabled = true
	return cfg
}

// T175: the handshake and the tool list must not wait for the stores. A call
// that cannot be served yet says so, and works once the start is done.
func TestStdioAnswersWhileStarting(t *testing.T) {
	cfg := stdioStartConfig()
	release := make(chan struct{})
	built := newTestServer(t, "")
	start := startStdio(cfg, func() *MCPServer {
		<-release
		return built
	}, 0)
	client := newStdioClient(t, start)

	client.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"early-client"}}}`)
	init := client.recv()
	if init["result"].(map[string]any)["protocolVersion"] != protocolVersion {
		t.Fatalf("initialize: %v", init)
	}

	client.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	client.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	names := toolNames(t, client.recv())
	if !names["store_memory"] {
		t.Error("memory is enabled in the config, but store_memory is not listed")
	}
	if names["semantic_search"] {
		t.Error("RAG is disabled in the config, but semantic_search is listed")
	}

	client.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_stats","arguments":{}}}`)
	starting := client.recv()
	rpcErr, ok := starting["error"].(map[string]any)
	if !ok || !strings.Contains(rpcErr["message"].(string), "still starting") {
		t.Fatalf("call during start: %v", starting)
	}
	if data := rpcErr["data"].(map[string]any); data["status"] != "starting" {
		t.Errorf("error data: %v", data)
	}
	if start.Server() != nil {
		t.Error("Server() must be nil while starting")
	}

	close(release)
	<-start.ready
	if start.Server() != built {
		t.Error("Server() must return the finished server")
	}
	if len(start.early) != 0 {
		t.Errorf("early requests not replayed: %v", start.early)
	}

	client.send(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if got := toolNames(t, client.recv()); len(got) == 0 {
		t.Error("the finished server lists no tools")
	}
}

// A start that finishes within the wait is invisible to the client: its first
// call gets the real answer rather than the "starting" error.
func TestStdioCallWaitsForShortStart(t *testing.T) {
	release := make(chan struct{})
	built := newTestServer(t, "")
	start := startStdio(stdioStartConfig(), func() *MCPServer {
		<-release
		return built
	}, time.Minute)
	client := newStdioClient(t, start)

	client.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	time.AfterFunc(50*time.Millisecond, func() { close(release) })
	msg := client.recv()
	if msg["error"] != nil {
		t.Fatalf("call during a short start: %v", msg)
	}
	want := fmt.Sprint(len(built.availableToolDefs()))
	if got := fmt.Sprint(len(toolNames(t, msg))); got != want {
		t.Errorf("tools = %s, want the finished server's %s", got, want)
	}
}
