// Package config provides configuration loading for the MCP server.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
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
	IndexDirs         []string // Directories and files to index for RAG (relative to RootPath or absolute)
	IndexExcludeDirs  []string // Directory names or repo-relative paths to exclude from RAG indexing
	IndexExcludeGlobs []string // Glob patterns matched against slash-separated repo-relative paths
	RedactSecrets     bool     // Redact common secret-like content before indexing
	ChunkSize         int      // Characters per chunk (default: 2000)
	ChunkOverlap      int      // Overlap between chunks (default: 200)

	// Embeddings configuration
	JinaAPIKey         string // Jina AI API key
	OpenAIAPIKey       string // OpenAI API key (or compatible: Together, Mistral, etc.)
	OpenAIBaseURL      string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel        string // OpenAI embedding model (default: text-embedding-3-small)
	OllamaBaseURL      string // Ollama base URL (default: http://localhost:11434)
	EmbeddingDimension int    // Embedding vector dimension (default: 1024)
	EmbeddingMode      string // Embedding mode: auto or local-only

	// RAG indexing behavior
	AutoIndex        bool          // Auto-index on startup (default: true)
	FileWatcher      bool          // Watch for file changes (default: true)
	WatchInterval    time.Duration // File watcher poll interval (default: 5m)
	DebounceDuration time.Duration // Debounce before reindexing (default: 30s)

	// HTTP server configuration
	HTTPMode                         string // "stdio" or "http" (default: "stdio")
	HTTPHost                         string // HTTP bind host (default: 127.0.0.1)
	HTTPPort                         int    // HTTP server port (default: 18080)
	HTTPAuthToken                    string // Optional Bearer token for HTTP auth
	HTTPInsecureAllowUnauthenticated bool   // Allow unauthenticated HTTP on non-loopback hosts

	// Background session tracking
	SessionTrackingEnabled    bool          // Enable automatic session tracking and background close-session orchestration
	SessionIdleTimeout        time.Duration // Idle timeout before auto-close
	SessionCheckpointInterval time.Duration // Periodic raw checkpoint interval during active sessions
	SessionMinEvents          int           // Minimum observed tool events before auto-close

	// Stewardship configuration
	StewardEnabled            bool    // Enable knowledge stewardship service
	StewardMode               string  // Policy mode: off, manual, scheduled, event_driven
	StewardScheduleInterval   string  // Schedule interval (e.g. "24h")
	StewardDuplicateThreshold float64 // Similarity threshold for duplicate detection (default: 0.85)
	StewardStaleDays          int     // Days before a memory is considered stale (default: 30)
	StewardCanonicalMinConf   float64 // Minimum confidence for canonical promotion (default: 0.80)

	// Hooks CLI dedup (T45) — prevents flood of near-duplicate session-checkpoint
	// records coming from the `auto-capture` and `checkpoint` hook CLI paths.
	// MCP programmatic `store_memory` is unaffected.
	CheckpointDedupDisabled  bool          // Escape hatch — true disables the filter
	CheckpointDedupThreshold float64       // Jaccard similarity at/above which a record is considered a duplicate (default: 0.9)
	CheckpointDedupWindow    time.Duration // How far back to scan for a recent duplicate (default: 10m)
	CheckpointDedupMinChars  int           // Skip summaries shorter than this as "empty" (default: 100)

	// Task archive sweep (T47) — pull-mode archive consolidation. Each root is
	// scanned for subdirectories (task slugs); working memories whose Context
	// matches a slug are marked outdated (high-importance ones go to review queue).
	TaskArchiveRoots []string       // Colon-separated list of absolute paths. Empty = sweep disabled.
	TaskSlugPattern  *regexp.Regexp // Optional regex filter for slug basenames; nil = accept all.

	// Neural reranker (T44) — opt-in cross-encoder that re-orders the top-N
	// hybrid-search candidates. Disabled by default; the retrieval pipeline
	// must degrade gracefully on timeout or provider error.
	RerankEnabled     bool          // MCP_RERANK_ENABLED (default: false)
	RerankProvider    string        // MCP_RERANK_PROVIDER: "jina" or "disabled" (default: "disabled")
	JinaRerankerModel string        // JINA_RERANKER_MODEL (default: jina-reranker-v2-base-multilingual)
	RerankTimeout     time.Duration // MCP_RERANK_TIMEOUT (default: 5s)
	RerankTopN        int           // MCP_RERANK_TOP_N (default: 40)

	// Memory sedimentation (T48) — feature flag governing transition rules
	// and layer-aware retrieval weighting. When false, the sediment_layer
	// column still exists (migration is mandatory) but recall and cycle do
	// not change behaviour.
	SedimentEnabled bool // MCP_SEDIMENT_ENABLED (default: false)
}

