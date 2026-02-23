// Package config provides configuration loading for the MCP server.
package config

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxFileBytes    = int64(2 * 1024 * 1024)
	DefaultMaxSearchResult = 200
	DefaultMaxDepth        = 3
)

// Config holds all MCP server configuration.
type Config struct {
	// Core settings
	RootPath         string   // Repository root path
	AllowedPaths     []string // Allowlisted paths for file access
	MaxFileBytes     int64
	MaxSearchResults int
	MaxDepth         int
	OutputMode       string
	StatsEnabled     bool
	StatsPath        string
	StatsSampleRate  float64

	// Data storage paths
	DataPath     string // Base path for all data (default: {RootPath}/data)
	RAGIndexPath string // Path to RAG vector index (default: {DataPath}/rag-index)
	MemoryDBPath string // Path to memory database (default: {DataPath}/memory-store/memories.db)
	LogPath      string // Path to diagnostic log file (default: {RootPath}/logs/mcp-diagnostics.log)

	// RAG configuration
	RAGEnabled    bool
	RAGMaxResults int

	// Memory configuration
	MemoryEnabled bool

	// Document indexing configuration
	IndexDirs    []string // Directories and files to index for RAG (relative to RootPath or absolute)
	ChunkSize    int      // Characters per chunk (default: 2000)
	ChunkOverlap  int      // Overlap between chunks (default: 200)

	// Embeddings configuration
	JinaAPIKey         string // Jina AI API key
	OpenAIAPIKey       string // OpenAI API key (or compatible: Together, Mistral, etc.)
	OpenAIBaseURL      string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel        string // OpenAI embedding model (default: text-embedding-3-small)
	OllamaBaseURL      string // Ollama base URL (default: http://localhost:11434)
	EmbeddingDimension int    // Embedding vector dimension (default: 1024)

	// RAG indexing behavior
	AutoIndex        bool          // Auto-index on startup (default: true)
	FileWatcher      bool          // Watch for file changes (default: true)
	WatchInterval    time.Duration // File watcher poll interval (default: 5m)
	DebounceDuration time.Duration // Debounce before reindexing (default: 30s)

	// HTTP server configuration
	HTTPMode      string // "stdio" or "http" (default: "stdio")
	HTTPPort      int    // HTTP server port (default: 18080)
	HTTPAuthToken string // Optional Bearer token for HTTP auth
}

// envValues holds raw values read from environment variables before path resolution.
type envValues struct {
	root               string
	allow              string
	outputMode         string
	statsEnabled       bool
	statsPath          string
	statsSample        float64
	maxFileBytes       int64
	maxSearch          int
	maxDepth           int
	ragEnabled         bool
	ragMaxResults      int
	memoryEnabled      bool
	dataPath           string
	ragIndexPath       string
	memoryDBPath       string
	logPath            string
	indexDirs          string
	chunkSize          int
	chunkOverlap       int
	jinaAPIKey         string
	openaiAPIKey       string
	openaiBaseURL      string
	openaiModel        string
	ollamaBaseURL      string
	embeddingDimension int
	autoIndex        bool
	fileWatcher      bool
	watchInterval    string
	debounceDuration string
	httpMode         string
	httpPort         int
	httpAuthToken    string
}

