package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/compose/composetest"
	"github.com/l0wskilled/mutti/internal/engine"
)

func hookModel() *compose.Model {
	m := webModel()
	m.XMutti = []byte(`{"hooks":{
		"bootstrap":[{"service":"worker","run":"seed"}],
		"post-up":[{"service":"web","run":"warm","user":"service"}],
		"pre-down":[{"service":"web","run":"flush","user":"root"}]}}`)
	return m
}

func TestUpRunsComposeAndHooks(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel())

	res, err := h.app.Up(ctx, root, UpOptions{Build: true, Selection: Selection{Profiles: []string{"tools"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Bootstrapped || res.Instance.Name != "shop" {
		t.Fatalf("result %+v", res)
	}

	override := filepath.Join(h.app.Dirs.InstanceDir("shop"), "override.yml")
	content, err := os.ReadFile(override)
	if err != nil || !strings.Contains(string(content), `"web":`) {
		t.Fatalf("override: %q %v", content, err)
	}
	if fi, _ := os.Stat(override); fi.Mode().Perm() != 0o600 {
		t.Errorf("override mode %v", fi.Mode())
	}

	env := []string{
		"--env", "MUTTI_INSTANCE=shop", "--env", "MUTTI_PROJECT=shop", "--env", "MUTTI_WORKTREE=shop",
		"--env", "MUTTI_HOST_PATH=" + root, "--env", "MUTTI_HOSTNAME=shop.localhost", "--env", "MUTTI_UID=1000", "--env", "MUTTI_GID=1001",
		"--env", "MUTTI_DOCKER_SOCKET=/var/run/docker.sock", "--env", "MUTTI_GIT_COMMON_DIR=" + filepath.Join(root, ".git"),
	}
	want := [][]string{
		{"up", "--detach", "--wait", "--remove-orphans", "--build"},
		append(append([]string{"exec", "-T", "--user", "1000:1001", "--env", "HOME=/tmp"}, env...), "worker", "sh", "-c", "seed"),
		append(append([]string{"exec", "-T"}, env...), "web", "sh", "-c", "warm"),
	}
	if diff := cmp.Diff(want, h.compose.Runs); diff != "" {
		t.Fatalf("runs (-want +got):\n%s", diff)
	}
	upSpec := h.compose.Specs[len(h.compose.Specs)-3]
	if !slices.Equal(upSpec.Files, []string{filepath.Join(root, "compose.yml"), override}) || !slices.Equal(upSpec.Profiles, []string{"tools"}) {
		t.Errorf("up spec %+v", upSpec)
	}

	// Second up: no bootstrap, post-up again.
	h.compose.Runs = nil
	res, err = h.app.Up(ctx, root, UpOptions{})
	if err != nil || res.Bootstrapped {
		t.Fatalf("second up: %+v %v", res, err)
	}
	if len(h.compose.RunsWith("exec")) != 1 {
		t.Fatalf("second up runs %v", h.compose.Runs)
	}
}

func TestUpWritesRoutesAndIngressIntoOverride(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(h.app.Dirs.InstanceDir("shop"), "override.yml"))
	for _, want := range []string{
		`"mutti.instance": "shop"`, `"mutti.hostname": "shop.localhost"`,
		`"mutti.route.hosts": "shop.localhost"`, `"mutti.route.port": "80"`,
		"mutti-ingress: {}", `name: "mutti-shop"`, "external: true",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("override lacks %s:\n%s", want, content)
		}
	}
}

func TestUpWithOverlay(t *testing.T) {
	root := newRepo(t, "shop")
	base := webModel()
	base.XMutti = []byte(`{"overlays":{"e2e":["compose.e2e.yml"]}}`)
	withE2E := webModel()
	withE2E.Services["runner"] = compose.Service{Ports: []compose.Port{{Target: 9323}}}
	withE2E.XMutti = base.XMutti

	h := newHarness(t, base)
	h.compose.ConfigFn = func(s compose.Spec) (*compose.Model, error) {
		if len(s.Files) > 1 {
			return withE2E, nil
		}
		return base, nil
	}
	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Overlays: []string{"e2e"}}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(h.app.Dirs.InstanceDir("shop"), "override-e2e.yml"))
	if err != nil || !strings.Contains(string(content), `"runner":`) {
		t.Fatalf("overlay override: %q %v", content, err)
	}

	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Overlays: []string{"nope"}}}); appErrCode(err) != CodeUnknownOverlay {
		t.Errorf("unknown overlay: %v", err)
	}

	h.compose.ConfigFn = func(s compose.Spec) (*compose.Model, error) {
		if len(s.Files) > 1 {
			return nil, errors.New("broken overlay")
		}
		return base, nil
	}
	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Overlays: []string{"e2e"}}}); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("broken overlay: %v", err)
	}
}

