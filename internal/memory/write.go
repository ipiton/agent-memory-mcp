package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Store saves a new memory, generating an ID and embedding if not provided.
func (ms *Store) Store(ctx context.Context, m *Memory) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	if err := m.Validate(); err != nil {
		return err
	}

	if m.ID == "" {
		m.ID = uuid.New().String()
	}

	now := time.Now()
	m.CreatedAt = now
	m.UpdatedAt = now
	m.AccessedAt = now

	if ms.embedder != nil && len(m.Embedding) == 0 {
		result, err := ms.embedder.EmbedDetailed(ctx, m.Content)
		if err != nil {
			ms.logger.Warn("Failed to generate embedding for memory", zap.String("id", m.ID), zap.Error(err))
		} else {
			m.Embedding = result.Embedding
			m.EmbeddingModel = result.ModelID
		}
	}

	if err := insertMemoryRow(ms.db, m); err != nil {
		return err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(m))
	ms.mu.Unlock()

	ms.logger.Info("Memory stored",
		zap.String("id", m.ID),
		zap.String("type", string(m.Type)),
		zap.String("title", m.Title))

	return nil
}

// Update modifies an existing memory identified by id with the provided field updates.
func (ms *Store) Update(ctx context.Context, id string, updates Update) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	current, err := ms.Get(id)
	if err != nil {
		return err
	}

	m := copyMemory(current)

	if updates.Content != "" {
		m.Content = strings.TrimSpace(updates.Content)
		m.Embedding = nil
		m.EmbeddingModel = ""
		if ms.embedder != nil {
			result, err := ms.embedder.EmbedDetailed(ctx, m.Content)
			if err == nil {
				m.Embedding = result.Embedding
				m.EmbeddingModel = result.ModelID
			} else {
				ms.logger.Warn("Failed to re-generate embedding for updated memory", zap.String("id", id), zap.Error(err))
			}
		}
	}
	if updates.Title != "" {
		m.Title = strings.TrimSpace(updates.Title)
	}
	if len(updates.Tags) > 0 {
		m.Tags = NormalizeTags(updates.Tags)
	}
	if updates.Context != "" {
		m.Context = strings.TrimSpace(updates.Context)
	}
	if updates.Importance != nil {
		m.Importance = *updates.Importance
	}
	if len(updates.Metadata) > 0 {
		mergedMetadata := copyMetadata(m.Metadata)
		if mergedMetadata == nil {
			mergedMetadata = make(map[string]string)
		}
		for k, v := range updates.Metadata {
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k == "" {
				continue
			}
			if v == "" {
				delete(mergedMetadata, k)
				continue
			}
			mergedMetadata[k] = v
		}
		m.Metadata = NormalizeMetadata(mergedMetadata)
	}

	m.UpdatedAt = time.Now()
	if err := m.Validate(); err != nil {
		return err
	}

	if err := updateMemoryRow(ms.db, m); err != nil {
		return err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(m))
	ms.mu.Unlock()

	ms.logger.Info("Memory updated", zap.String("id", id))
	return nil
}

// Update contains optional fields for modifying an existing memory.
type Update struct {
	Content    string            `json:"content,omitempty"`
	Title      string            `json:"title,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Context    string            `json:"context,omitempty"`
	Importance *float64          `json:"importance,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Delete removes a memory by ID from both the database and cache.
func (ms *Store) Delete(ctx context.Context, id string) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	if _, err := ms.db.Exec("DELETE FROM memories WHERE id = ?", id); err != nil {
		return fmt.Errorf("failed to delete memory: %w", err)
	}

	ms.mu.Lock()
	ms.cacheDeleteLocked(id)
	ms.mu.Unlock()

	ms.logger.Info("Memory deleted", zap.String("id", id))
	return nil
}

