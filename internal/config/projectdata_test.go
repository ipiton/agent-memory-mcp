package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func writeText(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

func TestProjectDataOfPlainRepository(t *testing.T) {
	base := evalSymlinks(t, t.TempDir())
	repo := filepath.Join(base, "My Repo")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "sub", "dir"))

	got := resolveProjectData(repo, "", "/home/u")
	if !strings.HasPrefix(got.dataPath, "/home/u/.local/share/agent-memory-mcp/projects/My_Repo-") {
		t.Errorf("dataPath = %q", got.dataPath)
	}
	if got.indexRoot != repo {
		t.Errorf("indexRoot = %q, want %q", got.indexRoot, repo)
	}
	if fromSub := resolveProjectData(filepath.Join(repo, "sub", "dir"), "", "/home/u"); fromSub != got {
		t.Errorf("from a subdirectory: %+v, want %+v", fromSub, got)
	}
	if xdg := resolveProjectData(repo, "/xdg", "/home/u"); !strings.HasPrefix(xdg.dataPath, "/xdg/agent-memory-mcp/projects/") {
		t.Errorf("XDG_DATA_HOME ignored: %q", xdg.dataPath)
	}
}

func TestProjectDataSeparatesClones(t *testing.T) {
	base := t.TempDir()
	one, two := filepath.Join(base, "a", "repo"), filepath.Join(base, "b", "repo")
	mkdirs(t, filepath.Join(one, ".git"), filepath.Join(two, ".git"))

	if a, b := resolveProjectData(one, "", "/h"), resolveProjectData(two, "", "/h"); a.dataPath == b.dataPath {
		t.Errorf("two clones with one name share %q", a.dataPath)
	}
}

func TestProjectDataWorktreeSharesMainCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := evalSymlinks(t, t.TempDir())
	main, tree := filepath.Join(base, "main"), filepath.Join(base, "trees", "feature")
	mkdirs(t, main)
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", tree, "-b", "feature"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	fromMain, fromTree := resolveProjectData(main, "", "/h"), resolveProjectData(tree, "", "/h")
	if fromMain != fromTree {
		t.Errorf("worktree %+v, main checkout %+v", fromTree, fromMain)
	}
	if fromTree.indexRoot != main {
		t.Errorf("indexRoot = %q, want the main checkout %q", fromTree.indexRoot, main)
	}
}

func TestProjectDataWorktreeOfBareRepository(t *testing.T) {
	base := evalSymlinks(t, t.TempDir())
	common := filepath.Join(base, "proj.git")
	tree := filepath.Join(base, "tree")
	gitDir := filepath.Join(common, "worktrees", "tree")
	mkdirs(t, gitDir, tree)
	writeText(t, filepath.Join(gitDir, "commondir"), "../..\n")
	writeText(t, filepath.Join(tree, ".git"), "gitdir: "+gitDir+"\n")

	got := resolveProjectData(tree, "", "/h")
	if !strings.Contains(got.dataPath, "/projects/proj-") {
		t.Errorf("dataPath = %q", got.dataPath)
	}
	if got.indexRoot != "" {
		t.Errorf("indexRoot = %q, a bare repository has no main checkout", got.indexRoot)
	}
}

func TestProjectDataSubmoduleIsItsOwnProject(t *testing.T) {
	base := evalSymlinks(t, t.TempDir())
	super := filepath.Join(base, "super")
	sub := filepath.Join(super, "vendor", "lib")
	mkdirs(t, filepath.Join(super, ".git", "modules", "lib"), sub)
	writeText(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")

	got := resolveProjectData(sub, "", "/h")
	if got.dataPath == resolveProjectData(super, "", "/h").dataPath {
		t.Errorf("submodule shares the superproject's data: %q", got.dataPath)
	}
	if got.indexRoot != sub {
		t.Errorf("indexRoot = %q, want %q", got.indexRoot, sub)
	}
}

func TestProjectDataWithoutGit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plain")
	mkdirs(t, root)

	got := resolveProjectData(root, "", "/h")
	if !strings.Contains(got.dataPath, "/projects/plain-") || got.indexRoot != "" {
		t.Errorf("got %+v", got)
	}
}

func TestProjectDataWithoutHome(t *testing.T) {
	root := t.TempDir()
	got := resolveProjectData(root, "", "")
	if want := filepath.Join(root, ".agent-memory"); got.dataPath != want {
		t.Errorf("dataPath = %q, want %q", got.dataPath, want)
	}
}

func TestProjectDataIgnoresWhatIsNotAGitFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "odd")
	mkdirs(t, root)
	writeText(t, filepath.Join(root, ".git"), "not a pointer")

	if got := resolveProjectData(root, "", "/h"); got.indexRoot != "" {
		t.Errorf("got %+v", got)
	}
}