// explicitConfigPath is set via SetExplicitConfigPath before Load()/LoadFromEnv().
var explicitConfigPath string

// SetExplicitConfigPath sets an explicit config file path (e.g., from --config flag).
// When set, only this file is loaded instead of the default config search chain.
func SetExplicitConfigPath(path string) {
	explicitConfigPath = path
}

// envValues holds raw values read from environment variables before path resolution.
type envValues struct {
	root                             string
	allow                            string
	outputMode                       string
	statsEnabled                     bool
	statsPath                        string
	statsSample                      float64
	maxFileBytes                     int64
	maxSearch                        int
	maxDepth                         int
	ragEnabled                       bool
	ragMaxResults                    int
	memoryEnabled                    bool
	dataPath                         string
	ragIndexPath                     string
	memoryDBPath                     string
	logPath                          string
	indexDirs                        string
	indexExcludeDirs                 string
	indexExcludeGlobs                string
	redactSecrets                    bool
	chunkSize                        int
	chunkOverlap                     int
	jinaAPIKey                       string
	openaiAPIKey                     string
	openaiBaseURL                    string
	openaiModel                      string
	ollamaBaseURL                    string
	embeddingDimension               int
	embeddingMode                    string
	autoIndex                        bool
	fileWatcher                      bool
	watchInterval                    string
	debounceDuration                 string
	httpMode                         string
	httpHost                         string
	httpPort                         int
	httpAuthToken                    string
	httpInsecureAllowUnauthenticated bool
	sessionTrackingEnabled           bool
	sessionIdleTimeout               string
	sessionCheckpointInterval        string
	sessionMinEvents                 int
	stewardEnabled                   bool
	stewardMode                      string
	stewardScheduleInterval          string
	stewardDuplicateThreshold        float64
	stewardStaleDays                 int
	stewardCanonicalMinConf          float64
	checkpointDedupDisabled          bool
	checkpointDedupThreshold         float64
	checkpointDedupWindow            string
	checkpointDedupMinChars          int
	taskArchiveRoots                 string
	taskSlugPattern                  string
	rerankEnabled                    bool
	rerankProvider                   string
	jinaRerankerModel                string
	rerankTimeout                    string
	rerankTopN                       int
	sedimentEnabled                  bool
}

// loadEnv loads dotenv files and reads all configuration from environment variables.
func loadEnv() (envValues, error) {
	if err := loadDotEnvFiles(explicitConfigPath); err != nil {
		return envValues{}, err
	}
	return readEnvValues()
}

