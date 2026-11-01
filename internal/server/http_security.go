package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"go.uber.org/zap"
)

func httpListenAddr(cfg config.Config) string {
	host := strings.TrimSpace(cfg.HTTP.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(cfg.HTTP.Port))
}

func validateHTTPExposure(cfg config.Config) error {
	if cfg.HTTP.Mode != "http" {
		return nil
	}

	host := strings.TrimSpace(cfg.HTTP.Host)
	if host == "" {
		host = "127.0.0.1"
	}

	if isLoopbackHost(host) || strings.TrimSpace(cfg.HTTP.AuthToken) != "" || cfg.HTTP.InsecureAllowUnauthenticated {
		return nil
	}

	return fmt.Errorf(
		"refusing to start HTTP server on non-loopback host %q without MCP_HTTP_AUTH_TOKEN; set MCP_HTTP_AUTH_TOKEN or explicitly opt in with MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true",
		host,
	)
}

func logHTTPExposurePolicy(server *MCPServer) {
	if server.fileLogger == nil {
		return
	}

	host := strings.TrimSpace(server.config.HTTP.Host)
	if host == "" {
		host = "127.0.0.1"
	}

	switch {
	case strings.TrimSpace(server.config.HTTP.AuthToken) != "":
		server.fileLogger.Info("HTTP authentication enabled",
			zap.String("host", host),
			zap.Int("port", server.config.HTTP.Port),
		)
	case server.config.HTTP.InsecureAllowUnauthenticated:
		server.fileLogger.Warn("HTTP server explicitly allows unauthenticated access on configured host",
			zap.String("host", host),
			zap.Int("port", server.config.HTTP.Port),
		)
	case isLoopbackHost(host):
		server.fileLogger.Info("HTTP server running without auth on loopback-only bind",
			zap.String("host", host),
			zap.Int("port", server.config.HTTP.Port),
		)
	}
}

// requireTrustedOrigin (T147) rejects requests a browser was tricked into
// sending, as the MCP transport spec requires. A page on a name rebound to
// 127.0.0.1 is same-origin with the server, so CORS does not stop it: on an
// open loopback bind the Host must be a loopback name too. With a token that
// page has nothing to send, and a reverse proxy may forward a public Host.
// An Origin, when present, must name the Host; non-browser clients send none.
func requireTrustedOrigin(server *MCPServer, next http.Handler) http.Handler {
	checkHost := isLoopbackHost(server.config.HTTP.Host) && strings.TrimSpace(server.config.HTTP.AuthToken) == ""

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reason := ""
		origin := r.Header.Get("Origin")
		if checkHost && !isLoopbackHost((&url.URL{Host: r.Host}).Hostname()) {
			reason = "Host is not a loopback name"
		} else if origin != "" && !originMatchesHost(origin, r.Host) {
			reason = "Origin does not match Host"
		}
		if reason == "" {
			next.ServeHTTP(w, r)
			return
		}

		if server.fileLogger != nil {
			server.fileLogger.Warn("HTTP request rejected",
				zap.String("reason", reason),
				zap.String("host", r.Host),
				zap.String("origin", origin),
				zap.String("path", r.URL.Path),
			)
		}
		http.Error(w, "Forbidden: "+reason, http.StatusForbidden)
	})
}

// originMatchesHost compares names without ports: a proxy forwarding $host drops
// the port the browser kept. It also rejects the opaque Origin "null" (sandboxed
// frames, file:// pages), which parses to an empty name.
func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Hostname() != "" && strings.EqualFold(parsed.Hostname(), (&url.URL{Host: host}).Hostname())
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")

	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
