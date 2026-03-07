package memory

import "testing"

func TestValidateProjectBankViewAcceptsOverviewAlias(t *testing.T) {
	got, err := ValidateProjectBankView("overview")
	if err != nil {
		t.Fatalf("ValidateProjectBankView: %v", err)
	}
	if got != ProjectBankViewCanonicalOverview {
		t.Fatalf("view = %q, want %q", got, ProjectBankViewCanonicalOverview)
	}
}

func TestProjectBankViewBuildsOverviewSections(t *testing.T) {
	store := newTestStore(t)

	decision := &Memory{
		Title:      "Disable HPA",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.85,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			MetadataEntity:  string(EngineeringTypeDecision),
			MetadataService: "api",
			MetadataStatus:  "accepted",
		},
	}
	if err := store.Store(decision); err != nil {
		t.Fatalf("Store decision: %v", err)
	}
	if _, err := store.PromoteToCanonical(decision.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	runbook := &Memory{
		Title:      "Rollback api",
		Content:    "Runbook: Roll back api deployment and verify health.",
		Type:       TypeProcedural,
		Context:    "payments",
		Importance: 0.80,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			MetadataEntity:  string(EngineeringTypeRunbook),
			MetadataService: "api",
			MetadataStatus:  "accepted",
		},
	}
	if err := store.Store(runbook); err != nil {
		t.Fatalf("Store runbook: %v", err)
	}

	session := &Memory{
		Title:      "Session close / payments / api",
		Content:    "Migration summary for payments api rollout.",
		Type:       TypeEpisodic,
		Context:    "payments",
		Importance: 0.20,
		Tags:       []string{"session-summary", "service:api"},
		Metadata: map[string]string{
			MetadataRecordKind:  RecordKindSessionSummary,
			MetadataService:     "api",
			MetadataSessionMode: string(SessionModeMigration),
		},
	}
	if err := store.Store(session); err != nil {
		t.Fatalf("Store session summary: %v", err)
	}

	caveat := &Memory{
		Title:      "Known rollout caveat",
		Content:    "Caveat: verification is still manual after rollout.",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.60,
		Tags:       []string{"caveat", "service:api"},
		Metadata: map[string]string{
			MetadataEntity:          string(EngineeringTypeCaveat),
			MetadataService:         "api",
			MetadataLifecycleStatus: string(LifecycleOutdated),
			MetadataReviewRequired:  "true",
		},
	}
	if err := store.Store(caveat); err != nil {
		t.Fatalf("Store caveat: %v", err)
	}

	reviewItem := &Memory{
		Title:      "Review queue / Replace rollback runbook / api",
		Content:    "Action: merge\nHandling: hard_review\nWhy: candidate overlaps strongly with an existing engineering item and should be reviewed for merge",
		Type:       TypeWorking,
		Context:    "payments",
		Importance: 0.45,
		Tags:       []string{"review-queue", "service:api", "runbook"},
		Metadata: map[string]string{
			MetadataEntity:         string(EngineeringTypeRunbook),
			MetadataService:        "api",
			MetadataRecordKind:     RecordKindReviewQueueItem,
			MetadataReviewRequired: "true",
			MetadataStatus:         "review_required",
			MetadataSessionMode:    string(SessionModeIncident),
		},
	}
	if err := store.Store(reviewItem); err != nil {
		t.Fatalf("Store review queue item: %v", err)
	}

	view, err := store.ProjectBankView(ProjectBankViewCanonicalOverview, ProjectBankOptions{
		Filters: Filters{Context: "payments"},
		Service: "api",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView: %v", err)
	}

	if view.View != ProjectBankViewCanonicalOverview {
		t.Fatalf("view = %q, want %q", view.View, ProjectBankViewCanonicalOverview)
	}
	if view.SectionCounts["canonical_knowledge"] != 1 {
		t.Fatalf("canonical_knowledge = %d, want 1", view.SectionCounts["canonical_knowledge"])
	}
	if view.SectionCounts["recent_session_deltas"] != 1 {
		t.Fatalf("recent_session_deltas = %d, want 1", view.SectionCounts["recent_session_deltas"])
	}
	if view.SectionCounts["review_queue"] != 1 {
		t.Fatalf("review_queue = %d, want 1", view.SectionCounts["review_queue"])
	}
	if view.SectionCounts["needs_review_or_refresh"] != 1 {
		t.Fatalf("needs_review_or_refresh = %d, want 1", view.SectionCounts["needs_review_or_refresh"])
	}
	if view.EntityCounts["decisions"] != 1 {
		t.Fatalf("decisions count = %d, want 1", view.EntityCounts["decisions"])
	}
	if view.EntityCounts["runbooks"] != 1 {
		t.Fatalf("runbooks count = %d, want 1", view.EntityCounts["runbooks"])
	}
	if view.EntityCounts["caveats"] != 1 {
		t.Fatalf("caveats count = %d, want 1", view.EntityCounts["caveats"])
	}
}

