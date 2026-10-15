package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
)

func routedModel() *compose.Model {
	m := &compose.Model{Services: map[string]compose.Service{
		"web":  {Ports: []compose.Port{{Target: 80}}},
		"api":  {Ports: []compose.Port{{Target: 9003}, {Target: 8000}}},
		"blog": {Ports: []compose.Port{{Target: 8080}}},
		"db":   {Ports: []compose.Port{{Target: 5432}}},
	}}
	m.XMutti = []byte(`{"main":"web","routes":{"api":{"path":"/api","port":8000},"blog":{"host":""}}}`)
	return m
}

func TestURL(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, routedModel())
	for service, want := range map[string]string{
		"":     "https://shop.localhost",
		"web":  "https://shop.localhost",
		"api":  "https://api.shop.localhost",
		"blog": "https://shop.localhost",
	} {
		if got, err := h.app.URL(ctx, root, service); err != nil || got != want {
			t.Errorf("%q: %q %v", service, got, err)
		}
	}
	if _, err := h.app.URL(ctx, root, "db"); appErrCode(err) != CodeNoRoute {
		t.Errorf("db: %v", err)
	}
	if _, err := h.app.URL(ctx, root, "nope"); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("unknown: %v", err)
	}
	if _, err := h.app.URL(ctx, t.TempDir(), ""); appErrCode(err) != CodeNoComposeFile {
		t.Errorf("no project: %v", err)
	}
	h.app.Host.Getenv = env{"MUTTI_HTTPS_PORT": "8443"}.get
	if got, _ := h.app.URL(ctx, root, "api"); got != "https://api.shop.localhost:8443" {
		t.Errorf("custom port: %q", got)
	}
	h.app.Host.Getenv = env{"MUTTI_HTTPS_PORT": "x"}.get
	if _, err := h.app.URL(ctx, root, ""); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("settings: %v", err)
	}

	// A path route on the main hostname points there.
	m := routedModel()
	m.XMutti = []byte(`{"main":"web","routes":{"api":{"host":"","path":"/api"}}}`)
	h = newHarness(t, m)
	if got, _ := h.app.URL(ctx, root, "api"); got != "https://shop.localhost/api" {
		t.Errorf("path route: %q", got)
	}

	h = newHarness(t, &compose.Model{Services: map[string]compose.Service{"worker": {}}})
	if _, err := h.app.URL(ctx, root, ""); appErrCode(err) != CodeNoRoute {
		t.Errorf("nothing routed: %v", err)
	}
}

func TestOpen(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, routedModel())
	var opened []string
	h.app.Host.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	if url, err := h.app.Open(ctx, root, "api"); err != nil || url != "https://api.shop.localhost" || !slices.Equal(opened, []string{url}) {
		t.Fatalf("%q %v %v", url, err, opened)
	}
	if _, err := h.app.Open(ctx, root, "db"); appErrCode(err) != CodeNoRoute {
		t.Errorf("db: %v", err)
	}
	h.app.Host.OpenURL = func(string) error { return errors.New("no display") }
	if _, err := h.app.Open(ctx, root, ""); appErrCode(err) != CodeOpenFailed {
		t.Errorf("no browser: %v", err)
	}
}

func TestShell(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, routedModel())
	if _, err := h.app.Shell(ctx, root, "", ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	last := h.compose.Runs[len(h.compose.Runs)-1]
	if !slices.Equal(last[len(last)-4:], append([]string{"web"}, shellCommand...)) {
		t.Fatalf("args %v", last)
	}
	if _, err := h.app.Shell(ctx, root, "db", ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	// Without a web service the first service is used.
	h = newHarness(t, &compose.Model{Services: map[string]compose.Service{"b": {}, "a": {}}})
	if _, err := h.app.Shell(ctx, root, "", ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if last := h.compose.Runs[len(h.compose.Runs)-1]; !slices.Contains(last, "a") {
		t.Fatalf("args %v", last)
	}

	h = newHarness(t, &compose.Model{Services: map[string]compose.Service{}})
	if _, err := h.app.Shell(ctx, root, "", ExecOptions{}); appErrCode(err) != CodeInvalidConfig {
		t.Errorf("no services: %v", err)
	}
	if _, err := h.app.Shell(ctx, t.TempDir(), "", ExecOptions{}); appErrCode(err) != CodeNoComposeFile {
		t.Errorf("no project: %v", err)
	}
}

func TestStatus(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, routedModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	// A container of a service that is no longer in the configuration is ignored.
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "old"}})
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=db")
	_ = h.eng.RemoveContainer(ctx, cs[0].ID)

	st, err := h.app.Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "shop" || st.Branch != "main" || st.URL != "https://shop.localhost" || len(st.Services) != 4 {
		t.Fatalf("status %+v", st)
	}
	byName := map[string]ServiceStatus{}
	for _, s := range st.Services {
		byName[s.Service] = s
	}
	if byName["db"].State != "missing" || byName["api"].State != "running" || len(byName["api"].Ports) != 2 || byName["api"].Ports[0].ContainerPort != 8000 ||
		!slices.Equal(byName["api"].URLs, []string{"https://api.shop.localhost"}) {
		t.Fatalf("services %+v", st.Services)
	}

	for name, setup := range map[string]func(h *harness){
		"engine":   func(h *harness) { h.eng.FailOn = map[string]error{"ListContainers": engine.ErrUnavailable} },
		"settings": func(h *harness) { h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get },
	} {
		h := newHarness(t, routedModel())
		setup(h)
		if _, err := h.app.Status(ctx, root); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := h.app.Status(ctx, t.TempDir()); err == nil {
		t.Error("no project: want error")
	}
	h.app.Git = noBranchGit{}
	if st, _ := h.app.Status(ctx, root); st.Branch != "" {
		t.Error("branch without git answer")
	}
	h = newHarness(t, &compose.Model{Services: map[string]compose.Service{"db": {Ports: []compose.Port{{Target: 5432}}}}})
	if st, _ := h.app.Status(ctx, root); st.URL != "" {
		t.Error("URL without a main route")
	}
}

