package app

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/identity"
)

func TestUpClaimsAndSavesIdentity(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	id, ok, err := identity.Load(filepath.Join(root, ".git", "mutti", "identity.json"))
	if err != nil || !ok || id.Name != "shop" {
		t.Fatalf("identity %+v ok=%v err=%v", id, ok, err)
	}
	l := claimLabels(t, h.eng, "shop")
	if l[engine.LabelID] != id.ID || l[engine.LabelPath] != root || l[engine.LabelGitDir] != filepath.Join(root, ".git") ||
		l[engine.LabelRole] != RoleClaim || l[engine.LabelSchema] != engine.SchemaVersion || l[engine.LabelProject] != "shop" {
		t.Fatalf("claim labels %v", l)
	}
}

func TestSecondCloneWithSameNameIsRefused(t *testing.T) {
	a := newRepo(t, "shop")
	b := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, a, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := h.app.Up(ctx, b, UpOptions{})
	if appErrCode(err) != CodeNameTaken {
		t.Fatalf("got %v", err)
	}
	if _, ok, _ := identity.Load(filepath.Join(b, ".git", "mutti", "identity.json")); ok {
		t.Fatal("a refused claim must not write an identity")
	}

	// Renaming the second clone resolves the conflict.
	if _, err := h.app.Rename(ctx, b, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, b, UpOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleClaimOfDeletedWorktree(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	first := newRepoIn(t, base, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, first, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	again := newRepoIn(t, base, "shop")
	if _, err := h.app.Up(ctx, again, UpOptions{}); appErrCode(err) != CodeStaleClaim {
		t.Fatalf("got %v", err)
	}
	if _, err := h.app.Tidy(ctx, TidyOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, again, UpOptions{}); err != nil {
		t.Fatalf("after tidy: %v", err)
	}
}

func TestMovedLinkedWorktreeKeepsInstance(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	before := claimLabels(t, h.eng, "shop-feature")

	moved := filepath.Join(filepath.Dir(root), "elsewhere")
	runGit(t, root, "worktree", "move", wt, moved)
	res, err := h.app.Up(ctx, moved, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after := claimLabels(t, h.eng, "shop-feature")
	if res.Instance.Name != "shop-feature" || after[engine.LabelID] != before[engine.LabelID] || after[engine.LabelPath] != moved {
		t.Fatalf("before %v after %v", before, after)
	}
}

func TestMovedMainRepoKeepsInstance(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(root), "renamed-dir")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	// The identity and the project name were fixed at first up; the directory name no longer matters
	// for the worktree part, but the project is derived from the main worktree's directory.
	res, err := h.app.Up(ctx, moved, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance.Worktree != "shop" {
		t.Fatalf("identity lost: %+v", res.Instance)
	}
}

func TestCopiedWorktreeIsRefused(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "shop-feature")
	if out, err := exec.Command("cp", "-r", wt, cp).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	if _, err := h.app.Up(ctx, cp, UpOptions{}); appErrCode(err) != CodeCopiedWorktree {
		t.Fatalf("got %v", err)
	}
}

func TestClaimFailures(t *testing.T) {
	root := newRepo(t, "shop")
	boom := errors.New("boom")

	t.Run("daemon unavailable", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"CreateVolume": engine.ErrUnavailable}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("lock dir unusable", func(t *testing.T) {
		h := newHarness(t, webModel())
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, "")
		h.app.Dirs.State = file
		if _, err := h.app.Up(ctx, root, UpOptions{}); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("identity not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permissions")
		}
		repo := newRepo(t, "shop")
		dir := filepath.Join(repo, ".git", "mutti")
		if err := os.MkdirAll(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		h := newHarness(t, webModel())
		if _, err := h.app.Up(ctx, repo, UpOptions{}); err == nil {
			t.Fatal("want error")
		}
	})

	for _, method := range []string{"RemoveVolume", "CreateVolume"} {
		t.Run("refresh fails at "+method, func(t *testing.T) {
			repo := newRepo(t, "shop")
			wt := addWorktree(t, repo, "shop-feature")
			h := newHarness(t, webModel())
			if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(filepath.Dir(repo), "moved")
			runGit(t, repo, "worktree", "move", wt, moved)
			creates := 0
			h.eng.OnCall = func(m string) error {
				if m == "CreateVolume" {
					creates++
				}
				if m == method && (method == "RemoveVolume" || creates == 2) {
					return boom
				}
				return nil
			}
			if _, err := h.app.Up(ctx, moved, UpOptions{}); !errors.Is(err, boom) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestOwnerRootWithoutGit(t *testing.T) {
	h := newHarness(t, webModel())
	dir := filepath.Join(t.TempDir(), "plain")
	writeFile(t, filepath.Join(dir, "compose.yml"), "")
	if _, err := h.app.Up(ctx, dir, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	vol, _, _ := h.eng.InspectVolume(ctx, claimVolume("plain"))
	if root, owner := h.app.ownerRoot(ctx, vol); owner != ownerAlive || root != dir {
		t.Fatalf("root=%s owner=%v", root, owner)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, owner := h.app.ownerRoot(ctx, vol); owner != ownerGone {
		t.Fatalf("deleted dir: %v", owner)
	}
}

func TestOwnerRootOnlyCallsGoneWhatIsGone(t *testing.T) {
	h := newHarness(t, webModel())
	base := t.TempDir()
	at := func(labels map[string]string) engine.Volume {
		l := map[string]string{LabelOrigin: "host", engine.LabelID: "id1"}
		maps.Copy(l, labels)
		return engine.Volume{Labels: l}
	}

	// A gitdir file that cannot be read.
	admin := filepath.Join(base, "worktrees", "x")
	if err := os.MkdirAll(filepath.Join(admin, "gitdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A path this user may not look into.
	locked := filepath.Join(base, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	// An identity that cannot be read.
	broken := filepath.Join(base, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".mutti", "local", "identity.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, vol := range map[string]engine.Volume{
		"made in a container":  at(map[string]string{LabelOrigin: "container:abc", engine.LabelPath: "/gone"}),
		"made before origins":  {Labels: map[string]string{engine.LabelPath: "/gone"}},
		"unreadable admin dir": at(map[string]string{engine.LabelGitDir: admin, engine.LabelPath: "/gone"}),
		"unreadable worktree":  at(map[string]string{engine.LabelPath: filepath.Join(locked, "wt")}),
		"unreadable git dir":   at(map[string]string{engine.LabelGitDir: filepath.Join(locked, "wt", ".git"), engine.LabelPath: "/gone"}),
		"unreadable identity":  at(map[string]string{engine.LabelPath: broken}),
	} {
		if _, owner := h.app.ownerRoot(ctx, vol); owner != ownerUnknown {
			t.Errorf("%s: %v, want unknown", name, owner)
		}
	}
	if _, owner := h.app.ownerRoot(ctx, at(map[string]string{engine.LabelGitDir: filepath.Join(base, "nope", ".git"), engine.LabelPath: "/gone"})); owner != ownerGone {
		t.Errorf("missing git dir: %v", owner)
	}

	// A repository made with --separate-git-dir: its git dir has no gitdir file; the path label
	// leads to the worktree.
	sep := filepath.Join(base, "sep")
	runGit(t, base, "init", "-q", "--separate-git-dir", filepath.Join(base, "sep.git"), sep)
	writeFile(t, filepath.Join(sep, "compose.yml"), "services: {}\n")
	if _, err := h.app.Up(ctx, sep, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	vol, _, _ := h.eng.InspectVolume(ctx, claimVolume("sep"))
	if root, owner := h.app.ownerRoot(ctx, vol); owner != ownerAlive || root != sep {
		t.Errorf("separate git dir: %s %v", root, owner)
	}
}

func TestClaimOfAnUncheckableOwnerIsTaken(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.eng.CreateVolume(ctx, claimVolume("shop"), map[string]string{LabelOrigin: "container:abc", engine.LabelID: "other", engine.LabelPath: "/app"}); err != nil {
		t.Fatal(err)
	}
	_, err := h.app.Up(ctx, root, UpOptions{})
	if appErrCode(err) != CodeNameTaken || !strings.Contains(err.Error(), "cannot check from here") {
		t.Fatal(err)
	}
}

func TestTidyKeepsWhatItCannotCheck(t *testing.T) {
	h := newHarness(t, webModel())
	h.app.Host.ContainerID = "abc" // the claims below come from the host
	if _, err := h.eng.CreateVolume(ctx, claimVolume("ghost"), map[string]string{
		LabelOrigin: "host", engine.LabelSchema: engine.SchemaVersion, engine.LabelRole: RoleClaim,
		engine.LabelInstance: "ghost", engine.LabelPath: "/not/here",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := h.app.Tidy(ctx, TidyOptions{Apply: true})
	if err != nil || len(res.Removed) != 0 {
		t.Fatalf("tidy: %+v %v", res, err)
	}
	if _, ok, _ := h.eng.InspectVolume(ctx, claimVolume("ghost")); !ok {
		t.Fatal("claim removed")
	}
}

func TestOtherWorktreesCannotTouchAClaimedInstance(t *testing.T) {
	a := newRepo(t, "shop")
	b := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, a, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"stop":    func() error { _, err := h.app.Stop(ctx, b, Selection{}); return err },
		"down":    func() error { _, err := h.app.Down(ctx, b, Selection{}); return err },
		"destroy": func() error { _, err := h.app.Destroy(ctx, b); return err },
		"exec":    func() error { _, err := h.app.Exec(ctx, b, "web", []string{"id"}, ExecOptions{}); return err },
		"compose": func() error {
			_, err := h.app.ComposePassthrough(ctx, b, Selection{}, []string{"down", "-v"})
			return err
		},
	} {
		if err := run(); appErrCode(err) != CodeNameTaken {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s := summaryOf(t, h, "shop"); s.Status != StatusRunning {
		t.Fatalf("the first clone's instance was touched: %+v", s)
	}

	h.eng.FailOn = map[string]error{"InspectVolume": engine.ErrUnavailable}
	if _, err := h.app.Stop(ctx, a, Selection{}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
}

func TestFailedIdentitySaveReleasesTheClaim(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	// The identity's directory cannot be written: saving fails after the claim was made.
	dir := filepath.Join(root, ".git", "mutti")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := h.app.Up(ctx, root, UpOptions{}); err == nil || !strings.Contains(err.Error(), "save identity") {
		t.Fatal(err)
	}
	if _, ok, _ := h.eng.InspectVolume(ctx, claimVolume("shop")); ok {
		t.Fatal("the claim stayed and blocks this worktree")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatalf("after fixing it: %v", err)
	}
}

func TestLongWorktreeDirectoryNames(t *testing.T) {
	root := newRepo(t, "shop")
	long := "shop-" + strings.Repeat("feature-", 10) + "x"
	wt := addWorktree(t, root, long)
	h := newHarness(t, webModel())
	res, err := h.app.Up(ctx, wt, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if w := res.Instance.Worktree; !identity.Valid(w) || len(w) > identity.MaxLen {
		t.Fatalf("worktree name %q", w)
	}
	if !strings.HasSuffix(res.Instance.Hostname, ".shop.localhost") {
		t.Errorf("hostname %q", res.Instance.Hostname)
	}
}

func TestConcurrentFirstUpsShareTheClaim(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	// Both resolved before either claimed: neither knows an identity yet.
	first, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.claim(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := h.app.claim(ctx, second); err != nil {
		t.Fatalf("second claim of the same worktree: %v", err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("ids %q %q", first.ID, second.ID)
	}
}
