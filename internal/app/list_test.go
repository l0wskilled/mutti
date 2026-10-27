package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/project"
)

func TestListSummarizes(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{
		"web": {Ports: []compose.Port{{Target: 80}, {Target: 53, Protocol: "udp"}}},
		"api": {Ports: []compose.Port{{Target: 8080}}},
	}})
	for _, dir := range []string{root, wt} {
		if _, err := h.app.Up(ctx, dir, UpOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Some unrelated Compose project without a claim must not show up.
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: "foreign"}})

	list, err := h.app.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Summary{
		{
			Name: "shop", Project: "shop", Path: root, Branch: "main", Status: StatusRunning, Running: 2, Total: 2,
			URLs: []string{"https://shop.localhost", "https://api.shop.localhost"},
			Ports: []PortSummary{
				{Service: "api", Address: "127.0.0.1:40001", ContainerPort: 8080, Routed: true},
				{Service: "web", Address: "127.0.0.1:40003/udp", ContainerPort: 53},
				{Service: "web", Address: "127.0.0.1:40002", ContainerPort: 80, Routed: true},
			},
		},
		{
			Name: "shop-feature", Project: "shop", Path: wt, Branch: "shop-feature", Status: StatusRunning, Running: 2, Total: 2,
			URLs: []string{"https://feature.shop.localhost", "https://api.feature.shop.localhost"},
			Ports: []PortSummary{
				{Service: "api", Address: "127.0.0.1:40004", ContainerPort: 8080, Routed: true},
				{Service: "web", Address: "127.0.0.1:40006/udp", ContainerPort: 53},
				{Service: "web", Address: "127.0.0.1:40005", ContainerPort: 80, Routed: true},
			},
		},
	}
	if diff := cmp.Diff(want, list); diff != "" {
		t.Fatalf("list (-want +got):\n%s", diff)
	}

	// One container down: partial.
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop")
	h.eng.SetContainerState(cs[0].ID, "exited")
	if s := summaryOf(t, h, "shop"); s.Status != StatusPartial || s.Running != 1 {
		t.Fatalf("partial: %+v", s)
	}

	// Without the main service's container the main hostname still comes first.
	for _, c := range cs {
		if c.Labels[engine.LabelComposeService] == "web" {
			if err := h.eng.RemoveContainer(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if s := summaryOf(t, h, "shop"); !slices.Equal(s.URLs, []string{"https://shop.localhost", "https://api.shop.localhost"}) {
		t.Fatalf("without web: %v", s.URLs)
	}
}

type noBranchGit struct{ gitdir.Git }

func (noBranchGit) Branch(context.Context, string) (string, error) { return "", errors.New("detached") }

func TestListWithoutBranch(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.app.Git = noBranchGit{}
	if s := summaryOf(t, h, "shop"); s.Branch != "" || s.Orphaned {
		t.Fatalf("%+v", s)
	}
}

func TestListErrors(t *testing.T) {
	for _, method := range []string{"ListVolumes", "ListContainers"} {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
		if _, err := h.app.List(ctx); appErrCode(err) != CodeDockerUnavailable {
			t.Errorf("%s: %v", method, err)
		}
		if _, err := h.app.Tidy(ctx, TidyOptions{}); appErrCode(err) != CodeDockerUnavailable {
			t.Errorf("tidy %s: %v", method, err)
		}
	}
}

func TestTidy(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-gone")
	h := newHarness(t, webModel())
	for _, dir := range []string{root, wt} {
		if _, err := h.app.Up(ctx, dir, UpOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	dry, err := h.app.Tidy(ctx, TidyOptions{})
	if err != nil || len(dry.Removed) != 1 || dry.Removed[0].Name != "shop-gone" || !dry.Removed[0].Orphaned {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if s := summaryOf(t, h, "shop-gone"); s.Status != StatusRunning {
		t.Fatal("dry run removed something")
	}

	if other, err := h.app.Tidy(ctx, TidyOptions{Project: "other-project", Apply: true}); err != nil || len(other.Removed) != 0 {
		t.Fatalf("another project's tidy touched this one: %+v %v", other, err)
	}
	removed, err := h.app.Tidy(ctx, TidyOptions{Apply: true})
	if err != nil || len(removed.Removed) != 1 {
		t.Fatalf("apply: %+v %v", removed, err)
	}
	list, _ := h.app.List(ctx)
	if len(list) != 1 || list[0].Name != "shop" {
		t.Fatalf("after tidy: %+v", list)
	}
	if cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop-gone"); len(cs) != 0 {
		t.Fatal("containers of the orphan left")
	}
}

func TestTidyStopsOnPurgeError(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-gone")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"RemoveContainer": engine.ErrUnavailable}
	if _, err := h.app.Tidy(ctx, TidyOptions{Apply: true}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
}

func TestRename(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())

	if _, err := h.app.Rename(ctx, root, "Bad Name"); appErrCode(err) != CodeInvalidName {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := h.app.Rename(ctx, t.TempDir(), "ok"); appErrCode(err) != CodeNoComposeFile {
		t.Fatalf("no project: %v", err)
	}

	// Never claimed: just takes the new name.
	inst, err := h.app.Rename(ctx, root, "first")
	if err != nil || inst.Name != "shop-first" || inst.Hostname != "first.shop.localhost" {
		t.Fatalf("inst=%+v err=%v", inst, err)
	}

	// Claimed and running: refused.
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Rename(ctx, root, "second"); appErrCode(err) != CodeInstanceInUse {
		t.Fatalf("in use: %v", err)
	}

	// Destroyed: renamed, old claim gone.
	if _, err := h.app.Destroy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Destroy(ctx, root); err != nil {
		t.Fatal(err)
	}
	// Destroy removed the claim too; claim it again without containers to test the hand-over.
	inst, err = h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.claim(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Rename(ctx, root, "second"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.eng.InspectVolume(ctx, claimVolume("shop-first")); ok {
		t.Fatal("old claim left")
	}
	claimLabels(t, h.eng, "shop-second")
}

func TestRenameFailures(t *testing.T) {
	boom := errors.New("boom")
	setup := func(t *testing.T) (*harness, string) {
		root := newRepo(t, "shop")
		h := newHarness(t, webModel())
		inst, err := h.app.Resolve(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.app.claim(ctx, inst); err != nil {
			t.Fatal(err)
		}
		return h, root
	}
	for _, method := range []string{"InspectVolume", "ListContainers", "ListVolumes"} {
		t.Run(method, func(t *testing.T) {
			h, root := setup(t)
			h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
			if _, err := h.app.Rename(ctx, root, "x"); appErrCode(err) != CodeDockerUnavailable {
				t.Fatal(err)
			}
		})
	}
	t.Run("new name taken", func(t *testing.T) {
		h, root := setup(t)
		_, _ = h.eng.CreateVolume(ctx, claimVolume("shop-x"), map[string]string{LabelOrigin: "host", engine.LabelID: "someone", engine.LabelPath: "/gone"})
		if _, err := h.app.Rename(ctx, root, "x"); appErrCode(err) != CodeStaleClaim {
			t.Fatal(err)
		}
	})
	t.Run("old claim not removable", func(t *testing.T) {
		h, root := setup(t)
		h.eng.FailOn = map[string]error{"RemoveVolume": boom}
		if _, err := h.app.Rename(ctx, root, "x"); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
}

func TestDoctorHealthy(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	r := h.app.Doctor(ctx, root)
	if r.Failed() {
		t.Fatalf("failed: %+v", r.Checks)
	}
	names := []string{}
	for _, c := range r.Checks {
		names = append(names, c.Name+"="+c.Status)
	}
	if got := strings.Join(names, " "); got != "docker=ok compose=ok buildx=ok git=ok router=ok https=warn project=ok" {
		t.Fatalf("checks %s", got)
	}
	if len(r.ComposeFeatures) != len(compose.Features) {
		t.Fatalf("features %+v", r.ComposeFeatures)
	}
	if !strings.Contains(r.Checks[6].Detail, "shop (shop.localhost), 2 services") {
		t.Errorf("project detail %q", r.Checks[6].Detail)
	}
}

func TestDoctorProblems(t *testing.T) {
	h := newHarness(t, webModel())
	h.eng.FailOn = map[string]error{"Info": engine.ErrUnavailable}
	h.compose.VersionErr = compose.ErrNotInstalled
	h.compose.BuildxErr = errors.New("no buildx")
	h.app.Host.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	r := h.app.Doctor(ctx, t.TempDir())
	want := map[string]string{"docker": CheckFail, "compose": CheckFail, "buildx": CheckWarn, "git": CheckWarn, "router": CheckWarn, "https": CheckWarn, "project": CheckWarn}
	for _, c := range r.Checks {
		if want[c.Name] != c.Status {
			t.Errorf("%s = %s, want %s", c.Name, c.Status, want[c.Name])
		}
	}
	if !r.Failed() || r.ComposeFeatures != nil {
		t.Fatalf("report %+v", r)
	}
}

func TestDoctorRootlessRemoteOldComposeBrokenProject(t *testing.T) {
	root := newRepo(t, "shop")
	m := webModel()
	m.XMutti = []byte(`{"typo":1}`)
	h := newHarness(t, m)
	h.eng.SetInfo(engine.Info{ServerVersion: "29", OS: "Linux", Rootless: true, Host: "tcp://build.example.com:2376"})
	h.compose.Ver = compose.MustParseVersion("2.20.0")
	r := h.app.Doctor(ctx, root)
	got := map[string]Check{}
	for _, c := range r.Checks {
		got[c.Name] = c
	}
	if !strings.HasSuffix(got["docker"].Detail, ", rootless") || got["docker host"].Status != CheckWarn ||
		got["compose"].Status != CheckFail || got["project"].Status != CheckFail {
		t.Fatalf("checks %+v", r.Checks)
	}
	if r.ComposeFeatures[3].Available {
		t.Error("2.20.0 has no !override")
	}
}

func TestRemoteHost(t *testing.T) {
	tests := map[string]string{
		"unix:///var/run/docker.sock":  "",
		"npipe:////./pipe/docker":      "",
		"tcp://localhost:2375":         "",
		"tcp://127.0.0.1:2375":         "",
		"tcp://[::1]:2375":             "",
		"tcp://build.example.com:2376": "build.example.com:2376",
		"ssh://me@devbox":              "devbox",
		"::not a url":                  "",
	}
	for addr, want := range tests {
		if got := remoteHost(addr); got != want {
			t.Errorf("remoteHost(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestTidyStopsIdleInstances(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-busy")
	h := newHarness(t, webModel())
	for _, dir := range []string{root, wt} {
		if _, err := h.app.Up(ctx, dir, UpOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Both were just used; the git files of the repo are older. Make "shop" look unused for two
	// days: its last-used file and its git files lie in the past, while the clock moves on.
	old := testNow.Add(-48 * time.Hour)
	for _, p := range []string{h.app.lastUsedFile("shop"), filepath.Join(root, ".git", "index"), filepath.Join(root, ".git", "HEAD"), filepath.Join(root, ".git", "logs", "HEAD")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	busyGit := filepath.Join(root, ".git", "worktrees", "shop-busy")
	busyFiles := []string{filepath.Join(busyGit, "index"), filepath.Join(busyGit, "HEAD"), filepath.Join(busyGit, "logs", "HEAD")}
	for _, p := range busyFiles {
		if err := os.Chtimes(p, testNow, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(h.app.lastUsedFile("shop-busy"), old, old); err != nil {
		t.Fatal(err)
	}

	dry, err := h.app.Tidy(ctx, TidyOptions{Idle: 24 * time.Hour})
	if err != nil || len(dry.Stopped) != 1 || dry.Stopped[0].Name != "shop" {
		t.Fatalf("dry: %+v %v", dry, err)
	}
	res, err := h.app.Tidy(ctx, TidyOptions{Idle: 24 * time.Hour, Apply: true})
	if err != nil || len(res.Stopped) != 1 {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if s := summaryOf(t, h, "shop"); s.Status != StatusStopped {
		t.Fatalf("not stopped: %+v", s)
	}
	if s := summaryOf(t, h, "shop-busy"); s.Status != StatusRunning {
		t.Fatalf("a worktree with git activity was stopped: %+v", s)
	}

	// A stop that fails is reported.
	for _, p := range append(busyFiles, h.app.lastUsedFile("shop-busy")) {
		_ = os.Chtimes(p, old, old)
	}
	h.compose.Exit = func(args []string) int { return map[string]int{"stop": 1}[args[0]] }
	h.app.Now = func() time.Time { return testNow.Add(72 * time.Hour) }
	if _, err := h.app.Tidy(ctx, TidyOptions{Idle: 24 * time.Hour, Apply: true}); appErrCode(err) != CodeComposeFailed {
		t.Fatalf("failing stop: %v", err)
	}
}

func TestTouchIgnoresUnwritableState(t *testing.T) {
	h := newHarness(t, webModel())
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	h.app.Dirs.State = file
	h.app.touch("shop") // must not panic or fail anything
}

func TestLookingIsNotUsing(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	old := testNow.Add(-48 * time.Hour)
	file := h.app.lastUsedFile("shop")
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	used := func() bool {
		fi, err := os.Stat(file)
		return err == nil && fi.ModTime().After(old.Add(time.Hour))
	}
	for name, look := range map[string]func() error{
		"status": func() error { _, err := h.app.Status(ctx, root); return err },
		"url":    func() error { _, err := h.app.URL(ctx, root, ""); return err },
		"env":    func() error { _, err := h.app.EnvOf(ctx, root); return err },
		"list":   func() error { _, err := h.app.List(ctx); return err },
	} {
		if err := look(); err != nil || used() {
			t.Errorf("%s counted as use (err %v)", name, err)
		}
	}
	if _, err := h.app.Exec(ctx, root, "web", []string{"true"}, ExecOptions{}); err != nil || !used() {
		t.Errorf("exec did not count as use (err %v)", err)
	}
}

func TestOneShotsThatDidTheirJobDoNotCount(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	dependsOn := map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "app", labelDependsOn: "deps:service_completed_successfully:false,db:service_healthy:false"}
	h.eng.AddContainer(engine.Container{State: "running", Labels: dependsOn})
	h.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (0) 2 minutes ago", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "deps"}})
	s := summaryOf(t, h, "shop")
	if s.Status != StatusRunning || s.Running != s.Total {
		t.Fatalf("a finished one-shot counted: %+v", s)
	}
	if p := h.app.Prompt(ctx, root); !strings.HasSuffix(p, fmt.Sprintf("%d/%d", s.Running, s.Total)) {
		t.Errorf("prompt %q", p)
	}

	// Failed, it counts; and a stopped service is not a one-shot, whatever its exit code.
	h.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (1) 1 minute ago", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "deps"}})
	h.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (0) 1 minute ago", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "db"}})
	if s := summaryOf(t, h, "shop"); s.Total-s.Running != 2 {
		t.Fatalf("failed one-shot and stopped db: %+v", s)
	}
}

func TestLegacyClaimsGetTheirOriginAndShowTheirBranch(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	// A claim from before claims carried their origin: unknown to tidy, still with its branch.
	labels := claimLabels(t, h.eng, "shop")
	delete(labels, LabelOrigin)
	_ = h.eng.RemoveVolume(ctx, claimVolume("shop"))
	if _, err := h.eng.CreateVolume(ctx, claimVolume("shop"), labels); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, h, "shop"); s.Branch != "main" || s.Orphaned {
		t.Fatalf("legacy claim: %+v", s)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if claimLabels(t, h.eng, "shop")[LabelOrigin] != "host" {
		t.Error("up did not write the origin")
	}
}

func TestOptionalServicesCountOnlyWhileTheyRun(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{"web": {}, "tool": {Profiles: []string{"tools"}}}})
	if _, err := h.app.Up(ctx, root, UpOptions{Selection: Selection{Profiles: []string{"tools"}}}); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, h, "shop"); s.Running != 2 || s.Total != 2 {
		t.Fatalf("tool running: %+v", s)
	}
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=tool")
	if cs[0].Labels[LabelOptional] != "true" {
		t.Fatalf("tool labels %v", cs[0].Labels)
	}
	h.eng.SetContainerState(cs[0].ID, "exited")
	if s := summaryOf(t, h, "shop"); s.Status != StatusRunning || s.Running != 1 || s.Total != 1 {
		t.Fatalf("tool stopped: %+v", s)
	}
}

func TestRemoveNeedsNoWorktree(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Remove(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.app.List(ctx); len(list) != 0 {
		t.Errorf("left: %+v", list)
	}
	if err := h.app.Remove(ctx, "../x"); err == nil {
		t.Error("an invalid name")
	}
}

func TestListPointsAtTheProjectInASubdirectory(t *testing.T) {
	root := newRepo(t, "shop")
	sub := filepath.Join(root, "deploy", "local")
	writeFile(t, filepath.Join(sub, "compose.yml"), "services: {}\n")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, sub, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	list, err := h.app.List(ctx)
	if err != nil || len(list) != 1 || list[0].Path != sub {
		t.Fatalf("%+v %v", list, err)
	}
	// What the list says works for everything that takes a directory.
	if _, err := h.app.Status(ctx, list[0].Path); err != nil {
		t.Fatal(err)
	}
	// A gone worktree points at where it was.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.app.List(ctx); list[0].Path != root || !list[0].Orphaned {
		t.Errorf("gone: %+v", list[0])
	}
}

func TestProjectDir(t *testing.T) {
	for _, c := range []struct{ root, dir, want string }{
		{"/r", "/r", "."},
		{"/r", "/r/a/b", "a/b"},
		{"/r", "/elsewhere", "."},
	} {
		if got := projectDir(&Instance{Root: c.root, Files: project.Files{Dir: c.dir}}); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}