// MarkOutdated archives a memory from normal operational use while keeping it queryable.
func (ms *Store) MarkOutdated(ctx context.Context, id string, reason string, supersededBy string) (*MarkOutdatedResult, error) {
	mem, err := ms.Get(id)
	if err != nil {
		return nil, err
	}

	metadata := copyMetadata(mem.Metadata)
	status := "outdated"
	supersededBy = strings.TrimSpace(supersededBy)
	if supersededBy != "" {
		status = "superseded"
		metadata["superseded_by"] = supersededBy
	}
	if strings.TrimSpace(reason) != "" {
		metadata["outdated_reason"] = strings.TrimSpace(reason)
	}
	metadata["status"] = status
	metadata["archived"] = "true"
	metadata["last_verified_at"] = time.Now().UTC().Format(time.RFC3339)

	importance := mem.Importance
	if importance > 0.25 {
		importance = 0.25
	}

	if err := ms.Update(ctx, id, Update{
		Importance: &importance,
		Metadata:   metadata,
	}); err != nil {
		return nil, err
	}

	// Build supersession chain: set temporal fields on old entry.
	now := time.Now().UTC()
	if err := ms.SetTemporalFields(ctx, id, nil, &now, supersededBy, ""); err != nil {
		// Non-fatal: temporal fields are supplementary.
		ms.logger.Warn("Failed to set temporal fields on outdated entry", zap.String("id", id), zap.Error(err))
	}

	// If superseding entry exists, link it back.
	if supersededBy != "" {
		if err := ms.SetTemporalFields(ctx, supersededBy, &now, nil, "", id); err != nil {
			ms.logger.Warn("Failed to set temporal fields on superseding entry", zap.String("id", supersededBy), zap.Error(err))
		}
	}

	return &MarkOutdatedResult{
		ID:           id,
		Status:       status,
		SupersededBy: supersededBy,
		Importance:   importance,
	}, nil
}

// PromoteToCanonical marks a memory as the current canonical entry.
func (ms *Store) PromoteToCanonical(ctx context.Context, id string, owner string) (*PromoteToCanonicalResult, error) {
	mem, err := ms.Get(id)
	if err != nil {
		return nil, err
	}

	metadata := copyMetadata(mem.Metadata)
	if strings.TrimSpace(owner) != "" {
		metadata["owner"] = strings.TrimSpace(owner)
	}
	if s := normalizeStatus(metadata["status"]); s == "" || s == "draft" {
		metadata["status"] = "confirmed"
	}
	metadata["knowledge_layer"] = "canonical"
	metadata["canonical"] = "true"
	metadata["canonical_promoted_at"] = time.Now().UTC().Format(time.RFC3339)
	metadata["last_verified_at"] = time.Now().UTC().Format(time.RFC3339)
	delete(metadata, "archived")

	importance := mem.Importance
	if importance < 0.95 {
		importance = 0.95
	}

	if err := ms.Update(ctx, id, Update{
		Importance: &importance,
		Metadata:   metadata,
	}); err != nil {
		return nil, err
	}

	resultOwner := strings.TrimSpace(owner)
	if resultOwner == "" {
		resultOwner = strings.TrimSpace(metadata["owner"])
		if resultOwner == "" {
			resultOwner = defaultOwnerForMemorySource(memoryEntity(mem))
		}
	}

	return &PromoteToCanonicalResult{
		ID:         id,
		Layer:      "canonical",
		Owner:      resultOwner,
		Status:     normalizeStatus(metadata["status"]),
		Importance: importance,
	}, nil
}

