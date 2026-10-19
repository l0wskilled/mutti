package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/router"
)

// nestedHarness runs mutti "inside" container "self", which has the parent of root mounted from
// /host<parent>.
func nestedHarness(t *testing.T, root string, m *compose.Model) *harness {
	t.Helper()
	h := newHarness(t, m)
	parent := filepath.Dir(root)
	id := h.eng.AddContainer(engine.Container{Name: "self", State: "running"})
	h.eng.SetMounts(id, []engine.Mount{{Source: "/host" + parent, Target: parent}})
	h.app.Host.ContainerID = id
	return h
}

func bindModel(root string) *compose.Model {
	return &compose.Model{Services: map[string]compose.Service{
		"web": {
			Ports: []compose.Port{{Target: 80}},
			Volumes: []compose.VolumeMount{
				{Type: "bind", Source: filepath.Join(root, "www"), Target: "/www", ReadOnly: true, Bind: &compose.BindOpts{CreateHostPath: true, Propagation: "rprivate", SELinux: "z"}},
				{Type: "volume", Source: "data", Target: "/data"},
			},
		},
	}}
}

func TestNestedUpTranslatesPaths(t *testing.T) {
	root := newRepo(t, "shop")
	h := nestedHarness(t, root, bindModel(root))
	res, err := h.app.Up(ctx, root, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance.HostPath != "/host"+root {
		t.Fatalf("host path %q", res.Instance.HostPath)
	}
	override, _ := os.ReadFile(filepath.Join(root, ".mutti", "local", "compose.yml"))
	for _, want := range []string{
		`source: "/host` + root + `/www"`, `target: "/www"`, "read_only: true",
		"create_host_path: true", `propagation: "rprivate"`, `selinux: "z"`,
	} {
		if !strings.Contains(string(override), want) {
			t.Errorf("override lacks %s:\n%s", want, override)
		}
	}
	if strings.Contains(string(override), `"data"`) {
		t.Error("named volumes must not be translated")
	}
	// The readiness probe ran as a helper container on the instance's network.
	runs := h.eng.Runs()
	if len(runs) == 0 || runs[0].Image != HelperImage || runs[0].Networks[0] != "mutti-shop" || !strings.HasPrefix(runs[0].Cmd[len(runs[0].Cmd)-1], "http://shop.localhost/.well-known/mutti/") {
		t.Fatalf("probe runs %+v", runs)
	}
	env := h.app.Env(res.Instance)
	if !slicesContains(env, "MUTTI_HOST_PATH=/host"+root) {
		t.Errorf("env %v", env)
	}
}

func TestNestedKeepsBindsWrittenWithTheHostPath(t *testing.T) {
	root := newRepo(t, "shop")
	m := bindModel(root)
	m.Services["web"] = compose.Service{Volumes: []compose.VolumeMount{{Type: "bind", Source: "/host" + root + "/src", Target: "/app"}}}
	h := nestedHarness(t, root, m)
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	override, _ := os.ReadFile(filepath.Join(root, ".mutti", "local", "compose.yml"))
	if !strings.Contains(string(override), `source: "/host`+root+`/src"`) {
		t.Errorf("override:\n%s", override)
	}
	if underPath("/hostx", "/host") || !underPath("/host", "/host") || underPath("/a", "") {
		t.Error("underPath")
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestNestedProbeRetriesAndFails(t *testing.T) {
	root := newRepo(t, "shop")
	h := nestedHarness(t, root, webModel())
	h.eng.RunExit = 1
	h.app.ReadyTimeout = 300e6
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeRouterFailed {
		t.Fatalf("probe never succeeds: %v", err)
	}
	h = nestedHarness(t, root, webModel())
	h.eng.AddImage(router.Image) // only the helper image is missing
	h.eng.FailOn = map[string]error{"PullImage": engine.ErrUnavailable}
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatalf("helper image: %v", err)
	}
}

func TestNestedPathProblems(t *testing.T) {
	root := newRepo(t, "shop")

	// A bind source outside every mount cannot work.
	m := bindModel(root)
	m.Services["web"] = compose.Service{Volumes: []compose.VolumeMount{{Type: "bind", Source: "/only/in/here", Target: "/x"}}}
	h := nestedHarness(t, root, m)
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeUnmappable {
		t.Errorf("unmappable bind: %v", err)
	}

	// The worktree itself is not mounted.
	h = newHarness(t, webModel())
	id := h.eng.AddContainer(engine.Container{Name: "self"})
	h.eng.SetMounts(id, []engine.Mount{{Source: "/x", Target: "/elsewhere"}})
	h.app.Host.ContainerID = id
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeUnmappable {
		t.Errorf("unmounted worktree: %v", err)
	}

	// The daemon does not know the container (Docker-in-Docker): MUTTI_HOST_ROOT helps.
	h.app.Host.ContainerID = "unknown"
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeSelfUnknown {
		t.Errorf("unknown container: %v", err)
	}
	h.app.Host.Getenv = env{"MUTTI_HOST_ROOT": "/srv/build/shop"}.get
	if inst, err := h.app.Resolve(ctx, root); err != nil || inst.HostPath != "/srv/build/shop" {
		t.Errorf("MUTTI_HOST_ROOT in a container: %+v %v", inst, err)
	}
	h.eng.FailOn = map[string]error{"InspectContainer": engine.ErrUnavailable}
	if _, err := h.app.Resolve(ctx, root); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("inspect: %v", err)
	}

	// On the host, MUTTI_HOST_ROOT alone renames the root.
	h = newHarness(t, webModel())
	h.app.Host.Getenv = env{"MUTTI_HOST_ROOT": "/mnt/shop"}.get
	if inst, err := h.app.Resolve(ctx, root); err != nil || inst.HostPath != "/mnt/shop" {
		t.Errorf("MUTTI_HOST_ROOT on the host: %+v %v", inst, err)
	}

	// MUTTI_HOST_PATH is what mutti hands to commands; inherited by a nested mutti, it means nothing.
	h.app.Host.Getenv = env{"MUTTI_HOST_PATH": "/elsewhere"}.get
	if inst, err := h.app.Resolve(ctx, root); err != nil || inst.HostPath != root {
		t.Errorf("inherited MUTTI_HOST_PATH: %+v %v", inst, err)
	}
}

func TestNestedFixedPortsTrustDocker(t *testing.T) {
	root := newRepo(t, "shop")
	h := nestedHarness(t, root, dbModel())
	h.app.Host.PortFree = func(uint16) bool { return false } // the container's own loopback
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestAttach(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Attach(ctx, root, false); appErrCode(err) != CodeNotNested {
		t.Fatalf("on the host: %v", err)
	}

	h = nestedHarness(t, root, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // twice: attaching is idempotent
		if _, err := h.app.Attach(ctx, root, false); err != nil {
			t.Fatal(err)
		}
	}
	self, _, _ := h.eng.InspectContainer(ctx, "self")
	if _, ok := self.Networks["mutti-shop"]; !ok {
		t.Fatalf("not attached: %v", self.Networks)
	}
	// Destroy detaches it, or removing the network would fail.
	if _, err := h.app.Destroy(ctx, root); err != nil {
		t.Fatal(err)
	}
	self, _, _ = h.eng.InspectContainer(ctx, "self")
	if _, ok := self.Networks["mutti-shop"]; ok {
		t.Fatal("still attached after destroy")
	}

	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Attach(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := h.app.Attach(ctx, root, true); err != nil {
			t.Fatal(err)
		}
	}
	self, _, _ = h.eng.InspectContainer(ctx, "self")
	if _, ok := self.Networks["mutti-shop"]; ok {
		t.Fatal("still attached after detach")
	}

	if _, err := h.app.Attach(ctx, t.TempDir(), false); err == nil {
		t.Error("no project: want error")
	}
	h.eng.FailOn = map[string]error{"ConnectNetwork": engine.ErrUnavailable}
	if _, err := h.app.Attach(ctx, root, false); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("connect: %v", err)
	}
	inspects := 0
	h.eng.FailOn = nil
	h.eng.OnCall = func(m string) error {
		if m == "InspectContainer" {
			if inspects++; inspects == 2 { // 1: path map in resolve
				return engine.ErrUnavailable
			}
		}
		return nil
	}
	if _, err := h.app.Attach(ctx, root, false); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("inspect self: %v", err)
	}
	h.eng.OnCall = nil

	other := newRepo(t, "shop") // same name, other clone: not ours
	h2 := nestedHarness(t, other, webModel())
	h2.eng = h.eng
	h2.app.Engine = h.eng
	h2.app.Host.ContainerID = h.app.Host.ContainerID
	h.eng.SetMounts(h.app.Host.ContainerID, []engine.Mount{{Source: "/", Target: "/"}})
	if _, err := h2.app.Attach(ctx, other, false); appErrCode(err) != CodeNameTaken {
		t.Errorf("foreign instance: %v", err)
	}
}

