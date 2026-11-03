package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DataScopeProject keeps a project's data in the user's directory, keyed by the
// repository rather than by the checkout (T165). The default scope keeps
// {root}/data, which installations already have data in.
const DataScopeProject = "project"

// projectData says where a project's data lives and which tree its index covers.
type projectData struct {
	dataPath string
	// indexRoot is the main checkout, so every worktree shares one index built
	// from one tree. Indexed paths are relative to the root: two trees writing to
	// one index would delete each other's branch-only files, which is what the
	// T161 root guard refuses. Empty when there is no main checkout to name
	// (a bare repository), and the caller's own root stays the index root.
	indexRoot string
}

var projectSlugUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// resolveProjectData survives deleting a clone, and worktrees of one repository
// land on one directory. Moving the repository changes the key: the data stays
// where it was and has to be moved by hand, or pointed at with MCP_DATA_PATH.
func resolveProjectData(root, xdgDataHome, home string) projectData {
	repo := findGitRepository(root)

	key, name, indexRoot := root, filepath.Base(root), ""
	if repo.commonDir != "" {
		key, name, indexRoot = repo.commonDir, repo.name(), repo.mainCheckout
	}

	base := xdgDataHome
	if base == "" {
		if home == "" {
			// No user directory, as in a bare container: the project is the only
			// place known to be there.
			return projectData{dataPath: filepath.Join(root, ".agent-memory"), indexRoot: indexRoot}
		}
		base = filepath.Join(home, ".local", "share")
	}

	sum := sha256.Sum256([]byte(canonicalPath(key)))
	id := fmt.Sprintf("%s-%s", projectSlugUnsafe.ReplaceAllString(name, "_"), hex.EncodeToString(sum[:6]))
	return projectData{
		dataPath:  filepath.Join(base, "agent-memory-mcp", "projects", id),
		indexRoot: indexRoot,
	}
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

type gitRepository struct {
	// commonDir is shared by all worktrees of a repository. A submodule has its
	// own, so it counts as a project of its own.
	commonDir    string
	mainCheckout string
}

func (r gitRepository) name() string {
	if r.mainCheckout != "" {
		return filepath.Base(r.mainCheckout)
	}
	return strings.TrimSuffix(filepath.Base(r.commonDir), ".git")
}

// findGitRepository reads the layout directly: a plugin may start where git is
// not on PATH, and the format of these files is stable.
func findGitRepository(start string) gitRepository {
	for dir := start; ; dir = filepath.Dir(dir) {
		marker := filepath.Join(dir, ".git")
		info, err := os.Stat(marker)
		if err == nil {
			if info.IsDir() {
				return gitRepository{commonDir: canonicalPath(marker), mainCheckout: canonicalPath(dir)}
			}
			return repositoryFromGitFile(marker, dir)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return gitRepository{}
		}
	}
}

// repositoryFromGitFile handles the two things a .git file means: a worktree,
// whose git directory has a commondir file, and a submodule, whose has none.
func repositoryFromGitFile(marker, checkout string) gitRepository {
	raw, err := os.ReadFile(marker)
	if err != nil {
		return gitRepository{}
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return gitRepository{}
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(checkout, gitDir)
	}

	rawCommon, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitRepository{commonDir: canonicalPath(gitDir), mainCheckout: canonicalPath(checkout)}
	}
	common := strings.TrimSpace(string(rawCommon))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	common = canonicalPath(common)

	repo := gitRepository{commonDir: common}
	if filepath.Base(common) == ".git" {
		repo.mainCheckout = filepath.Dir(common)
	}
	return repo
}
