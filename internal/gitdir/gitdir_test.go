package gitdir

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(CleanEnv(os.Environ()),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo creates a repository with one commit and returns its root (symlinks resolved, since git
// reports physical paths).
func repo(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	return root
}

func TestFindMainWorktree(t *testing.T) {
	root := repo(t, "shop")
	info, ok, err := Git{}.Find(context.Background(), filepath.Join(root, "sub"))
	if err != nil || !ok {
		t.Fatalf("Find: ok=%v err=%v", ok, err)
	}
	want := Info{Root: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git")}
	if info != want {
		t.Errorf("got %+v, want %+v", info, want)
	}
	if info.Linked() {
		t.Error("main worktree reported as linked")
	}
	if info.MainName() != "shop" {
		t.Errorf("MainName = %q", info.MainName())
	}
}

func TestFindLinkedWorktree(t *testing.T) {
	root := repo(t, "shop")
	wt := filepath.Join(filepath.Dir(root), "feature-x")
	git(t, root, "worktree", "add", "-q", wt)

	info, ok, err := Git{}.Find(context.Background(), wt)
	if err != nil || !ok {
		t.Fatalf("Find: ok=%v err=%v", ok, err)
	}
	if info.Root != wt || info.CommonDir != filepath.Join(root, ".git") || !info.Linked() {
		t.Errorf("unexpected %+v", info)
	}
	if info.MainName() != "shop" {
		t.Errorf("MainName = %q, want main worktree's name", info.MainName())
	}
}

func TestFindOutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, ok, err := Git{}.Find(context.Background(), t.TempDir())
	if err != nil || ok {
		t.Fatalf("want ok=false without error, got ok=%v err=%v", ok, err)
	}
}

func TestFindIgnoresGitDirEnv(t *testing.T) {
	root := repo(t, "shop")
	other := repo(t, "other")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	info, _, err := Git{}.Find(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != root {
		t.Errorf("GIT_DIR leaked into lookup: %+v", info)
	}
}

func TestWorktreesMarksRemovedOnesPrunable(t *testing.T) {
	root := repo(t, "shop")
	wt := filepath.Join(filepath.Dir(root), "gone")
	git(t, root, "worktree", "add", "-q", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	list, err := Git{}.Worktrees(context.Background(), filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Path != root || list[0].Prunable || list[1].Path != wt || !list[1].Prunable {
		t.Errorf("unexpected list %+v", list)
	}
}

func TestBranch(t *testing.T) {
	root := repo(t, "shop")
	b, err := Git{}.Branch(context.Background(), root)
	if err != nil || b != "main" {
		t.Fatalf("Branch = %q, %v", b, err)
	}
}

func TestMissingBinary(t *testing.T) {
	_, _, err := Git{Path: "/nonexistent/git"}.Find(context.Background(), t.TempDir())
	if !errors.Is(err, ErrNoGit) {
		t.Fatalf("want ErrNoGit, got %v", err)
	}
}

func TestMainNameBare(t *testing.T) {
	if got := (Info{CommonDir: "/srv/shop.git"}).MainName(); got != "shop" {
		t.Errorf("MainName = %q", got)
	}
}

// fakeGit writes an executable that prints out and exits with code.
func fakeGit(t *testing.T, out string, code int) Git {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s' '" + out + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Git{Path: path}
}

func TestFindRejectsUnexpectedOutput(t *testing.T) {
	_, _, err := fakeGit(t, "only-one-line", 0).Find(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("want error for malformed rev-parse output")
	}
}

func TestFindReportsOtherGitFailures(t *testing.T) {
	_, ok, err := fakeGit(t, "", 3).Find(context.Background(), t.TempDir())
	if err == nil || ok {
		t.Fatalf("want error, got ok=%v err=%v", ok, err)
	}
}

func TestFindInMissingDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, _, err := Git{}.Find(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("want error for a missing directory")
	}
}

func TestWorktreesOfMissingRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := (Git{}).Worktrees(context.Background(), filepath.Join(t.TempDir(), "nope.git")); err == nil {
		t.Fatal("want error")
	}
}

func TestCleanEnv(t *testing.T) {
	got := CleanEnv([]string{"HOME=/h", "GIT_DIR=/x", "GIT_INDEX_FILE=/i", "LC_ALL=de_DE", "GIT_AUTHOR_NAME=a"})
	want := []string{"HOME=/h", "GIT_AUTHOR_NAME=a", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindInsideGitHookEnv(t *testing.T) {
	root := repo(t, "shop")
	other := repo(t, "other")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	wt := filepath.Join(filepath.Dir(root), "feature")
	git(t, root, "worktree", "add", "-q", wt)
	info, _, err := Git{}.Find(context.Background(), wt)
	if err != nil || info.CommonDir != filepath.Join(root, ".git") {
		t.Fatalf("hook env leaked: %+v, %v", info, err)
	}
}

func TestWorktreeRoot(t *testing.T) {
	root := repo(t, "shop")
	wt := filepath.Join(filepath.Dir(root), "feature")
	git(t, root, "worktree", "add", "-q", wt)
	info, _, err := Git{}.Find(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok, err := WorktreeRoot(info.GitDir); err != nil || !ok || got != wt {
		t.Fatalf("linked: %q %v %v", got, ok, err)
	}
	if got, ok, err := WorktreeRoot(info.CommonDir); err != nil || !ok || got != root {
		t.Fatalf("main: %q %v %v", got, ok, err)
	}

	moved := filepath.Join(filepath.Dir(root), "moved")
	git(t, root, "worktree", "move", wt, moved)
	if got, _, _ := WorktreeRoot(info.GitDir); got != moved {
		t.Fatalf("after move: %q, want %q", got, moved)
	}

	git(t, root, "worktree", "remove", moved)
	if _, ok, err := WorktreeRoot(info.GitDir); ok || err != nil {
		t.Fatalf("after remove: ok=%v err=%v", ok, err)
	}
	if _, ok, err := WorktreeRoot(filepath.Join(t.TempDir(), ".git")); ok || err != nil {
		t.Fatalf("missing main: ok=%v err=%v", ok, err)
	}
}

func TestWorktreeRootRelativePointerAndErrors(t *testing.T) {
	admin := filepath.Join(t.TempDir(), "worktrees", "x")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte("../../wt/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := WorktreeRoot(admin)
	if err != nil || !ok || got != filepath.Join(filepath.Dir(filepath.Dir(admin)), "wt") {
		t.Fatalf("got %q %v %v", got, ok, err)
	}

	// gitdir is a directory: a read error that is not "missing"
	bad := filepath.Join(t.TempDir(), "worktrees", "y")
	if err := os.MkdirAll(filepath.Join(bad, "gitdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorktreeRoot(bad); err == nil {
		t.Fatal("want error")
	}
	// main .git below a file: stat error that is not "missing"
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorktreeRoot(filepath.Join(file, ".git")); err == nil {
		t.Fatal("want error")
	}
}

func TestAddAndRemoveWorktrees(t *testing.T) {
	root := repo(t, "shop")
	ctx := context.Background()
	g := Git{}
	base := filepath.Dir(root)

	if err := g.AddWorktree(ctx, root, filepath.Join(base, "shop-new"), "", "new", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.Branch(ctx, filepath.Join(base, "shop-new")); b != "new" {
		t.Errorf("branch %q", b)
	}
	git(t, root, "branch", "existing")
	if err := g.AddWorktree(ctx, root, filepath.Join(base, "shop-existing"), "existing", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := g.AddWorktree(ctx, root, filepath.Join(base, "shop-from"), "", "from", "existing"); err != nil {
		t.Fatal(err)
	}
	if err := g.AddWorktree(ctx, root, filepath.Join(base, "shop-x"), "no-such-branch", "", ""); err == nil {
		t.Error("unknown branch: want error")
	}

	wt := filepath.Join(base, "shop-new")
	if dirty, err := g.Dirty(ctx, wt); err != nil || dirty {
		t.Fatalf("clean: %v %v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := g.Dirty(ctx, wt); !dirty {
		t.Fatal("untracked file not reported")
	}
	if err := g.RemoveWorktree(ctx, root, wt, false); err == nil {
		t.Fatal("dirty worktree removed without force")
	}
	if err := g.RemoveWorktree(ctx, root, wt, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("still there")
	}
	info, _, _ := g.Find(ctx, root)
	if info.MainRoot() != root {
		t.Errorf("MainRoot %s", info.MainRoot())
	}
}