func TestDetachAllFailures(t *testing.T) {
	h := newHarness(t, webModel())
	h.eng.FailOn = map[string]error{"NetworkContainers": engine.ErrUnavailable}
	if err := h.app.detachAll(ctx, "shop"); appErrCode(err) != CodeDockerUnavailable {
		t.Errorf("members: %v", err)
	}
	h.eng.FailOn = map[string]error{"DisconnectNetwork": errors.New("boom")}
	if err := h.app.detachAll(ctx, "shop"); err == nil {
		t.Error("disconnect: want error")
	}
}

func TestDoctorInContainer(t *testing.T) {
	root := newRepo(t, "shop")
	check := func(h *harness) Check {
		r := &Report{}
		h.app.checkContainer(ctx, r)
		return r.Checks[0]
	}
	h := nestedHarness(t, root, webModel())
	if c := check(h); c.Status != CheckOK {
		t.Errorf("known: %+v", c)
	}
	h.app.Host.ContainerID = "unknown"
	if c := check(h); c.Status != CheckWarn || !strings.Contains(c.Hint, "MUTTI_HOST_ROOT") {
		t.Errorf("unknown: %+v", c)
	}
	r := &Report{}
	newHarness(t, webModel()).app.checkContainer(ctx, r)
	if len(r.Checks) != 0 {
		t.Errorf("on the host: %+v", r.Checks)
	}
}