// MergeDuplicates consolidates duplicate memories into a primary entry and archives the rest.
func (ms *Store) MergeDuplicates(ctx context.Context, primaryID string, duplicateIDs []string) (*MergeDuplicatesResult, error) {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	primaryID = strings.TrimSpace(primaryID)
	if primaryID == "" {
		return nil, &ErrValidation{Message: "primary memory id is required"}
	}

	primary, err := ms.Get(primaryID)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{primaryID: {}}
	duplicates := make([]*Memory, 0, len(duplicateIDs))
	normalizedDuplicateIDs := make([]string, 0, len(duplicateIDs))
	for _, duplicateID := range duplicateIDs {
		duplicateID = strings.TrimSpace(duplicateID)
		if duplicateID == "" {
			continue
		}
		if _, ok := seen[duplicateID]; ok {
			continue
		}
		seen[duplicateID] = struct{}{}
		duplicate, err := ms.Get(duplicateID)
		if err != nil {
			return nil, err
		}
		duplicates = append(duplicates, duplicate)
		normalizedDuplicateIDs = append(normalizedDuplicateIDs, duplicateID)
	}
	if len(duplicates) == 0 {
		return nil, &ErrValidation{Message: "at least one duplicate memory id is required"}
	}

	now := time.Now()
	metadata := copyMetadata(primary.Metadata)
	if owner := strings.TrimSpace(metadata["owner"]); owner == "" {
		for _, duplicate := range duplicates {
			if duplicateOwner := strings.TrimSpace(duplicate.Metadata["owner"]); duplicateOwner != "" {
				metadata["owner"] = duplicateOwner
				break
			}
		}
	}
	metadata["merged_from"] = joinCSVUnique(splitCSV(metadata["merged_from"]), normalizedDuplicateIDs)
	metadata["last_verified_at"] = now.UTC().Format(time.RFC3339)

	tags := append([]string(nil), primary.Tags...)
	mergedContent := mergeContent(primary.Content, duplicates)
	for _, duplicate := range duplicates {
		tags = UnionStrings(tags, duplicate.Tags)
	}

	updatedPrimary := copyMemory(primary)
	updatedPrimary.Content = mergedContent
	updatedPrimary.Tags = tags
	updatedPrimary.Metadata = NormalizeMetadata(metadata)
	updatedPrimary.UpdatedAt = now
	if updatedPrimary.Content != primary.Content {
		updatedPrimary.Embedding = nil
		updatedPrimary.EmbeddingModel = ""
		if ms.embedder != nil {
			result, err := ms.embedder.EmbedDetailed(ctx, updatedPrimary.Content)
			if err != nil {
				ms.logger.Warn("Failed to re-generate embedding for merged memory", zap.String("id", primaryID), zap.Error(err))
			} else {
				updatedPrimary.Embedding = result.Embedding
				updatedPrimary.EmbeddingModel = result.ModelID
			}
		}
	}
	if err := updatedPrimary.Validate(); err != nil {
		return nil, err
	}

	updatedDuplicates := make([]*Memory, 0, len(duplicates))
	archivedDuplicateIDs := make([]string, 0, len(normalizedDuplicateIDs))
	for _, duplicate := range duplicates {
		updatedDuplicate := copyMemory(duplicate)
		duplicateMetadata := copyMetadata(updatedDuplicate.Metadata)
		if duplicateMetadata == nil {
			duplicateMetadata = make(map[string]string)
		}
		duplicateMetadata["superseded_by"] = primaryID
		duplicateMetadata["outdated_reason"] = "merged duplicate"
		duplicateMetadata["status"] = "merged"
		duplicateMetadata["merged_into"] = primaryID
		duplicateMetadata["archived"] = "true"
		duplicateMetadata["last_verified_at"] = now.UTC().Format(time.RFC3339)
		importance := updatedDuplicate.Importance
		if importance > 0.10 {
			importance = 0.10
		}
		updatedDuplicate.Importance = importance
		updatedDuplicate.Metadata = NormalizeMetadata(duplicateMetadata)
		updatedDuplicate.UpdatedAt = now
		if err := updatedDuplicate.Validate(); err != nil {
			return nil, err
		}
		updatedDuplicates = append(updatedDuplicates, updatedDuplicate)
		archivedDuplicateIDs = append(archivedDuplicateIDs, updatedDuplicate.ID)
	}

	tx, err := ms.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin merge transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := updateMemoryRow(tx, updatedPrimary); err != nil {
		return nil, err
	}
	for _, duplicate := range updatedDuplicates {
		if err := updateMemoryRow(tx, duplicate); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit merge transaction: %w", err)
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(updatedPrimary))
	for _, duplicate := range updatedDuplicates {
		ms.cacheSetLocked(toCachedMemory(duplicate))
	}
	ms.mu.Unlock()

	return &MergeDuplicatesResult{
		PrimaryID:            primaryID,
		DuplicateIDs:         normalizedDuplicateIDs,
		ArchivedDuplicateIDs: archivedDuplicateIDs,
		MergedFromCount:      len(normalizedDuplicateIDs),
	}, nil
}

