package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"go.uber.org/zap"
)

const autoSessionOrigin = "background_auto"

type sessionTracker struct {
	store              *memory.Store
	closeService       *sessionclose.Service
	fileLogger         *logger.FileLogger
	idleTimeout        time.Duration
	checkpointInterval time.Duration
	minEvents          int
	now                func() time.Time
	ctx                context.Context
	cancel             context.CancelFunc

	mu      sync.Mutex
	timer   *time.Timer
	current *trackedSession
	closed  bool
}

type trackedSession struct {
	startedAt        time.Time
	lastActivityAt   time.Time
	lastCheckpointAt time.Time
	context          string
	service          string
	mode             memory.SessionMode
	tags             []string
	activities       []trackedActivity
}

type trackedActivity struct {
	Tool string
	Line string
	At   time.Time
}

type sessionNotification struct {
	Event    string            `json:"event"`
	Summary  string            `json:"summary,omitempty"`
	Context  string            `json:"context,omitempty"`
	Service  string            `json:"service,omitempty"`
	Mode     string            `json:"mode,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func newSessionTracker(cfg config.Config, store *memory.Store, fileLogger *logger.FileLogger) *sessionTracker {
	if store == nil || !cfg.SessionTrackingEnabled {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionTracker{
		store:              store,
		closeService:       sessionclose.New(store),
		fileLogger:         fileLogger,
		idleTimeout:        cfg.SessionIdleTimeout,
		checkpointInterval: cfg.SessionCheckpointInterval,
		minEvents:          cfg.SessionMinEvents,
		now:                time.Now,
		ctx:                ctx,
		cancel:             cancel,
	}
}

func (st *sessionTracker) HandleToolCall(name string, args map[string]any, rErr *rpcError) {
	if st == nil || rErr != nil {
		return
	}
	if st.handleManualSessionBoundary(name, args) {
		return
	}

	activity, ok := st.buildActivity(name, args)
	if !ok {
		return
	}

	now := st.now()
	var checkpoint *trackedSession

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	if st.current == nil {
		st.current = &trackedSession{
			startedAt:        now,
			lastActivityAt:   now,
			lastCheckpointAt: now,
		}
	}
	session := st.current
	session.lastActivityAt = now
	if session.startedAt.IsZero() {
		session.startedAt = now
	}
	if session.lastCheckpointAt.IsZero() {
		session.lastCheckpointAt = now
	}
	if session.context == "" && activityContext(args) != "" {
		session.context = activityContext(args)
	}
	if session.service == "" && activityService(args) != "" {
		session.service = activityService(args)
	}
	if activityMode := activitySessionMode(name, args); session.mode == "" && activityMode != "" {
		session.mode = activityMode
	}
	session.tags = memory.NormalizeTags(append(session.tags, activityTags(name, args)...))
	if len(session.activities) == 0 || session.activities[len(session.activities)-1].Line != activity.Line {
		session.activities = append(session.activities, trackedActivity{
			Tool: name,
			Line: activity.Line,
			At:   now,
		})
		// Cap activities to prevent unbounded memory growth in long sessions.
		const maxActivities = 1000
		if len(session.activities) > maxActivities {
			session.activities = session.activities[len(session.activities)-maxActivities:]
		}
	}
	st.resetIdleTimerLocked()
	if st.shouldCheckpointLocked(now) {
		checkpoint = cloneTrackedSession(session)
		session.lastCheckpointAt = now
	}
	st.mu.Unlock()

	if checkpoint != nil {
		st.saveCheckpoint(checkpoint)
	}
}

func (st *sessionTracker) HandleNotification(method string, params json.RawMessage) {
	if st == nil {
		return
	}
	switch strings.TrimSpace(method) {
	case "initialized", "notifications/initialized":
		return
	case "notifications/session_event", "session_event":
	default:
		return
	}

	var event sessionNotification
	if err := json.Unmarshal(params, &event); err != nil {
		st.logWarn("failed to parse session notification", zap.String("method", method), zap.Error(err))
		return
	}
	st.handleSessionNotification(event)
}

func (st *sessionTracker) Close() {
	if st == nil {
		return
	}

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	session := cloneTrackedSession(st.current)
	st.current = nil
	st.mu.Unlock()

	st.flushSession("shutdown", session)
	st.cancel()
}

func (st *sessionTracker) handleManualSessionBoundary(name string, args map[string]any) bool {
	switch name {
	case "review_session_changes":
		return true
	case "accept_session_changes":
		st.reset()
		return true
	case "close_session", "analyze_session":
		if sessionToolWrites(args) {
			st.reset()
		}
		return true
	default:
		return false
	}
}

func (st *sessionTracker) handleSessionNotification(event sessionNotification) {
	switch normalizeSessionEvent(event.Event) {
	case "reset":
		st.reset()
	case "checkpoint":
		st.forceCheckpoint(event)
	case "task_done", "final_summary":
		st.flushWithNotification(normalizeSessionEvent(event.Event), event)
	default:
		st.logWarn("ignored unknown session event", zap.String("event", event.Event))
	}
}

func (st *sessionTracker) reset() {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.current = nil
}

func (st *sessionTracker) flushFromIdle() {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	session := cloneTrackedSession(st.current)
	st.current = nil
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.mu.Unlock()

	st.flushSession("idle_timeout", session)
}

func (st *sessionTracker) flushWithNotification(boundary string, event sessionNotification) {
	now := st.now()

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	session := cloneTrackedSession(st.current)
	if session == nil {
		session = &trackedSession{
			startedAt:        now,
			lastActivityAt:   now,
			lastCheckpointAt: now,
		}
	}
	applySessionNotification(session, event, now)
	st.current = nil
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.mu.Unlock()

	st.flushSession(boundary, session)
}

func (st *sessionTracker) forceCheckpoint(event sessionNotification) {
	now := st.now()

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	session := cloneTrackedSession(st.current)
	if session == nil {
		session = &trackedSession{
			startedAt:        now,
			lastActivityAt:   now,
			lastCheckpointAt: now,
		}
	}
	applySessionNotification(session, event, now)
	if st.current != nil {
		st.current.lastCheckpointAt = now
	}
	st.mu.Unlock()

	st.saveCheckpoint(session)
}

func (st *sessionTracker) flushSession(boundary string, session *trackedSession) {
	if st == nil || session == nil || !hasEnoughTrackedMaterial(session, st.minEvents) {
		return
	}

	summary := session.summary(boundary)
	result, err := st.closeService.Analyze(st.ctx, sessionclose.AnalyzeRequest{
		Summary:          summary,
		DryRun:           false,
		SaveRaw:          true,
		AutoApplyLowRisk: true,
	})
	if err != nil {
		st.logWarn("background session close failed", zap.String("boundary", boundary), zap.Error(err))
		return
	}
	if err := st.persistReviewQueue(boundary, result); err != nil {
		st.logWarn("background review queue persistence failed", zap.String("boundary", boundary), zap.Error(err))
		return
	}

	st.logInfo("background session consolidated",
		zap.String("boundary", boundary),
		zap.String("mode", string(summary.Mode)),
		zap.String("context", summary.Context),
		zap.String("service", summary.Service),
		zap.Int("activities", len(session.activities)),
		zap.Int("review_items", result.Review.PendingCount),
	)
}

func (st *sessionTracker) saveCheckpoint(session *trackedSession) {
	if st == nil || session == nil || !hasEnoughTrackedMaterial(session, st.minEvents) {
		return
	}

	summary := session.summary("checkpoint")
	if _, err := st.closeService.SaveRawSummaryWithOptions(st.ctx, summary, sessionclose.RawSaveOptions{
		RecordKind: memory.RecordKindSessionCheckpoint,
		ExtraTags:  []string{"session-checkpoint"},
		Metadata: map[string]string{
			memory.MetadataSessionBoundary: "checkpoint",
			memory.MetadataSessionOrigin:   autoSessionOrigin,
		},
	}); err != nil {
		st.logWarn("background session checkpoint failed", zap.Error(err))
	}
}

func (st *sessionTracker) persistReviewQueue(boundary string, result *sessionclose.AnalysisResult) error {
	if st == nil || result == nil {
		return nil
	}

	for _, action := range result.Actions {
		if action.Kind == sessionclose.ActionRawOnly || action.State != sessionclose.ActionStateReviewRequired {
			continue
		}

		tags := memory.BuildEngineeringTags(
			action.EngineeringType,
			result.Summary.Service,
			"",
			"review_required",
			true,
			append(result.Summary.Tags, "review-queue", "session-close-review", "action:"+string(action.Kind), "handling:"+string(action.Handling)),
		)
		metadata := memory.BuildEngineeringMetadata(
			action.EngineeringType,
			result.Summary.Service,
			"",
			"review_required",
			true,
			map[string]string{
				memory.MetadataRecordKind:      memory.RecordKindReviewQueueItem,
				memory.MetadataSessionMode:     string(result.Summary.Mode),
				memory.MetadataSessionBoundary: boundary,
				memory.MetadataSessionOrigin:   autoSessionOrigin,
				memory.MetadataActionKind:      string(action.Kind),
				memory.MetadataActionHandling:  string(action.Handling),
				memory.MetadataReviewReason:    action.Rationale,
			},
		)
		if result.RawSummarySaved != "" {
			metadata[memory.MetadataSourceSessionID] = result.RawSummarySaved
			metadata[memory.MetadataDerivedFrom] = memory.RecordKindSessionSummary
		}

		mem := &memory.Memory{
			Title:      reviewQueueTitle(result.Summary, action),
			Content:    reviewQueueContent(action),
			Type:       memory.TypeWorking,
			Context:    result.Summary.Context,
			Importance: reviewQueueImportance(action),
			Tags:       tags,
			Metadata:   metadata,
		}
		if err := st.store.Store(st.ctx, mem); err != nil {
			return err
		}
	}
	return nil
}

func (st *sessionTracker) resetIdleTimerLocked() {
	if st.idleTimeout <= 0 {
		return
	}
	if st.timer == nil {
		st.timer = time.AfterFunc(st.idleTimeout, st.flushFromIdle)
		return
	}
	st.timer.Reset(st.idleTimeout)
}

func (st *sessionTracker) shouldCheckpointLocked(now time.Time) bool {
	if st.checkpointInterval <= 0 || st.current == nil || len(st.current.activities) == 0 {
		return false
	}
	return now.Sub(st.current.lastCheckpointAt) >= st.checkpointInterval
}

func (st *sessionTracker) buildActivity(name string, args map[string]any) (trackedActivity, bool) {
	line := buildActivityLine(name, args)
	if strings.TrimSpace(line) == "" {
		return trackedActivity{}, false
	}
	return trackedActivity{
		Tool: name,
		Line: line,
	}, true
}

func buildActivityLine(name string, args map[string]any) string {
	switch name {
	case "store_decision":
		return prefixedActivity("Decision", firstNonEmpty(trimArg(args, "decision"), trimArg(args, "title")))
	case "store_incident":
		return prefixedActivity("Incident", firstNonEmpty(trimArg(args, "summary"), trimArg(args, "title")))
	case "store_runbook":
		return prefixedActivity("Runbook", firstNonEmpty(trimArg(args, "procedure"), trimArg(args, "title")))
	case "store_postmortem":
		return prefixedActivity("Postmortem", firstNonEmpty(trimArg(args, "summary"), trimArg(args, "title")))
	case "store_memory":
		return buildGenericMemoryActivity(args)
	case "update_memory":
		return prefixedActivity("Updated knowledge", firstNonEmpty(trimArg(args, "content"), trimArg(args, "title"), trimArg(args, "id")))
	case "merge_duplicates":
		return prefixedActivity("Merged duplicates", firstNonEmpty(trimArg(args, "primary_id"), trimArg(args, "duplicate_ids")))
	case "mark_outdated":
		return prefixedActivity("Marked outdated", firstNonEmpty(trimArg(args, "id"), trimArg(args, "reason")))
	case "promote_to_canonical":
		return prefixedActivity("Promoted canonical", trimArg(args, "id"))
	case "search_runbooks":
		return prefixedActivity("Runbook search", trimArg(args, "query"))
	case "recall_similar_incidents":
		return prefixedActivity("Incident investigation", trimArg(args, "query"))
	case "summarize_project_context":
		return prefixedActivity("Project context", firstNonEmpty(trimArg(args, "focus"), trimArg(args, "context"), trimArg(args, "service")))
	case "semantic_search":
		return prefixedActivity("Document search", trimArg(args, "query"))
	case "recall_memory":
		return prefixedActivity("Memory recall", trimArg(args, "query"))
	case "repo_read":
		return prefixedActivity("Inspected file", trimArg(args, "path"))
	case "repo_search":
		return prefixedActivity("Repo search", firstNonEmpty(trimArg(args, "query"), trimArg(args, "path")))
	case "repo_list":
		return prefixedActivity("Listed repo path", trimArg(args, "path"))
	case "project_bank_view":
		return prefixedActivity("Project bank review", firstNonEmpty(trimArg(args, "view"), trimArg(args, "context"), trimArg(args, "service")))
	default:
		return ""
	}
}

func buildGenericMemoryActivity(args map[string]any) string {
	content := firstNonEmpty(trimArg(args, "content"), trimArg(args, "title"))
	if content == "" {
		return ""
	}
	entity := ""
	metadata := getStringMap(args, "metadata")
	if len(metadata) > 0 {
		entity = strings.TrimSpace(metadata[memory.MetadataEntity])
	}
	if entity == "" {
		for _, tag := range getStringSlice(args, "tags") {
			if value, err := memory.ValidateEngineeringType(tag, true); err == nil && value != "" {
				entity = string(value)
				break
			}
		}
	}
	switch entity {
	case string(memory.EngineeringTypeDecision):
		return prefixedActivity("Decision", content)
	case string(memory.EngineeringTypeIncident):
		return prefixedActivity("Incident", content)
	case string(memory.EngineeringTypeRunbook), string(memory.EngineeringTypeProcedure):
		return prefixedActivity("Runbook", content)
	case string(memory.EngineeringTypeMigrationNote):
		return prefixedActivity("Migration", content)
	case string(memory.EngineeringTypeCaveat):
		return prefixedActivity("Caveat", content)
	default:
		return prefixedActivity("Stored memory", content)
	}
}

func prefixedActivity(prefix string, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s", prefix, truncateText(body, 220))
}

func activityContext(args map[string]any) string {
	return trimArg(args, "context")
}

func activityService(args map[string]any) string {
	return trimArg(args, "service")
}

func activityTags(name string, args map[string]any) []string {
	tags := memory.NormalizeTags(getStringSlice(args, "tags"))
	switch name {
	case "store_incident", "recall_similar_incidents":
		tags = append(tags, "mode:incident")
	case "store_postmortem":
		tags = append(tags, "mode:incident")
	case "store_runbook", "search_runbooks":
		tags = append(tags, "mode:coding")
	}
	return memory.NormalizeTags(tags)
}

func activitySessionMode(name string, args map[string]any) memory.SessionMode {
	if modeValue := trimArg(args, "mode"); modeValue != "" {
		if mode, err := memory.ValidateSessionMode(modeValue, ""); err == nil {
			return mode
		}
	}
	switch name {
	case "store_incident", "store_postmortem", "recall_similar_incidents":
		return memory.SessionModeIncident
	default:
		return ""
	}
}

func sessionToolWrites(args map[string]any) bool {
	if dryRun, ok := getBool(args, "dry_run"); ok && !dryRun {
		return true
	}
	if saveRaw, ok := getBool(args, "save_raw"); ok && saveRaw {
		return true
	}
	if autoApply, ok := getBool(args, "auto_apply_low_risk"); ok && autoApply {
		return true
	}
	return false
}

func hasEnoughTrackedMaterial(session *trackedSession, minEvents int) bool {
	if session == nil || len(session.activities) == 0 {
		return false
	}
	if len(session.activities) >= minEvents {
		return true
	}

	totalLen := 0
	for _, activity := range session.activities {
		totalLen += len(activity.Line)
		switch activity.Tool {
		case "store_decision",
			"store_incident",
			"store_runbook",
			"store_postmortem",
			"store_memory",
			"update_memory",
			"merge_duplicates",
			"mark_outdated",
			"promote_to_canonical":
			return true
		}
	}
	return totalLen >= 120
}

func cloneTrackedSession(session *trackedSession) *trackedSession {
	if session == nil {
		return nil
	}
	clone := *session
	clone.tags = append([]string(nil), session.tags...)
	clone.activities = append([]trackedActivity(nil), session.activities...)
	return &clone
}

func applySessionNotification(session *trackedSession, event sessionNotification, now time.Time) {
	if session == nil {
		return
	}
	session.context = firstNonEmpty(event.Context, session.context)
	session.service = firstNonEmpty(event.Service, session.service)
	if session.mode == "" {
		if mode, err := memory.ValidateSessionMode(event.Mode, ""); err == nil {
			session.mode = mode
		}
	}
	session.tags = memory.NormalizeTags(append(session.tags, event.Tags...))
	lines := splitNotificationSummary(event.Summary)
	for _, line := range lines {
		session.activities = append(session.activities, trackedActivity{
			Tool: "notification:" + normalizeSessionEvent(event.Event),
			Line: line,
			At:   now,
		})
	}
	if session.startedAt.IsZero() {
		session.startedAt = now
	}
	session.lastActivityAt = now
	if session.lastCheckpointAt.IsZero() {
		session.lastCheckpointAt = now
	}
}

func (ts *trackedSession) summary(boundary string) memory.SessionSummary {
	lines := make([]string, 0, len(ts.activities))
	for _, activity := range ts.activities {
		lines = append(lines, "- "+activity.Line)
	}
	metadata := map[string]string{
		memory.MetadataSessionBoundary: boundary,
		memory.MetadataSessionOrigin:   autoSessionOrigin,
	}
	return memory.SessionSummary{
		Mode:      ts.mode,
		Context:   ts.context,
		Service:   ts.service,
		Summary:   strings.Join(lines, "\n"),
		StartedAt: ts.startedAt,
		EndedAt:   ts.lastActivityAt,
		Tags:      memory.NormalizeTags(append([]string{"auto-session"}, ts.tags...)),
		Metadata:  metadata,
	}
}

func reviewQueueTitle(summary memory.SessionSummary, action sessionclose.CandidateAction) string {
	base := firstNonEmpty(action.Title, string(action.Kind))
	parts := []string{"Review queue", base}
	if summary.Service != "" {
		parts = append(parts, summary.Service)
	}
	return strings.Join(parts, " / ")
}

func reviewQueueContent(action sessionclose.CandidateAction) string {
	lines := []string{
		fmt.Sprintf("Action: %s", action.Kind),
		fmt.Sprintf("Handling: %s", action.Handling),
	}
	if action.TargetTitle != "" {
		lines = append(lines, fmt.Sprintf("Target: %s", action.TargetTitle))
	} else if action.TargetMemoryID != "" {
		lines = append(lines, fmt.Sprintf("Target memory: %s", action.TargetMemoryID))
	}
	if action.Rationale != "" {
		lines = append(lines, fmt.Sprintf("Why: %s", action.Rationale))
	}
	if len(action.DecisionTrace) > 0 {
		lines = append(lines, fmt.Sprintf("Trace: %s", strings.Join(action.DecisionTrace, ", ")))
	}
	if action.Candidate != nil && strings.TrimSpace(action.Candidate.Content) != "" {
		lines = append(lines, fmt.Sprintf("Candidate: %s", truncateText(strings.TrimSpace(action.Candidate.Content), 220)))
	}
	return strings.Join(lines, "\n")
}

func reviewQueueImportance(action sessionclose.CandidateAction) float64 {
	switch action.Handling {
	case sessionclose.ActionHandlingHardReview:
		return 0.55
	case sessionclose.ActionHandlingSoftReview:
		return 0.40
	default:
		return 0.35
	}
}

func normalizeSessionEvent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func splitNotificationSummary(summary string) []string {
	summary = strings.ReplaceAll(summary, "\r\n", "\n")
	lines := strings.Split(summary, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*• \t"))
		if line == "" {
			continue
		}
		out = append(out, truncateText(line, 220))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func trimArg(args map[string]any, key string) string {
	value, _ := getString(args, key)
	return strings.TrimSpace(value)
}

func (st *sessionTracker) logInfo(msg string, fields ...zap.Field) {
	if st == nil || st.fileLogger == nil {
		return
	}
	st.fileLogger.Info(msg, fields...)
}

func (st *sessionTracker) logWarn(msg string, fields ...zap.Field) {
	if st == nil || st.fileLogger == nil {
		return
	}
	st.fileLogger.Warn(msg, fields...)
}