// loadEnv reads all configuration from environment variables.
func loadEnv() envValues {
	return envValues{
		root:               EnvOrDefault("MCP_ROOT", ""),
		allow:              EnvOrDefault("MCP_ALLOW_DIRS", ""),
		outputMode:         normalizeOutputMode(EnvOrDefault("MCP_STDIO_MODE", "")),
		statsEnabled:       EnvBool("MCP_STATS_ENABLED", false),
		statsPath:          EnvOrDefault("MCP_STATS_PATH", ""),
		statsSample:        EnvFloat("MCP_STATS_SAMPLE_RATE", 1),
		maxFileBytes:       EnvInt64("MCP_MAX_FILE_BYTES", DefaultMaxFileBytes),
		maxSearch:          EnvInt("MCP_MAX_SEARCH_RESULTS", DefaultMaxSearchResult),
		maxDepth:           EnvInt("MCP_MAX_DEPTH", DefaultMaxDepth),
		ragEnabled:         EnvBool("MCP_RAG_ENABLED", true),
		ragMaxResults:      EnvInt("MCP_RAG_MAX_RESULTS", 10),
		memoryEnabled:      EnvBool("MCP_MEMORY_ENABLED", true),
		dataPath:           EnvOrDefault("MCP_DATA_PATH", ""),
		ragIndexPath:       EnvOrDefault("MCP_RAG_INDEX_PATH", ""),
		memoryDBPath:       EnvOrDefault("MCP_MEMORY_DB_PATH", ""),
		logPath:            EnvOrDefault("MCP_LOG_PATH", ""),
		indexDirs:          EnvOrDefault("MCP_INDEX_DIRS", "docs"),
		chunkSize:          EnvInt("MCP_CHUNK_SIZE", 2000),
		chunkOverlap:       EnvInt("MCP_CHUNK_OVERLAP", 200),
		jinaAPIKey:         EnvOrDefault("JINA_API_KEY", ""),
		openaiAPIKey:       EnvOrDefault("OPENAI_API_KEY", ""),
		openaiBaseURL:      EnvOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		openaiModel:        EnvOrDefault("OPENAI_EMBEDDING_MODEL", "text-embedding-3-small"),
		ollamaBaseURL:      EnvOrDefault("OLLAMA_BASE_URL", "http://localhost:11434"),
		embeddingDimension: EnvInt("MCP_EMBEDDING_DIMENSION", 1024),
		autoIndex:        EnvBool("MCP_RAG_AUTO_INDEX", true),
		fileWatcher:      EnvBool("MCP_RAG_FILE_WATCHER", true),
		watchInterval:    EnvOrDefault("MCP_RAG_WATCH_INTERVAL", "5m"),
		debounceDuration: EnvOrDefault("MCP_RAG_DEBOUNCE", "30s"),
		httpMode:         EnvOrDefault("MCP_HTTP_MODE", "stdio"),
		httpPort:         EnvInt("MCP_HTTP_PORT", 18080),
		httpAuthToken:    EnvOrDefault("MCP_HTTP_AUTH_TOKEN", ""),
	}
}

// resolvePaths resolves all data paths relative to root and returns a Config.
func resolvePaths(ev envValues) (Config, error) {
	root := ev.root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		root = cwd
	}

	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}

	allowed := splitAllowlist(ev.allow)

	dataPath := ev.dataPath
	if dataPath == "" {
		dataPath = filepath.Join(root, "data")
	} else if !filepath.IsAbs(dataPath) {
		dataPath = filepath.Join(root, dataPath)
	}

	ragIndexPath := ev.ragIndexPath
	if ragIndexPath == "" {
		ragIndexPath = filepath.Join(dataPath, "rag-index")
	} else if !filepath.IsAbs(ragIndexPath) {
		ragIndexPath = filepath.Join(root, ragIndexPath)
	}

	memoryDBPath := ev.memoryDBPath
	if memoryDBPath == "" {
		memoryDBPath = filepath.Join(dataPath, "memory-store", "memories.db")
	} else if !filepath.IsAbs(memoryDBPath) {
		memoryDBPath = filepath.Join(root, memoryDBPath)
	}

	statsPath := ev.statsPath
	if ev.statsEnabled && statsPath == "" {
		statsPath = filepath.Join(root, "logs", "mcp-usage.jsonl")
	}

	logPath := ev.logPath
	if logPath == "" {
		logPath = filepath.Join(root, "logs", "mcp-diagnostics.log")
	} else if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(root, logPath)
	}

	indexDirsList := splitAllowlist(ev.indexDirs)

	return Config{
		RootPath:         root,
		AllowedPaths:     allowed,
		MaxFileBytes:     ev.maxFileBytes,
		MaxSearchResults: ev.maxSearch,
		MaxDepth:         ev.maxDepth,
		OutputMode:       normalizeOutputMode(ev.outputMode),
		StatsEnabled:     ev.statsEnabled,
		StatsPath:        statsPath,
		StatsSampleRate:  ev.statsSample,

		DataPath:     dataPath,
		RAGIndexPath: ragIndexPath,
		MemoryDBPath: memoryDBPath,
		LogPath:      logPath,

		RAGEnabled:    ev.ragEnabled,
		RAGMaxResults: ev.ragMaxResults,
		MemoryEnabled: ev.memoryEnabled,

		IndexDirs:    indexDirsList,
		ChunkSize:    ev.chunkSize,
		ChunkOverlap:  ev.chunkOverlap,

		JinaAPIKey:         ev.jinaAPIKey,
		OpenAIAPIKey:       ev.openaiAPIKey,
		OpenAIBaseURL:      ev.openaiBaseURL,
		OpenAIModel:        ev.openaiModel,
		OllamaBaseURL:      ev.ollamaBaseURL,
		EmbeddingDimension: ev.embeddingDimension,

		AutoIndex:        ev.autoIndex,
		FileWatcher:      ev.fileWatcher,
		WatchInterval:    parseDurationOrDefault(ev.watchInterval, 5*time.Minute),
		DebounceDuration: parseDurationOrDefault(ev.debounceDuration, 30*time.Second),

		HTTPMode:      ev.httpMode,
		HTTPPort:      ev.httpPort,
		HTTPAuthToken: ev.httpAuthToken,
	}, nil
}

