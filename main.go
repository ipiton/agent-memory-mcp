package main

import (
	"fmt"
	"os"
)

func main() {
	// Note: We no longer redirect stderr to /dev/null because we use file logger
	// File logger writes to a separate file, so MCP responses remain clean

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	guard, err := NewPathGuard(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build path guard: %v\n", err)
		os.Exit(1)
	}

	server := NewMCPServer(cfg, guard)

	// Debug output
	fmt.Fprintf(os.Stderr, "MCP Config: HTTPMode=%s, HTTPPort=%d, RAGEnabled=%t\n", cfg.HTTPMode, cfg.HTTPPort, cfg.RAGEnabled)

	if cfg.HTTPMode == "http" {
		if err := RunHTTP(server); err != nil {
			fmt.Fprintf(os.Stderr, "mcp http server stopped: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := RunStdio(server); err != nil {
			fmt.Fprintf(os.Stderr, "mcp server stopped: %v\n", err)
			os.Exit(1)
		}
	}
}