func TestUpFailures(t *testing.T) {
	root := newRepo(t, "shop")
	boom := errors.New("boom")

	t.Run("compose too old", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.Ver = compose.MustParseVersion("2.20.0")
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeComposeTooOld {
			t.Fatal(err)
		}
	})
	t.Run("resolve fails", func(t *testing.T) {
		h := newHarness(t, webModel())
		if _, err := h.app.Up(ctx, t.TempDir(), UpOptions{}); appErrCode(err) != CodeNoComposeFile {
			t.Fatal(err)
		}
	})
	t.Run("compose up fails", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.Exit = func(args []string) int { return map[string]int{"up": 1}[args[0]] }
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeComposeFailed {
			t.Fatal(err)
		}
	})
	t.Run("compose cannot start", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.RunErr = boom
		if _, err := h.app.Up(ctx, root, UpOptions{}); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
	t.Run("bootstrap hook fails, no marker", func(t *testing.T) {
		h := newHarness(t, hookModel())
		h.compose.Exit = func(args []string) int {
			if slices.Contains(args, "seed") {
				return 3
			}
			return 0
		}
		_, err := h.app.Up(ctx, root, UpOptions{})
		if appErrCode(err) != CodeHookFailed || !strings.Contains(err.Error(), "bootstrap hook 1 (worker: seed) failed with exit code 3") {
			t.Fatal(err)
		}
		if _, ok, _ := h.eng.InspectVolume(ctx, bootstrapVolume("shop")); ok {
			t.Fatal("marker written after a failed bootstrap")
		}
	})
	t.Run("post-up hook fails", func(t *testing.T) {
		h := newHarness(t, hookModel())
		h.compose.Exit = func(args []string) int {
			if slices.Contains(args, "warm") {
				return 1
			}
			return 0
		}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeHookFailed {
			t.Fatal(err)
		}
	})
	t.Run("hook cannot start", func(t *testing.T) {
		h := newHarness(t, hookModel())
		h.app.Compose = &startFailsAfter{Fake: h.compose, ok: 1, err: boom}
		if _, err := h.app.Up(ctx, root, UpOptions{}); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
	t.Run("marker inspect fails", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"InspectVolume": engine.ErrUnavailable}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("marker create fails", func(t *testing.T) {
		h := newHarness(t, webModel())
		creates := 0
		h.eng.OnCall = func(m string) error {
			if m == "CreateVolume" {
				// 1: claim, 2: the project volume the fake up creates, 3: bootstrap marker
				if creates++; creates == 3 {
					return boom
				}
			}
			return nil
		}
		if _, err := h.app.Up(ctx, root, UpOptions{}); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
	t.Run("user lookup fails", func(t *testing.T) {
		h := newHarness(t, hookModel())
		h.eng.FailOn = map[string]error{"Info": engine.ErrUnavailable}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
}

// startFailsAfter lets the first ok Runs succeed and fails every later one to start.
type startFailsAfter struct {
	*composetest.Fake
	ok  int
	err error
}

func (s *startFailsAfter) Run(c context.Context, sp compose.Spec, io compose.IO, args ...string) (int, error) {
	if s.ok == 0 {
		return 0, s.err
	}
	s.ok--
	return s.Fake.Run(c, sp, io, args...)
}

func TestPrepareFileErrors(t *testing.T) {
	root := newRepo(t, "shop")

	t.Run("state dir not creatable", func(t *testing.T) {
		h := newHarness(t, webModel())
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, "")
		h.app.Dirs.State = filepath.Join(file, "state")
		inst, err := h.app.Resolve(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.prepare(ctx, inst, Selection{}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("override not writable", func(t *testing.T) {
		h := newHarness(t, webModel())
		inst, err := h.app.Resolve(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(h.app.Dirs.InstanceDir("shop"), "override.yml"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.prepare(ctx, inst, Selection{}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestStopDownDestroy(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.app.Stop(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, h, "shop"); s.Status != StatusStopped {
		t.Fatalf("after stop: %+v", s)
	}

	// pre-down needs a running service: skipped after stop, run after up.
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil || !strings.Contains(h.stderr.String(), "skipping pre-down hook 1: web is not running") {
		t.Fatalf("down after stop: %v %q", err, h.stderr.String())
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.compose.Runs = nil
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if r := h.compose.Runs; len(r) != 2 || !slices.Contains(r[0], "flush") || !slices.Equal(r[1], []string{"down", "--remove-orphans"}) {
		t.Fatalf("down runs %v", r)
	}
	if s := summaryOf(t, h, "shop"); s.Status != StatusDown {
		t.Fatalf("after down: %+v", s)
	}
	if vs, _ := h.eng.ListVolumes(ctx, engine.LabelComposeProject+"=shop"); len(vs) != 1 {
		t.Fatalf("down must keep volumes: %v", vs)
	}

	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Destroy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if vs, _ := h.eng.ListVolumes(ctx); len(vs) != 0 {
		t.Fatalf("destroy left volumes: %v", vs)
	}
	if _, err := os.Stat(h.app.Dirs.InstanceDir("shop")); !os.IsNotExist(err) {
		t.Fatal("instance dir left")
	}
	if l, _ := h.app.List(ctx); len(l) != 0 {
		t.Fatalf("still listed: %v", l)
	}
}

func TestPreDownFailureDoesNotBlock(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.compose.Exit = func(args []string) int {
		if slices.Contains(args, "flush") {
			return 1
		}
		return 0
	}
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "pre-down hook 1 (web: flush) failed with exit code 1 (continuing)") {
		t.Fatalf("stderr %q", h.stderr.String())
	}
}

func TestStopDownDestroyFailures(t *testing.T) {
	root := newRepo(t, "shop")
	cmds := map[string]func(h *harness) error{
		"stop":    func(h *harness) error { _, err := h.app.Stop(ctx, root, Selection{}); return err },
		"down":    func(h *harness) error { _, err := h.app.Down(ctx, root, Selection{}); return err },
		"destroy": func(h *harness) error { _, err := h.app.Destroy(ctx, root); return err },
	}
	for name, run := range cmds {
		t.Run(name+" compose missing", func(t *testing.T) {
			h := newHarness(t, webModel())
			h.compose.VersionErr = compose.ErrNotInstalled
			if err := run(h); appErrCode(err) != CodeComposeMissing {
				t.Fatal(err)
			}
		})
		t.Run(name+" no project", func(t *testing.T) {
			h := newHarness(t, webModel())
			if err := run(&harness{app: withDir(h.app), compose: h.compose}); err == nil {
				t.Fatal("want error")
			}
		})
		t.Run(name+" compose fails", func(t *testing.T) {
			h := newHarness(t, webModel())
			h.compose.Exit = func(args []string) int {
				if args[0] == "exec" {
					return 0
				}
				return 1
			}
			if err := run(h); appErrCode(err) != CodeComposeFailed {
				t.Fatal(err)
			}
		})
	}
}

// withDir returns a copy of a whose Compose model is unreadable, so Resolve fails.
func withDir(a *App) *App {
	c := *a
	fc := *a.Compose.(*composetest.Fake)
	fc.ConfigFn = func(compose.Spec) (*compose.Model, error) { return nil, errors.New("unreadable") }
	c.Compose = &fc
	return &c
}

func TestPurgeFailures(t *testing.T) {
	for _, method := range []string{"ListContainers", "RemoveContainer", "ListNetworks", "RemoveNetwork", "ListVolumes", "RemoveVolume"} {
		t.Run(method, func(t *testing.T) {
			root := newRepo(t, "shop")
			h := newHarness(t, webModel())
			if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
			if err := h.app.purge(ctx, "shop"); appErrCode(err) != CodeDockerUnavailable {
				t.Fatal(err)
			}
		})
	}
}

func TestExec(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	h.compose.Exit = func(args []string) int {
		if args[0] == "exec" {
			return 42
		}
		return 0
	}

	code, err := h.app.Exec(ctx, root, "web", []string{"php", "-v"}, ExecOptions{})
	if err != nil || code != 42 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	got := h.compose.Runs[len(h.compose.Runs)-1]
	if !slices.Equal(got[:6], []string{"exec", "-T", "--user", "1000:1001", "--env", "HOME=/tmp"}) || !slices.Equal(got[len(got)-3:], []string{"web", "php", "-v"}) {
		t.Fatalf("args %v", got)
	}

	if _, err := h.app.Exec(ctx, root, "web", []string{"ls"}, ExecOptions{Workdir: "/app", Env: []string{"A=1", "B=2"}}); err != nil {
		t.Fatal(err)
	}
	got = h.compose.Runs[len(h.compose.Runs)-1]
	if !slices.Equal(got[len(got)-8:], []string{"--workdir", "/app", "--env", "A=1", "--env", "B=2", "web", "ls"}) {
		t.Fatalf("workdir/env args %v", got)
	}

	h.app.Host.StdinTTY = true
	if _, err := h.app.Exec(ctx, root, "web", []string{"sh"}, ExecOptions{User: "root"}); err != nil {
		t.Fatal(err)
	}
	got = h.compose.Runs[len(h.compose.Runs)-1]
	if !slices.Equal(got[:3], []string{"exec", "--user", "root"}) {
		t.Fatalf("tty args %v", got)
	}

	h.eng.FailOn = map[string]error{"Info": engine.ErrUnavailable}
	if _, err := h.app.Exec(ctx, root, "web", []string{"sh"}, ExecOptions{}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatalf("info error: %v", err)
	}
	if _, err := h.app.Exec(ctx, t.TempDir(), "web", []string{"sh"}, ExecOptions{}); err == nil {
		t.Fatal("want resolve error")
	}
}

func TestExecSkipsUserMappingWhereDockerMapsItself(t *testing.T) {
	root := newRepo(t, "shop")
	for name, info := range map[string]engine.Info{
		"rootless":       {Rootless: true, ServerVersion: "29", Host: "unix:///run/user/1000/docker.sock"},
		"docker desktop": {OS: "Docker Desktop", ServerVersion: "29", Host: "unix:///x"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, webModel())
			h.eng.SetInfo(info)
			if _, err := h.app.Exec(ctx, root, "web", []string{"id"}, ExecOptions{}); err != nil {
				t.Fatal(err)
			}
			if got := h.compose.Runs[len(h.compose.Runs)-1]; slices.Contains(got, "--user") {
				t.Fatalf("mapped anyway: %v", got)
			}
		})
	}
}

func TestComposePassthrough(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.ComposePassthrough(ctx, root, Selection{}, []string{"ps", "-a"}); err != nil {
		t.Fatal(err)
	}
	if got := h.compose.Runs[len(h.compose.Runs)-1]; !slices.Equal(got, []string{"ps", "-a"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := h.app.ComposePassthrough(ctx, t.TempDir(), Selection{}, []string{"ps"}); err == nil {
		t.Fatal("want error")
	}
}

func summaryOf(t *testing.T, h *harness, name string) Summary {
	t.Helper()
	list, err := h.app.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s not listed in %+v", name, list)
	return Summary{}
}

func TestCommandsUseTheLastSelection(t *testing.T) {
	root := newRepo(t, "shop")
	base := webModel()
	base.XMutti = []byte(`{"overlays":{"e2e":["compose.e2e.yml"]}}`)
	h := newHarness(t, base)
	overlay := filepath.Join(root, "compose.e2e.yml")
	writeFile(t, overlay, "services: {}\n")
	lastFiles := func() []string { return h.compose.Specs[len(h.compose.Specs)-1].Files }

	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Overlays: []string{"e2e"}, Profiles: []string{"tools"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Exec(ctx, root, "web", []string{"true"}, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if spec := h.compose.Specs[len(h.compose.Specs)-1]; !slices.Contains(spec.Files, overlay) || !slices.Equal(spec.Profiles, []string{"tools"}) {
		t.Errorf("exec after an up with overlay: %+v", spec)
	}
	// An explicit selection wins.
	if _, err := h.app.ComposePassthrough(ctx, root, Selection{Profiles: []string{"other"}}, []string{"ps"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lastFiles(), overlay) {
		t.Errorf("explicit selection ignored: %v", lastFiles())
	}
	// Reset keeps the selection, a plain up drops it.
	if _, err := h.app.Reset(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := h.app.lastSelection("shop"); !slices.Equal(got.Overlays, []string{"e2e"}) {
		t.Errorf("after reset: %+v", got)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lastFiles(), overlay) {
		t.Errorf("down after a plain up: %v", lastFiles())
	}
}

func TestDamagedSelectionMeansNone(t *testing.T) {
	h := newHarness(t, webModel())
	writeFile(t, h.app.selectionFile("shop"), "{nope")
	if got := h.app.lastSelection("shop"); !got.empty() {
		t.Fatalf("got %+v", got)
	}
}

func TestUpFailsWhenTheSelectionCannotBeSaved(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if err := os.MkdirAll(h.app.selectionFile("shop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err == nil {
		t.Fatal("want error")
	}
}

func TestUpSomeServices(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel()) // bootstrap in worker, post-up in web

	res, err := h.app.Up(ctx, root, UpOptions{Services: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if up := h.compose.RunsWith("up"); !slices.Equal(up[0], []string{"up", "--detach", "--wait", "--remove-orphans", "web"}) {
		t.Fatalf("up %v", up)
	}
	// The bootstrap hook's service did not start: skipped, and the bootstrap is not done.
	if res.Bootstrapped || !strings.Contains(h.stderr.String(), "skipping bootstrap hook 1: worker is not running") {
		t.Fatalf("bootstrapped=%v stderr %q", res.Bootstrapped, h.stderr.String())
	}
	if _, ok, _ := h.eng.InspectVolume(ctx, bootstrapVolume("shop")); ok {
		t.Fatal("marker written although a hook was skipped")
	}
	if res, err = h.app.Up(ctx, root, UpOptions{}); err != nil || !res.Bootstrapped {
		t.Fatalf("full up: %+v %v", res, err)
	}
}

func TestHooksFailWhenContainersCannotBeListed(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, hookModel())
	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"ListContainers": engine.ErrUnavailable}
	if _, err := h.app.runHooks(ctx, inst, compose.Spec{}, "post-up", inst.Config.Hooks.PostUp); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
}

func TestOverlaySetsGetOwnOverrides(t *testing.T) {
	root := newRepo(t, "shop")
	m := webModel()
	m.XMutti = []byte(`{"overlays":{"a":["a.yml"],"b":["b.yml"],"a-b":["ab.yml"]}}`)
	h := newHarness(t, m)
	for _, sel := range [][]string{{"a", "b"}, {"a-b"}} {
		if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Overlays: sel}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"override-a+b.yml", "override-a-b.yml"} {
		if _, err := os.Stat(filepath.Join(h.app.Dirs.InstanceDir("shop"), name)); err != nil {
			t.Error(err)
		}
	}
}

func TestUpStartsProfiledServicesOnlyWithTheirProfile(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{"web": {}, "tool": {Profiles: []string{"tools"}}}})
	started := func() int {
		cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=tool")
		return len(cs)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil || started() != 0 {
		t.Fatalf("without profile: %d %v", started(), err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Profiles: []string{"tools"}}}); err != nil || started() != 1 {
		t.Fatalf("with profile: %d %v", started(), err)
	}
}

func TestStopAndDownReachServicesStartedByName(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{"web": {}, "tool": {Profiles: []string{"tools"}}}})
	tool := func() []engine.Container {
		cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=tool")
		return cs
	}
	if _, err := h.app.Up(ctx, root, UpOptions{Services: []string{"tool"}}); err != nil || len(tool()) != 1 {
		t.Fatalf("up tool: %v", err)
	}
	if _, err := h.app.Stop(ctx, root, Selection{}); err != nil || tool()[0].State != "exited" {
		t.Fatalf("stop left the tool %+v: %v", tool(), err)
	}
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil || len(tool()) != 0 {
		t.Fatalf("down left the tool %+v: %v", tool(), err)
	}
}