// LoadFromEnv reads configuration only from environment variables (no flag parsing).
// Use this for CLI subcommands that define their own flags.
func LoadFromEnv() (Config, error) {
	return resolvePaths(loadEnv())
}

// Load reads configuration from environment variables and command-line flags.
func Load() (Config, error) {
	ev := loadEnv()

	flag.StringVar(&ev.root, "root", ev.root, "Repository root (defaults to current dir)")
	flag.StringVar(&ev.allow, "allow", ev.allow, "Comma-separated allowlist of repo-relative paths")
	flag.StringVar(&ev.outputMode, "stdio-mode", ev.outputMode, "Stdio framing: line or content-length")
	flag.BoolVar(&ev.statsEnabled, "stats-enabled", ev.statsEnabled, "Enable MCP usage stats logging")
	flag.StringVar(&ev.statsPath, "stats-path", ev.statsPath, "Path for MCP usage stats log (jsonl)")
	flag.Float64Var(&ev.statsSample, "stats-sample-rate", ev.statsSample, "Sample rate for stats logging (0-1)")
	flag.Int64Var(&ev.maxFileBytes, "max-file-bytes", ev.maxFileBytes, "Max bytes to read per file")
	flag.IntVar(&ev.maxSearch, "max-search-results", ev.maxSearch, "Max search results")
	flag.IntVar(&ev.maxDepth, "max-depth", ev.maxDepth, "Max directory depth for listing")
	flag.Parse()

	return resolvePaths(ev)
}

func splitAllowlist(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		item = filepath.Clean(item)
		out = append(out, item)
	}
	return out
}

// EnvOrDefault returns the value of an environment variable, or fallback if empty.
func EnvOrDefault(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}

// EnvInt returns an environment variable parsed as int, or fallback on error.
func EnvInt(key string, fallback int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}

// EnvInt64 returns an environment variable parsed as int64, or fallback on error.
func EnvInt64(key string, fallback int64) int64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func normalizeOutputMode(value string) string {
	val := strings.TrimSpace(strings.ToLower(value))
	switch val {
	case "line", "jsonl", "newline":
		return "line"
	case "content-length", "header", "headers":
		return "content-length"
	default:
		return ""
	}
}

// EnvBool returns an environment variable parsed as bool, or fallback on error.
func EnvBool(key string, fallback bool) bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return fallback
	}
	switch val {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

// EnvFloat returns an environment variable parsed as float64, or fallback on error.
func EnvFloat(key string, fallback float64) float64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseDurationOrDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// BoolToString converts a bool to "true" or "false" string for logging.
func BoolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
