package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
	"github.com/l0wskilled/mutti/internal/router"
)

func routerDir(h *harness) string { return filepath.Join(h.app.Dirs.State, "router", defaultRouter) }

// routerConfig is the configuration copied into the router container.
func routerConfig(t *testing.T, h *harness) string {
	t.Helper()
	return string(h.eng.Files(defaultRouter)[router.ConfigFile])
}

func routerDetails(t *testing.T, h *harness) engine.ContainerDetails {
	t.Helper()
	d, ok, err := h.eng.InspectContainer(ctx, defaultRouter)
	if err != nil || !ok {
		t.Fatalf("router: ok=%v err=%v", ok, err)
	}
	return d
}

func TestUpStartsRouterAndRoutes(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-feature")
	h := newHarness(t, webModel())

	res, err := h.app.Up(ctx, root, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://shop.localhost" {
		t.Errorf("url %q", res.URL)
	}
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}

	d := routerDetails(t, h)
	if d.State != "running" || d.Labels[engine.LabelRole] != router.Role {
		t.Fatalf("router %+v", d)
	}
	if got := d.Networks["mutti-shop"]; !slices.Equal(got, []string{"shop.localhost"}) {
		t.Errorf("aliases on mutti-shop: %v", got)
	}
	if got := d.Networks["mutti-shop-feature"]; !slices.Equal(got, []string{"feature.shop.localhost"}) {
		t.Errorf("aliases on mutti-shop-feature: %v", got)
	}
	cfg := routerConfig(t, h)
	for _, want := range []string{"http://shop-web-1:80", "http://shop-feature-web-1:80", "BEGIN CERTIFICATE", "BEGIN EC PRIVATE KEY"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %s:\n%s", want, cfg)
		}
	}

	// Down removes the routes but keeps the router attached; destroy detaches it.
	if _, err := h.app.Down(ctx, root, Selection{}); err != nil {
		t.Fatal(err)
	}
	if cfg := routerConfig(t, h); strings.Contains(cfg, "shop-web-1") || !strings.Contains(cfg, "shop-feature-web-1") {
		t.Errorf("routes after down:\n%s", cfg)
	}
	if _, err := h.app.Destroy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, attached := routerDetails(t, h).Networks["mutti-shop"]; attached {
		t.Error("router still on a destroyed instance's network")
	}
	if nets, _ := h.eng.ListNetworks(ctx, engine.LabelInstance+"=shop"); len(nets) != 0 {
		t.Errorf("ingress network left: %v", nets)
	}
}

func TestUpWithoutRoutesHasNoURL(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, &compose.Model{Services: map[string]compose.Service{"worker": {}}})
	res, err := h.app.Up(ctx, root, UpOptions{})
	if err != nil || res.URL != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestReconcileKeepsCurrentRouterAndFiles(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	before := routerDetails(t, h)
	cert := filepath.Join(routerDir(h), "certs", "shop.pem")
	stat1, _ := os.Stat(cert)

	var calls []string
	h.eng.OnCall = func(m string) error { calls = append(calls, m); return nil }
	if _, err := h.app.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"RemoveContainer", "CreateContainer", "StartContainer", "ConnectNetwork", "DisconnectNetwork", "PullImage", "CopyToContainer"} {
		if slices.Contains(calls, forbidden) {
			t.Errorf("reconcile of an unchanged state called %s", forbidden)
		}
	}
	if routerDetails(t, h).ID != before.ID {
		t.Error("router replaced")
	}
	stat2, _ := os.Stat(cert)
	if !stat2.ModTime().Equal(stat1.ModTime()) {
		t.Error("valid certificate rewritten")
	}
}

func TestReconcileReplacesOutdatedAndStartsStoppedRouter(t *testing.T) {
	h := newHarness(t, webModel())
	s, _ := h.app.routerSettings()
	old := router.Spec(s)
	old.Labels[router.LabelVersion] = "old"
	h.eng.AddImage(router.Image)
	oldID, _ := h.eng.CreateContainer(ctx, old)
	if _, err := h.app.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := routerDetails(t, h)
	if d.ID == oldID || d.Labels[router.LabelVersion] == "old" || d.State != "running" {
		t.Fatalf("not replaced: %+v", d)
	}

	h.eng.SetContainerState(d.ID, "exited")
	if _, err := h.app.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := routerDetails(t, h); got.ID != d.ID || got.State != "running" {
		t.Fatalf("not restarted in place: %+v", got)
	}
}

