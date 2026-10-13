package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/compose"
)

func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func noEnv(string) string { return "" }

func TestDiscoverWalksUpToStop(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "compose.yml"), filepath.Join(root, "compose.override.yml"))
	start := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(start, root, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := Files{Dir: root, Paths: []string{filepath.Join(root, "compose.yml"), filepath.Join(root, "compose.override.yml")}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Error(diff)
	}
}

func TestDiscoverPrefersComposeYaml(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "docker-compose.yml"), filepath.Join(root, "compose.yaml"))
	got, err := Discover(root, root, noEnv)
	if err != nil || got.Paths[0] != filepath.Join(root, "compose.yaml") || len(got.Paths) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDiscoverStopsAtStop(t *testing.T) {
	outer := t.TempDir()
	touch(t, filepath.Join(outer, "compose.yml"))
	inner := filepath.Join(outer, "repo")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(inner, inner, noEnv); !errors.Is(err, ErrNoComposeFile) {
		t.Fatalf("must not look above stop, got %v", err)
	}
}

func TestDiscoverWithoutStopReachesRoot(t *testing.T) {
	if _, err := Discover(t.TempDir(), "", noEnv); !errors.Is(err, ErrNoComposeFile) {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverIgnoresDirectoriesNamedLikeComposeFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "compose.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root, root, noEnv); !errors.Is(err, ErrNoComposeFile) {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverFromComposeFileEnv(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "packages", "compose.yml"), filepath.Join(root, "ci.yml"))
	env := map[string]string{"COMPOSE_FILE": "packages/compose.yml;;" + filepath.Join(root, "ci.yml"), "COMPOSE_PATH_SEPARATOR": ";"}
	got, err := Discover(root, root, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := Files{Dir: filepath.Join(root, "packages"), Paths: []string{filepath.Join(root, "packages", "compose.yml"), filepath.Join(root, "ci.yml")}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Error(diff)
	}
}

func TestDiscoverFromComposeFileEnvErrors(t *testing.T) {
	root := t.TempDir()
	for _, list := range []string{"missing.yml", string(os.PathListSeparator)} {
		env := func(k string) string {
			if k == "COMPOSE_FILE" {
				return list
			}
			return ""
		}
		if _, err := Discover(root, root, env); err == nil {
			t.Errorf("COMPOSE_FILE=%q: want error", list)
		}
	}
}

func model(xmutti string, services ...string) *compose.Model {
	m := &compose.Model{Services: map[string]compose.Service{}, XMutti: []byte(xmutti)}
	for _, s := range services {
		m.Services[s] = compose.Service{}
	}
	return m
}

func TestParseConfigDefaults(t *testing.T) {
	for _, raw := range []string{"", "null"} {
		c, err := ParseConfig(model(raw, "web"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Exec.User != UserHost || c.Exec.Home != "/tmp" {
			t.Errorf("%q: defaults not applied: %+v", raw, c)
		}
	}
}

func TestParseConfigFull(t *testing.T) {
	raw := `{"name":"shop","main":"web","exec":{"user":"service","home":"/home/app"},
		"hooks":{"bootstrap":[{"service":"api","run":"composer install"}],"post-up":[{"service":"web","run":"true","user":"root"}],"pre-down":[{"service":"api","run":"bin/stop"}]},
		"overlays":{"e2e":["compose.e2e.yml"]}}`
	c, err := ParseConfig(model(raw, "web", "api"))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Name: "shop", Main: "web", Exec: Exec{User: "service", Home: "/home/app"},
		Hooks: Hooks{
			Bootstrap: []Hook{{Service: "api", Run: "composer install"}},
			PostUp:    []Hook{{Service: "web", Run: "true", User: "root"}},
			PreDown:   []Hook{{Service: "api", Run: "bin/stop"}},
		},
		Overlays: map[string][]string{"e2e": {"compose.e2e.yml"}},
	}
	if diff := cmp.Diff(want, c); diff != "" {
		t.Error(diff)
	}
}

func TestParseConfigRejects(t *testing.T) {
	tests := map[string]string{
		`{"mian":"web"}`:  `unknown field "mian"`,
		`{"name":"Shop"}`: `name "Shop" must be a DNS label`,
		`{"main":"nope"}`: `main: unknown service "nope" (services: web)`,
		`{"hooks":{"post-up":[{"service":"nope","run":"x"}]}}`:   `hooks.post-up[0].service: unknown service "nope"`,
		`{"hooks":{"bootstrap":[{"service":"web","run":"  "}]}}`: `hooks.bootstrap[0].run is empty`,
		`{"overlays":{"e2e":[]}}`:                                `overlays.e2e lists no files`,
		`{"routes":{"nope":{}}}`:                                 `routes.nope: unknown service "nope"`,
		`{"routes":{"web":{"host":"Bad Host"}}}`:                 `routes.web.host "Bad Host" must be a DNS label`,
		`{"routes":{"web":{"path":"api"}}}`:                      `routes.web.path "api" must start with /`,
		"{\"routes\":{\"web\":{\"path\":\"/a`) || Host(`x\"}}}":  "routes.web.path \"/a`) || Host(`x\" must start with /",
		`{"routes":{"web":{"path":"/a,b"}}}`:                     `routes.web.path "/a,b" must start with /`,
		`{"overlays":{"E2E":["x.yml"]}}`:                         `overlays: name "E2E" must be lower case`,
		`{"overlays":{"../x":["x.yml"]}}`:                        `overlays: name "../x" must be lower case`,
		`{"ports":{"nope":[5432]}}`:                              `ports.nope: unknown service "nope"`,
		`{"ports":{"web":[0]}}`:                                  `ports.web: 0 is not a port`,
		`{"worktree":{"copy":["/etc/passwd"]}}`:                  `worktree.copy: "/etc/passwd" must be a relative path`,
		`{"worktree":{"copy":["../x"]}}`:                         `worktree.copy: "../x" must be a relative path`,
		`{"worktree":{"copy":["[bad"]}}`:                         `worktree.copy: "[bad" must be a relative path`,
	}
	for raw, want := range tests {
		_, err := ParseConfig(model(raw, "web"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want error containing %q, got %v", raw, want, err)
		}
	}
}

func TestParseConfigUnescapesDollar(t *testing.T) {
	c, err := ParseConfig(model(`{"hooks":{"post-up":[{"service":"web","run":"echo $$HOME $$$$"}]}}`, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Hooks.PostUp[0].Run; got != "echo $HOME $$" {
		t.Fatalf("run = %q", got)
	}
}

func TestDiscoverFindsTheOnlySubdirOfStop(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "packages", "compose.yml"), filepath.Join(root, ".hidden", "compose.yml"), filepath.Join(root, "README"))
	got, err := Discover(root, root, noEnv)
	if err != nil || got.Dir != filepath.Join(root, "packages") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDiscoverRefusesSeveralSubdirs(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "a", "compose.yml"), filepath.Join(root, "b", "docker-compose.yml"))
	_, err := Discover(root, root, noEnv)
	if !errors.Is(err, ErrNoComposeFile) || !strings.Contains(err.Error(), "several subdirectories") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverUnreadableStop(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, err := Discover(root, root, noEnv); err == nil || errors.Is(err, ErrNoComposeFile) {
		t.Fatalf("want read error, got %v", err)
	}
}
