package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
	"github.com/l0wskilled/mutti/internal/reaper"
)

// fakeReaper records what a run hands to its reaper.
type fakeReaper struct {
	addr    string
	filters []string
	closed  bool
	err     error
}

func (f *fakeReaper) dial(_ context.Context, addr string, filters ...string) (io.Closer, error) {
	f.addr, f.filters = addr, filters
	return f, f.err
}

func (f *fakeReaper) Close() error { f.closed = true; return nil }

func runHarness(t *testing.T, m *compose.Model) (*harness, *fakeReaper) {
	t.Helper()
	h := newHarness(t, m)
	r := &fakeReaper{}
	h.app.DialReaper = r.dial
	return h, r
}

func TestRunOnTheHost(t *testing.T) {
	root := newRepo(t, "shop")
	h, r := runHarness(t, hookModel())
	var out strings.Builder
	h.app.IO.Out = &out

	res, err := h.app.Run(ctx, root, RunOptions{Command: []string{"sh", "-c", `echo "$MUTTI_RUN_ID $MUTTI_URL $MUTTI_INSTANCE"; exit 7`}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 || !strings.HasPrefix(res.Instance, "shop-run-") || res.URL != "https://run-"+res.RunID+".shop.localhost" {
		t.Fatalf("result %+v", res)
	}
	if got := out.String(); got != res.RunID+" "+res.URL+" "+res.Instance+"\n" {
		t.Errorf("command saw %q", got)
	}
	if !strings.HasPrefix(r.addr, "127.0.0.1:") || !r.closed ||
		!slices.Equal(r.filters, []string{"label=com.docker.compose.project=" + res.Instance, "label=mutti.run=" + res.RunID}) {
		t.Errorf("reaper %+v", r)
	}
	if _, err := os.Stat(filepath.Join(res.Logs, "services.log")); err != nil || res.Logs != filepath.Join(root, ".mutti", "local", "runs", res.RunID) {
		t.Errorf("logs %s: %v", res.Logs, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); !strings.Contains(string(b), "/.mutti/local/\n") {
		t.Errorf("exclude %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, ".mutti", ".gitignore")); !os.IsNotExist(err) {
		t.Error("a git repository must not get a .mutti/.gitignore")
	}
	// Bootstrap ran in the fresh instance; nothing of the run is left.
	if hooks := h.compose.RunsWith("exec"); len(hooks) == 0 || !slices.Contains(hooks[0], "seed") {
		t.Errorf("bootstrap did not run: %v", h.compose.Runs)
	}
	if cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"="+res.Instance); len(cs) != 0 {
		t.Errorf("containers left: %+v", cs)
	}
	if ns, _ := h.eng.ListNetworks(ctx, LabelRun); len(ns) != 0 {
		t.Errorf("networks left: %+v", ns)
	}
	if l, _ := h.app.List(ctx); len(l) != 0 {
		t.Errorf("a run must not show up as an instance: %+v", l)
	}
}

func TestRunInAService(t *testing.T) {
	root := newRepo(t, "shop")
	h, _ := runHarness(t, webModel())
	h.compose.Exit = func(args []string) int {
		if args[0] == "run" {
			return 3
		}
		return 0
	}
	logs := filepath.Join(t.TempDir(), "artifacts")
	res, err := h.app.Run(ctx, root, RunOptions{Service: "web", Command: []string{"npm", "test"}, LogsDir: logs, Build: true})
	if err != nil || res.ExitCode != 3 || res.Logs != logs {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	run := h.compose.RunsWith("run")[0]
	if !slices.Equal(run[:4], []string{"run", "--rm", "--no-deps", "-T"}) || !slices.Equal(run[len(run)-3:], []string{"web", "npm", "test"}) || !slices.Contains(run, "MUTTI_RUN_ID="+res.RunID) {
		t.Errorf("run args %v", run)
	}
	if up := h.compose.RunsWith("up")[0]; !slices.Contains(up, "--build") {
		t.Errorf("up args %v", up)
	}
	h.app.Host.StdinTTY = true
	if _, err := h.app.Run(ctx, root, RunOptions{Service: "web", User: "root", Command: []string{"sh"}}); err != nil {
		t.Fatal(err)
	}
	if run := h.compose.RunsWith("run")[1]; slices.Contains(run, "-T") || !slices.Contains(run, "root") {
		t.Errorf("with a terminal, as root: %v", run)
	}
}

func TestRunTeardownAfterFailures(t *testing.T) {
	root := newRepo(t, "shop")
	for name, setup := range map[string]func(h *harness){
		"compose up":   func(h *harness) { h.compose.Exit = func(a []string) int { return map[string]int{"up": 1}[a[0]] } },
		"router":       func(h *harness) { h.app.Probe = nil; h.app.ReadyTimeout = 1; h.app.Probe = probeNever },
		"missing tool": func(h *harness) {},
		"user lookup": func(h *harness) {
			infos := 0
			h.eng.OnCall = func(m string) error {
				if m == "Info" {
					if infos++; infos == 1 {
						return engine.ErrUnavailable
					}
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, r := runHarness(t, webModel())
			setup(h)
			opts := RunOptions{Command: []string{"/no/such/tool"}}
			if name == "user lookup" {
				opts.Service = "web"
			}
			res, err := h.app.Run(ctx, root, opts)
			if err == nil {
				t.Fatal("want error")
			}
			if cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"="+res.Instance); len(cs) != 0 || !r.closed {
				t.Fatalf("not torn down: %+v closed=%v", cs, r.closed)
			}
		})
	}
}

func probeNever(context.Context, string, string) (int, error) { return 0, errors.New("never") }

func TestRunTeardownFailureFailsTheRun(t *testing.T) {
	root := newRepo(t, "shop")
	for name, setup := range map[string]func(h *harness, logs string){
		"down": func(h *harness, _ string) {
			h.compose.Exit = func(a []string) int { return map[string]int{"down": 1}[a[0]] }
		},
		"down cannot start": func(h *harness, _ string) {
			h.app.Compose = &startFailsAfter{Fake: h.compose, ok: 2, err: errors.New("boom")} // up and logs run, down cannot start
		},
		"logs dir": func(h *harness, logs string) { writeFile(t, logs, "a file where a dir belongs") },
		"logs file": func(h *harness, logs string) {
			if err := os.MkdirAll(filepath.Join(logs, "services.log", "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"purge": func(h *harness, _ string) {
			h.eng.OnCall = func(m string) error {
				if m == "RemoveVolume" {
					return engine.ErrUnavailable
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := runHarness(t, &compose.Model{Services: map[string]compose.Service{"worker": {}}})
			logs := filepath.Join(t.TempDir(), "logs")
			setup(h, logs)
			res, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}, LogsDir: logs})
			if appErrCode(err) != CodeTeardownFailed || res.ExitCode != 0 {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}

	// prepare fails during teardown (state dir gone bad meanwhile).
	h, _ := runHarness(t, webModel())
	h.app.Host.Getenv = env{}.get
	calls := 0
	h.app.Host.Getenv = func(k string) string {
		if k == "MUTTI_HTTP_PORT" {
			if calls++; calls > 3 {
				return "x"
			}
		}
		return ""
	}
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); err == nil {
		t.Fatal("want error")
	}
}

func TestRunEarlyFailures(t *testing.T) {
	root := newRepo(t, "shop")
	h, _ := runHarness(t, webModel())
	h.compose.VersionErr = compose.ErrNotInstalled
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeComposeMissing {
		t.Errorf("compose: %v", err)
	}
	h, _ = runHarness(t, webModel())
	if _, err := h.app.Run(ctx, t.TempDir(), RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeNoComposeFile {
		t.Errorf("no project: %v", err)
	}
	h, _ = runHarness(t, webModel())
	_ = os.Remove(filepath.Join(root, ".git", "info", "exclude")) // git init made it a file
	writeFile(t, filepath.Join(root, ".git", "info", "exclude", "blocker"), "")
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); err == nil {
		t.Error("exclude: want error")
	}
	_ = os.RemoveAll(filepath.Join(root, ".git", "info", "exclude"))

	plain := filepath.Join(t.TempDir(), "plain")
	writeFile(t, filepath.Join(plain, "compose.yml"), "")
	writeFile(t, filepath.Join(plain, ".mutti"), "a file")
	h, _ = runHarness(t, webModel())
	if _, err := h.app.Run(ctx, plain, RunOptions{Command: []string{"true"}}); err == nil {
		t.Error("gitignore outside git: want error")
	}
	_ = os.Remove(filepath.Join(plain, ".mutti"))
	h, _ = runHarness(t, webModel())
	if _, err := h.app.Run(ctx, plain, RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(plain, ".mutti", ".gitignore")); string(b) != "local/\n" {
		t.Errorf("gitignore outside git %q", b)
	}

	// A sweep that fails only warns.
	h, _ = runHarness(t, webModel())
	h.eng.OnCall = func(m string) error {
		if m == "ListNetworks" {
			h.eng.OnCall = nil
			return engine.ErrUnavailable
		}
		return nil
	}
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); err != nil || !strings.Contains(h.stderr.String(), "could not sweep") {
		t.Errorf("sweep warning: %v %q", err, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "could not sweep old runs") {
		t.Error("warning missing")
	}
}

func TestReaperFailures(t *testing.T) {
	root := newRepo(t, "shop")
	for _, method := range []string{"ImageExists", "CreateContainer", "StartContainer", "ListContainers"} {
		t.Run(method, func(t *testing.T) {
			h, _ := runHarness(t, webModel())
			h.eng.AddImage(reaper.Image)
			lists := 0
			h.eng.OnCall = func(m string) error {
				if m != method {
					return nil
				}
				if m == "ListContainers" {
					// The sweep lists the reapers first; the reaper's own list is the second.
					if lists++; lists < 2 {
						return nil
					}
				}
				return engine.ErrUnavailable
			}
			if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeDockerUnavailable {
				t.Fatal(err)
			}
			h.eng.OnCall = nil
			if left, _ := h.eng.ListContainers(ctx, LabelReaper); len(left) != 0 {
				t.Errorf("reaper left: %+v", left)
			}
		})
	}
	t.Run("no port", func(t *testing.T) {
		h, _ := runHarness(t, webModel())
		h.app.Engine = portless{h.eng}
		if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeReaperFailed {
			t.Fatal(err)
		}
		if left, _ := h.eng.ListContainers(ctx, LabelReaper); len(left) != 0 {
			t.Errorf("reaper left: %+v", left)
		}
	})
	t.Run("dial", func(t *testing.T) {
		h, r := runHarness(t, webModel())
		r.err = errors.New("refused")
		if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeReaperFailed {
			t.Fatal(err)
		}
		if left, _ := h.eng.ListContainers(ctx, LabelReaper); len(left) != 0 {
			t.Errorf("reaper left: %+v", left)
		}
	})
}

// portless reports the reaper without a published port, as if it died right after starting.
type portless struct{ *enginetest.Fake }

func (p portless) ListContainers(c context.Context, filters ...string) ([]engine.Container, error) {
	cs, err := p.Fake.ListContainers(c, filters...)
	if len(filters) == 1 && strings.HasPrefix(filters[0], LabelReaper+"=") {
		for i := range cs {
			cs[i].Ports = nil
		}
	}
	return cs, err
}

func TestRunNested(t *testing.T) {
	root := newRepo(t, "shop")
	h := nestedHarness(t, root, webModel())
	r := &fakeReaper{}
	h.app.DialReaper = r.dial
	_ = h.eng.ConnectNetwork(ctx, mustNetwork(t, h, "ci-net"), "self", nil)
	res, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.addr, "10.") || !strings.HasSuffix(r.addr, ":8080") {
		t.Errorf("reaper address %q", r.addr)
	}
	_ = res

	h.eng.FailOn = map[string]error{"InspectContainer": engine.ErrUnavailable}
	if _, err := h.app.startReaper(ctx, &Instance{runID: "x"}); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("inspect self: %v", err)
	}
	h.eng.FailOn = nil
	inspects := 0
	h.eng.OnCall = func(m string) error {
		if m == "InspectContainer" {
			if inspects++; inspects == 2 {
				return engine.ErrUnavailable
			}
		}
		return nil
	}
	if _, err := h.app.startReaper(ctx, &Instance{runID: "y"}); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("inspect reaper: %v", err)
	}
}

func mustNetwork(t *testing.T, h *harness, name string) string {
	t.Helper()
	n, err := h.eng.CreateNetwork(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return n.Name
}

func TestSweep(t *testing.T) {
	h := newHarness(t, webModel())
	old := NewRunID(testNow.Add(-48 * time.Hour))
	fresh := NewRunID(testNow.Add(-time.Hour))
	dead := NewRunID(testNow.Add(-time.Minute))
	for _, id := range []string{old, fresh, dead} {
		inst := "shop-run-" + id
		h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: inst, LabelRun: id}})
		h.eng.AddNetwork(engine.Network{Name: "mutti-" + inst, Labels: map[string]string{engine.LabelInstance: inst, engine.LabelRole: RoleIngress, LabelRun: id}})
	}
	// Runs whose reaper still runs are in progress; dead has none.
	for _, id := range []string{old, fresh} {
		h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{LabelReaper: id}})
	}
	h.eng.AddNetwork(engine.Network{Name: "garbage", Labels: map[string]string{engine.LabelInstance: "shop-run-x", LabelRun: "not-a-run-id"}})

	if err := h.app.Sweep(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	left, _ := h.eng.ListContainers(ctx, LabelRun)
	if len(left) != 1 || left[0].Labels[LabelRun] != fresh {
		t.Fatalf("left %+v", left)
	}

	for _, method := range []string{"ListContainers", "ListNetworks"} {
		h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
		if err := h.app.Sweep(ctx, time.Hour); appErrCode(err) != CodeDockerUnavailable {
			t.Errorf("%s: %v", method, err)
		}
	}
	lists := 0
	h.eng.FailOn = nil
	h.eng.OnCall = func(m string) error {
		if m == "ListContainers" {
			if lists++; lists == 2 {
				return engine.ErrUnavailable
			}
		}
		return nil
	}
	if err := h.app.Sweep(ctx, time.Hour); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("reapers: %v", err)
	}
	h.eng.OnCall = nil
	h.eng.FailOn = map[string]error{"RemoveContainer": engine.ErrUnavailable}
	if err := h.app.Sweep(ctx, time.Nanosecond); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("purge: %v", err)
	}
}

