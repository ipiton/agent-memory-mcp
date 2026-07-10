package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// h13GoldenEnv sets one non-default value per config field so the golden
// captures every resolved value. Kept alphabetically-ish by subsystem.
func h13GoldenEnv(t *testing.T) {
	t.Helper()
	hermeticDotEnv(t)
	set := map[string]string{
		"MCP_ROOT":                                "/tmp/h13root",
		"MCP_ALLOW_DIRS":                          "docs,src",
		"MCP_STDIO_MODE":                          "content-length",
		"MCP_STATS_ENABLED":                       "true",
		"MCP_STATS_PATH":                          "/tmp/h13stats.jsonl",
		"MCP_STATS_SAMPLE_RATE":                   "0.5",
		"MCP_MAX_FILE_BYTES":                      "12345",
		"MCP_MAX_SEARCH_RESULTS":                  "77",
		"MCP_MAX_DEPTH":                           "9",
		"MCP_RAG_ENABLED":                         "true",
		"MCP_RAG_MAX_RESULTS":                     "15",
		"MCP_MEMORY_ENABLED":                      "true",
		"MCP_MEMORY_PREVIEW_RUNES":                "42",
		"MCP_DATA_PATH":                           "/tmp/h13data",
		"MCP_RAG_INDEX_PATH":                      "/tmp/h13idx",
		"MCP_MEMORY_DB_PATH":                      "/tmp/h13mem.db",
		"MCP_LOG_PATH":                            "/tmp/h13.log",
		"MCP_INDEX_DIRS":                          "docs,notes",
		"MCP_INDEX_EXCLUDE_DIRS":                  "vendor,node_modules",
		"MCP_INDEX_EXCLUDE_GLOBS":                 "*.tmp,*.bak",
		"MCP_REDACT_SECRETS":                      "false",
		"MCP_CHUNK_SIZE":                          "1500",
		"MCP_CHUNK_OVERLAP":                       "150",
		"MCP_RAG_KEEP_NOISE":                      "true",
		"MCP_TRIPLE_EXTRACTOR_ENABLED":            "true",
		"MCP_TRIPLE_EXTRACTOR_BASE_URL":           "http://te.local",
		"MCP_TRIPLE_EXTRACTOR_API_KEY":            "te-key",
		"MCP_TRIPLE_EXTRACTOR_MODEL":              "te-model",
		"MCP_TRIPLE_EXTRACTOR_TIMEOUT":            "45s",
		"JINA_API_KEY":                            "jina-key",
		"OPENAI_API_KEY":                          "oa-key",
		"OPENAI_BASE_URL":                         "http://oa.local",
		"OPENAI_EMBEDDING_MODEL":                  "oa-model",
		"OLLAMA_BASE_URL":                         "http://ol.local",
		"LLAMACPP_BASE_URL":                       "http://lc.local",
		"LLAMACPP_EMBEDDING_MODEL":                "lc-model",
		"MCP_EMBEDDING_DIMENSION":                 "512",
		"MCP_EMBEDDING_MODE":                      "local-only",
		"MCP_EMBEDDING_TIMEOUT":                   "7s",
		"MCP_EMBEDDING_MAX_RETRIES":               "3",
		"MCP_RAG_AUTO_INDEX":                      "false",
		"MCP_RAG_FILE_WATCHER":                    "false",
		"MCP_RAG_WATCH_INTERVAL":                  "6m",
		"MCP_RAG_DEBOUNCE":                        "25s",
		"MCP_HTTP_MODE":                           "http",
		"MCP_HTTP_HOST":                           "0.0.0.0",
		"MCP_HTTP_PORT":                           "9999",
		"MCP_HTTP_AUTH_TOKEN":                     "tok",
		"MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED": "true",
		"MCP_SESSION_TRACKING_ENABLED":            "false",
		"MCP_SESSION_IDLE_TIMEOUT":                "11m",
		"MCP_SESSION_CHECKPOINT_INTERVAL":         "33m",
		"MCP_SESSION_MIN_EVENTS":                  "5",
		"MCP_STEWARD_ENABLED":                     "true",
		"MCP_STEWARD_MODE":                        "scheduled",
		"MCP_STEWARD_SCHEDULE_INTERVAL":           "12h",
		"MCP_STEWARD_DUPLICATE_THRESHOLD":         "0.7",
		"MCP_STEWARD_STALE_DAYS":                  "45",
		"MCP_STEWARD_CANONICAL_MIN_CONFIDENCE":    "0.6",
		"MCP_CHECKPOINT_DEDUP_DISABLED":           "true",
		"MCP_CHECKPOINT_DEDUP_THRESHOLD":          "0.8",
		"MCP_CHECKPOINT_DEDUP_WINDOW":             "15m",
		"MCP_CHECKPOINT_DEDUP_MIN_CHARS":          "50",
		"MCP_TASK_ARCHIVE_ROOTS":                  "/tmp/arch1:/tmp/arch2",
		"MCP_TASK_SLUG_PATTERN":                   "^task-.*$",
		"MCP_ARCHIVE_SWEEP_ENABLED":               "false",
		"MCP_ARCHIVE_SWEEP_INTERVAL":              "2h",
		"MCP_RERANK_ENABLED":                      "true",
		"MCP_RERANK_PROVIDER":                     "jina",
		"JINA_RERANKER_MODEL":                     "rr-model",
		"MCP_RERANK_TIMEOUT":                      "8s",
		"MCP_RERANK_TOP_N":                        "25",
		"MCP_SEDIMENT_ENABLED":                    "true",
		"MCP_SEDIMENT_SCHEDULE_INTERVAL":          "20m",
		"MCP_RECALL_HALFLIFE_DAYS":                "15",
		"MCP_TOOL_GROUPING":                       "true",
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
}

// serializeConfigForGolden renders every resolved field under a STABLE logical
// key, independent of the Go struct shape, so the golden survives the H13
// sub-struct split (only the RHS field access changes).
func serializeConfigForGolden(c Config) string {
	var b strings.Builder
	p := func(k string, v any) { fmt.Fprintf(&b, "%s=%v\n", k, v) }
	slug := ""
	if c.Lifecycle.TaskSlugPattern != nil {
		slug = c.Lifecycle.TaskSlugPattern.String()
	}
	p("RootPath", c.RootPath)
	p("AllowedPaths", c.AllowedPaths)
	p("MaxFileBytes", c.MaxFileBytes)
	p("MaxSearchResults", c.MaxSearchResults)
	p("MaxDepth", c.MaxDepth)
	p("OutputMode", c.OutputMode)
	p("DataPath", c.DataPath)
	p("LogPath", c.LogPath)
	p("ToolGrouping", c.ToolGrouping)
	p("Stats.Enabled", c.Stats.Enabled)
	p("Stats.Path", c.Stats.Path)
	p("Stats.SampleRate", c.Stats.SampleRate)
	p("Memory.Enabled", c.Memory.Enabled)
	p("Memory.DBPath", c.Memory.DBPath)
	p("Memory.PreviewRunes", c.Memory.PreviewRunes)
	p("RAG.Enabled", c.RAG.Enabled)
	p("RAG.MaxResults", c.RAG.MaxResults)
	p("RAG.IndexPath", c.RAG.IndexPath)
	p("RAG.IndexDirs", c.RAG.IndexDirs)
	p("RAG.IndexExcludeDirs", c.RAG.IndexExcludeDirs)
	p("RAG.IndexExcludeGlobs", c.RAG.IndexExcludeGlobs)
	p("RAG.RedactSecrets", c.RAG.RedactSecrets)
	p("RAG.ChunkSize", c.RAG.ChunkSize)
	p("RAG.ChunkOverlap", c.RAG.ChunkOverlap)
	p("RAG.KeepNoise", c.RAG.KeepNoise)
	p("RAG.AutoIndex", c.RAG.AutoIndex)
	p("RAG.FileWatcher", c.RAG.FileWatcher)
	p("RAG.WatchInterval", c.RAG.WatchInterval)
	p("RAG.DebounceDuration", c.RAG.DebounceDuration)
	p("Embeddings.JinaAPIKey", c.Embeddings.JinaAPIKey)
	p("Embeddings.OpenAIAPIKey", c.Embeddings.OpenAIAPIKey)
	p("Embeddings.OpenAIBaseURL", c.Embeddings.OpenAIBaseURL)
	p("Embeddings.OpenAIModel", c.Embeddings.OpenAIModel)
	p("Embeddings.OllamaBaseURL", c.Embeddings.OllamaBaseURL)
	p("Embeddings.LlamaCPPBaseURL", c.Embeddings.LlamaCPPBaseURL)
	p("Embeddings.LlamaCPPModel", c.Embeddings.LlamaCPPModel)
	p("Embeddings.Dimension", c.Embeddings.Dimension)
	p("Embeddings.Mode", c.Embeddings.Mode)
	p("Embeddings.Timeout", c.Embeddings.Timeout)
	p("Embeddings.MaxRetries", c.Embeddings.MaxRetries)
	p("TripleExtractor.Enabled", c.TripleExtractor.Enabled)
	p("TripleExtractor.BaseURL", c.TripleExtractor.BaseURL)
	p("TripleExtractor.APIKey", c.TripleExtractor.APIKey)
	p("TripleExtractor.Model", c.TripleExtractor.Model)
	p("TripleExtractor.Timeout", c.TripleExtractor.Timeout)
	p("HTTP.Mode", c.HTTP.Mode)
	p("HTTP.Host", c.HTTP.Host)
	p("HTTP.Port", c.HTTP.Port)
	p("HTTP.AuthToken", c.HTTP.AuthToken)
	p("HTTP.InsecureAllowUnauthenticated", c.HTTP.InsecureAllowUnauthenticated)
	p("Session.TrackingEnabled", c.Session.TrackingEnabled)
	p("Session.IdleTimeout", c.Session.IdleTimeout)
	p("Session.CheckpointInterval", c.Session.CheckpointInterval)
	p("Session.MinEvents", c.Session.MinEvents)
	p("Steward.Enabled", c.Steward.Enabled)
	p("Steward.Mode", c.Steward.Mode)
	p("Steward.ScheduleInterval", c.Steward.ScheduleInterval)
	p("Steward.DuplicateThreshold", c.Steward.DuplicateThreshold)
	p("Steward.StaleDays", c.Steward.StaleDays)
	p("Steward.CanonicalMinConf", c.Steward.CanonicalMinConf)
	p("HooksDedup.Disabled", c.HooksDedup.Disabled)
	p("HooksDedup.Threshold", c.HooksDedup.Threshold)
	p("HooksDedup.Window", c.HooksDedup.Window)
	p("HooksDedup.MinChars", c.HooksDedup.MinChars)
	p("Lifecycle.TaskArchiveRoots", c.Lifecycle.TaskArchiveRoots)
	p("Lifecycle.TaskSlugPattern", slug)
	p("Lifecycle.ArchiveSweepEnabled", c.Lifecycle.ArchiveSweepEnabled)
	p("Lifecycle.ArchiveSweepInterval", c.Lifecycle.ArchiveSweepInterval)
	p("Rerank.Enabled", c.Rerank.Enabled)
	p("Rerank.Provider", c.Rerank.Provider)
	p("Rerank.JinaModel", c.Rerank.JinaModel)
	p("Rerank.Timeout", c.Rerank.Timeout)
	p("Rerank.TopN", c.Rerank.TopN)
	p("Sediment.Enabled", c.Sediment.Enabled)
	p("Sediment.ScheduleInterval", c.Sediment.ScheduleInterval)
	p("Sediment.RecallHalfLifeDays", c.Sediment.RecallHalfLifeDays)
	return b.String()
}

// TestConfigResolvedValuesGolden pins every resolved config value against a
// golden file, proving the H13 sub-struct split preserved all values.
func TestConfigResolvedValuesGolden(t *testing.T) {
	h13GoldenEnv(t)
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	got := serializeConfigForGolden(cfg)

	goldenPath := "/private/tmp/claude-501/-Users-vit-Documents-Projects-mcp-memory/3ffe0da0-9694-419d-85c1-ee31ac484b86/scratchpad/config_golden.txt"
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (%s): %v", goldenPath, err)
	}
	if got != string(want) {
		_ = os.WriteFile("/private/tmp/claude-501/-Users-vit-Documents-Projects-mcp-memory/3ffe0da0-9694-419d-85c1-ee31ac484b86/scratchpad/config_after.txt", []byte(got), 0644)
		t.Fatalf("resolved config differs from golden — see config_after.txt")
	}
}
