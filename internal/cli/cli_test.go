package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/compose/composetest"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/home"
	"github.com/l0wskilled/mutti/internal/tui"
)

type world struct {
	t       *testing.T
	app     *app.App
	eng     *enginetest.Fake
	compose *composetest.Fake
	root    string
	stdin   string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "shop")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", "-b", "main")
	git.Dir = root
	git.Env = gitdir.CleanEnv(os.Environ())
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	eng := enginetest.New()
	model := &compose.Model{Services: map[string]compose.Service{"web": {Ports: []compose.Port{{Target: 80}}}}}
	fc := composetest.New(eng, model)
	w := &world{t: t, eng: eng, compose: fc, root: root}
	w.app = &app.App{
		Engine:  eng,
		Compose: fc,
		Git:     gitdir.Git{},
		Dirs:    home.Dirs{State: filepath.Join(base, "state"), Data: filepath.Join(base, "data")},
		Host:    app.Host{UID: 1000, GID: 1000, Getenv: func(string) string { return "" }, LookPath: exec.LookPath, SystemRoots: func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }},
		IO:      compose.IO{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}},
		Domain:  "localhost",
		Now:     time.Now,
		Probe:   func(context.Context, string, string) (int, error) { return http.StatusOK, nil },
		Trusts:  &fakeTrust{},
	}
	return w
}

// run executes mutti in dir and returns exit code, stdout and stderr.
func (w *world) run(dir string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(context.Background(), Deps{App: w.app, Dir: dir, In: strings.NewReader(w.stdin), Out: &out, Err: &errOut, Version: "1.2.3"}, args)
	return code, out.String(), errOut.String()
}

