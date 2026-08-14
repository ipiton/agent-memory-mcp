package memory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Store saves a new memory, generating an ID and embedding if not provided.
func (ms *Store) Store(ctx context.Context, m *Memory) error {
	if err := m.Validate(); err != nil {
		return err
	}

	// T90 M1: the embedding is computed BEFORE writeMu is taken. It is a
	// network call to the embedding provider — hundreds of milliseconds, more
	// when the provider is remote or retrying — and holding the store's single
	// write lock across it made every other writer wait behind it, so write
	// throughput was capped by embedder latency rather than by the database.
	// The input is m.Content, which the caller already owns, so nothing about
	// the computation needs the lock.
	//
	// T84: review-queue items are service pointers excluded from semantic
	// recall, so embedding them is pure waste — a vector built from the
	// target's query text that only ever added kNN noise. Skip it; the
	// ProjectBank review view is List-based and needs no vector.
	if ms.embedder != nil && len(m.Embedding) == 0 && !IsReviewQueueMemory(m) {
		result, truncated, err := ms.embedForWrite(ctx, m.ID, m.Content)
		if err == nil {
			m.Embedding = result.Embedding
			m.EmbeddingModel = result.ModelID
			if truncated {
				markEmbeddingTruncated(m)
			}
		}
	}

	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	if m.ID == "" {
		m.ID = uuid.New().String()
	}

	now := ms.now()
	m.CreatedAt = now
	m.UpdatedAt = now
	m.AccessedAt = now

	if err := insertMemoryRow(ms.db, m); err != nil {
		return err
	}

	// T75: read-after-write veto — confirm the row is durably queryable in the
	// store before reporting success (and before populating the cache), so a
	// silent write loss surfaces as an error instead of a false success. The
	// cache is only trusted once the durable write is verified.
	if err := verifyMemoryPersisted(ms.db, m.ID); err != nil {
		return err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(m))
	ms.mu.Unlock()

	ms.logger.Info("Memory stored",
		zap.String("id", m.ID),
		zap.String("type", string(m.Type)),
		zap.String("title", m.Title))

	// Async fan-out for T50 graph layer. No-op when no extractor is wired.
	ms.fanoutTripleExtraction(m)

	return nil
}

// Update modifies an existing memory identified by id with the provided field updates.
func (ms *Store) Update(ctx context.Context, id string, updates Update) error {
	// T90 M1: re-embed before taking the write lock — see Store. The new
	// content comes from the caller, so the provider call needs no lock, and
	// holding one across it stalled every other writer.
	var embedded *embeddedContent
	if content := strings.TrimSpace(updates.Content); content != "" && ms.embedder != nil {
		result, truncated, err := ms.embedForWrite(ctx, id, content)
		if err != nil {
			embedded = &embeddedContent{content: content}
		} else {
			embedded = &embeddedContent{content: content, embedding: result.Embedding, model: result.ModelID, truncated: truncated}
		}
	}

	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()
	return ms.updateLocked(ctx, id, updates, embedded)
}

// embeddedContent carries an embedding computed outside the write lock. A nil
// embedding with a non-empty content means the provider failed and the update
// proceeds without a vector, matching the previous fail-open behaviour.
type embeddedContent struct {
	content   string
	embedding []float32
	model     string
	// truncated marks a vector built from the opening of content only, because
	// the encoder refused the whole body (T120).
	truncated bool
}

