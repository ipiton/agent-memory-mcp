package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

type documentService struct {
	config docServiceConfig
	logger *zap.Logger
}

var defaultIndexExcludeDirs = map[string]struct{}{
	".git":              {},
	".idea":             {},
	".vscode":           {},
	".next":             {},
	".terraform":        {},
	".agent-memory":     {},
	"node_modules":      {},
	"vendor":            {},
	"dist":              {},
	"build":             {},
	"coverage":          {},
	"logs":              {},
	"artifacts":         {},
	"test-results":      {},
	"playwright-report": {},
}

var privateKeyBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
var inlineSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s]+)`),
	regexp.MustCompile(`(?i)(\b(?:api[_-]?key|secret|token|password|passwd|client_secret|access_key)\b\s*[:=]\s*)(.+)$`),
}

func calculateFileHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func newDocumentService(cfg docServiceConfig, logger *zap.Logger) *documentService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &documentService{config: cfg, logger: logger}
}

func (ds *documentService) collectDocuments() ([]document, error) {
	var allDocs []document

	for _, dir := range ds.config.IndexDirs {
		fullPath := dir
		if !filepath.IsAbs(dir) {
			fullPath = filepath.Join(ds.config.RepoRoot, dir)
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			ds.logger.Warn("Path not found", zap.String("path", fullPath))
			continue
		}

		if !info.IsDir() {
			if ds.shouldSkipPath(fullPath, false) {
				continue
			}
			if ds.supportedSourceType(fullPath) != "" {
				docs, err := ds.processFile(fullPath)
				if err != nil {
					ds.logger.Warn("Failed to process file", zap.String("path", fullPath), zap.Error(err))
				} else {
					allDocs = append(allDocs, docs...)
				}
			}
			continue
		}

		err = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path != fullPath && ds.shouldSkipPath(path, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() && ds.supportedSourceType(path) != "" {
				docs, err := ds.processFile(path)
				if err != nil {
					ds.logger.Warn("Failed to process file", zap.String("path", path), zap.Error(err))
					return nil
				}
				allDocs = append(allDocs, docs...)
			}
			return nil
		})
		if err != nil {
			ds.logger.Error("Failed to walk directory", zap.String("path", fullPath), zap.Error(err))
		}
	}

	return allDocs, nil
}

func (ds *documentService) processFile(path string) ([]document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	relPath := ds.relPath(path)
	sourceType := classifySourceType(relPath, "", "")
	if sourceType == "" {
		return nil, nil
	}

	cleanContent := string(content)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		cleanContent = ds.removeFrontmatter(cleanContent)
	}
	if ds.config.RedactSecrets {
		cleanContent = redactSensitiveContent(cleanContent)
	}
	title := ds.extractTitle(cleanContent, filepath.Base(path))
	if title == "" {
		title = filepath.Base(path)
	}

	fileHash := calculateFileHash(cleanContent)
	modTime := ds.getFileModTime(path)
	chunks := ds.splitIntoChunks(cleanContent)

	var docs []document
	for i, chunk := range chunks {
		docs = append(docs, document{
			ID:           fmt.Sprintf("%s-%d", relPath, i),
			Content:      chunk,
			Title:        title,
			Path:         relPath,
			LastModified: modTime,
			FileHash:     fileHash,
		})
	}

	return docs, nil
}

func (ds *documentService) supportedSourceType(absPath string) string {
	return classifySourceType(ds.relPath(absPath), "", "")
}

func (ds *documentService) relPath(abs string) string {
	rel, err := filepath.Rel(ds.config.RepoRoot, abs)
	if err != nil {
		return filepath.ToSlash(filepath.Base(abs))
	}
	return filepath.ToSlash(rel)
}

func (ds *documentService) shouldSkipPath(absPath string, isDir bool) bool {
	relPath := ds.relPath(absPath)
	base := filepath.Base(absPath)

	if isDir {
		if _, ok := defaultIndexExcludeDirs[base]; ok {
			return true
		}
		for _, excluded := range ds.config.IndexExcludeDirs {
			cleanExcluded := filepath.ToSlash(filepath.Clean(excluded))
			if cleanExcluded == "" || cleanExcluded == "." {
				continue
			}
			if base == cleanExcluded || relPath == cleanExcluded || strings.HasPrefix(relPath, cleanExcluded+"/") {
				return true
			}
		}
	}

	for _, pattern := range ds.config.IndexExcludeGlobs {
		if globMatches(pattern, relPath) || globMatches(pattern, base) {
			return true
		}
	}

	return false
}

func globMatches(pattern string, value string) bool {
	if pattern == "" {
		return false
	}
	matched, err := pathpkg.Match(pattern, value)
	return err == nil && matched
}

func classifySourceType(docPath string, title string, content string) string {
	pathLower := strings.ToLower(filepath.ToSlash(docPath))
	baseLower := strings.ToLower(filepath.Base(pathLower))
	titleLower := strings.ToLower(title)
	contentLower := strings.ToLower(content)
	ext := strings.ToLower(filepath.Ext(pathLower))

	switch {
	case baseLower == "changelog.md" || strings.Contains(baseLower, "release-notes") || strings.Contains(pathLower, "/changelog") || strings.Contains(titleLower, "release notes"):
		return "changelog"
	case strings.Contains(pathLower, "/runbook") || strings.Contains(pathLower, "runbooks/") || strings.Contains(titleLower, "runbook"):
		return "runbook"
	case strings.Contains(pathLower, "/postmortem") || strings.Contains(pathLower, "postmortems/") || strings.Contains(titleLower, "postmortem"):
		return "postmortem"
	case strings.Contains(pathLower, "/adr") || strings.HasPrefix(baseLower, "adr") || strings.Contains(titleLower, "architecture decision"):
		return "adr"
	case strings.Contains(pathLower, "/rfc") || strings.HasPrefix(titleLower, "rfc") || strings.Contains(baseLower, "rfc"):
		return "rfc"
	case strings.Contains(pathLower, ".github/workflows/") || baseLower == ".gitlab-ci.yml" || baseLower == "jenkinsfile":
		return "ci_config"
	case strings.Contains(pathLower, "helm/") || baseLower == "chart.yaml" || baseLower == "chart.yml" || (strings.HasPrefix(baseLower, "values") && (ext == ".yaml" || ext == ".yml")):
		return "helm"
	case strings.Contains(pathLower, "terraform/") || ext == ".tf" || ext == ".tfvars" || ext == ".hcl":
		return "terraform"
	case strings.Contains(pathLower, "/k8s/") || strings.Contains(pathLower, "/kubernetes/") || strings.HasPrefix(baseLower, "deployment.") || strings.HasPrefix(baseLower, "service.") || strings.HasPrefix(baseLower, "ingress."):
		if ext == ".yaml" || ext == ".yml" {
			return "k8s"
		}
	case ext == ".md":
		if baseLower == "readme.md" || strings.HasPrefix(pathLower, "docs/") || strings.Contains(pathLower, "/docs/") || strings.Contains(contentLower, "# ") {
			return "docs"
		}
	case ext == ".yaml" || ext == ".yml":
		if strings.Contains(contentLower, "apiVersion:") && strings.Contains(contentLower, "kind:") {
			return "k8s"
		}
	}

	return ""
}

func (ds *documentService) extractTitle(content, filename string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

func (ds *documentService) removeFrontmatter(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && strings.TrimSpace(lines[0]) == "---" {
		for i, line := range lines[1:] {
			if strings.TrimSpace(line) == "---" {
				return strings.Join(lines[i+2:], "\n")
			}
		}
	}
	return content
}

func (ds *documentService) splitIntoChunks(content string) []string {
	chunkSize := ds.config.ChunkSize
	overlap := ds.config.ChunkOverlap

	if len(content) <= chunkSize {
		return []string{content}
	}

	var chunks []string
	contentLen := len(content)
	step := chunkSize - overlap

	for start := 0; start < contentLen; start += step {
		end := start + chunkSize
		if end > contentLen {
			end = contentLen
		}

		if end < contentLen {
			breakPoint := end
			for i := end; i > end-100 && i > start; i-- {
				if content[i] == ' ' || content[i] == '\n' {
					breakPoint = i
					break
				}
			}
			end = breakPoint
		}

		chunk := strings.TrimSpace(content[start:end])
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}

		if end >= contentLen {
			break
		}
	}

	return chunks
}

func (ds *documentService) getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Now()
	}
	return info.ModTime()
}

func redactSensitiveContent(content string) string {
	redacted := privateKeyBlockPattern.ReplaceAllString(content, "[REDACTED PRIVATE KEY]")
	lines := strings.Split(redacted, "\n")
	for i, line := range lines {
		for _, pattern := range inlineSecretPatterns {
			line = pattern.ReplaceAllString(line, "${1}[REDACTED]")
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
