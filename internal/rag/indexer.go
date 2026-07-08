package rag

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

func (re *Engine) IndexDocuments(ctx context.Context) error {
	if re == nil || re.docService == nil || re.vecService == nil {
		return fmt.Errorf("RAG engine not available")
	}

	// Serialise all indexing runs (foreground tool + background watcher) so
	// they never write to vectors.db concurrently. See Engine.indexMu.
	re.indexMu.Lock()
	defer re.indexMu.Unlock()

	startTime := time.Now().UTC()
	// chunkerVersion bumps trigger a full rebuild on next IndexDocuments.
	//   char-v1     — naive char-budget splitter (initial)
	//   skeleton-v1 — T49: Markdown skeleton tree + breadcrumb prefix +
	//                 section-aware boundaries + heuristic noise filter.
	// Pre-T49 indices upgrade automatically on the next index call.
	const chunkerVersion = "skeleton-v1"

	allDocs, err := re.docService.collectDocuments()
	if err != nil {
		return fmt.Errorf("failed to collect documents: %w", err)
	}

	store := re.vecService.store
	oldModel, _ := store.GetMetadata("embedding_model")
	oldChunker, _ := store.GetMetadata("chunker_version")
	indexState, _ := store.GetMetadata(indexStateMetadataKey)

	needsRebuild := false
	if indexState == indexStateDirty {
		re.logger.Warn("Index state marked dirty - forcing rebuild to recover tracking consistency")
		needsRebuild = true
	}
	if oldModel != "" && len(allDocs) > 0 {
		currentModel, err := re.vecService.detectModelID(ctx, allDocs[0].Content)
		if err != nil {
			return fmt.Errorf("failed to detect current embedding model: %w", err)
		}
		if oldModel != currentModel {
			re.logger.Warn("Embedding model changed - full rebuild required",
				zap.String("old_model", oldModel),
				zap.String("current_model", currentModel),
			)
			needsRebuild = true
		}
	}
	if oldChunker != "" && oldChunker != chunkerVersion {
		re.logger.Warn("Chunker version changed - full rebuild required")
		needsRebuild = true
	}

	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		indexedFiles = make(map[string]*vectorstore.IndexedFileInfo)
	}

	if len(allDocs) == 0 && len(indexedFiles) == 0 && !needsRebuild {
		return fmt.Errorf("no documents found to index")
	}

	toAdd, toRemove := re.calculateIndexChanges(allDocs, indexedFiles, needsRebuild)

	re.logger.Info("Indexing analysis",
		zap.Int("total_documents", len(allDocs)),
		zap.Int("indexed_files", len(indexedFiles)),
		zap.Int("documents_to_add", len(toAdd)),
		zap.Int("files_to_remove", len(toRemove)),
		zap.Bool("force_rebuild", needsRebuild),
	)

	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata: map[string]string{
			indexStateMetadataKey: indexStateDirty,
			indexStartedAtKey:     startTime.Format(time.RFC3339),
		},
	}); err != nil {
		return fmt.Errorf("failed to mark index state dirty: %w", err)
	}

	if len(toRemove) > 0 {
		re.logger.Info("Removing deleted documents", zap.Int("count", len(toRemove)))
		for _, filePath := range toRemove {
			if err := re.vecService.removeDocument(filePath); err != nil {
				return fmt.Errorf("failed to remove indexed document %q: %w", filePath, err)
			}
		}
	}

	indexedModel := oldModel
	indexedFilesToUpsert := make(map[string]*vectorstore.IndexedFileInfo)
	failedFiles := make(map[string]struct{})

	if len(toAdd) > 0 {
		re.logger.Info("Starting to index documents", zap.Int("count", len(toAdd)))

		chunkCountsByFile := make(map[string]int)
		docsByID := make(map[string]document, len(toAdd))
		for _, doc := range toAdd {
			chunkCountsByFile[doc.Path]++
			docsByID[doc.ID] = doc
		}

		// Build a content-hash → embedding reuse map from the existing chunks of
		// each file we're about to re-index, BEFORE deleting them. Structure-aware
		// chunking (T49) keeps unchanged sections byte-identical, so editing one
		// section of a large file reuses the embeddings of every other section
		// instead of re-embedding the whole file (T70 incremental re-index).
		// Skipped on a full rebuild: the model or chunker may have changed, so old
		// vectors are not reusable.
		reuseEmbeddings := make(map[string][]float32)
		if !needsRebuild {
			for path := range chunkCountsByFile {
				existing, err := re.vecService.store.ChunksByDocPath(path)
				if err != nil {
					re.logger.Warn("Failed to load existing chunks for reuse",
						zap.String("path", path), zap.Error(err))
					continue
				}
				for _, c := range existing {
					if len(c.Embedding) > 0 {
						reuseEmbeddings[calculateFileHash(c.Content)] = c.Embedding
					}
				}
			}
		}

		// Delete old chunks before re-indexing to prevent stale data.
		// Upsert (INSERT OR REPLACE) only replaces by chunk ID; if chunk
		// count changes, orphaned chunks with higher indices would remain.
		cleanedPaths := make(map[string]struct{})
		for _, doc := range toAdd {
			if _, done := cleanedPaths[doc.Path]; done {
				continue
			}
			cleanedPaths[doc.Path] = struct{}{}
			if err := re.vecService.removeDocument(doc.Path); err != nil {
				re.logger.Warn("Failed to clean old chunks before re-index",
					zap.String("path", doc.Path), zap.Error(err))
			}
		}

		batchSize := 50
		totalBatches := (len(toAdd) + batchSize - 1) / batchSize
		currentBatchModel := ""

		for batchNum := 0; batchNum < totalBatches; batchNum++ {
			start := batchNum * batchSize
			end := start + batchSize
			if end > len(toAdd) {
				end = len(toAdd)
			}
			batch := toAdd[start:end]

			re.logger.Info("Processing batch",
				zap.Int("batch", batchNum+1),
				zap.Int("total", totalBatches))

			result, err := re.vecService.indexDocuments(ctx, batch, reuseEmbeddings)
			if err != nil {
				return fmt.Errorf("failed to index: %w", err)
			}
			if result.ModelID != "" {
				if currentBatchModel == "" {
					currentBatchModel = result.ModelID
				} else if currentBatchModel != result.ModelID {
					return fmt.Errorf("embedding model changed during indexing: started with %s, got %s", currentBatchModel, result.ModelID)
				}
			}

			for _, failedID := range result.FailedIDs {
				doc, ok := docsByID[failedID]
				if !ok {
					continue
				}
				failedFiles[doc.Path] = struct{}{}
				delete(indexedFilesToUpsert, doc.Path)
			}

			for _, successID := range result.SuccessIDs {
				doc, ok := docsByID[successID]
				if !ok {
					continue
				}
				if _, failed := failedFiles[doc.Path]; failed {
					continue
				}
				if _, alreadyTracked := indexedFilesToUpsert[doc.Path]; alreadyTracked {
					continue
				}

				fileHash := doc.FileHash
				if fileHash == "" {
					fileHash = calculateFileHash(doc.Content)
				}

				chunkCount := chunkCountsByFile[doc.Path]
				if chunkCount == 0 {
					chunkCount = 1
				}

				indexedFilesToUpsert[doc.Path] = &vectorstore.IndexedFileInfo{
					FilePath:   doc.Path,
					Hash:       fileHash,
					ModTime:    doc.LastModified,
					Size:       int64(len(doc.Content)),
					ChunkCount: chunkCount,
				}
			}

			if len(failedFiles) > 0 {
				re.logger.Warn("Files with failed chunks will be re-indexed next cycle",
					zap.Int("failed_files", len(failedFiles)))
			}

			re.logger.Info("Batch indexed",
				zap.Int("success", len(result.SuccessIDs)),
				zap.Int("failed", len(result.FailedIDs)))

			if batchNum < totalBatches-1 {
				time.Sleep(50 * time.Millisecond)
			}
		}

		if currentBatchModel != "" {
			indexedModel = currentBatchModel
		}
	}

	deletePaths := append([]string(nil), toRemove...)
	for filePath := range failedFiles {
		deletePaths = append(deletePaths, filePath)
	}
	sort.Strings(deletePaths)

	upsertPaths := make([]string, 0, len(indexedFilesToUpsert))
	for filePath := range indexedFilesToUpsert {
		upsertPaths = append(upsertPaths, filePath)
	}
	sort.Strings(upsertPaths)

	upsertFiles := make([]*vectorstore.IndexedFileInfo, 0, len(upsertPaths))
	for _, filePath := range upsertPaths {
		upsertFiles = append(upsertFiles, indexedFilesToUpsert[filePath])
	}

	finalMetadata := map[string]string{
		"chunker_version":     chunkerVersion,
		indexStateMetadataKey: indexStateReady,
		indexStartedAtKey:     startTime.Format(time.RFC3339),
		"last_indexed":        time.Now().UTC().Format(time.RFC3339),
	}
	if indexedModel != "" {
		finalMetadata["embedding_model"] = indexedModel
	}
	if len(failedFiles) > 0 {
		finalMetadata[indexStateMetadataKey] = indexStateDirty
	}

	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata:        finalMetadata,
		UpsertFiles:     upsertFiles,
		DeleteFilePaths: deletePaths,
	}); err != nil {
		return fmt.Errorf("failed to commit index state: %w", err)
	}

	// Remove orphan chunks that survived a failed removeDocument during re-indexing.
	if orphans, err := store.CleanOrphans(); err != nil {
		re.logger.Warn("Post-indexing orphan cleanup failed", zap.Error(err))
	} else if orphans > 0 {
		re.logger.Info("Post-indexing orphan cleanup", zap.Int("removed", orphans))
	}

	duration := time.Since(startTime)
	re.logger.Info("Indexing completed", zap.Duration("duration", duration))

	if len(failedFiles) > 0 {
		return fmt.Errorf("indexing completed with %d failed file(s); index state remains dirty for recovery", len(failedFiles))
	}

	return nil
}

