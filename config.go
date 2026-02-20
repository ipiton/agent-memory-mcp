package main

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultMaxFileBytes    = int64(2 * 1024 * 1024)
	defaultMaxSearchResult = 200
	defaultMaxDepth        = 3
)

// Config holds all MCP server configuration
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
	DataPath       string // Base path for all data (default: {RootPath}/data)
	RAGIndexPath   string // Path to RAG vector index (default: {DataPath}/rag-index)
	MemoryDBPath   string // Path to memory database (default: {DataPath}/memory-store/memories.db)
	LogPath        string // Path to diagnostic log file (default: {RootPath}/logs/mcp-diagnostics.log)

	// RAG configuration
	RAGEnabled    bool
	RAGMaxResults int

	// Memory configuration
	MemoryEnabled bool

	// Document indexing configuration
	IndexDirs      []string // Directories to index (relative to RootPath)
	ChangelogPath  string   // Path to changelog file (relative to RootPath)
	ChunkSize      int      // Characters per chunk (default: 2000)
	ChunkOverlap   int      // Overlap between chunks (default: 200)

	// Embeddings configuration
	JinaAPIKey     string // Jina AI API key
	OpenAIAPIKey   string // OpenAI API key (or compatible: Together, Mistral, etc.)
	OpenAIBaseURL  string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel    string // OpenAI embedding model (default: text-embedding-3-small)
	OllamaBaseURL      string // Ollama base URL (default: http://localhost:11434)
	EmbeddingDimension int    // Embedding vector dimension (default: 1024)

	// HTTP server configuration
	HTTPMode string // "stdio" or "http" (default: "stdio")
	HTTPPort int    // HTTP server port (default: 8080)
}