// PromoteSedimentResult reports the outcome of a PromoteSediment call.
type PromoteSedimentResult struct {
	ID       string        `json:"id"`
	From     SedimentLayer `json:"from"`
	To       SedimentLayer `json:"to"`
	Layer    SedimentLayer `json:"layer"` // alias for To for symmetry with PromoteToCanonicalResult
	Reason   string        `json:"reason,omitempty"`
	Affected bool          `json:"affected"` // false when From == To (no-op)
}

// DemoteSedimentResult reports the outcome of a DemoteSediment call.
type DemoteSedimentResult struct {
	ID       string        `json:"id"`
	From     SedimentLayer `json:"from"`
	To       SedimentLayer `json:"to"`
	Layer    SedimentLayer `json:"layer"`
	Reason   string        `json:"reason,omitempty"`
	Affected bool          `json:"affected"`
}

// PromoteSediment updates the memory's sediment_layer to target and returns
// the before/after state. Lock order: writeMu → mu (via Update path we'd
// otherwise incur double-embed). We write the row directly and refresh the
// cache under mu to avoid re-embedding.
//
// target must be a valid SedimentLayer; callers should validate with
// IsValidSedimentLayer before invoking.
func (ms *Store) PromoteSediment(ctx context.Context, id string, target SedimentLayer) (*PromoteSedimentResult, error) {
	target = NormalizeSedimentLayer(string(target))
	if target == "" {
		return nil, &ErrValidation{Message: "invalid target sediment layer"}
	}
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	current, err := ms.Get(id)
	if err != nil {
		return nil, err
	}
	from := NormalizeSedimentLayer(current.SedimentLayer)
	if from == "" {
		from = DefaultSedimentLayer
	}
	if from == target {
		return &PromoteSedimentResult{
			ID: id, From: from, To: target, Layer: target, Affected: false,
		}, nil
	}

	// Direct column update — no Validate() round-trip, no embedding churn.
	if _, err := ms.db.Exec(
		`UPDATE memories SET sediment_layer = ?, updated_at = ? WHERE id = ?`,
		string(target), time.Now(), id,
	); err != nil {
		return nil, fmt.Errorf("promote_sediment: update failed: %w", err)
	}

	ms.mu.Lock()
	if cm, ok := ms.memories[id]; ok {
		cm.SedimentLayer = target
		cm.UpdatedAt = time.Now()
	}
	ms.mu.Unlock()

	ms.logger.Info("Sediment layer promoted",
		zap.String("id", id),
		zap.String("from", string(from)),
		zap.String("to", string(target)),
	)

	return &PromoteSedimentResult{
		ID: id, From: from, To: target, Layer: target, Affected: true,
	}, nil
}

// DemoteSediment moves the memory one layer closer to surface. No-op when
// already at surface (returns Affected=false).
func (ms *Store) DemoteSediment(ctx context.Context, id string) (*DemoteSedimentResult, error) {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	current, err := ms.Get(id)
	if err != nil {
		return nil, err
	}
	from := NormalizeSedimentLayer(current.SedimentLayer)
	if from == "" {
		from = DefaultSedimentLayer
	}
	to := DemoteOneStep(from)
	if to == "" {
		// Already at surface — no further demotion.
		return &DemoteSedimentResult{
			ID: id, From: from, To: from, Layer: from, Reason: "already-at-surface", Affected: false,
		}, nil
	}

	if _, err := ms.db.Exec(
		`UPDATE memories SET sediment_layer = ?, updated_at = ? WHERE id = ?`,
		string(to), time.Now(), id,
	); err != nil {
		return nil, fmt.Errorf("demote_sediment: update failed: %w", err)
	}

	ms.mu.Lock()
	if cm, ok := ms.memories[id]; ok {
		cm.SedimentLayer = to
		cm.UpdatedAt = time.Now()
	}
	ms.mu.Unlock()

	ms.logger.Info("Sediment layer demoted",
		zap.String("id", id),
		zap.String("from", string(from)),
		zap.String("to", string(to)),
	)

	return &DemoteSedimentResult{
		ID: id, From: from, To: to, Layer: to, Affected: true,
	}, nil
}