func TestEnvOfServicesAndWorktreeNames(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, routedModel())
	vars, err := h.app.EnvOf(ctx, root)
	if err != nil || !slices.Contains(vars, "MUTTI_INSTANCE=shop") {
		t.Fatalf("%v %v", vars, err)
	}
	if _, err := h.app.EnvOf(ctx, t.TempDir()); err == nil {
		t.Error("no project: want error")
	}
	if got := h.app.Services(ctx, root); !slices.Equal(got, []string{"api", "blog", "db", "web"}) {
		t.Errorf("services %v", got)
	}
	if got := h.app.Services(ctx, t.TempDir()); got != nil {
		t.Errorf("no project: %v", got)
	}

	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "one", NoUp: true}); err != nil {
		t.Fatal(err)
	}
	if got := h.app.WorktreeNames(ctx, root); !slices.Equal(got, []string{"one"}) {
		t.Errorf("worktrees %v", got)
	}
	if got := h.app.WorktreeNames(ctx, t.TempDir()); got != nil {
		t.Errorf("outside git: %v", got)
	}
	h.app.Git = worktreeListFails{}
	if got := h.app.WorktreeNames(ctx, root); got != nil {
		t.Errorf("git failure: %v", got)
	}
}

func TestPrompt(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature")
	h := newHarness(t, webModel())
	if got := h.app.Prompt(ctx, root); got != "" {
		t.Errorf("before up: %q", got)
	}
	for _, dir := range []string{root, wt} {
		if _, err := h.app.Up(ctx, dir, UpOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.app.Prompt(ctx, wt); got != "shop-feature running 2/2" {
		t.Errorf("running: %q", got)
	}
	if _, err := h.app.Stop(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Prompt(ctx, root); got != "shop stopped 0/2" {
		t.Errorf("stopped: %q", got)
	}
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Prompt(ctx, root); got != "shop down" {
		t.Errorf("down: %q", got)
	}

	// Outside git the claim is found by path, also from a subdirectory.
	plain := filepath.Join(t.TempDir(), "plain")
	writeFile(t, filepath.Join(plain, "compose.yml"), "")
	writeFile(t, filepath.Join(plain, "sub", "x"), "")
	if _, err := h.app.Up(ctx, plain, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Prompt(ctx, filepath.Join(plain, "sub")); got != "plain running 2/2" {
		t.Errorf("plain: %q", got)
	}

	h.app.Git = brokenGit{}
	if got := h.app.Prompt(ctx, root); got != "" {
		t.Errorf("git failure: %q", got)
	}
	h.app.Git = gitdir.Git{}
	for _, method := range []string{"ListVolumes", "ListContainers"} {
		h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
		if got := h.app.Prompt(ctx, wt); got != "" {
			t.Errorf("%s failure: %q", method, got)
		}
	}
}

func TestStatusListsOptionalServicesLast(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{
		"web": {}, "db": {}, "tool": {Profiles: []string{"tools"}}, "admin": {Profiles: []string{"tools"}},
	}})
	if _, err := h.app.Up(ctx, root, UpOptions{Services: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	st, err := h.app.Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range st.Services {
		got = append(got, s.Service+":"+s.State+":"+strings.Join(s.Profiles, ","))
	}
	if want := []string{"db:missing:", "web:running:", "admin:missing:tools", "tool:missing:tools"}; !slices.Equal(got, want) {
		t.Errorf("services %v, want %v", got, want)
	}
}

func TestStatusTellsCompletedOneShotsFromStoppedServices(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{"web": {}, "deps": {}, "db": {}}})
	if _, err := h.app.Up(ctx, root, UpOptions{Services: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	labels := func(service, dependsOn string) map[string]string {
		return map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: service, labelDependsOn: dependsOn}
	}
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=web")
	_ = h.eng.RemoveContainer(ctx, cs[0].ID)
	h.eng.AddContainer(engine.Container{State: "running", Labels: labels("web", "deps:service_completed_successfully:false")})
	h.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (0) 1 minute ago", Labels: labels("deps", "")})
	h.eng.AddContainer(engine.Container{State: "exited", Status: "Exited (0) 1 minute ago", Labels: labels("db", "")})
	st, err := h.app.Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range st.Services {
		got[s.Service] = s.Completed
	}
	if !got["deps"] || got["db"] || got["web"] {
		t.Errorf("completed %v", got)
	}
}
