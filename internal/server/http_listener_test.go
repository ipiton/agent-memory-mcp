package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func healthOf(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	return resp.StatusCode, body.Status
}

// T174: the port stayed closed for 11 minutes while the store loaded on a machine
// out of memory, which looks the same as a service that died.
func TestPortAnswersBeforeServerIsReady(t *testing.T) {
	l, err := ListenHTTP(config.Config{HTTP: config.HTTPConfig{Host: "127.0.0.1"}})
	if err != nil {
		t.Fatalf("ListenHTTP: %v", err)
	}
	url := "http://" + l.listener.Addr().String()

	if code, status := healthOf(t, url); code != http.StatusServiceUnavailable || status != "starting" {
		t.Fatalf("before ready: %d %q, want 503 starting", code, status)
	}

	s := newTestServer(t, "")
	s.config.HTTP.Mode = "http"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunHTTP(ctx, s, l) }()

	// RunHTTP swaps the handler on its way to the select, so poll for it.
	var code int
	var status string
	for range 200 {
		if code, status = healthOf(t, url); code == http.StatusOK {
			break
		}
	}
	if code != http.StatusOK || status != "ok" {
		t.Fatalf("after ready: %d %q, want 200 ok", code, status)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunHTTP: %v", err)
	}
}

func TestTakenPortFailsBeforeStartup(t *testing.T) {
	l, err := ListenHTTP(config.Config{HTTP: config.HTTPConfig{Host: "127.0.0.1"}})
	if err != nil {
		t.Fatalf("ListenHTTP: %v", err)
	}
	defer func() { _ = l.server.Close() }()
	port := l.listener.Addr().(*net.TCPAddr).Port

	if _, err := ListenHTTP(config.Config{HTTP: config.HTTPConfig{Host: "127.0.0.1", Port: port}}); err == nil {
		t.Fatal("ListenHTTP on a taken port succeeded")
	}
}