// updateLocked performs the update assuming the caller already holds writeMu.
// Compound operations (MarkOutdated, PromoteToCanonical) hold the lock across
// their whole read-modify-write so a concurrent writer cannot slip in between
// their Get and this call — T89 H2.
func (ms *Store) updateLocked(ctx context.Context, id string, updates Update, embedded *embeddedContent) error {
	current, err := ms.Get(id)
	if err != nil {
		return err
	}

	m := copyMemory(current)

	if updates.Content != "" {
		m.Content = strings.TrimSpace(updates.Content)
		m.Embedding = nil
		m.EmbeddingModel = ""
		if embedded != nil && embedded.content == m.Content {
			m.Embedding = embedded.embedding
			m.EmbeddingModel = embedded.model
			if embedded.truncated {
				markEmbeddingTruncated(m)
			} else {
				delete(m.Metadata, MetadataEmbeddingTruncated)
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

	m.UpdatedAt = ms.now()
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

	// Re-extract triples whenever the content changed: stale triples
	// from the previous content would mislead future graph walks. The
	// fanout is a no-op when no extractor is wired.
	if updates.Content != "" {
		ms.fanoutTripleExtraction(m)
	}

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
// Knowledge-graph triples whose provenance is this memory cascade away in the
// same operation: the FK declaration alone is unreliable across SQLite
// drivers / pragma settings, so we issue an explicit DELETE for portability.
func (ms *Store) Delete(ctx context.Context, id string) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	if _, err := ms.db.ExecContext(ctx, "DELETE FROM memory_triples WHERE memory_id = ?", id); err != nil {
		return fmt.Errorf("failed to delete triples for memory: %w", err)
	}
	if _, err := ms.db.ExecContext(ctx, "DELETE FROM memories WHERE id = ?", id); err != nil {
		return fmt.Errorf("failed to delete memory: %w", err)
	}

	ms.mu.Lock()
	ms.cacheDeleteLocked(id)
	ms.mu.Unlock()

	ms.logger.Info("Memory deleted", zap.String("id", id))
	return nil
}

// MarkOutdated archives a memory from normal operational use while keeping it queryable.
//
// T89 H2. This used to run as four independent writes — Update, then
// SetTemporalFields on the retired entry, then SetTemporalFields and
// IncrementReferencedByCount on the successor — each taking writeMu on its own,
// with the initial Get taken outside any lock. Two consequences, both observed
// as possible by construction:
//
//   - Lost update: a concurrent writer could commit between the Get and the
//     Update and have its change silently overwritten.
//   - Split-brain supersession: a failure after the second write left an entry
//     marked superseded while the successor knew nothing about it, or the
//     reverse. Nothing retried and nothing rolled back.
//
// Now the whole operation holds writeMu, re-reads under it, and commits both
// rows in one transaction. A dangling superseded_by (successor not in the
// store) is still tolerated — the pointer is recorded on the retired entry and
// the successor half is skipped, as before. What is no longer tolerated is a
// half-written pair.
func (ms *Store) MarkOutdated(ctx context.Context, id string, reason string, supersededBy string) (*MarkOutdatedResult, error) {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	mem, err := ms.Get(id)
	if err != nil {
		return nil, err
	}

	now := ms.now()
	nowUTC := now.UTC()
	supersededBy = strings.TrimSpace(supersededBy)

	metadata := copyMetadata(mem.Metadata)
	status := "outdated"
	if supersededBy != "" {
		status = "superseded"
		metadata["superseded_by"] = supersededBy
	}
	if strings.TrimSpace(reason) != "" {
		metadata["outdated_reason"] = strings.TrimSpace(reason)
	}
	metadata["status"] = status
	metadata["archived"] = "true"
	metadata["last_verified_at"] = nowUTC.Format(time.RFC3339)

	importance := mem.Importance
	if importance > 0.25 {
		importance = 0.25
	}

	retired := copyMemory(mem)
	retired.Importance = importance
	retired.Metadata = NormalizeMetadata(metadata)
	retired.ValidUntil = &nowUTC
	if supersededBy != "" {
		retired.SupersededBy = supersededBy
	}
	retired.UpdatedAt = now
	if err := retired.Validate(); err != nil {
		return nil, err
	}

	// Link the successor back and bump its referenced_by_count so the T48
	// semantic→character "by refs" rule eventually fires. A successor that
	// cannot be read is not an error: the supersession still stands on the
	// retired entry.
	var successor *Memory
	if supersededBy != "" {
		s, sErr := ms.Get(supersededBy)
		if sErr != nil {
			ms.logger.Warn("Superseding entry not found; recording supersession without back-link",
				zap.String("id", supersededBy), zap.Error(sErr))
		} else {
			successor = copyMemory(s)
			successor.ValidFrom = &nowUTC
			successor.Replaces = id
			successorMeta := copyMetadata(successor.Metadata)
			if successorMeta == nil {
				successorMeta = make(map[string]string)
			}
			successorMeta[MetadataReferencedByCount] = strconv.Itoa(referencedByCountFromMetadata(successorMeta) + 1)
			successor.Metadata = NormalizeMetadata(successorMeta)
			successor.UpdatedAt = now
			if err := successor.Validate(); err != nil {
				return nil, err
			}
		}
	}

	tx, err := ms.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("mark outdated: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := updateMemoryRow(tx, retired); err != nil {
		return nil, err
	}
	if successor != nil {
		if err := updateMemoryRow(tx, successor); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mark outdated: commit: %w", err)
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(retired))
	if successor != nil {
		ms.cacheSetLocked(toCachedMemory(successor))
	}
	ms.mu.Unlock()

	ms.logger.Info("Memory marked outdated",
		zap.String("id", id),
		zap.String("status", status),
		zap.String("superseded_by", supersededBy),
	)

	return &MarkOutdatedResult{
		ID:           id,
		Status:       status,
		SupersededBy: supersededBy,
		Importance:   importance,
	}, nil
}

// ErrPromotionRequiresVerification is returned when an automated pipeline tries
// to promote a conversational-origin memory to canonical without a human/verify
// gate (T77 memory-poisoning defense). Canonical records are fully trusted, so
// an attacker who plants ordinary-looking conversational records must not be
// able to have them auto-promoted — promotion of untrusted-provenance records
// requires an explicit verified=true (human review) decision.
var ErrPromotionRequiresVerification = errors.New("promotion to canonical requires verification: conversational-origin memory cannot be auto-promoted")

// PromotionAllowed reports whether PromoteToCanonical would accept this
// promotion, without performing it. verified=true is the human decision and
// always clears; an automated caller only clears for already-trusted
// provenance.
//
// T92: the gate needs a name because callers legitimately have to know the
// answer before the write. The archive sweep's dry run used to open-code its
// own shorter version of this chain, so the preview counted as "promoted"
// records that the real run routed to review — a 15× divergence on the
// 2026-07-16 measurement. Both the preview and the write now read this one
// definition, which is what keeps them from drifting the next time the gate
// gains a condition.
func PromotionAllowed(m *Memory, verified bool) bool {
	return verified || ProvenanceIsTrusted(ProvenanceOf(m))
}

// PromoteToCanonical marks a memory as the current canonical entry.
//
// Threat model (T77): canonical is the highest-trust layer. Auto-promotion
// pipelines (steward auto-run, archive-sweep) are an attack vector — a planted
// conversational record could be silently canonicalized. So promotion is gated
// on provenance: when verified is false (automated caller), a memory whose
// provenance is not already trusted (verified/external) is refused with
// ErrPromotionRequiresVerification. When verified is true (a human review via
// the MCP tool or steward inbox), promotion proceeds and stamps
// provenance=verified.
// T89 H2/M3: holds writeMu across the whole read-modify-write (the Get used to
// sit outside any lock, so a concurrent writer's change could be overwritten),
// and lifts the sediment layer along with the canonical flag — the two axes
// used to drift apart, leaving entries that were canonical on one axis and
// surface-level on the other.
func (ms *Store) PromoteToCanonical(ctx context.Context, id string, owner string, verified bool) (*PromoteToCanonicalResult, error) {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	mem, err := ms.Get(id)
	if err != nil {
		return nil, err
	}

	if !PromotionAllowed(mem, verified) {
		return nil, ErrPromotionRequiresVerification
	}

	metadata := copyMetadata(mem.Metadata)
	if verified {
		metadata[MetadataProvenance] = ProvenanceVerified
	}
	if strings.TrimSpace(owner) != "" {
		metadata["owner"] = strings.TrimSpace(owner)
	}
	if s := normalizeStatus(metadata["status"]); s == "" || s == "draft" {
		metadata["status"] = "confirmed"
	}
	metadata["knowledge_layer"] = "canonical"
	metadata["canonical"] = "true"
	canonicalNow := ms.now().UTC().Format(time.RFC3339)
	metadata["canonical_promoted_at"] = canonicalNow
	// T111: only a promotion that actually went through verification may claim
	// one. The stamp used to be unconditional, so the archive sweep's automatic
	// promotions (verified=false) wrote "last verified: now" for records nobody
	// had looked at — 12 such canonical entries on the 2026-08-12 measurement,
	// none of which had ever been through verify_entry. Freshness scoring is
	// unaffected either way: deriveTrust falls back to UpdatedAt, which the
	// promotion sets regardless. What changes is that the field stops asserting
	// something that did not happen.
	if verified {
		metadata["last_verified_at"] = canonicalNow
	}
	delete(metadata, "archived")

	importance := mem.Importance
	if importance < 0.95 {
		importance = 0.95
	}

	promoted := copyMemory(mem)
	promoted.Importance = importance
	promoted.Metadata = NormalizeMetadata(metadata)
	// M3: canonical knowledge is load-bearing by definition, so it belongs in
	// the character layer. The sediment cycle proposes this transition only from
	// `semantic` and only as a non-auto suggestion, so an entry promoted from
	// surface or episodic stayed there indefinitely — canonical on one axis,
	// evictable on the other.
	promoted.SedimentLayer = string(LayerCharacter)
	promoted.UpdatedAt = ms.now()
	if err := promoted.Validate(); err != nil {
		return nil, err
	}

	if err := updateMemoryRow(ms.db, promoted); err != nil {
		return nil, err
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(promoted))
	ms.mu.Unlock()

	ms.logger.Info("Memory promoted to canonical", zap.String("id", id), zap.Bool("verified", verified))

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
	skippedDuplicateIDs := make([]string, 0)
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
			// T81: a missing/already-archived duplicate id must not fail the
			// whole batch — skip it (reported in the result) so bulk merges are
			// idempotent and a single stale id doesn't abort the rest.
			skippedDuplicateIDs = append(skippedDuplicateIDs, duplicateID)
			continue
		}
		duplicates = append(duplicates, duplicate)
		normalizedDuplicateIDs = append(normalizedDuplicateIDs, duplicateID)
	}
	if len(duplicates) == 0 {
		if len(skippedDuplicateIDs) > 0 {
			// Every requested duplicate was already gone — a no-op re-run, not an
			// error. Return success with the skipped ids so callers can see it.
			return &MergeDuplicatesResult{
				PrimaryID:           primaryID,
				SkippedDuplicateIDs: skippedDuplicateIDs,
			}, nil
		}
		return nil, &ErrValidation{Message: "at least one duplicate memory id is required"}
	}

	now := ms.now()
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
		SkippedDuplicateIDs:  skippedDuplicateIDs,
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
	now := ms.now()
	if _, err := ms.db.Exec(
		`UPDATE memories SET sediment_layer = ?, updated_at = ? WHERE id = ?`,
		string(target), now, id,
	); err != nil {
		return nil, fmt.Errorf("promote_sediment: update failed: %w", err)
	}

	ms.updateCachedField(id, func(cm *cachedMemory) {
		cm.SedimentLayer = target
		cm.UpdatedAt = now
	})

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

	now := ms.now()
	if _, err := ms.db.Exec(
		`UPDATE memories SET sediment_layer = ?, updated_at = ? WHERE id = ?`,
		string(to), now, id,
	); err != nil {
		return nil, fmt.Errorf("demote_sediment: update failed: %w", err)
	}

	ms.updateCachedField(id, func(cm *cachedMemory) {
		cm.SedimentLayer = to
		cm.UpdatedAt = now
	})

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

// BackdateForTest rewrites CreatedAt and both access counters for the given
// memory directly in SQLite, then reloads the in-memory cache. accessCount
// lands in targeted_access_count too: the parameter answers "how used is this
// entry", and after T113 that is the counter the sediment gate reads.
// Exists solely to
// support tests that need to simulate aged memories without waiting real
// time — callers outside tests should never invoke it (it bypasses the
// write path entirely). Mirrors the pattern used by the sediment
// integration tests that reach into store.db directly.
func (ms *Store) BackdateForTest(id string, createdAt time.Time, accessCount int) error {
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()
	if _, err := ms.db.Exec(
		`UPDATE memories SET created_at = ?, access_count = ?, targeted_access_count = ? WHERE id = ?`,
		createdAt, accessCount, accessCount, id,
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
	updated.UpdatedAt = ms.now()
	if err := updateMemoryRow(ms.db, updated); err != nil {
		return fmt.Errorf("failed to update embedding: %w", err)
	}

	ms.mu.Lock()
	ms.cacheSetLocked(toCachedMemory(updated))
	ms.mu.Unlock()
	return nil
}
