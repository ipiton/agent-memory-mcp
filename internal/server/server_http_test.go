package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func TestHTTPAuthRequired(t *testing.T) {
	s := newTestServer(t, "secret-token-123")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	// No auth header → 401
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Wrong token → 401
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", rec.Code)
	}

	// Correct token → 200
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-123")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", rec.Code)
	}
}

func TestHTTPNoAuthConfigured(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth config, got %d", rec.Code)
	}
}

func TestValidateHTTPExposureAllowsLoopbackWithoutAuth(t *testing.T) {
	cfg := config.Config{
		HTTP: config.HTTPConfig{Mode: "http", Host: "127.0.0.1", Port: 18080},
	}

	if err := validateHTTPExposure(cfg); err != nil {
		t.Fatalf("validateHTTPExposure returned error: %v", err)
	}
}

func TestValidateHTTPExposureRejectsNonLoopbackWithoutAuth(t *testing.T) {
	cfg := config.Config{
		HTTP: config.HTTPConfig{Mode: "http", Host: "0.0.0.0", Port: 18080},
	}

	err := validateHTTPExposure(cfg)
	if err == nil {
		t.Fatal("expected validation error for non-loopback host without auth")
	}
	if !strings.Contains(err.Error(), "MCP_HTTP_AUTH_TOKEN") {
		t.Fatalf("error = %q, want MCP_HTTP_AUTH_TOKEN hint", err.Error())
	}
}

func TestValidateHTTPExposureAllowsExplicitInsecureOverride(t *testing.T) {
	cfg := config.Config{
		HTTP: config.HTTPConfig{Mode: "http", Host: "0.0.0.0", Port: 18080, InsecureAllowUnauthenticated: true},
	}

	if err := validateHTTPExposure(cfg); err != nil {
		t.Fatalf("validateHTTPExposure returned error: %v", err)
	}
}

func TestHTTPMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPGetSSEStreamOpens(t *testing.T) {
	s := newTestServer(t, "")
	srv := httptest.NewServer(buildMux(s))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	want := ": stream open\n\n"
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("first frame = %q, want %q", string(buf), want)
	}
}

func TestHTTPGetSSERequiresAuth(t *testing.T) {
	s := newTestServer(t, "secret")
	srv := httptest.NewServer(buildMux(s))
	defer srv.Close()

	get := func(token string) *http.Response {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/mcp", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Accept", "text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /mcp: %v", err)
		}
		return resp
	}

	respNoToken := get("")
	defer func() { _ = respNoToken.Body.Close() }()
	if respNoToken.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", respNoToken.StatusCode)
	}

	respWrong := get("nope")
	defer func() { _ = respWrong.Body.Close() }()
	if respWrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", respWrong.StatusCode)
	}

	respOK := get("secret")
	defer func() { _ = respOK.Body.Close() }()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", respOK.StatusCode)
	}
}

func TestHTTPGetSSEReturnsOnClientCancel(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()

	// Let the handler write the initial frame and block in the keepalive loop.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancel")
	}

	if !strings.HasPrefix(rec.Body.String(), ": stream open") {
		t.Fatalf("expected stream-open frame, got %q", rec.Body.String())
	}
}

func TestHTTPOptions(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", rec.Code)
	}

	// CORS header should deny by default
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "" {
		t.Fatalf("expected empty CORS origin, got %q", origin)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := buildMux(s)

	// Health requires auth when token is configured
	reqNoAuth := httptest.NewRequest(http.MethodGet, "/health", nil)
	recNoAuth := httptest.NewRecorder()
	mux.ServeHTTP(recNoAuth, reqNoAuth)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", recNoAuth.Code)
	}

	// Health with valid auth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", result["status"])
	}

	// Health without token configured should work without auth
	sNoToken := newTestServer(t, "")
	muxNoToken := buildMux(sNoToken)
	reqOpen := httptest.NewRequest(http.MethodGet, "/health", nil)
	recOpen := httptest.NewRecorder()
	muxNoToken.ServeHTTP(recOpen, reqOpen)
	if recOpen.Code != http.StatusOK {
		t.Fatalf("expected 200 without token, got %d", recOpen.Code)
	}
}
