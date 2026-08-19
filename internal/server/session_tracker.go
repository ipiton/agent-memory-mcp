package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/hooks"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"go.uber.org/zap"
)

const autoSessionOrigin = "background_auto"

type sessionTracker struct {
	store               *memory.Store
	closeService        *sessionclose.Service
	fileLogger          *logger.FileLogger
	idleTimeout         time.Duration
	checkpointInterval  time.Duration
	minEvents           int
	suppressReviewQueue bool
	dedupCfg            hooks.DedupConfig
	now                 func() time.Time
	ctx                 context.Context
	cancel              context.CancelFunc
	onSessionClose      func() // optional callback after session close

	mu      sync.Mutex
	timer   *time.Timer
	current *trackedSession
	closed  bool

	// Round 3 M10: checkpoints used to run synchronously in HandleToolCall,
	// adding a DB-write + embedder-call latency tax to every tool response
	// that landed on a checkpoint boundary. checkpointSem bounds the
	// concurrent checkpoint goroutines (drops the request if the worker
	// pool is full — the next tool call will retrigger), and
	// checkpointWG lets Close() drain in-flight checkpoints cleanly.
	checkpointSem chan struct{}
	checkpointWG  sync.WaitGroup

	// flushWG tracks in-flight flushSession goroutines (idle timer,
	// notifications). Tests use waitForBackground to drain deterministically
	// instead of guessing time.Sleep durations.
	flushWG sync.WaitGroup
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

// newTrackedSession constructs a fresh trackedSession with all three
// timestamp fields stamped to the same `now`. Centralises the start/activity/
// checkpoint trio that was inlined three times in flush/checkpoint paths.
func newTrackedSession(now time.Time) *trackedSession {
	return &trackedSession{
		startedAt:        now,
		lastActivityAt:   now,
		lastCheckpointAt: now,
	}
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
	if store == nil || !cfg.Session.TrackingEnabled {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionTracker{
		store:               store,
		closeService:        sessionclose.New(store),
		fileLogger:          fileLogger,
		idleTimeout:         cfg.Session.IdleTimeout,
		checkpointInterval:  cfg.Session.CheckpointInterval,
		minEvents:           cfg.Session.MinEvents,
		suppressReviewQueue: cfg.Session.SuppressReviewQueueWrites,
		dedupCfg: hooks.NewDedupConfig(
			cfg.HooksDedup.Disabled,
			cfg.HooksDedup.Threshold,
			cfg.HooksDedup.MinChars,
			cfg.HooksDedup.Window,
		),
		now:           time.Now,
		ctx:           ctx,
		cancel:        cancel,
		checkpointSem: make(chan struct{}, 2),
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
		st.current = newTrackedSession(now)
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

	// Drain in-flight async checkpoints (Round 3 M10) BEFORE flush + cancel
	// so they observe an uncancelled context and don't write into a closing
	// store.
	st.checkpointWG.Wait()

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
		st.forceCheckpoint(event, "checkpoint")
	case "pre_compact":
		st.forceCheckpoint(event, "pre_compact")
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
	st.flushWG.Add(1)
	defer st.flushWG.Done()

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
		session = newTrackedSession(now)
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

func (st *sessionTracker) forceCheckpoint(event sessionNotification, boundary string) {
	now := st.now()

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	session := cloneTrackedSession(st.current)
	if session == nil {
		session = newTrackedSession(now)
	}
	applySessionNotification(session, event, now)
	if st.current != nil {
		st.current.lastCheckpointAt = now
	}
	st.mu.Unlock()

	st.saveCheckpointWithBoundary(session, boundary)
}

func (st *sessionTracker) flushSession(boundary string, session *trackedSession) {
	if st == nil || session == nil || !hasEnoughTrackedMaterial(session, st.minEvents) {
		return
	}

	// Round 3 M11: shutdown flushes must NOT inherit st.ctx, which Close()
	// has already cancelled by the time this runs from the shutdown path.
	// Use a fresh background context with a generous timeout so a slow
	// embedder/LLM doesn't hang the shutdown indefinitely while still
	// giving the consolidation time to land.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	summary := session.summary(boundary)
	result, err := st.closeService.Analyze(ctx, sessionclose.AnalyzeRequest{
		Summary:          summary,
		DryRun:           false,
		SaveRaw:          true,
		AutoApplyLowRisk: true,
	})
	if err != nil {
		st.logWarn("background session close failed", zap.String("boundary", boundary), zap.Error(err))
		return
	}
	if err := st.persistReviewQueue(ctx, boundary, result); err != nil {
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

	// Notify steward scheduler about session close event.
	if st.onSessionClose != nil {
		go st.onSessionClose()
	}
}

// waitForCheckpoints drains all in-flight async checkpoints. Tests use it
// to deterministically assert on checkpoint side effects without resorting
// to time.Sleep guesswork.
func (st *sessionTracker) waitForCheckpoints() {
	if st == nil {
		return
	}
	st.checkpointWG.Wait()
}

// waitForBackground drains both checkpoint workers and idle-timer-driven
// flushSession goroutines. Tests should call this instead of sleeping
// before asserting on side effects of flushFromIdle / saveCheckpoint.
//
// An armed idle timer has not run flushFromIdle yet, so flushWG is still at
// zero and Wait would return before the flush it is supposed to wait for even
// starts. Worse, the Add(1) that flushFromIdle then performs lands next to a
// zero-counter Wait — a WaitGroup misuse the race detector reports, which is
// how this surfaced: a CI runner slow enough to push a 20ms timer past the
// caller's sleep. So the timer is drained first, and only then the group.
func (st *sessionTracker) waitForBackground() {
	if st == nil {
		return
	}
	st.checkpointWG.Wait()
	st.waitForIdleTimer()
	st.flushWG.Wait()
}

// waitForIdleTimer blocks until no idle timer is armed — flushFromIdle clears
// it once it fires, and the cancel paths clear it when the flush will never
// happen. The deadline keeps a caller that kept the session busy (and so kept
// resetting the timer) from hanging instead of failing.
func (st *sessionTracker) waitForIdleTimer() {
	// Wall clock on purpose: this waits on a real timer, not on the tracker's
	// injectable notion of "now".
	deadline := time.Now().Add(2 * time.Second)
	for {
		st.mu.Lock()
		armed := st.timer != nil
		st.mu.Unlock()
		if !armed || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// saveCheckpoint is the async entry point used from the HandleToolCall hot
// path. It dispatches to a bounded worker pool so a slow embedder/DB write
// can't add latency to the tool-call response. If the pool is saturated
// (cap=2), the checkpoint is dropped — the next tool call will retrigger.
// Round 3 M10.
func (st *sessionTracker) saveCheckpoint(session *trackedSession) {
	if st == nil || st.checkpointSem == nil {
		st.saveCheckpointWithBoundary(session, "checkpoint")
		return
	}
	select {
	case st.checkpointSem <- struct{}{}:
	default:
		st.logWarn("background checkpoint dropped: worker pool busy")
		return
	}

	// T88 M4: register with the WaitGroup under the same mutex that guards
	// `closed`. The caller dropped st.mu before getting here, so Close could
	// otherwise slip in between — pass its checkpointWG.Wait() drain, cancel
	// st.ctx, and leave this goroutine to write the last checkpoint on a dead
	// context. Now either Close waits for it, or it sees closed and declines
	// (Close flushes the session itself).
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		<-st.checkpointSem
		return
	}
	st.checkpointWG.Add(1)
	st.mu.Unlock()

	go func() {
		defer st.checkpointWG.Done()
		defer func() { <-st.checkpointSem }()
		st.saveCheckpointWithBoundary(session, "checkpoint")
	}()
}

func (st *sessionTracker) saveCheckpointWithBoundary(session *trackedSession, boundary string) {
	if st == nil || session == nil || !hasEnoughTrackedMaterial(session, st.minEvents) {
		return
	}

	summary := session.summary(boundary)

	// Mirror the CLI hook dedup gate (T45) so the in-process auto-session
	// pipeline does not flood the store with near-identical session-checkpoint
	// records on every boundary tick. Fail-open on unexpected errors.
	if dedup, err := hooks.Check(st.ctx, st.store, summary, st.dedupCfg); err != nil {
		st.logWarn("background session checkpoint dedup check failed", zap.String("boundary", boundary), zap.Error(err))
	} else if dedup.Skip {
		st.store.IncrementDedupSkipped(dedup.Reason)
		return
	}

	tags := []string{"session-checkpoint"}
	if boundary != "checkpoint" {
		tags = append(tags, boundary)
	}

	if _, err := st.closeService.SaveRawSummaryWithOptions(st.ctx, summary, sessionclose.RawSaveOptions{
		RecordKind: memory.RecordKindSessionCheckpoint,
		ExtraTags:  tags,
		Metadata: map[string]string{
			memory.MetadataSessionBoundary: boundary,
			memory.MetadataSessionOrigin:   autoSessionOrigin,
		},
	}); err != nil {
		st.logWarn("background session checkpoint failed", zap.String("boundary", boundary), zap.Error(err))
	}
}

func (st *sessionTracker) persistReviewQueue(ctx context.Context, boundary string, result *sessionclose.AnalysisResult) error {
	if st == nil || result == nil {
		return nil
	}

	// Config-controlled suppression: when SEMA_MCP_SUPPRESS_REVIEW_QUEUE_WRITES=1,
	// skip persisting auto-generated review queue items as working memories. They
	// accumulate as noise (typically 5-10 per session_close, low-importance
	// 0.35–0.55) and pollute recall. Caller can still get them via close_session
	// result.Actions in-memory. Resolved at config load (T89 H5) rather than read
	// from the process environment, which stopped carrying .env values.
	if st.suppressReviewQueue {
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
		if err := st.store.Store(ctx, mem); err != nil {
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
