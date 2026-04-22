package memory

import (
	"testing"
	"time"
)

func TestNormalizeSedimentLayer(t *testing.T) {
	cases := []struct {
		in   string
		want SedimentLayer
	}{
		{"", ""},
		{" ", ""},
		{"surface", LayerSurface},
		{"SURFACE", LayerSurface},
		{" Surface ", LayerSurface},
		{"episodic", LayerEpisodic},
		{"semantic", LayerSemantic},
		{"character", LayerCharacter},
		{"unknown", ""},
		{"canonical", ""}, // canonical is a lifecycle status, not a sediment layer
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := NormalizeSedimentLayer(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeSedimentLayer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsValidSedimentLayer(t *testing.T) {
	valid := []string{"surface", "episodic", "semantic", "character", "SURFACE", " surface "}
	for _, s := range valid {
		if !IsValidSedimentLayer(s) {
			t.Errorf("IsValidSedimentLayer(%q) = false, want true", s)
		}
	}
	invalid := []string{"", " ", "canonical", "raw", "foo"}
	for _, s := range invalid {
		if IsValidSedimentLayer(s) {
			t.Errorf("IsValidSedimentLayer(%q) = true, want false", s)
		}
	}
}

// fixedNow returns a policy whose Now always returns the given time.
func fixedPolicy(now time.Time) SedimentPolicy {
	return SedimentPolicy{Now: func() time.Time { return now }}
}

func TestDecide_SurfaceToEpisodic_ByAge(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:             "m1",
		Type:           TypeWorking,
		SedimentLayer:  string(LayerSurface),
		CreatedAt:      now.Add(-8 * 24 * time.Hour), // 8 days old
		AccessCount:    1,
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected transition, got nil")
	}
	if tr.From != LayerSurface || tr.To != LayerEpisodic {
		t.Errorf("got %s→%s, want surface→episodic", tr.From, tr.To)
	}
	if !tr.Auto {
		t.Errorf("surface→episodic should be auto")
	}
	if tr.Reason != "aged-surface" {
		t.Errorf("reason=%q, want aged-surface", tr.Reason)
	}
}

func TestDecide_SurfaceToEpisodic_NoAccess(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeWorking,
		SedimentLayer: string(LayerSurface),
		CreatedAt:     now.Add(-8 * 24 * time.Hour),
		AccessCount:   0,
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil (access_count=0), got %+v", tr)
	}
}

func TestDecide_SurfaceToEpisodic_TooFresh(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeWorking,
		SedimentLayer: string(LayerSurface),
		CreatedAt:     now.Add(-3 * 24 * time.Hour), // 3 days old, threshold 7
		AccessCount:   10,
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil (too fresh), got %+v", tr)
	}
}

func TestDecide_EpisodicToSemantic_ByAge(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeEpisodic,
		SedimentLayer: string(LayerEpisodic),
		CreatedAt:     now.Add(-31 * 24 * time.Hour),
		AccessCount:   3,
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected transition, got nil")
	}
	if tr.From != LayerEpisodic || tr.To != LayerSemantic {
		t.Errorf("got %s→%s, want episodic→semantic", tr.From, tr.To)
	}
	if tr.Auto {
		t.Errorf("episodic→semantic should NOT be auto")
	}
	if tr.Reason != "aged-episodic" {
		t.Errorf("reason=%q", tr.Reason)
	}
}

func TestDecide_EpisodicToSemantic_NotEnoughAccess(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeEpisodic,
		SedimentLayer: string(LayerEpisodic),
		CreatedAt:     now.Add(-31 * 24 * time.Hour),
		AccessCount:   2, // min is 3
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil, got %+v", tr)
	}
}

func TestDecide_SemanticToCharacter_ByRefs(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeSemantic,
		SedimentLayer: string(LayerSemantic),
		CreatedAt:     now.Add(-60 * 24 * time.Hour),
		Metadata: map[string]string{
			MetadataReferencedByCount: "25",
		},
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected transition, got nil")
	}
	if tr.From != LayerSemantic || tr.To != LayerCharacter {
		t.Errorf("got %s→%s, want semantic→character", tr.From, tr.To)
	}
	if tr.Auto {
		t.Errorf("semantic→character should NOT be auto")
	}
	if tr.Reason != "canonical-promotion" {
		t.Errorf("reason=%q", tr.Reason)
	}
}

