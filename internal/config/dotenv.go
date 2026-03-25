package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const dotenvFileName = ".env"

// configAppName is the directory name used in XDG and Homebrew config paths.
const configAppName = "agent-memory-mcp"

// resolvedConfigPath stores the first config file path that was actually loaded.
var resolvedConfigPath string

// ConfigFilePath returns the path to the config file that was loaded during startup.
// Returns empty string if no config file was found.
func ConfigFilePath() string {
	return resolvedConfigPath
}

// loadDotEnvFiles loads .env files from a chain of known paths.
// If explicitPath is non-empty, only that file is loaded (no chain).
// Each file only fills in values not already set in the environment.
func loadDotEnvFiles(explicitPath string) error {
	resolvedConfigPath = ""

	if explicitPath != "" {
		resolvedConfigPath = explicitPath
		return loadDotEnv(explicitPath)
	}

	// Chain: CWD .env → XDG config → Homebrew prefix
	paths := collectConfigPaths()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if resolvedConfigPath == "" {
			resolvedConfigPath = p
		}
		if err := loadDotEnv(p); err != nil {
			return err
		}
	}
	return nil
}

// collectConfigPaths returns the ordered list of config file paths to try.
func collectConfigPaths() []string {
	var paths []string

	// 1. CWD/.env
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, dotenvFileName))
	}

	// 2. XDG_CONFIG_HOME/agent-memory-mcp/config.env
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdg = filepath.Join(home, ".config")
		}
	}
	if xdg != "" {
		paths = append(paths, filepath.Join(xdg, configAppName, "config.env"))
	}

	// 3. Homebrew prefix
	if prefix := os.Getenv("HOMEBREW_PREFIX"); prefix != "" {
		paths = append(paths, filepath.Join(prefix, "etc", configAppName, "config.env"))
	} else {
		// Try common Homebrew defaults
		for _, p := range []string{"/opt/homebrew", "/usr/local"} {
			paths = append(paths, filepath.Join(p, "etc", configAppName, "config.env"))
		}
	}

	return paths
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		if current := strings.TrimSpace(os.Getenv(key)); current != "" {
			continue
		}

		if err := os.Setenv(key, parseDotEnvValue(value)); err != nil {
			return fmt.Errorf("set %s from %s: %w", key, path, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func parseDotEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return value
	}

	if value[0] == '"' && value[len(value)-1] == '"' {
		unquoted, err := strconv.Unquote(value)
		if err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}

	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}

	return value
}
