package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const toolsListBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func postMCP(t *testing.T, srv *httptest.Server, host, origin, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestHTTPLoopbackBindChecksHostAndOrigin(t *testing.T) {
	srv := httptest.NewServer(buildMux(newTestServer(t, "")))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	port := base.Port()

	tests := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"cli client, no Origin", "", "", http.StatusOK},
		{"localhost by name", "localhost:" + port, "", http.StatusOK},
		{"ipv6 loopback", "[::1]:" + port, "", http.StatusOK},
		{"console on its own origin", "", srv.URL, http.StatusOK},
		{"console via localhost", "localhost:" + port, "http://localhost:" + port, http.StatusOK},
		{"foreign Origin", "", "http://evil.example", http.StatusForbidden},
		{"opaque Origin", "", "null", http.StatusForbidden},
		{"unparseable Origin", "", "http://[bad", http.StatusForbidden},
		{"foreign Host", "evil.example:" + port, "", http.StatusForbidden},
		{"rebound name, same-origin page", "evil.example:" + port, "http://evil.example:" + port, http.StatusForbidden},
		{"subdomain of localhost", "evil.localhost:" + port, "http://evil.localhost:" + port, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postMCP(t, srv, tt.host, tt.origin, toolsListBody); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// The whole mux is behind the check, including what serves before auth.
func TestHTTPForeignHostRejectedOnEveryRoute(t *testing.T) {
	srv := httptest.NewServer(buildMux(newTestServer(t, "")))
	defer srv.Close()

	routes := []struct {
		method, path, accept string
	}{
		{http.MethodGet, "/console", ""},
		{http.MethodGet, "/health", ""},
		{http.MethodPost, "/console/api/query", ""},
		{http.MethodOptions, "/mcp", ""},
		{http.MethodGet, "/mcp", "text/event-stream"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, route.method, srv.URL+route.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Host = "evil.example"
			if route.accept != "" {
				req.Header.Set("Accept", route.accept)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

// The harm is the write, so the witness is the store, not the status code.
func TestHTTPRebindingPageCannotWriteMemory(t *testing.T) {
	s := newMemoryTestServer(t)
	srv := httptest.NewServer(buildMux(s))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"store_memory","arguments":{"content":"planted","type":"semantic"}}}`
	if got := postMCP(t, srv, "evil.example:18080", "http://evil.example:18080", body); got != http.StatusForbidden {
		t.Fatalf("rebound page: status = %d, want 403", got)
	}
	if n := s.memoryStore.Count(); n != 0 {
		t.Fatalf("rebound page stored %d memories, want 0", n)
	}

	if got := postMCP(t, srv, "", "", body); got != http.StatusOK {
		t.Fatalf("cli client: status = %d, want 200", got)
	}
	if n := s.memoryStore.Count(); n != 1 {
		t.Fatalf("cli client stored %d memories, want 1", n)
	}
}

// deploy/nginx binds the server to loopback and forwards `Host $host`, which also
// drops a non-default port from the Host but not from the browser's Origin.
func TestHTTPLoopbackBindWithTokenAcceptsProxiedHost(t *testing.T) {
	srv := httptest.NewServer(buildMux(newTestServer(t, "secret")))
	defer srv.Close()

	const public = "memory.example.com"
	tests := []struct {
		name, origin string
		want         int
	}{
		{"proxied, no token reaches auth", "", http.StatusUnauthorized},
		{"console through the proxy", "https://" + public + ":8443", http.StatusUnauthorized},
		{"foreign Origin stops before auth", "http://evil.example", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postMCP(t, srv, public, tt.origin, toolsListBody); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// A shared instance is reached by names the server cannot know, and its token
// is what protects it. Origin is still checked against whatever the Host is.
func TestHTTPNonLoopbackBindChecksOnlyOrigin(t *testing.T) {
	s := newTestServer(t, "")
	s.config.HTTP.Host = "0.0.0.0"
	srv := httptest.NewServer(buildMux(s))
	defer srv.Close()

	const service = "agent-memory.moving.svc:18080"
	tests := []struct {
		name, origin string
		want         int
	}{
		{"service name, no Origin", "", http.StatusOK},
		{"console on the service name", "http://" + service, http.StatusOK},
		{"foreign Origin", "http://evil.example", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postMCP(t, srv, service, tt.origin, toolsListBody); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}
