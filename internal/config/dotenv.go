package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const dotenvFileName = ".env"

// configAppName is the directory name used in XDG and Homebrew config paths.
const configAppName = "agent-memory-mcp"

// configPathMu guards the package-level config-path globals (resolvedConfigPath
// and explicitConfigPath in config.go). They are written at startup but read at
// runtime from the SIGHUP handler and config-watcher goroutines via
// ConfigFilePath(); the lock makes that access race-free instead of relying on
// startup happens-before (Round 3 M14).
var configPathMu sync.RWMutex

// resolvedConfigPath stores the first config file path that was actually loaded.
var resolvedConfigPath string

// ConfigFilePath returns the path to the config file that was loaded during startup.
// Returns empty string if no config file was found.
func ConfigFilePath() string {
	configPathMu.RLock()
	defer configPathMu.RUnlock()
	return resolvedConfigPath
}

func setResolvedConfigPath(p string) {
	configPathMu.Lock()
	resolvedConfigPath = p
	configPathMu.Unlock()
}

// loadDotEnvFiles reads the .env chain and returns the merged values.
// If explicitPath is non-empty, only that file is read (no chain).
//
// T89 H5/M14: this used to push the values into the process environment with
// os.Setenv, skipping any key already present. On the first load that is the
// intended precedence — a real environment variable beats the file. On the
// SECOND load it was fatal: every key was now "already present" because the
// first load had set it, so a reload re-read nothing, the config compared equal
// to itself, and SIGHUP/the watcher reported success while applying nothing.
// The file is now parsed into a map the caller consults after the real
// environment, which keeps the precedence, makes reload actually re-read, and
// removes the os.Setenv race between the watcher and the signal handler.
//
// Earlier files in the chain still win over later ones.
func loadDotEnvFiles(explicitPath string) (map[string]string, error) {
	if explicitPath != "" {
		setResolvedConfigPath(explicitPath)
		return parseDotEnvFile(explicitPath)
	}

	// Chain: CWD .env → XDG config → Homebrew prefix
	resolved := ""
	merged := map[string]string{}
	paths := collectConfigPaths()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if resolved == "" {
			resolved = p
		}
		values, err := parseDotEnvFile(p)
		if err != nil {
			setResolvedConfigPath(resolved)
			return nil, err
		}
		for k, v := range values {
			if _, seen := merged[k]; !seen {
				merged[k] = v
			}
		}
	}
	setResolvedConfigPath(resolved)
	return merged, nil
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

// parseDotEnvFile reads a .env file into a map. A missing file is not an error
// (an installation may legitimately configure everything through the real
// environment). It performs no os.Setenv — see loadDotEnvFiles.
func parseDotEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
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

		if _, seen := values[key]; seen {
			continue
		}
		values[key] = parseDotEnvValue(value)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return values, nil
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