// readEnvValues reads all configuration from current environment variables.
func readEnvValues() (envValues, error) {
	return envValues{
		root:                             EnvOrDefault("MCP_ROOT", ""),
		allow:                            EnvOrDefault("MCP_ALLOW_DIRS", ""),
		outputMode:                       normalizeOutputMode(EnvOrDefault("MCP_STDIO_MODE", "")),
		statsEnabled:                     EnvBool("MCP_STATS_ENABLED", false),
		statsPath:                        EnvOrDefault("MCP_STATS_PATH", ""),
		statsSample:                      EnvFloat("MCP_STATS_SAMPLE_RATE", 1),
		maxFileBytes:                     EnvInt64("MCP_MAX_FILE_BYTES", DefaultMaxFileBytes),
		maxSearch:                        EnvInt("MCP_MAX_SEARCH_RESULTS", DefaultMaxSearchResult),
		maxDepth:                         EnvInt("MCP_MAX_DEPTH", DefaultMaxDepth),
		ragEnabled:                       EnvBool("MCP_RAG_ENABLED", true),
		ragMaxResults:                    EnvInt("MCP_RAG_MAX_RESULTS", 10),
		memoryEnabled:                    EnvBool("MCP_MEMORY_ENABLED", true),
		dataPath:                         EnvOrDefault("MCP_DATA_PATH", ""),
		ragIndexPath:                     EnvOrDefault("MCP_RAG_INDEX_PATH", ""),
		memoryDBPath:                     EnvOrDefault("MCP_MEMORY_DB_PATH", ""),
		logPath:                          EnvOrDefault("MCP_LOG_PATH", ""),
		indexDirs:                        EnvOrDefault("MCP_INDEX_DIRS", "docs"),
		indexExcludeDirs:                 EnvOrDefault("MCP_INDEX_EXCLUDE_DIRS", ""),
		indexExcludeGlobs:                EnvOrDefault("MCP_INDEX_EXCLUDE_GLOBS", ""),
		redactSecrets:                    EnvBool("MCP_REDACT_SECRETS", true),
		chunkSize:                        EnvInt("MCP_CHUNK_SIZE", 2000),
		chunkOverlap:                     EnvInt("MCP_CHUNK_OVERLAP", 200),
		jinaAPIKey:                       EnvOrDefault("JINA_API_KEY", ""),
		openaiAPIKey:                     EnvOrDefault("OPENAI_API_KEY", ""),
		openaiBaseURL:                    EnvOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		openaiModel:                      EnvOrDefault("OPENAI_EMBEDDING_MODEL", "text-embedding-3-small"),
		ollamaBaseURL:                    EnvOrDefault("OLLAMA_BASE_URL", "http://localhost:11434"),
		embeddingDimension:               EnvInt("MCP_EMBEDDING_DIMENSION", 1024),
		embeddingMode:                    normalizeEmbeddingMode(EnvOrDefault("MCP_EMBEDDING_MODE", "auto")),
		autoIndex:                        EnvBool("MCP_RAG_AUTO_INDEX", true),
		fileWatcher:                      EnvBool("MCP_RAG_FILE_WATCHER", true),
		watchInterval:                    EnvOrDefault("MCP_RAG_WATCH_INTERVAL", "5m"),
		debounceDuration:                 EnvOrDefault("MCP_RAG_DEBOUNCE", "30s"),
		httpMode:                         EnvOrDefault("MCP_HTTP_MODE", "stdio"),
		httpHost:                         EnvOrDefault("MCP_HTTP_HOST", "127.0.0.1"),
		httpPort:                         EnvInt("MCP_HTTP_PORT", 18080),
		httpAuthToken:                    EnvOrDefault("MCP_HTTP_AUTH_TOKEN", ""),
		httpInsecureAllowUnauthenticated: EnvBool("MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED", false),
		sessionTrackingEnabled:           EnvBool("MCP_SESSION_TRACKING_ENABLED", true),
		sessionIdleTimeout:               EnvOrDefault("MCP_SESSION_IDLE_TIMEOUT", "10m"),
		sessionCheckpointInterval:        EnvOrDefault("MCP_SESSION_CHECKPOINT_INTERVAL", "30m"),
		sessionMinEvents:                 EnvInt("MCP_SESSION_MIN_EVENTS", 2),
		stewardEnabled:                   EnvBool("MCP_STEWARD_ENABLED", false),
		stewardMode:                      EnvOrDefault("MCP_STEWARD_MODE", "manual"),
		stewardScheduleInterval:          EnvOrDefault("MCP_STEWARD_SCHEDULE_INTERVAL", "24h"),
		stewardDuplicateThreshold:        EnvFloat("MCP_STEWARD_DUPLICATE_THRESHOLD", 0.85),
		stewardStaleDays:                 EnvInt("MCP_STEWARD_STALE_DAYS", 30),
		stewardCanonicalMinConf:          EnvFloat("MCP_STEWARD_CANONICAL_MIN_CONFIDENCE", 0.80),
		checkpointDedupDisabled:          EnvBool("MCP_CHECKPOINT_DEDUP_DISABLED", false),
		checkpointDedupThreshold:         EnvFloat("MCP_CHECKPOINT_DEDUP_THRESHOLD", 0.9),
		checkpointDedupWindow:            EnvOrDefault("MCP_CHECKPOINT_DEDUP_WINDOW", "10m"),
		checkpointDedupMinChars:          EnvInt("MCP_CHECKPOINT_DEDUP_MIN_CHARS", 100),
		taskArchiveRoots:                 EnvOrDefault("MCP_TASK_ARCHIVE_ROOTS", ""),
		taskSlugPattern:                  EnvOrDefault("MCP_TASK_SLUG_PATTERN", ""),
		rerankEnabled:                    EnvBool("MCP_RERANK_ENABLED", false),
		rerankProvider:                   EnvOrDefault("MCP_RERANK_PROVIDER", "disabled"),
		jinaRerankerModel:                EnvOrDefault("JINA_RERANKER_MODEL", "jina-reranker-v2-base-multilingual"),
		rerankTimeout:                    EnvOrDefault("MCP_RERANK_TIMEOUT", "5s"),
		rerankTopN:                       EnvInt("MCP_RERANK_TOP_N", 40),
		sedimentEnabled:                  EnvBool("MCP_SEDIMENT_ENABLED", false),
	}, nil
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
	indexExcludeDirs := splitAllowlist(ev.indexExcludeDirs)
	indexExcludeGlobs := splitCSV(ev.indexExcludeGlobs)
	sessionMinEvents := ev.sessionMinEvents
	if sessionMinEvents <= 0 {
		sessionMinEvents = 2
	}

	cfg := Config{
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

		IndexDirs:         indexDirsList,
		IndexExcludeDirs:  indexExcludeDirs,
		IndexExcludeGlobs: indexExcludeGlobs,
		RedactSecrets:     ev.redactSecrets,
		ChunkSize:         ev.chunkSize,
		ChunkOverlap:      ev.chunkOverlap,

		JinaAPIKey:         ev.jinaAPIKey,
		OpenAIAPIKey:       ev.openaiAPIKey,
		OpenAIBaseURL:      ev.openaiBaseURL,
		OpenAIModel:        ev.openaiModel,
		OllamaBaseURL:      ev.ollamaBaseURL,
		EmbeddingDimension: ev.embeddingDimension,
		EmbeddingMode:      ev.embeddingMode,

		AutoIndex:        ev.autoIndex,
		FileWatcher:      ev.fileWatcher,
		WatchInterval:    parseDurationOrDefault(ev.watchInterval, 5*time.Minute),
		DebounceDuration: parseDurationOrDefault(ev.debounceDuration, 30*time.Second),

		HTTPMode:                         ev.httpMode,
		HTTPHost:                         strings.TrimSpace(ev.httpHost),
		HTTPPort:                         ev.httpPort,
		HTTPAuthToken:                    ev.httpAuthToken,
		HTTPInsecureAllowUnauthenticated: ev.httpInsecureAllowUnauthenticated,
		SessionTrackingEnabled:           ev.sessionTrackingEnabled,
		SessionIdleTimeout:               parseDurationOrDefault(ev.sessionIdleTimeout, 10*time.Minute),
		SessionCheckpointInterval:        parseDurationOrDefault(ev.sessionCheckpointInterval, 30*time.Minute),
		SessionMinEvents:                 sessionMinEvents,

		StewardEnabled:            resolveStewardEnabled(ev),
		StewardMode:               ev.stewardMode,
		StewardScheduleInterval:   ev.stewardScheduleInterval,
		StewardDuplicateThreshold: ev.stewardDuplicateThreshold,
		StewardStaleDays:          ev.stewardStaleDays,
		StewardCanonicalMinConf:   ev.stewardCanonicalMinConf,

		CheckpointDedupDisabled:  ev.checkpointDedupDisabled,
		CheckpointDedupThreshold: ev.checkpointDedupThreshold,
		CheckpointDedupWindow:    parseDurationOrDefault(ev.checkpointDedupWindow, 10*time.Minute),
		CheckpointDedupMinChars:  ev.checkpointDedupMinChars,

		TaskArchiveRoots: parseArchiveRoots(ev.taskArchiveRoots, root),

		RerankEnabled:     ev.rerankEnabled,
		RerankProvider:    ev.rerankProvider,
		JinaRerankerModel: ev.jinaRerankerModel,
		RerankTimeout:     parseDurationOrDefault(ev.rerankTimeout, 5*time.Second),
		RerankTopN:        ev.rerankTopN,

		SedimentEnabled: ev.sedimentEnabled,
	}
	if slugPattern := strings.TrimSpace(ev.taskSlugPattern); slugPattern != "" {
		re, err := regexp.Compile(slugPattern)
		if err != nil {
			return Config{}, fmt.Errorf("invalid MCP_TASK_SLUG_PATTERN %q: %w", slugPattern, err)
		}
		cfg.TaskSlugPattern = re
	}
	if err := validateResolvedConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseArchiveRoots splits a colon-separated (PATH-style) list of archive roots
// and resolves each to an absolute path, relative to `root` if needed.
// Empty entries are skipped; duplicates (after resolution) are dropped while
// preserving the first occurrence. Returns nil for empty input (sweep disabled).
func parseArchiveRoots(raw string, root string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ":")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LoadFromEnv reads configuration only from environment variables (no flag parsing).
// Use this for CLI subcommands that define their own flags.
func LoadFromEnv() (Config, error) {
	ev, err := loadEnv()
	if err != nil {
		return Config{}, err
	}
	return resolvePaths(ev)
}

// Load reads configuration from environment variables and command-line flags.
func Load() (Config, error) {
	ev, err := loadEnv()
	if err != nil {
		return Config{}, err
	}

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
	parts := splitCSV(raw)
	for i, p := range parts {
		parts[i] = filepath.Clean(p)
	}
	return parts
}

func splitCSV(raw string) []string {
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

func normalizeEmbeddingMode(value string) string {
	val := strings.TrimSpace(strings.ToLower(value))
	switch val {
	case "", "auto":
		return "auto"
	case "local-only", "local_only", "local":
		return "local-only"
	default:
		return "auto"
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

// resolveStewardEnabled determines if steward should be enabled.
// Auto-enables in HTTP mode with memory, unless explicitly disabled via env var.
func resolveStewardEnabled(ev envValues) bool {
	// If user explicitly set the env var, respect it.
	if raw := os.Getenv("MCP_STEWARD_ENABLED"); raw != "" {
		return ev.stewardEnabled
	}
	// Auto-enable in HTTP mode when memory is also enabled.
	if strings.EqualFold(ev.httpMode, "http") && ev.memoryEnabled {
		return true
	}
	return ev.stewardEnabled
}

// EmbedderConfig returns the embedder.Config derived from this server config.
func (c Config) EmbedderConfig() embedder.Config {
	return embedder.Config{
		JinaToken:     c.JinaAPIKey,
		OpenAIToken:   c.OpenAIAPIKey,
		OpenAIBaseURL: c.OpenAIBaseURL,
		OpenAIModel:   c.OpenAIModel,
		OllamaBaseURL: c.OllamaBaseURL,
		Dimension:     c.EmbeddingDimension,
		Mode:          c.EmbeddingMode,
		MaxRetries:    1,
		Timeout:       5 * time.Second,
	}
}

// BoolToString converts a bool to "true" or "false" string for logging.
func BoolToString(b bool) string {
	return strconv.FormatBool(b)
}