func TestDecide_SemanticToCharacter_ByCanonical(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeSemantic,
		SedimentLayer: string(LayerSemantic),
		Metadata: map[string]string{
			MetadataKnowledgeLayer: "canonical",
		},
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected transition, got nil")
	}
	if tr.To != LayerCharacter {
		t.Errorf("got to=%s, want character", tr.To)
	}
}

func TestDecide_SemanticStays_WithoutSignal(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeSemantic,
		SedimentLayer: string(LayerSemantic),
		Metadata: map[string]string{
			MetadataReferencedByCount: "5",
		},
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil (refs below threshold, not canonical), got %+v", tr)
	}
}

func TestDecide_CharacterToSemantic_ByDecay(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeSemantic,
		SedimentLayer: string(LayerCharacter),
		AccessedAt:    now.Add(-100 * 24 * time.Hour),
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected demotion, got nil")
	}
	if tr.From != LayerCharacter || tr.To != LayerSemantic {
		t.Errorf("got %s→%s, want character→semantic", tr.From, tr.To)
	}
	if tr.Auto {
		t.Errorf("character→semantic demotion should NOT be auto")
	}
	if tr.Reason != "character-decay" {
		t.Errorf("reason=%q", tr.Reason)
	}
}

func TestDecide_CharacterStays_RecentAccess(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeSemantic,
		SedimentLayer: string(LayerCharacter),
		AccessedAt:    now.Add(-10 * 24 * time.Hour),
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil (recent access), got %+v", tr)
	}
}

func TestDecide_NoTransition_FreshSurface(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeWorking,
		SedimentLayer: string(LayerSurface),
		CreatedAt:     now.Add(-1 * time.Hour),
		AccessCount:   5,
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("expected nil, got %+v", tr)
	}
}

func TestDecide_SkipsReviewQueueItem(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeWorking,
		SedimentLayer: string(LayerSurface),
		CreatedAt:     now.Add(-30 * 24 * time.Hour),
		AccessCount:   10,
		Metadata: map[string]string{
			MetadataRecordKind: RecordKindReviewQueueItem,
		},
	}
	if tr := Decide(m, fixedPolicy(now)); tr != nil {
		t.Errorf("review queue items must not transition, got %+v", tr)
	}
}

func TestDecide_EmptyLayer_TreatedAsSurface(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	m := &Memory{
		ID:            "m1",
		Type:          TypeWorking,
		SedimentLayer: "", // pre-T48 row that missed backfill
		CreatedAt:     now.Add(-8 * 24 * time.Hour),
		AccessCount:   1,
	}
	tr := Decide(m, fixedPolicy(now))
	if tr == nil {
		t.Fatalf("expected transition, got nil")
	}
	if tr.From != LayerSurface {
		t.Errorf("from=%s, want surface", tr.From)
	}
}

func TestDemoteOneStep(t *testing.T) {
	cases := []struct {
		in   SedimentLayer
		want SedimentLayer
	}{
		{LayerCharacter, LayerSemantic},
		{LayerSemantic, LayerEpisodic},
		{LayerEpisodic, LayerSurface},
		{LayerSurface, ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := DemoteOneStep(tc.in)
		if got != tc.want {
			t.Errorf("DemoteOneStep(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBackfillSedimentLayer(t *testing.T) {
	cases := []struct {
		name     string
		memType  Type
		metadata map[string]string
		want     SedimentLayer
	}{
		{"working → surface", TypeWorking, nil, LayerSurface},
		{"episodic → episodic", TypeEpisodic, nil, LayerEpisodic},
		{"semantic → semantic", TypeSemantic, nil, LayerSemantic},
		{"procedural → semantic", TypeProcedural, nil, LayerSemantic},
		{"canonical via knowledge_layer → character", TypeSemantic, map[string]string{MetadataKnowledgeLayer: "canonical"}, LayerCharacter},
		{"canonical bool → character", TypeSemantic, map[string]string{"canonical": "true"}, LayerCharacter},
		{"canonical via lifecycle_status → character", TypeSemantic, map[string]string{MetadataLifecycleStatus: "canonical"}, LayerCharacter},
		{"working + canonical → character (canonical wins)", TypeWorking, map[string]string{MetadataKnowledgeLayer: "canonical"}, LayerCharacter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BackfillSedimentLayer(tc.memType, tc.metadata)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
