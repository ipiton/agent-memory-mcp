package server

import (
	"reflect"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// reloadAppliedNote documents, in one place, what a config reload actually
// changes. Everything else is fixed at construction.
//
// T91 L12: the reload path only ever rebuilt the RAG engine. That was invisible
// while reload was broken outright (T89 H5) — nothing applied, so nothing stood
// out. With reload working, the asymmetry became the problem: an operator edits
// config.env, the log says the config reloaded, and their new session timeout
// or sweep interval is simply not in effect. Re-initialising those subsystems
// on the fly is not free (closing the session tracker flushes the live session;
// restarting a scheduler blocks the reload for its shutdown wait), so the
// reload keeps its narrow scope and instead names what it could not apply.
const reloadAppliedNote = "RAG engine, archive-sweep roots and slug pattern"

// restartRequiredChanges compares two configs and returns the settings that
// changed but cannot take effect until the process restarts. The names are the
// environment variables an operator would have edited, so the log line points
// at the thing they just touched.
func restartRequiredChanges(old, next config.Config) []string {
	var changed []string
	add := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}

	// Path guard and data locations are captured once in New.
	add("MCP_ROOT (path guard)", old.RootPath != next.RootPath)
	add("MCP_ALLOW_DIRS", !reflect.DeepEqual(old.AllowedPaths, next.AllowedPaths))
	add("MCP_DATA_PATH", old.DataPath != next.DataPath)
	add("MCP_LOG_PATH", old.LogPath != next.LogPath)
	add("MCP_MEMORY_DB_PATH", old.Memory.DBPath != next.Memory.DBPath)

	// Session tracking is wired into a live tracker holding an open session.
	add("MCP_SESSION_TRACKING_ENABLED", old.Session.TrackingEnabled != next.Session.TrackingEnabled)
	add("MCP_SESSION_IDLE_TIMEOUT", old.Session.IdleTimeout != next.Session.IdleTimeout)
	add("MCP_SESSION_CHECKPOINT_INTERVAL", old.Session.CheckpointInterval != next.Session.CheckpointInterval)
	add("MCP_SESSION_MIN_EVENTS", old.Session.MinEvents != next.Session.MinEvents)
	add("SEMA_MCP_SUPPRESS_REVIEW_QUEUE_WRITES", old.Session.SuppressReviewQueueWrites != next.Session.SuppressReviewQueueWrites)

	// Schedulers own a running goroutine started with their interval.
	add("MCP_SEDIMENT_ENABLED", old.Sediment.Enabled != next.Sediment.Enabled)
	add("MCP_SEDIMENT_SCHEDULE_INTERVAL", old.Sediment.ScheduleInterval != next.Sediment.ScheduleInterval)
	add("MCP_ARCHIVE_SWEEP_ENABLED", old.Lifecycle.ArchiveSweepEnabled != next.Lifecycle.ArchiveSweepEnabled)
	add("MCP_ARCHIVE_SWEEP_INTERVAL", old.Lifecycle.ArchiveSweepInterval != next.Lifecycle.ArchiveSweepInterval)
	add("MCP_STEWARD_ENABLED", old.Steward.Enabled != next.Steward.Enabled)

	// The embedder and its provider chain are built once.
	add("MCP_EMBEDDING_MODE", old.Embeddings.Mode != next.Embeddings.Mode)
	add("MCP_EMBEDDING_TIMEOUT", old.Embeddings.Timeout != next.Embeddings.Timeout)
	add("MCP_EMBEDDING_MAX_RETRIES", old.Embeddings.MaxRetries != next.Embeddings.MaxRetries)

	// The HTTP listener is bound at startup.
	add("MCP_HTTP_MODE", old.HTTP.Mode != next.HTTP.Mode)
	add("MCP_HTTP_HOST", old.HTTP.Host != next.HTTP.Host)
	add("MCP_HTTP_PORT", old.HTTP.Port != next.HTTP.Port)
	add("MCP_HTTP_AUTH_TOKEN", old.HTTP.AuthToken != next.HTTP.AuthToken)

	// The tool list is answered from the flag captured at initialize time.
	add("MCP_TOOL_GROUPING", old.ToolGrouping != next.ToolGrouping)

	// Hook dedup settings are baked into the session tracker's dedup config.
	add("MCP_CHECKPOINT_DEDUP_DISABLED", old.HooksDedup.Disabled != next.HooksDedup.Disabled)
	add("MCP_CHECKPOINT_DEDUP_THRESHOLD", old.HooksDedup.Threshold != next.HooksDedup.Threshold)
	add("MCP_CHECKPOINT_DEDUP_MIN_CHARS", old.HooksDedup.MinChars != next.HooksDedup.MinChars)
	add("MCP_CHECKPOINT_DEDUP_WINDOW", old.HooksDedup.Window != next.HooksDedup.Window)

	return changed
}
