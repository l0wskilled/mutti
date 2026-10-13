package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
)

func TestWorktreeAddCreatesAndStarts(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "feature-x"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(root), "shop-feature-x")
	if res.Path != want || res.Branch != "feature-x" || res.Up.Instance.Name != "shop-feature-x" || res.Up.URL != "https://feature-x.shop.localhost" {
		t.Fatalf("result %+v %+v", res, res.Up)
	}
	if b, _ := (gitdir.Git{}).Branch(ctx, want); b != "feature-x" {
		t.Errorf("branch %q", b)
	}
}

func TestWorktreeAddVariants(t *testing.T) {
	root := newRepo(t, "shop")
	runGit(t, root, "branch", "existing")
	h := newHarness(t, webModel())

	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "review", Branch: "existing", NoUp: true})
	if err != nil || res.Branch != "existing" || res.Up != nil {
		t.Fatalf("existing branch: %+v %v", res, err)
	}
	if b, _ := (gitdir.Git{}).Branch(ctx, res.Path); b != "existing" {
		t.Errorf("checked out %q", b)
	}

	res, err = h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "fix", From: "existing", Path: "../custom", NoUp: true})
	if err != nil || res.Path != filepath.Join(filepath.Dir(root), "custom") {
		t.Fatalf("from + relative path: %+v %v", res, err)
	}

	abs := filepath.Join(t.TempDir(), "elsewhere")
	if res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "abs", Path: abs, NoUp: true}); err != nil || res.Path != abs {
		t.Fatalf("absolute path: %+v %v", res, err)
	}
}

func TestWorktreeAddFailures(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())

	for _, bad := range []string{"Feature", "a b", ""} {
		if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: bad}); appErrCode(err) != CodeInvalidName {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := h.app.WorktreeAdd(ctx, t.TempDir(), WorktreeAddOptions{Name: "x"}); appErrCode(err) != CodeNotGit {
		t.Errorf("outside git: %v", err)
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "x", Branch: "nope"}); appErrCode(err) != CodeGitFailed {
		t.Errorf("unknown branch: %v", err)
	}
	h.compose.Exit = func(args []string) int { return map[string]int{"up": 1}[args[0]] }
	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "broken"})
	if appErrCode(err) != CodeComposeFailed || res == nil || res.Path == "" {
		t.Errorf("failing up keeps the worktree: %+v %v", res, err)
	}
}

func TestWorktreeRemove(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "gone"})
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(res.Path, "wip.txt"), "unsaved")
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "gone"}); appErrCode(err) != CodeDirtyTree {
		t.Fatalf("dirty: %v", err)
	}
	if s := summaryOf(t, h, "shop-gone"); s.Status != StatusRunning {
		t.Fatal("a refused removal destroyed the instance")
	}

	path, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "gone", Force: true})
	if err != nil || path != res.Path {
		t.Fatalf("force: %q %v", path, err)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Error("worktree still there")
	}
	if l, _ := h.app.List(ctx); len(l) != 0 {
		t.Errorf("instance left: %+v", l)
	}

	// By relative path, clean worktree.
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "rel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "../shop-rel"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeRemoveRefusals(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())

	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: root}); appErrCode(err) != CodeMainWorktree {
		t.Errorf("main: %v", err)
	}
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "never-made"}); appErrCode(err) != CodeNotGit {
		t.Errorf("unknown: %v", err)
	}
	other := newRepo(t, "other")
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: other}); appErrCode(err) != CodeNotGit {
		t.Errorf("foreign repo: %v", err)
	}
	if _, err := h.app.WorktreeRemove(ctx, t.TempDir(), WorktreeRemoveOptions{Target: "x"}); appErrCode(err) != CodeNotGit {
		t.Errorf("outside git: %v", err)
	}
}

func TestWorktreeRemoveWithoutComposeFile(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	res, _ := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "plain", NoUp: true})
	if err := os.Remove(filepath.Join(res.Path, "compose.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "plain", Force: true}); err != nil {
		t.Fatal(err)
	}
}

type failingGit struct {
	gitdir.Git
	dirtyErr, removeErr error
}

