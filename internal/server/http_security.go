package server

import (
	"fmt"
	"net"
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