// racingEngine reports the router as missing on the first inspect and creates it right after,
// as if another mutti won the race between inspect and create.
type racingEngine struct {
	*enginetest.Fake
	spec    engine.ContainerSpec
	raced   bool
	failGet bool // fail the inspect after the lost race
}

func (r *racingEngine) InspectContainer(c context.Context, id string) (engine.ContainerDetails, bool, error) {
	if !r.raced {
		r.raced = true
		if _, err := r.CreateContainer(c, r.spec); err != nil {
			return engine.ContainerDetails{}, false, err
		}
		return engine.ContainerDetails{}, false, nil
	}
	if r.failGet {
		return engine.ContainerDetails{}, false, engine.ErrUnavailable
	}
	return r.Fake.InspectContainer(c, id)
}

func TestReconcileUsesRouterCreatedConcurrently(t *testing.T) {
	for _, failGet := range []bool{false, true} {
		h := newHarness(t, webModel())
		s, _ := h.app.routerSettings()
		h.eng.AddImage(router.Image)
		h.app.Engine = &racingEngine{Fake: h.eng, spec: router.Spec(s), failGet: failGet}
		_, err := h.app.Reconcile(ctx)
		if failGet {
			if appErrCode(err) != CodeDockerUnavailable {
				t.Fatalf("inspect after race: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if routerDetails(t, h).State != "running" {
			t.Fatal("router not started")
		}
	}
}

func TestRouterSettings(t *testing.T) {
	h := newHarness(t, webModel())
	h.app.Host.Getenv = env{"MUTTI_ROUTER_NAME": "r2", "MUTTI_HTTP_PORT": "8080", "MUTTI_HTTPS_PORT": "8443"}.get
	h.app.Host.IPv6 = true
	s, err := h.app.routerSettings()
	if err != nil || s.Name != "r2" || s.HTTPPort != 8080 || s.HTTPSPort != 8443 || !s.IPv6 {
		t.Fatalf("settings %+v %v", s, err)
	}
	for _, bad := range []string{"x", "0", "70000"} {
		h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": bad}.get
		if _, err := h.app.routerSettings(); appErrCode(err) != CodeInvalidConfig {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if hostURL("a.localhost", 8443) != "https://a.localhost:8443" || hostURL("a.localhost", 443) != "https://a.localhost" {
		t.Error("hostURL")
	}
}

func TestDesiredRoutes(t *testing.T) {
	labels := func(service, state string, routed bool) engine.Container {
		l := map[string]string{engine.LabelInstance: "shop", engine.LabelComposeProject: "shop", LabelHostname: "shop.localhost", engine.LabelComposeService: service}
		if routed {
			l[LabelRoutePort] = "80"
			l[LabelRouteHosts] = service + ".shop.localhost"
			l[LabelRoutePaths] = "/" + service
		}
		return engine.Container{Name: "shop-" + service + "-" + state, State: state, Labels: l}
	}
	ingress := []engine.Network{{Name: "mutti-shop", Labels: map[string]string{engine.LabelInstance: "shop"}}}
	got := desiredRoutes([]engine.Container{
		labels("web", "running", true), labels("web", "exited", true), labels("worker", "running", false), labels("mail", "exited", true),
	}, ingress)
	if len(got) != 1 || len(got[0].Routes) != 2 {
		t.Fatalf("got %+v", got)
	}
	mail, web := got[0].Routes[0], got[0].Routes[1]
	if len(mail.Upstreams) != 0 || !slices.Equal(web.Upstreams, []string{"shop-web-running"}) || !slices.Equal(web.Paths, []string{"/web"}) {
		t.Fatalf("routes %+v", got[0].Routes)
	}
	if !routed(got[0]) || routed(router.Instance{Routes: []router.Route{mail}}) {
		t.Error("routed")
	}
}

func TestDesiredRoutesTrustNoForeignLabels(t *testing.T) {
	ingress := []engine.Network{
		{Name: "mutti-shop", Labels: map[string]string{engine.LabelInstance: "shop"}},
		{Name: "mutti-../../x", Labels: map[string]string{engine.LabelInstance: "../../x"}},
		{Name: "not-mutti-blog", Labels: map[string]string{engine.LabelInstance: "blog"}},
	}
	routedAs := func(instance, project, hostname, hosts, paths string) engine.Container {
		return engine.Container{Name: "c", State: "running", Labels: map[string]string{
			engine.LabelInstance: instance, engine.LabelComposeProject: project, LabelHostname: hostname, engine.LabelComposeService: "web",
			LabelRoutePort: "80", LabelRouteHosts: hosts, LabelRoutePaths: paths,
		}}
	}
	for name, c := range map[string]engine.Container{
		"path in the instance name":  routedAs("../../x", "../../x", "x.localhost", "x.localhost", ""),
		"instance without ingress":   routedAs("blog", "blog", "blog.localhost", "blog.localhost", ""),
		"other compose project":      routedAs("shop", "evil", "shop.localhost", "shop.localhost", ""),
		"invalid hostname":           routedAs("shop", "shop", "shop.localhost`)", "shop.localhost", ""),
		"host rule injection":        routedAs("shop", "shop", "shop.localhost", "x`) || PathPrefix(`/", ""),
		"host of another instance":   routedAs("shop", "shop", "shop.localhost", "blog.localhost", ""),
		"host merely ending in main": routedAs("shop", "shop", "shop.localhost", "evilshop.localhost", ""),
		"path rule injection":        routedAs("shop", "shop", "shop.localhost", "", "/a`) || Host(`x"),
	} {
		got := desiredRoutes([]engine.Container{c}, ingress)
		if slices.ContainsFunc(got, func(in router.Instance) bool { return len(in.Routes) > 0 }) {
			t.Errorf("%s: routed %+v", name, got)
		}
	}
	// A second hostname for the same instance is ignored, not mixed in.
	ok := routedAs("shop", "shop", "shop.localhost", "shop.localhost", "/api")
	other := routedAs("shop", "shop", "other.localhost", "other.localhost", "")
	other.Labels[engine.LabelComposeService] = "api"
	got := desiredRoutes([]engine.Container{ok, other}, ingress)
	if len(got) != 1 || got[0].MainHost != "shop.localhost" || len(got[0].Routes) != 1 {
		t.Errorf("mixed hostnames: %+v", got)
	}
}

func TestReconcileFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, tt := range []struct {
		method string
		code   string
	}{
		{"ListContainers", CodeDockerUnavailable},
		{"ListNetworks", CodeDockerUnavailable},
		{"InspectContainer", CodeDockerUnavailable},
		{"ImageExists", CodeDockerUnavailable},
		{"PullImage", CodeDockerUnavailable},
	} {
		t.Run(tt.method, func(t *testing.T) {
			h := newHarness(t, webModel())
			h.eng.FailOn = map[string]error{tt.method: engine.ErrUnavailable}
			if _, err := h.app.Reconcile(ctx); appErrCode(err) != tt.code {
				t.Fatal(err)
			}
		})
	}
	for _, method := range []string{"PullImage", "CreateContainer"} {
		t.Run(method+" fails for other reasons", func(t *testing.T) {
			h := newHarness(t, webModel())
			h.eng.FailOn = map[string]error{method: boom}
			if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeRouterFailed || !errors.Is(err, boom) {
				t.Fatal(err)
			}
		})
	}
	t.Run("start fails", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"StartContainer": errors.New("address already in use")}
		_, err := h.app.Reconcile(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeRouterFailed || !strings.Contains(e.Hint, "MUTTI_HTTP_PORT") {
			t.Fatal(err)
		}
	})
	t.Run("outdated router not removable", func(t *testing.T) {
		h := newHarness(t, webModel())
		s, _ := h.app.routerSettings()
		old := router.Spec(s)
		old.Labels[router.LabelVersion] = "old"
		h.eng.AddImage(router.Image)
		_, _ = h.eng.CreateContainer(ctx, old)
		h.eng.FailOn = map[string]error{"RemoveContainer": engine.ErrUnavailable}
		if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("settings", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.app.Host.Getenv = env{"MUTTI_HTTPS_PORT": "nope"}.get
		if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeInvalidConfig {
			t.Fatal(err)
		}
	})
	t.Run("lock", func(t *testing.T) {
		h := newHarness(t, webModel())
		file := filepath.Join(t.TempDir(), "f")
		writeFile(t, file, "")
		h.app.Dirs.State = file
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("router dir", func(t *testing.T) {
		h := newHarness(t, webModel())
		writeFile(t, routerDir(h), "a file where a dir belongs")
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("copy", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"CopyToContainer": engine.ErrUnavailable}
		if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("copy stamp", func(t *testing.T) {
		h := newHarness(t, webModel())
		if err := os.MkdirAll(filepath.Join(routerDir(h), "pushed", "blocker"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestAttachRouterFailures(t *testing.T) {
	root := newRepo(t, "shop")
	t.Run("connect", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"ConnectNetwork": engine.ErrUnavailable}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("reattach", func(t *testing.T) {
		h := newHarness(t, webModel())
		if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
			t.Fatal(err)
		}
		// Detach by hand and attach without aliases: the next reconcile must fix the aliases.
		_ = h.eng.DisconnectNetwork(ctx, "mutti-shop", defaultRouter)
		_ = h.eng.ConnectNetwork(ctx, "mutti-shop", defaultRouter, nil)
		if _, err := h.app.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if got := routerDetails(t, h).Networks["mutti-shop"]; !slices.Equal(got, []string{"shop.localhost"}) {
			t.Fatalf("aliases %v", got)
		}
		_ = h.eng.DisconnectNetwork(ctx, "mutti-shop", defaultRouter)
		_ = h.eng.ConnectNetwork(ctx, "mutti-shop", defaultRouter, nil)
		h.eng.FailOn = map[string]error{"DisconnectNetwork": engine.ErrUnavailable}
		if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("inspect after start", func(t *testing.T) {
		h := newHarness(t, webModel())
		inspects := 0
		h.eng.OnCall = func(m string) error {
			if m == "InspectContainer" {
				if inspects++; inspects == 2 {
					return engine.ErrUnavailable
				}
			}
			return nil
		}
		if _, err := h.app.Reconcile(ctx); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
}

func TestWriteRouterFilesFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	root := newRepo(t, "shop")
	up := func(t *testing.T) *harness {
		h := newHarness(t, webModel())
		if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
			t.Fatal(err)
		}
		return h
	}
	readOnly := func(t *testing.T, dir string) {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}

	t.Run("CA", func(t *testing.T) {
		h := newHarness(t, webModel())
		writeFile(t, filepath.Join(h.app.Dirs.Data, "ca"), "a file where a dir belongs")
		if _, err := h.app.Up(ctx, root, UpOptions{}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("certs dir", func(t *testing.T) {
		h := up(t)
		_ = os.Remove(filepath.Join(routerDir(h), "certs", "shop.pem"))
		readOnly(t, filepath.Join(routerDir(h), "certs"))
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("key", func(t *testing.T) {
		h := up(t)
		_ = os.Remove(filepath.Join(routerDir(h), "certs", "shop.pem"))
		_ = os.Remove(filepath.Join(routerDir(h), "certs", "shop-key.pem"))
		if err := os.Mkdir(filepath.Join(routerDir(h), "certs", "shop-key.pem"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("cert", func(t *testing.T) {
		h := up(t)
		cert := filepath.Join(routerDir(h), "certs", "shop.pem")
		_ = os.Remove(cert)
		writeFile(t, filepath.Join(cert, "blocker"), "")
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("stale certificate not removable", func(t *testing.T) {
		h := up(t)
		writeFile(t, filepath.Join(routerDir(h), "certs", "gone.pem", "blocker"), "")
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("certs dir not readable", func(t *testing.T) {
		h := up(t)
		if err := os.Chmod(filepath.Join(routerDir(h), "certs"), 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(routerDir(h), "certs"), 0o700) })
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("mismatched CA", func(t *testing.T) {
		h := up(t)
		other := newHarness(t, webModel())
		if _, err := other.app.Up(ctx, root, UpOptions{}); err != nil {
			t.Fatal(err)
		}
		otherKey, _ := os.ReadFile(filepath.Join(other.app.Dirs.Data, "ca", "ca-key.pem"))
		writeFile(t, filepath.Join(h.app.Dirs.Data, "ca", "ca-key.pem"), string(otherKey))
		_ = os.Remove(filepath.Join(routerDir(h), "certs", "shop.pem"))
		if _, err := h.app.Reconcile(ctx); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := writeIfChanged(p, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := writeIfChanged(p, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "g.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeIfChanged(filepath.Join(dir, "g"), []byte("a")); err == nil {
		t.Error("temp file blocked: want error")
	}
	sub := filepath.Join(dir, "sub")
	writeFile(t, filepath.Join(sub, "x"), "")
	if err := writeIfChanged(sub, []byte("a")); err == nil {
		t.Error("target is a directory: want error")
	}
	if _, err := os.Stat(sub + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestRouterRestart(t *testing.T) {
	h := newHarness(t, webModel())
	if _, err := h.app.RouterRestart(ctx); err != nil {
		t.Fatal(err)
	}
	first := routerDetails(t, h).ID
	if _, err := h.app.RouterRestart(ctx); err != nil {
		t.Fatal(err)
	}
	if routerDetails(t, h).ID == first {
		t.Fatal("not recreated")
	}
	h.eng.FailOn = map[string]error{"RemoveContainer": engine.ErrUnavailable}
	if _, err := h.app.RouterRestart(ctx); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"InspectContainer": engine.ErrUnavailable}
	if _, err := h.app.RouterRestart(ctx); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
	h.eng.FailOn = nil
	h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get
	if _, err := h.app.RouterRestart(ctx); appErrCode(err) != CodeInvalidConfig {
		t.Fatal(err)
	}
}

func TestRefreshRouterOnlyWarns(t *testing.T) {
	root := newRepo(t, "shop")

	t.Run("no router yet", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.app.refreshRouter(ctx)
		if _, ok, _ := h.eng.InspectContainer(ctx, defaultRouter); ok {
			t.Fatal("refresh must not start a router")
		}
	})
	for name, setup := range map[string]func(h *harness){
		"bad settings":  func(h *harness) { h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get },
		"inspect fails": func(h *harness) { h.eng.FailOn = map[string]error{"InspectContainer": engine.ErrUnavailable} },
		"reconcile fails": func(h *harness) {
			h.eng.FailOn = map[string]error{"ListNetworks": engine.ErrUnavailable}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, webModel())
			if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			setup(h)
			h.app.refreshRouter(ctx)
			if !strings.Contains(h.stderr.String(), "could not update the router") {
				t.Fatalf("stderr %q", h.stderr.String())
			}
		})
	}
}

func TestPurgeDetachFailures(t *testing.T) {
	root := newRepo(t, "shop")
	t.Run("detach", func(t *testing.T) {
		h := newHarness(t, webModel())
		if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
			t.Fatal(err)
		}
		h.eng.FailOn = map[string]error{"DisconnectNetwork": engine.ErrUnavailable}
		if _, err := h.app.Destroy(ctx, root); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
	t.Run("settings", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get
		if err := h.app.purge(ctx, "shop"); appErrCode(err) != CodeInvalidConfig {
			t.Fatal(err)
		}
		if _, err := h.app.List(ctx); appErrCode(err) != CodeInvalidConfig {
			t.Fatal(err)
		}
	})
	t.Run("list ingress", func(t *testing.T) {
		h := newHarness(t, webModel())
		lists := 0
		h.eng.OnCall = func(m string) error {
			if m == "ListNetworks" {
				if lists++; lists == 2 {
					return engine.ErrUnavailable
				}
			}
			return nil
		}
		if err := h.app.purge(ctx, "shop"); appErrCode(err) != CodeDockerUnavailable {
			t.Fatal(err)
		}
	})
}

func TestEnsureIngressFailure(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	h.eng.FailOn = map[string]error{"CreateNetwork": engine.ErrUnavailable}
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeDockerUnavailable {
		t.Fatal(err)
	}
}

func TestDoctorRouterStates(t *testing.T) {
	check := func(h *harness) Check {
		r := &Report{}
		h.app.checkRouter(ctx, r)
		return r.Checks[0]
	}
	h := newHarness(t, webModel())
	if c := check(h); c.Status != CheckWarn || c.Detail != "not created yet" {
		t.Errorf("missing: %+v", c)
	}
	if _, err := h.app.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c := check(h); c.Status != CheckOK {
		t.Errorf("running: %+v", c)
	}
	h.eng.SetContainerState(routerDetails(t, h).ID, "exited")
	if c := check(h); c.Status != CheckWarn || !strings.HasSuffix(c.Detail, "exited") {
		t.Errorf("stopped: %+v", c)
	}
	h.app.Host.IPv6 = true // changes the spec, so the running router is outdated
	if c := check(h); c.Status != CheckWarn || !strings.HasSuffix(c.Detail, "outdated") {
		t.Errorf("outdated: %+v", c)
	}
	h.eng.FailOn = map[string]error{"InspectContainer": errors.New("boom")}
	if c := check(h); c.Status != CheckFail {
		t.Errorf("inspect error: %+v", c)
	}
	h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get
	if c := check(h); c.Status != CheckFail {
		t.Errorf("settings: %+v", c)
	}
}

func TestPathRoutesBecomeLabels(t *testing.T) {
	root := newRepo(t, "shop")
	m := webModel()
	m.Services["api"] = compose.Service{Ports: []compose.Port{{Target: 8000}}}
	m.XMutti = []byte(`{"main":"web","routes":{"api":{"path":"/api"}}}`)
	h := newHarness(t, m)
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=api")
	if len(cs) != 1 || cs[0].Labels[LabelRoutePaths] != "/api" {
		t.Fatalf("labels %+v", cs)
	}
	if cfg := routerConfig(t, h); !strings.Contains(cfg, "PathPrefix(`/api`)") {
		t.Fatalf("config %s", cfg)
	}
}

func TestUpWaitsForTheRouter(t *testing.T) {
	root := newRepo(t, "shop")

	t.Run("ready after a few tries", func(t *testing.T) {
		h := newHarness(t, webModel())
		tries := 0
		var seen []string
		h.app.Probe = func(_ context.Context, url, host string) (int, error) {
			seen = append(seen, host+" "+url)
			if tries++; tries < 3 {
				return http.StatusNotFound, nil
			}
			return http.StatusOK, nil
		}
		if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
			t.Fatal(err)
		}
		if tries != 3 || !strings.HasPrefix(seen[0], "mutti-ready.internal http://127.0.0.1:80/.well-known/mutti/") {
			t.Fatalf("tries=%d seen=%v", tries, seen)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.app.ReadyTimeout = 50 * time.Millisecond
		h.app.Probe = func(context.Context, string, string) (int, error) { return 0, errors.New("refused") }
		_, err := h.app.Up(ctx, root, UpOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeRouterFailed || !strings.Contains(e.Hint, "docker logs mutti-router") {
			t.Fatal(err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		h := newHarness(t, webModel())
		c, cancel := context.WithCancel(ctx)
		h.app.Probe = func(context.Context, string, string) (int, error) {
			cancel()
			return http.StatusNotFound, nil
		}
		if _, err := h.app.Up(c, root, UpOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("default timeout", func(t *testing.T) {
		h := newHarness(t, webModel())
		if err := h.app.waitReady(ctx, &RouterStatus{}, &Instance{Hostname: "x.localhost"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExhaustedAddressPools(t *testing.T) {
	root := newRepo(t, "shop")

	t.Run("ingress network", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.eng.FailOn = map[string]error{"CreateNetwork": engine.ErrAddressPoolsExhausted}
		_, err := h.app.Up(ctx, root, UpOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Code != CodePoolsExhausted || !strings.Contains(e.Hint, "default-address-pools") {
			t.Fatal(err)
		}
	})
	t.Run("compose", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.Exit = func(args []string) int { return map[string]int{"up": 1}[args[0]] }
		h.compose.Stderr = func([]string) string {
			return strings.Repeat("noise\n", 3000) + "Error response from daemon: " + engine.PoolsExhaustedMessage + "\n"
		}
		if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodePoolsExhausted {
			t.Fatal(err)
		}
		if !strings.Contains(h.stderr.String(), "noise") {
			t.Error("compose output must still reach the user")
		}
	})
	t.Run("doctor warns early", func(t *testing.T) {
		h := newHarness(t, webModel())
		for i := range 24 {
			h.eng.AddNetwork(engine.Network{Name: "n" + strconv.Itoa(i)})
		}
		r := h.app.Doctor(ctx, root)
		if !slices.ContainsFunc(r.Checks, func(c Check) bool { return c.Name == "networks" && c.Status == CheckWarn }) {
			t.Fatalf("checks %+v", r.Checks)
		}
	})
}

func TestRouterSettingsErrorsBeforeCompose(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	h.app.Host.Getenv = env{"MUTTI_HTTP_PORT": "x"}.get
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeInvalidConfig {
		t.Fatalf("up: %v", err)
	}
	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.prepare(ctx, inst, Selection{}); appErrCode(err) != CodeInvalidConfig {
		t.Fatalf("prepare: %v", err)
	}
}

func TestRoutersOnlyServeTheirOwnInstances(t *testing.T) {
	root := newRepo(t, "shop")
	wt := addWorktree(t, root, "shop-other")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.app.Host.Getenv = env{"MUTTI_ROUTER_NAME": "second"}.get
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	first := routerDetails(t, h)
	second, _, _ := h.eng.InspectContainer(ctx, "second")
	if _, ok := first.Networks["mutti-shop-other"]; ok {
		t.Error("first router joined the second router's instance")
	}
	if _, ok := second.Networks["mutti-shop"]; ok {
		t.Error("second router joined the first router's instance")
	}
}

func TestDropHostConflicts(t *testing.T) {
	main := router.Instance{Name: "shop", MainHost: "shop.localhost", Routes: []router.Route{
		{Service: "web", Hosts: []string{"shop.localhost"}},
		{Service: "admin", Hosts: []string{"admin.shop.localhost"}},
	}}
	worktree := router.Instance{Name: "shop-admin", MainHost: "admin.shop.localhost", Routes: []router.Route{
		{Service: "web", Hosts: []string{"admin.shop.localhost"}},
		{Service: "admin", Hosts: []string{"admin.admin.shop.localhost"}},
	}}
	want := []HostConflict{{Instance: "shop-admin", Host: "admin.shop.localhost", Owner: "shop", Service: "admin"}}
	for _, in := range [][]router.Instance{{main, worktree}, {worktree, main}} {
		kept, conflicts := dropHostConflicts(in)
		if len(kept) != 1 || kept[0].Name != "shop" || !slices.Equal(conflicts, want) {
			t.Fatalf("kept %v, conflicts %+v", kept, conflicts)
		}
	}

	// Equals: the instance name decides.
	a := router.Instance{Name: "a", MainHost: "x.localhost"}
	b := router.Instance{Name: "b", MainHost: "x.localhost"}
	if kept, conflicts := dropHostConflicts([]router.Instance{b, a}); len(kept) != 1 || kept[0].Name != "a" || conflicts[0].Instance != "b" {
		t.Fatalf("equals: %v %+v", kept, conflicts)
	}

	// A loser gives up all its hostnames, which may free one it took from another: x's service
	// takes y's main hostname, so y's claim on z.localhost no longer keeps z out.
	x := router.Instance{Name: "x", MainHost: "x.localhost", Routes: []router.Route{{Service: "s", Hosts: []string{"y.localhost"}}}}
	y := router.Instance{Name: "y", MainHost: "y.localhost", Routes: []router.Route{{Service: "s", Hosts: []string{"z.localhost"}}}}
	z := router.Instance{Name: "z", MainHost: "w.localhost", Routes: []router.Route{{Service: "s", Hosts: []string{"z.localhost"}}}}
	kept, conflicts := dropHostConflicts([]router.Instance{x, y, z})
	if names := instanceNames(kept); !slices.Equal(names, []string{"x", "z"}) || len(conflicts) != 1 || conflicts[0].Instance != "y" {
		t.Fatalf("cascade: kept %v, conflicts %+v", names, conflicts)
	}

	// Two that hold each other's main hostnames both go.
	p := router.Instance{Name: "p", MainHost: "p.localhost", Routes: []router.Route{{Service: "s", Hosts: []string{"q.localhost"}}}}
	q := router.Instance{Name: "q", MainHost: "q.localhost", Routes: []router.Route{{Service: "s", Hosts: []string{"p.localhost"}}}}
	if kept, conflicts := dropHostConflicts([]router.Instance{p, q}); len(kept) != 0 || len(conflicts) != 2 {
		t.Fatalf("mutual: kept %v, conflicts %+v", instanceNames(kept), conflicts)
	}
}

func instanceNames(in []router.Instance) []string {
	names := []string{}
	for _, i := range in {
		names = append(names, i.Name)
	}
	return names
}

func TestUpGivesServicesTheirHostnames(t *testing.T) {
	root := newRepo(t, "shop")
	m := &compose.Model{Services: map[string]compose.Service{
		"web":   {Ports: []compose.Port{{Target: 80}}},
		"admin": {Ports: []compose.Port{{Target: 8080}}},
	}}
	wt := addWorktree(t, root, "shop-admin")

	// The worktree first: it is routed until the service that owns its hostname comes.
	h := newHarness(t, m)
	if _, err := h.app.Up(ctx, wt, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "shop-admin is no longer routed: admin.shop.localhost belongs to shop now") {
		t.Errorf("stderr %q", h.stderr.String())
	}
	// From then on, the worktree's up fails and says why.
	_, err := h.app.Up(ctx, wt, UpOptions{})
	if appErrCode(err) != CodeHostnameTaken || !strings.Contains(err.Error(), "admin.shop.localhost belongs to shop (service admin)") {
		t.Fatal(err)
	}
}

func TestRouterOfAnotherStateIsLeftAlone(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	d, _, _ := h.eng.InspectContainer(ctx, "mutti-router")
	if d.Labels[LabelCA] == "" {
		t.Fatalf("router without its CA: %v", d.Labels)
	}

	// The same router seen from another state, say mutti in a container with its own dirs.
	other := newHarness(t, webModel())
	other.app.Engine, other.compose.Eng = h.eng, h.eng
	before := h.eng.Files(d.ID)
	_, err := other.app.Reconcile(ctx)
	if appErrCode(err) != CodeRouterForeign || !strings.Contains(err.Error(), "belongs to another mutti") {
		t.Fatal(err)
	}
	after, still, _ := h.eng.InspectContainer(ctx, "mutti-router")
	if !still || after.ID != d.ID || !maps.EqualFunc(before, h.eng.Files(d.ID), bytes.Equal) {
		t.Fatal("the foreign router was replaced or got another configuration")
	}

	// A router from before routers carried their CA is taken over.
	labels := maps.Clone(d.Labels)
	delete(labels, LabelCA)
	_ = h.eng.RemoveContainer(ctx, d.ID)
	id, _ := h.eng.CreateContainer(ctx, engine.ContainerSpec{Name: "mutti-router", Image: router.Image, Labels: labels})
	if _, err := h.app.Reconcile(ctx); err != nil {
		t.Fatalf("unlabelled router: %v", err)
	}
	if d, _, _ := h.eng.InspectContainer(ctx, "mutti-router"); d.ID != id {
		t.Error("an unlabelled router of the current version was replaced")
	}
}