type ReembedResult struct {
	Total              int               `json:"total"`
	Reembedded         int               `json:"reembedded"`
	AlreadyCurrent     int               `json:"already_current"`
	Failed             int               `json:"failed"`
	CurrentModel       string            `json:"current_model"`
	ChangedFromByModel map[string]int    `json:"changed_from_by_model,omitempty"`
	FailedByID         map[string]string `json:"failed_by_id,omitempty"`
}

// ReembedAll regenerates embeddings with the currently available embedding model.
func (ms *Store) ReembedAll(ctx context.Context) (*ReembedResult, error) {
	if ms.embedder == nil {
		return nil, fmt.Errorf("embedder not available")
	}

	snapshot := ms.snapshotReadonlyMemories()

	result := &ReembedResult{
		Total:              len(snapshot),
		ChangedFromByModel: make(map[string]int),
		FailedByID:         make(map[string]string),
	}

	for _, m := range snapshot {
		// We need content for re-embedding
		full, err := ms.Get(m.ID)
		if err != nil {
			result.Failed++
			result.FailedByID[m.ID] = err.Error()
			continue
		}

		embedResult, err := ms.embedder.EmbedDetailed(ctx, full.Content)
		if err != nil {
			result.Failed++
			result.FailedByID[m.ID] = err.Error()
			continue
		}

		if result.CurrentModel == "" {
			result.CurrentModel = embedResult.ModelID
		} else if embedResult.ModelID != result.CurrentModel {
			return nil, fmt.Errorf("embedding model changed during re-embed: started with %s, then got %s", result.CurrentModel, embedResult.ModelID)
		}

		if m.EmbeddingModel == embedResult.ModelID && len(m.Embedding) > 0 {
			result.AlreadyCurrent++
			continue
		}

		if err := ms.updateStoredEmbedding(m.ID, embedResult.Embedding, embedResult.ModelID); err != nil {
			result.Failed++
			result.FailedByID[m.ID] = err.Error()
			continue
		}

		previousModel := m.EmbeddingModel
		if previousModel == "" {
			previousModel = "(none)"
		}
		result.ChangedFromByModel[previousModel]++
		result.Reembedded++
	}

	if len(result.FailedByID) == 0 {
		result.FailedByID = nil
	}

	return result, nil
}

// BackdateForTest rewrites CreatedAt and AccessCount for the given memory
// directly in SQLite, then reloads the in-memory cache. Exists solely to
// support tests that need to simulate aged memories without waiting real
// time — callers outside tests should never invoke it (it bypasses the
// write path entirely). Mirrors the pattern used by the sediment
// integration tests that reach into store.db directly.
func (ms *Store) BackdateForTest(id string, createdAt time.Time, accessCount int) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()
	if _, err := ms.db.Exec(
		`UPDATE memories SET created_at = ?, access_count = ? WHERE id = ?`,
		createdAt, accessCount, id,
	); err != nil {
		return fmt.Errorf("backdate: %w", err)
	}
	return ms.loadMemoriesToCache()
}

func (ms *Store) updateStoredEmbedding(id string, embedding []float32, embeddingModel string) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	current, err := ms.Get(id)
	if err != nil {
		return err
	}
	updated := copyMemory(current)
	updated.EmbeddingModel = embeddingModel
	updated.Embedding = make([]float32, len(embedding))
	copy(updated.Embedding, embedding)
	updated.UpdatedAt = time.Now()
	if err := updateMemoryRow(ms.db, updated); err != nil {
		return fmt.Errorf("failed to update embedding: %w", err)
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(updated))
	ms.mu.Unlock()
	return nil
}