func (f failingGit) Dirty(context.Context, string) (bool, error) { return false, f.dirtyErr }

func (f failingGit) RemoveWorktree(c context.Context, dir, path string, force bool) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.Git.RemoveWorktree(c, dir, path, force)
}

func TestWorktreeRemoveFailures(t *testing.T) {
	root := newRepo(t, "shop")
	boom := errors.New("boom")

	h := newHarness(t, webModel())
	res, _ := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "a", NoUp: true})
	h.app.Git = failingGit{dirtyErr: boom}
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "a"}); appErrCode(err) != CodeGitFailed {
		t.Errorf("dirty check: %v", err)
	}
	h.app.Git = failingGit{removeErr: boom}
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: "a"}); appErrCode(err) != CodeGitFailed {
		t.Errorf("git remove: %v", err)
	}
	h.app.Git = gitdir.Git{}
	h.compose.VersionErr = compose.ErrNotInstalled
	if _, err := h.app.WorktreeRemove(ctx, root, WorktreeRemoveOptions{Target: res.Path}); appErrCode(err) != CodeComposeMissing {
		t.Errorf("destroy: %v", err)
	}
}

type worktreeListFails struct{ gitdir.Git }

func (worktreeListFails) Worktrees(context.Context, string) ([]gitdir.Worktree, error) {
	return nil, errors.New("boom")
}