func (w *world) mustRun(args ...string) string {
	w.t.Helper()
	code, out, errOut := w.run(w.root, args...)
	if code != 0 {
		w.t.Fatalf("mutti %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func decode[T any](t *testing.T, s string) (int, T) {
	t.Helper()
	var env struct {
		SchemaVersion int `json:"schemaVersion"`
		Data          T   `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return env.SchemaVersion, env.Data
}

func TestLifecycleText(t *testing.T) {
	w := newWorld(t)
	if out := w.mustRun("up"); out != "shop is up: https://shop.localhost\n" {
		t.Errorf("up: %q", out)
	}
	out := w.mustRun("list")
	if !strings.HasPrefix(out, "NAME  STATUS       BRANCH  URL") || !strings.Contains(out, "running 1/1") || !strings.Contains(out, "https://shop.localhost") || strings.Contains(out, "web:80=") {
		t.Errorf("list: %q", out)
	}
	if out := w.mustRun("restart"); out != "shop is restarted.\n" {
		t.Errorf("restart: %q", out)
	}
	if out := w.mustRun("stop", "web"); out != "shop: web stopped.\n" {
		t.Errorf("stop web: %q", out)
	}
	if last := w.compose.Runs[len(w.compose.Runs)-1]; !slices.Equal(last, []string{"stop", "web"}) {
		t.Errorf("stop web ran %v", last)
	}
	if out := w.mustRun("restart", "web"); out != "shop: web restarted.\n" {
		t.Errorf("restart web: %q", out)
	}
	if out := w.mustRun("stop"); out != "shop is stopped.\n" {
		t.Errorf("stop: %q", out)
	}
	if out := w.mustRun("down"); out != "shop is down.\n" {
		t.Errorf("down: %q", out)
	}
	if out := w.mustRun("destroy", "--yes"); out != "shop is gone.\n" {
		t.Errorf("destroy: %q", out)
	}
	if out := w.mustRun("list"); out != "No instances. Start one with: mutti up\n" {
		t.Errorf("empty list: %q", out)
	}
}

func TestLifecycleJSON(t *testing.T) {
	w := newWorld(t)
	v, up := decode[map[string]any](t, w.mustRun("up", "--json", "--build", "--profile", "tools"))
	inst := up["instance"].(map[string]any)
	if v != SchemaVersion || inst["name"] != "shop" || inst["hostname"] != "shop.localhost" || inst["path"] != w.root || up["bootstrapped"] != true {
		t.Errorf("up json: %v", up)
	}
	_, list := decode[[]app.Summary](t, w.mustRun("list", "--json"))
	if len(list) != 1 || list[0].Status != app.StatusRunning {
		t.Errorf("list json: %+v", list)
	}
	for _, cmd := range []string{"stop", "down"} {
		_, res := decode[map[string]any](t, w.mustRun(cmd, "--json"))
		if res["instance"].(map[string]any)["name"] != "shop" {
			t.Errorf("%s json: %v", cmd, res)
		}
	}
	_, res := decode[map[string]any](t, w.mustRun("destroy", "--yes", "--json"))
	if res["instance"] == nil {
		t.Errorf("destroy json: %v", res)
	}
}

func TestDirFlag(t *testing.T) {
	w := newWorld(t)
	parent := filepath.Dir(w.root)
	if code, out, _ := w.run(parent, "-C", "shop", "up"); code != 0 || !strings.HasPrefix(out, "shop is up") {
		t.Fatalf("relative -C: %d %q", code, out)
	}
	if code, _, _ := w.run("/", "--dir", w.root, "down"); code != 0 {
		t.Fatalf("absolute -C: %d", code)
	}
}

func TestErrors(t *testing.T) {
	w := newWorld(t)
	dir := t.TempDir()

	code, out, errOut := w.run(dir, "up")
	if code != ExitError || out != "" || !strings.Contains(errOut, "mutti: no Compose file here") || !strings.Contains(errOut, "hint: run mutti in a directory") {
		t.Fatalf("text error: %d %q %q", code, out, errOut)
	}

	code, _, errOut = w.run(dir, "up", "--json")
	var env struct {
		SchemaVersion int `json:"schemaVersion"`
		Error         struct{ Code, Message, Hint string }
	}
	if err := json.Unmarshal([]byte(errOut), &env); err != nil || code != ExitError || env.Error.Code != app.CodeNoComposeFile || env.Error.Hint == "" {
		t.Fatalf("json error: %d %q", code, errOut)
	}

	code, _, errOut = w.run(dir, "up", "--bogus")
	if code != ExitError || !strings.Contains(errOut, "unknown flag: --bogus") || !strings.Contains(errOut, "hint: see mutti --help") {
		t.Fatalf("flag error: %d %q", code, errOut)
	}

	// A plain error without app.Error still prints.
	w.eng.FailOn = map[string]error{"ListVolumes": errors.New("plain")}
	if code, _, errOut := w.run(dir, "list"); code != ExitError || errOut != "mutti: plain\n" {
		t.Fatalf("plain error: %d %q", code, errOut)
	}
}

func TestEveryCommandReportsFailures(t *testing.T) {
	w := newWorld(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"stop"}, {"down"}, {"destroy", "--yes"}, {"exec", "web", "--", "id"}, {"logs"}, {"compose", "ps"}, {"rename", "x"},
	} {
		if code, _, _ := w.run(dir, args...); code != ExitError {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	w.eng.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable}
	if code, _, _ := w.run(w.root, "tidy"); code != ExitError {
		t.Errorf("tidy: exit %d", code)
	}
}

func TestDestroyConfirmation(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")

	code, _, errOut := w.run(w.root, "destroy")
	if code != ExitError || !strings.Contains(errOut, "pass --yes") {
		t.Fatalf("non-tty without --yes: %d %q", code, errOut)
	}

	w.app.Host.StdinTTY = true
	w.stdin = "n\n"
	if code, _, errOut := w.run(w.root, "destroy"); code != 1 || !strings.Contains(errOut, "[y/N]") {
		t.Fatalf("answered no: %d %q", code, errOut)
	}
	w.stdin = "yes\n"
	if code, out, _ := w.run(w.root, "destroy"); code != 0 || out != "shop is gone.\n" {
		t.Fatalf("answered yes: %d %q", code, out)
	}
}

func TestExecAndPassthrough(t *testing.T) {
	w := newWorld(t)
	w.compose.Exit = func(args []string) int {
		if args[0] == "exec" || args[0] == "logs" {
			return 7
		}
		return 0
	}
	if code, _, _ := w.run(w.root, "exec", "web", "--", "false"); code != 7 {
		t.Errorf("exec exit %d", code)
	}
	last := w.compose.Runs[len(w.compose.Runs)-1]
	if last[len(last)-2] != "web" || last[len(last)-1] != "false" {
		t.Errorf("exec args %v", last)
	}
	if code, _, _ := w.run(w.root, "exec", "-u", "root", "web", "--", "id"); code != 7 {
		t.Errorf("exec -u exit %d", code)
	}
	if code, _, errOut := w.run(w.root, "exec", "web", "id"); code != ExitError || !strings.Contains(errOut, "separate the command with --") {
		t.Errorf("exec without --: %d %q", code, errOut)
	}
	if code, _, _ := w.run(w.root, "logs", "-f", "web"); code != 7 {
		t.Errorf("logs exit %d", code)
	}
	if last := w.compose.Runs[len(w.compose.Runs)-1]; strings.Join(last, " ") != "logs -f web" {
		t.Errorf("logs args %v", last)
	}
	if code, _, _ := w.run(w.root, "compose", "ps", "--all"); code != 0 {
		t.Errorf("compose exit %d", code)
	}
	if last := w.compose.Runs[len(w.compose.Runs)-1]; strings.Join(last, " ") != "ps --all" {
		t.Errorf("compose args %v", last)
	}
}

func TestTidyOutput(t *testing.T) {
	w := newWorld(t)
	if out := w.mustRun("tidy"); out != "Nothing to tidy.\n" {
		t.Errorf("nothing: %q", out)
	}
	w.mustRun("up")
	if err := os.RemoveAll(w.root); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	code, out, _ := w.run(other, "tidy")
	if code != 0 || !strings.HasPrefix(out, "would remove shop (") || !strings.Contains(out, "Run it with --yes to do it.") {
		t.Errorf("dry: %d %q", code, out)
	}
	_, out, _ = w.run(other, "list")
	if !strings.Contains(out, "orphaned") {
		t.Errorf("list orphan: %q", out)
	}
	code, out, _ = w.run(other, "tidy", "--yes", "--json")
	_, res := decode[map[string]any](t, out)
	if code != 0 || len(res["removed"].([]any)) != 1 || len(res["stopped"].([]any)) != 0 {
		t.Errorf("apply json: %d %v", code, res)
	}
	w.mustRunIn(other, "up", "--help")
}

func (w *world) mustRunIn(dir string, args ...string) {
	w.t.Helper()
	if code, out, errOut := w.run(dir, args...); code != 0 {
		w.t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
}

func TestTidyApplyText(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	if err := os.RemoveAll(w.root); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := w.run(t.TempDir(), "tidy", "-y"); !strings.HasPrefix(out, "removed shop (") {
		t.Errorf("apply: %q", out)
	}
}

func TestRenameOutput(t *testing.T) {
	w := newWorld(t)
	if out := w.mustRun("rename", "feature"); out != "This worktree is now shop-feature.\n" {
		t.Errorf("rename: %q", out)
	}
	if code, _, _ := w.run(w.root, "rename"); code != ExitError {
		t.Errorf("rename without name: %d", code)
	}
}

func TestDoctorOutput(t *testing.T) {
	w := newWorld(t)
	out := w.mustRun("doctor")
	for _, want := range []string{"ok    docker", "ok    compose      docker compose 2.30.0", "Compose features:", "  yes  include"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}

	w.compose.Ver = compose.MustParseVersion("2.21.0")
	w.compose.BuildxErr = errors.New("missing")
	code, out, _ := w.run(w.root, "doctor")
	if code != ExitCheck || !strings.Contains(out, "  no   watch") || !strings.Contains(out, "hint: mutti needs 2.24.4 or newer") {
		t.Errorf("doctor failing: %d\n%s", code, out)
	}
	_, report := decode[app.Report](t, mustOut(w.run(w.root, "doctor", "--json")))
	if len(report.Checks) != 7 {
		t.Errorf("doctor json: %+v", report)
	}
}

func mustOut(_ int, out, _ string) string { return out }

func TestVersionFlag(t *testing.T) {
	w := newWorld(t)
	if out := w.mustRun("--version"); out != "mutti version 1.2.3\n" {
		t.Errorf("version: %q", out)
	}
}

func TestExitErrorMessage(t *testing.T) {
	if (exitError{code: 3}).Error() != "exit code 3" {
		t.Fatal("message")
	}
}

func TestSplitOverlays(t *testing.T) {
	sel, rest := splitOverlays([]string{"--overlay", "e2e", "--overlay=ci", "--profile", "e2e", "run", "--overlay", "x"})
	if strings.Join(sel.Overlays, ",") != "e2e,ci" || strings.Join(rest, " ") != "--profile e2e run --overlay x" {
		t.Fatalf("sel=%v rest=%v", sel.Overlays, rest)
	}
	if sel, rest := splitOverlays([]string{"--overlay"}); len(sel.Overlays) != 0 || len(rest) != 1 {
		t.Fatalf("dangling flag: %v %v", sel.Overlays, rest)
	}
}

func TestComposeWithOverlay(t *testing.T) {
	w := newWorld(t)
	code, _, errOut := w.run(w.root, "compose", "--overlay", "nope", "ps")
	if code != ExitError || !strings.Contains(errOut, `unknown overlay "nope"`) {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestRouterCommands(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	if out := w.mustRun("router", "status"); !strings.HasPrefix(out, "mutti-router listens on 127.0.0.1:80 (http) and :443 (https), routing 1 instance(s).\n  shop\n") {
		t.Errorf("status: %q", out)
	}
	_, status := decode[app.RouterStatus](t, w.mustRun("router", "restart", "--json"))
	if status.Name != "mutti-router" || len(status.Instances) != 1 {
		t.Errorf("restart json: %+v", status)
	}
	w.app.Host.Getenv = func(k string) string {
		if k == "MUTTI_HTTP_PORT" {
			return "x"
		}
		return ""
	}
	for _, sub := range []string{"status", "restart"} {
		if code, _, errOut := w.run(w.root, "router", sub); code != ExitError || !strings.Contains(errOut, "MUTTI_HTTP_PORT") {
			t.Errorf("%s: %d %q", sub, code, errOut)
		}
	}
}

func TestUpAndListWithoutRoutes(t *testing.T) {
	w := newWorld(t)
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"db": {Ports: []compose.Port{{Target: 5432}}}}}, nil
	}
	if out := w.mustRun("up"); out != "shop is up. See its ports with: mutti list\n" {
		t.Errorf("up: %q", out)
	}
	if out := w.mustRun("list"); !strings.Contains(out, "db:5432=127.0.0.1:") {
		t.Errorf("list: %q", out)
	}
}

type fakeTrust struct {
	installed []string
	err       error
}

func (f *fakeTrust) Install(certFile string) error {
	f.installed = append(f.installed, certFile)
	return f.err
}

func (f *fakeTrust) Uninstall(certFile string) error {
	f.installed = nil
	return f.err
}

func TestTrustOutput(t *testing.T) {
	w := newWorld(t)
	w.app.Host.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	out := w.mustRun("trust")
	if !strings.Contains(out, "is trusted now") || !strings.Contains(out, "install certutil") {
		t.Errorf("trust: %q", out)
	}
	w.app.Host.LookPath = exec.LookPath
	if out := w.mustRun("trust", "--uninstall"); !strings.Contains(out, "no longer trusted") {
		t.Errorf("uninstall: %q", out)
	}
	w.app.Trusts = &fakeTrust{err: errors.New("sudo: a password is required")}
	if code, _, errOut := w.run(w.root, "trust"); code != ExitError || !strings.Contains(errOut, "needs sudo") {
		t.Errorf("failure: %d %q", code, errOut)
	}
}

func TestWorktreeCommands(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.root
		cmd.Env = gitdir.CleanEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	wt := filepath.Join(filepath.Dir(w.root), "shop-feature")

	out := w.mustRun("worktree", "add", "feature")
	if out != "Created "+wt+" (branch feature).\nshop-feature is up: https://feature.shop.localhost\n" {
		t.Errorf("add: %q", out)
	}
	_, res := decode[map[string]any](t, w.mustRun("worktree", "add", "other", "--no-up", "--json"))
	if res["branch"] != "other" || res["instance"] != nil {
		t.Errorf("add --no-up json: %v", res)
	}
	if out := w.mustRun("worktree", "add", "quiet", "--from", "HEAD"); !strings.HasSuffix(out, "is up: https://quiet.shop.localhost\n") {
		t.Errorf("add --from: %q", out)
	}

	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"worker": {}}}, nil
	}
	if out := w.mustRun("worktree", "add", "nourl"); !strings.HasSuffix(out, "shop-nourl is up.\n") {
		t.Errorf("add without routes: %q", out)
	}
	w.compose.Exit = func(args []string) int { return map[string]int{"up": 1}[args[0]] }
	code, out, _ := w.run(w.root, "worktree", "add", "failing")
	if code != ExitError || !strings.HasPrefix(out, "Created ") {
		t.Errorf("failing up: %d %q", code, out)
	}
	w.compose.Exit = nil
	if code, _, _ := w.run(w.root, "worktree", "add", "Bad"); code != ExitError {
		t.Errorf("bad name: %d", code)
	}

	if code, _, errOut := w.run(w.root, "worktree", "remove", "feature"); code != ExitError || !strings.Contains(errOut, "pass --yes") {
		t.Errorf("remove without --yes: %d %q", code, errOut)
	}
	if out := w.mustRun("worktree", "remove", "feature", "--yes"); out != "Removed "+wt+".\n" {
		t.Errorf("remove: %q", out)
	}
	if code, _, _ := w.run(w.root, "worktree", "remove", "feature", "--yes"); code != ExitError {
		t.Errorf("remove twice: %d", code)
	}
}

func TestConvenienceCommands(t *testing.T) {
	w := newWorld(t)
	var opened string
	w.app.Host.OpenURL = func(u string) error { opened = u; return nil }

	if out := w.mustRun("url"); out != "https://shop.localhost\n" {
		t.Errorf("url: %q", out)
	}
	if out := w.mustRun("url", "web", "--json"); !strings.Contains(out, `"url": "https://shop.localhost"`) {
		t.Errorf("url json: %q", out)
	}
	if out := w.mustRun("open"); out != "Opening https://shop.localhost\n" || opened != "https://shop.localhost" {
		t.Errorf("open: %q %q", out, opened)
	}
	for _, cmd := range []string{"url", "open"} {
		if code, _, _ := w.run(w.root, cmd, "nope"); code != ExitError {
			t.Errorf("%s nope: %d", cmd, code)
		}
	}
	if code, _, _ := w.run(w.root, "shell"); code != 0 {
		t.Errorf("shell: %d", code)
	}
	if last := w.compose.Runs[len(w.compose.Runs)-1]; !slices.Contains(last, "web") || !slices.Contains(last, "--user") {
		t.Errorf("shell args %v", last)
	}
	w.mustRun("shell", "web", "-u", "root")

	w.mustRun("up")
	out := w.mustRun("status")
	if !strings.HasPrefix(out, "shop (main) in "+w.root+"\nhttps://shop.localhost\n\nSERVICE") || !strings.Contains(out, "web      Up 1 second  https://shop.localhost  80=127.0.0.1:") {
		t.Errorf("status: %q", out)
	}
	if code, _, _ := w.run(t.TempDir(), "status"); code != ExitError {
		t.Errorf("status outside: %d", code)
	}

	w.app.Host.Getenv = func(k string) string { return map[string]string{"SHELL": "/usr/bin/fish"}[k] }
	if out := w.mustRun("env"); !strings.Contains(out, "set -gx MUTTI_INSTANCE 'shop'\n") {
		t.Errorf("env fish: %q", out)
	}
	if out := w.mustRun("env", "--format", "sh"); !strings.Contains(out, "export MUTTI_HOSTNAME='shop.localhost'\n") {
		t.Errorf("env sh: %q", out)
	}
	if out := w.mustRun("env", "--json"); !strings.Contains(out, `"MUTTI_PROJECT": "shop"`) {
		t.Errorf("env json: %q", out)
	}
	if code, _, _ := w.run(t.TempDir(), "env"); code != ExitError {
		t.Errorf("env outside: %d", code)
	}
}

func TestStatusWithoutBranchAndURL(t *testing.T) {
	w := newWorld(t)
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"worker": {}}}, nil
	}
	w.app.Git = noGitBranch{}
	if out := w.mustRun("status"); !strings.HasPrefix(out, "shop in "+w.root+"\n\nSERVICE") || !strings.Contains(out, "worker   missing") {
		t.Errorf("status: %q", out)
	}
}

type noGitBranch struct{ gitdir.Git }

func (noGitBranch) Branch(context.Context, string) (string, error) { return "", errors.New("detached") }

func TestCompletion(t *testing.T) {
	w := newWorld(t)
	out := w.mustRun("__complete", "exec", "")
	if !strings.HasPrefix(out, "web\n") {
		t.Errorf("exec: %q", out)
	}
	if out := w.mustRun("__complete", "exec", "web", ""); strings.Contains(out, "web\n") {
		t.Errorf("exec second arg: %q", out)
	}
	if out := w.mustRun("__complete", "url", ""); !strings.HasPrefix(out, "web\n") {
		t.Errorf("url: %q", out)
	}
	if out := w.mustRun("__complete", "worktree", "remove", ""); !strings.HasPrefix(out, ":4") {
		t.Errorf("worktree remove without worktrees: %q", out)
	}
	if out := w.mustRun("__complete", "worktree", "remove", "x", ""); !strings.HasPrefix(out, ":4") {
		t.Errorf("worktree remove second arg: %q", out)
	}
}

func TestResetCommand(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	if code, _, errOut := w.run(w.root, "reset"); code != ExitError || !strings.Contains(errOut, "pass --yes") {
		t.Errorf("without --yes: %d %q", code, errOut)
	}
	if out := w.mustRun("reset", "--yes"); out != "shop is fresh and up.\n" {
		t.Errorf("reset: %q", out)
	}
	if out := w.mustRun("reset", "--yes", "--json"); !strings.Contains(out, `"url": "https://shop.localhost"`) {
		t.Errorf("reset json: %q", out)
	}
	if code, _, _ := w.run(t.TempDir(), "reset", "--yes"); code != ExitError {
		t.Errorf("outside: %d", code)
	}
}

func TestWorktreeAddShowsCopiedFiles(t *testing.T) {
	w := newWorld(t)
	if err := os.WriteFile(filepath.Join(w.root, ".env.local"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "compose.yml"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.root
		cmd.Env = gitdir.CleanEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"web": {Ports: []compose.Port{{Target: 80}}}}, XMutti: []byte(`{"worktree":{"copy":[".env.local"]}}`)}, nil
	}
	if out := w.mustRun("worktree", "add", "x", "--no-up"); !strings.Contains(out, "Copied from the main worktree: .env.local\n") {
		t.Errorf("add: %q", out)
	}
}

func TestCustomCommands(t *testing.T) {
	w := newWorld(t)
	cmds := filepath.Join(w.root, ".mutti", "commands")
	for path, content := range map[string]string{
		"host/hello":        "#!/bin/sh\n# Say hello\necho \"hello $*\"\n",
		"web/db-migrate":    "# Migrate\nmigrate\n",
		"worker/db-migrate": "migrate\n",
		"web/up":            "# tries to shadow up\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(cmds, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cmds, path), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	w.app.IO.Out = &out

	code, help, errOut := w.run(w.root, "--help")
	help = strings.Join(strings.Fields(help), " ")
	if code != 0 || !strings.Contains(help, "Project commands (.mutti/commands): hello Say hello (on the host) "+
		"web:db-migrate Migrate (in web) worker:db-migrate custom command (in worker)") {
		t.Errorf("help: %s", help)
	}
	if !strings.Contains(errOut, `custom command "up" is ignored`) {
		t.Errorf("shadow warning: %q", errOut)
	}

	if code, _, _ := w.run(w.root, "hello", "--flag", "x"); code != 0 || out.String() != "hello --flag x\n" {
		t.Errorf("host command: %d %q", code, out.String())
	}
	if code, _, _ := w.run(w.root, "web:db-migrate"); code != 0 {
		t.Errorf("service command: %d", code)
	}

	// From a subdirectory and through -C the commands are found too.
	sub := filepath.Join(w.root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := w.run(sub, "hello"); code != 0 {
		t.Errorf("from a subdir: %d", code)
	}
	if code, _, _ := w.run(t.TempDir(), "-C", w.root, "hello"); code != 0 {
		t.Errorf("with -C: %d", code)
	}

	// A broken commands dir only warns.
	if err := os.Chmod(cmds, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cmds, 0o755) })
	if os.Geteuid() != 0 {
		code, _, errOut := w.run(w.root, "list")
		if code != 0 || !strings.Contains(errOut, "ignoring custom commands") {
			t.Errorf("broken dir: %d %q", code, errOut)
		}
	}
}

func TestDirFromArgs(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"up"}, "/cwd"},
		{[]string{"-C", "sub", "up"}, "/cwd/sub"},
		{[]string{"--dir", "/abs", "up"}, "/abs"},
		{[]string{"--dir=../x", "up"}, "/x"},
		{[]string{"-C=y"}, "/cwd/y"},
		{[]string{"exec", "web", "--", "-C", "nope"}, "/cwd"},
		{[]string{"-C"}, "/cwd"},
	} {
		if got := dirFromArgs("/cwd", tt.args); got != tt.want {
			t.Errorf("%v: %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestWorktreeAddWithDataFrom(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.root
		cmd.Env = gitdir.CleanEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	w.eng.AddImage(app.HelperImage)
	w.mustRun("up")
	if out := w.mustRun("worktree", "add", "copy", "--data-from", "main"); !strings.Contains(out, "Data copied from main: data\n") {
		t.Errorf("add: %q", out)
	}
}

func TestTidyIdleOutput(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	later := time.Now().Add(72 * time.Hour)
	w.app.Now = func() time.Time { return later }
	if out := w.mustRun("tidy", "--idle", "24h"); out != "would stop shop (idle, "+w.root+")\nRun it with --yes to do it.\n" {
		t.Errorf("dry: %q", out)
	}
	if out := w.mustRun("tidy", "--idle", "24h", "--yes"); out != "stopped shop (idle, "+w.root+")\n" {
		t.Errorf("apply: %q", out)
	}
}

func TestPromptCommand(t *testing.T) {
	w := newWorld(t)
	if out := w.mustRun("prompt"); out != "" {
		t.Errorf("before up: %q", out)
	}
	w.mustRun("up")
	if out := w.mustRun("prompt"); out != "shop running 1/1\n" {
		t.Errorf("up: %q", out)
	}
}

func TestAttachCommand(t *testing.T) {
	w := newWorld(t)
	if code, _, errOut := w.run(w.root, "attach"); code != ExitError || !strings.Contains(errOut, "only works inside a container") {
		t.Fatalf("on the host: %d %q", code, errOut)
	}
	w.app.Host.ContainerID = w.eng.AddContainer(engine.Container{Name: "self", State: "running"})
	w.eng.SetMounts(w.app.Host.ContainerID, []engine.Mount{{Source: "/", Target: "/"}})
	w.mustRun("up")
	if out := w.mustRun("attach"); out != "Attached to shop.\n" {
		t.Errorf("attach: %q", out)
	}
	if out := w.mustRun("attach", "--detach", "--json"); !strings.Contains(out, `"attached": false`) {
		t.Errorf("detach json: %q", out)
	}
	if out := w.mustRun("attach", "--detach"); out != "Detached from shop.\n" {
		t.Errorf("detach: %q", out)
	}
}

type cliReaper struct{}

func (cliReaper) Close() error { return nil }

func TestRunCommands(t *testing.T) {
	w := newWorld(t)
	w.app.DialReaper = func(context.Context, string, ...string) (io.Closer, error) { return cliReaper{}, nil }

	code, _, errOut := w.run(w.root, "run", "--", "sh", "-c", "exit 5")
	if code != 5 || !strings.Contains(errOut, "exited with 5, logs in ") {
		t.Errorf("run: %d %q", code, errOut)
	}
	code, out, _ := w.run(w.root, "run", "--json", "--", "true")
	if code != 0 || !strings.Contains(out, `"exitCode": 0`) || !strings.Contains(out, `"runId": "`) {
		t.Errorf("run json: %d %q", code, out)
	}
	// Failing before the command: no exit code to report.
	if code, _, errOut := w.run(w.root, "run", "--overlay", "nope", "--", "true"); code != ExitError || !strings.Contains(errOut, "failed, logs in ") || strings.Contains(errOut, "exited with") {
		t.Errorf("failed run: %d %q", code, errOut)
	}
	if code, _, errOut := w.run(w.root, "run", "true"); code != ExitError || !strings.Contains(errOut, "separate the command with --") {
		t.Errorf("without --: %d %q", code, errOut)
	}
	if code, _, _ := w.run(t.TempDir(), "run", "--", "true"); code != ExitError {
		t.Errorf("outside: %d", code)
	}

	if out := w.mustRun("run-id"); !strings.Contains(out, "-") || len(strings.TrimSpace(out)) < 10 {
		t.Errorf("run-id: %q", out)
	}

	if out := w.mustRun("sweep"); out != "Swept.\n" {
		t.Errorf("sweep: %q", out)
	}
	if code, out, _ := w.run(t.TempDir(), "sweep", "--older-than", "1h"); code != 0 || out != "Swept.\n" {
		t.Errorf("sweep outside a project: %d %q", code, out)
	}
	w.app.Host.Getenv = func(k string) string { return map[string]string{"MUTTI_HTTP_PORT": "x"}[k] }
	if code, _, _ := w.run(w.root, "sweep"); code != ExitError {
		t.Errorf("sweep with broken settings: %d", code)
	}
	w.app.Host.Getenv = func(string) string { return "" }
	w.eng.FailOn = map[string]error{"ListContainers": engine.ErrUnavailable}
	if code, _, _ := w.run(w.root, "sweep"); code != ExitError {
		t.Errorf("sweep with docker down: %d", code)
	}
}

func TestCommandsForDocs(t *testing.T) {
	root := Commands()
	for _, name := range []string{"up", "run", "worktree", "router"} {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestInit(t *testing.T) {
	w := newWorld(t)
	file := filepath.Join(w.root, "compose.yml")
	if err := os.WriteFile(file, []byte("services:\n  web: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out := w.mustRun("init", "--dry-run"); !strings.Contains(out, "compose.yml:\n+ # mutti") || !strings.Contains(out, "AGENTS.md:\n+ <!-- mutti:begin") {
		t.Errorf("dry run: %q", out)
	}
	if out := w.mustRun("init", "--dry-run", "--no-agents"); strings.Contains(out, "AGENTS.md") {
		t.Errorf("dry run without agents: %q", out)
	}
	if code, _, errOut := w.run(w.root, "init"); code != ExitError || !strings.Contains(errOut, "pass --yes") {
		t.Errorf("non-tty without --yes: %d %q", code, errOut)
	}
	if code, _, errOut := w.run(w.root, "init", "--json"); code != ExitError || !strings.Contains(errOut, "confirmation-required") {
		t.Errorf("json without --yes: %d %q", code, errOut)
	}
	w.app.Host.StdinTTY = true
	w.stdin = "n\n"
	if code, _, errOut := w.run(w.root, "init"); code != 1 || !strings.Contains(errOut, "+ x-mutti:") || !strings.Contains(errOut, "Write these changes? [y/N]") {
		t.Errorf("answered no: %d %q", code, errOut)
	}
	if b, _ := os.ReadFile(file); string(b) != "services:\n  web: {}\n" {
		t.Fatalf("written after no: %q", b)
	}
	w.stdin = "y\n"
	if code, out, _ := w.run(w.root, "init"); code != 0 || !strings.Contains(out, "Added x-mutti to ") || !strings.Contains(out, "Wrote the mutti section of ") || !strings.HasSuffix(out, "Next: mutti up\n") {
		t.Errorf("answered yes: %d %q", code, out)
	}

	// The fake's model keeps no x-mutti; one that has it leaves the Compose file alone.
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"web": {}}, XMutti: []byte(`{}`)}, nil
	}
	if out := w.mustRun("init", "--yes", "--no-agents"); out != "Nothing to change: the Compose file has x-mutti and AGENTS.md is current.\n" {
		t.Errorf("nothing to do: %q", out)
	}
	if out := w.mustRun("init", "--yes", "--json"); !strings.Contains(out, `"written": false`) || !strings.Contains(out, `"draft"`) {
		t.Errorf("json: %q", out)
	}
	if code, _, _ := w.run(t.TempDir(), "init", "--yes"); code != ExitError {
		t.Errorf("outside a project: %d", code)
	}
	// Planned, but AGENTS.md cannot be written.
	_ = os.Remove(filepath.Join(w.root, "AGENTS.md"))
	if err := os.Chmod(w.root, 0o555); err != nil {
		t.Fatal(err)
	}
	code, _, _ := w.run(w.root, "init", "--yes")
	_ = os.Chmod(w.root, 0o755)
	if code != ExitError {
		t.Errorf("unwritable: %d", code)
	}
}

func TestJSONStdoutHoldsOnlyJSON(t *testing.T) {
	w := newWorld(t)
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{
			Services: map[string]compose.Service{"web": {Ports: []compose.Port{{Target: 80}}}},
			XMutti:   []byte(`{"hooks":{"post-up":[{"service":"web","run":"chatty"}]}}`),
		}, nil
	}
	w.compose.Stdout = func(args []string) string {
		if args[0] == "exec" {
			return "hook says hi\n"
		}
		return ""
	}
	appOut, appErr := w.app.IO.Out.(*bytes.Buffer), w.app.IO.Err.(*bytes.Buffer)

	out := w.mustRun("up", "--json")
	decode[map[string]any](t, out)
	if appOut.Len() != 0 || !strings.Contains(appErr.String(), "hook says hi") {
		t.Fatalf("hook output: stdout %q, stderr %q", appOut, appErr)
	}
	// Passthrough output is the result, and stdout is restored after a --json command.
	w.mustRun("exec", "web", "--json", "--", "true")
	if !strings.Contains(appOut.String(), "hook says hi") {
		t.Errorf("exec output went elsewhere: %q", appOut)
	}
}

func TestFailureKeepsJoinedErrorsAndNamesCancellation(t *testing.T) {
	var out bytes.Buffer
	p := printer{json: true, err: &out}
	joined := errors.Join(&app.Error{Code: "compose-failed", Message: "up failed", Hint: "look"}, errors.New("teardown failed too"))
	p.failure(joined)
	var env struct {
		Error jsonError `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "compose-failed" || env.Error.Hint != "look" || !strings.Contains(env.Error.Message, "teardown failed too") {
		t.Errorf("joined: %+v", env.Error)
	}

	out.Reset()
	p.failure(fmt.Errorf("waiting: %w", context.Canceled))
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Error.Code != app.CodeCancelled {
		t.Errorf("cancelled: %+v %v", env.Error, err)
	}
}

func TestMCPServesOverTheCommandsStreams(t *testing.T) {
	w := newWorld(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan int)
	go func() {
		code := Run(context.Background(), Deps{App: w.app, Dir: w.root, In: inR, Out: outW, Err: io.Discard, Version: "1.2.3"}, []string{"mcp"})
		_ = outW.Close()
		done <- code
	}()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{
		"attach", "command", "commands", "compose", "destroy", "diagnose", "doctor", "down", "env", "exec", "init", "list", "logs", "open",
		"rename", "reset", "restart", "router_restart", "router_status", "run", "status", "stop", "sweep", "tidy", "up", "url", "worktree_add", "worktree_remove",
	}) {
		t.Errorf("tools %v", names)
	}
	if got := session.InitializeResult(); got != nil && got.ServerInfo.Version != "1.2.3" {
		t.Errorf("server info %+v", got.ServerInfo)
	}
	_ = session.Close()
	if code := <-done; code != 0 {
		t.Errorf("exit %d", code)
	}
}

// stream runs a streaming command and hands its stdout line by line, once it listens for events.
// stop ends it, or with cancel false waits for it to end by itself.
func (w *world) stream(args ...string) (lines <-chan string, stop func(cancel bool) (int, string)) {
	subs := w.eng.Subscribers()
	ctx, cancel := context.WithCancel(context.Background())
	outR, outW := io.Pipe()
	var errOut bytes.Buffer
	done := make(chan int)
	go func() {
		code := Run(ctx, Deps{App: w.app, Dir: w.root, In: strings.NewReader(""), Out: outW, Err: &errOut, Version: "1.2.3"}, args)
		_ = outW.Close()
		done <- code
	}()
	ch := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			ch <- sc.Text()
		}
		close(ch)
	}()
	for deadline := time.Now().Add(5 * time.Second); w.eng.Subscribers() == subs && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	return ch, func(stop bool) (int, string) {
		if stop {
			cancel()
		}
		for range ch { //nolint:revive // drain until the command ends
		}
		cancel()
		return <-done, errOut.String()
	}
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no output")
		return ""
	}
}

func TestEventsCommand(t *testing.T) {
	w := newWorld(t)
	lines, stop := w.stream("events", "--json")
	id := w.eng.AddContainer(engine.Container{Name: "shop-web-1", State: "running", Labels: map[string]string{engine.LabelInstance: "shop", engine.LabelComposeService: "web"}})
	_, ev := decode[app.InstanceEvent](t, next(t, lines))
	if ev.Instance != "shop" || ev.Service != "web" || ev.Event != "created" {
		t.Errorf("event %+v", ev)
	}
	w.eng.SetContainerState(id, "exited")
	if _, ev = decode[app.InstanceEvent](t, next(t, lines)); ev.Event != "started" {
		t.Errorf("event %+v", ev)
	}
	if code, _ := stop(true); code != 0 {
		t.Errorf("exit %d", code)
	}

	lines, stop = w.stream("events")
	w.eng.SetContainerState(id, "exited")
	if l := next(t, lines); !strings.HasSuffix(l, "  shop  web  stopped (exit 0)") {
		t.Errorf("text: %q", l)
	}
	stop(true)

	w.eng.FailOn = map[string]error{"Events": engine.ErrUnavailable}
	if code, _, _ := w.run(w.root, "events"); code != ExitError {
		t.Errorf("failing stream: %d", code)
	}
}

func TestListWatch(t *testing.T) {
	settle := watchSettle
	watchSettle = 10 * time.Millisecond
	t.Cleanup(func() { watchSettle = settle })
	w := newWorld(t)
	w.mustRun("up")

	lines, stop := w.stream("list", "--watch", "--json")
	if _, list := decode[[]app.Summary](t, next(t, lines)); len(list) != 1 || list[0].Status != app.StatusRunning {
		t.Fatalf("first list %+v", list)
	}
	cs, _ := w.eng.ListContainers(context.Background(), engine.LabelComposeProject+"=shop")
	w.eng.SetContainerState(cs[0].ID, "exited")
	if _, list := decode[[]app.Summary](t, next(t, lines)); len(list) != 1 || list[0].Status != app.StatusStopped {
		t.Fatalf("after a change %+v", list)
	}
	// A list that cannot be made ends the watch.
	w.eng.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable}
	w.eng.SetContainerState(cs[0].ID, "running")
	if code, errOut := stop(false); code != ExitError || !strings.Contains(errOut, "docker") {
		t.Errorf("failing list: %d %q", code, errOut)
	}

	// Text on a terminal clears the screen before every list.
	w.eng.FailOn = nil
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := Run(ctx, Deps{App: w.app, Dir: w.root, In: strings.NewReader(""), Out: &out, Err: io.Discard, OutTTY: true}, []string{"list", "--watch"})
	if code != 0 || !strings.HasPrefix(out.String(), "\x1b[H\x1b[2J") || !strings.Contains(out.String(), "shop") {
		t.Errorf("tty: %d %q", code, out.String())
	}

	w.eng.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable}
	if code, _, _ := w.run(w.root, "list", "--watch"); code != ExitError {
		t.Errorf("first list fails: %d", code)
	}
	w.eng.FailOn = map[string]error{"Events": engine.ErrUnavailable}
	if code, _, _ := w.run(w.root, "list", "--watch"); code != ExitError {
		t.Errorf("failing stream: %d", code)
	}
}

func TestServe(t *testing.T) {
	w := newWorld(t)
	var opened string
	w.app.Host.OpenURL = func(u string) error { opened = u; return errors.New("no browser") }
	ctx, cancel := context.WithCancel(context.Background())
	outR, outW := io.Pipe()
	var errOut bytes.Buffer
	done := make(chan int)
	go func() {
		code := Run(ctx, Deps{App: w.app, Dir: w.root, In: strings.NewReader(""), Out: outW, Err: &errOut}, []string{"serve", "--port", "0", "--open"})
		_ = outW.Close()
		done <- code
	}()
	line, _ := bufio.NewReader(outR).ReadString('\n')
	base, ok := strings.CutPrefix(strings.TrimSuffix(line, " (Ctrl-C to stop)\n"), "mutti serves ")
	if !ok {
		t.Fatalf("first line %q", line)
	}
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	res, err := http.Get(base + "/api/instances")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || opened != base {
		t.Errorf("status %d, opened %q", res.StatusCode, opened)
	}

	// The same port again cannot be had.
	port := base[strings.LastIndex(base, ":")+1:]
	if code, _, errOut := w.run(w.root, "serve", "--port", port); code != ExitError || !strings.Contains(errOut, "--port") {
		t.Errorf("port in use: %d %q", code, errOut)
	}

	cancel()
	if code := <-done; code != 0 || !strings.Contains(errOut.String(), "cannot open a browser") {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestStatusShowsProfiles(t *testing.T) {
	w := newWorld(t)
	w.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"web": {}, "report": {Profiles: []string{"tools"}}}}, nil
	}
	w.mustRun("up")
	out := w.mustRun("status")
	if !strings.Contains(out, "PROFILE  SERVICE  STATE") || !strings.Contains(out, "\n         web      Up 1 second") || !strings.Contains(out, "\ntools    report   off") {
		t.Errorf("status: %q", out)
	}
}

func TestDiagnoseAndStatusExplainTrouble(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	if out := w.mustRun("diagnose"); out != "All services are well.\n" {
		t.Errorf("well: %q", out)
	}
	w.compose.Stdout = func(args []string) string {
		if args[0] == "logs" {
			return strings.Repeat("line\n", 12) + "panic: no database\n"
		}
		return ""
	}
	id := w.eng.AddContainer(engine.Container{State: "running", Status: "Up 1 minute (unhealthy)", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "web"}})
	w.eng.SetRuntime(id, engine.Runtime{Health: []engine.HealthProbe{{Start: time.Unix(0, 0), ExitCode: 1, Output: "curl: (7) refused\n"}}})

	out := w.mustRun("status")
	if !strings.Contains(out, "! web: unhealthy: its health check exits with 1\n  -> ") || !strings.Contains(out, "exit 1):\n    curl: (7) refused\n  last logs:\n") ||
		strings.Count(out, "    line\n") != 7 || !strings.HasSuffix(out, "    panic: no database\n\nMore: mutti diagnose\n") {
		t.Errorf("status: %q", out)
	}
	if out := w.mustRun("diagnose", "web"); strings.Count(out, "    line\n") != 24 { // both web containers, all lines
		t.Errorf("diagnose: %q", out)
	}
	if code, _, _ := w.run(w.root, "diagnose", "--json"); code != 0 {
		t.Errorf("json: %d", code)
	}
	if code, _, _ := w.run(t.TempDir(), "diagnose"); code != ExitError {
		t.Errorf("outside: %d", code)
	}
}

func TestFailedUpExplainsWhatIsWrong(t *testing.T) {
	w := newWorld(t)
	w.mustRun("up")
	id := w.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (127) 1 second ago", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "web"}})
	w.eng.SetRuntime(id, engine.Runtime{ExitCode: 127})
	w.compose.Exit = func(args []string) int { return map[string]int{"up": 1, "restart": 1}[args[0]] }
	for _, cmd := range []string{"up", "restart"} {
		if code, _, stderr := w.run(w.root, cmd); code == 0 || !strings.Contains(stderr, "! web: exited with 127: the command was not found") {
			t.Errorf("%s: %d %q", cmd, code, stderr)
		}
	}
}

func TestTUICommand(t *testing.T) {
	w := newWorld(t)
	if code, _, stderr := w.run(w.root, "tui"); code != ExitError || !strings.Contains(stderr, "needs a terminal") {
		t.Fatalf("no terminal: %d %q", code, stderr)
	}
	defer func(r func(context.Context, tui.Options, io.Reader, io.Writer) error, e func() (string, error)) {
		runTUI, executable = r, e
	}(runTUI, executable)
	var got tui.Options
	runTUI = func(_ context.Context, opts tui.Options, _ io.Reader, _ io.Writer) error { got = opts; return nil }
	tty := func() int {
		return Run(context.Background(), Deps{App: w.app, Dir: w.root, In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, OutTTY: true}, []string{"tui"})
	}
	if code := tty(); code != 0 || got.Dir != w.root || got.Self == "" {
		t.Fatalf("tui: %d %+v", code, got)
	}
	uses := map[string]string{}
	for _, c := range got.Commands {
		uses[strings.Join(c.Path, " ")] = c.Use
	}
	for path, use := range map[string]string{
		"up":              "up [flags] [service...]",
		"doctor":          "doctor",
		"router status":   "router status",
		"run":             "run [flags] -- <command> [args...]",
		"exec":            "exec [flags] <service> -- <command> [args...]",
		"worktree remove": "worktree remove [flags] <name|path>",
	} {
		if uses[path] != use {
			t.Errorf("%s: %q", path, uses[path])
		}
	}
	for _, path := range []string{"tui", "mcp", "help", "router", "worktree"} {
		if _, ok := uses[path]; ok {
			t.Errorf("offers %s", path)
		}
	}
	executable = func() (string, error) { return "", errors.New("no binary") }
	if code := tty(); code != ExitError {
		t.Errorf("no binary: %d", code)
	}
}