func LoadConfig() (Config, error) {
	// Core settings
	root := envOrDefault("MCP_ROOT", "")
	allow := envOrDefault("MCP_ALLOW_DIRS", "")
	outputMode := normalizeOutputMode(envOrDefault("MCP_STDIO_MODE", ""))
	statsEnabled := envBool("MCP_STATS_ENABLED", false)
	statsPath := envOrDefault("MCP_STATS_PATH", "")
	statsSample := envFloat("MCP_STATS_SAMPLE_RATE", 1)
	maxFileBytes := envInt64("MCP_MAX_FILE_BYTES", defaultMaxFileBytes)
	maxSearch := envInt("MCP_MAX_SEARCH_RESULTS", defaultMaxSearchResult)
	maxDepth := envInt("MCP_MAX_DEPTH", defaultMaxDepth)

	// RAG & Memory settings
	ragEnabled := envBool("MCP_RAG_ENABLED", true)
	ragMaxResults := envInt("MCP_RAG_MAX_RESULTS", 10)
	memoryEnabled := envBool("MCP_MEMORY_ENABLED", true)

	// Data paths
	dataPath := envOrDefault("MCP_DATA_PATH", "")
	ragIndexPath := envOrDefault("MCP_RAG_INDEX_PATH", "")
	memoryDBPath := envOrDefault("MCP_MEMORY_DB_PATH", "")
	logPath := envOrDefault("MCP_LOG_PATH", "")

	// Indexing settings
	indexDirs := envOrDefault("MCP_INDEX_DIRS", "docs")
	changelogPath := envOrDefault("MCP_CHANGELOG_PATH", "CHANGELOG.md")
	chunkSize := envInt("MCP_CHUNK_SIZE", 2000)
	chunkOverlap := envInt("MCP_CHUNK_OVERLAP", 200)

	// Embeddings
	jinaAPIKey := envOrDefault("JINA_API_KEY", "")
	openaiAPIKey := envOrDefault("OPENAI_API_KEY", "")
	openaiBaseURL := envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1")
	openaiModel := envOrDefault("OPENAI_EMBEDDING_MODEL", "text-embedding-3-small")
	ollamaBaseURL := envOrDefault("OLLAMA_BASE_URL", "http://localhost:11434")
	embeddingDimension := envInt("MCP_EMBEDDING_DIMENSION", 1024)

	// HTTP settings
	httpMode := envOrDefault("MCP_HTTP_MODE", "stdio")
	httpPort := envInt("MCP_HTTP_PORT", 8080)

	flag.StringVar(&root, "root", root, "Repository root (defaults to current dir)")
	flag.StringVar(&allow, "allow", allow, "Comma-separated allowlist of repo-relative paths")
	flag.StringVar(&outputMode, "stdio-mode", outputMode, "Stdio framing: line or content-length")
	flag.BoolVar(&statsEnabled, "stats-enabled", statsEnabled, "Enable MCP usage stats logging")
	flag.StringVar(&statsPath, "stats-path", statsPath, "Path for MCP usage stats log (jsonl)")
	flag.Float64Var(&statsSample, "stats-sample-rate", statsSample, "Sample rate for stats logging (0-1)")
	flag.Int64Var(&maxFileBytes, "max-file-bytes", maxFileBytes, "Max bytes to read per file")
	flag.IntVar(&maxSearch, "max-search-results", maxSearch, "Max search results")
	flag.IntVar(&maxDepth, "max-depth", maxDepth, "Max directory depth for listing")
	flag.Parse()

	// Resolve root path
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		root = cwd
	}

	root, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}

	allowed := splitAllowlist(allow)

	// Set default data paths relative to root
	if dataPath == "" {
		dataPath = filepath.Join(root, "data")
	} else if !filepath.IsAbs(dataPath) {
		dataPath = filepath.Join(root, dataPath)
	}

	if ragIndexPath == "" {
		ragIndexPath = filepath.Join(dataPath, "rag-index")
	} else if !filepath.IsAbs(ragIndexPath) {
		ragIndexPath = filepath.Join(root, ragIndexPath)
	}

	if memoryDBPath == "" {
		memoryDBPath = filepath.Join(dataPath, "memory-store", "memories.db")
	} else if !filepath.IsAbs(memoryDBPath) {
		memoryDBPath = filepath.Join(root, memoryDBPath)
	}

	if statsEnabled && statsPath == "" {
		statsPath = filepath.Join(root, "logs", "mcp-usage.jsonl")
	}

	if logPath == "" {
		logPath = filepath.Join(root, "logs", "mcp-diagnostics.log")
	} else if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(root, logPath)
	}

	// Parse index directories
	indexDirsList := splitAllowlist(indexDirs)

	return Config{
		// Core
		RootPath:         root,
		AllowedPaths:     allowed,
		MaxFileBytes:     maxFileBytes,
		MaxSearchResults: maxSearch,
		MaxDepth:         maxDepth,
		OutputMode:       normalizeOutputMode(outputMode),
		StatsEnabled:     statsEnabled,
		StatsPath:        statsPath,
		StatsSampleRate:  statsSample,

		// Data paths
		DataPath:     dataPath,
		RAGIndexPath: ragIndexPath,
		MemoryDBPath: memoryDBPath,
		LogPath:      logPath,

		// RAG & Memory
		RAGEnabled:    ragEnabled,
		RAGMaxResults: ragMaxResults,
		MemoryEnabled: memoryEnabled,

		// Indexing
		IndexDirs:     indexDirsList,
		ChangelogPath: changelogPath,
		ChunkSize:     chunkSize,
		ChunkOverlap:  chunkOverlap,

		// Embeddings
		JinaAPIKey:    jinaAPIKey,
		OpenAIAPIKey:  openaiAPIKey,
		OpenAIBaseURL: openaiBaseURL,
		OpenAIModel:   openaiModel,
		OllamaBaseURL:      ollamaBaseURL,
		EmbeddingDimension: embeddingDimension,

		// HTTP
		HTTPMode: httpMode,
		HTTPPort: httpPort,
	}, nil
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

func envOrDefault(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}

func envInt(key string, fallback int) int {
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

func envInt64(key string, fallback int64) int64 {
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

func envBool(key string, fallback bool) bool {
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

func envFloat(key string, fallback float64) float64 {
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