func TestSweepHooks(t *testing.T) {
	root := newRepo(t, "shop")
	m := webModel()
	m.XMutti = []byte(`{"hooks":{"sweep":[{"service":"web","run":"drop-old-dbs"}]}}`)
	h := newHarness(t, m)
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	ran, err := h.app.SweepHooks(ctx, root, 2*time.Hour)
	if err != nil || !ran {
		t.Fatalf("%v %v", ran, err)
	}
	last := h.compose.Runs[len(h.compose.Runs)-1]
	if !slices.Contains(last, "MUTTI_SWEEP_OLDER_THAN=7200") || last[len(last)-1] != "drop-old-dbs" {
		t.Errorf("hook args %v", last)
	}
	h = newHarness(t, webModel())
	if ran, err := h.app.SweepHooks(ctx, root, time.Hour); err != nil || ran {
		t.Errorf("no hooks: %v %v", ran, err)
	}
	if _, err := h.app.SweepHooks(ctx, t.TempDir(), time.Hour); err == nil {
		t.Error("no project: want error")
	}
}

func TestRunIDs(t *testing.T) {
	id := NewRunID(testNow)
	if made, ok := runTime(id); !ok || !made.Equal(testNow) {
		t.Errorf("%s: %v %v", id, made, ok)
	}
	if id == NewRunID(testNow) {
		t.Error("ids must differ")
	}
	for _, bad := range []string{"", "nodash", "!!-x"} {
		if _, ok := runTime(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRunSetupFailures(t *testing.T) {
	root := newRepo(t, "shop")
	cases := map[string]struct {
		setup func(h *harness)
		opts  RunOptions
	}{
		"ingress": {setup: func(h *harness) { h.eng.FailOn = map[string]error{"CreateNetwork": engine.ErrUnavailable} }},
		"router":  {setup: func(h *harness) { h.eng.FailOn = map[string]error{"CopyToContainer": engine.ErrUnavailable} }},
		"overlay": {opts: RunOptions{Selection: Selection{Overlays: []string{"nope"}}}},
		"bootstrap": {setup: func(h *harness) {
			h.compose.Exit = func(a []string) int {
				if slices.Contains(a, "seed") {
					return 1
				}
				return 0
			}
		}},
		"post-up": {setup: func(h *harness) {
			h.compose.Exit = func(a []string) int {
				if slices.Contains(a, "warm") {
					return 1
				}
				return 0
			}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := runHarness(t, hookModel())
			if tc.setup != nil {
				tc.setup(h)
			}
			tc.opts.Command = []string{"true"}
			if _, err := h.app.Run(ctx, root, tc.opts); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRunWithTheRealDialer(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel()) // DialReaper nil: reaper.Dial against a port nobody listens on
	c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := h.app.Run(c, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeReaperFailed {
		t.Fatal(err)
	}
}

func TestUpWarnsWhenSweepFails(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	h.eng.OnCall = func(m string) error {
		if m == "ListNetworks" {
			h.eng.OnCall = nil
			return engine.ErrUnavailable
		}
		return nil
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "could not sweep old runs") {
		t.Errorf("stderr %q", h.stderr.String())
	}
}

func TestCommandsInsideARunUseItsInstance(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	vars := env{"MUTTI_RUN_ID": "abc-1234", "MUTTI_INSTANCE": "shop-run-abc-1234"}
	h.app.Host.Getenv = vars.get

	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "shop-run-abc-1234" || inst.Hostname != "run-abc-1234.shop.localhost" || inst.runID != "abc-1234" {
		t.Fatalf("instance %+v", inst)
	}
	if _, err := h.app.Exec(ctx, root, "web", []string{"true"}, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := h.compose.Specs[len(h.compose.Specs)-1].Project; got != "shop-run-abc-1234" {
		t.Errorf("exec went to %s", got)
	}

	for name, call := range map[string]func() error{
		"up":      func() error { _, err := h.app.Up(ctx, root, UpOptions{}); return err },
		"destroy": func() error { _, err := h.app.Destroy(ctx, root); return err },
		"reset":   func() error { _, err := h.app.Reset(ctx, root, UpOptions{}); return err },
		"rename":  func() error { _, err := h.app.Rename(ctx, root, "other"); return err },
	} {
		if err := call(); appErrCode(err) != CodeRunInstance {
			t.Errorf("%s: %v", name, err)
		}
	}

	// MUTTI_RUN_ID alone, inherited by a command in another project, changes nothing.
	vars["MUTTI_INSTANCE"] = "elsewhere"
	if inst, err := h.app.Resolve(ctx, root); err != nil || inst.Name != "shop" {
		t.Fatalf("other instance: %+v %v", inst, err)
	}
}

func TestRunFailsWhenTheSelectionCannotBeSaved(t *testing.T) {
	root := newRepo(t, "shop")
	h, _ := runHarness(t, webModel())
	h.compose.Exit = func(args []string) int {
		if args[0] == "up" {
			dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(h.app.Dirs.InstanceDir("x")), "shop-run-*"))
			for _, d := range dirs {
				_ = os.Mkdir(filepath.Join(d, "selection.json"), 0o700)
			}
		}
		return 0
	}
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); err == nil {
		t.Fatal("want error")
	}
}

func TestSweepIgnoresForeignLabels(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	id := NewRunID(testNow.Add(-48 * time.Hour))
	// An image can carry any label: a container of shop claiming to belong to a run, and one
	// without a project.
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: "shop", LabelRun: id}})
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{LabelRun: id}})
	// Networks that are no run's ingress network: wrong name, invalid instance, not ingress.
	h.eng.AddNetwork(engine.Network{Name: "elsewhere", Labels: map[string]string{engine.LabelInstance: "shop", engine.LabelRole: RoleIngress, LabelRun: id}})
	h.eng.AddNetwork(engine.Network{Name: "mutti-", Labels: map[string]string{engine.LabelInstance: "", engine.LabelRole: RoleIngress, LabelRun: id}})
	h.eng.AddNetwork(engine.Network{Name: "mutti-../x", Labels: map[string]string{engine.LabelInstance: "../x", engine.LabelRole: RoleIngress, LabelRun: id}})
	h.eng.AddNetwork(engine.Network{Name: "mutti-shop", Labels: map[string]string{engine.LabelInstance: "shop", LabelRun: id}})

	if err := h.app.Sweep(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, h, "shop"); s.Status != StatusRunning {
		t.Fatalf("shop was swept: %+v", s)
	}
	if _, err := os.Stat(h.app.Dirs.InstanceDir("shop")); err != nil {
		t.Fatalf("instance dirs were removed: %v", err)
	}
	if err := h.app.purge(ctx, ""); err == nil {
		t.Error("purge of an empty name: want error")
	}
}

func TestRunWhoseHostnameIsTaken(t *testing.T) {
	root := newRepo(t, "shop")
	h, _ := runHarness(t, webModel())
	// While the run starts, another instance turns up with a service on the run's hostname.
	h.compose.Exit = func(args []string) int {
		if args[0] != "up" {
			return 0
		}
		nets, _ := h.eng.ListNetworks(ctx, LabelRun)
		host := "run-" + nets[0].Labels[LabelRun] + ".shop.localhost"
		h.eng.AddNetwork(engine.Network{Name: "mutti-other", Labels: map[string]string{engine.LabelInstance: "other", engine.LabelRole: RoleIngress, LabelRouter: "mutti-router"}})
		h.eng.AddContainer(engine.Container{Name: "other-x-1", State: "running", Labels: map[string]string{
			engine.LabelInstance: "other", engine.LabelComposeProject: "other", engine.LabelComposeService: "x",
			LabelHostname: "shop.localhost", LabelRouter: "mutti-router", LabelRoutePort: "80", LabelRouteHosts: host,
		}})
		return 0
	}
	if _, err := h.app.Run(ctx, root, RunOptions{Command: []string{"true"}}); appErrCode(err) != CodeHostnameTaken {
		t.Fatal(err)
	}
}
