package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/identity"
)

var ctx = context.Background()

func TestResolveMainWorktree(t *testing.T) {
	root := newRepo(t, "Shop")
	h := newHarness(t, webModel())
	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "shop" || inst.Project != "shop" || inst.Worktree != "shop" || inst.Hostname != "shop.localhost" || inst.ID != "" {
		t.Fatalf("unexpected %+v", inst)
	}
	if inst.IdentityPath != filepath.Join(root, ".git", "mutti", "identity.json") {
		t.Errorf("identity path %s", inst.IdentityPath)
	}
	spec := h.compose.Specs[0]
	if spec.Project != "shop" || spec.Dir != root || !slices.Equal(spec.Files, []string{filepath.Join(root, "compose.yml")}) {
		t.Errorf("config spec %+v", spec)
	}
	if !slices.Contains(spec.Env, "MUTTI_HOST_PATH="+root) || !slices.Contains(spec.Env, "MUTTI_UID=1000") || !slices.Contains(spec.Env, "MUTTI_GID=1001") {
		t.Errorf("env %v", spec.Env)
	}
}

func TestResolveLinkedWorktreeDropsRepoPrefix(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature-x")
	h := newHarness(t, webModel())
	inst, err := h.app.Resolve(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "shop-feature-x" || inst.Worktree != "feature-x" || inst.Hostname != "feature-x.shop.localhost" || inst.Root != wt {
		t.Fatalf("unexpected %+v", inst)
	}
}

func TestResolveLinkedWorktreeKeepsNameWithXMuttiName(t *testing.T) {
	root := newRepo(t, "checkout")
	wt := addWorktree(t, root, "checkout-fix")
	m := webModel()
	m.XMutti = []byte(`{"name":"shop"}`)
	h := newHarness(t, m)
	inst, err := h.app.Resolve(ctx, wt)
	if err != nil || inst.Name != "shop-fix" || inst.Hostname != "fix.shop.localhost" {
		t.Fatalf("inst=%+v err=%v", inst, err)
	}
}

func TestResolveOutsideGit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	writeFile(t, filepath.Join(dir, "compose.yml"), "")
	h := newHarness(t, webModel())
	inst, err := h.app.Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "plain" || inst.GitDir != "" || inst.IdentityPath != filepath.Join(dir, ".mutti", "local", "identity.json") {
		t.Fatalf("unexpected %+v", inst)
	}
}

func TestResolveWithoutGitBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	writeFile(t, filepath.Join(dir, "compose.yml"), "")
	h := newHarness(t, webModel())
	h.app.Git = gitdir.Git{Path: filepath.Join(t.TempDir(), "no-git")}
	if inst, err := h.app.Resolve(ctx, dir); err != nil || inst.GitDir != "" {
		t.Fatalf("inst=%+v err=%v", inst, err)
	}
}

type brokenGit struct{ gitdir.Git }

func (brokenGit) Find(context.Context, string) (gitdir.Info, bool, error) {
	return gitdir.Info{}, false, errors.New("git exploded")
}

func TestResolveGitError(t *testing.T) {
	h := newHarness(t, webModel())
	h.app.Git = brokenGit{}
	if _, err := h.app.Resolve(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "exploded") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveNoComposeFile(t *testing.T) {
	h := newHarness(t, webModel())
	_, err := h.app.Resolve(ctx, t.TempDir())
	if appErrCode(err) != CodeNoComposeFile {
		t.Fatalf("got %v", err)
	}
}

func TestResolveUsesStoredIdentity(t *testing.T) {
	root := newRepo(t, "shop")
	if err := identity.Save(filepath.Join(root, ".git", "mutti", "identity.json"), identity.Identity{Name: "stored", ID: "abc"}); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, webModel())
	inst, err := h.app.Resolve(ctx, root)
	if err != nil || inst.Worktree != "stored" || inst.ID != "abc" || inst.Name != "shop-stored" {
		t.Fatalf("inst=%+v err=%v", inst, err)
	}
}

func TestResolveCorruptIdentity(t *testing.T) {
	root := newRepo(t, "shop")
	writeFile(t, filepath.Join(root, ".git", "mutti", "identity.json"), "{")
	h := newHarness(t, webModel())
	if _, err := h.app.Resolve(ctx, root); err == nil {
		t.Fatal("want error")
	}
}

func TestResolveProjectNameFromXMutti(t *testing.T) {
	root := newRepo(t, "checkout")
	m := webModel()
	m.XMutti = []byte(`{"name":"shop"}`)
	h := newHarness(t, m)
	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Project != "shop" || inst.Name != "shop" || len(h.compose.Specs) != 2 || h.compose.Specs[1].Project != "shop" {
		t.Fatalf("inst=%+v specs=%+v", inst, h.compose.Specs)
	}
}

func TestResolveUnusableDirectoryNames(t *testing.T) {
	root := newRepo(t, "___")

	h := newHarness(t, webModel())
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeInvalidName {
		t.Fatalf("want invalid name without x-mutti.name, got %v", err)
	}

	m := webModel()
	m.XMutti = []byte(`{"name":"shop"}`)
	h = newHarness(t, m)
	inst, err := h.app.Resolve(ctx, root)
	if err != nil || inst.Name != "shop" {
		t.Fatalf("x-mutti.name must rescue it: inst=%+v err=%v", inst, err)
	}
}