func TestProjectBankViewFiltersByOwnerStatusAndService(t *testing.T) {
	store := newTestStore(t)

	canonicalDecision := &Memory{
		Title:      "Use maintenance window",
		Content:    "Decision: Use a maintenance window for payments-db cutover accepted.",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.80,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			MetadataEntity:  string(EngineeringTypeDecision),
			MetadataService: "payments-db",
			MetadataStatus:  "accepted",
		},
	}
	if err := store.Store(canonicalDecision); err != nil {
		t.Fatalf("Store canonical decision: %v", err)
	}
	if _, err := store.PromoteToCanonical(canonicalDecision.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	rawDecision := &Memory{
		Title:      "Temporary rollout note",
		Content:    "Decision: Disable background workers during cutover.",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			MetadataEntity:  string(EngineeringTypeDecision),
			MetadataService: "payments-db",
			MetadataStatus:  "draft",
		},
	}
	if err := store.Store(rawDecision); err != nil {
		t.Fatalf("Store raw decision: %v", err)
	}

	otherService := &Memory{
		Title:      "Catalog decision",
		Content:    "Decision: Use smaller batch size for catalog imports.",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.65,
		Tags:       []string{"decision", "service:catalog"},
		Metadata: map[string]string{
			MetadataEntity:  string(EngineeringTypeDecision),
			MetadataService: "catalog",
			MetadataStatus:  "accepted",
		},
	}
	if err := store.Store(otherService); err != nil {
		t.Fatalf("Store other service decision: %v", err)
	}

	view, err := store.ProjectBankView(ProjectBankViewDecisions, ProjectBankOptions{
		Filters: Filters{Context: "payments"},
		Service: "payments-db",
		Status:  "canonical",
		Owner:   "platform",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView: %v", err)
	}

	if len(view.Sections) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(view.Sections))
	}
	if len(view.Sections[0].Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(view.Sections[0].Items))
	}
	item := view.Sections[0].Items[0]
	if item.Title != canonicalDecision.Title {
		t.Fatalf("title = %q, want %q", item.Title, canonicalDecision.Title)
	}
	if item.Owner != "platform" {
		t.Fatalf("owner = %q, want platform", item.Owner)
	}
	if item.Lifecycle != LifecycleCanonical {
		t.Fatalf("lifecycle = %q, want %q", item.Lifecycle, LifecycleCanonical)
	}
}

func TestProjectBankViewReviewQueueShowsOnlyPendingReviewItems(t *testing.T) {
	store := newTestStore(t)

	reviewItem := &Memory{
		Title:      "Review queue / Replace rollback runbook / api",
		Content:    "Action: merge\nHandling: hard_review\nWhy: replacement is ambiguous",
		Type:       TypeWorking,
		Context:    "payments",
		Importance: 0.45,
		Tags:       []string{"review-queue", "service:api", "runbook"},
		Metadata: map[string]string{
			MetadataEntity:         string(EngineeringTypeRunbook),
			MetadataService:        "api",
			MetadataRecordKind:     RecordKindReviewQueueItem,
			MetadataReviewRequired: "true",
			MetadataStatus:         "review_required",
			MetadataSessionMode:    string(SessionModeIncident),
		},
	}
	if err := store.Store(reviewItem); err != nil {
		t.Fatalf("Store review queue item: %v", err)
	}

	checkpoint := &Memory{
		Title:      "Session close / payments / api",
		Content:    "Checkpoint summary",
		Type:       TypeEpisodic,
		Context:    "payments",
		Importance: 0.10,
		Tags:       []string{"session-checkpoint", "service:api"},
		Metadata: map[string]string{
			MetadataRecordKind: RecordKindSessionCheckpoint,
			MetadataService:    "api",
		},
	}
	if err := store.Store(checkpoint); err != nil {
		t.Fatalf("Store checkpoint: %v", err)
	}

	view, err := store.ProjectBankView(ProjectBankViewReviewQueue, ProjectBankOptions{
		Filters: Filters{Context: "payments"},
		Service: "api",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView(review_queue): %v", err)
	}
	if len(view.Sections) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(view.Sections))
	}
	if len(view.Sections[0].Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(view.Sections[0].Items))
	}
	if view.Sections[0].Items[0].Title != reviewItem.Title {
		t.Fatalf("title = %q, want %q", view.Sections[0].Items[0].Title, reviewItem.Title)
	}
}