func (re *Engine) calculateIndexChanges(currentDocs []document, indexedFiles map[string]*vectorstore.IndexedFileInfo, forceRebuild bool) (toAdd []document, toRemove []string) {
	if forceRebuild {
		toAdd = append(toAdd, currentDocs...)
		for filePath := range indexedFiles {
			toRemove = append(toRemove, filePath)
		}
		return toAdd, toRemove
	}

	// Group docs by path for O(1) lookup
	docsByPath := make(map[string][]document)
	for _, doc := range currentDocs {
		docsByPath[doc.Path] = append(docsByPath[doc.Path], doc)
	}

	for filePath := range indexedFiles {
		if _, exists := docsByPath[filePath]; !exists {
			toRemove = append(toRemove, filePath)
		}
	}

	addedFiles := make(map[string]bool)

	for path, docs := range docsByPath {
		if addedFiles[path] {
			continue
		}

		indexed, exists := indexedFiles[path]
		if forceRebuild || !exists {
			toAdd = append(toAdd, docs...)
			addedFiles[path] = true
			continue
		}

		fileHash := docs[0].FileHash
		if fileHash == "" {
			fileHash = calculateFileHash(docs[0].Content)
		}

		if indexed.Hash != fileHash || indexed.ModTime.Before(docs[0].LastModified) {
			toAdd = append(toAdd, docs...)
			addedFiles[path] = true
		}
	}

	return toAdd, toRemove
}