func TestResolveConfigErrors(t *testing.T) {
	root := newRepo(t, "shop")

	h := newHarness(t, nil)
	h.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) { return nil, errors.New("yaml: broken") }
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("compose error: %v", err)
	}

	m := webModel()
	m.XMutti = []byte(`{"bogus":1}`)
	h = newHarness(t, m)
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("x-mutti error: %v", err)
	}

	// The second load (after x-mutti.name changed the project) can fail too.
	calls := 0
	h = newHarness(t, nil)
	h.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("second load")
		}
		m := webModel()
		m.XMutti = []byte(`{"name":"other"}`)
		return m, nil
	}
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("second load: %v", err)
	}
}

func TestDefaultWorktree(t *testing.T) {
	tests := []struct{ dir, project, want string }{
		{"shop", "shop", "shop"},
		{"shop-feature", "shop", "feature"},
		{"feature", "shop", "feature"},
		{"---", "shop", "shop"},
		{"Shop-Fix_1", "shop", "fix-1"},
	}
	for _, tt := range tests {
		if got := defaultWorktree(tt.dir, tt.project); got != tt.want {
			t.Errorf("defaultWorktree(%q, %q) = %q, want %q", tt.dir, tt.project, got, tt.want)
		}
	}
}

func TestErrorFormatting(t *testing.T) {
	inner := errors.New("inner")
	e := &Error{Code: "c", Message: "msg", Err: inner}
	if e.Error() != "msg: inner" || !errors.Is(e, inner) {
		t.Errorf("got %q", e.Error())
	}
	if (&Error{Message: "msg"}).Error() != "msg" {
		t.Error("without cause")
	}
	other := errors.New("other")
	if !errors.Is(engineErr(other), other) {
		t.Error("engineErr must pass other errors through")
	}
}

func TestCheckCompose(t *testing.T) {
	h := newHarness(t, webModel())

	h.compose.VersionErr = compose.ErrNotInstalled
	if err := h.app.checkCompose(ctx); appErrCode(err) != CodeComposeMissing {
		t.Errorf("missing: %v", err)
	}
	h.compose.VersionErr = errors.New("weird")
	if err := h.app.checkCompose(ctx); err == nil || appErrCode(err) == CodeComposeMissing {
		t.Errorf("other error: %v", err)
	}
	h.compose.VersionErr = nil
	h.compose.Ver = compose.MustParseVersion("2.24.3")
	if err := h.app.checkCompose(ctx); appErrCode(err) != CodeComposeTooOld {
		t.Errorf("too old: %v", err)
	}
	h.compose.Ver = compose.MinVersion()
	if err := h.app.checkCompose(ctx); err != nil {
		t.Errorf("min version: %v", err)
	}
}

func TestEnvContainsEverything(t *testing.T) {
	h := newHarness(t, webModel())
	inst := &Instance{Name: "shop-x", Project: "shop", Worktree: "x", Root: "/w", HostPath: "/w", Hostname: "x.shop.localhost"}
	want := []string{
		"MUTTI_INSTANCE=shop-x", "MUTTI_PROJECT=shop", "MUTTI_WORKTREE=x", "MUTTI_HOST_PATH=/w",
		"MUTTI_HOSTNAME=x.shop.localhost", "MUTTI_UID=1000", "MUTTI_GID=1001", "MUTTI_DOCKER_SOCKET=/var/run/docker.sock",
	}
	if got := h.app.Env(inst); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	h.app.Host.DockerSocket = ""
	if got := h.app.Env(inst); slices.ContainsFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "MUTTI_DOCKER_SOCKET=") }) {
		t.Fatalf("unknown socket: %v", got)
	}
	h.app.Host.DockerGID = 963
	if got := h.app.Env(inst); got[len(got)-1] != "MUTTI_DOCKER_GID=963" {
		t.Fatalf("with docker gid: %v", got)
	}
	inst.CommonDir = "/repo/.git"
	if got := h.app.Env(inst); got[len(got)-1] != "MUTTI_GIT_COMMON_DIR=/repo/.git" {
		t.Fatalf("in git: %v", got)
	}
}

func TestUserSpecResolvesOverlayPaths(t *testing.T) {
	h := newHarness(t, webModel())
	inst := &Instance{Name: "p", Files: projectFiles("/w", "/w/compose.yml")}
	inst.Config.Overlays = map[string][]string{"e2e": {"compose.e2e.yml", "/abs/ci.yml"}}
	got := h.app.userSpec(inst, []string{"e2e"}, []string{"tools"})
	if !slices.Equal(got.Files, []string{"/w/compose.yml", "/w/compose.e2e.yml", "/abs/ci.yml"}) || !slices.Equal(got.Profiles, []string{"tools"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveMissingDir(t *testing.T) {
	h := newHarness(t, webModel())
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	for _, dir := range []string{"/no/such/dir", file} {
		if _, err := h.app.Resolve(ctx, dir); appErrCode(err) != CodeNoSuchDir {
			t.Errorf("%s: %v", dir, err)
		}
	}
}