func TestWorktreeAddCopiesUntrackedFiles(t *testing.T) {
	root := newRepo(t, "shop")
	writeFile(t, filepath.Join(root, ".env.local"), "SECRET=1")
	writeFile(t, filepath.Join(root, "config", "a.json"), "{}")
	writeFile(t, filepath.Join(root, "config", "b.json"), "{}")
	writeFile(t, filepath.Join(root, "secrets", "nested", "key"), "k")
	// compose.yml is tracked, so the new worktree has it already: it must stay as git made it.
	m := webModel()
	m.XMutti = []byte(`{"worktree":{"copy":[".env.local","config/*.json","secrets","compose.yml","missing.txt"]}}`)
	h := newHarness(t, m)

	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "x", NoUp: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Copied, []string{".env.local", "config/a.json", "config/b.json", "secrets"}) {
		t.Fatalf("copied %v", res.Copied)
	}
	for _, f := range []string{".env.local", "config/a.json", "secrets/nested/key"} {
		if _, err := os.Stat(filepath.Join(res.Path, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(res.Path, "compose.yml")); string(b) != "services: {}\n" {
		t.Errorf("tracked file overwritten: %q", b)
	}
}

func TestCopyIntoFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	src := t.TempDir()

	t.Run("dangling symlink", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(src, "nowhere"), filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := copyInto(src, t.TempDir(), []string{"link"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("target parent is a file", func(t *testing.T) {
		writeFile(t, filepath.Join(src, "d", "f"), "x")
		dst := t.TempDir()
		writeFile(t, filepath.Join(dst, "d"), "a file")
		if _, err := copyInto(src, dst, []string{"d/f"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		writeFile(t, filepath.Join(src, "secret"), "x")
		if err := os.Chmod(filepath.Join(src, "secret"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := copyInto(src, t.TempDir(), []string{"secret"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("unwritable target", func(t *testing.T) {
		writeFile(t, filepath.Join(src, "ok"), "x")
		dst := t.TempDir()
		if err := os.Chmod(dst, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dst, 0o700) })
		if _, err := copyInto(src, dst, []string{"ok"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("unreadable dir", func(t *testing.T) {
		writeFile(t, filepath.Join(src, "dir", "sub", "f"), "x")
		if err := os.Chmod(filepath.Join(src, "dir", "sub"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "dir", "sub"), 0o700) })
		if _, err := copyInto(src, t.TempDir(), []string{"dir"}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestWorktreeAddTakesBackAFailedSetup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	root := newRepo(t, "shop")
	writeFile(t, filepath.Join(root, "secret"), "x")
	if err := os.Chmod(filepath.Join(root, "secret"), 0); err != nil {
		t.Fatal(err)
	}
	m := webModel()
	m.XMutti = []byte(`{"worktree":{"copy":["secret"]}}`)
	h := newHarness(t, m)
	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "x"})
	if err == nil || res != nil || !strings.Contains(err.Error(), "copy files") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "shop-x")); !os.IsNotExist(err) {
		t.Fatalf("worktree left: %v", err)
	}
	if b, _ := exec.Command("git", "-C", root, "branch", "--list", "x").Output(); len(b) != 0 {
		t.Fatalf("branch left: %s", b)
	}

	// Fixed, the same command works.
	if err := os.Chmod(filepath.Join(root, "secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "x", NoUp: true}); err != nil {
		t.Fatal(err)
	}

	// An existing branch is checked out, not made, so it stays; a failed take-back is reported.
	runGit(t, root, "branch", "kept")
	_ = os.Chmod(filepath.Join(root, "secret"), 0)
	h.app.Git = failingGit{removeErr: errors.New("boom")}
	_, err = h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "y", Branch: "kept"})
	if err == nil || !strings.Contains(err.Error(), "remove the half set up worktree") {
		t.Fatalf("failed take-back: %v", err)
	}
	if b, _ := exec.Command("git", "-C", root, "branch", "--list", "kept").Output(); len(b) == 0 {
		t.Fatal("an existing branch was deleted")
	}
}

func TestWorktreeAddDropsAHalfClonedInstance(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, cloneModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"RunContainer": engine.ErrUnavailable}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "copy", DataFrom: "main"}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
	h.eng.FailOn = nil
	if _, ok, _ := h.eng.InspectVolume(ctx, claimVolume("shop-copy")); ok {
		t.Error("claim of the half cloned instance left")
	}
	if _, ok, _ := h.eng.InspectVolume(ctx, "shop-copy_data"); ok {
		t.Error("half copied volume left")
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "copy", DataFrom: "main"}); err != nil {
		t.Fatalf("second try: %v", err)
	}
}

func TestCopyTreeKeepsSymlinks(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "out")
	writeFile(t, filepath.Join(src, "pkg", "index.js"), "x")
	writeFile(t, filepath.Join(src, "bin", "tool"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(src, "bin", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pkg/index.js", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../gone", filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "pkg/index.js" {
		t.Errorf("link: %q %v", l, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "link")); string(b) != "x" {
		t.Errorf("through the link: %q", b)
	}
	if fi, err := os.Stat(filepath.Join(dst, "bin", "tool")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("executable lost its mode: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "dangling")); err != nil {
		t.Errorf("dangling link not copied: %v", err)
	}
	if err := copyTree(src, dst); err == nil {
		t.Error("existing target: want error")
	}
	if err := copyTree(filepath.Join(src, "missing"), dst); err == nil {
		t.Error("missing source: want error")
	}
	if err := copyTree(src, filepath.Join(src, "pkg", "index.js", "x")); err == nil {
		t.Error("target below a file: want error")
	}
	// A file that cannot be read stops the copy.
	if err := os.Chmod(filepath.Join(src, "pkg", "index.js"), 0); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Error("unreadable file: want error")
	}
}

func TestReset(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := h.app.Reset(ctx, root, UpOptions{})
	if err != nil || !res.Bootstrapped {
		t.Fatalf("reset: %+v %v", res, err)
	}
	downs := h.compose.RunsWith("down")
	if len(downs) != 1 || !slices.Contains(downs[0], "--volumes") {
		t.Fatalf("downs %v", downs)
	}

	if _, err := h.app.Reset(ctx, t.TempDir(), UpOptions{}); err == nil {
		t.Error("no project: want error")
	}
	h.eng.FailOn = map[string]error{"RemoveVolume": engine.ErrUnavailable}
	if _, err := h.app.Reset(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("marker: %v", err)
	}
	h.eng.FailOn = nil
	h.compose.Exit = func(args []string) int { return map[string]int{"down": 1}[args[0]] }
	if _, err := h.app.Reset(ctx, root, UpOptions{}); appErrCode(err) != CodeComposeFailed {
		t.Errorf("down: %v", err)
	}
}