func (re *Engine) autoIndexIfNeeded() {
	select {
	case <-time.After(2 * time.Second):
	case <-re.stopWatcher:
		return
	}

	if re.needsIndexing() {
		re.logger.Info("Index needs updating, starting auto-indexing")
		re.indexWithLock("initial_startup")
	} else {
		re.logger.Info("Index is up to date, skipping auto-indexing")
	}
}

func (re *Engine) startFileWatcher() {
	interval := re.config.RAG.WatchInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	debounceDuration := re.config.RAG.DebounceDuration
	if debounceDuration <= 0 {
		debounceDuration = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var pendingMu sync.Mutex
	var pendingReindex bool
	var debounceTimer *time.Timer

	for {
		select {
		case <-re.stopWatcher:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case <-ticker.C:
			if re.needsIndexing() {
				pendingMu.Lock()
				pendingReindex = true
				pendingMu.Unlock()

				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				debounceTimer = time.AfterFunc(debounceDuration, func() {
					pendingMu.Lock()
					shouldIndex := pendingReindex
					pendingReindex = false
					pendingMu.Unlock()
					if shouldIndex {
						re.indexWithLock("file_watcher")
					}
				})
			}
		}
	}
}

func (re *Engine) indexWithLock(trigger string) {
	re.mu.Lock()
	if re.indexing {
		re.mu.Unlock()
		re.logger.Debug("Indexing already in progress, skipping", zap.String("trigger", trigger))
		return
	}
	re.indexing = true
	re.mu.Unlock()

	defer func() {
		re.mu.Lock()
		re.indexing = false
		re.mu.Unlock()
	}()

	re.logger.Info("Starting indexing", zap.String("trigger", trigger))
	err := re.IndexDocuments(context.Background())
	if err != nil {
		re.logger.Error("Indexing failed", zap.Error(err), zap.String("trigger", trigger))
	} else {
		re.logger.Info("Indexing completed successfully", zap.String("trigger", trigger))
		re.mu.Lock()
		re.lastIndexCheck = time.Now()
		re.mu.Unlock()
	}
}

func (re *Engine) needsIndexing() bool {
	store := re.vecService.store

	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		re.logger.Warn("Failed to get indexed files from store, indexing required",
			zap.Error(err),
			zap.String("store_type", "SQLiteStore"),
		)
		return true
	}
	if len(indexedFiles) == 0 {
		re.logger.Info("No indexed files found, indexing required",
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	currentDocs, err := re.docService.collectDocuments()
	if err != nil {
		re.logger.Warn("Failed to check filesystem, indexing required",
			zap.Error(err),
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	toAdd, toRemove := re.calculateIndexChanges(currentDocs, indexedFiles, false)

	needsIndex := len(toAdd) > 0 || len(toRemove) > 0
	if needsIndex {
		re.logger.Info("Index needs updating",
			zap.Int("indexed", len(indexedFiles)),
			zap.Int("current", len(currentDocs)),
			zap.Int("to_add", len(toAdd)),
			zap.Int("to_remove", len(toRemove)),
			zap.String("repo_root", re.repoRoot),
		)
	} else {
		re.logger.Debug("Index is up to date",
			zap.Int("indexed", len(indexedFiles)),
			zap.Int("current", len(currentDocs)),
		)
	}

	return needsIndex
}

// === Vector Service ===
